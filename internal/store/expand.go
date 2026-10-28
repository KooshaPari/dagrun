// Expansion: turning a verified work set into real nodes.
//
// The layout stays static. A discover node names an artifact that lists units;
// when that artifact verifies, the run gains one child node per unit. Children
// are ordinary nodes from that moment on: their own leases, artifacts,
// retries, and trace events. Nothing else in the executor changes.
package store

import (
	"github.com/KooshaPari/dagrun/internal/plan"
)

// Materialize adds the children produced by a fan-out parent.
//
// Idempotent by parent: the first call for a parent wins and every later call
// is a no-op, so N executors racing on the same expansion cannot double-add.
// An empty work set still marks the parent expanded, so a legitimately empty
// result is not retried forever.
func (s *Store) Materialize(parentID string, children []*plan.Node, fanInID string) (int, error) {
	added := 0
	err := s.With(func(st *State) error {
		if parentID == "" {
			return nil
		}
		if st.Expanded[parentID] {
			return nil
		}
		st.Expanded[parentID] = true

		budget := s.plan.MaxDynamic - len(st.Dynamic)
		for _, c := range children {
			if budget <= 0 || c == nil {
				break
			}
			if _, dup := st.Nodes[c.ID]; dup {
				continue
			}
			st.Dynamic[c.ID] = c.Clone()
			st.Nodes[c.ID] = &NodeState{Status: plan.StatusPending}
			added++
			budget--
		}

		if fanInID == "" {
			return nil
		}
		base := s.lookup(st, fanInID)
		if base == nil {
			return nil
		}
		upd := base.Clone()
		seen := map[string]bool{}
		for _, d := range upd.Deps {
			seen[d] = true
		}
		for _, c := range children {
			if c == nil || seen[c.ID] {
				continue
			}
			if _, ok := st.Dynamic[c.ID]; !ok {
				continue // not materialised (budget exhausted)
			}
			upd.Deps = append(upd.Deps, c.ID)
			seen[c.ID] = true
		}
		st.Dynamic[fanInID] = upd
		return nil
	})
	return added, err
}

// Expanded reports whether a discover node has already been expanded.
func Expanded(st *State, parentID string) bool {
	if st == nil || st.Expanded == nil {
		return false
	}
	return st.Expanded[parentID]
}

// Children returns the ids materialised for a parent, in id order.
func Children(st *State, parentID string) []string {
	if st == nil {
		return nil
	}
	prefix := parentID + "/"
	var out []string
	for id := range st.Dynamic {
		if len(id) > len(prefix) && id[:len(prefix)] == prefix {
			out = append(out, id)
		}
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && plan.Less(out[j], out[j-1]); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
