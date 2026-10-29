package plan

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// ScopeIssues reports parallel nodes whose file_scope sets intersect.
//
// Two nodes are parallel when neither is an ancestor of the other. Sequential
// nodes may share scope freely: that is a hand-off, not a conflict.
//
// Fan_out template nodes and optional nodes are exempt for the same reasons
// Overlaps exempts them: a template never executes itself, and an optional
// node's failure is not a contract. A node that declares no file_scope can
// never conflict on scope.
func (p *Plan) ScopeIssues() []Issue {
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
	report := func(code, node, detail string) {
		key := code + "\x00" + node + "\x00" + detail
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, Issue{Node: node, Code: code, Detail: detail})
	}

	for i := 0; i < len(elig); i++ {
		for j := 0; j < len(elig); j++ {
			a, b := elig[i], elig[j]
			if i == j || !parallel(a.ID, b.ID) {
				continue
			}

			// Rule 2: two parallel nodes whose scopes intersect.
			for _, sa := range a.FileScope {
				if sa == "" {
					continue
				}
				for _, sb := range b.FileScope {
					if !scopeParallelOverlap(sa, sb) {
						continue
					}
					report("scope_overlap", a.ID, fmt.Sprintf(
						"%s scope '%s' overlaps %s scope '%s'; both run in parallel",
						a.ID, sa, b.ID, sb))
				}
			}

			// Rule 4: a scope that claims another node's declared artifact.
			for _, sa := range a.FileScope {
				if sa == "" {
					continue
				}
				for _, art := range b.Artifacts {
					if !scopeHitsArtifact(sa, art.Path, p.Root) {
						continue
					}
					report("scope_vs_artifact", a.ID, fmt.Sprintf(
						"%s scope '%s' overlaps %s artifact '%s'; both run in parallel",
						a.ID, sa, b.ID, art.Path))
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

// scopeHitsArtifact reports whether a scope entry claims a declared artifact
// path. Artifacts are often resolved against the plan root (absolute), while
// scopes are written repo-relative, so the plan-relative spelling is tried
// too. Any match is enough: the check is deliberately conservative.
func scopeHitsArtifact(scope, artifact, root string) bool {
	if artifact == "" {
		return false
	}
	cands := []string{artifact, filepath.Clean(artifact)}
	if root != "" {
		if rel, err := filepath.Rel(root, artifact); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			cands = append(cands, rel)
		}
	}
	for _, c := range cands {
		if scopeParallelOverlap(scope, c) {
			return true
		}
	}
	return false
}

// scopeParallelOverlap reports whether two file_scope entries can claim the
// same file.
//
//   - Identical (or equivalently cleaned) strings always intersect.
//   - A literal path intersects a glob when filepath.Match accepts it.
//   - Two globs intersect when their literal prefixes, up to the first glob
//     metacharacter in either, are prefixes of one another. This is a
//     conservative approximation: it can report an overlap that cannot
//     actually occur (e.g. "src/*.go" vs "src/no/*.go" style wildcards in the
//     wrong position), but it never misses one that can. "src/*.go" and
//     "src/a/*.go" intersect; "src/*.go" and "docs/*.md" do not.
func scopeParallelOverlap(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if a == b || filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ag, bg := hasGlobMeta(a), hasGlobMeta(b)
	switch {
	case ag && bg:
		pa, pb := literalPrefix(a), literalPrefix(b)
		return strings.HasPrefix(pa, pb) || strings.HasPrefix(pb, pa)
	case ag:
		return matchesGlob(a, b)
	case bg:
		return matchesGlob(b, a)
	}
	return false
}

// hasGlobMeta reports whether s contains any filepath.Match metacharacter.
func hasGlobMeta(s string) bool {
	return strings.ContainsAny(s, "*?[")
}

// literalPrefix returns the part of s before its first glob metacharacter.
func literalPrefix(s string) string {
	if i := strings.IndexAny(s, "*?["); i >= 0 {
		return s[:i]
	}
	return s
}

// matchesGlob applies a glob to a literal path, tolerating a malformed pattern
// by treating it as non-matching.
func matchesGlob(glob, literal string) bool {
	if ok, err := filepath.Match(glob, literal); err == nil && ok {
		return true
	}
	clean := filepath.Clean(literal)
	if clean == literal {
		return false
	}
	ok, err := filepath.Match(glob, clean)
	return err == nil && ok
}
