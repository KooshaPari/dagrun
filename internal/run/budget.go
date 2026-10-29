package run

import (
	"sync"

	"github.com/KooshaPari/dagrun/internal/plan"
)

// SharedBudget is a cost ceiling that several executors charge against.
//
// The problem it solves: a multi-repo workspace runs one executor per repo. If
// each executor owned its own budget, a plan declaring max_cost_usd: 10 would
// allow each repo to spend 10 — so a three-repo workspace could spend 30 while
// the plan said 10. The ceiling has to be a property of the RUN, not of the
// executor.
//
// It is safe for concurrent use: every read and every charge goes through one
// mutex, so the reported spend is the sum of what all repos actually spent.
type SharedBudget struct {
	mu sync.Mutex
	b  plan.BudgetState
}

// NewSharedBudget returns a shared ceiling. A negative ceiling is clamped to
// zero, which means "no limit" rather than "already exceeded".
func NewSharedBudget(maxUSD float64) *SharedBudget {
	return &SharedBudget{b: plan.NewBudget(maxUSD)}
}

// Add charges a spend against the shared ceiling.
func (s *SharedBudget) Add(sp plan.Spend, r plan.Rate) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.b.Add(sp, r)
}

// Exceeded reports whether the shared ceiling has been crossed.
func (s *SharedBudget) Exceeded() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Exceeded()
}

// Spent returns the measured spend and the ceiling.
func (s *SharedBudget) Spent() (spent, max float64) {
	if s == nil {
		return 0, 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Spent, s.b.Max
}
