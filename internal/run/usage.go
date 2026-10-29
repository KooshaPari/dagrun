// Node usage capture: what the child harness printed, and what it cost.
//
// The child's output is a log file first and a data source second. We keep the
// tail of it in memory so the harness's own accounting (jcode/kcode/codex emit
// a usage object) can be read after the process exits, without re-reading and
// unbounded log off disk.
package run

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/KooshaPari/dagrun/internal/harness"
	"github.com/KooshaPari/dagrun/internal/plan"
	"github.com/KooshaPari/dagrun/internal/trace"
)

// captureLimit bounds the in-memory tail. Token accounting is printed at the
// end of a harness run, so the tail is the part worth keeping; 1 MiB holds a
// final usage event many times over while keeping a 32-worker burst cheap.
const captureLimit = 1 << 20

// tailBuffer keeps the last N bytes written to it.
type tailBuffer struct {
	max int
	buf []byte
}

func newTailBuffer(max int) *tailBuffer {
	return &tailBuffer{max: max}
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	switch {
	case t.max <= 0:
		return n, nil
	case len(p) >= t.max:
		t.buf = append(t.buf[:0], p[len(p)-t.max:]...)
		return n, nil
	}
	overflow := len(t.buf) + len(p) - t.max
	if overflow > 0 {
		t.buf = append(t.buf[:0], t.buf[overflow:]...)
	}
	t.buf = append(t.buf, p...)
	return n, nil
}

// Bytes returns the captured tail.
func (t *tailBuffer) Bytes() []byte { return t.buf }

// logSink writes to the per-node log file and mirrors the tail in memory.
//
// A log write failure is returned so the child sees a broken sink rather than
// silently losing output; the in-memory mirror never fails.
type logSink struct {
	f    *os.File
	tail *tailBuffer
}

func newLogSink(f *os.File) *logSink {
	return &logSink{f: f, tail: newTailBuffer(captureLimit)}
}

func (s *logSink) Write(p []byte) (int, error) {
	n, err := s.f.Write(p)
	s.tail.Write(p)
	return n, err
}

var _ io.Writer = (*logSink)(nil)

// recordUsage parses the captured tail and stores what it found.
//
// DurationMS is recorded even when the harness reported no accounting: the
// wall time is ours to measure and every node has one.
func (e *Executor) recordUsage(n *plan.Node, epoch int64, captured []byte, dur time.Duration) {
	u, err := harness.ParseUsage(captured)
	if err != nil {
		// A malformed line is not fatal: the run's verdict comes from verify.
		// Fall back to the streaming form, which keeps the last good object.
		if nd, nerr := harness.ParseUsageNDJSON(captured); nerr == nil {
			u = nd
		}
	} else if u == nil {
		// Multi-line streams (codex JSONL) never parse as a single object.
		if nd, nerr := harness.ParseUsageNDJSON(captured); nerr == nil {
			u = nd
		}
	}
	ok, rerr := e.st.RecordUsage(n.ID, e.opt.Owner, epoch, u, dur.Milliseconds())
	if rerr != nil {
		e.setErr(rerr)
		return
	}
	if !ok {
		return // lease lost: the successor owns this node's record now
	}
	// Charge the run's budget. The rate comes from the plan when it names one,
	// otherwise from the default table.
	if u != nil {
		rate := e.opt.Rate
		if rate == (plan.Rate{}) {
			rate = plan.DefaultRate(n.Model)
		}
		e.budgetMu.Lock()
		e.budget.Add(plan.Spend{
			InputTokens:     u.InputTokens,
			OutputTokens:    u.OutputTokens,
			CacheReadTokens: u.CacheReadTokens,
		}, rate)
		spent := e.budget.Spent
		e.budgetMu.Unlock()
		if e.budget.Exceeded() {
			fmt.Fprintf(e.opt.Out, "budget spent: $%.2f of $%.2f; no further nodes will be claimed\n",
				spent, e.opt.MaxCostUSD)
			_ = e.tr.Emit(e.opt.Owner, trace.Event{Run: e.st.Plan().Run, Worker: e.opt.Owner,
				Event: "budget.spent", Message: fmt.Sprintf("spent=%.2f max=%.2f", spent, e.opt.MaxCostUSD)})
		}
	}
}
