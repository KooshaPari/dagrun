package workspace

import (
	"fmt"
	"os"
	"strings"

	"github.com/KooshaPari/dagrun/internal/plan"
)

// CombinedPlan builds one synthetic Plan whose nodes are the namespaced union
// of every repo's nodes, with cross-repo dependencies rewritten.
//
// The returned map keys each namespaced node id to the repo path that owns it,
// so a scheduler can resolve each node's artifacts against its own repo root.
func (w *Workspace) CombinedPlan() (*plan.Plan, map[string]string, error) {
	combined := &plan.Plan{
		Version: 1,
		Run:     w.Name,
		Intent:  w.Intent,
	}
	owners := make(map[string]string)
	var unresolved []string
	first := true

	for _, repo := range w.Repos {
		planPath := w.PlanPath(repo)
		raw, err := os.ReadFile(planPath)
		if err != nil {
			return nil, nil, err
		}
		src, err := planFromYAML(raw)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", planPath, err)
		}
		if first {
			combined.MaxParallel = src.MaxParallel
			combined.ArtifactsRoot = src.ArtifactsRoot
			first = false
		}
		repoRoot := w.RepoRoot(repo)
		for _, n := range src.Sorted() {
			id := w.Namespace(repo, n.ID)
			node := &plan.Node{
				ID:          id,
				Kind:        n.Kind,
				Title:       n.Title,
				Estimate:    n.Estimate,
				Role:        n.Role,
				Harness:     n.Harness,
				Provider:    n.Provider,
				Model:       n.Model,
				Prompt:      n.Prompt,
				Artifacts:   n.Artifacts,
				FileScope:   n.FileScope,
				Check:       n.Check,
				Workdir:     n.Workdir,
				Isolation:   n.Isolation,
				Timeout:     n.Timeout,
				MaxAttempts: n.MaxAttempts,
				Optional:    n.Optional,
				Discover:    n.Discover,
				FanOut:      n.FanOut,
				FanIn:       n.FanIn,
			}
			if len(n.Deps) > 0 {
				resolved, missing := w.ResolveDeps(planPath, n.Deps)
				node.Deps = resolved
				for _, d := range missing {
					unresolved = append(unresolved,
						fmt.Sprintf("%s (node %s in %s)", d, n.ID, repo.Path))
				}
			}
			combined.Nodes = append(combined.Nodes, node)
			owners[id] = repoRoot
		}
	}

	if len(unresolved) > 0 {
		return nil, nil, fmt.Errorf("unresolved dependencies dropped from combined plan: %s",
			strings.Join(unresolved, ", "))
	}
	return combined, owners, nil
}

// CombinedTopo returns the dependency order across all repos.
func (w *Workspace) CombinedTopo() ([]string, error) {
	combined, _, err := w.CombinedPlan()
	if err != nil {
		return nil, err
	}
	order := combined.Topo()
	if len(order) != len(combined.Nodes) {
		return nil, fmt.Errorf("combined plan has a dependency cycle: ordered %d of %d nodes",
			len(order), len(combined.Nodes))
	}
	return order, nil
}
