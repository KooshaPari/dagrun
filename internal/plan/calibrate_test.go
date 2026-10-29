package plan

import (
	"math"
	"strings"
	"testing"
	"time"
)

func approxRatio(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestCalibrateKnownSet(t *testing.T) {
	obs := []Observation{
		// IMPL: ratios 2.0, 1.5, 2.2, 1.6 -> sorted 1.5,1.6,2.0,2.2; median 1.6, p95 2.2
		{NodeID: "1", Kind: "IMPL", Estimate: 10 * time.Minute, Actual: 20 * time.Minute},
		{NodeID: "2", Kind: "IMPL", Estimate: 20 * time.Minute, Actual: 30 * time.Minute},
		{NodeID: "3", Kind: "IMPL", Estimate: 10 * time.Minute, Actual: 22 * time.Minute},
		{NodeID: "4", Kind: "IMPL", Estimate: 10 * time.Minute, Actual: 16 * time.Minute},
		// RESEARCH: ratios 1.0, 1.5
		{NodeID: "5", Kind: "RESEARCH", Estimate: 30 * time.Minute, Actual: 30 * time.Minute},
		{NodeID: "6", Kind: "RESEARCH", Estimate: 30 * time.Minute, Actual: 45 * time.Minute},
	}

	c := Calibrate(obs)
	if c.Samples != 6 {
		t.Fatalf("Samples = %d, want 6", c.Samples)
	}

	// All ratios: 1.5,1.6,2.0,2.2,1.0,1.5 -> sorted 1.0,1.5,1.5,1.6,2.0,2.2
	// nearest-rank median of 6 -> rank 3 -> 1.5
	approxRatio(t, c.MedianRatio, 1.5)
	// p95 of 6 -> rank round(5.7)=6 -> 2.2
	approxRatio(t, c.P95Ratio, 2.2)

	impl := c.ByKind["IMPL"]
	if impl.Samples != 4 {
		t.Fatalf("IMPL Samples = %d, want 4", impl.Samples)
	}
	// actuals 16,20,22,30 -> median rank 2 -> 20m
	if impl.MedianActual != 20*time.Minute {
		t.Fatalf("IMPL MedianActual = %v, want 20m", impl.MedianActual)
	}
	if impl.P95Actual != 30*time.Minute {
		t.Fatalf("IMPL P95Actual = %v, want 30m", impl.P95Actual)
	}

	res := c.ByKind["RESEARCH"]
	if res.MedianActual != 30*time.Minute {
		t.Fatalf("RESEARCH MedianActual = %v, want 30m", res.MedianActual)
	}
}

func TestCalibrateSkipsZeroEstimate(t *testing.T) {
	obs := []Observation{
		{NodeID: "1", Kind: "IMPL", Estimate: 0, Actual: 30 * time.Minute},
		{NodeID: "2", Kind: "IMPL", Estimate: 10 * time.Minute, Actual: 20 * time.Minute},
	}
	c := Calibrate(obs)
	if c.Samples != 1 {
		t.Fatalf("Samples = %d, want 1", c.Samples)
	}
	approxRatio(t, c.MedianRatio, 2.0)
}

func TestCalibrateSkipsZeroActual(t *testing.T) {
	obs := []Observation{
		{NodeID: "1", Kind: "IMPL", Estimate: 10 * time.Minute, Actual: 0},
		{NodeID: "2", Kind: "IMPL", Estimate: 10 * time.Minute, Actual: 10 * time.Minute},
	}
	if c := Calibrate(obs); c.Samples != 1 {
		t.Fatalf("Samples = %d, want 1", c.Samples)
	}
}

func TestCalibrateSkipsAdopted(t *testing.T) {
	obs := []Observation{
		{NodeID: "1", Kind: "IMPL", Estimate: 10 * time.Minute, Actual: 90 * time.Minute, Adopted: true},
		{NodeID: "2", Kind: "IMPL", Estimate: 10 * time.Minute, Actual: 20 * time.Minute},
	}
	c := Calibrate(obs)
	if c.Samples != 1 {
		t.Fatalf("Samples = %d, want 1 (adopted excluded)", c.Samples)
	}
	approxRatio(t, c.MedianRatio, 2.0)
	if c.Suggested["IMPL"] != 20*time.Minute {
		t.Fatalf("Suggested = %v, want 20m", c.Suggested["IMPL"])
	}
}

func TestCalibrateSuggestedRoundsToMinute(t *testing.T) {
	obs := []Observation{
		{NodeID: "1", Kind: "IMPL", Estimate: 10 * time.Minute, Actual: 7*time.Minute + 29*time.Second},
		{NodeID: "2", Kind: "IMPL", Estimate: 10 * time.Minute, Actual: 19*time.Minute + 40*time.Second},
	}
	c := Calibrate(obs)
	// actuals 7m29s, 19m40s -> median rank 1 -> 7m29s -> rounds to 7m
	if c.Suggested["IMPL"] != 7*time.Minute {
		t.Fatalf("Suggested = %v, want 7m", c.Suggested["IMPL"])
	}
}

func TestCalibrateSuggestedMedianPerKind(t *testing.T) {
	obs := []Observation{
		{NodeID: "1", Kind: "IMPL", Estimate: 10 * time.Minute, Actual: 10 * time.Minute},
		{NodeID: "2", Kind: "IMPL", Estimate: 10 * time.Minute, Actual: 30 * time.Minute},
		{NodeID: "3", Kind: "IMPL", Estimate: 20 * time.Minute, Actual: 40 * time.Minute},
		{NodeID: "4", Kind: "GATE", Estimate: 5 * time.Minute, Actual: 4 * time.Minute},
	}
	c := Calibrate(obs)
	// IMPL actuals 10,30,40 -> rank 2 -> 30m
	if got := c.Suggested["IMPL"]; got != 30*time.Minute {
		t.Fatalf("Suggested[IMPL] = %v, want 30m", got)
	}
	if got := c.Suggested["GATE"]; got != 4*time.Minute {
		t.Fatalf("Suggested[GATE] = %v, want 4m", got)
	}
}

func TestDriftEmpty(t *testing.T) {
	c := Calibrate(nil)
	if got := c.Drift(); got != "no measurements yet" {
		t.Fatalf("Drift() = %q, want %q", got, "no measurements yet")
	}
}

func TestDriftContainsRatioAndSuggestion(t *testing.T) {
	obs := []Observation{
		{NodeID: "1", Kind: "IMPL", Estimate: 10 * time.Minute, Actual: 20 * time.Minute},
		{NodeID: "2", Kind: "IMPL", Estimate: 10 * time.Minute, Actual: 20 * time.Minute},
		{NodeID: "3", Kind: "RESEARCH", Estimate: 30 * time.Minute, Actual: 45 * time.Minute},
	}
	d := Calibrate(obs).Drift()

	if !strings.Contains(d, "n=3") {
		t.Errorf("Drift missing sample count: %q", d)
	}
	if !strings.Contains(d, "1.5x") {
		t.Errorf("Drift missing median ratio: %q", d)
	}
	if !strings.Contains(d, "IMPL") || !strings.Contains(d, "suggested default 20m") {
		t.Errorf("Drift missing IMPL suggestion: %q", d)
	}
	if !strings.Contains(d, "RESEARCH") || !strings.Contains(d, "suggested default 45m") {
		t.Errorf("Drift missing RESEARCH suggestion: %q", d)
	}
}
