package main

import (
	"fmt"

	"github.com/KooshaPari/dagrun/internal/skill"
)

// cmdSkill prints the agent skill for dagrun: frontmatter plus the command
// table rendered from the single source of truth in internal/skill. An agent
// that loads this skill knows the verbs, the plan format, and the contract
// without having to run --help.
func cmdSkill(args []string) error {
	for _, a := range args {
		if a == "--help" || a == "-h" {
			fmt.Println("dagrun skill — print the agent skill for dagrun (YAML frontmatter + command table).")
			return nil
		}
	}
	fmt.Print(skill.SkillMarkdown())
	return nil
}
