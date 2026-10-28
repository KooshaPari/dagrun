package plan

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Discover lets a node turn a verified artifact into a work set, and a template
// node expand into one child per unit at runtime.
//
// This is the map/reduce shape that a static DAG cannot express: how many files
// have lint findings, how many tests fail, how many endpoints need docs is not
// known at plan time. The work set is an ordinary artifact, so it is verified
// and adopted like anything else, and expansion stays deterministic across
// reruns.
type Discover struct {
	// From is a path to an artifact produced by (or before) this node.
	From string `yaml:"from" json:"from"`
	// Format is json (array), jsonl (one value per line), or lines (default).
	Format string `yaml:"format,omitempty" json:"format,omitempty"`
	// Key names the field holding the unit id when items are objects.
	// Default "id". Ignored for lines.
	Key string `yaml:"key,omitempty" json:"key,omitempty"`
	// Max caps expansion. Defaults to 100; a plan-level cap also applies.
	Max int `yaml:"max,omitempty" json:"max,omitempty"`
}

// Unit is one expanded work item.
type Unit struct {
	Key  string `json:"key"`
	Slug string `json:"slug"`
}

// LoadUnits reads the work set named by d, resolved against root.
// Keys are de-duplicated by slug so a messy linter report cannot produce two
// children with the same id.
func LoadUnits(root string, d Discover) ([]Unit, error) {
	if strings.TrimSpace(d.From) == "" {
		return nil, fmt.Errorf("discover.from is empty")
	}
	path := d.From
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, d.From)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("discover %s: %w", d.From, err)
	}

	format := strings.ToLower(strings.TrimSpace(d.Format))
	if format == "" {
		format = "lines"
	}

	var keys []string
	switch format {
	case "lines":
		for _, line := range strings.Split(string(raw), "\n") {
			if k := strings.TrimSpace(line); k != "" {
				keys = append(keys, k)
			}
		}
	case "jsonl":
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var v any
			if err := json.Unmarshal([]byte(line), &v); err != nil {
				return nil, fmt.Errorf("discover %s: bad jsonl line: %w", d.From, err)
			}
			k, err := unitKey(v, d.Key)
			if err != nil {
				return nil, fmt.Errorf("discover %s: %w", d.From, err)
			}
			keys = append(keys, k)
		}
	case "json":
		var arr []any
		if err := json.Unmarshal(raw, &arr); err != nil {
			return nil, fmt.Errorf("discover %s: want a JSON array: %w", d.From, err)
		}
		for _, v := range arr {
			k, err := unitKey(v, d.Key)
			if err != nil {
				return nil, fmt.Errorf("discover %s: %w", d.From, err)
			}
			keys = append(keys, k)
		}
	default:
		return nil, fmt.Errorf("discover %s: unknown format %q (want json, jsonl, or lines)", d.From, d.Format)
	}

	max := d.Max
	if max <= 0 {
		max = 100
	}
	if len(keys) > max {
		keys = keys[:max]
	}

	seen := map[string]int{}
	units := make([]Unit, 0, len(keys))
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		base := unitSlug(k)
		if base == "" {
			base = "unit"
		}
		seen[base]++
		slug := base
		if n := seen[base]; n > 1 {
			slug = fmt.Sprintf("%s-%d", base, n)
		}
		units = append(units, Unit{Key: k, Slug: slug})
	}
	return units, nil
}

// unitSlug is like Slug but also folds dots to dashes, because a child id is a
// new identifier ("4.1/a-go") rather than a WBS number.
func unitSlug(s string) string {
	out := strings.ReplaceAll(Slug(s), ".", "-")
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	return strings.Trim(out, "-")
}

func unitKey(v any, field string) (string, error) {
	switch t := v.(type) {
	case string:
		return t, nil
	case float64:
		return fmt.Sprintf("%v", t), nil
	case map[string]any:
		if field == "" {
			field = "id"
		}
		if raw, ok := t[field]; ok {
			if s, ok := raw.(string); ok {
				return s, nil
			}
			return fmt.Sprint(raw), nil
		}
		return "", fmt.Errorf("item has no %q field", field)
	}
	return "", fmt.Errorf("unsupported item type %T", v)
}

// Subst replaces the unit placeholders in a template string.
func Subst(s string, u Unit) string {
	if !strings.Contains(s, "{{") {
		return s
	}
	r := strings.NewReplacer(
		"{{unit}}", u.Key,
		"{{slug}}", u.Slug,
	)
	return r.Replace(s)
}

// Clone returns a deep copy so a template is never mutated by expansion.
func (n *Node) Clone() *Node {
	if n == nil {
		return nil
	}
	c := *n
	c.Deps = append([]string(nil), n.Deps...)
	c.Artifacts = make([]Artifact, len(n.Artifacts))
	for i, a := range n.Artifacts {
		a.MustContain = append([]string(nil), a.MustContain...)
		c.Artifacts[i] = a
	}
	if n.Discover != nil {
		d := *n.Discover
		c.Discover = &d
	}
	return &c
}

// Children expands a fan-out template into one node per unit. Every child is an
// ordinary node: its own lease, artifacts, retries, and trace events.
func Children(template *Node, parentID string, units []Unit) []*Node {
	out := make([]*Node, 0, len(units))
	for _, u := range units {
		child := template.Clone()
		child.ID = parentID + "/" + u.Slug
		child.FanOut = ""
		child.FanIn = ""
		child.Discover = nil
		child.Title = Subst(template.Title, u)
		child.Prompt = Subst(template.Prompt, u)
		child.Check = Subst(template.Check, u)
		child.Workdir = Subst(template.Workdir, u)
		child.Deps = []string{parentID}
		for i := range child.Artifacts {
			child.Artifacts[i].Path = Subst(child.Artifacts[i].Path, u)
			for j := range child.Artifacts[i].MustContain {
				child.Artifacts[i].MustContain[j] = Subst(child.Artifacts[i].MustContain[j], u)
			}
			child.Artifacts[i].MustMatch = Subst(child.Artifacts[i].MustMatch, u)
		}
		out = append(out, child)
	}
	return out
}
