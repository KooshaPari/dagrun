package verify

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/KooshaPari/dagrun/internal/plan"
)

// fuzzDeadline bounds one artifact/kind check so a pathological regex or a
// runaway loop fails the iteration instead of stalling the fuzzer.
const fuzzDeadline = 2 * time.Second

// within runs fn and fails if it does not return in time. Only applied to longer
// inputs: guarding every iteration would leak a goroutine per input.
func within(t *testing.T, inLen int, fn func()) {
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

// fixedArtifactRoot builds a temp root holding one known file.
func fixedArtifactRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello world\n"), 0o644); err != nil {
		t.Fatalf("write temp artifact: %v", err)
	}
	return root
}

func FuzzCheckArtifact(f *testing.F) {
	f.Add("a.txt", "hello", `hel+o`)
	f.Add("a.txt", "", "")
	f.Add("*.txt", "hello", `^hello`)
	f.Add("missing.txt", "x", "y")
	f.Add("[", "", "")
	f.Add("a.txt", "nope", "(")
	f.Add("**", "", `.*`)

	f.Fuzz(func(t *testing.T, glob, mustContain, mustMatch string) {
		// Keep the regex bounded. NOTE: checkArtifact / plan.Validate only
		// reject an *invalid* must_match; neither caps its length. A plan with a
		// multi-KB must_match is therefore compiled and matched verbatim, which
		// is a ReDoS-shaped risk and a finding to report.
		if len(mustMatch) > 200 {
			mustMatch = mustMatch[:200]
		}
		if len(mustContain) > 200 {
			mustContain = mustContain[:200]
		}
		root := fixedArtifactRoot(t)
		a := plan.Artifact{Path: glob, MustMatch: mustMatch}
		if mustContain != "" {
			a.MustContain = []string{mustContain}
		}
		within(t, len(glob)+len(mustContain)+len(mustMatch), func() {
			_ = checkArtifact(root, a)
		})
	})
}

func FuzzKindCheck(f *testing.F) {
	f.Add("# Spec\nThis is a spec.\n", "SPEC")
	f.Add("# Test\nwe test it\n", "TEST")
	f.Add("", "DOC")
	f.Add("plain text", "IMPL")
	f.Add("random {{{", "nonsense-kind")
	f.Add("[broken link](nowhere.md)\n", "RESEARCH")
	f.Add("SOTA state of the art\n", "SOTA")
	f.Add("INTENT we will\n", "INTENT")

	f.Fuzz(func(t *testing.T, text, kind string) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "doc.md"), []byte(text), 0o644); err != nil {
			t.Fatalf("write temp artifact: %v", err)
		}
		n := &plan.Node{
			ID:        "n1",
			Kind:      kind,
			Artifacts: []plan.Artifact{{Path: "doc.md"}},
		}
		within(t, len(text)+len(kind), func() {
			_ = KindCheck(n, root)
		})
	})
}
