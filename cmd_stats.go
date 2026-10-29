package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/KooshaPari/dagrun/internal/plan"
	"github.com/KooshaPari/dagrun/internal/store"
)

// kindStat is one row of the per-kind statistics table.
type kindStat struct {
	Kind     string `json:"kind"`
	Nodes    int    `json:"nodes"`
	Done     int    `json:"done"`
	Failed   int    `json:"failed"`
	Retried  int    `json:"retried"`
	Adopted  int    `json:"adopted"`
	MedianMS int64  `json:"median_ms,omitempty"`
	P95MS    int64  `json:"p95_ms,omitempty"`
}

// cmdStats prints per-kind duration, adoption and retry statistics.
//
// Tokens are only counted when a harness actually reported usage; when none
// did, the command says so rather than printing a zero that reads as "free".
func cmdStats(args []string) error {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dir := fs.String("dir", defaultDir, "run directory")
	jsonOut := fs.Bool("json", false, "machine-readable output on stdout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	_, st, _, err := openRun(*dir)
	if err != nil {
		return err
	}
	snap, err := st.Snapshot()
	if err != nil {
		return err
	}
	p := st.Plan()

	kinds := map[string]*kindStat{}
	var durs []int64
	noDuration := 0
	tokensIn, tokensOut, tokensTotal := 0, 0, 0
	usageReported := 0

	for _, n := range p.Sorted() {
		ns := snap.Nodes[n.ID]
		if ns == nil {
			continue
		}
		k := kinds[n.Kind]
		if k == nil {
			k = &kindStat{Kind: n.Kind}
			kinds[n.Kind] = k
		}
		k.Nodes++
		switch ns.Status {
		case plan.StatusDone:
			k.Done++
		case plan.StatusFailed:
			k.Failed++
		}
		if ns.Attempts > 1 {
			k.Retried++
		}
		if ns.Status == plan.StatusDone && ns.Attempts == 0 {
			k.Adopted++
		}
		if ns.DurationMS > 0 {
			durs = append(durs, ns.DurationMS)
		} else {
			noDuration++
		}
		if ns.Usage != nil {
			usageReported++
			tokensIn += ns.Usage.InputTokens
			tokensOut += ns.Usage.OutputTokens
			tokensTotal += ns.Usage.Total()
		}
	}

	names := make([]string, 0, len(kinds))
	for name := range kinds {
		names = append(names, name)
	}
	sort.Strings(names)

	byKind := make([]kindStat, 0, len(names))
	for _, name := range names {
		k := kinds[name]
		var kd []int64
		for _, n := range p.Sorted() {
			if n.Kind != name {
				continue
			}
			if ns := snap.Nodes[n.ID]; ns != nil && ns.DurationMS > 0 {
				kd = append(kd, ns.DurationMS)
			}
		}
		k.MedianMS = percentile(kd, 0.5)
		k.P95MS = percentile(kd, 0.95)
		byKind = append(byKind, *k)
	}

	totalNodes := 0
	for _, k := range byKind {
		totalNodes += k.Nodes
	}

	if *jsonOut {
		out := map[string]any{
			"run": p.Run, "kinds": byKind, "nodes": totalNodes,
			"duration_measured": len(durs), "duration_missing": noDuration,
			"usage_reported_nodes": usageReported,
		}
		if usageReported > 0 {
			out["tokens_input"] = tokensIn
			out["tokens_output"] = tokensOut
			out["tokens_total"] = tokensTotal
		} else {
			out["tokens"] = "not reported by any harness"
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	fmt.Printf("run %s — %d node(s)\n\n", p.Run, totalNodes)
	fmt.Printf("%-11s %5s %6s %7s %8s %9s %9s\n", "KIND", "NODES", "DONE", "FAILED", "RETRIED", "ADOPTED", "MEDIAN")
	for _, k := range byKind {
		fmt.Printf("%-11s %5d %6d %7d %8d %9d %9s\n",
			k.Kind, k.Nodes, k.Done, k.Failed, k.Retried, k.Adopted,
			msOrDash(k.MedianMS))
	}
	fmt.Printf("\ntotal node time: %s (measured on %d node(s), %d without a recorded duration)\n",
		msOrDash(int64(sumAll(durs))), len(durs), noDuration)

	// Estimate calibration: how far the plan's estimates are from reality.
	var obs []plan.Observation
	for _, n := range p.Sorted() {
		ns := snap.Nodes[n.ID]
		if ns == nil || ns.DurationMS <= 0 {
			continue
		}
		obs = append(obs, plan.Observation{
			NodeID:   n.ID,
			Kind:     n.Kind,
			Estimate: n.EstimateDuration(),
			Actual:   time.Duration(ns.DurationMS) * time.Millisecond,
			Attempts: ns.Attempts,
			Adopted:  ns.Attempts == 0,
		})
	}
	if len(obs) > 0 {
		cal := plan.Calibrate(obs)
		fmt.Printf("\nestimate calibration\n%s\n", cal.Drift())
	}

	// The budget.
	if p.MaxCostUSD > 0 {
		fmt.Printf("\nbudget: $%.2f of $%.2f spent\n", tokensToUSD(tokensIn, tokensOut, tokensTotal, p.Run), p.MaxCostUSD)
	}
	if usageReported > 0 {
		fmt.Printf("tokens: %d in, %d out, %d total (reported by %d node(s))\n",
			tokensIn, tokensOut, tokensTotal, usageReported)
		if done := doneCount(byKind); done > 0 {
			fmt.Printf("tokens per done node: %d\n", tokensTotal/done)
		}
	} else {
		fmt.Println("tokens: not reported by any harness")
	}
	return nil
}

func doneCount(kinds []kindStat) int {
	n := 0
	for _, k := range kinds {
		n += k.Done
	}
	return n
}

func sumAll(v []int64) int64 {
	var s int64
	for _, x := range v {
		s += x
	}
	return s
}

// percentile returns the p-quantile of a sorted-or-unsorted slice; -1 when empty.
func percentile(v []int64, q float64) int64 {
	if len(v) == 0 {
		return -1
	}
	s := append([]int64(nil), v...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	idx := int(q * float64(len(s)-1))
	if idx < 0 {
		idx = 0
	}
	return s[idx]
}

func msOrDash(ms int64) string {
	if ms < 0 {
		return "-"
	}
	return fmt.Sprintf("%dms", ms)
}

// tokensToUSD estimates spend from tokens using the default rate table.
func tokensToUSD(in, out, total int, run string) float64 {
	rate, ok := plan.DefaultRates[run]
	if !ok {
		rate = plan.Rate{InputPerMillion: 3, OutputPerMillion: 15}
	}
	return plan.Cost(plan.Spend{InputTokens: in, OutputTokens: out}, rate)
}

var _ = filepath.Join
var _ = store.StateVersion
