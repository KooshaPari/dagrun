// Package store is the only mutable state in dagrun: one JSON document per
// run, guarded by an OS file lock, written atomically.
//
// Why not SQLite: every dagrun command is short-lived and holds the lock for
// microseconds, so a rewrite of a few hundred kilobytes beats any dependency.
// Why not in-memory: many independent agent processes share one run, and a
// crash must not lose a claim. The lock is the election; the file is the truth.
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/KooshaPari/dagrun/internal/plan"
	"github.com/KooshaPari/dagrun/internal/verify"
)

// StateVersion is the on-disk state schema version.
const StateVersion = 1

// NodeState is the mutable per-node record.
type NodeState struct {
	Status     string         `json:"status"`
	Attempts   int            `json:"attempts,omitempty"`
	Epoch      int64          `json:"epoch,omitempty"`
	Owner      string         `json:"owner,omitempty"`
	LeaseUntil time.Time      `json:"lease_until,omitempty"`
	StartedAt  time.Time      `json:"started_at,omitempty"`
	FinishedAt time.Time      `json:"finished_at,omitempty"`
	ExitCode   int            `json:"exit_code,omitempty"`
	LastError  string         `json:"last_error,omitempty"`
	Verify     *verify.Report `json:"verify,omitempty"`
}

// State is the whole run document.
type State struct {
	Version   int                   `json:"version"`
	Run       string                `json:"run"`
	PlanHash  string                `json:"plan_hash"`
	CreatedAt time.Time             `json:"created_at"`
	UpdatedAt time.Time             `json:"updated_at"`
	Nodes     map[string]*NodeState `json:"nodes"`
	// Dynamic holds nodes materialised at runtime by fan_out. They are not part
	// of the plan, so the plan hash stays static and a rerun re-expands the
	// same set from the same verified work set.
	Dynamic map[string]*plan.Node `json:"dynamic,omitempty"`
	// Expanded records that a discover node has already been expanded, so an
	// empty work set is not retried on every scheduler tick.
	Expanded map[string]bool `json:"expanded,omitempty"`
}

// Store is a handle to one run directory.
type Store struct {
	dir      string
	root     string
	plan     *plan.Plan
	planHash string
}

// Claim is the result of trying to take a node.
type Claim struct {
	Won    bool
	Epoch  int64
	Reason string
}

// Open prepares a run directory. If state is missing it is created; if it
// exists the plan hash must match, because a run is defined by one plan.
func Open(dir string, p *plan.Plan, planBytes []byte, reset bool) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	for _, sub := range []string{"trace", "logs"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, err
		}
	}
	sum := sha256.Sum256(planBytes)
	s := &Store{dir: dir, root: p.Root, plan: p, planHash: hex.EncodeToString(sum[:])}

	path := s.statePath()
	if _, err := os.Stat(path); err == nil && !reset {
		st, err := s.readState()
		if err != nil {
			return nil, err
		}
		if st.PlanHash != "" && st.PlanHash != s.planHash {
			return nil, fmt.Errorf("plan changed since this run started (%s != %s); rerun with --reset to discard progress",
				short(st.PlanHash), short(s.planHash))
		}
		return s, nil
	}
	return s, s.With(func(st *State) error {
		fresh := s.freshState()
		*st = *fresh
		return nil
	})
}

func short(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

func (s *Store) statePath() string { return filepath.Join(s.dir, "state.json") }

func (s *Store) freshState() *State {
	now := time.Now().UTC()
	st := &State{
		Version:   StateVersion,
		Run:       s.plan.Run,
		PlanHash:  s.planHash,
		CreatedAt: now,
		UpdatedAt: now,
		Nodes:     make(map[string]*NodeState, len(s.plan.Nodes)),
		Dynamic:   map[string]*plan.Node{},
		Expanded:  map[string]bool{},
	}
	for _, n := range s.plan.Nodes {
		if n != nil {
			st.Nodes[n.ID] = &NodeState{Status: plan.StatusPending}
		}
	}
	return st
}

func (s *Store) readState() (*State, error) {
	raw, err := os.ReadFile(s.statePath())
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, fmt.Errorf("corrupt state at %s: %w", s.statePath(), err)
	}
	if st.Nodes == nil {
		st.Nodes = map[string]*NodeState{}
	}
	if st.Dynamic == nil {
		st.Dynamic = map[string]*plan.Node{}
	}
	if st.Expanded == nil {
		st.Expanded = map[string]bool{}
	}
	return &st, nil
}

func (s *Store) writeState(st *State) error {
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp := s.statePath() + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.statePath()); err != nil {
		return err
	}
	if d, err := os.Open(s.dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// With runs fn under an exclusive lock and persists the result. fn must not
// block on a child process: keep the critical section to microseconds.
func (s *Store) With(fn func(*State) error) error {
	lockPath := filepath.Join(s.dir, "lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := lockExclusive(f); err != nil {
		return err
	}
	defer unlock(f)

	st, err := s.readState()
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		st = s.freshState()
	}
	if err := fn(st); err != nil {
		return err
	}
	st.UpdatedAt = time.Now().UTC()
	return s.writeState(st)
}

// Snapshot returns a copy of the state.
func (s *Store) Snapshot() (*State, error) {
	var out *State
	err := s.With(func(st *State) error {
		cp := *st
		cp.Nodes = make(map[string]*NodeState, len(st.Nodes))
		for k, v := range st.Nodes {
			c := *v
			cp.Nodes[k] = &c
		}
		cp.Dynamic = make(map[string]*plan.Node, len(st.Dynamic))
		for k, v := range st.Dynamic {
			cp.Dynamic[k] = v.Clone()
		}
		cp.Expanded = make(map[string]bool, len(st.Expanded))
		for k, v := range st.Expanded {
			cp.Expanded[k] = v
		}
		out = &cp
		return nil
	})
	return out, err
}

// Root is the directory artifact paths resolve against.
func (s *Store) Root() string { return s.root }

// Dir is the run directory.
func (s *Store) Dir() string { return s.dir }

// Plan is the immutable plan for this run.
func (s *Store) Plan() *plan.Plan { return s.plan }

// Role returns the role a node was assigned in the plan.
func (s *Store) Role(nodeID string) string {
	for _, n := range s.plan.Nodes {
		if n != nil && n.ID == nodeID {
			return n.Role
		}
	}
	return ""
}

// ResetExpired returns expired leases to pending. A crashed worker therefore
// needs no cleanup path of its own: its lease simply runs out.
func (s *Store) ResetExpired(now time.Time) ([]string, error) {
	var reaped []string
	err := s.With(func(st *State) error {
		ids := make([]string, 0, len(st.Nodes))
		for id := range st.Nodes {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			ns := st.Nodes[id]
			if ns.Status == plan.StatusRunning && !ns.LeaseUntil.After(now) {
				ns.Status = plan.StatusPending
				ns.Owner = ""
				ns.LeaseUntil = time.Time{}
				ns.LastError = "lease expired"
				reaped = append(reaped, id)
			}
		}
		return nil
	})
	return reaped, err
}
