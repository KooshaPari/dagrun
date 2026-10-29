// Package plan defines the work-DAG format and its structural validation.
//
// A plan is authored by an agent. Structural validity (unique ids, resolvable
// deps, no cycles, every node verifiable) is checked before a single node runs,
// because a wrong plan is the only thing this tool cannot recover from.
package plan

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// SchemaVersion is the only plan version this binary accepts.
const SchemaVersion = 1

// Node statuses. Stored, not derived.
const (
	StatusPending   = "pending"
	StatusRunning   = "running"
	StatusDone      = "done"
	StatusFailed    = "failed"
	StatusSkipped   = "skipped"
	StatusCancelled = "cancelled"
)

// Kinds are the artifact classes a node can produce.
var Kinds = map[string]bool{
	"INTENT": true, "SPEC": true, "PLAN": true, "RESEARCH": true,
	"SOTA": true, "TEST": true, "IMPL": true, "UI": true,
	"CODE": true, "DOC": true, "GATE": true, "OTHER": true,
}

// Defaults are inherited by every node that does not override them.
type Defaults struct {
	Estimate    string `yaml:"estimate,omitempty" json:"estimate,omitempty"`
	Role        string `yaml:"role,omitempty" json:"role,omitempty"`
	Harness     string `yaml:"harness,omitempty" json:"harness,omitempty"`
	Provider    string `yaml:"provider,omitempty" json:"provider,omitempty"`
	Model       string `yaml:"model,omitempty" json:"model,omitempty"`
	Timeout     string `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	MaxAttempts int    `yaml:"max_attempts,omitempty" json:"max_attempts,omitempty"`
}

// Artifact is one declared output of a node and the predicate that proves it
// exists and is not a placeholder.
type Artifact struct {
	Path        string   `yaml:"path" json:"path"`
	MinBytes    int      `yaml:"min_bytes,omitempty" json:"min_bytes,omitempty"`
	MustContain []string `yaml:"must_contain,omitempty" json:"must_contain,omitempty"`
	MustMatch   string   `yaml:"must_match,omitempty" json:"must_match,omitempty"`
	MinCount    int      `yaml:"min_count,omitempty" json:"min_count,omitempty"`
	// AllowEmpty accepts a zero-byte artifact. Needed when "no findings" is the
	// good outcome: a linter that reports nothing still has to produce a file,
	// and that file is legitimately empty.
	AllowEmpty bool `yaml:"allow_empty,omitempty" json:"allow_empty,omitempty"`
}

// Node is one unit of work. It is the scheduling unit and the trace unit.
type Node struct {
	ID        string     `yaml:"id" json:"id"`
	Kind      string     `yaml:"kind" json:"kind"`
	Title     string     `yaml:"title,omitempty" json:"title,omitempty"`
	Deps      []string   `yaml:"deps,omitempty" json:"deps,omitempty"`
	Estimate  string     `yaml:"estimate,omitempty" json:"estimate,omitempty"`
	Role      string     `yaml:"role,omitempty" json:"role,omitempty"`
	Harness   string     `yaml:"harness,omitempty" json:"harness,omitempty"`
	Provider  string     `yaml:"provider,omitempty" json:"provider,omitempty"`
	Model     string     `yaml:"model,omitempty" json:"model,omitempty"`
	Prompt    string     `yaml:"prompt,omitempty" json:"prompt,omitempty"`
	Artifacts []Artifact `yaml:"artifacts,omitempty" json:"artifacts,omitempty"`
	// FileScope lists the glob paths this node may edit. Plan-time collision
	// detection rejects two parallel nodes whose scopes intersect.
	FileScope []string `yaml:"file_scope,omitempty" json:"file_scope,omitempty"`
	Check     string   `yaml:"check,omitempty" json:"check,omitempty"`
	Workdir   string   `yaml:"workdir,omitempty" json:"workdir,omitempty"`
	// Isolation is "" (shared checkout) or "worktree" (its own git worktree and
	// branch). Worktree nodes resolve artifacts inside their own checkout.
	Isolation   string `yaml:"isolation,omitempty" json:"isolation,omitempty"`
	Timeout     string `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	MaxAttempts int    `yaml:"max_attempts,omitempty" json:"max_attempts,omitempty"`
	Optional    bool   `yaml:"optional,omitempty" json:"optional,omitempty"`
	// Hooks are the shell commands run before, after, and on failure of this
	// node. A failing pre or post hook fails the node: the hook is contract.
	Hooks *Hooks `yaml:"hooks,omitempty" json:"hooks,omitempty"`

	// Dynamic decomposition. Discover turns a verified artifact into a work
	// set; FanOut marks this node as the template expanded from a parent's
	// work set; FanIn marks this node as the reduce over a parent's children.
	Discover *Discover `yaml:"discover,omitempty" json:"discover,omitempty"`
	FanOut   string    `yaml:"fan_out,omitempty" json:"fan_out,omitempty"`
	FanIn    string    `yaml:"fan_in,omitempty" json:"fan_in,omitempty"`
}

// Plan is a whole work DAG.
type Plan struct {
	Version       int            `yaml:"version" json:"version"`
	Run           string         `yaml:"run" json:"run"`
	Intent        string         `yaml:"intent,omitempty" json:"intent,omitempty"`
	ArtifactsRoot string         `yaml:"artifacts_root,omitempty" json:"artifacts_root,omitempty"`
	Defaults      Defaults       `yaml:"defaults,omitempty" json:"defaults,omitempty"`
	RoleCaps      map[string]int `yaml:"role_caps,omitempty" json:"role_caps,omitempty"`
	MaxParallel   int            `yaml:"max_parallel,omitempty" json:"max_parallel,omitempty"`
	MaxDynamic    int            `yaml:"max_dynamic,omitempty" json:"max_dynamic,omitempty"`
	Nodes         []*Node        `yaml:"nodes" json:"nodes"`

	// Root is the absolute directory artifact paths are resolved against. It is
	// set on load and never serialized, so a checked-in plan stays portable.
	Root string `yaml:"-" json:"-"`
}

// Issue is one structural problem. An empty slice means the plan is runnable.
type Issue struct {
	Node   string `json:"node,omitempty" json:"node"`
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

func (i Issue) String() string {
	if i.Node == "" {
		return fmt.Sprintf("%s: %s", i.Code, i.Detail)
	}
	return fmt.Sprintf("%s [%s]: %s", i.Node, i.Code, i.Detail)
}

// Load reads a plan from a YAML or JSON file and resolves defaults.
func Load(path string) (*Plan, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	p, err := Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	abs, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	p.Root = abs
	return p, nil
}

// Parse decodes YAML (JSON is a YAML subset) and applies defaults.
func Parse(raw []byte) (*Plan, error) {
	var p Plan
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(false) // forward-compatible: unknown keys are ignored
	if err := dec.Decode(&p); err != nil {
		return nil, err
	}
	p.normalize()
	return &p, nil
}

func (p *Plan) normalize() {
	if p.Run == "" {
		p.Run = "run"
	}
	if p.ArtifactsRoot == "" {
		p.ArtifactsRoot = "artifacts"
	}
	if p.MaxParallel <= 0 {
		p.MaxParallel = 4
	}
	if p.MaxDynamic <= 0 {
		p.MaxDynamic = 200
	}
	for _, n := range p.Nodes {
		if n == nil {
			continue
		}
		if n.Estimate == "" {
			n.Estimate = firstNonEmpty(p.Defaults.Estimate, "10m")
		}
		if n.Role == "" {
			n.Role = firstNonEmpty(p.Defaults.Role, "impl")
		}
		if n.Harness == "" {
			n.Harness = firstNonEmpty(p.Defaults.Harness, "auto")
		}
		if n.Provider == "" {
			n.Provider = p.Defaults.Provider
		}
		if n.Model == "" {
			n.Model = p.Defaults.Model
		}
		if n.Timeout == "" {
			n.Timeout = firstNonEmpty(p.Defaults.Timeout, "30m")
		}
		if n.MaxAttempts <= 0 {
			n.MaxAttempts = firstNonEmptyInt(p.Defaults.MaxAttempts, 2)
		}
		if n.Title == "" {
			n.Title = n.ID
		}
		if len(n.Artifacts) == 0 {
			n.Artifacts = DefaultArtifacts(n.Kind, p.ArtifactsRoot, n.ID)
		}
	}
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func firstNonEmptyInt(v ...int) int {
	for _, i := range v {
		if i > 0 {
			return i
		}
	}
	return 0
}

// DefaultArtifacts gives each kind a sensible contract so a minimal plan still
// has something to verify.
func DefaultArtifacts(kind, root, id string) []Artifact {
	slug := Slug(id)
	switch kind {
	case "INTENT":
		return []Artifact{{Path: root + "/INTENT.md", MinBytes: 300, MustContain: []string{"Acceptance"}}}
	case "SPEC":
		return []Artifact{{Path: root + "/SPEC.md", MinBytes: 400}}
	case "PLAN":
		return []Artifact{{Path: root + "/PLAN.md", MinBytes: 200}}
	case "RESEARCH":
		return []Artifact{{Path: root + "/RESEARCH-" + slug + ".md", MinBytes: 500, MustContain: []string{"## Sources"}}}
	case "SOTA":
		return []Artifact{{Path: root + "/SOTA.md", MinBytes: 400}}
	case "UI":
		return []Artifact{{Path: root + "/UI-" + slug + ".md", MinBytes: 200}}
	case "DOC":
		return []Artifact{{Path: root + "/DOC-" + slug + ".md", MinBytes: 200}}
	case "GATE":
		return nil
	}
	return nil
}

// Slug turns "1.2.3 auth flow" into "1.2.3-auth-flow".
func Slug(id string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(id) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	s := strings.Trim(b.String(), "-")
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return s
}

// Index maps node id to node.
func (p *Plan) Index() map[string]*Node {
	m := make(map[string]*Node, len(p.Nodes))
	for _, n := range p.Nodes {
		if n != nil {
			m[n.ID] = n
		}
	}
	return m
}

// ByID returns one static node by id.
func (p *Plan) ByID(id string) *Node {
	for _, n := range p.Nodes {
		if n != nil && n.ID == id {
			return n
		}
	}
	return nil
}

// Sorted returns nodes in natural WBS order (numeric segment wise).
func (p *Plan) Sorted() []*Node {
	out := make([]*Node, 0, len(p.Nodes))
	for _, n := range p.Nodes {
		if n != nil {
			out = append(out, n)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return Less(out[i].ID, out[j].ID) })
	return out
}

// Less compares hierarchical ids so 1.2 < 1.10 < 2.
func Less(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		ai, aerr := strconv.Atoi(as[i])
		bi, berr := strconv.Atoi(bs[i])
		if aerr == nil && berr == nil {
			if ai != bi {
				return ai < bi
			}
			continue
		}
		if as[i] != bs[i] {
			return as[i] < bs[i]
		}
	}
	return len(as) < len(bs)
}

// ParseEstimate accepts "90s", "10m", "2h", "1h30m" or a bare number of minutes.
func ParseEstimate(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 10 * time.Minute, nil
	}
	if n, err := strconv.Atoi(s); err == nil {
		return time.Duration(n) * time.Minute, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("bad estimate %q: want e.g. 10m, 1h, or a number of minutes", s)
	}
	if d <= 0 {
		return 0, fmt.Errorf("bad estimate %q: must be > 0", s)
	}
	return d, nil
}

// Timeout returns the node timeout as a duration.
func (n *Node) TimeoutDuration() time.Duration {
	d, err := time.ParseDuration(n.Timeout)
	if err != nil || d <= 0 {
		return 30 * time.Minute
	}
	return d
}

// EstimateDuration returns the node estimate as a duration.
func (n *Node) EstimateDuration() time.Duration {
	d, err := ParseEstimate(n.Estimate)
	if err != nil {
		return 10 * time.Minute
	}
	return d
}

// IsGate reports whether the node only runs a check and produces no artifact.
func (n *Node) IsGate() bool { return n.Kind == "GATE" }
