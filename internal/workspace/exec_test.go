package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KooshaPari/dagrun/internal/plan"
	"github.com/KooshaPari/dagrun/internal/store"
)

// The two repos deliberately share the local id "1.1". Only "1.9" exists in api
// alone, so web's dependency on it must be rewritten to "api/1.9".
const xApiPlan = `
version: 1
run: api
nodes:
  - id: "1.1"
    kind: IMPL
    title: api first
    artifacts: [{path: api-a.txt, min_bytes: 1}]
  - id: "1.9"
    kind: IMPL
    title: api prerequisite for web
    artifacts: [{path: api-b.txt, min_bytes: 1}]
`

const xWebPlan = `
version: 1
run: web
nodes:
  - id: "1.1"
    kind: IMPL
    title: web first
    artifacts: [{path: web-a.txt, min_bytes: 1}]
  - id: "2.1"
    kind: IMPL
    title: web waits on api
    deps: ["1.9"]
    artifacts: [{path: web-b.txt, min_bytes: 1}]
`

func crossRepoFixture(t *testing.T) *Workspace {
	t.Helper()
	root := t.TempDir()
	mk := func(dir, body string) {
		full := filepath.Join(root, dir)
		if err := os.MkdirAll(full, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(full, "plan.yaml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("api", xApiPlan)
	mk("web", xWebPlan)
	ws := `version: 1
name: cross-repo-run
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
	return w
}

func TestCombinedPlanNamespacesCollidingIDsAndRewritesCrossRepoDeps(t *testing.T) {
	w := crossRepoFixture(t)
	combined, _, err := w.CombinedPlan()
	if err != nil {
		t.Fatal(err)
	}

	ids := map[string]bool{}
	for _, n := range combined.Nodes {
		ids[n.ID] = true
	}
	if !ids["api/1.1"] || !ids["web/1.1"] || !ids["api/1.9"] || !ids["web/2.1"] {
		t.Fatalf("combined ids missing: %v", ids)
	}
	if len(combined.Nodes) != 4 {
		t.Fatalf("combined plan has %d nodes, want 4", len(combined.Nodes))
	}

	web := combined.ByID("web/2.1")
	if web == nil {
		t.Fatal("web/2.1 missing")
	}
	if len(web.Deps) != 1 || web.Deps[0] != "api/1.9" {
		t.Fatalf("web/2.1 deps = %v, want [api/1.9]", web.Deps)
	}
}

func TestCombinedTopoPutsTheApiNodeFirst(t *testing.T) {
	w := crossRepoFixture(t)
	order, err := w.CombinedTopo()
	if err != nil {
		t.Fatal(err)
	}
	pos := map[string]int{}
	for i, id := range order {
		pos[id] = i
	}
	if pos["api/1.9"] >= pos["web/2.1"] {
		t.Fatalf("api/1.9 at %d not before web/2.1 at %d (%v)",
			pos["api/1.9"], pos["web/2.1"], order)
	}
}

func TestCombinedPlanReportsUnknownDependencies(t *testing.T) {
	root := t.TempDir()
	broken := `
version: 1
run: web
nodes:
  - id: "1.1"
    kind: IMPL
    deps: ["does-not-exist"]
    artifacts: [{path: x.txt}]
`
	if err := os.WriteFile(filepath.Join(root, "plan.yaml"), []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	ws := "version: 1\nname: broken\nrepos:\n  - path: .\n    plan: plan.yaml\n    label: web\n"
	path := filepath.Join(root, "dagrun.workspace.yaml")
	if err := os.WriteFile(path, []byte(ws), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = w.CombinedPlan()
	if err == nil {
		t.Fatal("expected an error for an unknown dependency, got nil")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Fatalf("error does not name the unknown dep: %v", err)
	}
}

// statesFor builds per-repo state maps with everything pending except the
// overrides given as "namespaced-id" -> status.
func statesFor(t *testing.T, w *Workspace, overrides map[string]string) map[string]*store.State {
	t.Helper()
	states := map[string]*store.State{}
	for _, r := range w.Repos {
		p, err := plan.Load(w.PlanPath(r))
		if err != nil {
			t.Fatal(err)
		}
		st := &store.State{Version: store.StateVersion, Run: p.Run, Nodes: map[string]*store.NodeState{}}
		for _, n := range p.Nodes {
			st.Nodes[n.ID] = &store.NodeState{Status: plan.StatusPending}
		}
		states[w.RepoRoot(r)] = st
	}
	for id, status := range overrides {
		repo, local, ok := w.repoForNamespaced(id)
		if !ok {
			t.Fatalf("cannot resolve %s", id)
		}
		st := states[w.RepoRoot(repo)]
		ns := st.Nodes[local]
		if ns == nil {
			t.Fatalf("%s is not in %s's plan", local, repo.Path)
		}
		ns.Status = status
	}
	return states
}

func TestReadyNodesWaitsForTheCrossRepoDependency(t *testing.T) {
	w := crossRepoFixture(t)
	combined, _, err := w.CombinedPlan()
	if err != nil {
		t.Fatal(err)
	}

	ready := w.ReadyNodes(combined, statesFor(t, w, nil))
	if has(ready, "web/2.1") {
		t.Fatalf("web/2.1 is ready before api/1.9 is done: %v", ready)
	}
	if !has(ready, "api/1.9") || !has(ready, "api/1.1") || !has(ready, "web/1.1") {
		t.Fatalf("independent nodes should be ready: %v", ready)
	}

	ready = w.ReadyNodes(combined, statesFor(t, w, map[string]string{"api/1.9": plan.StatusDone}))
	if !has(ready, "web/2.1") {
		t.Fatalf("web/2.1 not ready after api/1.9 done: %v", ready)
	}

	// A skipped dependency counts as satisfied, exactly like the single-repo store.
	ready = w.ReadyNodes(combined, statesFor(t, w, map[string]string{"api/1.9": plan.StatusSkipped}))
	if !has(ready, "web/2.1") {
		t.Fatalf("web/2.1 not ready after api/1.9 skipped: %v", ready)
	}
}

func TestNodeOwnerResolvesToTheOwningReposLocalNode(t *testing.T) {
	w := crossRepoFixture(t)
	combined, _, err := w.CombinedPlan()
	if err != nil {
		t.Fatal(err)
	}

	root, node, ok := w.NodeOwner(combined, "web/2.1")
	if !ok {
		t.Fatal("web/2.1 not resolved")
	}
	if root != w.RepoRoot(w.Repos[1]) {
		t.Fatalf("owner root = %s, want the web repo", root)
	}
	if node.ID != "2.1" {
		t.Fatalf("node id = %s, want the LOCAL id 2.1", node.ID)
	}
	if len(node.Artifacts) != 1 || node.Artifacts[0].Path != "web-b.txt" {
		t.Fatalf("contract not taken from the repo plan: %+v", node.Artifacts)
	}

	if _, _, ok := w.NodeOwner(combined, "web/9.9"); ok {
		t.Fatal("web/9.9 should not resolve")
	}
	if _, _, ok := w.NodeOwner(combined, "nope/1.1"); ok {
		t.Fatal("nope/1.1 should not resolve")
	}
}

// TestRunExecutesAcrossReposInOrder drives the real executor. The web node's
// check fails unless the api repo has already produced its artifact, so a
// green run proves the cross-repo dependency gated the start.
func TestRunExecutesAcrossReposInOrder(t *testing.T) {
	root := t.TempDir()
	apiPlan := `
version: 1
run: api
nodes:
  - id: "1.9"
    kind: IMPL
    harness: exec
    artifacts: [{path: out.txt, min_bytes: 1}]
`
	webPlan := `
version: 1
run: web
nodes:
  - id: "2.1"
    kind: IMPL
    harness: exec
    deps: ["1.9"]
    artifacts: [{path: out.txt, min_bytes: 1}]
    check: sh -c 'test -f ../api/out.txt'
`
	for dir, body := range map[string]string{"api": apiPlan, "web": webPlan} {
		full := filepath.Join(root, dir)
		if err := os.MkdirAll(full, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(full, "plan.yaml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ws := `version: 1
name: ordered
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

	sum, err := w.Run(context.Background(), RunOptions{
		Workers: 1,
		Out:     os.Stderr,
		// Never spawns a real agent: the template writes the declared artifact.
		// The template must write the artifact each node DECLARES (out.txt):
		// the executor verifies the contract, so a template that writes
		// something else correctly fails the node. The marker files are extra
		// evidence that the node really ran inside its own repo.
		ExecTemplate: `sh -c "echo ok > out.txt && echo ok > api-a.txt && echo ok > api-b.txt && echo ok > web-a.txt && echo ok > web-b.txt"`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Failed != 0 || sum.Done != 2 {
		t.Fatalf("summary = %+v, want 2 done and 0 failed", sum)
	}
	for _, f := range []string{
		filepath.Join(root, "api", "api-a.txt"),
		filepath.Join(root, "api", "api-b.txt"),
		filepath.Join(root, "web", "web-a.txt"),
		filepath.Join(root, "web", "web-b.txt"),
	} {
		if _, err := os.Stat(f); err != nil {
			t.Fatalf("%s was not produced: %v", f, err)
		}
	}
}

// TestRunDoesNotHangOnFailedCrossRepoDependency is the regression test for the
// hang: api's node fails (its artifact is never produced), so web's dependency
// on it can NEVER become done. Before the fix, the gate reported only "not
// ready", the web executor took that as "wait for the other repo", and Run
// polled forever: the external state it waited on was never going to exist.
//
// The requirement is termination with a clear failure, not a green run.
func TestRunDoesNotHangOnFailedCrossRepoDependency(t *testing.T) {
	root := t.TempDir()
	apiPlan := `
version: 1
run: api
nodes:
  - id: "1.9"
    kind: IMPL
    harness: exec
    artifacts: [{path: out.txt, min_bytes: 1}]
`
	webPlan := `
version: 1
run: web
nodes:
  - id: "2.1"
    kind: IMPL
    harness: exec
    deps: ["1.9"]
    artifacts: [{path: out.txt, min_bytes: 1}]
`
	for dir, body := range map[string]string{"api": apiPlan, "web": webPlan} {
		full := filepath.Join(root, dir)
		if err := os.MkdirAll(full, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(full, "plan.yaml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ws := `version: 1
name: failed-dep
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

	// The template never writes out.txt, so both nodes fail verification.
	done := make(chan RunSummary, 1)
	go func() {
		sum, _ := w.Run(context.Background(), RunOptions{
			Workers:      1,
			Out:          os.Stderr,
			ExecTemplate: `sh -c "true"`,
		})
		done <- sum
	}()
	select {
	case sum := <-done:
		if sum.Failed == 0 {
			t.Fatalf("summary = %+v, want at least one failed node", sum)
		}
		if sum.Pending != 0 || sum.Ran != 0 {
			t.Fatalf("summary = %+v, want no node left pending or running", sum)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return: the failed cross-repo dependency was waited on forever")
	}
}

func has(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// TestRunAcrossReposIsNotFlaky pins the bug that made a cross-repo run
// intermittently skip the second repo.
//
// The dependency lived in ANOTHER repo, so it was never in the web repo's own
// state. The gate treated "not in the state yet" as "can never be done", so
// the web executor gave up instead of waiting for the api executor to claim
// and finish the node.
//
// It passed when the machine was quiet and failed when it was busy, because the
// race was about which executor got there first.
func TestRunAcrossReposIsNotFlaky(t *testing.T) {
	for i := 0; i < 8; i++ {
		w := crossRepoFixture(t)
		sum, err := w.Run(context.Background(), RunOptions{
			Workers:      1,
			Out:          os.Stderr,
			ExecTemplate: `sh -c "echo ok > api-a.txt && echo ok > api-b.txt && echo ok > web-a.txt && echo ok > web-b.txt"`,
		})
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		// The fixture has two nodes per repo: api/1.1, api/1.9, web/1.1,
		// web/2.1. All four must finish.
		if sum.Done != 4 || sum.Failed != 0 || sum.Pending != 0 {
			t.Fatalf("run %d: summary = %+v, want 4 done, 0 failed, 0 pending", i, sum)
		}
	}
}
