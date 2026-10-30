package run

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KooshaPari/dagrun/internal/plan"
	"github.com/KooshaPari/dagrun/internal/store"
	"github.com/KooshaPari/dagrun/internal/trace"
)

// TestProviderAdmissionEndToEnd proves the per-provider cooldown works in a
// REAL run, not just in the unit tests.
//
// The plan has three nodes:
//   - bad1: provider "bad", exits non-zero (simulating a provider refusal)
//   - bad2: provider "bad", succeeds on its own
//   - good: provider "good", succeeds
//
// The policy gives "bad" a 400ms backoff after a single failure. The
// assertions are the ones that describe the DESIGNED behaviour:
//
//  1. bad1 fails as a harness_error, which is what cools the provider.
//  2. The trace carries a provider.cooldown event naming "bad" and the class.
//  3. bad2 is DELAYED by at least the backoff. A cooldown is a delay, not a
//     permanent block: the node must still complete once it expires. The
//     delay is measured from the cooldown event, and it is the proof that the
//     provider was actually gated rather than merely annotated.
//  4. "good" completes, so the cooldown is per-provider and does not stall the
//     rest of the run.
func TestProviderAdmissionEndToEnd(t *testing.T) {
	root := t.TempDir()
	yaml := `
version: 1
run: admission-e2e
max_parallel: 4
provider_policy:
  provider_limits:
    bad:
      max_concurrent: 1
      backoff: 400ms
      cooldown_after: 1
nodes:
  - id: bad1
    kind: IMPL
    title: bad one
    provider: bad
    harness: exec
    max_attempts: 1
    artifacts: [{path: artifacts/bad1.txt}]
  - id: bad2
    kind: IMPL
    title: bad two
    provider: bad
    harness: exec
    max_attempts: 1
    artifacts: [{path: artifacts/bad2.txt}]
  - id: good
    kind: IMPL
    title: good node
    provider: good
    harness: exec
    artifacts: [{path: artifacts/good.txt}]
`
	p, err := plan.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	p.Root = root
	if issues := p.Validate(); len(issues) > 0 {
		t.Fatalf("plan invalid: %v", issues)
	}
	dir := filepath.Join(root, ".dagrun")
	st, err := store.Open(dir, p, []byte(yaml), true)
	if err != nil {
		t.Fatal(err)
	}
	// One template serves all three nodes. The prompt names the node's
	// artifact path, so the template can tell them apart. Each node records
	// when it started, outside the artifacts dir, so the cooldown delay can
	// be measured.
	script := filepath.Join(root, "tmpl.sh")
	if err := os.WriteFile(script, []byte(`#!/bin/sh
mkdir -p artifacts
prompt=$(cat)
case "$prompt" in
  *bad1.txt*) echo "provider refusal" >&2; exit 7 ;;
  *bad2.txt*) name=bad2 ;;
  *good.txt*) name=good ;;
esac
echo "start $name $(date +%s%N)" >> timing.log
echo ok > artifacts/$name.txt
exit 0
`), 0o755); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	ex := New(st, trace.New(filepath.Join(dir, "trace")), Options{
		Workers:        3,
		Owner:          "test",
		Lease:          time.Minute,
		ExecTemplate:   "sh -c " + script + " {stdin}",
		Out:            &out,
		Admission:      plan.NewAdmission(),
		ProviderPolicy: p.ProviderPolicy,
	})
	ex.opt.Admission.ApplyPolicy(p.ProviderPolicy)

	sum, err := ex.Loop(context.Background())
	if err != nil {
		t.Fatalf("loop: %v (out: %s)", err, out.String())
	}

	snap, _ := st.Snapshot()
	// 1. bad1 failed as a harness error.
	if got := snap.Nodes["bad1"].FailureClass; got != string(FailureHarnessError) {
		t.Fatalf("bad1 class = %q, want %q (out: %s)", got, FailureHarnessError, out.String())
	}
	if snap.Nodes["bad1"].Status != plan.StatusFailed {
		t.Fatalf("bad1 status = %q, want failed", snap.Nodes["bad1"].Status)
	}

	// 2. The trace carries the cooldown event.
	events, err := trace.Merge(filepath.Join(dir, "trace"))
	if err != nil {
		t.Fatal(err)
	}
	var cooldownAt time.Time
	var cooldownFound bool
	for _, e := range events {
		if e.Event != "provider.cooldown" || e.Attrs["provider"] != "bad" {
			continue
		}
		if e.Attrs["class"] != string(FailureHarnessError) {
			continue
		}
		cooldownAt = e.TS
		cooldownFound = true
	}
	if !cooldownFound {
		t.Fatalf("no provider.cooldown event for the bad provider (out: %s)", out.String())
	}

	// 3. bad2 completed, and it STARTED at least the backoff after the
	// cooldown began. That is the proof the provider was gated rather than
	// merely annotated.
	if snap.Nodes["bad2"].Status != plan.StatusDone {
		t.Fatalf("bad2 status = %q, want done — the cooldown must delay, not block (out: %s)", snap.Nodes["bad2"].Status, out.String())
	}
	raw, rerr := os.ReadFile(filepath.Join(root, "timing.log"))
	if rerr != nil {
		t.Fatalf("timing log: %v", rerr)
	}
	var bad2Start time.Time
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		f := strings.Fields(line)
		if len(f) != 3 || f[0] != "start" || f[1] != "bad2" {
			continue
		}
		var ns int64
		if _, serr := fmt.Sscanf(f[2], "%d", &ns); serr == nil {
			bad2Start = time.Unix(0, ns)
		}
	}
	if bad2Start.IsZero() {
		t.Fatalf("bad2 never recorded a start: %q", string(raw))
	}
	if delay := bad2Start.Sub(cooldownAt); delay < 350*time.Millisecond {
		t.Fatalf("bad2 started %v after the cooldown, want at least ~400ms — the provider was not gated", delay)
	}

	// 4. "good" completed, so the cooldown is per-provider.
	if snap.Nodes["good"].Status != plan.StatusDone {
		t.Fatalf("good status = %q, want done (out: %s)", snap.Nodes["good"].Status, out.String())
	}

	if sum.Done != 2 || sum.Failed != 1 {
		t.Fatalf("summary = %+v, want 2 done 1 failed (out: %s)", sum, out.String())
	}
}

// TestProviderAdmissionContractMissDoesNotCoolDown proves the AGENT/provider
// fault distinction: a node that runs and produces the WRONG artifact is a
// contract_miss, which must NOT cool the provider down. The proof is that a
// second node on the same provider still gets claimed and runs.
func TestProviderAdmissionContractMissDoesNotCoolDown(t *testing.T) {
	root := t.TempDir()
	yaml := `
version: 1
run: contract-miss
max_parallel: 4
provider_policy:
  provider_limits:
    c:
      max_concurrent: 1
      backoff: 120s
      cooldown_after: 1
nodes:
  - id: miss
    kind: IMPL
    title: produces the wrong artifact
    provider: c
    harness: exec
    max_attempts: 1
    artifacts: [{path: artifacts/wanted.txt}]
  - id: after
    kind: IMPL
    title: should still run
    provider: c
    harness: exec
    artifacts: [{path: artifacts/after.txt}]
`
	p, err := plan.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	p.Root = root
	if issues := p.Validate(); len(issues) > 0 {
		t.Fatalf("plan invalid: %v", issues)
	}
	dir := filepath.Join(root, ".dagrun")
	st, err := store.Open(dir, p, []byte(yaml), true)
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "tmpl.sh")
	if err := os.WriteFile(script, []byte(`#!/bin/sh
mkdir -p artifacts
prompt=$(cat)
case "$prompt" in
  *wanted.txt*) echo ok > artifacts/wrong.txt; exit 0 ;;
  *after.txt*)  echo ok > artifacts/after.txt; exit 0 ;;
esac
exit 0
`), 0o755); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	ex := New(st, trace.New(filepath.Join(dir, "trace")), Options{
		Workers:        2,
		Owner:          "test",
		Lease:          time.Minute,
		ExecTemplate:   "sh -c " + script + " {stdin}",
		Out:            &out,
		Admission:      plan.NewAdmission(),
		ProviderPolicy: p.ProviderPolicy,
	})
	ex.opt.Admission.ApplyPolicy(p.ProviderPolicy)

	sum, err := ex.Loop(context.Background())
	if err != nil {
		t.Fatalf("loop: %v (out: %s)", err, out.String())
	}

	snap, _ := st.Snapshot()
	if got := snap.Nodes["miss"].FailureClass; got != string(FailureContractMiss) {
		t.Fatalf("miss class = %q, want %q (out: %s)", got, FailureContractMiss, out.String())
	}
	// The provider must NOT have been cooled down: "after" ran.
	if snap.Nodes["after"].Status != plan.StatusDone {
		t.Fatalf("after status = %q, want done — the provider was wrongly cooled down (out: %s)", snap.Nodes["after"].Status, out.String())
	}
	if snap.Nodes["after"].Attempts == 0 {
		t.Fatal("after was never claimed — the provider was cooled down for an agent's mistake")
	}
	// And the trace must NOT carry a provider.cooldown event.
	events, _ := trace.Merge(filepath.Join(dir, "trace"))
	for _, e := range events {
		if e.Event == "provider.cooldown" {
			t.Fatalf("a contract_miss must not produce a provider.cooldown event: %+v", e)
		}
	}
	if sum.Done != 1 || sum.Failed != 1 {
		t.Fatalf("summary = %+v, want 1 done 1 failed (out: %s)", sum, out.String())
	}
}

// TestProviderAdmissionMaxConcurrentIsHonoured proves the cap: two nodes on one
// provider with max_concurrent: 1 never run at the same time.
//
// The proof is a pair of timestamps written OUTSIDE the lock directory. Each
// node records when it started and when it finished; the assertion is that no
// start falls inside the other node's interval. A cap that was ignored would
// let both nodes start before either finished, and the overlap would show up
// here.
func TestProviderAdmissionMaxConcurrentIsHonoured(t *testing.T) {
	root := t.TempDir()
	yaml := `
version: 1
run: maxconcurrent
max_parallel: 4
provider_policy:
  provider_limits:
    p:
      max_concurrent: 1
nodes:
  - id: a
    kind: IMPL
    title: first
    provider: p
    harness: exec
    artifacts: [{path: artifacts/a.txt}]
  - id: b
    kind: IMPL
    title: second
    provider: p
    harness: exec
    artifacts: [{path: artifacts/b.txt}]
`
	p, err := plan.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	p.Root = root
	if issues := p.Validate(); len(issues) > 0 {
		t.Fatalf("plan invalid: %v", issues)
	}
	dir := filepath.Join(root, ".dagrun")
	st, err := store.Open(dir, p, []byte(yaml), true)
	if err != nil {
		t.Fatal(err)
	}
	// Every node appends its start and end to ONE log in the repo root. The
	// node also holds its work for 200ms, so an ignored cap would produce two
	// "start" lines before any "end" line.
	script := filepath.Join(root, "tmpl.sh")
	if err := os.WriteFile(script, []byte(`#!/bin/sh
mkdir -p artifacts
prompt=$(cat)
case "$prompt" in
  *a.txt*) name=a ;;
  *b.txt*) name=b ;;
esac
echo "start $name $(date +%s%N)" >> timing.log
sleep 0.2
echo "end $name $(date +%s%N)" >> timing.log
echo ok > artifacts/$name.txt
exit 0
`), 0o755); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	ex := New(st, trace.New(filepath.Join(dir, "trace")), Options{
		Workers:        2,
		Owner:          "test",
		Lease:          time.Minute,
		ExecTemplate:   "sh -c " + script + " {stdin}",
		Out:            &out,
		Admission:      plan.NewAdmission(),
		ProviderPolicy: p.ProviderPolicy,
	})
	ex.opt.Admission.ApplyPolicy(p.ProviderPolicy)

	sum, err := ex.Loop(context.Background())
	if err != nil {
		t.Fatalf("loop: %v (out: %s)", err, out.String())
	}
	if sum.Done != 2 {
		t.Fatalf("summary = %+v, want 2 done (out: %s)", sum, out.String())
	}

	raw, err := os.ReadFile(filepath.Join(root, "timing.log"))
	if err != nil {
		t.Fatalf("timing log: %v", err)
	}
	type mark struct {
		kind string
		at   int64
	}
	var marks []mark
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		f := strings.Fields(line)
		if len(f) != 3 {
			continue
		}
		var ts int64
		if _, serr := fmt.Sscanf(f[2], "%d", &ts); serr != nil {
			continue
		}
		marks = append(marks, mark{kind: f[0], at: ts})
	}
	if len(marks) != 4 {
		t.Fatalf("timing log has %d marks, want 4 (start/end for two nodes): %q", len(marks), string(raw))
	}
	// Walk the marks. A start while another node is still open is an overlap.
	open := 0
	maxOpen := 0
	for _, m := range marks {
		switch m.kind {
		case "start":
			open++
			if open > maxOpen {
				maxOpen = open
			}
		case "end":
			open--
		}
	}
	if maxOpen > 1 {
		t.Fatalf("max concurrent nodes was %d, want 1 — the per-provider cap was ignored (out: %s)", maxOpen, out.String())
	}
}
