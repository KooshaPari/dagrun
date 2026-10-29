package plan

import (
	"sync"
	"testing"
	"time"
)

func TestProviderOf(t *testing.T) {
	cases := []struct {
		name string
		node *Node
		want string
	}{
		{"explicit", &Node{Provider: "anthropic"}, "anthropic"},
		{"empty", &Node{}, DefaultProvider},
		{"nil", nil, DefaultProvider},
		{"harness only", &Node{Harness: "codex"}, DefaultProvider},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ProviderOf(c.node); got != c.want {
				t.Fatalf("ProviderOf = %q, want %q", got, c.want)
			}
		})
	}
}

func TestProviderPolicyValidate(t *testing.T) {
	ok := ProviderPolicy{Limits: map[string]ProviderLimits{
		"anthropic": {MaxConcurrent: 2, Backoff: "30s", CooldownAfter: 3},
		"openai":    {MaxConcurrent: 0},
	}}
	if issues := ok.Validate(); len(issues) != 0 {
		t.Fatalf("valid policy reported %v", issues)
	}

	bad := ProviderPolicy{Limits: map[string]ProviderLimits{
		"anthropic": {Backoff: "not-a-duration"},
	}}
	issues := bad.Validate()
	if len(issues) != 1 {
		t.Fatalf("got %d issues, want 1: %v", len(issues), issues)
	}
	if issues[0].Code != "bad_provider_backoff" {
		t.Fatalf("code = %q, want bad_provider_backoff", issues[0].Code)
	}

	neg := ProviderPolicy{Limits: map[string]ProviderLimits{
		"openai": {CooldownAfter: -1},
	}}
	if issues := neg.Validate(); len(issues) != 1 || issues[0].Code != "bad_provider_cooldown" {
		t.Fatalf("negative cooldown issues = %v, want one bad_provider_cooldown", issues)
	}
}

func TestBackoffDuration(t *testing.T) {
	if d, err := (ProviderLimits{}).BackoffDuration(); err != nil || d != 0 {
		t.Fatalf("empty backoff = %v, %v; want 0, nil", d, err)
	}
	if d, err := (ProviderLimits{Backoff: "2m"}).BackoffDuration(); err != nil || d != 2*time.Minute {
		t.Fatalf("2m backoff = %v, %v", d, err)
	}
	if _, err := (ProviderLimits{Backoff: "-5s"}).BackoffDuration(); err == nil {
		t.Fatal("negative backoff should error")
	}
}

func TestLimitsForUndeclaredProviderIsUncapped(t *testing.T) {
	p := ProviderPolicy{Limits: map[string]ProviderLimits{"anthropic": {MaxConcurrent: 1}}}
	if got := p.LimitsFor("openai"); got != (ProviderLimits{}) {
		t.Fatalf("undeclared provider limits = %+v, want zero", got)
	}
	var nilPolicy ProviderPolicy
	if got := nilPolicy.LimitsFor("anthropic"); got != (ProviderLimits{}) {
		t.Fatalf("nil policy limits = %+v, want zero", got)
	}
}

func TestAdmissionAcquireRespectsMaxConcurrent(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	a := NewAdmission()
	a.SetLimits("anthropic", ProviderLimits{MaxConcurrent: 2})

	if !a.Acquire("anthropic", now) {
		t.Fatal("first acquire refused")
	}
	if !a.Acquire("anthropic", now) {
		t.Fatal("second acquire refused")
	}
	if a.Acquire("anthropic", now) {
		t.Fatal("third acquire should hit the cap")
	}
	if got := a.Running("anthropic"); got != 2 {
		t.Fatalf("running = %d, want 2", got)
	}
	// Another provider is unaffected by the cap.
	if !a.Acquire("openai", now) {
		t.Fatal("openai acquire refused")
	}
}

func TestAdmissionUncappedProviderIsUnlimited(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	a := NewAdmission()
	for i := 0; i < 50; i++ {
		if !a.Acquire("openai", now) {
			t.Fatalf("uncapped acquire refused at i=%d", i)
		}
	}
	if got := a.Running("openai"); got != 50 {
		t.Fatalf("running = %d, want 50", got)
	}
}

func TestAdmissionReleaseFreesSlot(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	a := NewAdmission()
	a.SetLimits("anthropic", ProviderLimits{MaxConcurrent: 1})

	if !a.Acquire("anthropic", now) {
		t.Fatal("acquire refused")
	}
	if a.Acquire("anthropic", now) {
		t.Fatal("second acquire should be refused")
	}
	a.Release("anthropic")
	if got := a.Running("anthropic"); got != 0 {
		t.Fatalf("running = %d, want 0", got)
	}
	if !a.Acquire("anthropic", now) {
		t.Fatal("acquire after release refused")
	}
}

func TestAdmissionReleaseNeverGoesNegative(t *testing.T) {
	a := NewAdmission()
	a.SetLimits("anthropic", ProviderLimits{MaxConcurrent: 1})
	a.Release("anthropic")
	a.Release("anthropic")
	if got := a.Running("anthropic"); got != 0 {
		t.Fatalf("running = %d, want 0", got)
	}
	// A stray release must not hand out an extra slot.
	if !a.Acquire("anthropic", time.Now()) {
		t.Fatal("acquire refused")
	}
	if a.Acquire("anthropic", time.Now()) {
		t.Fatal("stray releases invented a slot")
	}
}

func TestAdmissionFailTripsCooldownAtThreshold(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	limits := ProviderLimits{MaxConcurrent: 4, Backoff: "30s", CooldownAfter: 2}
	a := NewAdmission()
	a.SetLimits("anthropic", limits)

	a.Fail("anthropic", limits, now)
	if a.InCooldown("anthropic", now) {
		t.Fatal("cooled down after one failure, threshold is 2")
	}
	a.Fail("anthropic", limits, now)
	if !a.InCooldown("anthropic", now) {
		t.Fatal("not cooling down after reaching the threshold")
	}
	if a.Acquire("anthropic", now) {
		t.Fatal("acquire allowed during cooldown")
	}
}

func TestAdmissionCooldownExpires(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	limits := ProviderLimits{Backoff: "30s", CooldownAfter: 1}
	a := NewAdmission()
	a.SetLimits("anthropic", limits)

	a.Fail("anthropic", limits, now)
	if !a.InCooldown("anthropic", now) {
		t.Fatal("not cooling down")
	}
	if !a.InCooldown("anthropic", now.Add(29*time.Second)) {
		t.Fatal("cooldown ended early")
	}
	if a.InCooldown("anthropic", now.Add(30*time.Second)) {
		t.Fatal("cooldown did not expire at the backoff boundary")
	}
	if !a.Acquire("anthropic", now.Add(30*time.Second)) {
		t.Fatal("acquire refused after cooldown expired")
	}
}

func TestAdmissionNeverCoolsDownWhenThresholdIsZero(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	limits := ProviderLimits{Backoff: "30s", CooldownAfter: 0}
	a := NewAdmission()
	for i := 0; i < 10; i++ {
		a.Fail("anthropic", limits, now)
	}
	if a.InCooldown("anthropic", now) {
		t.Fatal("cooldown_after 0 must never cool down")
	}
}

func TestAdmissionSuccessResetsFailures(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	limits := ProviderLimits{Backoff: "30s", CooldownAfter: 3}
	a := NewAdmission()
	a.SetLimits("anthropic", limits)

	a.Fail("anthropic", limits, now)
	a.Fail("anthropic", limits, now)
	a.Success("anthropic")
	if got := a.Failures("anthropic"); got != 0 {
		t.Fatalf("failures after success = %d, want 0", got)
	}
	// Two more failures after the reset must not be enough on their own.
	a.Fail("anthropic", limits, now)
	a.Fail("anthropic", limits, now)
	if a.InCooldown("anthropic", now) {
		t.Fatal("success did not reset the consecutive failure count")
	}
	a.Fail("anthropic", limits, now)
	if !a.InCooldown("anthropic", now) {
		t.Fatal("threshold never tripped after reset")
	}
}

func TestAdmissionCooldownRestartsWindow(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	limits := ProviderLimits{Backoff: "10s", CooldownAfter: 1}
	a := NewAdmission()
	a.SetLimits("anthropic", limits)

	a.Fail("anthropic", limits, now)
	later := now.Add(11 * time.Second)
	if !a.Acquire("anthropic", later) {
		t.Fatal("acquire refused after cooldown")
	}
	if got := a.Failures("anthropic"); got != 0 {
		t.Fatalf("failures after cooldown = %d, want 0", got)
	}
}

func TestIsProviderFailure(t *testing.T) {
	for _, c := range []string{"harness_error", "timeout"} {
		if !IsProviderFailure(c) {
			t.Fatalf("%q should be a provider failure", c)
		}
	}
	for _, c := range []string{"contract_miss", "check_failed", "harness_missing", "", "unknown"} {
		if IsProviderFailure(c) {
			t.Fatalf("%q must not be a provider failure", c)
		}
	}
}

func TestAdmissionConcurrentUse(t *testing.T) {
	const maxConcurrent = 4
	const goroutines = 32
	const perGoroutine = 200

	now := time.Unix(1_700_000_000, 0)
	a := NewAdmission()
	a.SetLimits("anthropic", ProviderLimits{MaxConcurrent: maxConcurrent, Backoff: "1s", CooldownAfter: 5})

	var wg sync.WaitGroup
	var mu sync.Mutex
	peak := 0
	record := func(n int) {
		mu.Lock()
		if n > peak {
			peak = n
		}
		mu.Unlock()
	}

	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				if !a.Acquire("anthropic", now) {
					continue
				}
				record(a.Running("anthropic"))
				_ = a.InCooldown("anthropic", now)
				if i%50 == 0 {
					a.Fail("anthropic", ProviderLimits{Backoff: "1ms", CooldownAfter: 1000}, now)
				}
				if i%97 == 0 {
					a.Success("anthropic")
				}
				a.Release("anthropic")
			}
		}(g)
	}
	wg.Wait()

	if peak > maxConcurrent {
		t.Fatalf("observed %d concurrent slots, cap is %d", peak, maxConcurrent)
	}
	if got := a.Running("anthropic"); got != 0 {
		t.Fatalf("running after all releases = %d, want 0", got)
	}
}
