package plan

import "fmt"

// SemanticIssues reports plan problems that are structurally valid but
// semantically wrong: a plan that would run but should not.
//
// Advisory findings use a "warn:" code prefix so callers can separate them
// from errors without changing the Issue type.
func (p *Plan) SemanticIssues() []Issue {
	var out []Issue
	if p == nil {
		return out
	}
	add := func(node, code, format string, args ...any) {
		out = append(out, Issue{Node: node, Code: code, Detail: fmt.Sprintf(format, args...)})
	}

	byID := p.Index()
	nodes := p.Sorted()

	var specs, tests, docs, gates, intents []*Node
	hasSpec := false
	hasDoc := false
	hasGate := false
	for _, n := range nodes {
		switch n.Kind {
		case "SPEC":
			hasSpec = true
			specs = append(specs, n)
		case "TEST":
			tests = append(tests, n)
		case "DOC":
			hasDoc = true
			docs = append(docs, n)
		case "GATE":
			hasGate = true
			gates = append(gates, n)
		case "INTENT":
			intents = append(intents, n)
		}
	}

	// 1. spec_no_test: a SPEC with no TEST to hold the contract to.
	if hasSpec && len(tests) == 0 {
		add(specs[0].ID, "spec_no_test",
			"SPEC %s exists but the plan has no TEST node; add a TEST node that depends on the SPEC and proves the contract",
			specs[0].ID)
	}

	// 2. impl_no_spec: an IMPL whose contract is never stated upstream.
	for _, n := range nodes {
		if n.Kind != "IMPL" {
			continue
		}
		if semHasAncestorKind(byID, n, "SPEC") {
			continue
		}
		add(n.ID, "impl_no_spec",
			"IMPL %s has no SPEC ancestor; add a dep on the SPEC node that states the contract, or insert a SPEC node",
			n.ID)
	}

	// 3. doc_no_gate: documentation with no gate to check it.
	if hasDoc && !hasGate {
		add(docs[0].ID, "doc_no_gate",
			"DOC %s exists but the plan has no GATE node; add a GATE that checks the documentation it produces",
			docs[0].ID)
	}

	// 4. warn:unreachable: a non-GATE node nothing depends on, so its result
	// feeds no gate and no later work.
	if len(nodes) > 1 {
		dependents := semDependents(byID)
		for _, n := range nodes {
			if n.Kind == "GATE" || len(dependents[n.ID]) > 0 {
				continue
			}
			add(n.ID, "warn:unreachable",
				"node %s (%s) is unreachable: nothing depends on it, directly or transitively; wire it into a later node or the gate, or delete it",
				n.ID, n.Kind)
		}
	}

	// 5. no_intent: nothing states why the run exists.
	if len(intents) == 0 {
		add("", "no_intent",
			"the plan has no INTENT node; add an INTENT node that states why the run exists and dep the first real node on it")
	}

	// 6. orphan_discovery: a discover whose work set is never expanded.
	fanOutTargets := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		if n.FanOut != "" {
			fanOutTargets[n.FanOut] = true
		}
	}
	for _, n := range nodes {
		if n.Discover == nil || fanOutTargets[n.ID] {
			continue
		}
		add(n.ID, "orphan_discovery",
			"node %s has discover but no node declares it as fan_out: %s; add fan_out: %q to the template node, or drop discover",
			n.ID, n.ID, n.ID)
	}

	// 7. fanout_no_template: expansion of something that discovers nothing.
	for _, n := range nodes {
		if n.FanOut == "" {
			continue
		}
		if target := byID[n.FanOut]; target != nil && target.Discover != nil {
			continue
		}
		add(n.ID, "fanout_no_template",
			"node %s has fan_out: %s but %s is not a node with discover; point fan_out at the discover node that produces the work set",
			n.ID, n.FanOut, n.FanOut)
	}

	// 8. fanin_no_expansion: a reduce over a fan-out that never happened.
	for _, n := range nodes {
		if n.FanIn == "" {
			continue
		}
		if target := byID[n.FanIn]; target != nil && target.Discover != nil {
			continue
		}
		add(n.ID, "fanin_no_expansion",
			"node %s has fan_in: %s but %s has no discover; point fan_in at the discover node that expanded the work set",
			n.ID, n.FanIn, n.FanIn)
	}

	// 9. warn:gate_soft: a gate that cannot fail.
	for _, n := range gates {
		if n.Check != "true" {
			continue
		}
		add(n.ID, "warn:gate_soft",
			"GATE %s has check \"true\", so it can never fail; replace it with the real predicate that proves the work",
			n.ID)
	}

	// 10. warn:estimate_defaulted: no plan-wide default, so every node guessed.
	if p.Defaults.Estimate == "" {
		for _, n := range nodes {
			if n.Estimate != "" {
				continue
			}
			add(n.ID, "warn:estimate_defaulted",
				"node %s has no estimate and defaults.estimate is empty; set defaults.estimate once, or set estimate on %s",
				n.ID, n.ID)
		}
	}

	return out
}

// semDependents maps a node id to the ids of nodes that depend on it directly.
// Transitive dependency closes over this map, so an empty entry means nothing
// reaches the node downstream.
func semDependents(byID map[string]*Node) map[string]map[string]bool {
	out := make(map[string]map[string]bool, len(byID))
	for id, n := range byID {
		for _, d := range n.Deps {
			if _, ok := byID[d]; !ok {
				continue
			}
			if out[d] == nil {
				out[d] = make(map[string]bool, 2)
			}
			out[d][id] = true
		}
	}
	return out
}

// semHasAncestorKind walks deps transitively and reports whether any ancestor
// has the given kind. A cycle is tolerated: ids already visited are not
// re-entered, so the walk always terminates.
func semHasAncestorKind(byID map[string]*Node, n *Node, kind string) bool {
	seen := make(map[string]bool, len(byID))
	var walk func(id string) bool
	walk = func(id string) bool {
		cur := byID[id]
		if cur == nil {
			return false
		}
		for _, d := range cur.Deps {
			if seen[d] {
				continue
			}
			seen[d] = true
			dep := byID[d]
			if dep == nil {
				continue
			}
			if dep.Kind == kind {
				return true
			}
			if walk(d) {
				return true
			}
		}
		return false
	}
	return walk(n.ID)
}
