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

// scopePlan declares a node that is isolated and claims a file scope.
const scopePlan = `
version: 1
run: scope
max_parallel: 1
nodes:
  - id: "1"
    kind: IMPL
    title: edit src
    isolation: worktree
    file_scope: ["src/*.go"]
    artifacts:
      - path: src/a.go
        min_bytes: 3
`

func scopeGitRepo(t *testing.T) string {
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
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "a.go"), []byte("package src\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "base")
	return dir
}

// scopeRig runs a plan in a git repository with the given edit script.
func scopeRig(t *testing.T, script string) (rig, string) {
	t.Helper()
	root := scopeGitRepo(t)
	p, err := plan.Parse([]byte(scopePlan))
	if err != nil {
		t.Fatal(err)
	}
	p.Root = root
	dir := filepath.Join(root, ".dagrun")
	st, err := store.Open(dir, p, []byte(scopePlan), true)
	if err != nil {
		t.Fatal(err)
	}
	h := rig{st: st, tr: trace.New(filepath.Join(dir, "trace"))}
	h.loop(t, script, 1)
	return h, root
}

func TestIsolatedNodeThatTouchesItsScopePasses(t *testing.T) {
	h, _ := scopeRig(t, `sh -c "printf 'package src // changed' > src/a.go"`)
	snap, err := h.st.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	ns := snap.Nodes["1"]
	if ns == nil || ns.Status != plan.StatusDone {
		t.Fatalf("node = %+v, want done (last error: %v)", ns, ns.LastError)
	}
}

func TestIsolatedNodeThatTouchesNothingFails(t *testing.T) {
	// The node claims to edit src/*.go but writes its artifact elsewhere and
	// changes no tracked file.
	h, _ := scopeRig(t, `sh -c "mkdir -p artifacts && printf 'x' > artifacts/x.txt"`)
	snap, err := h.st.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	ns := snap.Nodes["1"]
	if ns == nil || ns.Status == plan.StatusDone {
		t.Fatalf("node = %+v, want failed (it changed nothing)", ns)
	}
	if !strings.Contains(ns.LastError, "scope") {
		t.Errorf("error should name the scope problem, got %q", ns.LastError)
	}
}
