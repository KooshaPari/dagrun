package plan

import (
	"fmt"
	"strings"
)

// Prompt renders the instruction an agent receives for a node.
//
// The point of putting the prompt in the tool is consistency: every node gets
// the same contract, so "done" means the same thing everywhere. A node may
// override the body with its own `prompt`, but the artifact contract is always
// appended by dagrun and cannot be edited away by the node author.
func Prompt(p *Plan, n *Node, idx map[string]*Node) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# Work node %s — %s\n\n", n.ID, n.Kind)
	if p.Intent != "" {
		fmt.Fprintf(&b, "## Run intent\n\n%s\n\n", strings.TrimSpace(p.Intent))
	}
	if n.Title != "" && n.Title != n.ID {
		fmt.Fprintf(&b, "## This node\n\n%s\n\n", n.Title)
	}
	if n.Prompt != "" {
		fmt.Fprintf(&b, "## Task\n\n%s\n\n", strings.TrimSpace(n.Prompt))
	}

	if len(n.Deps) > 0 {
		b.WriteString("## Inputs already produced\n\n")
		for _, d := range n.Deps {
			dep := idx[d]
			if dep == nil {
				continue
			}
			paths := make([]string, 0, len(dep.Artifacts))
			for _, a := range dep.Artifacts {
				paths = append(paths, a.Path)
			}
			if len(paths) > 0 {
				fmt.Fprintf(&b, "- %s (%s): %s\n", dep.ID, dep.Kind, strings.Join(paths, ", "))
			}
		}
		b.WriteString("\n")
	}

	b.WriteString("## Required output\n\n")
	if len(n.Artifacts) == 0 && n.Check == "" {
		b.WriteString("This node has no declared artifacts. Attest completion with `dagrun done`.\n")
	}
	for _, a := range n.Artifacts {
		fmt.Fprintf(&b, "- `%s`", a.Path)
		var reqs []string
		if a.MinBytes > 0 {
			reqs = append(reqs, fmt.Sprintf("at least %d bytes", a.MinBytes))
		}
		if len(a.MustContain) > 0 {
			quoted := make([]string, 0, len(a.MustContain))
			for _, s := range a.MustContain {
				quoted = append(quoted, fmt.Sprintf("%q", s))
			}
			reqs = append(reqs, "containing "+strings.Join(quoted, ", "))
		}
		if a.MustMatch != "" {
			reqs = append(reqs, fmt.Sprintf("matching /%s/", a.MustMatch))
		}
		if a.MinCount > 1 {
			reqs = append(reqs, fmt.Sprintf("at least %d files", a.MinCount))
		}
		if len(reqs) > 0 {
			fmt.Fprintf(&b, " — %s", strings.Join(reqs, "; "))
		}
		b.WriteString("\n")
	}
	if n.Check != "" {
		fmt.Fprintf(&b, "- The command `%s` must exit 0.\n", n.Check)
	}

	b.WriteString("\n## Rules\n\n")
	b.WriteString("1. Create the artifacts above. Do not describe them in chat instead of writing them.\n")
	b.WriteString("2. Do not modify artifacts owned by other nodes.\n")
	b.WriteString("3. When finished, stop. dagrun verifies the artifacts itself and does not trust your claim.\n")
	if n.Optional {
		b.WriteString("4. This node is optional: if it cannot be done, reply with SKIPPED and the reason.\n")
	}
	return b.String()
}
