// Package export renders a finished or in-flight run into a format another
// system can ingest. Nothing here mutates state, so export is always safe.
//
// Two targets are implemented:
//   - agileplus: the ImportBundle JSON shape that crates/agileplus-import
//     already parses (natural keys only: slug/sequence, never integer ids).
//   - tracera: a flat nodes/edges/artifacts document that maps onto Tracera's
//     relational tier (swee node_type=task, swee edge_type=depends_on,
//     evidence rows), since its OTel span table cannot hold a multi-parent DAG.
package export

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/KooshaPari/dagrun/internal/plan"
	"github.com/KooshaPari/dagrun/internal/store"
	"github.com/KooshaPari/dagrun/internal/trace"
)

// Input is everything export may read.
type Input struct {
	Plan   *plan.Plan
	State  *store.State
	Events []trace.Event
}

// JSON renders the raw run document.
func JSON(in Input) ([]byte, error) {
	doc := map[string]any{
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"plan":         in.Plan,
		"state":        in.State,
		"trace":        in.Events,
	}
	return json.MarshalIndent(doc, "", "  ")
}

// ---------------------------------------------------------------- agileplus

type apBundle struct {
	Projects []apProject `json:"projects"`
	Modules  []any       `json:"modules"`
	Features []any       `json:"features"`
	Cycles   []any       `json:"cycles"`
}

type apProject struct {
	Slug     string   `json:"slug"`
	Name     string   `json:"name"`
	Features []apFeat `json:"features"`
}

type apFeat struct {
	FriendlyName string   `json:"friendly_name"`
	Slug         string   `json:"slug"`
	SpecContent  string   `json:"spec_content"`
	State        string   `json:"state"`
	TargetBranch string   `json:"target_branch,omitempty"`
	Labels       []string `json:"labels,omitempty"`
	WorkPackages []apWP   `json:"work_packages"`
}

type apWP struct {
	Sequence           int      `json:"sequence"`
	Title              string   `json:"title"`
	State              string   `json:"state"`
	AcceptanceCriteria string   `json:"acceptance_criteria,omitempty"`
	FileScope          []string `json:"file_scope,omitempty"`
	DependsOnSequences []int    `json:"depends_on_sequences,omitempty"`
}

// nugatory kinds carry documentation, not deliverables
var narrativeKinds = map[string]bool{
	"INTENT": true, "SPEC": true, "PLAN": true, "RESEARCH": true,
	"SOTA": true, "DOC": true, "CONSTITUTION": true,
}

// AgilePlus renders an ImportBundle. Only natural keys are emitted: features
// are keyed by slug, work packages by (feature, sequence).
func AgilePlus(in Input) ([]byte, error) {
	runSlug := slugify(in.Plan.Run)
	if runSlug == "" {
		runSlug = "dagrun"
	}
	feat := apFeat{
		FriendlyName: in.Plan.Run,
		Slug:         runSlug,
		State:        "doing",
		Labels:       []string{"dagrun"},
	}

	// narrative artifacts become the feature spec
	var spec strings.Builder
	fmt.Fprintf(&spec, "# Specification: %s\n\n", in.Plan.Run)
	if in.Plan.Intent != "" {
		spec.WriteString("## Problem Statement\n\n")
		spec.WriteString(strings.TrimSpace(in.Plan.Intent))
		spec.WriteString("\n\n")
	}

	seq := map[string]int{}
	next := 1
	for _, n := range in.Plan.Sorted() {
		if narrativeKinds[n.Kind] {
			spec.WriteString(narrativeBlock(in, n))
			continue
		}
		seq[n.ID] = next
		next++
	}

	for _, n := range in.Plan.Sorted() {
		s, ok := seq[n.ID]
		if !ok {
			continue
		}
		wp := apWP{Sequence: s, Title: n.Title, State: apState(status(in, n.ID))}
		for _, a := range n.Artifacts {
			wp.FileScope = append(wp.FileScope, a.Path)
		}
		if n.Check != "" {
			wp.AcceptanceCriteria = n.Check
		}
		for _, d := range n.Deps {
			if ds, ok := seq[d]; ok {
				wp.DependsOnSequences = append(wp.DependsOnSequences, ds)
			}
		}
		feat.WorkPackages = append(feat.WorkPackages, wp)
	}

	if allDone(in) {
		feat.State = "done"
	}
	feat.SpecContent = spec.String()

	bundle := apBundle{
		Projects: []apProject{{
			Slug:     runSlug,
			Name:     in.Plan.Run,
			Features: []apFeat{feat},
		}},
		Modules:  []any{},
		Features: []any{},
		Cycles:   []any{},
	}
	return json.MarshalIndent(bundle, "", "  ")
}

func narrativeBlock(in Input, n *plan.Node) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## %s — %s\n\nStatus: %s\n\n", n.Kind, n.Title, status(in, n.ID))
	if ns := in.State.Nodes[n.ID]; ns != nil && ns.Verify != nil {
		for _, a := range ns.Verify.Artifacts {
			if a.OK {
				fmt.Fprintf(&b, "- artifact `%s` (%d bytes, sha256 %s)\n", a.Path, a.Bytes, shortHash(a.SHA256))
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

func apState(s string) string {
	switch s {
	case plan.StatusDone:
		return "done"
	case plan.StatusRunning:
		return "doing"
	case plan.StatusFailed:
		return "blocked"
	case plan.StatusSkipped, plan.StatusCancelled:
		return "done"
	}
	return "planned"
}

// ------------------------------------------------------------------ tracera

type trDoc struct {
	Project     string        `json:"project"`
	Run         string        `json:"run"`
	GeneratedAt string        `json:"generated_at"`
	Nodes       []trNode      `json:"nodes"`
	Edges       []trEdge      `json:"edges"`
	Artifacts   []trArtifact  `json:"artifacts"`
	Events      []trace.Event `json:"events,omitempty"`
}

type trNode struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Kind       string `json:"kind"`
	Label      string `json:"label"`
	Status     string `json:"status"`
	Estimate   string `json:"estimate,omitempty"`
	Role       string `json:"role,omitempty"`
	Attempts   int    `json:"attempts,omitempty"`
	LastError  string `json:"last_error,omitempty"`
	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
}

type trEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Type string `json:"type"`
}

type trArtifact struct {
	Node     string `json:"node"`
	Path     string `json:"path"`
	SHA256   string `json:"sha256,omitempty"`
	Bytes    int64  `json:"bytes,omitempty"`
	Verified bool   `json:"verified"`
	Detail   string `json:"detail,omitempty"`
}

// Tracera renders the relational-tier view: one task node per DAG node,
// depends_on edges, and one evidence row per verified artifact.
func Tracera(in Input) ([]byte, error) {
	doc := trDoc{
		Project:     in.Plan.Run,
		Run:         in.Plan.Run,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Events:      in.Events,
	}
	for _, n := range in.Plan.Sorted() {
		ns := in.State.Nodes[n.ID]
		node := trNode{
			ID: n.ID, Type: "task", Kind: n.Kind, Label: n.Title,
			Status: trStatus(status(in, n.ID)), Estimate: n.Estimate, Role: n.Role,
		}
		if ns != nil {
			node.Attempts = ns.Attempts
			node.LastError = ns.LastError
			if !ns.StartedAt.IsZero() {
				node.StartedAt = ns.StartedAt.UTC().Format(time.RFC3339)
			}
			if !ns.FinishedAt.IsZero() {
				node.FinishedAt = ns.FinishedAt.UTC().Format(time.RFC3339)
			}
			if ns.Verify != nil {
				for _, a := range ns.Verify.Artifacts {
					doc.Artifacts = append(doc.Artifacts, trArtifact{
						Node: n.ID, Path: a.Path, SHA256: a.SHA256, Bytes: a.Bytes,
						Verified: a.OK, Detail: strings.TrimSpace(a.Code + " " + a.Detail),
					})
				}
			}
		}
		doc.Nodes = append(doc.Nodes, node)
		for _, d := range n.Deps {
			doc.Edges = append(doc.Edges, trEdge{From: d, To: n.ID, Type: "depends_on"})
		}
	}
	sort.SliceStable(doc.Edges, func(i, j int) bool {
		if doc.Edges[i].From != doc.Edges[j].From {
			return plan.Less(doc.Edges[i].From, doc.Edges[j].From)
		}
		return plan.Less(doc.Edges[i].To, doc.Edges[j].To)
	})
	return json.MarshalIndent(doc, "", "  ")
}

func trStatus(s string) string {
	switch s {
	case plan.StatusDone:
		return "Done"
	case plan.StatusRunning:
		return "InProgress"
	case plan.StatusFailed:
		return "Blocked"
	case plan.StatusSkipped, plan.StatusCancelled:
		return "Cancelled"
	}
	return "Ready"
}

// ------------------------------------------------------------------ helpers

func status(in Input, id string) string {
	if ns := in.State.Nodes[id]; ns != nil {
		return ns.Status
	}
	return plan.StatusPending
}

func allDone(in Input) bool {
	for _, n := range in.Plan.Nodes {
		if n == nil {
			continue
		}
		st := status(in, n.ID)
		if st != plan.StatusDone && st != plan.StatusSkipped {
			return false
		}
	}
	return true
}

func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

func slugify(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}
