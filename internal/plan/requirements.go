package plan

import "strings"

// KindRequirements describes what a node's kind-specific contract will check,
// in the agent's terms.
//
// The reason this exists: dagrun verifies a node against BOTH the declared
// artifact contract and the kind's own contract. The prompt used to state only
// the first, so an agent could satisfy every declared requirement and still
// fail verification ("intent_thin_acceptance") with no way to anticipate it.
// A contract the agent cannot see is a contract the agent cannot meet.
//
// Each entry is a short, checkable instruction. The verifier's own codes and
// hints remain the authority for the failure message; this is the same
// contract stated in advance.
//
// The list is kept here rather than in the verify package because verify
// imports plan: the prompt builder lives in plan and cannot import verify
// without a cycle.
var kindRequirements = map[string][]string{
	"INTENT": {
		"Include a heading containing the words `Acceptance Criteria`.",
		"Under it, list at least 3 bullets that each start with `- `.",
	},
	"SPEC": {
		"Include at least one requirement statement (a line starting with `Requirement`).",
		"Include at least one scenario or example (a line starting with `Scenario` or `Example`).",
	},
	"RESEARCH": {
		"Include a `## Sources` section listing where the findings came from.",
		"Include a `## Decision` section stating what was decided and why.",
	},
	"SOTA": {
		"Name at least one prior candidate (a `## Candidate` section).",
		"Compare candidates on at least three axes (a `## Axes` section).",
		"State a verdict: what to adopt and what to reject (a `## Verdict` section).",
	},
	"UI": {
		"Describe the empty, loading and error states.",
		"Describe the accessibility story (mention `a11y`, `accessibility` or `aria`).",
	},
	"TEST": {
		"Include at least one test file that exercises the node's requirements.",
		"Reference the requirement ids of the SPEC this node depends on.",
	},
	"DOC": {
		"Every relative link must resolve to a file that exists.",
	},
}

// KindRequirements returns the kind-specific contract as a list of
// instructions. It returns nil for kinds that carry no extra contract.
func KindRequirements(kind string) []string {
	return kindRequirements[strings.ToUpper(strings.TrimSpace(kind))]
}
