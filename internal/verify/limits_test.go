package verify

import (
	"strings"
	"testing"

	"github.com/KooshaPari/dagrun/internal/plan"
)

// bigger returns a string of n bytes.
func bigger(n int) string { return strings.Repeat("a", n) }

func TestLimitsRejectOverlongRegex(t *testing.T) {
	root := t.TempDir()
	write(t, root, "artifacts/doc.md", "hello world")

	res := checkArtifact(root, plan.Artifact{
		Path:      "artifacts/doc.md",
		MustMatch: bigger(MaxMustMatchLength + 1),
	})
	if res.OK {
		t.Fatalf("overlong pattern accepted")
	}
	if res.Code != "regex_too_long" {
		t.Fatalf("code = %q, want regex_too_long (detail %q)", res.Code, res.Detail)
	}
	if !strings.Contains(res.Detail, "4096") {
		t.Fatalf("detail %q does not name the limit", res.Detail)
	}
}

func TestLimitsAcceptRegexAtCap(t *testing.T) {
	root := t.TempDir()
	write(t, root, "artifacts/doc.md", "hello world")

	// a long but legal pattern: literals joined by an optional char class
	res := checkArtifact(root, plan.Artifact{
		Path:      "artifacts/doc.md",
		MustMatch: "hello" + strings.Repeat("x?", MaxMustMatchLength/2-3),
	})
	if res.Code == "regex_too_long" {
		t.Fatalf("pattern under the cap was rejected: %q", res.Detail)
	}

	// and the plain short case still matches
	ok := checkArtifact(root, plan.Artifact{Path: "artifacts/doc.md", MustMatch: `h.llo`})
	if !ok.OK {
		t.Fatalf("short pattern failed: %s %s", ok.Code, ok.Detail)
	}
}

func TestLimitsGlobCap(t *testing.T) {
	tooMany := make([]string, MaxGlobMatches+1)
	if err := checkGlobCap(tooMany); err == nil {
		t.Fatalf("cap not enforced at %d matches", len(tooMany))
	} else if !strings.Contains(err.Error(), "5000") {
		t.Fatalf("error %q does not name the limit", err)
	}

	atCap := make([]string, MaxGlobMatches)
	if err := checkGlobCap(atCap); err != nil {
		t.Fatalf("cap rejected an allowed count: %v", err)
	}
	if err := checkGlobCap(nil); err != nil {
		t.Fatalf("empty matches rejected: %v", err)
	}
}

func TestLimitsRejectOverlongMustContain(t *testing.T) {
	root := t.TempDir()
	write(t, root, "artifacts/doc.md", "hello world")

	res := checkArtifact(root, plan.Artifact{
		Path:        "artifacts/doc.md",
		MustContain: []string{bigger(MaxMustContainLength + 1)},
	})
	if res.OK {
		t.Fatalf("overlong must_contain accepted")
	}
	if res.Code != "must_contain_too_long" {
		t.Fatalf("code = %q, want must_contain_too_long", res.Code)
	}
	if !strings.Contains(res.Detail, "1024") {
		t.Fatalf("detail %q does not name the limit", res.Detail)
	}
}

func TestLimitsAcceptMustContainAtCap(t *testing.T) {
	root := t.TempDir()
	write(t, root, "artifacts/doc.md", "hello world")

	res := checkArtifact(root, plan.Artifact{
		Path:        "artifacts/doc.md",
		MustContain: []string{"hello"},
	})
	if !res.OK {
		t.Fatalf("short must_contain failed: %s %s", res.Code, res.Detail)
	}
}

func TestLimitsSummary(t *testing.T) {
	got := LimitsSummary()
	for _, want := range []string{"4096", "5000", "1024"} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary %q missing %s", got, want)
		}
	}
}
