package plan

// The attempt budget lives here, in the plan package, because TWO independent
// places must agree on it:
//
//   - internal/run decides whether a failed node goes back to PENDING.
//   - internal/store decides whether a node may be CLAIMED.
//
// When those two disagreed, the disagreement was invisible and the run failed
// anyway. The run granted a throttled node six attempts; the store enforced the
// plan's raw two and failed the node on the very next tick, so the extra
// attempts never happened. A measured run showed it as:
//
//     n1 status=failed class=rate_limited attempts=1 last_error="exhausted 1 attempt(s)"
//
// The package placement is forced by the dependency graph: internal/run imports
// internal/store, so the store cannot import internal/run. The plan package is
// below both, so both can call it.

// MinRateLimitAttempts is the attempt floor for a throttled node, regardless of
// the plan's own budget.
//
// The default budget is 2 attempts, roughly a minute of patience at the 30s
// default backoff. A measured run burned both attempts inside one provider
// throttle window and failed a node that would have succeeded a few minutes
// later. A throttle is not the node's fault and is temporary by definition, so
// it earns more patience than an agent's own mistake.
//
// It is a floor, never a ceiling: a plan that asks for more keeps its number.
// And it is bounded, so a permanently exhausted quota gives up rather than
// spinning forever.
const MinRateLimitAttempts = 6

// EffectiveAttempts returns the attempt budget for a node, given the failure
// class of its last attempt.
//
// Only rate_limited is raised. Every other class is passed through untouched,
// which is what keeps "the plan said 2" meaning 2 for an agent's own mistakes.
// That distinction is the whole design: patience is for the provider being
// busy, not for the agent being wrong.
//
// A non-positive planMax is clamped to 1, because a budget of zero would mean
// the node can never run at all.
func EffectiveAttempts(planMax int, class string) int {
	if planMax < 1 {
		planMax = 1
	}
	if class == ClassRateLimited && planMax < MinRateLimitAttempts {
		return MinRateLimitAttempts
	}
	return planMax
}
