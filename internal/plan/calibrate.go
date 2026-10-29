package plan

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Estimate calibration.
//
// PERT numbers are only useful if they resemble reality. This file takes what
// actually happened on finished nodes and compares it to what was promised, so
// the next plan can start from a measured default instead of a guess.
//
// The core measurement is the ratio actual/estimate. A ratio above 1 means the
// estimate was too short: the node took longer than the plan claimed.

// Observation is one measured node execution.
type Observation struct {
	NodeID   string
	Kind     string
	Estimate time.Duration
	Actual   time.Duration
	Attempts int
	Adopted  bool
}

// KindCalibration summarises one kind of node (IMPL, RESEARCH, ...).
type KindCalibration struct {
	Kind         string
	Samples      int
	MedianActual time.Duration
	P95Actual    time.Duration
	MedianRatio  float64
}

// Calibration summarises how estimates compare to reality.
type Calibration struct {
	Samples     int
	ByKind      map[string]KindCalibration
	MedianRatio float64 // median(actual/estimate) across samples
	P95Ratio    float64
	Suggested   map[string]time.Duration // suggested default estimate per kind
}

// Calibrate computes the calibration from observations.
//
// An observation counts only when it measures real work: Estimate > 0,
// Actual > 0, and it was not adopted. An adopted node (Adopted == true) was
// taken over rather than executed, so it measures nothing and is excluded
// entirely, including from Samples.
func Calibrate(obs []Observation) Calibration {
	c := Calibration{
		ByKind:    make(map[string]KindCalibration),
		Suggested: make(map[string]time.Duration),
	}

	ratios := make([]float64, 0, len(obs))
	actualsByKind := make(map[string][]time.Duration)
	ratiosByKind := make(map[string][]float64)

	for _, o := range obs {
		if o.Adopted {
			continue // measures nothing
		}
		if o.Estimate <= 0 || o.Actual <= 0 {
			continue
		}
		c.Samples++
		r := float64(o.Actual) / float64(o.Estimate)
		ratios = append(ratios, r)
		actualsByKind[o.Kind] = append(actualsByKind[o.Kind], o.Actual)
		ratiosByKind[o.Kind] = append(ratiosByKind[o.Kind], r)
	}

	c.MedianRatio = median(ratios)
	c.P95Ratio = percentile(ratios, 95)

	for kind, actuals := range actualsByKind {
		kc := KindCalibration{
			Kind:         kind,
			Samples:      len(actuals),
			MedianActual: medianDurations(actuals),
			P95Actual:    percentileDurations(actuals, 95),
			MedianRatio:  median(ratiosByKind[kind]),
		}
		c.ByKind[kind] = kc
		// The number a planner should use next time: the observed median,
		// rounded to the nearest minute.
		c.Suggested[kind] = roundDuration(kc.MedianActual, time.Minute)
	}

	return c
}

// Drift renders a human summary of how far the estimates ran from reality.
func (c Calibration) Drift() string {
	if c.Samples == 0 {
		return "no measurements yet"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "n=%d  median actual/estimate %.1fx  p95 %.1fx",
		c.Samples, c.MedianRatio, c.P95Ratio)

	kinds := make([]string, 0, len(c.ByKind))
	for k := range c.ByKind {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)

	for _, k := range kinds {
		kc := c.ByKind[k]
		fmt.Fprintf(&b, "\n%-8s n=%d  median actual %s  suggested default %s",
			k, kc.Samples, shortDuration(kc.MedianActual), shortDuration(c.Suggested[k]))
		switch {
		case kc.MedianRatio > 1:
			fmt.Fprintf(&b, " (estimates run %.1fx short)", kc.MedianRatio)
		case kc.MedianRatio > 0 && kc.MedianRatio < 1:
			fmt.Fprintf(&b, " (estimates run %.1fx long)", 1/kc.MedianRatio)
		}
	}

	return b.String()
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	return percentileSorted(s, 50)
}

func percentile(v []float64, p float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	return percentileSorted(s, p)
}

// percentileSorted picks the nearest-rank percentile of an already sorted slice.
func percentileSorted(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(p/100*float64(len(sorted)) + 0.5)
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

func medianDurations(v []time.Duration) time.Duration {
	if len(v) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), v...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	rank := int(50.0/100*float64(len(s)) + 0.5)
	if rank < 1 {
		rank = 1
	}
	if rank > len(s) {
		rank = len(s)
	}
	return s[rank-1]
}

func percentileDurations(v []time.Duration, p float64) time.Duration {
	if len(v) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), v...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	rank := int(p/100*float64(len(s)) + 0.5)
	if rank < 1 {
		rank = 1
	}
	if rank > len(s) {
		rank = len(s)
	}
	return s[rank-1]
}

// roundDuration rounds d to the nearest unit.
func roundDuration(d, unit time.Duration) time.Duration {
	if unit <= 0 {
		return d
	}
	if d < 0 {
		return -roundDuration(-d, unit)
	}
	return ((d + unit/2) / unit) * unit
}

// shortDuration renders a duration compactly: 22m, 1h30m, 45s.
func shortDuration(d time.Duration) string {
	if d <= 0 {
		return "0m"
	}
	if d < time.Minute {
		return d.Round(time.Second).String()
	}
	return d.Round(time.Minute).String()
}
