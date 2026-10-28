package plan

import (
	"fmt"
	"strings"
	"time"
)

// Timing is the PERT result for one node. Durations are relative to run start.
type Timing struct {
	ID        string
	Earliest  time.Duration // earliest start
	EarliestF time.Duration // earliest finish
	Latest    time.Duration // latest start without delaying the run
	LatestF   time.Duration // latest finish
	Slack     time.Duration
	Critical  bool
}

// Schedule is the whole PERT result.
type Schedule struct {
	Timings   map[string]Timing
	Order     []string
	Critical  []string
	Total     time.Duration // critical-path length (single worker, no parallelism)
	Wallclock map[int]time.Duration
	Waves     [][]string
}

// Pert computes earliest/latest times, slack, the critical path, and the
// parallel "waves" implied by the dependency graph. Estimates are advisory:
// nothing here affects correctness, only the printed plan and `--workers` math.
func (p *Plan) Pert() *Schedule {
	order := p.Topo()
	idx := p.Index()
	s := &Schedule{Timings: make(map[string]Timing, len(order)), Order: order}

	// forward pass
	for _, id := range order {
		n := idx[id]
		if n == nil {
			continue
		}
		var start time.Duration
		for _, d := range n.Deps {
			if t, ok := s.Timings[d]; ok && t.EarliestF > start {
				start = t.EarliestF
			}
		}
		t := s.Timings[id]
		t.ID = id
		t.Earliest = start
		t.EarliestF = start + n.EstimateDuration()
		s.Timings[id] = t
	}

	var total time.Duration
	for _, t := range s.Timings {
		if t.EarliestF > total {
			total = t.EarliestF
		}
	}
	s.Total = total

	// backward pass
	for i := len(order) - 1; i >= 0; i-- {
		id := order[i]
		n := idx[id]
		if n == nil {
			continue
		}
		finish := total
		for _, other := range order {
			on := idx[other]
			if on == nil {
				continue
			}
			for _, d := range on.Deps {
				if d != id {
					continue
				}
				if t := s.Timings[other]; t.Latest < finish {
					finish = t.Latest
				}
			}
		}
		t := s.Timings[id]
		t.LatestF = finish
		t.Latest = finish - n.EstimateDuration()
		t.Slack = t.Latest - t.Earliest
		if t.Slack < 0 {
			t.Slack = 0
		}
		t.Critical = t.Slack == 0
		s.Timings[id] = t
	}

	for _, id := range order {
		if s.Timings[id].Critical {
			s.Critical = append(s.Critical, id)
		}
	}
	s.Waves = p.Waves()
	s.Wallclock = map[int]time.Duration{}
	for w := 1; w <= len(s.Waves)+1; w++ {
		s.Wallclock[w] = wallclock(s.Waves, s.Timings, idx, w)
	}
	return s
}

// Waves groups nodes by longest-path depth. Every node in a wave has all
// dependencies in earlier waves, so a wave is the natural parallel unit.
func (p *Plan) Waves() [][]string {
	idx := p.Index()
	depth := make(map[string]int, len(idx))
	var solve func(string) int
	solve = func(id string) int {
		if d, ok := depth[id]; ok {
			return d
		}
		n := idx[id]
		if n == nil {
			return 0
		}
		depth[id] = 0 // provisional, guards against cycles already rejected
		best := 0
		for _, dep := range n.Deps {
			if _, ok := idx[dep]; !ok {
				continue
			}
			if d := solve(dep) + 1; d > best {
				best = d
			}
		}
		depth[id] = best
		return best
	}
	for _, n := range p.Sorted() {
		solve(n.ID)
	}
	maxDepth := 0
	for _, d := range depth {
		if d > maxDepth {
			maxDepth = d
		}
	}
	waves := make([][]string, maxDepth+1)
	for _, n := range p.Sorted() {
		d := depth[n.ID]
		waves[d] = append(waves[d], n.ID)
	}
	return waves
}

// wallclock estimates makespan with n workers by simulating wave-by-wave work
// in longest-processing-time-first order. It is a lower-bound heuristic, not a
// proof, and is only used for the printed plan.
func wallclock(waves [][]string, t map[string]Timing, idx map[string]*Node, n int) time.Duration {
	if n < 1 {
		n = 1
	}
	free := make([]time.Duration, n)
	for _, wave := range waves {
		type item struct {
			dur time.Duration
		}
		items := make([]item, 0, len(wave))
		for _, id := range wave {
			node := idx[id]
			if node == nil {
				continue
			}
			items = append(items, item{dur: node.EstimateDuration()})
		}
		// longest first onto the earliest-free slot
		for i := 1; i < len(items); i++ {
			for j := i; j > 0 && items[j].dur > items[j-1].dur; j-- {
				items[j], items[j-1] = items[j-1], items[j]
			}
		}
		for _, it := range items {
			best := 0
			for i := range free {
				if free[i] < free[best] {
					best = i
				}
			}
			free[best] += it.dur
		}
	}
	var makespan time.Duration
	for _, f := range free {
		if f > makespan {
			makespan = f
		}
	}
	return makespan
}

// RenderPlan writes a human PERT/WBS summary.
func (p *Plan) RenderPlan(w *strings.Builder, workers int) {
	s := p.Pert()
	fmt.Fprintf(w, "run: %s   nodes: %d   waves: %d   critical path: %s\n",
		p.Run, len(p.Nodes), len(s.Waves), dur(s.Total))
	if workers > 0 {
		fmt.Fprintf(w, "estimated wall clock at %d worker(s): %s\n", workers, dur(s.Wallclock[min(workers, len(s.Waves)+1)]))
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "%-8s %-10s %-6s %-6s %-6s %-6s %s\n", "ID", "KIND", "EST", "ES", "SLACK", "WAVE", "TITLE")
	depth := map[string]int{}
	for i, wave := range s.Waves {
		for _, id := range wave {
			depth[id] = i
		}
	}
	for _, n := range p.Sorted() {
		t := s.Timings[n.ID]
		mark := " "
		if t.Critical {
			mark = "*"
		}
		fmt.Fprintf(w, "%s%-7s %-10s %-6s %-6s %-6s %-6d %s\n",
			mark, n.ID, n.Kind, dur(n.EstimateDuration()), dur(t.Earliest),
			dur(t.Slack), depth[n.ID], n.Title)
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "critical path: %s\n", strings.Join(s.Critical, " -> "))
}

func dur(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	h := int(d.Hours())
	m := int(d.Minutes()) - h*60
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh%dm", h, m)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
