package plan

import (
	"embed"
	"fmt"
	"sort"
	"strings"
)

// The shipped plan templates live beside this package so they can be embedded
// in the binary: a template is useful precisely when there is no checkout to
// read it from. Add a template by dropping a .yaml file in templates/ and a
// description in templateDescriptions; TestTemplateDescriptions fails until
// both exist.
//
//go:embed templates/*.yaml
var templateFS embed.FS

// templateDir is the embedded directory holding the YAML files.
const templateDir = "templates"

// TemplateName is one plan template.
type TemplateName struct {
	Name        string
	Description string
}

// templateDescriptions says what each template is for, in one line. The text is
// the answer to "which one do I want?" at the moment of choosing.
var templateDescriptions = map[string]string{
	"greenfield":      "a new capability, from intent to a passing gate",
	"bugfix":          "a defect, with a regression test that fails first",
	"migration":       "a change to existing structure, planned before it is written",
	"audit":           "an investigation that ends in findings, not in code",
	"next-usable-cvp": "the checkpoint where the work is safe to hand over",
	"lint-sweep":      "a tool decides how many nodes there are: discover, fan out, reduce",
}

// Templates lists the available templates, sorted by name.
func Templates() []TemplateName {
	entries, err := templateFS.ReadDir(templateDir)
	if err != nil {
		return nil
	}
	out := make([]TemplateName, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".yaml")
		out = append(out, TemplateName{Name: name, Description: templateDescriptions[name]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Template returns the YAML of one template.
func Template(name string) ([]byte, error) {
	name = strings.TrimSuffix(strings.TrimSpace(name), ".yaml")
	if name == "" || strings.ContainsAny(name, `/\`) {
		return nil, fmt.Errorf("invalid template name %q", name)
	}
	raw, err := templateFS.ReadFile(templateDir + "/" + name + ".yaml")
	if err != nil {
		return nil, fmt.Errorf("unknown template %q (available: %s)", name, templateNames())
	}
	return raw, nil
}

// templateNames joins the available names for error messages.
func templateNames() string {
	all := Templates()
	names := make([]string, 0, len(all))
	for _, t := range all {
		names = append(names, t.Name)
	}
	return strings.Join(names, ", ")
}
