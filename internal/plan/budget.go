package plan

import "strings"

// Cost budgeting for an unattended run.
//
// A run that nobody is watching needs a ceiling, not a hope. The plan declares
// one number: max_cost_usd. The runtime reads that number from Plan.MaxCostUSD
// (the field the coordinator adds to the Plan struct) and accumulates measured
// spend against it. When the ceiling is crossed the run stops scheduling new
// work; nodes already running are allowed to finish and report.
//
// The YAML key is "max_cost_usd", so a plan says:
//
//	version: 1
//	run: demo
//	max_cost_usd: 25.00
//
// A zero (or absent) value means "no limit", never "nothing may run". That
// matters because the plan format is authored by agents: an omitted field must
// degrade to the old behaviour, not to a run that refuses to start.
//
// The Budget type below exists so this file can own the schema without editing
// plan.go. Plan.MaxCostUSD is the field the runtime actually reads; keep the
// tag identical or the YAML key will silently stop binding.

// Budget is the plan-level spend ceiling. It mirrors the field the runtime
// reads on Plan (Plan.MaxCostUSD) and carries the same YAML/JSON key so the two
// cannot drift. The coordinator adds the field to Plan; this type documents and
// round-trips it in the meantime.
type Budget struct {
	MaxCostUSD float64 `yaml:"max_cost_usd,omitempty" json:"max_cost_usd,omitempty"`
}

// Spend is the accumulated cost of a run: token counts as measured, plus the
// dollar cost those tokens represent at the rate in force when they were
// recorded. Tokens are kept raw so a later rate correction can re-price a run
// without replaying it.
type Spend struct {
	InputTokens     int
	OutputTokens    int
	CacheReadTokens int
	CostUSD         float64
}

// Rate is the price of a model per million tokens. Cache reads are billed
// separately from fresh input because the discount is the whole reason a long
// run is affordable.
type Rate struct {
	InputPerMillion     float64
	OutputPerMillion    float64
	CacheReadPerMillion float64
}

// tokensPerMillion converts a per-million price to a per-token price.
const tokensPerMillion = 1_000_000.0

// Cost computes the dollar cost of a spend at a rate. It prices only the
// tokens; Spend.CostUSD is ignored so this stays a pure function of usage and
// price. A negative token count or rate is treated as zero: a corrupt counter
// must not produce a negative bill that hides real overspend.
func Cost(s Spend, r Rate) float64 {
	in := float64(nonNegativeInt(s.InputTokens))
	out := float64(nonNegativeInt(s.OutputTokens))
	cache := float64(nonNegativeInt(s.CacheReadTokens))

	total := in/tokensPerMillion*nonNegativeFloat(r.InputPerMillion) +
		out/tokensPerMillion*nonNegativeFloat(r.OutputPerMillion) +
		cache/tokensPerMillion*nonNegativeFloat(r.CacheReadPerMillion)
	if total < 0 {
		return 0
	}
	return total
}

// BudgetState tracks the budget of one run. It is a value, not a pointer, so a
// caller can snapshot it for a status line without taking a lock.
type BudgetState struct {
	// Max is the ceiling in dollars. Zero means no limit.
	Max float64
	// Spent is the measured cost so far.
	Spent float64
}

// NewBudget returns a budget state with the given ceiling. A negative ceiling
// is clamped to zero, which means "no limit" rather than "already exceeded".
func NewBudget(maxUSD float64) BudgetState {
	return BudgetState{Max: nonNegativeFloat(maxUSD)}
}

// Add records a node's usage against the budget. Tokens are not retained here
// (the run's ledger owns them); this only accumulates the billable cost.
func (b *BudgetState) Add(s Spend, r Rate) {
	if b == nil {
		return
	}
	b.Spent += Cost(s, r)
}

// Exceeded reports whether the budget is spent. A zero Max means no limit, so
// it is never exceeded. The comparison is strict: spending exactly the ceiling
// is still inside it, and only the next dollar crosses.
func (b *BudgetState) Exceeded() bool {
	if b == nil || b.Max <= 0 {
		return false
	}
	return b.Spent > b.Max
}

// Remaining is the unspent budget. It never goes negative: an overspent run
// reports zero headroom, not a negative one that could re-enable scheduling if
// it were summed. With no limit (Max <= 0) it returns 0, since there is no
// meaningful headroom figure to report.
func (b *BudgetState) Remaining() float64 {
	if b == nil || b.Max <= 0 {
		return 0
	}
	left := b.Max - b.Spent
	if left < 0 {
		return 0
	}
	return left
}

func nonNegativeInt(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

func nonNegativeFloat(f float64) float64 {
	if f < 0 {
		return 0
	}
	return f
}

// DefaultRates is the fallback price table, keyed by model name as it appears
// in a node's "model" field. Prices are USD per million tokens.
//
// These are indicative; override with the plan's rate table. Providers change
// prices without notice and a stale table silently mis-budgets a run, so the
// runtime should treat a miss here as "unknown price" and refuse to guess
// rather than billing at zero.
var DefaultRates = map[string]Rate{
	// Frontier Claude: expensive output, steep cache discount.
	"claude-opus-4": {InputPerMillion: 15.00, OutputPerMillion: 75.00, CacheReadPerMillion: 1.50},
	// Workhorse Claude: the usual default for implementation nodes.
	"claude-sonnet-4": {InputPerMillion: 3.00, OutputPerMillion: 15.00, CacheReadPerMillion: 0.30},
	// Cheap Claude: triage, summarisation, classification.
	"claude-haiku-4": {InputPerMillion: 0.80, OutputPerMillion: 4.00, CacheReadPerMillion: 0.08},
	// OpenAI frontier.
	"gpt-5": {InputPerMillion: 1.25, OutputPerMillion: 10.00, CacheReadPerMillion: 0.125},
	// DeepSeek: very cheap output, small cache discount.
	"deepseek-v4.1-flash": {InputPerMillion: 0.28, OutputPerMillion: 0.42, CacheReadPerMillion: 0.028},
	// MiniMax: mid-tier, strong cache discount.
	"minimax-m3": {InputPerMillion: 0.30, OutputPerMillion: 1.20, CacheReadPerMillion: 0.03},
	// Gemini: cheap input, moderate output.
	"gemini-2.5-pro": {InputPerMillion: 1.25, OutputPerMillion: 10.00, CacheReadPerMillion: 0.31},
}

// DefaultRate returns the price for a model, falling back to a mid-range rate
// for a model the table does not know.
//
// A missing rate must not mean a free run: an unknown model is priced at the
// default rather than at zero.
func DefaultRate(model string) Rate {
	if r, ok := DefaultRates[model]; ok {
		return r
	}
	for name, r := range DefaultRates {
		if strings.Contains(model, name) || strings.Contains(name, model) {
			return r
		}
	}
	return Rate{InputPerMillion: 3, OutputPerMillion: 15, CacheReadPerMillion: 0.3}
}
