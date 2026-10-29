package plan

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Validate performs structural validation without touching the filesystem.
// An empty result means the plan is safe to execute.
func (p *Plan) Validate() []Issue {
	var out []Issue
	add := func(node, code, format string, args ...any) {
		out = append(out, Issue{Node: node, Code: code, Detail: fmt.Sprintf(format, args...)})
	}

	if p.Version != 0 && p.Version != SchemaVersion {
		add("", "bad_version", "plan version %d, want %d", p.Version, SchemaVersion)
	}
	if len(p.Nodes) == 0 {
		add("", "empty_plan", "plan has no nodes")
		return out
	}

	seen := make(map[string]bool, len(p.Nodes))
	for _, n := range p.Nodes {
		if n == nil {
			add("", "nil_node", "null entry in nodes list")
			continue
		}
		if strings.TrimSpace(n.ID) == "" {
			add("", "missing_id", "node has no id")
			continue
		}
		if seen[n.ID] {
			add(n.ID, "duplicate_id", "id appears more than once")
		}
		seen[n.ID] = true

		if n.Kind == "" {
			add(n.ID, "missing_kind", "node has no kind; one of %s", kindList())
		} else if !Kinds[n.Kind] {
			add(n.ID, "bad_kind", "unknown kind %q; one of %s", n.Kind, kindList())
		}
		if _, err := ParseEstimate(n.Estimate); err != nil {
			add(n.ID, "bad_estimate", "%v", err)
		}
		if _, err := time.ParseDuration(n.Timeout); err != nil || n.Timeout == "" {
			add(n.ID, "bad_timeout", "timeout %q is not a duration", n.Timeout)
		}
		switch n.Isolation {
		case "", "worktree":
		default:
			add(n.ID, "bad_isolation", "unknown isolation %q (want \"\" or worktree)", n.Isolation)
		}
		switch n.ToolProfile {
		case "", "full", "minimal", "lite", "none":
		default:
			add(n.ID, "bad_tool_profile", "unknown tool_profile %q (want \"full\", \"minimal\", or \"none\")", n.ToolProfile)
		}
		if n.Isolation == "worktree" && n.Workdir != "" {
			add(n.ID, "isolation_conflict", "isolation: worktree sets the working directory; drop workdir")
		}
		if n.Workdir != "" {
			if filepath.IsAbs(n.Workdir) {
				add(n.ID, "bad_workdir", "workdir must be relative to the plan root")
			} else if strings.Contains(n.Workdir, "..") {
				add(n.ID, "bad_workdir", "workdir must not escape the plan root")
			}
		}

		// A node is only admissible if success can be decided. Anything else
		// lets a node "finish" without evidence.
		verifiable := len(n.Artifacts) > 0 || n.Check != ""
		if !verifiable && !n.Optional && n.FanOut == "" {
			add(n.ID, "unverifiable", "declare artifacts, a check, or mark optional: true")
		}
		if n.IsGate() && n.Check == "" && n.Discover == nil {
			add(n.ID, "gate_without_check", "GATE nodes must declare a check")
		}

		if n.Discover != nil {
			if strings.TrimSpace(n.Discover.From) == "" {
				add(n.ID, "discover_no_source", "discover.from is required")
			}
			if script, isCmd := cmdSource(n.Discover.From); isCmd {
				if script == "" {
					add(n.ID, "discover_empty_cmd", "discover.from is %q with no command", CmdPrefix)
				}
			} else if filepath.IsAbs(n.Discover.From) || strings.Contains(n.Discover.From, "..") {
				add(n.ID, "discover_bad_path", "discover.from %q must be relative to the plan root", n.Discover.From)
			}
			switch strings.ToLower(n.Discover.Format) {
			case "", "lines", "json", "jsonl":
			default:
				add(n.ID, "discover_bad_format", "unknown format %q (want json, jsonl, or lines)", n.Discover.Format)
			}
			if n.Discover.Max < 0 {
				add(n.ID, "discover_bad_max", "max must be >= 0")
			}
		}
		if n.FanOut != "" && n.FanIn != "" {
			add(n.ID, "fan_both", "a node cannot be both a fan_out template and a fan_in reduce")
		}

		for _, a := range n.Artifacts {
			if strings.TrimSpace(a.Path) == "" {
				add(n.ID, "artifact_no_path", "artifact entry has no path")
				continue
			}
			if filepath.IsAbs(a.Path) {
				add(n.ID, "artifact_absolute", "artifact path %q must be relative to the plan root", a.Path)
			}
			if strings.Contains(a.Path, "..") {
				add(n.ID, "artifact_escape", "artifact path %q must not escape the plan root", a.Path)
			}
			if a.MinBytes < 0 {
				add(n.ID, "artifact_bad_min_bytes", "artifact %q has negative min_bytes", a.Path)
			}
			if a.MustMatch != "" {
				if _, err := regexp.Compile(a.MustMatch); err != nil {
					add(n.ID, "artifact_bad_regex", "artifact %q must_match: %v", a.Path, err)
				}
			}
		}
	}

	for _, n := range p.Nodes {
		if n == nil {
			continue
		}
		seenDep := map[string]bool{}
		for _, d := range n.Deps {
			if d == n.ID {
				add(n.ID, "self_dep", "node depends on itself")
			}
			if !seen[d] {
				add(n.ID, "unknown_dep", "depends on unknown node %q", d)
			}
			if seenDep[d] {
				add(n.ID, "duplicate_dep", "lists dependency %q twice", d)
			}
			seenDep[d] = true
		}

		switch {
		case n.FanOut != "":
			if !seen[n.FanOut] {
				add(n.ID, "fan_out_unknown", "fan_out references unknown node %q", n.FanOut)
			}
		case n.FanIn != "":
			parent := p.ByID(n.FanIn)
			if parent == nil {
				add(n.ID, "fan_in_unknown", "fan_in references unknown node %q", n.FanIn)
			} else if parent.Discover == nil {
				add(n.ID, "fan_in_not_discover", "fan_in parent %q declares no discover", n.FanIn)
			}
		}
	}

	if cyc, ok := p.Cycle(); ok {
		add("", "cycle", "dependency cycle: %s", strings.Join(cyc, " -> "))
	}

	// The provider policy is validated with the rest of the plan: a bad
	// backoff duration would otherwise silently become "no cooldown" at run
	// time, which is the failure mode a limit exists to prevent.
	for _, issue := range p.ProviderPolicy.Validate() {
		add("", issue.Code, "%s", issue.Detail)
	}

	// Collisions are structural: two nodes that can run at the same time and
	// write the same file race by construction, so they are rejected before a
	// single node starts rather than discovered as a corrupt artifact later.
	out = append(out, p.Overlaps()...)
	return out
}

func kindList() string {
	keys := make([]string, 0, len(Kinds))
	for k := range Kinds {
		keys = append(keys, k)
	}
	// stable output without importing sort twice
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return strings.Join(keys, ", ")
}

// Cycle returns one dependency cycle if the graph is not a DAG.
func (p *Plan) Cycle() ([]string, bool) {
	const (
		white = 0
		grey  = 1
		black = 2
	)
	color := make(map[string]int, len(p.Nodes))
	idx := p.Index()
	var stack []string
	var found []string

	var visit func(id string) bool
	visit = func(id string) bool {
		switch color[id] {
		case grey:
			for i, s := range stack {
				if s == id {
					found = append(append([]string{}, stack[i:]...), id)
					return true
				}
			}
			found = []string{id, id}
			return true
		case black:
			return false
		}
		color[id] = grey
		stack = append(stack, id)
		if n := idx[id]; n != nil {
			for _, d := range n.Deps {
				if _, ok := idx[d]; !ok {
					continue
				}
				if visit(d) {
					return true
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[id] = black
		return false
	}

	for _, n := range p.Sorted() {
		if visit(n.ID) {
			return found, true
		}
	}
	return nil, false
}

// Topo returns node ids in a deterministic dependency-respecting order.
// Ties are broken by natural WBS id order so runs are reproducible.
func (p *Plan) Topo() []string {
	idx := p.Index()
	indeg := make(map[string]int, len(idx))
	children := make(map[string][]string, len(idx))
	for id := range idx {
		indeg[id] = 0
	}
	for id, n := range idx {
		for _, d := range n.Deps {
			if _, ok := idx[d]; ok {
				indeg[id]++
				children[d] = append(children[d], id)
			}
		}
	}
	sorted := p.Sorted()
	var ready []string
	for _, n := range sorted {
		if indeg[n.ID] == 0 {
			ready = append(ready, n.ID)
		}
	}
	out := make([]string, 0, len(idx))
	for len(ready) > 0 {
		// pick the smallest id among ready so the order is stable
		best := 0
		for i := range ready {
			if Less(ready[i], ready[best]) {
				best = i
			}
		}
		id := ready[best]
		ready = append(ready[:best], ready[best+1:]...)
		out = append(out, id)
		for _, c := range children[id] {
			indeg[c]--
			if indeg[c] == 0 {
				ready = append(ready, c)
			}
		}
	}
	if len(out) != len(idx) {
		// cycle: append the remainder so callers can still render something
		for _, n := range sorted {
			if indeg[n.ID] > 0 {
				out = append(out, n.ID)
			}
		}
	}
	return out
}
