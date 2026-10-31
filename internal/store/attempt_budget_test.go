package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/KooshaPari/dagrun/internal/plan"
	"github.com/KooshaPari/dagrun/internal/verify"
)

// The attempt budget is decided in TWO places: internal/run decides whether a
// failed node goes back to PENDING, and this package decides whether a node
// may be CLAIMED.
//
// When they disagreed, the disagreement was invisible and the run failed
// anyway. The run granted a throttled node six attempts; Claim enforced the
// plan's raw two and failed the node on the very next tick, so the extra
// attempts never happened. A measured run showed it as:
//
//	n1 status=failed class=rate_limited attempts=1 last_error="exhausted 1 attempt(s)"
//
// These tests are the regression guard: Claim must use the SAME number the
// retry policy granted.

const budgetPlan = `
version: 1
run: budget
nodes:
  - id: "1"
    kind: IMPL
    max_attempts: 2
    artifacts: [{path: a}]
`

func budgetStore(t *testing.T) *Store {
	t.Helper()
	root := t.TempDir()
	p, err := plan.Parse([]byte(budgetPlan))
	if err != nil {
		t.Fatal(err)
	}
	p.Root = root
	st, err := Open(filepath.Join(root, ".dagrun"), p, []byte(budgetPlan), true)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// markThrottled records the failure class the way the run package does.
func markThrottled(t *testing.T, st *Store, node string, attempts int) {
	t.Helper()
	err := st.With(func(s *State) error {
		ns := s.Nodes[node]
		if ns == nil {
			t.Fatalf("node %s not in state", node)
		}
		ns.FailureClass = plan.ClassRateLimited
		ns.Attempts = attempts
		ns.Status = plan.StatusPending
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A throttled node must be claimable past the plan's raw budget. Before the
// fix this failed the node instead.
func TestClaimHonoursTheRateLimitAttemptFloor(t *testing.T) {
	st := budgetStore(t)
	markThrottled(t, st, "1", 2)

	claim, err := st.Claim("1", "worker", time.Minute, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !claim.Won {
		t.Fatalf("claim lost for a throttled node at attempt 2 of a 2-attempt plan: %q", claim.Reason)
	}
	snap, _ := st.Snapshot()
	if got := snap.Nodes["1"].Attempts; got != 3 {
		t.Fatalf("attempts = %d, want 3 (the floor must allow attempts past the plan's budget)", got)
	}
}

// The floor is bounded: a throttled node must still give up eventually, or a
// permanently exhausted quota would spin forever.
func TestClaimStopsAtTheRateLimitAttemptCeiling(t *testing.T) {
	st := budgetStore(t)
	markThrottled(t, st, "1", plan.MinRateLimitAttempts)

	claim, err := st.Claim("1", "worker", time.Minute, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if claim.Won {
		t.Fatal("claim succeeded past the rate-limit ceiling; a dead quota would spin forever")
	}
	snap, _ := st.Snapshot()
	if snap.Nodes["1"].Status != plan.StatusFailed {
		t.Fatalf("status = %q, want failed", snap.Nodes["1"].Status)
	}
	if got := snap.Nodes["1"].LastError; got != "exhausted 6 attempt(s)" {
		t.Fatalf("last error = %q, want it to report the EFFECTIVE budget, not the plan's", got)
	}
}

// Every other class keeps the plan's budget exactly. The floor is for a busy
// provider, not for an agent that keeps producing the wrong thing.
func TestClaimIgnoresTheFloorForOtherClasses(t *testing.T) {
	st := budgetStore(t)
	err := st.With(func(s *State) error {
		ns := s.Nodes["1"]
		ns.FailureClass = plan.ClassContractMiss
		ns.Attempts = 2
		ns.Status = plan.StatusPending
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := st.Claim("1", "worker", time.Minute, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if claim.Won {
		t.Fatal("claim succeeded past the plan's budget for a contract miss")
	}
	snap, _ := st.Snapshot()
	if got := snap.Nodes["1"].LastError; got != "exhausted 2 attempt(s)" {
		t.Fatalf("last error = %q, want the plan's own budget of 2", got)
	}
}

// A node with no recorded failure class keeps the plan's budget.
func TestClaimWithoutAFailureClassUsesThePlanBudget(t *testing.T) {
	st := budgetStore(t)
	err := st.With(func(s *State) error {
		ns := s.Nodes["1"]
		ns.Attempts = 2
		ns.Status = plan.StatusPending
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := st.Claim("1", "worker", time.Minute, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if claim.Won {
		t.Fatal("claim succeeded past the plan's budget with no failure class")
	}
}

// The report is irrelevant to the budget; the class is what matters.
func TestClaimIgnoresTheReport(t *testing.T) {
	st := budgetStore(t)
	markThrottled(t, st, "1", 2)
	// A failing report must not change the budget.
	_, err := st.Finish("1", "worker", 0, plan.StatusPending, &verify.Report{OK: false}, 1, "x", time.Now())
	_ = err
	claim, err := st.Claim("1", "worker", time.Minute, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !claim.Won {
		t.Fatalf("a failing report changed the attempt budget: %q", claim.Reason)
	}
}
