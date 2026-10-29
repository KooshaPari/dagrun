package run

import (
	"errors"
	"testing"

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

	class := Classify(&rep, nil, nil, false, 7)
	if class != FailureHarnessError {
		t.Fatalf("class = %q, want %q (a non-zero exit is the provider refusing the work)", class, FailureHarnessError)
	}
}

// A zero exit with a missing artifact is still the agent's fault.
func TestClassifyZeroExitWithMissingArtifactIsAContractMiss(t *testing.T) {
	rep := verify.Report{NodeID: "n", OK: false, Reason: "out.txt: missing file does not exist"}

	class := Classify(&rep, nil, nil, false, 0)
	if class != FailureContractMiss {
		t.Fatalf("class = %q, want %q (the agent ran and produced the wrong thing)", class, FailureContractMiss)
	}
}

// A missing binary is a LOCAL problem. It must not be blamed on the provider,
// or a provider is cooled down for something the operator's machine did.
func TestClassifyMissingBinaryIsNotAProviderFailure(t *testing.T) {
	rep := verify.Report{NodeID: "n", OK: false, Reason: "spawn failed"}

	class := Classify(&rep, nil, errors.New("exec: \"nope\": executable file not found in $PATH"), false, -1)
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

	class := Classify(&rep, nil, nil, true, 0)
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

	class := Classify(&rep, checkErr(rep), nil, false, 0)
	if class != FailureCheckFailed {
		t.Fatalf("class = %q, want %q", class, FailureCheckFailed)
	}
	if isProviderFailureClass(class) {
		t.Fatal("a failing check must not be a provider failure")
	}
}

// The set that cools a provider down must be exactly the provider's fault.
func TestProviderFailureSetIsExactlyTheProviders(t *testing.T) {
	provider := []FailureClass{FailureHarnessError, FailureTimeout}
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
