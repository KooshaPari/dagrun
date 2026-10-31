package plan

import "testing"

// The attempt budget must be ONE number that both the retry policy and the
// claim check agree on. When they disagreed the run granted a throttled node
// six attempts and the store failed it on the raw plan value of two, so the
// extra attempts never happened:
//
//	n1 status=failed class=rate_limited attempts=1 last_error="exhausted 1 attempt(s)"

func TestEffectiveAttemptsRaisesThrottlesToTheFloor(t *testing.T) {
	if MinRateLimitAttempts <= 2 {
		t.Fatalf("MinRateLimitAttempts = %d, must exceed the default budget of 2 or it changes nothing", MinRateLimitAttempts)
	}
	if got := EffectiveAttempts(2, ClassRateLimited); got != MinRateLimitAttempts {
		t.Fatalf("attempts = %d, want the floor %d", got, MinRateLimitAttempts)
	}
}

func TestEffectiveAttemptsFloorIsNotACeiling(t *testing.T) {
	// A plan asking for more keeps its own number.
	if got := EffectiveAttempts(10, ClassRateLimited); got != 10 {
		t.Fatalf("attempts = %d, want 10 (the floor must not lower a larger budget)", got)
	}
}

func TestEffectiveAttemptsLeavesOtherClassesAlone(t *testing.T) {
	// Only a throttle earns more patience. An agent's own mistake keeps the
	// plan's budget exactly.
	for _, class := range []string{
		ClassHarnessError,
		ClassTimeout,
		ClassContractMiss,
		"check_failed",
		"harness_missing",
		"",
	} {
		if got := EffectiveAttempts(2, class); got != 2 {
			t.Errorf("class %q: attempts = %d, want 2 untouched", class, got)
		}
	}
}

func TestEffectiveAttemptsClampsANonPositiveBudget(t *testing.T) {
	// A budget of zero would mean the node can never run at all.
	for _, max := range []int{0, -1, -100} {
		if got := EffectiveAttempts(max, ""); got != 1 {
			t.Errorf("planMax %d: attempts = %d, want 1", max, got)
		}
	}
}
