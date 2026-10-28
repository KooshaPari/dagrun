package run

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KooshaPari/dagrun/internal/plan"
	"github.com/KooshaPari/dagrun/internal/store"
	"github.com/KooshaPari/dagrun/internal/trace"
)

const worktreePlan = `
version: 1
run: wt
max_parallel: 2
nodes:
  - id: "1"
    kind: IMPL
    title: isolated edit
    isolation: worktree
    artifacts:
      - path: made.txt
        min_bytes: 2
  - id: "2"
    kind: IMPL
    title: shared edit
    optional: true
`

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init")
	gitRun(t, dir, "config", "user.email", "t@example.com")
	gitRun(t, dir, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("base"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-m", "init")
	return dir
}

func TestWorktreeIsolationKeepsEditsOutOfTheSharedTree(t *testing.T) {
	root := gitRepo(t)
	p, err := plan.Parse([]byte(worktreePlan))
	if err != nil {
		t.Fatal(err)
	}
	p.Root = root
	if issues := p.Validate(); len(issues) > 0 {
		t.Fatalf("plan invalid: %v", issues)
	}
	dir := filepath.Join(root, ".dagrun")
	st, err := store.Open(dir, p, []byte(worktreePlan), true)
	if err != nil {
		t.Fatal(err)
	}

	h := rig{st: st, tr: trace.New(filepath.Join(dir, "trace"))}
	// Node 2 is optional with no contract, so it is skipped and never runs. If
	// it ran in the shared checkout with the same command it would write
	// made.txt there too - which is exactly the collision worktree isolation
	// exists to prevent.
	sum := h.loop(t, `sh -c "printf hi > made.txt"`, 2)
	if sum.Done < 1 {
		t.Fatalf("expected the isolated node to complete, got %+v", sum)
	}

	snap, err := st.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	ns := snap.Nodes["1"]
	if ns == nil || ns.Status != plan.StatusDone {
		t.Fatalf("node 1 = %+v", ns)
	}
	if ns.Branch == "" || ns.Worktree == "" {
		t.Fatalf("worktree/branch not recorded: %+v", ns)
	}

	// the edit landed in the isolation checkout...
	if _, err := os.Stat(filepath.Join(ns.Worktree, "made.txt")); err != nil {
		t.Fatalf("artifact missing from the worktree: %v", err)
	}
	// ...and not in the shared tree
	if _, err := os.Stat(filepath.Join(root, "made.txt")); err == nil {
		t.Fatal("isolated node wrote into the shared checkout")
	}
	// the branch exists and is not the current branch
	if out := gitRun(t, root, "branch", "--list", ns.Branch); out == "" {
		t.Fatalf("branch %s was not created", ns.Branch)
	}
	if cur := gitRun(t, root, "rev-parse", "--abbrev-ref", "HEAD"); cur == ns.Branch {
		t.Fatal("the main checkout was switched onto the node branch")
	}
}

func TestWorktreeIsReusedNotReset(t *testing.T) {
	root := gitRepo(t)
	p, err := plan.Parse([]byte(worktreePlan))
	if err != nil {
		t.Fatal(err)
	}
	p.Root = root
	dir := filepath.Join(root, ".dagrun")
	if _, err := store.Open(dir, p, []byte(worktreePlan), true); err != nil {
		t.Fatal(err)
	}
	n := p.ByID("1")
	wt1, err := ensureWorktree(root, dir, p.Run, n)
	if err != nil {
		t.Fatal(err)
	}
	// leave a marker inside the worktree, then ask for it again
	if err := os.WriteFile(filepath.Join(wt1.Path, "marker"), []byte("kept"), 0o644); err != nil {
		t.Fatal(err)
	}
	wt2, err := ensureWorktree(root, dir, p.Run, n)
	if err != nil {
		t.Fatal(err)
	}
	if wt1.Path != wt2.Path || wt1.Branch != wt2.Branch {
		t.Fatalf("second call changed the worktree: %+v vs %+v", wt1, wt2)
	}
	if _, err := os.Stat(filepath.Join(wt2.Path, "marker")); err != nil {
		t.Fatal("reusing a worktree discarded its contents")
	}
}

func TestIsolationValidation(t *testing.T) {
	bad := `
version: 1
nodes:
  - id: "1"
    kind: IMPL
    isolation: container
    artifacts: [{path: a}]
`
	p, _ := plan.Parse([]byte(bad))
	found := false
	for _, is := range p.Validate() {
		if is.Code == "bad_isolation" {
			found = true
		}
	}
	if !found {
		t.Fatalf("unknown isolation accepted: %v", p.Validate())
	}

	conflict := `
version: 1
nodes:
  - id: "1"
    kind: IMPL
    isolation: worktree
    workdir: sub
    artifacts: [{path: a}]
`
	p2, _ := plan.Parse([]byte(conflict))
	found = false
	for _, is := range p2.Validate() {
		if is.Code == "isolation_conflict" {
			found = true
		}
	}
	if !found {
		t.Fatalf("worktree+workdir accepted: %v", p2.Validate())
	}
}
