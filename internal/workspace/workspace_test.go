package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const apiPlan = `
version: 1
run: api
nodes:
  - id: "1.1"
    kind: IMPL
    title: api work
    artifacts: [{path: a.txt}]
  - id: "1.2"
    kind: IMPL
    title: api follow-up
    deps: ["1.1"]
    artifacts: [{path: b.txt}]
`

const webPlan = `
version: 1
run: web
nodes:
  - id: "1.1"
    kind: IMPL
    title: web work
    artifacts: [{path: c.txt}]
  - id: "2.1"
    kind: IMPL
    title: web waits on api
    deps: ["1.1"]
    artifacts: [{path: d.txt}]
`

// fixture builds a workspace with two repos and a repo-free entry.
func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mk := func(dir, plan string) {
		full := filepath.Join(root, dir)
		if err := os.MkdirAll(full, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(full, "plan.yaml"), []byte(plan), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("api", apiPlan)
	mk("web", webPlan)
	mk("research", apiPlan)
	ws := `version: 1
name: cross-repo
intent: do the thing across two repos
repos:
  - path: api
    plan: plan.yaml
  - path: web
    plan: plan.yaml
  - path: research
    plan: plan.yaml
    label: notes
`
	if err := os.WriteFile(filepath.Join(root, "dagrun.workspace.yaml"), []byte(ws), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestLoadResolvesPathsRelativeToTheWorkspaceFile(t *testing.T) {
	root := fixture(t)
	// load from a different cwd to prove resolution is not cwd-based
	wd, _ := os.Getwd()
	defer func() { _ = os.Chdir(wd) }()
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	w, err := Load(filepath.Join(root, "dagrun.workspace.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Repos) != 3 {
		t.Fatalf("got %d repos", len(w.Repos))
	}
	want := filepath.Join(root, "api", "plan.yaml")
	if got := w.PlanPath(w.Repos[0]); got != want {
		t.Errorf("plan path = %q, want %q", got, want)
	}
}

func TestValidateCatchesStructuralProblems(t *testing.T) {
	root := t.TempDir()
	bad := `version: 1
name: broken
repos:
  - path: a
    plan: missing.yaml
  - path: a
    plan: other.yaml
`
	path := filepath.Join(root, "dagrun.workspace.yaml")
	if err := os.WriteFile(path, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	errs := w.Validate()
	if len(errs) < 3 {
		t.Fatalf("expected at least 3 problems, got %v", errs)
	}
	joined := ""
	for _, e := range errs {
		joined += e.Error() + "\n"
	}
	for _, want := range []string{"not found", "duplicate path"} {
		if !strings.Contains(joined, want) {
			t.Errorf("problems missing %q:\n%s", want, joined)
		}
	}
}

func TestValidateRejectsAnEmptyRepoList(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "w.yaml")
	if err := os.WriteFile(path, []byte("version: 1\nname: x\nrepos: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w, _ := Load(path)
	errs := w.Validate()
	if len(errs) == 0 {
		t.Fatal("an empty repo list must be rejected")
	}
}

func TestNamespacesAreDistinct(t *testing.T) {
	root := fixture(t)
	w, err := Load(filepath.Join(root, "dagrun.workspace.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	api := w.Namespace(w.Repos[0], "1.1")
	web := w.Namespace(w.Repos[1], "1.1")
	if api == web {
		t.Fatalf("two repos produced the same namespace: %q", api)
	}
	if !strings.HasPrefix(api, "api/") || !strings.HasPrefix(web, "web/") {
		t.Fatalf("namespaces = %q / %q", api, web)
	}
	notes := w.Namespace(w.Repos[2], "1.1")
	if !strings.HasPrefix(notes, "notes/") {
		t.Errorf("label should win over path: %q", notes)
	}
}

func TestNamespacedIDsCoversEveryNode(t *testing.T) {
	root := fixture(t)
	w, _ := Load(filepath.Join(root, "dagrun.workspace.yaml"))
	ids, err := w.NamespacedIDs(w.PlanPath(w.Repos[0]))
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("got %d ids, want 2: %v", len(ids), ids)
	}
	if ids[0] != "api/1.1" || ids[1] != "api/1.2" {
		t.Errorf("ids = %v", ids)
	}
}

func TestResolveDepsFindsACrossRepoDependency(t *testing.T) {
	root := fixture(t)
	w, _ := Load(filepath.Join(root, "dagrun.workspace.yaml"))
	webPlan := w.PlanPath(w.Repos[1])
	resolved, unresolved := w.ResolveDeps(webPlan, []string{"1.1", "9.9"})
	if len(unresolved) != 1 || unresolved[0] != "9.9" {
		t.Fatalf("unresolved = %v, want [9.9]", unresolved)
	}
	// "1.1" exists in both api and web; a dep that is local wins
	if len(resolved) != 1 || resolved[0] != "web/1.1" {
		t.Fatalf("resolved = %v, want [web/1.1]", resolved)
	}
}

func TestResolveDepsPrefersTheLocalRepo(t *testing.T) {
	root := fixture(t)
	w, _ := Load(filepath.Join(root, "dagrun.workspace.yaml"))
	apiPlan := w.PlanPath(w.Repos[0])
	resolved, unresolved := w.ResolveDeps(apiPlan, []string{"1.1"})
	if len(unresolved) != 0 {
		t.Fatalf("unresolved = %v", unresolved)
	}
	if resolved[0] != "api/1.1" {
		t.Fatalf("resolved = %v, want api/1.1", resolved)
	}
}
