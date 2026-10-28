// Lease and ownership: the only operations that change a node's status.
//
// Every mutating call is a compare-and-set against owner+epoch, so a worker
// whose lease expired cannot write a result over its successor's.
package store

import (
	"fmt"
	"time"

	"github.com/KooshaPari/dagrun/internal/plan"
	"github.com/KooshaPari/dagrun/internal/verify"
)

// Resolve returns a node by id, preferring a runtime-materialised one over the
// static plan so a fan_out child or an expanded fan_in reduce is visible to
// every check that walks the graph.
func Resolve(st *State, id string) *plan.Node {
	if st != nil && st.Dynamic != nil {
		if n, ok := st.Dynamic[id]; ok && n != nil {
			return n
		}
	}
	return nil
}

// All returns static and dynamic nodes in WBS order.
func (s *Store) All(st *State) []*plan.Node {
	out := append([]*plan.Node(nil), s.plan.Nodes...)
	static := make(map[string]bool, len(s.plan.Nodes))
	for _, n := range s.plan.Nodes {
		if n != nil {
			static[n.ID] = true
		}
	}
	for id, n := range st.Dynamic {
		// A fan_in node is materialised only to grow its dependency list; its
		// static declaration is still the node of record.
		if n == nil || id == "" || static[id] {
			continue
		}
		out = append(out, n)
	}
	return orderNodes(out)
}

func orderNodes(nodes []*plan.Node) []*plan.Node {
	out := make([]*plan.Node, 0, len(nodes))
	for _, n := range nodes {
		if n != nil {
			out = append(out, n)
		}
	}
	// insertion sort by natural id order: n is small and this keeps the
	// dependency on plan.Less explicit rather than pulling in sort twice
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && plan.Less(out[j].ID, out[j-1].ID); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// lookup resolves id against the plan and the run's dynamic set.
func (s *Store) lookup(st *State, id string) *plan.Node {
	if n := Resolve(st, id); n != nil {
		return n
	}
	return s.plan.ByID(id)
}

// DepsDone reports whether every dependency of a node is done or skipped.
func (s *Store) DepsDone(st *State, nodeID string) bool {
	n := s.lookup(st, nodeID)
	if n == nil {
		return false
	}
	for _, d := range n.Deps {
		ns := st.Nodes[d]
		if ns == nil {
			continue
		}
		if ns.Status != plan.StatusDone && ns.Status != plan.StatusSkipped {
			return false
		}
	}
	return true
}

func (s *Store) node(id string) *plan.Node {
	return s.plan.ByID(id)
}

// Claim atomically takes a node for owner. It enforces dependency order,
// terminal states, attempt budget, and both concurrency caps.
func (s *Store) Claim(nodeID, owner string, ttl time.Duration, now time.Time) (Claim, error) {
	var out Claim
	err := s.With(func(st *State) error {
		ns := st.Nodes[nodeID]
		if ns == nil {
			out.Reason = "unknown node"
			return nil
		}
		switch ns.Status {
		case plan.StatusDone, plan.StatusSkipped, plan.StatusCancelled:
			out.Reason = "node is " + ns.Status
			return nil
		case plan.StatusFailed:
			out.Reason = "node was failed and is not retried automatically"
			return nil
		case plan.StatusRunning:
			if ns.LeaseUntil.After(now) && ns.Owner != owner {
				out.Reason = "leased by " + ns.Owner
				return nil
			}
		}
		n := s.lookup(st, nodeID)
		if n == nil {
			out.Reason = "node not in plan"
			return nil
		}
		if !s.DepsDone(st, nodeID) {
			out.Reason = "dependencies not done"
			return nil
		}
		if ns.Attempts >= n.MaxAttempts {
			ns.Status = plan.StatusFailed
			ns.LastError = fmt.Sprintf("exhausted %d attempt(s)", n.MaxAttempts)
			out.Reason = ns.LastError
			return nil
		}
		if reason := s.capBlocked(st, n, now); reason != "" {
			out.Reason = reason
			return nil
		}

		ns.Status = plan.StatusRunning
		ns.Owner = owner
		ns.Epoch++
		ns.Attempts++
		ns.StartedAt = now
		ns.LeaseUntil = now.Add(ttl)
		ns.LastError = ""
		out.Won = true
		out.Epoch = ns.Epoch
		return nil
	})
	return out, err
}

func (s *Store) capBlocked(st *State, n *plan.Node, now time.Time) string {
	running := 0
	byRole := map[string]int{}
	for id, ns := range st.Nodes {
		if ns.Status != plan.StatusRunning || !ns.LeaseUntil.After(now) {
			continue
		}
		running++
		if node := s.lookup(st, id); node != nil {
			byRole[node.Role]++
		}
	}
	if s.plan.MaxParallel > 0 && running >= s.plan.MaxParallel {
		return fmt.Sprintf("max_parallel %d reached", s.plan.MaxParallel)
	}
	if cap, ok := s.plan.RoleCaps[n.Role]; ok && cap > 0 && byRole[n.Role] >= cap {
		return fmt.Sprintf("role cap %d for %q reached", cap, n.Role)
	}
	return ""
}

// Heartbeat extends a lease if the caller still owns the node at that epoch.
func (s *Store) Heartbeat(nodeID, owner string, epoch int64, ttl time.Duration, now time.Time) (bool, error) {
	ok := false
	err := s.With(func(st *State) error {
		ns := st.Nodes[nodeID]
		if ns == nil || ns.Owner != owner || ns.Epoch != epoch || ns.Status != plan.StatusRunning {
			return nil
		}
		ns.LeaseUntil = now.Add(ttl)
		ok = true
		return nil
	})
	return ok, err
}

// Finish records a terminal status. It fails closed: if the caller has lost
// ownership (stale epoch), nothing is written and false is returned.
func (s *Store) Finish(nodeID, owner string, epoch int64, status string, rep *verify.Report, exitCode int, lastErr string, now time.Time) (bool, error) {
	ok := false
	err := s.With(func(st *State) error {
		ns := st.Nodes[nodeID]
		if ns == nil || ns.Owner != owner || ns.Epoch != epoch {
			return nil
		}
		ns.Status = status
		ns.Owner = ""
		ns.LeaseUntil = time.Time{}
		ns.FinishedAt = now
		ns.ExitCode = exitCode
		ns.LastError = lastErr
		ns.Verify = rep
		ok = true
		return nil
	})
	return ok, err
}

// Force sets a status regardless of ownership. Used by `dagrun done|fail|skip`
// when a human or a driving agent attests the outcome directly.
func (s *Store) Force(nodeID, status, reason string, rep *verify.Report, now time.Time) error {
	return s.With(func(st *State) error {
		ns := st.Nodes[nodeID]
		if ns == nil {
			return fmt.Errorf("unknown node %q", nodeID)
		}
		ns.Status = status
		ns.Owner = ""
		ns.LeaseUntil = time.Time{}
		ns.FinishedAt = now
		ns.LastError = reason
		if rep != nil {
			ns.Verify = rep
		}
		return nil
	})
}
