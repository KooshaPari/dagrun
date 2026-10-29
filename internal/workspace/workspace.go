// Package workspace models work that spans several repositories, or none.
//
// dagrun does not assume a repository: artifacts are files and checks are shell
// commands. A workspace is therefore just a list of plans in different places,
// with node ids namespaced so two plans can both contain "1.1" without
// colliding.
package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Version is the only workspace schema version accepted.
const Version = 1

// Repo is one plan location inside a workspace.
type Repo struct {
	// Path is the repository root, relative to the workspace file. It may be
	// "." for work with no repository at all.
	Path string `yaml:"path" json:"path"`
	// Plan is the plan file, relative to Path.
	Plan string `yaml:"plan" json:"plan"`
	// ArtifactsRoot overrides the plan's artifacts_root when set.
	ArtifactsRoot string `yaml:"artifacts_root,omitempty" json:"artifacts_root,omitempty"`
	// Label is a human name for the repo, used in the namespace when Path is
	// not a usable identifier.
	Label string `yaml:"label,omitempty" json:"label,omitempty"`
}

// Workspace is a set of plans that together satisfy one intent.
type Workspace struct {
	Version int    `yaml:"version" json:"version"`
	Name    string `yaml:"name" json:"name"`
	Intent  string `yaml:"intent,omitempty" json:"intent,omitempty"`
	Repos   []Repo `yaml:"repos" json:"repos"`

	// Root is the directory the workspace file lives in. Paths resolve
	// against it, not the working directory.
	Root string `yaml:"-" json:"-"`
}

// Load reads a workspace file and resolves repo paths against its directory.
func Load(path string) (*Workspace, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var w Workspace
	if err := yaml.Unmarshal(raw, &w); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	abs, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	w.Root = abs
	if w.Version == 0 {
		w.Version = Version
	}
	return &w, nil
}

// Validate reports every structural problem with the workspace.
func (w *Workspace) Validate() []error {
	var out []error
	if w.Version != Version {
		out = append(out, fmt.Errorf("workspace version %d, want %d", w.Version, Version))
	}
	if len(w.Repos) == 0 {
		out = append(out, fmt.Errorf("workspace has no repos"))
	}
	seen := map[string]bool{}
	for i, r := range w.Repos {
		if strings.TrimSpace(r.Path) == "" {
			out = append(out, fmt.Errorf("repo %d: path is empty", i))
		}
		if strings.TrimSpace(r.Plan) == "" {
			out = append(out, fmt.Errorf("repo %d: plan is empty", i))
		}
		key := r.Path
		if seen[key] {
			out = append(out, fmt.Errorf("repo %d: duplicate path %q", i, r.Path))
		}
		seen[key] = true
		if r.Plan != "" {
			full := w.PlanPath(r)
			if _, err := os.Stat(full); err != nil {
				out = append(out, fmt.Errorf("repo %d (%s): plan file not found: %s", i, r.Path, full))
			}
		}
	}
	return out
}

// RepoPath returns the absolute repository root.
func (r Repo) RepoPath(root string) string {
	if filepath.IsAbs(r.Path) {
		return r.Path
	}
	return filepath.Join(root, r.Path)
}

// RepoRoot is the repository root for a repo in this workspace.
func (w *Workspace) RepoRoot(r Repo) string {
	return r.RepoPath(w.Root)
}

// PlanPath returns the absolute plan file path.
func (w *Workspace) PlanPath(r Repo) string {
	return filepath.Join(r.RepoPath(w.Root), r.Plan)
}

// PlanPaths returns every plan file path in workspace order.
func (w *Workspace) PlanPaths() []string {
	out := make([]string, 0, len(w.Repos))
	for _, r := range w.Repos {
		out = append(out, w.PlanPath(r))
	}
	return out
}

// Prefix is the namespace a repo's node ids carry inside a workspace.
func (w *Workspace) Prefix(r Repo) string {
	base := r.Label
	if base == "" {
		base = r.Path
	}
	base = strings.ReplaceAll(strings.Trim(base, "./"), string(filepath.Separator), "-")
	base = strings.ReplaceAll(base, "/", "-")
	base = strings.ReplaceAll(base, " ", "-")
	for strings.Contains(base, "--") {
		base = strings.ReplaceAll(base, "--", "-")
	}
	base = strings.Trim(base, "-")
	if base == "" {
		base = "root"
	}
	return base
}

// Namespace is the namespaced id for a node in a repo.
func (w *Workspace) Namespace(r Repo, nodeID string) string {
	return w.Prefix(r) + "/" + nodeID
}

// NamespacedIDs returns every node id of a repo's plan, namespaced.
func (w *Workspace) NamespacedIDs(planPath string) ([]string, error) {
	raw, err := os.ReadFile(planPath)
	if err != nil {
		return nil, err
	}
	p, err := planFromYAML(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", planPath, err)
	}
	repo, ok := w.repoForPlan(planPath)
	if !ok {
		return nil, fmt.Errorf("no repo in this workspace owns %s", planPath)
	}
	out := make([]string, 0, len(p.Nodes))
	for _, n := range p.Sorted() {
		out = append(out, w.Namespace(repo, n.ID))
	}
	return out, nil
}

// repoForPlan finds the repo whose plan file matches path.
func (w *Workspace) repoForPlan(planPath string) (Repo, bool) {
	abs, err := filepath.Abs(planPath)
	if err != nil {
		return Repo{}, false
	}
	for _, r := range w.Repos {
		if w.PlanPath(r) == abs {
			return r, true
		}
	}
	return Repo{}, false
}

// AllIDs returns every node id in the workspace, namespaced, in workspace order.
func (w *Workspace) AllIDs() (map[string]string, error) {
	out := map[string]string{}
	for _, r := range w.Repos {
		ids, err := w.NamespacedIDs(w.PlanPath(r))
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			out[id] = w.PlanPath(r)
		}
	}
	return out, nil
}

// ResolveDeps maps a repo's dependency ids onto the workspace.
//
// A dep that names a node in the same repo resolves to that repo's namespaced
// form. A dep that names a node in ANOTHER repo resolves to that other repo's
// namespaced form, which is how a repo waits on a different repo. A dep that
// names nothing is returned in unresolved.
func (w *Workspace) ResolveDeps(planPath string, deps []string) ([]string, []string) {
	owner, ok := w.repoForPlan(planPath)
	if !ok {
		return nil, deps
	}
	local := map[string]bool{}
	if ids, err := w.NamespacedIDs(planPath); err == nil {
		for _, id := range ids {
			local[strings.TrimPrefix(id, w.Prefix(owner)+"/")] = true
		}
	}
	var resolved, unresolved []string
	for _, d := range deps {
		if local[d] {
			resolved = append(resolved, w.Namespace(owner, d))
			continue
		}
		if other, found := w.findAnywhere(d); found {
			resolved = append(resolved, w.Namespace(other, d))
			continue
		}
		unresolved = append(unresolved, d)
	}
	sort.Strings(resolved)
	sort.Strings(unresolved)
	return resolved, unresolved
}

// findAnywhere looks for a node id in any repo of the workspace.
func (w *Workspace) findAnywhere(id string) (Repo, bool) {
	for _, r := range w.Repos {
		ids, err := w.NamespacedIDs(w.PlanPath(r))
		if err != nil {
			continue
		}
		for _, got := range ids {
			if strings.TrimPrefix(got, w.Prefix(r)+"/") == id {
				return r, true
			}
		}
	}
	return Repo{}, false
}
