package run

import (
	"errors"
	"testing"

	"github.com/KooshaPari/dagrun/internal/plan"
	"github.com/KooshaPari/dagrun/internal/verify"
)

// A harness that exits NON-ZERO did not deliver its work. That is a harness
// error, and the provider behind it is usually the reason.
//
// The bug this file guards against: a provider refusal that produced no
// artifacts was classified contract_miss — the agent's fault — so per-provider
// admission never cooled the provider down, and every later node kept hitting
// the same refusing provider. The distinction between "the agent produced the
// wrong thing" and "the provider refused" is the whole point of the cooldown.

func TestClassifyNonZeroExitIsAHarnessError(t *testing.T) {
	rep := verify.Report{NodeID: "n", OK: false, Reason: "out.txt: missing file does not exist"}

	class := Classify(Evidence{Report: &rep, ExitCode: 7})
	if class != FailureHarnessError {
		t.Fatalf("class = %q, want %q (a non-zero exit is the provider refusing the work)", class, FailureHarnessError)
	}
}

// A zero exit with a missing artifact is still the agent's fault.
func TestClassifyZeroExitWithMissingArtifactIsAContractMiss(t *testing.T) {
	rep := verify.Report{NodeID: "n", OK: false, Reason: "out.txt: missing file does not exist"}

	class := Classify(Evidence{Report: &rep, ExitCode: 0})
	if class != FailureContractMiss {
		t.Fatalf("class = %q, want %q (the agent ran and produced the wrong thing)", class, FailureContractMiss)
	}
}

// A missing binary is a LOCAL problem. It must not be blamed on the provider,
// or a provider is cooled down for something the operator's machine did.
func TestClassifyMissingBinaryIsNotAProviderFailure(t *testing.T) {
	rep := verify.Report{NodeID: "n", OK: false, Reason: "spawn failed"}

	class := Classify(Evidence{Report: &rep, SpawnErr: errors.New("exec: \"nope\": executable file not found in $PATH"), ExitCode: -1})
	if class != FailureHarnessMissing {
		t.Fatalf("class = %q, want %q", class, FailureHarnessMissing)
	}
	if isProviderFailureClass(class) {
		t.Fatal("a missing binary must not be a provider failure")
	}
}

// A timeout is the provider's fault: it was too slow.
func TestClassifyTimeoutIsAHarnessError(t *testing.T) {
	rep := verify.Report{NodeID: "n", OK: false, Reason: "timed out"}

	class := Classify(Evidence{Report: &rep, TimedOut: true, ExitCode: 0})
	if class != FailureTimeout {
		t.Fatalf("class = %q, want %q", class, FailureTimeout)
	}
	if !isProviderFailureClass(class) {
		t.Fatal("a timeout must be a provider failure")
	}
}

// A failing CHECK with satisfied artifacts is the check's problem, not the
// provider's: the agent did its work and the verification command failed.
func TestClassifyFailingCheckIsACheckFailure(t *testing.T) {
	rep := verify.Report{
		NodeID: "n", OK: true,
		Check: &verify.CheckResult{OK: false, ExitCode: 1, Command: "go test ./..."},
		Artifacts: []verify.ArtifactResult{
			{Path: "out.txt", OK: true},
		},
	}

	class := Classify(Evidence{Report: &rep, CheckErr: checkErr(rep), ExitCode: 0})
	if class != FailureCheckFailed {
		t.Fatalf("class = %q, want %q", class, FailureCheckFailed)
	}
	if isProviderFailureClass(class) {
		t.Fatal("a failing check must not be a provider failure")
	}
}

// The set that cools a provider down must be exactly the provider's fault.
func TestProviderFailureSetIsExactlyTheProviders(t *testing.T) {
	provider := []FailureClass{FailureHarnessError, FailureRateLimited, FailureTimeout}
	notProvider := []FailureClass{
		FailureNone, FailureHarnessMissing, FailureContractMiss,
		FailureCapBlocked, FailureDepBlocked, FailureDiscoverInvalid,
		FailureCheckFailed, FailureUnknown,
	}
	for _, c := range provider {
		if !isProviderFailureClass(c) {
			t.Errorf("%q must be a provider failure", c)
		}
	}
	for _, c := range notProvider {
		if isProviderFailureClass(c) {
			t.Errorf("%q must NOT be a provider failure", c)
		}
	}
}

// The evidence below is the REAL body from a run on 2026-10-30. It arrived as
// HTTP 429 with a "monthly" limit label, but the same credential served
// normally minutes later, so it was a transient burst throttle. Treating it as
// a plain harness error meant an immediate retry into the same wall and a dead
// run.
const realRateLimitBody = `Error: OpenAI-compatible chat request failed
  endpoint: https://opencode.ai/zen/go/v1/chat/completions
  model: deepseek-v4.1-flash
  auth: OPENCODE_GO_API_KEY
  status: 429 Too Many Requests
  response: {"type":"error","error":{"type":"GoUsageLimitError","message":"Go usage limit exceeded"},"metadata":{"workspace":"wrk_01M1TXN1MKC0YAAKWPW7MB8NKZ","limitName":"monthly"}}
`

func TestClassifyRealRateLimitBody(t *testing.T) {
	if !IsRateLimited([]byte(realRateLimitBody)) {
		t.Fatal("the captured 429 body was not recognised as a throttle")
	}
	rep := verify.Report{NodeID: "n", OK: false, Reason: "research.md: missing file does not exist"}
	class := Classify(Evidence{Report: &rep, ExitCode: 1, Output: []byte(realRateLimitBody)})
	if class != FailureRateLimited {
		t.Fatalf("class = %q, want %q", class, FailureRateLimited)
	}
	// A throttle is the provider's doing, so it must cool the provider down.
	if !isProviderFailureClass(class) {
		t.Fatal("rate_limited must be a provider failure")
	}
	if !ShouldRetry(class, 1, 3) {
		t.Fatal("rate_limited must be retryable: waiting is the correct response")
	}
}

func TestClassifyThrottleBeatsGenericHarnessError(t *testing.T) {
	// Both look like a non-zero exit. Only one is worth waiting out, so the
	// throttle check has to come first.
	plain := Classify(Evidence{Report: &verify.Report{OK: false}, ExitCode: 1, Output: []byte("boom")})
	throttled := Classify(Evidence{Report: &verify.Report{OK: false}, ExitCode: 1, Output: []byte("HTTP 429 Too Many Requests")})
	if plain == throttled {
		t.Fatalf("both classified %q; a throttle must be distinguishable from a failure", plain)
	}
	if throttled != FailureRateLimited {
		t.Fatalf("throttled = %q, want %q", throttled, FailureRateLimited)
	}
}

func TestIsRateLimitedRecognisesTheCommonSignals(t *testing.T) {
	yes := []string{
		"status: 429 Too Many Requests",
		"Go usage limit exceeded",
		"rate limit reached",
		"RateLimitError",
		"RESOURCE_EXHAUSTED",
		"the server is overloaded",
		"please try again later",
		"throttled by upstream",
		"quota exceeded",
	}
	for _, s := range yes {
		if !IsRateLimited([]byte(s)) {
			t.Errorf("%q not recognised as a throttle", s)
		}
	}
	no := []string{
		"",
		"build failed",
		"undefined: foo",
		"permission denied",
	}
	for _, s := range no {
		if IsRateLimited([]byte(s)) {
			t.Errorf("%q wrongly recognised as a throttle", s)
		}
	}
}

func TestEffectiveLimitsGivesThrottlesADefaultBackoff(t *testing.T) {
	// With no plan policy at all, a throttle still gets a pause. Without it an
	// unattended run turns every transient throttle into a failure.
	got := effectiveLimits(plan.ProviderLimits{}, FailureRateLimited)
	if got.Backoff == "" {
		t.Fatal("rate_limited got no backoff with an empty policy")
	}
	if got.CooldownAfter != 1 {
		t.Fatalf("CooldownAfter = %d, want 1 (a throttle should pause immediately)", got.CooldownAfter)
	}
	if d, err := got.BackoffDuration(); err != nil || d != DefaultRateLimitBackoff {
		t.Fatalf("backoff = %v (err %v), want %v", d, err, DefaultRateLimitBackoff)
	}

	// Every other class keeps the plan's word: no policy still means no pause.
	for _, c := range []FailureClass{FailureHarnessError, FailureTimeout, FailureContractMiss, FailureCheckFailed} {
		got := effectiveLimits(plan.ProviderLimits{}, c)
		if got.Backoff != "" || got.CooldownAfter != 0 {
			t.Errorf("%q got a default it should not have: %+v", c, got)
		}
	}

	// A plan that knows better wins.
	own := plan.ProviderLimits{Backoff: "5s", CooldownAfter: 3}
	got = effectiveLimits(own, FailureRateLimited)
	if got.Backoff != "5s" || got.CooldownAfter != 3 {
		t.Fatalf("the plan's explicit limits were overridden: %+v", got)
	}
}
