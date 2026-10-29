package plan

import "testing"

const chainYAML = `
version: 1
run: chain
nodes:
  - id: "1"
    kind: INTENT
  - id: "2"
    kind: SPEC
    deps: ["1"]
  - id: "3"
    kind: IMPL
    deps: ["2"]
  - id: "4"
    kind: GATE
    deps: ["3"]
`

const diamondYAML = `
version: 1
run: diamond
nodes:
  - id: "1"
    kind: INTENT
  - id: "2"
    kind: SPEC
    deps: ["1"]
  - id: "3"
    kind: RESEARCH
    deps: ["1"]
  - id: "4"
    kind: IMPL
    deps: ["2", "3"]
  - id: "5"
    kind: OTHER
`

func TestCVPDistanceToChain(t *testing.T) {
	p := parsePlan(t, chainYAML)
	cases := []struct {
		from, target string
		want         int
	}{
		{"1", "1", 0},
		{"2", "1", 1},
		{"3", "1", 2},
		{"4", "1", 3},
		{"1", "4", -1}, // edges point at deps, not dependents
		{"4", "4", 0},
		{"1", "nope", -1},
		{"nope", "1", -1},
		{"", "1", -1},
	}
	for _, c := range cases {
		if got := DistanceTo(p, c.from, c.target); got != c.want {
			t.Errorf("DistanceTo(%q,%q) = %d, want %d", c.from, c.target, got, c.want)
		}
	}
}

func TestCVPDistanceToDiamond(t *testing.T) {
	p := parsePlan(t, diamondYAML)
	cases := []struct {
		from, target string
		want         int
	}{
		{"4", "1", 2}, // 4 -> 2 -> 1 (not 3)
		{"4", "2", 1},
		{"4", "3", 1},
		{"2", "1", 1},
		{"5", "1", -1}, // disconnected node
	}
	for _, c := range cases {
		if got := DistanceTo(p, c.from, c.target); got != c.want {
			t.Errorf("DistanceTo(%q,%q) = %d, want %d", c.from, c.target, got, c.want)
		}
	}
}

func TestCVPOnPathToAncestors(t *testing.T) {
	p := parsePlan(t, diamondYAML)
	got := OnPathTo(p, "4")
	want := []string{"1", "2", "3"}
	if len(got) != len(want) {
		t.Fatalf("OnPathTo(4) = %v, want exactly %v", got, want)
	}
	for _, id := range want {
		if !got[id] {
			t.Errorf("OnPathTo(4) missing ancestor %q (got %v)", id, got)
		}
	}
	if got["4"] {
		t.Errorf("OnPathTo(4) must not include the target itself")
	}
	if got["5"] {
		t.Errorf("OnPathTo(4) must not include the disconnected node 5")
	}
	if len(OnPathTo(p, "5")) != 0 {
		t.Errorf("OnPathTo(5) should be empty, got %v", OnPathTo(p, "5"))
	}
	if len(OnPathTo(p, "missing")) != 0 {
		t.Errorf("OnPathTo(missing) should be empty")
	}
}

func TestCVPGatePicksLastGateInTopoOrder(t *testing.T) {
	p := parsePlan(t, `
version: 1
run: two-gates
nodes:
  - id: "1"
    kind: INTENT
  - id: "2"
    kind: GATE
    deps: ["1"]
  - id: "3"
    kind: IMPL
    deps: ["2"]
  - id: "4"
    kind: GATE
    deps: ["3"]
  - id: "9"
    kind: DOC
`)
	if got := CVPGate(p); got != "4" {
		t.Fatalf("CVPGate = %q, want %q (last GATE in topo order)", got, "4")
	}
}

func TestCVPGateNoneDeclared(t *testing.T) {
	p := parsePlan(t, diamondYAML)
	if got := CVPGate(p); got != "" {
		t.Fatalf("CVPGate on a plan with no GATE = %q, want empty", got)
	}
	if _, ok := CVPGateFromYAML([]byte(diamondYAML)); ok {
		t.Fatalf("CVPGateFromYAML found a cvp_gate key that is not there")
	}
}

func TestCVPGateExplicitKeyOverrides(t *testing.T) {
	// The gate id here is 2, which topologically precedes gate 4, so only the
	// explicit key can produce it.
	raw := []byte(`
version: 1
run: explicit
cvp_gate: "2"
nodes:
  - id: "1"
    kind: INTENT
  - id: "2"
    kind: GATE
    deps: ["1"]
  - id: "3"
    kind: IMPL
    deps: ["2"]
  - id: "4"
    kind: GATE
    deps: ["3"]
`)
	p := parsePlan(t, string(raw))

	if got, ok := CVPGateFromYAML(raw); !ok || got != "2" {
		t.Fatalf("CVPGateFromYAML = (%q,%v), want (\"2\",true)", got, ok)
	}
	if got := ResolveCVPGate(p, raw); got != "2" {
		t.Fatalf("ResolveCVPGate = %q, want explicit key %q", got, "2")
	}
	// Without the plan text, the topo default wins.
	if got := ResolveCVPGate(p, nil); got != "4" {
		t.Fatalf("ResolveCVPGate(nil) = %q, want topo default %q", got, "4")
	}
}

func TestCVPGateFromYAMLForms(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"plain", "run: x\ncvp_gate: \"3.2\"\nnodes: []\n", "3.2", true},
		{"unquoted", "cvp_gate: 3.2\n", "3.2", true},
		{"single quotes", "cvp_gate: '3.2'\n", "3.2", true},
		{"trailing comment", "cvp_gate: \"3.2\" # the checkpoint\n", "3.2", true},
		{"trailing spaces", "cvp_gate: 3.2   \n", "3.2", true},
		{"empty value", "cvp_gate:\n", "", false},
		{"absent", "run: x\n", "", false},
		{"nested is not top level", "  cvp_gate: 3.2\n", "", false},
	}
	for _, c := range cases {
		got, ok := CVPGateFromYAML([]byte(c.in))
		if got != c.want || ok != c.ok {
			t.Errorf("%s: CVPGateFromYAML = (%q,%v), want (%q,%v)", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestCVPGateExplicitKeyUnknownNodeIgnored(t *testing.T) {
	// A typo must not silently retarget the checkpoint.
	raw := []byte(`
version: 1
run: chain
cvp_gate: "nope"
nodes:
  - id: "1"
    kind: INTENT
  - id: "2"
    kind: SPEC
    deps: ["1"]
  - id: "4"
    kind: GATE
    deps: ["2"]
`)
	p := parsePlan(t, string(raw))
	if got, ok := CVPGateFromYAML(raw); !ok || got != "nope" {
		t.Fatalf("CVPGateFromYAML = %q, want the raw value %q", got, "nope")
	}
	if got := ResolveCVPGate(p, raw); got != "4" {
		t.Fatalf("ResolveCVPGate with unknown gate = %q, want fallback %q", got, "4")
	}
}

func TestCVPDistanceHelper(t *testing.T) {
	p := parsePlan(t, chainYAML)
	if d, ok := CVPDistance(p, "4", "2"); !ok || d != 2 {
		t.Fatalf("CVPDistance(4,2) = (%d,%v), want (2,true)", d, ok)
	}
	if d, ok := CVPDistance(p, "", "2"); ok || d != -1 {
		t.Fatalf("CVPDistance with no gate = (%d,%v), want (-1,false)", d, ok)
	}
}
