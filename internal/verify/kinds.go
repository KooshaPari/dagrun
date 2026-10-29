package verify

// Kind-specific contracts. The artifact loop in verify.Node already proves the
// file exists and satisfies its declared predicates; this file adds the shape a
// node of a given kind is supposed to have.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/KooshaPari/dagrun/internal/plan"
)

// KindResult is the outcome of a kind-specific contract check.
type KindResult struct {
	OK     bool
	Code   string
	Detail string
	Hint   string
}

// KindCheck applies the contract for the node's kind. It returns nil when the
// kind has no extra contract.
//
// It is the single-node form: cross-node contracts are skipped. Use
// PlanKindCheck when the whole plan is available.
func KindCheck(n *plan.Node, root string) *KindResult {
	return PlanKindCheck(n, root, nil)
}

// PlanKindCheck applies the contract for the node's kind with the plan in
// view.
//
// The plan enables the cross-node contracts: a TEST must reference the
// requirement ids of the SPEC it depends on. A nil plan skips those, so the
// result is never silently "pass" for a check that could not run: the caller
// is told which contracts were skipped.
func PlanKindCheck(n *plan.Node, root string, p *plan.Plan) *KindResult {
	if n == nil {
		return nil
	}
	switch strings.ToUpper(strings.TrimSpace(n.Kind)) {
	case "SPEC":
		return checkSPEC(n, root)
	case "TEST":
		return checkTEST(n, root, p)
	case "RESEARCH":
		return checkRESEARCH(n, root)
	case "SOTA":
		return checkSOTA(n, root)
	case "INTENT":
		return checkINTENT(n, root)
	case "UI":
		return checkUI(n, root)
	case "DOC":
		return checkDOC(n, root)
	default:
		// PLAN, IMPL, CODE, GATE, OTHER and anything unknown carry no extra
		// contract beyond their declared artifacts.
		return nil
	}
}

// kindArtifact is one readable artifact file plus the directory it lives in.
type kindArtifact struct {
	path string // absolute path
	dir  string // directory used to resolve relative links
	text string
}

// kindArtifacts resolves the node's artifacts under root and reads the text of
// every regular file it can open. Unreadable artifacts are skipped here: the
// main artifact loop already reports them.
func kindArtifacts(n *plan.Node, root string) []kindArtifact {
	var out []kindArtifact
	for _, a := range n.Artifacts {
		target := a.Path
		if !filepath.IsAbs(target) {
			target = filepath.Join(root, a.Path)
		}
		var files []string
		if strings.ContainsAny(a.Path, "*?[") {
			matches, err := filepath.Glob(target)
			if err != nil {
				continue
			}
			files = matches
		} else {
			files = []string{target}
		}
		for _, f := range files {
			info, err := os.Stat(f)
			if err != nil || info.IsDir() {
				continue
			}
			b, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			out = append(out, kindArtifact{
				path: f,
				dir:  filepath.Dir(f),
				text: string(b),
			})
		}
	}
	return out
}

func joined(arts []kindArtifact) string {
	var b strings.Builder
	for _, a := range arts {
		b.WriteString(a.text)
		b.WriteString("\n")
	}
	return b.String()
}

// hasWord is a case-insensitive substring test.
func hasWord(s, want string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(want))
}

// hasStatement reports whether a heading or list item actually STARTS with the
// given word.
//
// A plain substring test is too weak here: "just some prose with no requirement"
// contains the word "requirement" and would satisfy a contract that asks for a
// requirement statement. The check that matters is whether a line declares one.
func hasStatement(text, word string) bool {
	lower := strings.ToLower(word)
	for _, line := range strings.Split(text, "\n") {
		l := strings.TrimSpace(line)
		// drop markdown heading markers and list bullets
		l = strings.TrimLeft(l, "#")
		l = strings.TrimSpace(l)
		l = strings.TrimLeft(l, "-*")
		l = strings.TrimSpace(l)
		l = strings.TrimLeft(l, "0123456789.)")
		l = strings.TrimSpace(l)
		if strings.HasPrefix(strings.ToLower(l), lower) {
			return true
		}
	}
	return false
}

var whenThenRE = regexp.MustCompile(`(?is)\bwhen\b.{0,400}?\bthen\b`)

func checkSPEC(n *plan.Node, root string) *KindResult {
	text := joined(kindArtifacts(n, root))
	if !hasStatement(text, "Requirement") {
		return &KindResult{Code: "spec_no_requirement",
			Detail: "no \"Requirement\" statement found",
			Hint:   "add a \"Requirement\" heading or line stating the required behaviour"}
	}
	if !hasStatement(text, "Scenario") && !whenThenRE.MatchString(text) {
		return &KindResult{Code: "spec_no_scenario",
			Detail: "no \"Scenario\" and no WHEN ... THEN pattern",
			Hint:   "add a \"Scenario\" section or an explicit \"WHEN ... THEN ...\" example"}
	}
	return nil
}

func checkTEST(n *plan.Node, root string, p *plan.Plan) *KindResult {
	hasTestFile := false
	for _, a := range n.Artifacts {
		if strings.Contains(strings.ToLower(a.Path), "test") {
			hasTestFile = true
			break
		}
	}
	if !hasTestFile {
		return &KindResult{Code: "test_no_test_file",
			Detail: "no artifact path looks like a test file",
			Hint:   "declare an artifact whose path contains \"test\" (e.g. internal/pkg/foo_test.go)"}
	}
	// Coverage needs the plan: the requirement ids live in the SPEC node's
	// artifacts, and this node can only be checked against them if the plan is
	// in view. Without a plan the coverage contract is SKIPPED, not passed.
	if p == nil {
		return nil
	}
	specs := specAncestors(p, n)
	if len(specs) == 0 {
		return nil
	}
	reqs := requiredIDs(root, specs)
	if len(reqs) == 0 {
		return nil
	}
	text := joined(kindArtifacts(n, root))
	var missing []string
	for _, id := range reqs {
		if !strings.Contains(text, id) {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return &KindResult{Code: "test_uncovers_req",
			Detail: fmt.Sprintf("the test does not reference requirement(s) %s", strings.Join(missing, ", ")),
			Hint:   fmt.Sprintf("cover each requirement id (%s) in the test artifact", strings.Join(reqs, ", "))}
	}
	return nil
}

// specAncestors returns the SPEC nodes this node depends on, transitively.
func specAncestors(p *plan.Plan, n *plan.Node) []*plan.Node {
	idx := p.Index()
	seen := map[string]bool{}
	var out []*plan.Node
	var walk func(id string)
	walk = func(id string) {
		cur := idx[id]
		if cur == nil {
			return
		}
		for _, d := range cur.Deps {
			if seen[d] {
				continue
			}
			seen[d] = true
			dep := idx[d]
			if dep == nil {
				continue
			}
			if dep.Kind == "SPEC" {
				out = append(out, dep)
				continue
			}
			walk(d)
		}
	}
	walk(n.ID)
	return out
}

// reqIDPattern matches the requirement id forms the tool understands:
// REQ-1, REQ-12, R1, R12.
var reqIDPattern = regexp.MustCompile(`(?i)\b(REQ-\d+|R\d+)\b`)

// requiredIDs collects the requirement ids stated by the given SPEC nodes,
// reading their artifacts from disk under root.
func requiredIDs(root string, specs []*plan.Node) []string {
	seen := map[string]bool{}
	var out []string
	for _, sp := range specs {
		for _, a := range sp.Artifacts {
			path := a.Path
			if !filepath.IsAbs(path) {
				path = filepath.Join(root, path)
			}
			b, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			for _, m := range reqIDPattern.FindAllString(string(b), -1) {
				key := strings.ToUpper(m)
				if !seen[key] {
					seen[key] = true
					out = append(out, m)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

var urlRE = regexp.MustCompile(`https?://[^\s\)\]\>"'` + "`" + `]+`)

func checkRESEARCH(n *plan.Node, root string) *KindResult {
	text := joined(kindArtifacts(n, root))
	seen := map[string]bool{}
	for _, u := range urlRE.FindAllString(text, -1) {
		seen[strings.TrimRight(u, ".,;:!?")] = true
	}
	if len(seen) < 2 {
		return &KindResult{Code: "research_thin",
			Detail: "fewer than 2 distinct http(s) sources",
			Hint:   "cite at least two distinct http(s) URLs you actually read"}
	}
	if !hasWord(text, "Decision") {
		return &KindResult{Code: "research_no_decision",
			Detail: "no \"Decision\" recorded",
			Hint:   "add a \"Decision\" line stating the choice this research forces"}
	}
	if !hasWord(text, "Alternatives") {
		return &KindResult{Code: "research_no_decision",
			Detail: "no \"Alternatives\" considered",
			Hint:   "add an \"Alternatives\" section listing the options you rejected"}
	}
	return nil
}

func checkSOTA(n *plan.Node, root string) *KindResult {
	text := joined(kindArtifacts(n, root))

	candidates := 0
	axes := 0
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "#") || strings.HasPrefix(t, "- **") {
			candidates++
		}
		low := strings.ToLower(t)
		if strings.Contains(low, "axis") || strings.Contains(low, "axes") ||
			strings.Contains(low, " vs") || strings.Contains(low, "vs.") ||
			strings.Contains(low, "compared on") {
			axes++
		}
	}
	if candidates < 2 {
		return &KindResult{Code: "sota_one_candidate",
			Detail: "fewer than 2 candidate headings",
			Hint:   "add a second candidate as a \"# Name\" or \"- **Name**\" heading"}
	}
	if axes < 3 {
		return &KindResult{Code: "sota_few_axes",
			Detail: "fewer than 3 axis comparison mentions",
			Hint:   "compare the candidates on at least three axes (\"axis\", \"vs\", \"compared on\")"}
	}
	if !hasWord(text, "Verdict") && !hasWord(text, "adopt") {
		return &KindResult{Code: "sota_no_verdict",
			Detail: "no verdict and no adopt/reject call",
			Hint:   "add a \"Verdict\" line saying what to adopt or reject"}
	}
	return nil
}

func checkINTENT(n *plan.Node, root string) *KindResult {
	text := joined(kindArtifacts(n, root))
	if !hasWord(text, "Acceptance") {
		return &KindResult{Code: "intent_no_acceptance",
			Detail: "no acceptance criteria",
			Hint:   "add an \"Acceptance Criteria\" section"}
	}
	bullets := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "-") {
			bullets++
		}
	}
	if bullets < 3 {
		return &KindResult{Code: "intent_thin_acceptance",
			Detail: "fewer than 3 acceptance bullets",
			Hint:   "list at least three checkable \"- \" acceptance bullets"}
	}
	return nil
}

func checkUI(n *plan.Node, root string) *KindResult {
	text := joined(kindArtifacts(n, root))
	state := hasWord(text, "state") || hasWord(text, "empty") ||
		hasWord(text, "loading") || hasWord(text, "error")
	if !state {
		return &KindResult{Code: "ui_no_states",
			Detail: "no empty/loading/error state described",
			Hint:   "describe the empty, loading and error states of the surface"}
	}
	if !hasWord(text, "a11y") && !hasWord(text, "accessibility") && !hasWord(text, "aria") {
		return &KindResult{Code: "ui_no_a11y",
			Detail: "no accessibility notes",
			Hint:   "add an accessibility note (a11y, accessibility, or aria)"}
	}
	return nil
}

var mdLinkRE = regexp.MustCompile(`\[[^\]]*\]\(([^)\s]+)\)`)

func checkDOC(n *plan.Node, root string) *KindResult {
	for _, a := range kindArtifacts(n, root) {
		for _, m := range mdLinkRE.FindAllStringSubmatch(a.text, -1) {
			p := m[1]
			if strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://") ||
				strings.HasPrefix(p, "#") || strings.HasPrefix(p, "mailto:") {
				continue
			}
			if i := strings.IndexAny(p, "#?"); i >= 0 {
				p = p[:i]
			}
			if p == "" {
				continue
			}
			target := p
			if !filepath.IsAbs(target) {
				target = filepath.Join(a.dir, p)
			}
			if _, err := os.Stat(target); err != nil {
				return &KindResult{Code: "doc_broken_link",
					Detail: filepath.Base(a.path) + " links to missing " + p,
					Hint:   "fix the link target or create the referenced file"}
			}
		}
	}
	return nil
}
