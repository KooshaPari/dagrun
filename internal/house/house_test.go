package house

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KooshaPari/dagrun/internal/plan"
	"github.com/KooshaPari/dagrun/internal/store"
)

const housePlan = `
version: 1
run: house
nodes:
  - id: "1"
    kind: DOC
    artifacts: [{path: a.md}]
  - id: "2"
    kind: DOC
    artifacts: [{path: b.md}]
`

func houseRig(t *testing.T) (string, *store.Store, *store.State) {
	t.Helper()
	root := t.TempDir()
	p, err := plan.Parse([]byte(housePlan))
	if err != nil {
		t.Fatal(err)
	}
	p.Root = root
	dir := filepath.Join(root, ".dagrun")
	st, err := store.Open(dir, p, []byte(housePlan), true)
	if err != nil {
		t.Fatal(err)
	}
	// store.Open writes state.json; run.yaml is written by the plan command.
	if err := os.WriteFile(filepath.Join(dir, "run.yaml"), []byte(housePlan), 0o644); err != nil {
		t.Fatal(err)
	}
	snap, err := st.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	return dir, st, snap
}

func TestDoctorOnAFreshRunIsClean(t *testing.T) {
	dir, st, snap := houseRig(t)
	rep := Doctor(dir, st, snap)
	if !rep.Healthy() {
		t.Fatalf("fresh run reported actionable findings: %+v", rep.Findings)
	}
}

func TestDoctorDetectsAnExpiredLease(t *testing.T) {
	dir, st, snap := houseRig(t)
	now := time.Now()
	if _, err := st.Claim("1", "worker", time.Second, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	snap, _ = st.Snapshot()
	rep := Doctor(dir, st, snap)
	found := false
	for _, f := range rep.Findings {
		if f.Code == "expired_lease" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expired lease not detected: %+v", rep.Findings)
	}
	if rep.Healthy() {
		t.Error("an expired lease should be actionable")
	}
}

func TestDoctorDetectsAMissingPlan(t *testing.T) {
	dir, _, _ := houseRig(t)
	if err := os.Remove(filepath.Join(dir, "run.yaml")); err != nil {
		t.Fatal(err)
	}
	rep := Doctor(dir, nil, nil)
	found := false
	for _, f := range rep.Findings {
		if f.Code == "missing_plan" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing plan not detected: %+v", rep.Findings)
	}
}

func TestGCDryRunChangesNothing(t *testing.T) {
	dir, _, snap := houseRig(t)
	logDir := filepath.Join(dir, "logs", "1")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(logDir, "old.log")
	if err := os.WriteFile(old, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-100 * time.Hour)
	_ = os.Chtimes(old, oldTime, oldTime)

	before := listing(t, dir)
	GC(dir, snap, time.Hour, true)
	after := listing(t, dir)
	if strings.Join(before, "\n") != strings.Join(after, "\n") {
		t.Fatalf("dry run changed the directory:\nbefore:\n%s\nafter:\n%s",
			strings.Join(before, "\n"), strings.Join(after, "\n"))
	}
}

func TestGCRemovesOldLogsButKeepsNewOnes(t *testing.T) {
	dir, _, snap := houseRig(t)
	logDir := filepath.Join(dir, "logs", "1")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(logDir, "old.log")
	newPath := filepath.Join(logDir, "new.log")
	for _, p := range []string{oldPath, newPath} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	oldTime := time.Now().Add(-100 * time.Hour)
	_ = os.Chtimes(oldPath, oldTime, oldTime)

	res := GC(dir, snap, time.Hour, false)
	if _, err := os.Stat(oldPath); err == nil {
		t.Error("the old log was not removed")
	}
	if _, err := os.Stat(newPath); err != nil {
		t.Error("the new log was removed")
	}
	if res.BytesFreed == 0 {
		t.Error("no bytes reported as freed")
	}
}

func TestGCNeverRemovesTheRunFiles(t *testing.T) {
	dir, _, snap := houseRig(t)
	GC(dir, snap, time.Nanosecond, false)
	for _, name := range []string{"run.yaml", "state.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("gc removed %s", name)
		}
	}
}

// gitRepo makes a small repository for the worktree tests.
func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("base"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "init")
	return dir
}

func TestGCKeepsAnUnmergedWorktree(t *testing.T) {
	repo := gitRepo(t)
	dir := filepath.Join(repo, ".dagrun")
	p, _ := plan.Parse([]byte(housePlan))
	p.Root = repo
	st, err := store.Open(dir, p, []byte(housePlan), true)
	if err != nil {
		t.Fatal(err)
	}
	// a worktree on a branch with an unmerged commit
	wt := filepath.Join(dir, "worktrees", "1")
	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("worktree", "add", "-b", "dagrun/test/1", wt, "HEAD")
	if err := os.WriteFile(filepath.Join(wt, "new.txt"), []byte("work"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("-C", wt, "add", ".")
	run("-C", wt, "commit", "-m", "work")

	claim, err := st.Claim("1", "w", time.Minute, time.Now())
	if err != nil || !claim.Won {
		t.Fatalf("claim: %v %v", err, claim)
	}
	if err := st.Annotate("1", wt, "dagrun/test/1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Finish("1", "w", claim.Epoch, plan.StatusDone, nil, 0, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	snap, _ := st.Snapshot()

	res := GC(dir, snap, time.Hour, false)
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("an unmerged worktree was removed: %v", err)
	}
	kept := false
	for _, r := range res.Reasons {
		if strings.Contains(r, "not merged") {
			kept = true
		}
	}
	if !kept {
		t.Errorf("the keep reason was not reported: %+v", res.Reasons)
	}
}

func listing(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		out = append(out, path)
		return nil
	})
	return out
}
