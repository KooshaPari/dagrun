// Package run is the executor: pick ready nodes, claim them, drive a child
// agent, verify the artifacts, and record the outcome.
//
// Correctness rests on two rules. A node is only "done" when its artifacts
// verify, and only the current lease holder with the current epoch may write a
// terminal state. Everything else is scheduling.
package run

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/KooshaPari/dagrun/internal/plan"
	"github.com/KooshaPari/dagrun/internal/store"
	"github.com/KooshaPari/dagrun/internal/trace"
	"github.com/KooshaPari/dagrun/internal/verify"
)

// Options configures one executor run.
type Options struct {
	Workers      int
	Owner        string
	Lease        time.Duration
	Once         bool
	DryRun       bool
	Verbose      bool
	ExecTemplate string
	Tools        []string
	Out          io.Writer
}

// Summary is the outcome of a run loop.
type Summary struct {
	Done      int      `json:"done"`
	Failed    int      `json:"failed"`
	Skipped   int      `json:"skipped"`
	Pending   int      `json:"pending"`
	Adopted   int      `json:"adopted"`
	Ran       int      `json:"ran"`
	Remaining []string `json:"remaining,omitempty"`
}

// Executor holds the per-process state of a run loop.
type Executor struct {
	st       *store.Store
	tr       *trace.Writer
	opt      Options
	active   int64
	wg       sync.WaitGroup
	errMu    sync.Mutex
	firstErr error
}

// New builds an executor.
func New(st *store.Store, tr *trace.Writer, opt Options) *Executor {
	if opt.Workers < 1 {
		opt.Workers = 1
	}
	if opt.Lease <= 0 {
		opt.Lease = 2 * time.Minute
	}
	if opt.Out == nil {
		opt.Out = os.Stderr
	}
	return &Executor{st: st, tr: tr, opt: opt}
}

// Loop schedules work until every node is terminal or nothing can progress.
func (e *Executor) Loop(ctx context.Context) (Summary, error) {
	_ = e.tr.Emit(e.opt.Owner, trace.Event{Run: e.st.Plan().Run, Worker: e.opt.Owner, Event: "run.start"})

	for {
		if ctx.Err() != nil {
			break
		}
		if reaped, err := e.st.ResetExpired(time.Now()); err == nil && len(reaped) > 0 {
			for _, id := range reaped {
				_ = e.tr.Emit(e.opt.Owner, trace.Event{Run: e.st.Plan().Run, Worker: e.opt.Owner, Node: id, Event: "lease.reap"})
			}
		}
		snap, err := e.st.Snapshot()
		if err != nil {
			return Summary{}, err
		}

		// A discover node that has verified turns its work set into real nodes.
		// Expansion is idempotent per parent, so several executors can call it.
		// Always re-snapshot afterwards: expansion mutates the run even when it
		// materialises zero children (an expanded marker, a skipped template).
		if _, xerr := e.expand(ctx, snap); xerr != nil {
			e.setErr(xerr)
		} else if fresh, serr := e.st.Snapshot(); serr == nil {
			snap = fresh
		}
		idx := indexAll(e.st, snap)

		launched := 0
		for _, n := range e.st.All(snap) {
			if atomic.LoadInt64(&e.active) >= int64(e.opt.Workers) {
				break
			}
			if n.FanOut != "" {
				continue // a template is executed as its children, never itself
			}
			ns := snap.Nodes[n.ID]
			if ns == nil || ns.Status != plan.StatusPending {
				continue
			}
			if n.FanIn != "" && !store.Expanded(snap, n.FanIn) {
				continue // the reduce cannot run before the map has expanded
			}
			if !e.st.DepsDone(snap, n.ID) {
				continue
			}
			if e.adopt(ctx, n) {
				launched++
				continue
			}
			if !runnable(n) {
				e.finishDirect(n, plan.StatusSkipped, "no execution path: no prompt, no check, no harness")
				continue
			}
			if e.opt.DryRun {
				fmt.Fprintf(e.opt.Out, "would run %s (%s) via %s\n", n.ID, n.Kind, harnessName(n))
				e.finishDirect(n, plan.StatusSkipped, "dry run")
				continue
			}
			claim, cerr := e.st.Claim(n.ID, e.opt.Owner, e.opt.Lease, time.Now())
			if cerr != nil {
				e.setErr(cerr)
				continue
			}
			if !claim.Won {
				continue // another process won the race, or a cap is full
			}
			atomic.AddInt64(&e.active, 1)
			launched++
			e.wg.Add(1)
			go func(node *plan.Node, epoch int64) {
				defer e.wg.Done()
				defer atomic.AddInt64(&e.active, -1)
				e.execute(ctx, node, epoch, idx)
			}(n, claim.Epoch)
			_ = e.tr.Emit(e.opt.Owner, trace.Event{Run: e.st.Plan().Run, Worker: e.opt.Owner, Node: n.ID, Event: "node.claim", Epoch: claim.Epoch})
			if e.opt.Once {
				break
			}
		}

		running := atomic.LoadInt64(&e.active)
		if launched == 0 && running == 0 {
			// nothing running here and nothing new to start: settle the rest
			if e.settle(ctx) {
				break
			}
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(150 * time.Millisecond):
		}
	}

	e.wg.Wait()
	sum := e.summarize()
	_ = e.tr.Emit(e.opt.Owner, trace.Event{Run: e.st.Plan().Run, Worker: e.opt.Owner, Event: "run.end",
		Message: fmt.Sprintf("done=%d failed=%d skipped=%d pending=%d", sum.Done, sum.Failed, sum.Skipped, sum.Pending)})
	return sum, e.firstErr
}

// settle marks unreachable nodes so the run terminates with a clear reason
// instead of spinning forever.
func (e *Executor) settle(ctx context.Context) bool {
	snap, err := e.st.Snapshot()
	if err != nil {
		return true
	}
	changed := false
	for _, n := range e.st.All(snap) {
		if n.FanOut != "" {
			continue
		}
		ns := snap.Nodes[n.ID]
		if ns == nil || ns.Status != plan.StatusPending {
			continue
		}
		if n.FanIn != "" && !store.Expanded(snap, n.FanIn) {
			// The discovery parent never produced a work set.
			switch ps := snap.Nodes[n.FanIn]; {
			case ps == nil:
				e.finishDirect(n, plan.StatusFailed, "fan_in parent "+n.FanIn+" is not in this run")
			case ps.Status == plan.StatusFailed || ps.Status == plan.StatusCancelled:
				e.finishDirect(n, plan.StatusFailed, "fan_in parent "+n.FanIn+" is "+ps.Status)
			case ps.Status == plan.StatusDone:
				e.finishDirect(n, plan.StatusSkipped, "work set is empty")
			}
			changed = true
			continue
		}
		if e.adopt(ctx, n) {
			changed = true
			continue
		}
		if !e.st.DepsDone(snap, n.ID) {
			var missing []string
			for _, d := range n.Deps {
				if ds := snap.Nodes[d]; ds != nil && ds.Status != plan.StatusDone && ds.Status != plan.StatusSkipped {
					missing = append(missing, d+"="+ds.Status)
				}
			}
			e.finishDirect(n, plan.StatusFailed, "blocked on "+strings.Join(missing, ", "))
			changed = true
			continue
		}
		if !runnable(n) {
			e.finishDirect(n, plan.StatusSkipped, "no execution path")
			changed = true
		}
	}
	return changed
}

func runnable(n *plan.Node) bool {
	if n.Check != "" || n.Prompt != "" {
		return true
	}
	return len(n.Artifacts) > 0 // dagrun generates the prompt from the contract
}

func harnessName(n *plan.Node) string {
	if n.Check != "" && len(n.Artifacts) == 0 {
		return "check"
	}
	if n.Harness == "" {
		return "auto"
	}
	return n.Harness
}

// adopt accepts a node whose artifacts already satisfy the contract. This is
// the duplicate-work guard: re-running the tool never redoes finished nodes.
func (e *Executor) adopt(ctx context.Context, n *plan.Node) bool {
	if !verify.Satisfied(ctx, e.st.Root(), n) {
		return false
	}
	rep := verify.Node(ctx, e.st.Root(), n)
	if err := e.st.Force(n.ID, plan.StatusDone, "adopted: artifacts already satisfy the contract", &rep, time.Now()); err != nil {
		return false
	}
	_ = e.tr.Emit(e.opt.Owner, trace.Event{Run: e.st.Plan().Run, Worker: e.opt.Owner, Node: n.ID, Event: "node.adopt", Message: "artifacts already valid"})
	fmt.Fprintf(e.opt.Out, "adopted %s (%s)\n", n.ID, n.Kind)
	return true
}

func (e *Executor) finishDirect(n *plan.Node, status, reason string) {
	if err := e.st.Force(n.ID, status, reason, nil, time.Now()); err != nil {
		e.setErr(err)
	}
	_ = e.tr.Emit(e.opt.Owner, trace.Event{Run: e.st.Plan().Run, Worker: e.opt.Owner, Node: n.ID, Event: "node." + status, Message: reason})
}

func (e *Executor) summarize() Summary {
	snap, err := e.st.Snapshot()
	if err != nil {
		return Summary{}
	}
	var s Summary
	for _, n := range e.st.All(snap) {
		ns := snap.Nodes[n.ID]
		if ns == nil {
			continue
		}
		switch ns.Status {
		case plan.StatusDone:
			s.Done++
		case plan.StatusFailed:
			s.Failed++
			s.Remaining = append(s.Remaining, n.ID)
		case plan.StatusSkipped:
			s.Skipped++
		default:
			s.Pending++
			s.Remaining = append(s.Remaining, n.ID)
		}
	}
	return s
}

func (e *Executor) setErr(err error) {
	e.errMu.Lock()
	defer e.errMu.Unlock()
	if e.firstErr == nil {
		e.firstErr = err
	}
}

// sessionEnvDrop are variables that would bind a child process to the session
// that launched dagrun instead of making it an independent worker.
//
// This is not cosmetic. Observed: a child jcode inherited the parent's
// JCODE_RUNTIME_PROVIDER/JCODE_ACTIVE_PROVIDER and routed a request to the
// wrong provider, returning HTTP 400 "unknown model" for a model that works
// fine when the child starts clean.
var sessionEnvDrop = map[string]bool{
	"JCODE_SOCKET":                    true,
	"JCODE_SESSION_ID":                true,
	"JCODE_RUNTIME_PROVIDER":          true,
	"JCODE_ACTIVE_PROVIDER":           true,
	"JCODE_INITIAL_PROVIDER_EXPLICIT": true,
	"JCODE_RESUMING":                  true,
	"JCODE_NON_INTERACTIVE":           true,
	"CODEX_SESSION_ID":                true,
	"FORGE_SESSION_ID":                true,
	"HELIOSLITE_SESSION_ID":           true,
}

var sessionEnvDropPrefix = []string{"JCODE_TUI_INHERITED_"}

// childEnv gives each child a clean, non-interactive environment: the parent's
// session and provider bindings are removed so per-node provider/model choices
// actually take effect.
func childEnv() []string {
	src := os.Environ()
	env := make([]string, 0, len(src)+2)
	for _, kv := range src {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		k := kv[:i]
		if sessionEnvDrop[k] {
			continue
		}
		drop := false
		for _, pre := range sessionEnvDropPrefix {
			if strings.HasPrefix(k, pre) {
				drop = true
				break
			}
		}
		if drop {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "DAGRUN=1", "CI=1")
}
