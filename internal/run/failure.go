package run

import (
	"strings"

	"github.com/KooshaPari/dagrun/internal/verify"
)

// FailureClass names why a node failed, so a retry policy can act on the cause
// rather than on the symptom.
type FailureClass string

const (
	FailureNone            FailureClass = ""
	FailureHarnessMissing  FailureClass = "harness_missing"
	FailureHarnessError    FailureClass = "harness_error"
	FailureTimeout         FailureClass = "timeout"
	FailureContractMiss    FailureClass = "contract_miss"
	FailureCapBlocked      FailureClass = "cap_blocked"
	FailureDepBlocked      FailureClass = "dep_blocked"
	FailureDiscoverInvalid FailureClass = "discover_invalid"
	FailureCheckFailed     FailureClass = "check_failed"
	FailureUnknown         FailureClass = "unknown"
)

// Classify decides the failure class.
//
// exitCode is the child's exit status. It matters because a harness that
// exits NON-ZERO did not deliver its work: that is a harness error, and the
// provider that refused the work is the reason. Without this, a provider
// refusal that produced no artifacts was classified contract_miss — the
// agent's fault — and the provider was never cooled down, so every later
// node kept hitting it.
//
// A zero exit with a missing artifact is still contract_miss: the agent ran
// and produced the wrong thing, which is the agent's fault, not the
// provider's.
func Classify(rep *verify.Report, checkErr error, spawnErr error, timedOut bool, exitCode int) FailureClass {
	if spawnErr != nil {
		msg := strings.ToLower(spawnErr.Error())
		if strings.Contains(msg, "not found") || strings.Contains(msg, "no such file") {
			// The binary is missing. That is a LOCAL problem, not the
			// provider's, so it must not cool a provider down.
			return FailureHarnessMissing
		}
	}
	if timedOut {
		return FailureTimeout
	}
	if spawnErr != nil {
		return FailureHarnessError
	}
	// The child ran and reported failure. That is the harness's fault, and
	// the provider behind it usually is too.
	if exitCode != 0 {
		return FailureHarnessError
	}
	if checkErr != nil && artifactsOK(rep) {
		return FailureCheckFailed
	}
	if rep != nil && !rep.OK {
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

// ShouldRetry reports whether a failed node should be retried.
//
// contract_miss and check_failed are retried up to maxAttempts (the agent may
// fix it). harness_missing, dep_blocked, cap_blocked and discover_invalid are
// never retried: retrying them cannot help. harness_error and timeout are
// retried because they are usually transient.
func ShouldRetry(class FailureClass, attempts, maxAttempts int) bool {
	if attempts >= maxAttempts {
		return false
	}
	switch class {
	case FailureContractMiss, FailureCheckFailed, FailureHarnessError, FailureTimeout, FailureUnknown:
		return true
	default:
		return false
	}
}

// isProviderFailureClass reports whether a failure class is the PROVIDER's
// fault, and therefore grounds for cooling that provider down.
//
// The set is small on purpose. harness_error and timeout mean the provider
// refused the work or was too slow. Everything else is something else:
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
	case FailureHarnessError, FailureTimeout:
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
	case FailureTimeout:
		return "the node timed out"
	case FailureContractMiss:
		return "the agent ran but the artifacts did not verify"
	case FailureCapBlocked:
		return "the reachability cap blocked the node"
	case FailureDepBlocked:
		return "an upstream dependency blocked the node"
	case FailureDiscoverInvalid:
		return "the harness discovery output was invalid"
	case FailureCheckFailed:
		return "the check command failed"
	case FailureUnknown:
		return "the node failed for an unknown reason"
	default:
		return "unrecognized failure class: " + string(c)
	}
}
