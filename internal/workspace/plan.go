package workspace

import "github.com/KooshaPari/dagrun/internal/plan"

// planFromYAML parses a plan file. It is a thin wrapper so the workspace
// package does not need to re-implement plan decoding.
func planFromYAML(raw []byte) (*plan.Plan, error) {
	return plan.Parse(raw)
}
