package run

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KooshaPari/dagrun/internal/plan"
	"github.com/KooshaPari/dagrun/internal/store"
	"github.com/KooshaPari/dagrun/internal/trace"
)

const runPlan = `
version: 1
run: endtoend
intent: prove the loop
max_parallel: 4
nodes:
  - id: "1.1"
    kind: INTENT
    title: write intent
    artifacts:
      - path: artifacts/INTENT.md
        min_bytes: 10
  - id: "1.2"
    kind: IMPL
    title: write code
    deps: ["1.1"]
    artifacts:
      - path: artifacts/code.go
        must_contain: ["func main"]
  - id: "1.3"
    kind: GATE
    title: gate on the code file
    deps: ["1.2"]
    check: "grep -q 'func main' artifacts/code.go"
`

// goodTemplate writes both artifacts. It contains no backslashes because the
// template splitter treats backslash as an escape character.
const goodTemplate = `sh -c "mkdir -p artifacts && printf '0123456789abcdef' > artifacts/INTENT.md && printf 'package main func main() {}' > artifacts/code.go"`

type rig struct {
	st *store.Store
	tr *trace.Writer
}

func setup(t *testing.T) rig {
	t.Helper()
	root := t.TempDir()
	p, err := plan.Parse([]byte(runPlan))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	p.Root = root
	if issues := p.Validate(); len(issues) > 0 {
		t.Fatalf("plan invalid: %v", issues)
	}
	dir := filepath.Join(root, ".dagrun")
	st, err := store.Open(dir, p, []byte(runPlan), true)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	return rig{st: st, tr: trace.New(filepath.Join(dir, "trace"))}
}

func (h rig) loop(t *testing.T, tmpl string, workers int) Summary {
	t.Helper()
	var out bytes.Buffer
	ex := New(h.st, h.tr, Options{
		Workers:      workers,
		Owner:        "test",
		Lease:        30 * time.Second,
		ExecTemplate: tmpl,
		Out:          &out,
	})
	sum, err := ex.Loop(context.Background())
	if err != nil {
		t.Fatalf("loop: %v (out: %s)", err, out.String())
	}
	return sum
}

func TestLoopCompletesEveryNode(t *testing.T) {
	h := setup(t)
	sum := h.loop(t, goodTemplate, 2)
	if sum.Done != 3 || sum.Failed != 0 || sum.Pending != 0 {
		t.Fatalf("summary = %+v, want 3 done", sum)
	}
	root := h.st.Root()
	for _, f := range []string{"artifacts/INTENT.md", "artifacts/code.go"} {
		if _, err := os.Stat(filepath.Join(root, f)); err != nil {
			t.Errorf("missing artifact %s: %v", f, err)
		}
	}
	events, err := trace.Merge(filepath.Join(h.st.Dir(), "trace"))
	if err != nil {
		t.Fatal(err)
	}
	var claims, settled int
	for _, e := range events {
		switch e.Event {
		case "node.claim":
			claims++
		case "node.done", "node.adopt":
			settled++
		}
	}
	// Adoption is legitimate: a node whose artifacts already verify is done,
	// whether it produced them itself or a sibling did. What must hold is that
	// at least one node actually ran and every node ended up settled.
	if claims < 1 {
		t.Errorf("claims = %d, want at least 1", claims)
	}
	if settled < 3 {
		t.Errorf("settled events = %d, want 3 (done or adopted)", settled)
	}
}

// TestSecondRunAdoptsInsteadOfRedoing is the duplicate-work guard: a rerun must
// not re-execute nodes whose artifacts still verify. The template is replaced
// with one that would fail, so any re-execution is visible as a failure.
func TestSecondRunAdoptsInsteadOfRedoing(t *testing.T) {
	h := setup(t)
	h.loop(t, goodTemplate, 2)
	sum := h.loop(t, `sh -c "exit 1"`, 2)
	if sum.Done != 3 || sum.Failed != 0 {
		t.Fatalf("summary = %+v, want 3 adopted and no failures", sum)
	}
}

func TestFailingNodeIsRetriedThenFailed(t *testing.T) {
	root := t.TempDir()
	yaml := `
version: 1
run: failing
max_parallel: 2
defaults:
  max_attempts: 2
nodes:
  - id: "1"
    kind: IMPL
    artifacts: [{path: artifacts/never.md}]
`
	p, err := plan.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	p.Root = root
	dir := filepath.Join(root, ".dagrun")
	st, err := store.Open(dir, p, []byte(yaml), true)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	ex := New(st, trace.New(filepath.Join(dir, "trace")), Options{
		Workers: 1, Owner: "test", Lease: time.Minute,
		ExecTemplate: `sh -c "exit 1"`, Out: &out,
	})
	sum, err := ex.Loop(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sum.Failed != 1 {
		t.Fatalf("summary = %+v, want 1 failed (out: %s)", sum, out.String())
	}
	snap, _ := st.Snapshot()
	if got := snap.Nodes["1"].Attempts; got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
}

func TestDryRunExecutesNothing(t *testing.T) {
	h := setup(t)
	var out bytes.Buffer
	ex := New(h.st, h.tr, Options{Workers: 1, Owner: "test", Lease: time.Minute, DryRun: true, Out: &out})
	sum, err := ex.Loop(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sum.Done != 0 {
		t.Fatalf("dry run completed %d nodes", sum.Done)
	}
	if !strings.Contains(out.String(), "would run") {
		t.Fatalf("dry run said nothing: %s", out.String())
	}
	if _, err := os.Stat(filepath.Join(h.st.Root(), "artifacts")); err == nil {
		t.Fatal("dry run created artifacts")
	}
}

func TestChildEnvScrubsParentSession(t *testing.T) {
	t.Setenv("JCODE_RUNTIME_PROVIDER", "openai")
	t.Setenv("JCODE_SOCKET", "/tmp/x.sock")
	t.Setenv("JCODE_TUI_INHERITED_THEME", "dark")
	t.Setenv("KEEP_ME", "yes")
	env := childEnv()
	seen := map[string]string{}
	for _, kv := range env {
		if i := strings.IndexByte(kv, '='); i > 0 {
			seen[kv[:i]] = kv[i+1:]
		}
	}
	for _, k := range []string{"JCODE_RUNTIME_PROVIDER", "JCODE_SOCKET", "JCODE_TUI_INHERITED_THEME"} {
		if _, ok := seen[k]; ok {
			t.Errorf("%s leaked into the child environment", k)
		}
	}
	if seen["KEEP_ME"] != "yes" {
		t.Error("childEnv dropped an unrelated variable")
	}
	if seen["DAGRUN"] != "1" {
		t.Error("childEnv did not mark the child as dagrun-spawned")
	}
}
