package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KooshaPari/dagrun/internal/plan"
	"github.com/KooshaPari/dagrun/internal/store"
)

// The three tools added to close the adoption gap: trace, submit, run. Each is
// tested through the real JSON-RPC surface rather than by calling the method
// directly, because the point of the tool is the contract a CLIENT sees.

// fixture installs the shared test plan in a temp dir and chdirs into it.
// t.Chdir restores the original working directory on cleanup.
func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Chdir(root)
	// dagrun_plan reads a FILE, so the plan has to exist on disk as well as in
	// the store.
	if err := os.WriteFile(filepath.Join(root, "plan.yaml"), []byte(testPlan), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := plan.Parse([]byte(testPlan))
	if err != nil {
		t.Fatal(err)
	}
	p.Root = root
	if _, err := store.Open(root+"/.dagrun", p, []byte(testPlan), true); err != nil {
		t.Fatal(err)
	}
	return root
}

// textOf pulls the text block out of a tools/call result.
func textOf(t *testing.T, frame map[string]any) string {
	t.Helper()
	res, ok := frame["result"].(map[string]any)
	if !ok {
		t.Fatalf("frame has no result: %v", frame)
	}
	content, _ := res["content"].([]any)
	if len(content) == 0 {
		return ""
	}
	block, _ := content[0].(map[string]any)
	s, _ := block["text"].(string)
	return s
}

func TestToolTraceReturnsAnArrayAndEvents(t *testing.T) {
	fixture(t)
	frames := serve(t, []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"dagrun_plan","arguments":{"file":"plan.yaml","reset":true}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"dagrun_trace","arguments":{}}}`,
	})
	text := textOf(t, frames[1])
	// An empty run must report an ARRAY, not null: a client should not have to
	// special-case a missing field.
	if strings.Contains(text, `"events": null`) {
		t.Fatalf("trace reported null events, want []: %s", text)
	}
	if !strings.Contains(text, `"events"`) {
		t.Fatalf("trace payload lacks events: %s", text)
	}
}

func TestToolTraceLimitReturnsTheTail(t *testing.T) {
	fixture(t)
	// Produce a few events with a real (empty) run, then ask for the last 1.
	frames := serve(t, []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"dagrun_plan","arguments":{"file":"plan.yaml","reset":true}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"dagrun_run","arguments":{"max_nodes":1,"template":"sh -c \"mkdir -p artifacts && printf abc > artifacts/a.md\""}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"dagrun_trace","arguments":{"limit":1}}}`,
	})
	if frames[1]["result"].(map[string]any)["isError"] == true {
		t.Fatalf("run errored: %s", textOf(t, frames[1]))
	}
	text := textOf(t, frames[2])
	var got struct {
		Count  int `json:"count"`
		Events []struct {
			Event string `json:"event"`
		} `json:"events"`
	}
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("trace payload is not JSON: %v\n%s", err, text)
	}
	if got.Count != 1 || len(got.Events) != 1 {
		t.Fatalf("count = %d with %d events, want 1 and 1 (limit must cap both)", got.Count, len(got.Events))
	}
}

func TestToolSubmitDryRunSendsNothing(t *testing.T) {
	fixture(t)
	frames := serve(t, []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"dagrun_plan","arguments":{"file":"plan.yaml","reset":true}}}`,
		// No url is given: a dry run must not need one, and must not dial.
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"dagrun_submit","arguments":{"dry_run":true}}}`,
	})
	res := frames[1]["result"].(map[string]any)
	if res["isError"] == true {
		t.Fatalf("dry run errored: %s", textOf(t, frames[1]))
	}
	text := textOf(t, frames[1])
	if !strings.Contains(text, `"dry_run": true`) {
		t.Fatalf("payload lacks dry_run: %s", text)
	}
	if !strings.Contains(text, `"node:1"`) {
		t.Fatalf("payload lacks the namespaced node id: %s", text)
	}
}

func TestToolSubmitWithoutURLIsAnError(t *testing.T) {
	fixture(t)
	frames := serve(t, []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"dagrun_plan","arguments":{"file":"plan.yaml","reset":true}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"dagrun_submit","arguments":{"dry_run":false}}}`,
	})
	// A real submit with no url is a caller mistake: a protocol error, not a
	// silent no-op.
	if frames[1]["error"] == nil {
		t.Fatalf("submit without url returned a result, want a protocol error: %v", frames[1])
	}
	msg, _ := frames[1]["error"].(map[string]any)["message"].(string)
	if !strings.Contains(msg, "url is required") {
		t.Fatalf("error message = %q, want it to name the missing url", msg)
	}
}

func TestToolSubmitNeverLeaksTheToken(t *testing.T) {
	fixture(t)
	// The token argument names an ENV VAR. A name that is unset must fail with
	// the NAME in the message and no value, and the value must never appear.
	const secret = "super-secret-token-value"
	t.Setenv("DAGRUN_TEST_TOKEN", secret)
	frames := serve(t, []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"dagrun_plan","arguments":{"file":"plan.yaml","reset":true}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"dagrun_submit","arguments":{"url":"http://127.0.0.1:1","token":"DAGRUN_TEST_TOKEN"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"dagrun_submit","arguments":{"url":"http://127.0.0.1:1","token":"DAGRUN_UNSET_TOKEN_NAME"}}}`,
	})
	// Frame 2 fails to connect; the message must not carry the secret.
	raw, _ := json.Marshal(frames[1])
	if strings.Contains(string(raw), secret) {
		t.Fatalf("the token value leaked into the response: %s", raw)
	}
	// Frame 3 names an unset variable and must say so, by NAME.
	msg, _ := frames[2]["error"].(map[string]any)["message"].(string)
	if !strings.Contains(msg, "DAGRUN_UNSET_TOKEN_NAME") {
		t.Fatalf("error message = %q, want the env var NAME", msg)
	}
	if strings.Contains(msg, secret) {
		t.Fatalf("the token value leaked into the error: %s", msg)
	}
}

func TestToolRunIsBoundedAndRefusesOversize(t *testing.T) {
	fixture(t)
	frames := serve(t, []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"dagrun_plan","arguments":{"file":"plan.yaml","reset":true}}}`,
		// Over the cap: refused outright rather than silently clamped, so the
		// caller learns the surface is bounded.
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"dagrun_run","arguments":{"max_nodes":9999}}}`,
	})
	if frames[1]["error"] == nil {
		t.Fatalf("run with max_nodes 9999 returned a result, want a refusal: %v", frames[1])
	}
	msg, _ := frames[1]["error"].(map[string]any)["message"].(string)
	if !strings.Contains(msg, "exceeds the limit") {
		t.Fatalf("error message = %q, want it to name the limit", msg)
	}
}

func TestToolRunExecutesOneNodeAndReportsCost(t *testing.T) {
	fixture(t)
	frames := serve(t, []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"dagrun_plan","arguments":{"file":"plan.yaml","reset":true}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"dagrun_run","arguments":{"max_nodes":1,"template":"sh -c \"mkdir -p artifacts && printf abc > artifacts/a.md\""}}}`,
	})
	res := frames[1]["result"].(map[string]any)
	if res["isError"] == true {
		t.Fatalf("run errored: %s", textOf(t, frames[1]))
	}
	text := textOf(t, frames[1])
	if !strings.Contains(text, `"done": 1`) {
		t.Fatalf("summary does not report 1 done: %s", text)
	}
	// The cost fields must always be present so a caller can budget without a
	// second call.
	if !strings.Contains(text, `"cost_usd"`) || !strings.Contains(text, `"cost_limit"`) {
		t.Fatalf("summary lacks the cost fields: %s", text)
	}
}
