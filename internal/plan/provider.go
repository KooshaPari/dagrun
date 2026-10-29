package plan

import (
	"fmt"
	"sync"
	"time"
)

// Per-provider admission control.
//
// The point of this file is that a provider rate limit must degrade
// THROUGHPUT, never correctness. When a provider starts refusing work we do
// not fail the run and we do not mark nodes bad: we stop handing that provider
// new work for a while, let the other providers keep going, and start again
// once the cooldown expires. Nodes that never got a slot stay pending; they are
// not failures.
//
// Two numbers describe a provider:
//
//	max_concurrent  how many of its nodes may run at the same time (<=0: no cap)
//	backoff         how long to pause the provider after a failure ("30s", "2m")
//	cooldown_after  how many consecutive failures before that pause (<=0: never)
//
// The YAML key is provider_limits, so a plan says:
//
//	provider_limits:
//	  anthropic:
//	    max_concurrent: 4
//	    backoff: 30s
//	    cooldown_after: 2
//	  openai:
//	    max_concurrent: 8
//	    backoff: 2m
//	    cooldown_after: 3
//
// A zero (or absent) provider_limits map means every provider is uncapped and
// never cools down: an omitted field degrades to the old behaviour, it does not
// stop the run. The same is true per field.
//
// Plan.ProviderPolicy is the field the runtime reads; this file owns the schema
// so it can be written without editing plan.go. Keep the YAML tags identical or
// the key silently stops binding.

// DefaultProvider is the provider name used when a node names none.
const DefaultProvider = "default"

// ProviderLimits caps how many nodes may run at once per provider.
type ProviderLimits struct {
	// MaxConcurrent caps simultaneous nodes per provider. Zero or negative
	// means "no cap for this provider": the old behaviour.
	MaxConcurrent int `yaml:"max_concurrent,omitempty" json:"max_concurrent,omitempty"`
	// Backoff is how long to wait after a provider failure. It is a Go
	// duration string ("30s", "2m"). An unparseable value is reported by
	// Validate; it is never silently ignored.
	Backoff string `yaml:"backoff,omitempty" json:"backoff,omitempty"`
	// CooldownAfter is how many consecutive failures trigger a cooldown.
	// Zero or negative means "never cool down".
	CooldownAfter int `yaml:"cooldown_after,omitempty" json:"cooldown_after,omitempty"`
}

// ProviderPolicy maps a provider name to its limits.
type ProviderPolicy struct {
	Limits map[string]ProviderLimits `yaml:"provider_limits,omitempty" json:"provider_limits,omitempty"`
}

// LimitsFor returns the limits declared for a provider, or the zero value
// (uncapped, no cooldown) when the policy does not mention it. An undeclared
// provider must keep running: a missing entry is not a cap of zero.
func (p ProviderPolicy) LimitsFor(provider string) ProviderLimits {
	if p.Limits == nil {
		return ProviderLimits{}
	}
	return p.Limits[provider]
}

// BackoffDuration parses Backoff. An empty string is zero, meaning "no pause".
func (l ProviderLimits) BackoffDuration() (time.Duration, error) {
	if l.Backoff == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(l.Backoff)
	if err != nil {
		return 0, fmt.Errorf("backoff %q: %w", l.Backoff, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("backoff %q: must not be negative", l.Backoff)
	}
	return d, nil
}

// Validate reports provider limits that cannot be honoured. A bad duration is
// an error here rather than at Acquire time, so a typo in a plan fails at plan
// validation instead of quietly turning into "no cooldown".
func (p ProviderPolicy) Validate() []Issue {
	var out []Issue
	for name, l := range p.Limits {
		if _, err := l.BackoffDuration(); err != nil {
			out = append(out, Issue{
				Node:   "",
				Code:   "bad_provider_backoff",
				Detail: fmt.Sprintf("provider %q: %v", name, err),
			})
		}
		if l.CooldownAfter < 0 {
			out = append(out, Issue{
				Node:   "",
				Code:   "bad_provider_cooldown",
				Detail: fmt.Sprintf("provider %q: cooldown_after %d must not be negative", name, l.CooldownAfter),
			})
		}
	}
	return out
}

// ProviderOf returns the provider a node runs against. A node that names no
// provider still runs against something, so it gets DefaultProvider rather than
// an empty string: an empty key would merge every unnamed node into one
// anonymous bucket and make the limits unaddressable.
func ProviderOf(n *Node) string {
	if n == nil || n.Provider == "" {
		return DefaultProvider
	}
	return n.Provider
}

// Failure classes that belong to the PROVIDER. These mirror the class strings
// in internal/run/failure.go; they are duplicated as literals because run
// imports plan and importing run here would be a cycle. Keep them in sync.
//
// The distinction is the whole point of per-provider admission:
//
//   - harness_error and timeout mean the provider refused the work or was too
//     slow. That is the provider's fault: cool it down, let other providers
//     keep the run moving.
//   - contract_miss (and check_failed) mean the agent produced the wrong thing.
//     That is the agent's fault. Cooling the provider down for it would punish
//     a perfectly healthy provider for a bad prompt and would stall every other
//     node on that provider. Never pass these to Fail.
const (
	ClassHarnessError = "harness_error"
	ClassTimeout      = "timeout"
	ClassContractMiss = "contract_miss"
)

// IsProviderFailure reports whether a failure class is the provider's fault and
// therefore grounds for a cooldown. See the class constants above.
func IsProviderFailure(class string) bool {
	switch class {
	case ClassHarnessError, ClassTimeout:
		return true
	default:
		return false
	}
}

// Admission is the per-provider concurrency state of one run.
//
// It is safe for concurrent use: the scheduler acquires and releases from
// several goroutines at once. All state is guarded by one mutex; the critical
// sections are a few map operations, so contention is not a concern.
type Admission struct {
	mu       sync.Mutex
	running  map[string]int
	failures map[string]int
	until    map[string]time.Time
	// limits is the cap and cooldown policy, keyed by provider. It is loaded
	// once from the plan before the run starts and only read afterwards, but
	// it is guarded by the same mutex so a late SetLimits is safe too.
	limits map[string]ProviderLimits
}

// NewAdmission returns an empty admission state: every provider uncapped and
// never cooling down until limits are loaded.
func NewAdmission() *Admission {
	return &Admission{
		running:  make(map[string]int),
		failures: make(map[string]int),
		until:    make(map[string]time.Time),
		limits:   make(map[string]ProviderLimits),
	}
}

// SetLimits loads the limits for one provider. Call it before the run starts;
// calling it afterwards is safe but only affects future decisions.
func (a *Admission) SetLimits(provider string, l ProviderLimits) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.limits[provider] = l
}

// ApplyPolicy loads a whole ProviderPolicy.
func (a *Admission) ApplyPolicy(p ProviderPolicy) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for name, l := range p.Limits {
		a.limits[name] = l
	}
}

// Acquire reserves a slot for a provider. It returns false when the provider is
// at its cap or is in a cooldown. A false return is not a failure: the caller
// should leave the node pending and try again later.
func (a *Admission) Acquire(provider string, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	if until, ok := a.until[provider]; ok {
		if now.Before(until) {
			return false
		}
		// The cooldown expired; drop the entry so the map does not grow and
		// so the provider starts its next window with a clean count.
		delete(a.until, provider)
		delete(a.failures, provider)
	}

	if limit := a.limits[provider].MaxConcurrent; limit > 0 && a.running[provider] >= limit {
		return false
	}
	a.running[provider]++
	return true
}

// Release frees a slot. It never goes below zero: a stray Release must not hand
// the provider an extra slot and overshoot its cap.
func (a *Admission) Release(provider string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.running[provider] <= 0 {
		delete(a.running, provider)
		return
	}
	a.running[provider]--
	if a.running[provider] == 0 {
		delete(a.running, provider)
	}
}

// Fail records a provider failure and puts it in cooldown when the threshold is
// reached. Call it only for a provider failure (harness_error, timeout); see
// IsProviderFailure. The failure counter restarts after a cooldown so the next
// window needs another CooldownAfter failures.
//
// An unparseable Backoff cannot happen here in practice: Validate rejects it
// before a run starts. If it does happen the cooldown is set to now, i.e. it is
// already over, so a bad plan string never wedges the run.
func (a *Admission) Fail(provider string, limits ProviderLimits, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.failures[provider]++
	if limits.CooldownAfter <= 0 || a.failures[provider] < limits.CooldownAfter {
		return
	}
	d, err := limits.BackoffDuration()
	if err != nil {
		d = 0
	}
	a.until[provider] = now.Add(d)
	delete(a.failures, provider)
}

// Success resets the failure count for a provider. A success means the provider
// is healthy again, so it also clears a cooldown: one good answer is better
// evidence than the failures that preceded it.
func (a *Admission) Success(provider string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.failures, provider)
	delete(a.until, provider)
}

// InCooldown reports whether a provider is cooling down at now.
func (a *Admission) InCooldown(provider string, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	until, ok := a.until[provider]
	return ok && now.Before(until)
}

// Running reports the live count for a provider.
func (a *Admission) Running(provider string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.running[provider]
}

// Failures reports the consecutive failure count for a provider.
func (a *Admission) Failures(provider string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.failures[provider]
}
