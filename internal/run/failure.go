package run

import (
	"bytes"
	"strings"
	"time"

	"github.com/KooshaPari/dagrun/internal/plan"
	"github.com/KooshaPari/dagrun/internal/verify"
)

// FailureClass names why a node failed, so a retry policy can act on the cause
// rather than on the symptom.
type FailureClass string

const (
	FailureNone            FailureClass = ""
	FailureHarnessMissing  FailureClass = "harness_missing"
	FailureHarnessError    FailureClass = "harness_error"
	FailureRateLimited     FailureClass = "rate_limited"
	FailureTimeout         FailureClass = "timeout"
	FailureContractMiss    FailureClass = "contract_miss"
	FailureCapBlocked      FailureClass = "cap_blocked"
	FailureDepBlocked      FailureClass = "dep_blocked"
	FailureDiscoverInvalid FailureClass = "discover_invalid"
	FailureCheckFailed     FailureClass = "check_failed"
	FailureUnknown         FailureClass = "unknown"
)

// Evidence is everything the classifier is allowed to look at.
//
// It is a struct rather than a parameter list because the list kept growing,
// and a five-argument call of bare nils and bools is a place where the wrong
// argument silently lands in the wrong slot.
type Evidence struct {
	// Report is the artifact verification result.
	Report *verify.Report
	// CheckErr is the failure of the node's check command, if any.
	CheckErr error
	// SpawnErr is the error from starting the child. A non-nil SpawnErr with a
	// "not found" message means the harness binary is absent.
	SpawnErr error
	// TimedOut reports whether the child ran out of clock.
	TimedOut bool
	// ExitCode is the child's exit status. -1 means it never ran.
	ExitCode int
	// Output is the captured tail of what the child printed. It is where a
	// provider's rate-limit message appears, and that message is the only
	// evidence that distinguishes "the provider throttled us" from "the
	// provider failed".
	Output []byte
}

// rateLimitSignals are the substrings a harness or provider prints when it is
// throttling rather than failing.
//
// This list exists because of a real run: a 429 arrived with the body
// {"type":"error","error":{"type":"GoUsageLimitError","message":"Go usage limit
// exceeded"},"metadata":{"limitName":"monthly"}}. It classified as a plain
// harness_error, so the node was retried immediately into the same wall and the
// run failed. The same credential served normally minutes later, so the limit
// was a transient burst throttle wearing a "monthly" label. Waiting is the
// correct response, and waiting is what these signals unlock.
//
// The list errs toward recognition: a false positive costs one backoff, a false
// negative costs a whole run.
var rateLimitSignals = []string{
	"429",
	"too many requests",
	"rate limit",
	"ratelimit",
	"rate_limit",
	"usage limit",
	"quota",
	"resource_exhausted",
	"resource exhausted",
	"overloaded",
	"try again later",
	"throttl",
}

// IsRateLimited reports whether the child's output shows a provider throttle.
func IsRateLimited(output []byte) bool {
	if len(output) == 0 {
		return false
	}
	low := bytes.ToLower(output)
	for _, sig := range rateLimitSignals {
		if bytes.Contains(low, []byte(sig)) {
			return true
		}
	}
	return false
}

// Classify decides the failure class from the evidence.
//
// Order matters, and each step is ahead of the next for a reason:
//
//  1. A missing binary is a LOCAL problem, not the provider's.
//  2. A throttle is checked before the generic harness error: both look like a
//     non-zero exit, but only one is worth waiting out.
//  3. The clock is the provider being too slow.
//  4. A non-zero exit is the harness refusing or failing.
//  5. Only then do the artifact predicates speak: a zero exit with a missing
//     artifact is the AGENT's fault.
func Classify(e Evidence) FailureClass {
	if e.SpawnErr != nil {
		msg := strings.ToLower(e.SpawnErr.Error())
		if strings.Contains(msg, "not found") || strings.Contains(msg, "no such file") {
			return FailureHarnessMissing
		}
		return FailureHarnessError
	}
	if IsRateLimited(e.Output) {
		return FailureRateLimited
	}
	if e.TimedOut {
		return FailureTimeout
	}
	if e.ExitCode != 0 {
		return FailureHarnessError
	}
	if e.CheckErr != nil && artifactsOK(e.Report) {
		return FailureCheckFailed
	}
	if e.Report != nil && !e.Report.OK {
		return FailureContractMiss
	}
	return FailureUnknown
}

// artifactsOK reports whether every declared artifact passed its predicate.
func artifactsOK(rep *verify.Report) bool {
	if rep == nil {
		return true
	}
	for _, a := range rep.Artifacts {
		if !a.OK {
			return false
		}
	}
	return true
}

// ShouldRetry reports whether a class is worth another attempt within the
// node's attempt budget.
func ShouldRetry(class FailureClass, attempts, maxAttempts int) bool {
	if attempts >= maxAttempts {
		return false
	}
	switch class {
	case FailureContractMiss, FailureCheckFailed, FailureHarnessError,
		FailureRateLimited, FailureTimeout, FailureUnknown:
		return true
	default:
		return false
	}
}

// isProviderFailureClass reports whether a failure class is the PROVIDER's
// fault, and therefore grounds for cooling that provider down.
//
// The set is small on purpose. harness_error, rate_limited and timeout mean the
// provider refused the work, throttled it, or was too slow. Everything else is
// something else:
//
//   - contract_miss and check_failed are the AGENT's fault. Cooling the
//     provider down for them would punish a healthy provider for one bad
//     prompt and stall every other node that shares it.
//   - harness_missing is a LOCAL problem: the binary is not installed.
//   - cap_blocked, dep_blocked and discover_invalid are structural.
//
// The mirror of this predicate lives in internal/plan as
// plan.IsProviderFailure, because the plan package cannot import this one.
func isProviderFailureClass(c FailureClass) bool {
	switch c {
	case FailureHarnessError, FailureRateLimited, FailureTimeout:
		return true
	default:
		return false
	}
}

// FailureSummary returns a one-line human description of a class.
func FailureSummary(c FailureClass) string {
	switch c {
	case FailureNone:
		return "no failure"
	case FailureHarnessMissing:
		return "the harness binary was not found"
	case FailureHarnessError:
		return "the harness failed"
	case FailureRateLimited:
		return "the provider is throttling"
	case FailureTimeout:
		return "the node timed out"
	case FailureContractMiss:
		return "the agent ran but the artifacts did not verify"
	case FailureCapBlocked:
		return "a concurrency cap blocked it"
	case FailureDepBlocked:
		return "an external dependency is not done"
	case FailureDiscoverInvalid:
		return "the work set it discovered was invalid"
	case FailureCheckFailed:
		return "the check command failed"
	default:
		return "unknown failure"
	}
}

// DefaultRateLimitBackoff is how long a provider is paused after it throttles,
// when the plan declares no policy of its own.
//
// This default exists because of a measured failure. A real run hit
// {"type":"GoUsageLimitError","message":"Go usage limit exceeded"} with a
// "monthly" label, was retried immediately, hit the same wall, and failed the
// run. The same credential served normally minutes later: the limit was a
// transient burst throttle. Without a default pause, an unattended run converts
// a throttle into a failure every time, which is the one thing a rate limit
// must never do.
//
// 30s is generous for a burst window and cheap against a run that would
// otherwise die. A plan that knows better overrides it with provider_limits.
const DefaultRateLimitBackoff = 30 * time.Second

// effectiveLimits returns the limits to apply to a failure, filling in the
// defaults that a throttle needs even when the plan declared nothing.
//
// Only rate_limited gets a default, and deliberately so: it is the one class
// where waiting is unambiguously correct, and the one class where acting
// immediately is guaranteed to fail again. Every other class keeps the plan's
// word, which is how "no policy" stays "no behaviour change".
func effectiveLimits(l plan.ProviderLimits, class FailureClass) plan.ProviderLimits {
	if class != FailureRateLimited {
		return l
	}
	if l.Backoff == "" {
		l.Backoff = DefaultRateLimitBackoff.String()
	}
	// A throttle is an immediate reason to pause; waiting for N consecutive
	// failures would mean hitting the wall N times first.
	if l.CooldownAfter <= 0 {
		l.CooldownAfter = 1
	}
	return l
}
