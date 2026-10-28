package run

import (
	"context"
	"fmt"
	"time"

	"github.com/KooshaPari/dagrun/internal/plan"
	"github.com/KooshaPari/dagrun/internal/store"
	"github.com/KooshaPari/dagrun/internal/trace"
)

// expand materialises fan-out children for every done discover node.
//
// It is safe to call on every scheduler tick: the store records expansion per
// parent, so a second call is a no-op. A malformed work set fails the parent
// rather than silently expanding nothing, because "the plan said do N things
// and we did zero" is exactly the silent failure this tool exists to prevent.
func (e *Executor) expand(ctx context.Context, snap *store.State) (int, error) {
	fanInOf := map[string]string{}
	for _, n := range e.st.Plan().Nodes {
		if n != nil && n.FanIn != "" {
			fanInOf[n.FanIn] = n.ID
		}
	}

	added := 0
	for _, n := range e.st.Plan().Sorted() {
		if n == nil || n.Discover == nil {
			continue
		}
		ns := snap.Nodes[n.ID]
		if ns == nil || ns.Status != plan.StatusDone {
			continue
		}
		if store.Expanded(snap, n.ID) {
			continue
		}

		units, err := plan.LoadUnits(e.st.Root(), *n.Discover)
		if err != nil {
			if ferr := e.st.Force(n.ID, plan.StatusFailed, "discover: "+err.Error(), ns.Verify, time.Now()); ferr != nil {
				return added, ferr
			}
			_ = e.tr.Emit(e.opt.Owner, trace.Event{Run: e.st.Plan().Run, Worker: e.opt.Owner, Node: n.ID,
				Event: "node.failed", Message: "discover: " + err.Error()})
			fmt.Fprintf(e.opt.Out, "discover %s failed: %v\n", n.ID, err)
			continue
		}

		template := e.templateFor(n.ID)
		var children []*plan.Node
		if template != nil {
			children = plan.Children(template, n.ID, units)
		}

		k, merr := e.st.Materialize(n.ID, children, fanInOf[n.ID])
		if merr != nil {
			return added, merr
		}
		added += k
		// The template itself is not a unit of work; record that it was
		// expanded so status and the run summary do not report it as pending.
		if template != nil {
			_ = e.st.Force(template.ID, plan.StatusSkipped,
				fmt.Sprintf("expanded into %d child node(s)", k), nil, time.Now())
		}
		msg := fmt.Sprintf("%d unit(s)", len(units))
		if k != len(units) {
			msg = fmt.Sprintf("%d unit(s), %d materialised (max_dynamic reached)", len(units), k)
		}
		_ = e.tr.Emit(e.opt.Owner, trace.Event{Run: e.st.Plan().Run, Worker: e.opt.Owner, Node: n.ID,
			Event: "node.expand", Message: msg})
		fmt.Fprintf(e.opt.Out, "expanded %s -> %s\n", n.ID, msg)
	}
	return added, nil
}

// templateFor returns the node declared as fan_out from parentID.
func (e *Executor) templateFor(parentID string) *plan.Node {
	for _, n := range e.st.Plan().Nodes {
		if n != nil && n.FanOut == parentID {
			return n
		}
	}
	return nil
}

// indexAll resolves static and dynamic nodes for prompt rendering.
func indexAll(st *store.Store, snap *store.State) map[string]*plan.Node {
	m := map[string]*plan.Node{}
	for _, n := range st.All(snap) {
		if n != nil && n.ID != "" {
			m[n.ID] = n
		}
	}
	return m
}
