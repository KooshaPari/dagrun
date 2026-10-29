package workspace

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/KooshaPari/dagrun/internal/plan"
	"github.com/KooshaPari/dagrun/internal/run"
)

// The cost ceiling must be a property of the RUN, not of each repo's
// executor. Before the shared budget existed, every repo got the full
// MaxCostUSD, so a two-repo workspace with a 10 USD cap could spend 20.
//
// These tests exercise the shared budget directly rather than through a real
// spend, because a real spend needs real token accounting and a real model.
// The wiring is what matters: that the workspace hands ONE budget to every
// repo's executor.

// perMTok is the rate used throughout this file: one dollar per million
// input tokens, so a million tokens cost exactly one dollar and the
// arithmetic stays readable.
var perMTok = plan.Rate{InputPerMillion: 1}

func TestSharedBudgetIsOneCeilingNotOnePerRepo(t *testing.T) {
	b := run.NewSharedBudget(10.0)

	// Two "repos" charge the same shared budget.
	b.Add(plan.Spend{InputTokens: 4_000_000}, perMTok) // $4
	b.Add(plan.Spend{InputTokens: 4_000_000}, perMTok) // $4 more, total $8

	spent, max := b.Spent()
	if max != 10.0 {
		t.Fatalf("max = %v, want 10", max)
	}
	if spent != 8 {
		t.Fatalf("spent = %v, want 8", spent)
	}
	if b.Exceeded() {
		t.Fatal("budget reported exceeded at $8 of $10")
	}

	// The third repo pushes the SAME shared budget over the line. If each repo
	// had its own budget, this charge would have been against a fresh $10 and
	// the total would have read $4 instead of $12.
	b.Add(plan.Spend{InputTokens: 4_000_000}, perMTok) // $12 total
	spent, _ = b.Spent()
	if spent != 12 {
		t.Fatalf("spent = %v, want 12 (the charges must accumulate on one budget)", spent)
	}
	if !b.Exceeded() {
		t.Fatal("budget not exceeded at $12 of $10")
	}
}

func TestSharedBudgetZeroCeilingMeansNoLimit(t *testing.T) {
	b := run.NewSharedBudget(0)
	b.Add(plan.Spend{InputTokens: 100_000_000}, perMTok)
	if b.Exceeded() {
		t.Fatal("a zero ceiling must mean no limit, not exceeded")
	}
	spent, max := b.Spent()
	if max != 0 {
		t.Fatalf("max = %v, want 0", max)
	}
	if spent != 100 {
		t.Fatalf("spent = %v, want 100", spent)
	}
}

func TestSharedBudgetNegativeCeilingIsClampedToNoLimit(t *testing.T) {
	b := run.NewSharedBudget(-5)
	if b.Exceeded() {
		t.Fatal("a negative ceiling must clamp to no limit, not exceeded")
	}
	_, max := b.Spent()
	if max != 0 {
		t.Fatalf("max = %v, want 0", max)
	}
}

func TestSharedBudgetIsConcurrencySafe(t *testing.T) {
	b := run.NewSharedBudget(1_000_000)
	const n = 64
	done := make(chan struct{})
	for i := 0; i < n; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 50; j++ {
				b.Add(plan.Spend{InputTokens: 1000}, perMTok)
				_ = b.Exceeded()
				_, _ = b.Spent()
			}
		}()
	}
	for i := 0; i < n; i++ {
		<-done
	}
	// 64 goroutines x 50 charges x 1000 tokens at $1/M = 64 * 50 * 0.001 = $3.2.
	// The comparison is approximate: floating point addition of 3200 terms of
	// 0.001 does not land exactly on 3.2, and it must not.
	spent, _ := b.Spent()
	if math.Abs(spent-3.2) > 1e-9 {
		t.Fatalf("spent = %v, want ~3.2 (a lost update would show up here)", spent)
	}
}

// TestWorkspaceSpendIsAggregatedAcrossRepos is the test that matters. Two
// repos each report usage that is cheap on its own but expensive together.
// With a per-repo budget, a $1 ceiling would let both repos spend $0.60. With
// a shared budget, the second repo's spend pushes the run over $1 and no
// further nodes are claimed.
func TestWorkspaceSpendIsAggregatedAcrossRepos(t *testing.T) {
	root := t.TempDir()
	for _, repo := range []string{"api", "web"} {
		full := filepath.Join(root, repo)
		if err := os.MkdirAll(full, 0o755); err != nil {
			t.Fatal(err)
		}
		// Each node reports 600,000 input tokens, which is $0.60 at $1/M.
		// The node also writes its declared artifact so it verifies.
		body := `version: 1
run: r
nodes:
  - id: n
    kind: CODE
    title: t
    estimate: 1m
    timeout: 5m
    model: testmodel
    artifacts:
      - path: out.txt
        exists: true
`
		if err := os.WriteFile(filepath.Join(full, "plan.yaml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ws := `version: 1
name: budgeted
repos:
  - path: api
    plan: plan.yaml
  - path: web
    plan: plan.yaml
`
	path := filepath.Join(root, "dagrun.workspace.yaml")
	if err := os.WriteFile(path, []byte(ws), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	// The usage object is written to a FILE and cat'ed, because the exec
	// template splitter strips double quotes: a template that printed the JSON
	// inline would emit {input_tokens:600000} with the quotes gone, which is
	// not JSON and would parse as no usage at all.
	const usageLine = `{"model":"testmodel","input_tokens":600000,"output_tokens":0}`
	for _, repo := range []string{"api", "web"} {
		if err := os.WriteFile(filepath.Join(root, repo, "usage.json"), []byte(usageLine+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sum, err := w.Run(context.Background(), RunOptions{
		Workers: 1,
		Out:     os.Stderr,
		// $1 ceiling. One node at $0.60 fits; two nodes at $0.60 each do not.
		MaxCostUSD:   1.0,
		Rate:         perMTok,
		ExecTemplate: `sh -c "echo ok > out.txt && cat usage.json"`,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	// The summary reports the AGGREGATE across both repos.
	//
	// The number is $1.20, not $0.60, and that is the whole point: with a
	// per-repo budget each repo would report only its own $0.60, and the
	// ceiling would never be reached at all. Here the second repo's charge
	// lands on the SAME budget and crosses $1.
	//
	// $1.20 > $1.00 is expected and correct: the ceiling stops NEW nodes from
	// being claimed, it does not un-spend a node that was already in flight.
	// A run reports the truth it measured rather than pretending it stayed
	// under the line.
	if sum.CostLimitUSD != 1.0 {
		t.Fatalf("cost limit = %v, want 1", sum.CostLimitUSD)
	}
	if sum.CostUSD < 1.2 {
		t.Fatalf("aggregate spend = %v, want >= 1.2 (the charges did not aggregate onto one budget)", sum.CostUSD)
	}
	// Two nodes at $0.60 each: the first fits under $1, so the second is
	// claimed and pushes the run to $1.20.
	if sum.Done != 2 {
		t.Fatalf("done = %d, want 2", sum.Done)
	}
}
