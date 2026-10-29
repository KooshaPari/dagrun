package workspace

import (
	"strings"

	"github.com/KooshaPari/dagrun/internal/plan"
	"github.com/KooshaPari/dagrun/internal/store"
)

// DefaultRunDir is the per-repo run directory, relative to the repo root.
const DefaultRunDir = ".dagrun"

// repoForNamespaced resolves a namespaced id ("web/2.1") to its owning repo and
// the local id inside that repo ("2.1").
func (w *Workspace) repoForNamespaced(id string) (Repo, string, bool) {
	for _, r := range w.Repos {
		prefix := w.Prefix(r) + "/"
		if strings.HasPrefix(id, prefix) {
			return r, strings.TrimPrefix(id, prefix), true
		}
	}
	return Repo{}, "", false
}

// NodeOwner resolves a namespaced id to the repo that owns it and the node as
// that repo's own plan declares it.
//
// The returned repoPath is the repo root, which is what the node's artifact
// paths resolve against. The node is the local node of record, never the
// synthetic combined-plan copy, so a scheduler can execute the real contract.
func (w *Workspace) NodeOwner(combined *plan.Plan, id string) (repoPath string, node *plan.Node, ok bool) {
	if combined != nil {
		known := false
		for _, n := range combined.Nodes {
			if n != nil && n.ID == id {
				known = true
				break
			}
		}
		if !known {
			return "", nil, false
		}
	}
	repo, local, found := w.repoForNamespaced(id)
	if !found {
		return "", nil, false
	}
	p, err := plan.Load(w.PlanPath(repo))
	if err != nil {
		return "", nil, false
	}
	n := p.ByID(local)
	if n == nil {
		return "", nil, false
	}
	return w.RepoRoot(repo), n, true
}

// ReadyNodes returns the namespaced ids that may start now.
//
// states is keyed by absolute repo root and holds each repo's own run state.
// A node is ready only when it is pending in its own store AND every dependency
// the COMBINED plan names is done or skipped, in whichever repo owns it. That
// last clause is the whole point: a node in "web" cannot start while the "api"
// node it depends on is unfinished.
func (w *Workspace) ReadyNodes(combined *plan.Plan, states map[string]*store.State) []string {
	var out []string
	if combined == nil {
		return out
	}
	for _, n := range combined.Sorted() {
		if n == nil || n.FanOut != "" {
			continue // a fan_out template is executed as its children
		}
		repo, local, ok := w.repoForNamespaced(n.ID)
		if !ok {
			continue
		}
		st := states[w.RepoRoot(repo)]
		if st == nil {
			continue
		}
		ns := st.Nodes[local]
		if ns == nil || ns.Status != plan.StatusPending {
			continue
		}
		if !depsDoneInWorkspace(w, n, states) {
			continue
		}
		out = append(out, n.ID)
	}
	return out
}

// depsDoneInWorkspace reports whether every combined dependency is terminal.
// An unresolvable or unreadable dependency is NOT done: a missing dependency
// blocks rather than being silently treated as satisfied.
func depsDoneInWorkspace(w *Workspace, n *plan.Node, states map[string]*store.State) bool {
	for _, d := range n.Deps {
		repo, local, ok := w.repoForNamespaced(d)
		if !ok {
			return false
		}
		st := states[w.RepoRoot(repo)]
		if st == nil {
			return false
		}
		ds := st.Nodes[local]
		if ds == nil {
			return false
		}
		if ds.Status != plan.StatusDone && ds.Status != plan.StatusSkipped {
			return false
		}
	}
	return true
}
