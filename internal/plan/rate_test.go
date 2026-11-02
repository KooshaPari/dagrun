package plan

import "testing"

// TestDefaultRateIsDeterministic proves the same model always prices the
// same.
//
// This is a regression test for a real fleet-run defect: the substring
// fallback iterated the rate table with a bare `range`, so Go's randomized map
// iteration order returned a DIFFERENT rate on every call. In a 24-node
// mixed-provider fleet the reported spend jumped from $7.40 to $18.86 between
// two adjacent nodes that had the same token counts, because one was priced at
// $0.28/M and the other at $15/M. A budget ceiling checked against a
// non-reproducible spend is meaningless.
func TestDefaultRateIsDeterministic(t *testing.T) {
	models := []string{
		"",
		"deepseek-v4.1-flash",
		"claude-sonnet-4",
		"gpt-5",
		"minimax-m3",
		"gemini-2.5-pro",
		"some-unknown-model",
		"gpt-5-turbo-preview",
	}
	for _, m := range models {
		first := DefaultRate(m)
		for i := 0; i < 200; i++ {
			got := DefaultRate(m)
			if got != first {
				t.Fatalf("DefaultRate(%q) is not stable: got %+v, want %+v", m, got, first)
			}
		}
	}
}

// TestDefaultRateEmptyModelUsesTheMidRangeDefault pins the empty-model case.
// An empty name matches every table key as a substring, so without a
// special case it would be priced at whatever the table happened to yield.
func TestDefaultRateEmptyModelUsesTheMidRangeDefault(t *testing.T) {
	got := DefaultRate("")
	want := Rate{InputPerMillion: 3, OutputPerMillion: 15, CacheReadPerMillion: 0.3}
	if got != want {
		t.Fatalf("DefaultRate(\"\") = %+v, want %+v", got, want)
	}
}

// TestDefaultRateKnownModelIsExact proves the table still wins over the
// fallback, and that a versioned model name still resolves to its family.
func TestDefaultRateKnownModelIsExact(t *testing.T) {
	if got := DefaultRate("deepseek-v4.1-flash"); got != DefaultRates["deepseek-v4.1-flash"] {
		t.Fatalf("DefaultRate(\"deepseek-v4.1-flash\") = %+v, want %+v", got, DefaultRates["deepseek-v4.1-flash"])
	}
	// A dated variant of a known family resolves to that family's price.
	if got, want := DefaultRate("deepseek-v4.1-flash-20260101"), DefaultRates["deepseek-v4.1-flash"]; got != want {
		t.Fatalf("DefaultRate(dated variant) = %+v, want %+v", got, want)
	}
}
