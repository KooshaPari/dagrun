package store

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KooshaPari/dagrun/internal/plan"
	"github.com/KooshaPari/dagrun/internal/verify"
)

const testPlan = `
version: 1
run: storetest
max_parallel: 8
nodes:
  - id: "1"
    kind: IMPL
    role: impl
    artifacts: [{path: a}]
  - id: "2"
    kind: IMPL
    role: impl
    deps: ["1"]
    artifacts: [{path: b}]
  - id: "3"
    kind: IMPL
    role: docs
    artifacts: [{path: c}]
`

func withMaxParallel(src string, n int) string {
	lines := strings.Split(src, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "max_parallel:") {
			lines[i] = fmt.Sprintf("max_parallel: %d", n)
		}
	}
	return strings.Join(lines, "\n")
}

func newStore(t *testing.T, root string, maxParallel int) *Store {
	t.Helper()
	yaml := testPlan
	if maxParallel > 0 {
		yaml = withMaxParallel(testPlan, maxParallel)
	}
	p, err := plan.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	p.Root = root
	st, err := Open(filepath.Join(root, ".dagrun"), p, []byte(yaml), true)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func okReport() *verify.Report { return &verify.Report{OK: true} }

func TestClaimHasExactlyOneWinnerInProcess(t *testing.T) {
	st := newStore(t, t.TempDir(), 0)
	var mu sync.Mutex
	wins := 0
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			c, err := st.Claim("1", fmt.Sprintf("w%d", i), time.Minute, time.Now())
			if err != nil {
				t.Errorf("claim: %v", err)
				return
			}
			if c.Won {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}(i)
	}
	close(start)
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d winners, want exactly 1", wins)
	}
}

// TestClaimHasExactlyOneWinnerAcrossProcesses is the test that matters: the
// lock is cross-process, so the guarantee must hold across real OS processes.
func TestClaimHasExactlyOneWinnerAcrossProcesses(t *testing.T) {
	root := t.TempDir()
	newStore(t, root, 0)

	const procs = 8
	results := make(chan int, procs)
	var wg sync.WaitGroup
	for i := 0; i < procs; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=TestClaimHelperProcess")
			cmd.Env = append(os.Environ(),
				"DAGRUN_HELPER=claim",
				"DAGRUN_ROOT="+root,
				"DAGRUN_WORKER="+fmt.Sprintf("p%d", i),
			)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Errorf("helper %d: %v\n%s", i, err, out)
				results <- 0
				return
			}
			var won int
			fmt.Sscanf(string(out), "WON=%d", &won)
			results <- won
		}(i)
	}
	wg.Wait()
	close(results)
	total := 0
	for r := range results {
		total += r
	}
	if total != 1 {
		t.Fatalf("%d process-level winners, want exactly 1", total)
	}
}

func TestClaimHelperProcess(t *testing.T) {
	if os.Getenv("DAGRUN_HELPER") != "claim" {
		t.Skip("helper process")
	}
	root := os.Getenv("DAGRUN_ROOT")
	raw, err := os.ReadFile(filepath.Join(root, ".dagrun", "run.yaml"))
	if err != nil {
		raw = []byte(testPlan)
	}
	p, err := plan.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	p.Root = root
	st, err := Open(filepath.Join(root, ".dagrun"), p, raw, false)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	c, err := st.Claim("1", os.Getenv("DAGRUN_WORKER"), time.Minute, time.Now())
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	won := 0
	if c.Won {
		won = 1
	}
	fmt.Printf("WON=%d\n", won)
}

func TestClaimRespectsDependencies(t *testing.T) {
	st := newStore(t, t.TempDir(), 0)
	c, err := st.Claim("2", "w", time.Minute, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if c.Won {
		t.Fatal("claimed a node whose dependency is not done")
	}
	if err := st.Force("1", plan.StatusDone, "", okReport(), time.Now()); err != nil {
		t.Fatal(err)
	}
	c, err = st.Claim("2", "w", time.Minute, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !c.Won {
		t.Fatalf("expected claim after dep done, got %q", c.Reason)
	}
}

func TestLeaseExpiryReclaimsAndFences(t *testing.T) {
	st := newStore(t, t.TempDir(), 0)
	now := time.Now()
	c1, err := st.Claim("1", "a", time.Second, now)
	if err != nil || !c1.Won {
		t.Fatalf("first claim: %v %v", err, c1)
	}
	if c, _ := st.Claim("1", "b", time.Second, now); c.Won {
		t.Fatal("stole a live lease")
	}
	later := now.Add(2 * time.Second)
	reaped, err := st.ResetExpired(later)
	if err != nil {
		t.Fatal(err)
	}
	if len(reaped) != 1 {
		t.Fatalf("reaped %v, want 1", reaped)
	}
	c2, err := st.Claim("1", "b", time.Second, later)
	if err != nil || !c2.Won {
		t.Fatalf("reclaim failed: %v %v", err, c2)
	}
	if c2.Epoch <= c1.Epoch {
		t.Fatalf("epoch did not advance: %d -> %d", c1.Epoch, c2.Epoch)
	}
	ok, err := st.Finish("1", "a", c1.Epoch, plan.StatusDone, nil, 0, "", later)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("stale epoch write was accepted")
	}
	ok, err = st.Finish("1", "b", c2.Epoch, plan.StatusDone, nil, 0, "", later)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("current owner write was rejected")
	}
}

func TestGlobalCapBlocksASecondLease(t *testing.T) {
	st := newStore(t, t.TempDir(), 1)
	now := time.Now()
	if c, _ := st.Claim("1", "a", time.Minute, now); !c.Won {
		t.Fatalf("first claim should win: %q", c.Reason)
	}
	c, err := st.Claim("3", "b", time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	if c.Won {
		t.Fatal("exceeded max_parallel: a second lease was granted")
	}
	if !strings.Contains(c.Reason, "max_parallel") {
		t.Fatalf("reason = %q, want a max_parallel message", c.Reason)
	}
}

func TestRoleCapBlocksASecondLease(t *testing.T) {
	yaml := strings.Replace(testPlan, "max_parallel: 8", "max_parallel: 8\nrole_caps:\n  impl: 1", 1)
	p, err := plan.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	p.Root = root
	st, err := Open(filepath.Join(root, ".dagrun"), p, []byte(yaml), true)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if c, _ := st.Claim("1", "a", time.Minute, now); !c.Won {
		t.Fatal("first impl claim should win")
	}
	c, err := st.Claim("3", "b", time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Won {
		t.Fatalf("docs role should not be capped: %q", c.Reason)
	}
}

func TestHeartbeatRequiresOwnership(t *testing.T) {
	st := newStore(t, t.TempDir(), 0)
	now := time.Now()
	c, _ := st.Claim("1", "a", time.Minute, now)
	if !c.Won {
		t.Fatal("claim failed")
	}
	if ok, err := st.Heartbeat("1", "a", c.Epoch, time.Minute, now); err != nil || !ok {
		t.Fatalf("heartbeat by owner failed: %v %v", err, ok)
	}
	ok, err := st.Heartbeat("1", "b", c.Epoch, time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("heartbeat accepted from a non-owner")
	}
}

func TestAttemptBudgetExhaustsToFailed(t *testing.T) {
	st := newStore(t, t.TempDir(), 0)
	now := time.Now()
	for i := 0; i < 2; i++ {
		c, err := st.Claim("1", "a", time.Minute, now.Add(time.Duration(i)*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if !c.Won {
			t.Fatalf("attempt %d failed: %q", i, c.Reason)
		}
		if _, err := st.Finish("1", "a", c.Epoch, plan.StatusPending, nil, 1, "failed", now); err != nil {
			t.Fatal(err)
		}
	}
	c, err := st.Claim("1", "a", time.Minute, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if c.Won {
		t.Fatal("claimed after exhausting attempts")
	}
	snap, _ := st.Snapshot()
	if snap.Nodes["1"].Status != plan.StatusFailed {
		t.Fatalf("status = %q, want failed", snap.Nodes["1"].Status)
	}
}

func TestPlanChangeIsRejected(t *testing.T) {
	root := t.TempDir()
	newStore(t, root, 0)
	other := withMaxParallel(testPlan, 2)
	p, err := plan.Parse([]byte(other))
	if err != nil {
		t.Fatal(err)
	}
	p.Root = root
	if _, err := Open(filepath.Join(root, ".dagrun"), p, []byte(other), false); err == nil {
		t.Fatal("a changed plan must not silently reuse an existing run")
	}
	if _, err := Open(filepath.Join(root, ".dagrun"), p, []byte(other), true); err != nil {
		t.Fatalf("--reset should be accepted: %v", err)
	}
}
