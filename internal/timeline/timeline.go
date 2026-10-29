// Package timeline turns merged trace events into a human-readable view.
//
// The trace is the source of truth; the timeline is a projection of it. It
// keeps the original event so nothing is lost, and adds a single derived
// field: elapsed milliseconds from the first event in the run.
package timeline

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/KooshaPari/dagrun/internal/trace"
)

// Event is one trace event with its elapsed time from the run start.
type Event struct {
	trace.Event
	ElapsedMS int64 `json:"elapsed_ms"`
}

// Summary describes the run as a whole.
type Summary struct {
	Start        time.Time
	End          time.Time
	WallMS       int64
	ByType       map[string]int
	ByNode       map[string]int
	BusiestNode  string
	BusiestCount int
}

// Build turns merged trace events into a timeline.
//
// Events are ordered by timestamp (ties keep input order) and stamped with
// their elapsed milliseconds relative to the earliest event. The summary is
// computed from the same pass so the two can never disagree.
func Build(events []trace.Event) ([]Event, Summary) {
	s := Summary{
		ByType: map[string]int{},
		ByNode: map[string]int{},
	}
	if len(events) == 0 {
		return nil, s
	}

	sorted := make([]trace.Event, len(events))
	copy(sorted, events)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].TS.Before(sorted[j].TS)
	})

	s.Start = sorted[0].TS
	s.End = sorted[0].TS
	for _, e := range sorted {
		if e.TS.Before(s.Start) {
			s.Start = e.TS
		}
		if e.TS.After(s.End) {
			s.End = e.TS
		}
	}
	s.WallMS = s.End.Sub(s.Start).Milliseconds()

	out := make([]Event, 0, len(sorted))
	for _, e := range sorted {
		s.ByType[e.Event]++
		if e.Node != "" {
			s.ByNode[e.Node]++
		}
		out = append(out, Event{
			Event:     e,
			ElapsedMS: e.TS.Sub(s.Start).Milliseconds(),
		})
	}

	nodes := make([]string, 0, len(s.ByNode))
	for n := range s.ByNode {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)
	for _, n := range nodes {
		if s.ByNode[n] > s.BusiestCount {
			s.BusiestNode = n
			s.BusiestCount = s.ByNode[n]
		}
	}

	return out, s
}

// Render writes the human-readable timeline.
func Render(events []Event, s Summary) string {
	if len(events) == 0 {
		return "no trace events recorded"
	}

	var b strings.Builder

	workers := map[string]int{}
	for _, e := range events {
		if e.Worker != "" {
			workers[e.Worker]++
		}
	}

	fmt.Fprintf(&b, "run timeline: %d events over %.1fs", len(events), float64(s.WallMS)/1000)
	if len(workers) > 0 {
		b.WriteString(" (")
		b.WriteString(joinCounts(workers))
		b.WriteString(")")
	}
	b.WriteString("\n")

	for _, e := range events {
		// The embedded trace.Event has its own Event field, which the outer
		// embedded field name shadows; name the record explicitly.
		ev := e.Event
		fmt.Fprintf(&b, "  %-7s %-7s %-14s %s", formatElapsed(e.ElapsedMS), ev.Node, ev.Event, ev.Worker)
		if ev.DurMS != 0 {
			fmt.Fprintf(&b, "  (%dms)", ev.DurMS)
		}
		b.WriteString("\n")
	}

	if len(s.ByType) > 0 {
		fmt.Fprintf(&b, "  types: %s\n", joinCounts(s.ByType))
	}
	if s.BusiestNode != "" {
		fmt.Fprintf(&b, "  busiest node: %s (%d events)\n", s.BusiestNode, s.BusiestCount)
	}

	return strings.TrimRight(b.String(), "\n")
}

// formatElapsed renders milliseconds as "+1.2s".
func formatElapsed(ms int64) string {
	return fmt.Sprintf("+%.1fs", float64(ms)/1000)
}

// joinCounts renders "<key> <n>, <key> <n>", most frequent first with ties
// broken by key so output is stable.
func joinCounts(counts map[string]int) string {
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i] < keys[j]
	})

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", k, counts[k]))
	}
	return strings.Join(parts, ", ")
}
