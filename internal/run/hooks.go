// Node hooks: the shell commands a plan author attaches to a node.
//
// A hook is part of the node's contract, not a convenience. A failing pre hook
// means the child is never started; a failing post hook means the node did not
// verify, even when its artifacts did. Only on_fail is advisory: the node has
// already failed, so a failing cleanup cannot make it worse.
package run

import (
	"context"
	"fmt"

	"github.com/KooshaPari/dagrun/internal/hook"
	"github.com/KooshaPari/dagrun/internal/plan"
	"github.com/KooshaPari/dagrun/internal/trace"
)

// hookSet converts a plan declaration into the hook package's runtime form.
// The two types are deliberately separate: the plan package must not depend on
// the runner, and the runner must not depend on the plan package's layout.
func hookSet(h *plan.Hooks) hook.Hooks {
	if h == nil {
		return hook.Hooks{}
	}
	return hook.Hooks{Pre: h.Pre, Post: h.Post, OnFail: h.OnFail}
}

// hookEnv is the identity a hook is given. Hooks are the only place a plan
// author can reach into the run, so the run must say which node it is.
func (e *Executor) hookEnv(n *plan.Node) []string {
	return []string{
		"DAGRUN_NODE_ID=" + n.ID,
		"DAGRUN_RUN=" + e.st.Plan().Run,
	}
}

// runHook executes one phase ("pre", "post", "on_fail") in dir.
//
// An absent hook is a no-op. The error returned wraps the hook's own output so
// the node's last_error explains what the author's command did.
func (e *Executor) runHook(ctx context.Context, n *plan.Node, epoch int64, phase, dir string) error {
	h := hookSet(n.Hooks)
	if h.Pre == "" && h.Post == "" && h.OnFail == "" {
		return nil
	}

	err := hook.Run(ctx, phase, h, dir, e.hookEnv(n))
	event := "hook." + phase
	msg := ""
	if err != nil {
		msg = err.Error()
		event += ".failed"
	}
	_ = e.tr.Emit(e.opt.Owner, trace.Event{Run: e.st.Plan().Run, Worker: e.opt.Owner, Node: n.ID,
		Event: event, Epoch: epoch, Message: msg})
	if err != nil {
		return fmt.Errorf("%s hook: %w", phase, err)
	}
	return nil
}
