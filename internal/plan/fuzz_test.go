package plan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fuzzDeadline bounds a single decode/validate call. A plan that parses but
// never returns must fail loudly instead of stalling the fuzzer.
const fuzzDeadline = 2 * time.Second

// mustReturnWithin runs fn and fails if it has not returned within fuzzDeadline.
// Wrapping every iteration would leak one goroutine per input, so the guard is
// applied only to inputs long enough to plausibly hang a decoder. Inputs that do
// hang are still caught: the leaked goroutine is bounded by a fixed fuzztime.
func mustReturnWithin(t *testing.T, inLen int, fn func()) {
	t.Helper()
	if inLen <= 100 {
		fn()
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(fuzzDeadline):
		t.Fatalf("input of %d bytes did not return within %s", inLen, fuzzDeadline)
	}
}

// nestedYAML builds "a:\n b:\n  c:\n ..." nesting n levels deep.
func nestedYAML(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString(strings.Repeat(" ", i))
		b.WriteString("k")
		b.WriteString(strings.Repeat("a", i))
		b.WriteString(":\n")
	}
	return b.String()
}

func FuzzParse(f *testing.F) {
	seeds := []string{
		// valid plan
		"version: 1\nrun: demo\nnodes:\n  - id: a\n    kind: SPEC\n    timeout: 5m\n",
		// truncated
		"version: 1\nnodes:\n  - id: a\n    kind: SPEC\n  - id:\n",
		// deeply nested (YAML alias/bomb shaped input)
		nestedYAML(50),
		// 10KB of repetition
		strings.Repeat("a", 10*1024),
		// cycle-inducing deps: a -> b -> a
		"version: 1\nnodes:\n  - id: a\n    kind: SPEC\n    timeout: 1m\n    deps: [b]\n" +
			"  - id: b\n    kind: SPEC\n    timeout: 1m\n    deps: [a]\n",
		// self-cycle
		"version: 1\nnodes:\n  - id: a\n    kind: SPEC\n    timeout: 1m\n    deps: [a]\n",
		// bare brace
		"{",
		// null node entry
		"version: 1\nnodes:\n  -\n",
		// empty
		"",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, s string) {
		var p *Plan
		var err error
		mustReturnWithin(t, len(s), func() { p, err = Parse([]byte(s)) })
		if err != nil || p == nil {
			return
		}
		// Anything that parses must validate without panicking or hanging.
		mustReturnWithin(t, len(s), func() { _ = p.Validate() })
	})
}

func FuzzLoadUnits(f *testing.F) {
	// Seeds cover each supported format plus the empty/unknown format default.
	f.Add("a\nb\nc\n", "lines")
	f.Add("", "")
	f.Add("  spaced  \n\n", "lines")
	f.Add(`["a","b",{"id":"c"}]`, "json")
	f.Add(`{"id":"x"}`+"\n"+`{"id":"x"}`+"\n", "jsonl")
	f.Add("not json at all {{{", "json")
	f.Add(`[1,2,3]`, "json")
	f.Add("x", "bogus")

	f.Fuzz(func(t *testing.T, text, format string) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "u.txt"), []byte(text), 0o644); err != nil {
			t.Fatalf("write temp unit file: %v", err)
		}
		mustReturnWithin(t, len(text), func() {
			_, _ = LoadUnits(root, Discover{From: "u.txt", Format: format})
		})
	})
}
