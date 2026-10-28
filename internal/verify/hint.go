package verify

import (
	"fmt"
	"strings"

	"github.com/KooshaPari/dagrun/internal/plan"
)

// Hint turns a failed report into one actionable line.
//
// The mapping is deliberately mechanical: one predicate code in, one
// instruction out. There is no model, no guessing, and no I/O, so the same
// report always yields the same sentence. `dagrun explain` prints it under the
// failing predicate; tests pin it here.
//
// An empty string means "nothing to fix": the report already passed.
func Hint(n *plan.Node, r Report) string {
	if r.OK {
		return ""
	}
	for _, res := range r.Artifacts {
		if res.OK {
			continue
		}
		a := declaredArtifact(n, res.Path)
		if fn, ok := hintFor[res.Code]; ok {
			return oneLine(fn(n, a, res))
		}
		// Codes we do not name (stat, read, or a future code) still get a
		// pointer at the evidence rather than silence.
		return oneLine(fmt.Sprintf("inspect %s: %s", res.Path, res.Detail))
	}
	if r.Check != nil && !r.Check.OK {
		return checkHint(n, *r.Check)
	}
	return ""
}

// hintFn renders the instruction for one artifact predicate failure.
type hintFn func(n *plan.Node, a plan.Artifact, res ArtifactResult) string

// hintFor is the whole table: one entry per predicate code verify can emit.
var hintFor = map[string]hintFn{
	"missing": func(n *plan.Node, a plan.Artifact, res ArtifactResult) string {
		return fmt.Sprintf("create %s: the path does not exist, then run: dagrun done %s", res.Path, nodeID(n))
	},
	"empty": func(n *plan.Node, a plan.Artifact, res ArtifactResult) string {
		return fmt.Sprintf("write content into %s: it is zero bytes (set allow_empty: true only if \"no findings\" is the real result), then run: dagrun done %s", res.Path, nodeID(n))
	},
	"too_small": func(n *plan.Node, a plan.Artifact, res ArtifactResult) string {
		if a.MinBytes > 0 {
			return fmt.Sprintf("grow %s to at least %d bytes (currently %d), then run: dagrun done %s", res.Path, a.MinBytes, res.Bytes, nodeID(n))
		}
		return fmt.Sprintf("grow %s (%s), then run: dagrun done %s", res.Path, res.Detail, nodeID(n))
	},
	"missing_text": func(n *plan.Node, a plan.Artifact, res ArtifactResult) string {
		return fmt.Sprintf("write the required text into %s (%s), then run: dagrun done %s", res.Path, res.Detail, nodeID(n))
	},
	"no_match": func(n *plan.Node, a plan.Artifact, res ArtifactResult) string {
		want := a.MustMatch
		if want == "" {
			want = res.Detail
		} else {
			want = fmt.Sprintf("regex %q", want)
		}
		return fmt.Sprintf("make %s match %s, then run: dagrun done %s", res.Path, want, nodeID(n))
	},
	"count": func(n *plan.Node, a plan.Artifact, res ArtifactResult) string {
		need := a.MinCount
		if need <= 0 {
			need = 1
		}
		return fmt.Sprintf("add files so %s matches at least %d path(s) (%s), then run: dagrun done %s", res.Path, need, res.Detail, nodeID(n))
	},
	"is_dir": func(n *plan.Node, a plan.Artifact, res ArtifactResult) string {
		return fmt.Sprintf("replace the directory %s with a regular file (or point the artifact at a file inside it), then run: dagrun done %s", res.Path, nodeID(n))
	},
	"bad_regex": func(n *plan.Node, a plan.Artifact, res ArtifactResult) string {
		return fmt.Sprintf("fix the plan: must_match %q on %s is not a valid regex (%s); reinstall with dagrun plan FILE", a.MustMatch, res.Path, res.Detail)
	},
	"bad_glob": func(n *plan.Node, a plan.Artifact, res ArtifactResult) string {
		return fmt.Sprintf("fix the plan: path pattern %q is not a valid glob (%s); reinstall with dagrun plan FILE", res.Path, res.Detail)
	},
}

// checkHint renders the instruction when the node's shell check failed.
func checkHint(n *plan.Node, c CheckResult) string {
	if c.TimedOut {
		return oneLine(fmt.Sprintf("the check timed out: make `%s` finish inside the node timeout, then run: dagrun done %s", c.Command, nodeID(n)))
	}
	msg := fmt.Sprintf("make the check pass: `%s` exited %d", c.Command, c.ExitCode)
	if first := firstLine(c.Output); first != "" {
		msg += " (" + first + ")"
	}
	return oneLine(msg + fmt.Sprintf("; fix the failure, then run: dagrun done %s", nodeID(n)))
}

// declaredArtifact finds the plan-level artifact a result came from, so the
// hint can quote the requirement (min_bytes, must_match) rather than only the
// observed detail. A missing match yields the zero value, which every hint
// tolerates.
func declaredArtifact(n *plan.Node, path string) plan.Artifact {
	if n == nil {
		return plan.Artifact{}
	}
	for _, a := range n.Artifacts {
		if a.Path == path {
			return a
		}
	}
	return plan.Artifact{Path: path}
}

func nodeID(n *plan.Node) string {
	if n == nil || n.ID == "" {
		return "NODE"
	}
	return n.ID
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// oneLine keeps the promise that a hint is a single line, whatever a detail
// string carried in.
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\n", "; ")
	s = strings.ReplaceAll(s, "\t", " ")
	return strings.TrimSpace(s)
}
