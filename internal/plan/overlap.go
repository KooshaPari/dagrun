package plan

import (
	"fmt"
	"path/filepath"
	"sort"
)

// Overlaps reports plan-time collisions between nodes that can run at the same
// time: parallel nodes that would write the same artifact, or whose declared
// file_scope intersects.
//
// Two nodes are parallel when neither is an ancestor of the other: there is no
// dependency path connecting them in either direction. Sequential nodes may
// share an artifact or a scope freely, because the dependency hand-off is the
// ordering guarantee.
//
// Optional nodes and fan_out templates are exempt. An optional node's failure
// is not a contract, and a fan_out template's children carry the real scope;
// the template itself never executes.
func (p *Plan) Overlaps() []Issue {
	idx := p.Index()
	anc := p.ancestors()
	parallel := func(a, b string) bool { return !anc[a][b] && !anc[b][a] }

	elig := make([]*Node, 0, len(idx))
	for _, n := range p.Sorted() {
		if n.Optional || n.FanOut != "" {
			continue
		}
		elig = append(elig, n)
	}

	var out []Issue
	seen := map[string]bool{}
	report := func(code, node, pathA, pathB, other string) {
		key := code + "\x00" + node + "\x00" + pathA + "\x00" + pathB + "\x00" + other
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, Issue{
			Node:   node,
			Code:   code,
			Detail: fmt.Sprintf("%q collides with %q on node %q", pathA, pathB, other),
		})
	}

	for i := 0; i < len(elig); i++ {
		for j := i + 1; j < len(elig); j++ {
			a, b := elig[i], elig[j]
			if !parallel(a.ID, b.ID) {
				continue
			}
			for _, aa := range a.Artifacts {
				for _, ba := range b.Artifacts {
					if samePath(aa.Path, ba.Path) {
						report("artifact_collision", a.ID, aa.Path, ba.Path, b.ID)
					}
				}
			}
			for _, sa := range a.FileScope {
				for _, sb := range b.FileScope {
					if scopeOverlap(sa, sb) {
						report("scope_overlap", a.ID, sa, sb, b.ID)
					}
				}
			}
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Node != out[j].Node {
			return Less(out[i].Node, out[j].Node)
		}
		if out[i].Code != out[j].Code {
			return out[i].Code < out[j].Code
		}
		return out[i].Detail < out[j].Detail
	})
	return out
}

// ancestors maps a node id to the set of ids reachable by following its deps,
// i.e. every node that must finish before it starts. A cycle is tolerated here
// (the Cycle check owns that verdict) by not recursing into an in-progress id.
func (p *Plan) ancestors() map[string]map[string]bool {
	idx := p.Index()
	const (
		visiting = 1
		done     = 2
	)
	state := make(map[string]int, len(idx))
	memo := make(map[string]map[string]bool, len(idx))

	var visit func(id string) map[string]bool
	visit = func(id string) map[string]bool {
		switch state[id] {
		case done:
			return memo[id]
		case visiting:
			return nil // cycle guard: contributes nothing
		}
		state[id] = visiting
		set := make(map[string]bool, 4)
		n := idx[id]
		if n != nil {
			for _, d := range n.Deps {
				if _, ok := idx[d]; !ok {
					continue
				}
				if d != id {
					set[d] = true
				}
				for up := range visit(d) {
					set[up] = true
				}
			}
		}
		state[id] = done
		memo[id] = set
		return set
	}

	for id := range idx {
		visit(id)
	}
	return memo
}

// samePath reports whether two declared artifact paths name the same file.
func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// scopeOverlap reports whether two file_scope entries can claim the same file:
// the strings are equal, or one is a glob that matches the other's literal
// path.
func scopeOverlap(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	if ok, err := filepath.Match(a, b); err == nil && ok {
		return true
	}
	if ok, err := filepath.Match(b, a); err == nil && ok {
		return true
	}
	return false
}
