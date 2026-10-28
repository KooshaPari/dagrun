package plan

import (
	"strings"
	"testing"
	"time"
)

const validPlan = `
version: 1
run: demo
intent: prove the executor works
defaults:
  estimate: 10m
  timeout: 5m
nodes:
  - id: "1.1"
    kind: INTENT
    title: write intent
    artifacts:
      - path: artifacts/INTENT.md
        min_bytes: 20
  - id: "1.2"
    kind: IMPL
    title: implement
    deps: ["1.1"]
    prompt: do the thing
    artifacts:
      - path: artifacts/code.go
        must_contain: ["func main"]
    check: "true"
  - id: "1.3"
    kind: GATE
    title: gate
    deps: ["1.2"]
    check: "true"
`

func TestParseAndValidateClean(t *testing.T) {
	p, err := Parse([]byte(validPlan))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	p.Root = t.TempDir()
	if issues := p.Validate(); len(issues) != 0 {
		t.Fatalf("expected no issues, got %v", issues)
	}
	if p.MaxParallel != 4 {
		t.Errorf("default max_parallel = %d, want 4", p.MaxParallel)
	}
	if got := p.Nodes[1].MaxAttempts; got != 2 {
		t.Errorf("default max_attempts = %d, want 2", got)
	}
	if got := p.Nodes[0].Role; got != "impl" {
		t.Errorf("default role = %q, want impl", got)
	}
}

func TestValidateCatchesRealMistakes(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		code string
	}{
		{"cycle", `
version: 1
nodes:
  - {id: "1", kind: IMPL, deps: ["2"], artifacts: [{path: a}]}
  - {id: "2", kind: IMPL, deps: ["1"], artifacts: [{path: b}]}
`, "cycle"},
		{"unknown dep", `
version: 1
nodes:
  - {id: "1", kind: IMPL, deps: ["nope"], artifacts: [{path: a}]}
`, "unknown_dep"},
		{"unverifiable", `
version: 1
nodes:
  - {id: "1", kind: IMPL, title: nothing}
`, "unverifiable"},
		{"gate without check", `
version: 1
nodes:
  - {id: "1", kind: GATE, optional: true}
`, "gate_without_check"},
		{"escape", `
version: 1
nodes:
  - {id: "1", kind: IMPL, artifacts: [{path: "../etc/passwd"}]}
`, "artifact_escape"},
		{"bad kind", `
version: 1
nodes:
  - {id: "1", kind: NONSENSE, artifacts: [{path: a}]}
`, "bad_kind"},
		{"dup id", `
version: 1
nodes:
  - {id: "1", kind: IMPL, artifacts: [{path: a}]}
  - {id: "1", kind: IMPL, artifacts: [{path: b}]}
`, "duplicate_id"},
		{"empty", `
version: 1
nodes: []
`, "empty_plan"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := Parse([]byte(c.yaml))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			found := false
			for _, is := range p.Validate() {
				if is.Code == c.code {
					found = true
				}
			}
			if !found {
				t.Fatalf("expected issue %q, got %v", c.code, p.Validate())
			}
		})
	}
}

func TestTopoIsDeterministicAndRespectsDeps(t *testing.T) {
	yaml := `
version: 1
nodes:
  - {id: "1.10", kind: IMPL, artifacts: [{path: a}]}
  - {id: "1.2", kind: IMPL, deps: ["1.10"], artifacts: [{path: b}]}
  - {id: "1.1", kind: IMPL, artifacts: [{path: c}]}
`
	p, err := Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"1.1", "1.10", "1.2"}
	for i := 0; i < 5; i++ {
		got := p.Topo()
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("topo = %v, want %v", got, want)
		}
	}
}

func TestPertCriticalPath(t *testing.T) {
	yaml := `
version: 1
nodes:
  - {id: "1", kind: IMPL, estimate: 10m, artifacts: [{path: a}]}
  - {id: "2", kind: IMPL, estimate: 30m, deps: ["1"], artifacts: [{path: b}]}
  - {id: "3", kind: IMPL, estimate: 5m, deps: ["1"], artifacts: [{path: c}]}
`
	p, err := Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	s := p.Pert()
	if s.Total != 40*time.Minute {
		t.Errorf("total = %v, want 40m", s.Total)
	}
	if got := strings.Join(s.Critical, ","); got != "1,2" {
		t.Errorf("critical = %s, want 1,2", got)
	}
	if s.Timings["3"].Slack != 25*time.Minute {
		t.Errorf("slack(3) = %v, want 25m", s.Timings["3"].Slack)
	}
	if len(s.Waves) != 2 {
		t.Errorf("waves = %v, want 2", s.Waves)
	}
}

func TestParseEstimate(t *testing.T) {
	for _, c := range []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"10m", 10 * time.Minute, true},
		{"90s", 90 * time.Second, true},
		{"2h", 2 * time.Hour, true},
		{"1h30m", 90 * time.Minute, true},
		{"5", 5 * time.Minute, true},
		{"", 10 * time.Minute, true},
		{"nope", 0, false},
		{"0m", 0, false},
	} {
		got, err := ParseEstimate(c.in)
		if c.ok && err != nil {
			t.Errorf("%q: unexpected error %v", c.in, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%q: expected error", c.in)
		}
		if c.ok && got != c.want {
			t.Errorf("%q = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestDefaultArtifactsGiveEveryKindAContract(t *testing.T) {
	for kind := range Kinds {
		if kind == "GATE" {
			continue
		}
		arts := DefaultArtifacts(kind, "artifacts", "1.2")
		if len(arts) == 0 && kind != "TEST" && kind != "IMPL" && kind != "CODE" && kind != "OTHER" {
			t.Errorf("kind %s has no default artifact and no exemption", kind)
		}
	}
}

func TestPromptCarriesTheContract(t *testing.T) {
	p, err := Parse([]byte(validPlan))
	if err != nil {
		t.Fatal(err)
	}
	out := Prompt(p, p.Nodes[1], p.Index())
	for _, want := range []string{"Work node 1.2", "prove the executor works", "artifacts/code.go", "func main", "`true`", "dagrun verifies"} {
		if !strings.Contains(out, want) {
			t.Errorf("prompt missing %q\n%s", want, out)
		}
	}
}
