package plan

import (
	"bufio"
	"bytes"
	"strings"
)

// CVP awareness: answer "what should I do next?" in terms of the checkpoint
// (the CVP gate) rather than the tree.
//
// A checkpoint is closed by one node: the CVP gate. By default that is the last
// GATE node in topological order. A plan may name it explicitly with a
// top-level `cvp_gate: <node id>` key; that key is read from the plan *text*
// (see CVPGateFromYAML / ResolveCVPGate) rather than from a Plan field, because
// a single optional string does not justify widening the plan schema. The plan
// file stays the source of truth.
//
// Direction convention: edges point from a node to the nodes it depends on
// (Node.Deps). "Ancestors of X" therefore means "the work X depends on", and
// the gate's ancestors are exactly the nodes that must finish before the
// checkpoint closes.

// CVPGate returns the id of the node that closes the checkpoint: the last GATE
// node in topological order. It returns "" when the plan declares no GATE node.
//
// An explicit `cvp_gate:` key in the plan file overrides this; callers holding
// the plan text should use ResolveCVPGate instead.
func CVPGate(p *Plan) string {
	if p == nil {
		return ""
	}
	gate := ""
	for _, id := range p.Topo() {
		if n := p.ByID(id); n != nil && n.IsGate() {
			gate = id
		}
	}
	return gate
}

// CVPGateFromYAML scans plan text for a top-level `cvp_gate: <id>` key and
// returns the value. ok is false when the key is absent or empty. This is a
// deliberate raw scan, not a struct field: the key is optional, single-valued,
// and unknown YAML keys are already tolerated by Parse.
func CVPGateFromYAML(raw []byte) (string, bool) {
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), " \t\r")
		if !strings.HasPrefix(line, "cvp_gate:") {
			continue
		}
		v := strings.TrimSpace(strings.TrimPrefix(line, "cvp_gate:"))
		if i := strings.Index(v, " #"); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
		v = strings.Trim(v, `"'`)
		if v == "" {
			return "", false
		}
		return v, true
	}
	return "", false
}

// ResolveCVPGate returns the effective CVP gate: an explicit `cvp_gate:` key in
// raw wins, otherwise the last GATE node in topological order. raw may be nil.
func ResolveCVPGate(p *Plan, raw []byte) string {
	if len(raw) > 0 {
		if id, ok := CVPGateFromYAML(raw); ok && p != nil {
			// Only honour it when it names a node that exists; a typo must not
			// silently retarget the checkpoint.
			if p.ByID(id) != nil {
				return id
			}
		}
	}
	return CVPGate(p)
}

// DistanceTo returns the fewest dependency hops from `from` to `target`,
// following Node.Deps (from -> what from depends on). It returns 0 when
// from == target, and -1 when target is unreachable or either id is unknown.
func DistanceTo(p *Plan, from, target string) int {
	if p == nil || from == "" || target == "" {
		return -1
	}
	idx := p.Index()
	if idx[from] == nil || idx[target] == nil {
		return -1
	}
	if from == target {
		return 0
	}
	dist := map[string]int{from: 0}
	queue := []string{from}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		n := idx[cur]
		if n == nil {
			continue
		}
		for _, d := range n.Deps {
			if _, seen := dist[d]; seen {
				continue
			}
			if idx[d] == nil {
				continue
			}
			dist[d] = dist[cur] + 1
			if d == target {
				return dist[d]
			}
			queue = append(queue, d)
		}
	}
	return -1
}

// OnPathTo returns every transitive ancestor of target (everything target
// depends on, directly or indirectly). target itself is not included.
func OnPathTo(p *Plan, target string) map[string]bool {
	out := map[string]bool{}
	if p == nil || target == "" {
		return out
	}
	idx := p.Index()
	if idx[target] == nil {
		return out
	}
	stack := []string{target}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		n := idx[cur]
		if n == nil {
			continue
		}
		for _, d := range n.Deps {
			if out[d] || idx[d] == nil {
				continue
			}
			out[d] = true
			stack = append(stack, d)
		}
	}
	return out
}

// CVPDistance returns the hop count from gate down to a node (how far the node
// sits from the checkpoint along dependency edges) and whether the node is on
// the checkpoint's critical work set at all. Unrelated or downstream nodes
// report -1/false.
func CVPDistance(p *Plan, gate, node string) (int, bool) {
	if gate == "" || node == "" {
		return -1, false
	}
	if !OnPathTo(p, gate)[node] {
		return -1, false
	}
	return DistanceTo(p, gate, node), true
}
