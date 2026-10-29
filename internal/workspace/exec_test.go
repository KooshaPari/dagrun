package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
		ExecTemplate: `sh -c 'echo ok > out.txt'`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Failed != 0 || sum.Done != 2 {
		t.Fatalf("summary = %+v, want 2 done and 0 failed", sum)
	}
	for _, dir := range []string{"api", "web"} {
		if _, err := os.Stat(filepath.Join(root, dir, "out.txt")); err != nil {
			t.Fatalf("%s did not produce its artifact: %v", dir, err)
		}
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
