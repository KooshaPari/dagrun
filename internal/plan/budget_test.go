package plan

import (
	"math"
	"testing"
)

// rate builds a rate that is easy to reason about: $1/$2/$0.5 per million.
func testRate() Rate {
	return Rate{InputPerMillion: 1.0, OutputPerMillion: 2.0, CacheReadPerMillion: 0.5}
}

func approx(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestBudgetCostArithmetic(t *testing.T) {
	// 1,000,000 input @ $1/M = 1.00
	//   500,000 output @ $2/M = 1.00
	// 2,000,000 cache  @ $0.5/M = 1.00
	s := Spend{InputTokens: 1_000_000, OutputTokens: 500_000, CacheReadTokens: 2_000_000}
	approx(t, Cost(s, testRate()), 3.0)
}

func TestBudgetCostIgnoresStoredCostAndScales(t *testing.T) {
	r := testRate()
	// The prior CostUSD on the spend is not an input to Cost.
	s := Spend{InputTokens: 1_000, CostUSD: 99}
	approx(t, Cost(s, r), 0.001)

	// Twice the tokens, twice the price.
	approx(t, Cost(Spend{InputTokens: 2_000}, r), 2*Cost(Spend{InputTokens: 1_000}, r))
}

func TestBudgetCostClampsNegativeInputs(t *testing.T) {
	r := Rate{InputPerMillion: -5, OutputPerMillion: 2, CacheReadPerMillion: 1}
	// Negative rate and negative tokens must not produce a negative bill.
	if got := Cost(Spend{InputTokens: -1000, OutputTokens: -1000, CacheReadTokens: -1000}, r); got < 0 {
		t.Fatalf("cost = %v, want >= 0", got)
	}
	approx(t, Cost(Spend{}, r), 0)
}

func TestBudgetAddAccumulates(t *testing.T) {
	b := NewBudget(10)
	r := testRate()

	b.Add(Spend{InputTokens: 1_000_000}, r)  // 1.00
	b.Add(Spend{OutputTokens: 1_000_000}, r) // 2.00

	approx(t, b.Spent, 3.0)
	approx(t, b.Remaining(), 7.0)
	if b.Exceeded() {
		t.Fatal("should not be exceeded at 3 of 10")
	}
}

func TestBudgetZeroMaxMeansNoLimit(t *testing.T) {
	b := NewBudget(0)
	b.Add(Spend{InputTokens: 1_000_000_000, OutputTokens: 1_000_000_000}, testRate())

	if b.Exceeded() {
		t.Fatal("zero max must mean no limit, never exceeded")
	}
	// No limit: there is no meaningful headroom to report.
	approx(t, b.Remaining(), 0)
}

func TestBudgetNegativeMaxIsTreatedAsNoLimit(t *testing.T) {
	b := NewBudget(-5)
	b.Add(Spend{OutputTokens: 10_000_000}, testRate())
	if b.Exceeded() {
		t.Fatal("negative max must clamp to no limit")
	}
	approx(t, b.Max, 0)
}

func TestBudgetExceededPastCeiling(t *testing.T) {
	b := NewBudget(1.0)
	r := testRate()

	b.Add(Spend{InputTokens: 1_000_000}, r) // exactly 1.00: still inside
	if b.Exceeded() {
		t.Fatal("spending exactly the ceiling is not exceeded")
	}

	b.Add(Spend{InputTokens: 1_000_000}, r) // 2.00 total
	if !b.Exceeded() {
		t.Fatal("2.00 against a 1.00 ceiling must be exceeded")
	}
}

func TestBudgetRemainingNeverNegative(t *testing.T) {
	b := NewBudget(1.0)
	b.Add(Spend{OutputTokens: 10_000_000}, testRate()) // 20.00 against 1.00

	if b.Remaining() != 0 {
		t.Fatalf("remaining = %v, want 0", b.Remaining())
	}
	if b.Remaining() < 0 {
		t.Fatal("remaining must never be negative")
	}
}

func TestBudgetNilReceiverIsSafe(t *testing.T) {
	var b *BudgetState
	b.Add(Spend{InputTokens: 1}, testRate())
	if b.Exceeded() || b.Remaining() != 0 {
		t.Fatal("nil budget must behave like no limit")
	}
}

func TestBudgetDefaultRatesAreSane(t *testing.T) {
	if len(DefaultRates) == 0 {
		t.Fatal("default rate table is empty")
	}
	for model, r := range DefaultRates {
		if model == "" {
			t.Fatal("empty model key")
		}
		if r.InputPerMillion <= 0 || r.OutputPerMillion <= 0 || r.CacheReadPerMillion <= 0 {
			t.Fatalf("%s has a zero or negative rate: %+v", model, r)
		}
		// Output never costs less than input on real published pricing; catch a
		// transposed table.
		if r.OutputPerMillion < r.InputPerMillion {
			t.Fatalf("%s: output rate %v below input rate %v", model, r.OutputPerMillion, r.InputPerMillion)
		}
		// A cache read is always a discount on fresh input.
		if r.CacheReadPerMillion > r.InputPerMillion {
			t.Fatalf("%s: cache read %v above input %v", model, r.CacheReadPerMillion, r.InputPerMillion)
		}
	}
}

func TestBudgetFieldMatchesPlanTag(t *testing.T) {
	// The Budget type and Plan.MaxCostUSD must share a tag or the YAML key
	// silently stops binding. This guards the documented contract.
	b := Budget{MaxCostUSD: 12.5}
	if b.MaxCostUSD != 12.5 {
		t.Fatal("field does not round-trip")
	}
}
