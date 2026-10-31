// Package tracera ... (import direction)
package tracera

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/KooshaPari/dagrun/internal/plan"
)

// ImportResult is the outcome of reversing a Tracera payload back into a plan.
//
// Import is a STRUCTURAL reconstruction only: it rebuilds the DAG shape (nodes,
// dependency edges, declared artifacts) and deliberately fabricates no run state.
// Status, attempts, last_error, started/finished timestamps and verification
// reports are all per-run execution data that a freshly imported plan does not
// have, so they are ignored even when present on the payload items.
type ImportResult struct {
	Plan    *plan.Plan
	Dropped []string
}

// Import reconstructs a plan from a Tracera payload.
//
// Rules:
//   - node:<id> becomes a plan node. Its Type maps to a plan kind only when
//     present in plan.Kinds; otherwise the node keeps the safe "OTHER" kind and
//     the item is recorded in Dropped.
//   - edge:<from>-><to> means "to" depends on "from" (From is the dependency,
//     To is the dependent). An edge naming an unknown node is Dropped and never
//     silently ignored.
//   - artifact:<node>/<path> (or the colon form emitted by BuildPayload) becomes
//     an Artifact on that node. A missing owner is Dropped.
//   - The result is validated with plan.Plan.Validate; any issue is returned as
//     an error alongside the (possibly partial) result.
func Import(payload Payload) (ImportResult, error) {
	dropped := []string{}
	p := &plan.Plan{
		Version:       plan.SchemaVersion,
		Run:           "",
		ArtifactsRoot: "artifacts",
		MaxParallel:   4,
		MaxDynamic:    200,
		Nodes:         []*plan.Node{},
	}

	nodeIndex := map[string]*plan.Node{}
	nodeOrder := []string{}

	// First pass: nodes. Doing nodes first lets edge/artifact passes resolve
	// owners without depending on item ordering in the payload.
	for _, it := range payload.Items {
		if it.Kind != "node" {
			continue
		}
		id := strings.TrimPrefix(it.ID, "node:")
		if id == "" {
			id = it.Title
		}
		if id == "" {
			dropped = append(dropped, it.ID+": empty node id")
			continue
		}
		kind := it.Type
		if !plan.Kinds[kind] {
			if kind == "" {
				kind = "OTHER"
			} else {
				dropped = append(dropped, it.ID+": unknown kind "+kind)
				kind = "OTHER"
			}
		}
		n := &plan.Node{
			ID:       id,
			Kind:     kind,
			Title:    it.Title,
			Estimate: it.Estimate,
			Role:     it.Role,
			Timeout:  "30m",
			Deps:     []string{},
			Artifacts: []plan.Artifact{},
		}
		if n.Title == "" {
			n.Title = id
		}
		p.Nodes = append(p.Nodes, n)
		nodeIndex[id] = n
		nodeOrder = append(nodeOrder, id)
	}

	// Second pass: edges. from -> to means to depends on from.
	for _, it := range payload.Items {
		if it.Kind != "edge" {
			continue
		}
		from, to := it.From, it.To
		if from == "" || to == "" {
			pf, pt := parseEdgeID(it.ID)
			if from == "" {
				from = pf
			}
			if to == "" {
				to = pt
			}
		}
		if from == "" || to == "" {
			dropped = append(dropped, it.ID+": malformed edge")
			continue
		}
		if _, ok := nodeIndex[from]; !ok {
			dropped = append(dropped, it.ID+": unknown node "+from)
			continue
		}
		if _, ok := nodeIndex[to]; !ok {
			dropped = append(dropped, it.ID+": unknown node "+to)
			continue
		}
		target := nodeIndex[to]
		if !contains(target.Deps, from) {
			target.Deps = append(target.Deps, from)
		}
	}

	// Third pass: artifacts. artifact:<node>/<path>.
	for _, it := range payload.Items {
		if it.Kind != "artifact" {
			continue
		}
		owner, path := it.From, it.Path
		if owner == "" || path == "" {
			o, pp := parseArtifactID(it.ID)
			if owner == "" {
				owner = o
			}
			if path == "" {
				path = pp
			}
		}
		if owner == "" {
			dropped = append(dropped, it.ID+": missing owner")
			continue
		}
		if _, ok := nodeIndex[owner]; !ok {
			dropped = append(dropped, it.ID+": unknown node "+owner)
			continue
		}
		if path == "" {
			path = it.Title
		}
		if path == "" {
			dropped = append(dropped, it.ID+": missing path")
			continue
		}
		nodeIndex[owner].Artifacts = append(nodeIndex[owner].Artifacts, plan.Artifact{Path: path})
	}

	// Ensure verifiability so the imported plan passes Validate without
	// fabricating run state. Nodes that carry no artifacts and no check become
	// optional (or a trivially-true check for gates, which require one).
	for _, n := range p.Nodes {
		if len(n.Artifacts) == 0 && n.Check == "" {
			if n.Kind == "GATE" {
				n.Check = "true"
			} else {
				n.Optional = true
			}
		}
	}
	// Empty artifact slices -> nil for cleanliness? Keep as built; normalize would set defaults.
	// Remove empty Deps/Artifacts slices to match plan style.
	for _, n := range p.Nodes {
		if len(n.Deps) == 0 {
			n.Deps = nil
		}
		if len(n.Artifacts) == 0 {
			n.Artifacts = nil
		}
	}

	if p.Run == "" {
		p.Run = "run"
	}

	res := ImportResult{Plan: p, Dropped: dropped}
	if dropped == nil {
		res.Dropped = []string{}
	}
	issues := p.Validate()
	if len(issues) > 0 {
		msgs := make([]string, 0, len(issues))
		for _, is := range issues {
			msgs = append(msgs, is.Code+": "+is.Detail)
		}
		return res, fmt.Errorf("tracera: imported plan invalid: %s", strings.Join(msgs, "; "))
	}
	return res, nil
}

// ImportJSON accepts either {"items":[...]} or a bare [...].
func ImportJSON(data []byte) (ImportResult, error) {
	trimmed := bytes.TrimSpace(data)
	// Object form.
	var obj struct {
		Items []Item `json:"items"`
	}
	if err := json.Unmarshal(trimmed, &obj); err == nil && obj.Items != nil {
		return Import(Payload{Items: obj.Items})
	}
	// Bare array form.
	var items []Item
	if err := json.Unmarshal(trimmed, &items); err == nil {
		return Import(Payload{Items: items})
	}
	// Full payload form.
	var pl Payload
	if err := json.Unmarshal(trimmed, &pl); err == nil {
		return Import(pl)
	}
	// Return the last error.
	var e error
	if err := json.Unmarshal(trimmed, &pl); err != nil {
		e = err
	}
	if e == nil {
		e = fmt.Errorf("tracera: invalid import JSON")
	}
	return ImportResult{}, e
}

func parseEdgeID(id string) (string, string) {
	s := strings.TrimPrefix(id, "edge:")
	if i := strings.Index(s, "->"); i >= 0 {
		return s[:i], s[i+2:]
	}
	return "", ""
}

func parseArtifactID(id string) (string, string) {
	s := strings.TrimPrefix(id, "artifact:")
	if i := strings.Index(s, ":"); i >= 0 {
		return s[:i], s[i+1:]
	}
	if i := strings.Index(s, "/"); i >= 0 {
		return s[:i], s[i+1:]
	}
	return "", s
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
