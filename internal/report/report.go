// Package report renders a plan critique and a run review.
//
// Both documents exist for the same reason: a plan or a run is a claim, and a
// claim needs a second reader. Every statement here is derived from the plan,
// the state, or the trace. Where the information is missing, the document says
// so rather than filling the gap.
package report

import (
	"fmt"
	"sort"
	"strings"

	"github.com/KooshaPari/dagrun/internal/plan"
	"github.com/KooshaPari/dagrun/internal/store"
	"github.com/KooshaPari/dagrun/internal/trace"
)

// NodeRow is one node as the critique sees it.
type NodeRow struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	Deps      string `json:"deps"`
	Artifacts string `json:"artifacts"`
	Estimate  string `json:"estimate"`
}

// Critique is the assessment of a plan.
type Critique struct {
	Run          string     `json:"run"`
	Intent       string     `json:"intent,omitempty"`
	Nodes        []NodeRow  `json:"nodes"`
	Critical     []string   `json:"critical_path,omitempty"`
	Waves        [][]string `json:"waves,omitempty"`
	Gaps         []string   `json:"gaps,omitempty"`
	MissingGates []string   `json:"missing_gates,omitempty"`
	Risks        []string   `json:"risks,omitempty"`
	Edits        []string   `json:"suggested_edits,omitempty"`
}

// HappenedRow is one node as the review sees it.
type HappenedRow struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Status     string `json:"status"`
	Attempts   int    `json:"attempts"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	Artifacts  string `json:"artifacts,omitempty"`
}

// TraceSummary describes the execution record.
type TraceSummary struct {
	Events  int            `json:"events"`
	ByType  map[string]int `json:"by_type,omitempty"`
	WallMS  int64          `json:"wall_ms"`
	Missing bool           `json:"missing"`
}

// Review is the assessment of a run.
type Review struct {
	Run        string        `json:"run"`
	Verdict    string        `json:"verdict"`
	VerdictWhy string        `json:"verdict_why"`
	Happened   []HappenedRow `json:"happened"`
	Failures   []string      `json:"failures,omitempty"`
	Trace      TraceSummary  `json:"trace"`
	Hits       []string      `json:"hits,omitempty"`
	Next       []string      `json:"next,omitempty"`
}

// CritiquePlan assesses a plan.
func CritiquePlan(p *plan.Plan) Critique {
	c := Critique{Run: p.Run, Intent: p.Intent}
	idx := p.Index()

	for _, n := range p.Sorted() {
		row := NodeRow{
			ID: n.ID, Kind: n.Kind, Title: n.Title,
			Deps: strings.Join(n.Deps, ", "), Estimate: n.Estimate,
		}
		var paths []string
		for _, a := range n.Artifacts {
			paths = append(paths, a.Path)
		}
		row.Artifacts = strings.Join(paths, ", ")
		c.Nodes = append(c.Nodes, row)
	}

	sched := p.Pert()
	c.Critical = sched.Critical
	c.Waves = p.Waves()

	hasGate := false
	softGate := ""
	hasSpec := false
	hasTest := false
	for _, n := range p.Nodes {
		if n == nil {
			continue
		}
		switch n.Kind {
		case "GATE":
			hasGate = true
			if strings.TrimSpace(n.Check) == "true" {
				softGate = n.ID
			}
		case "SPEC":
			hasSpec = true
		case "TEST":
			hasTest = true
		}
	}

	for _, n := range p.Sorted() {
		if len(n.Artifacts) == 0 && n.Check == "" {
			c.Gaps = append(c.Gaps, fmt.Sprintf("%s (%s) declares neither an artifact nor a check, so nothing can prove it happened", n.ID, n.Kind))
		}
		for _, a := range n.Artifacts {
			if a.MinBytes == 0 {
				c.Gaps = append(c.Gaps, fmt.Sprintf("%s artifact %q has no min_bytes, so an empty file would satisfy it", n.ID, a.Path))
			}
		}
	}

	if !hasGate {
		c.MissingGates = append(c.MissingGates, "the plan has no GATE node, so nothing decides whether the work is finished")
	}
	if softGate != "" {
		c.MissingGates = append(c.MissingGates, fmt.Sprintf("GATE %s has check \"true\", which cannot fail", softGate))
	}

	for _, n := range p.Sorted() {
		if n.Kind != "IMPL" {
			continue
		}
		if !hasSpecAncestor(idx, n.ID) {
			c.Risks = append(c.Risks, fmt.Sprintf("IMPL %s has no SPEC ancestor, so nothing states the behaviour it implements", n.ID))
		}
	}
	if hasSpec && !hasTest {
		c.Risks = append(c.Risks, "the plan has a SPEC but no TEST, so the contract is never checked")
	}
	for _, n := range p.Sorted() {
		if n.FanOut == "" {
			continue
		}
		blob := ""
		for _, a := range n.Artifacts {
			blob += a.Path + " " + strings.Join(a.MustContain, " ")
		}
		if !strings.Contains(blob, "{{unit}}") && !strings.Contains(blob, "{{slug}}") {
			c.Risks = append(c.Risks, fmt.Sprintf("fan_out template %s has no {{unit}} or {{slug}} placeholder, so every child would target the same artifact", n.ID))
		}
	}

	for _, g := range c.Gaps {
		c.Edits = append(c.Edits, "fix: "+g)
	}
	for _, r := range c.Risks {
		c.Edits = append(c.Edits, "consider: "+r)
	}
	for _, m := range c.MissingGates {
		c.Edits = append(c.Edits, "fix: "+m)
	}
	return c
}

// hasSpecAncestor reports whether a SPEC is reachable through the deps.
func hasSpecAncestor(idx map[string]*plan.Node, id string) bool {
	seen := map[string]bool{}
	var walk func(string) bool
	walk = func(cur string) bool {
		n := idx[cur]
		if n == nil {
			return false
		}
		for _, d := range n.Deps {
			if seen[d] {
				continue
			}
			seen[d] = true
			dep := idx[d]
			if dep != nil && dep.Kind == "SPEC" {
				return true
			}
			if walk(d) {
				return true
			}
		}
		return false
	}
	return walk(id)
}

// ReviewRun assesses a finished or in-flight run.
func ReviewRun(p *plan.Plan, st *store.State, events []trace.Event) Review {
	r := Review{Run: p.Run}
	if st == nil {
		r.Verdict = "incomplete"
		r.VerdictWhy = "no run state was available"
		return r
	}

	done, failed, pending, running := 0, 0, 0, 0
	for _, n := range p.Sorted() {
		ns := st.Nodes[n.ID]
		if ns == nil {
			continue
		}
		row := HappenedRow{ID: n.ID, Kind: n.Kind, Status: ns.Status, Attempts: ns.Attempts, DurationMS: ns.DurationMS}
		if ns.Verify != nil {
			var ok []string
			for _, a := range ns.Verify.Artifacts {
				if a.OK {
					ok = append(ok, fmt.Sprintf("%s (%d bytes)", a.Path, a.Bytes))
				}
			}
			row.Artifacts = strings.Join(ok, ", ")
		}
		r.Happened = append(r.Happened, row)

		switch ns.Status {
		case plan.StatusDone:
			done++
			if ns.Attempts == 0 {
				r.Hits = append(r.Hits, n.ID)
			}
		case plan.StatusFailed:
			failed++
			r.Failures = append(r.Failures, fmt.Sprintf("%s (%s): %s", n.ID, n.Kind, ns.LastError))
		case plan.StatusRunning:
			running++
		default:
			pending++
		}
	}

	switch {
	case failed > 0:
		r.Verdict = "failed"
		r.VerdictWhy = fmt.Sprintf("%d node(s) failed", failed)
	case pending > 0 || running > 0:
		r.Verdict = "incomplete"
		r.VerdictWhy = fmt.Sprintf("%d done, %d pending, %d running", done, pending, running)
	case len(r.Happened) == 0:
		r.Verdict = "incomplete"
		r.VerdictWhy = "the run has no nodes"
	default:
		r.Verdict = "done"
		r.VerdictWhy = fmt.Sprintf("all %d node(s) finished", done)
		if len(r.Hits) > 0 {
			r.Verdict = "done-with-gaps"
			r.VerdictWhy = fmt.Sprintf("all %d node(s) finished, but %d were adopted rather than run", done, len(r.Hits))
		}
	}

	r.Trace = summarizeTrace(events)
	for _, n := range p.Sorted() {
		ns := st.Nodes[n.ID]
		if ns == nil {
			continue
		}
		if ns.Status == plan.StatusFailed || ns.Status == plan.StatusPending {
			r.Next = append(r.Next, fmt.Sprintf("%s (%s): %s", n.ID, n.Kind, firstNonEmpty(ns.LastError, "still pending")))
		}
	}
	return r
}

func summarizeTrace(events []trace.Event) TraceSummary {
	s := TraceSummary{ByType: map[string]int{}}
	if len(events) == 0 {
		s.Missing = true
		return s
	}
	s.Events = len(events)
	for _, e := range events {
		s.ByType[e.Event]++
	}
	first, last := events[0].TS, events[0].TS
	for _, e := range events {
		if e.TS.Before(first) {
			first = e.TS
		}
		if e.TS.After(last) {
			last = e.TS
		}
	}
	s.WallMS = last.Sub(first).Milliseconds()
	return s
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// RenderCritique writes the critique as markdown.
func RenderCritique(c Critique) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Critique: %s\n\n", c.Run)
	if c.Intent != "" {
		fmt.Fprintf(&b, "**Intent.** %s\n\n", strings.TrimSpace(c.Intent))
	}

	b.WriteString("## Nodes\n\n")
	b.WriteString("| ID | Kind | Title | Deps | Artifacts | Est |\n|---|---|---|---|---|---|\n")
	for _, n := range c.Nodes {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s |\n",
			n.ID, n.Kind, n.Title, n.Deps, n.Artifacts, n.Estimate)
	}
	b.WriteString("\n")

	if len(c.Critical) > 0 {
		fmt.Fprintf(&b, "## Critical path\n\n%s\n\n", strings.Join(c.Critical, " -> "))
	}
	if len(c.Waves) > 0 {
		b.WriteString("## Suggested waves\n\n")
		for i, w := range c.Waves {
			fmt.Fprintf(&b, "%d. %s\n", i+1, strings.Join(w, ", "))
		}
		b.WriteString("\n")
	}

	writeSection(&b, "Contract gaps", c.Gaps)
	writeSection(&b, "Missing gates", c.MissingGates)
	writeSection(&b, "Risks", c.Risks)
	if len(c.Edits) > 0 {
		b.WriteString("## Concrete next edits\n\n")
		for i, e := range c.Edits {
			fmt.Fprintf(&b, "%d. %s\n", i+1, e)
		}
		b.WriteString("\n")
	}
	if len(c.Gaps) == 0 && len(c.MissingGates) == 0 && len(c.Risks) == 0 {
		b.WriteString("## Verdict\n\nNo gaps found. The plan is runnable as written.\n")
	}
	return b.String()
}

// RenderReview writes the review as markdown.
func RenderReview(r Review) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Review: %s\n\n", r.Run)
	fmt.Fprintf(&b, "**Verdict: %s.** %s\n\n", r.Verdict, r.VerdictWhy)

	b.WriteString("## What actually happened\n\n")
	if len(r.Happened) == 0 {
		b.WriteString("No node state was recorded.\n\n")
	} else {
		b.WriteString("| Node | Kind | Status | Attempts | Duration | Artifacts verified |\n|---|---|---|---|---|---|\n")
		for _, h := range r.Happened {
			dur := "-"
			if h.DurationMS > 0 {
				dur = fmt.Sprintf("%dms", h.DurationMS)
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %d | %s | %s |\n",
				h.ID, h.Kind, h.Status, h.Attempts, dur, h.Artifacts)
		}
		b.WriteString("\n")
	}

	writeSection(&b, "The failures", r.Failures)

	b.WriteString("## The trace\n\n")
	if r.Trace.Missing {
		b.WriteString("**MISSING** — no trace events were recorded for this run, so the execution record is unknown.\n\n")
	} else {
		fmt.Fprintf(&b, "%d event(s) over %dms.\n\n", r.Trace.Events, r.Trace.WallMS)
		types := make([]string, 0, len(r.Trace.ByType))
		for t := range r.Trace.ByType {
			types = append(types, t)
		}
		sort.Strings(types)
		var parts []string
		for _, t := range types {
			parts = append(parts, fmt.Sprintf("%s %d", t, r.Trace.ByType[t]))
		}
		fmt.Fprintf(&b, "Types: %s\n\n", strings.Join(parts, ", "))
	}

	if len(r.Hits) > 0 {
		fmt.Fprintf(&b, "## The hits\n\nAdopted rather than run (the artifacts already satisfied the contract): %s\n\n",
			strings.Join(r.Hits, ", "))
	}
	writeSection(&b, "What to do next", r.Next)
	if len(r.Next) == 0 && r.Verdict == "done" {
		b.WriteString("## What to do next\n\nNothing outstanding.\n")
	}
	return b.String()
}

func writeSection(b *strings.Builder, title string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(b, "## %s\n\n", title)
	for _, i := range items {
		fmt.Fprintf(b, "- %s\n", i)
	}
	b.WriteString("\n")
}
