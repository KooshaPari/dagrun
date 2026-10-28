package plan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, root, rel, body string) {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadUnitsLines(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "u.txt", "a.go\n\n  b.go  \na.go\n")
	units, err := LoadUnits(root, Discover{From: "u.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 3 {
		t.Fatalf("got %d units, want 3 (blank lines skipped): %+v", len(units), units)
	}
	if units[0].Key != "a.go" || units[0].Slug != "a-go" {
		t.Errorf("first unit = %+v", units[0])
	}
	if units[2].Key != "a.go" || units[2].Slug != "a-go-2" {
		t.Errorf("duplicate key should get a distinct slug, got %+v", units[2])
	}
}

func TestLoadUnitsJSONWithKeyField(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "lint.json", `[{"filename":"a.py","count":3},{"filename":"b.py","count":1}]`)
	units, err := LoadUnits(root, Discover{From: "lint.json", Format: "json", Key: "filename"})
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 2 || units[1].Slug != "b-py" {
		t.Fatalf("unexpected units: %+v", units)
	}
}

func TestLoadUnitsJSONL(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "u.jsonl", "\"x\"\n{\"id\":\"y\"}\n")
	units, err := LoadUnits(root, Discover{From: "u.jsonl", Format: "jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 2 || units[0].Key != "x" || units[1].Key != "y" {
		t.Fatalf("unexpected units: %+v", units)
	}
}

func TestLoadUnitsMaxCaps(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "u.txt", "a\nb\nc\nd\n")
	units, err := LoadUnits(root, Discover{From: "u.txt", Max: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 2 {
		t.Fatalf("max not applied: %d units", len(units))
	}
}

func TestLoadUnitsRejectsBadInput(t *testing.T) {
	root := t.TempDir()
	if _, err := LoadUnits(root, Discover{From: "missing.txt"}); err == nil {
		t.Fatal("expected an error for a missing work set")
	}
	writeFile(t, root, "bad.json", `{"not":"an array"}`)
	if _, err := LoadUnits(root, Discover{From: "bad.json", Format: "json"}); err == nil {
		t.Fatal("expected an error for a non-array json work set")
	}
	writeFile(t, root, "obj.json", `[{"nope":1}]`)
	if _, err := LoadUnits(root, Discover{From: "obj.json", Format: "json", Key: "filename"}); err == nil {
		t.Fatal("expected an error when items lack the key field")
	}
}

func TestChildrenSubstituteTheContract(t *testing.T) {
	tmpl := &Node{
		ID:    "9.9",
		Kind:  "IMPL",
		Title: "fix {{unit}}",
		Check: "ruff check {{unit}}",
		Artifacts: []Artifact{{
			Path:        "artifacts/fix-{{slug}}.md",
			MustContain: []string{"{{unit}}"},
		}},
		FanOut: "4.1",
	}
	kids := Children(tmpl, "4.1", []Unit{{Key: "a/b.go", Slug: "a-b-go"}})
	if len(kids) != 1 {
		t.Fatalf("got %d children", len(kids))
	}
	k := kids[0]
	if k.ID != "4.1/a-b-go" {
		t.Errorf("id = %q", k.ID)
	}
	if k.Title != "fix a/b.go" || k.Check != "ruff check a/b.go" {
		t.Errorf("title/check not substituted: %q / %q", k.Title, k.Check)
	}
	if k.Artifacts[0].Path != "artifacts/fix-a-b-go.md" {
		t.Errorf("artifact path = %q", k.Artifacts[0].Path)
	}
	if k.Artifacts[0].MustContain[0] != "a/b.go" {
		t.Errorf("must_contain = %q", k.Artifacts[0].MustContain)
	}
	if k.FanOut != "" || k.Discover != nil {
		t.Error("a child must not inherit fan_out or discover")
	}
	if len(k.Deps) != 1 || k.Deps[0] != "4.1" {
		t.Errorf("child deps = %v, want [4.1]", k.Deps)
	}
	// the template must be untouched
	if tmpl.Artifacts[0].Path != "artifacts/fix-{{slug}}.md" {
		t.Error("Children mutated the template")
	}
}

func TestValidateFanOutAndFanIn(t *testing.T) {
	good := `
version: 1
nodes:
  - id: "4.1"
    kind: GATE
    check: "true"
    artifacts: [{path: units.txt}]
    discover: {from: units.txt, format: lines, max: 10}
  - id: "4.2"
    kind: IMPL
    fan_out: "4.1"
    title: "fix {{unit}}"
    artifacts: [{path: "artifacts/{{slug}}.md"}]
  - id: "4.3"
    kind: GATE
    fan_in: "4.1"
    check: "true"
`
	p, err := Parse([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if issues := p.Validate(); len(issues) != 0 {
		t.Fatalf("expected clean, got %v", issues)
	}

	bad := []struct{ name, yaml, code string }{
		{"unknown fan_out", `
version: 1
nodes:
  - {id: "1", kind: IMPL, fan_out: "nope", artifacts: [{path: a}]}
`, "fan_out_unknown"},
		{"fan_in without discover", `
version: 1
nodes:
  - {id: "1", kind: IMPL, artifacts: [{path: a}]}
  - {id: "2", kind: GATE, fan_in: "1", check: "true"}
`, "fan_in_not_discover"},
		{"both", `
version: 1
nodes:
  - {id: "1", kind: IMPL, artifacts: [{path: a}], fan_out: "2", fan_in: "2"}
  - {id: "2", kind: GATE, check: "true", discover: {from: f}}
`, "fan_both"},
		{"bad format", `
version: 1
nodes:
  - {id: "1", kind: GATE, check: "true", discover: {from: f, format: xml}}
`, "discover_bad_format"},
		{"escape", `
version: 1
nodes:
  - {id: "1", kind: GATE, check: "true", discover: {from: ../f}}
`, "discover_bad_path"},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			p, err := Parse([]byte(c.yaml))
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, is := range p.Validate() {
				if is.Code == c.code {
					found = true
				}
			}
			if !found {
				t.Fatalf("expected %q, got %v", c.code, p.Validate())
			}
		})
	}
}

func TestLoadUnitsFromCommand(t *testing.T) {
	root := t.TempDir()
	units, err := LoadUnits(root, Discover{From: CmdPrefix + "printf 'x.go\\ny.go\\n'"})
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 2 || units[0].Slug != "x-go" || units[1].Slug != "y-go" {
		t.Fatalf("unexpected units: %+v", units)
	}
}

func TestLoadUnitsCommandSeesRepoFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.go", "package a")
	writeFile(t, root, "sub/b.go", "package b")
	units, err := LoadUnits(root, Discover{From: CmdPrefix + "find . -name '*.go' | sed 's|^./||'"})
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 2 {
		t.Fatalf("got %d units, want 2: %+v", len(units), units)
	}
}

func TestLoadUnitsCommandFailureIsLoud(t *testing.T) {
	_, err := LoadUnits(t.TempDir(), Discover{From: CmdPrefix + "echo boom >&2; exit 3"})
	if err == nil {
		t.Fatal("a failing discovery command must be an error, not an empty work set")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error should carry stderr, got %v", err)
	}
}

func TestValidationAcceptsCmdSource(t *testing.T) {
	yaml := `
version: 1
nodes:
  - id: "1"
    kind: GATE
    check: "true"
    discover: {from: "cmd:printf 'a\\n'", format: lines}
`
	p, err := Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	if issues := p.Validate(); len(issues) != 0 {
		t.Fatalf("cmd: source rejected: %v", issues)
	}
	bad := `
version: 1
nodes:
  - id: "1"
    kind: GATE
    check: "true"
    discover: {from: "cmd:"}
`
	p2, _ := Parse([]byte(bad))
	found := false
	for _, is := range p2.Validate() {
		if is.Code == "discover_empty_cmd" {
			found = true
		}
	}
	if !found {
		t.Fatalf("empty cmd: should be rejected, got %v", p2.Validate())
	}
}
