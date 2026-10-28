// Package trace writes the append-only execution record.
//
// Each worker writes its own shard, so no two processes ever contend for a
// file. One JSON object per line means the trace is greppable, and `merge`
// reconstitutes a single ordered view on demand. The DAG is the trace: a node
// event is the only thing recorded, so there is no second system to drift.
package trace

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Event is one line of the trace.
type Event struct {
	TS      time.Time      `json:"ts"`
	Run     string         `json:"run,omitempty"`
	Node    string         `json:"node,omitempty"`
	Worker  string         `json:"worker,omitempty"`
	Event   string         `json:"event"`
	Epoch   int64          `json:"epoch,omitempty"`
	Attempt int            `json:"attempt,omitempty"`
	Exit    *int           `json:"exit,omitempty"`
	DurMS   int64          `json:"dur_ms,omitempty"`
	Message string         `json:"msg,omitempty"`
	Attrs   map[string]any `json:"attrs,omitempty"`
}

// Writer appends events to per-worker shards under dir.
type Writer struct {
	dir string
	mu  sync.Mutex
}

// New creates a writer rooted at dir.
func New(dir string) *Writer { return &Writer{dir: dir} }

// Emit appends one event. worker names the shard; use a stable per-process id.
func (w *Writer) Emit(worker string, e Event) error {
	if e.TS.IsZero() {
		e.TS = time.Now().UTC()
	} else {
		e.TS = e.TS.UTC()
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	w.mu.Lock()
	defer w.mu.Unlock()

	if err := os.MkdirAll(w.dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(w.dir, shardName(worker)), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(line)
	return err
}

// Name sanitizes an arbitrary id into a safe filename component.
func Name(s string) string {
	if s == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// shardName keeps worker ids from escaping the trace directory.
func shardName(worker string) string {
	if worker == "" {
		worker = "unknown"
	}
	var b strings.Builder
	for _, r := range worker {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String() + ".jsonl"
}

// Merge reads every shard and returns events ordered by timestamp.
func Merge(dir string) ([]Event, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Event
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		f, err := os.Open(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			line := sc.Bytes()
			if len(line) == 0 {
				continue
			}
			var ev Event
			if err := json.Unmarshal(line, &ev); err != nil {
				continue // a torn tail line must not break the whole view
			}
			out = append(out, ev)
		}
		scanErr := sc.Err()
		closeErr := f.Close()
		if scanErr != nil {
			return nil, scanErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TS.Before(out[j].TS) })
	return out, nil
}

// Render writes the merged trace, one line per event.
func Render(events []Event) string {
	var b strings.Builder
	for _, e := range events {
		ts := e.TS.Format("15:04:05.000")
		node := e.Node
		if node == "" {
			node = "-"
		}
		extra := ""
		if e.Message != "" {
			extra = " " + e.Message
		}
		if e.DurMS > 0 {
			extra += fmt.Sprintf(" (%dms)", e.DurMS)
		}
		fmt.Fprintf(&b, "%s %-6s %-8s %s%s\n", ts, node, e.Event, e.Worker, extra)
	}
	return b.String()
}
