package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KooshaPari/dagrun/internal/plan"
	"github.com/KooshaPari/dagrun/internal/store"
)

const testPlan = `
version: 1
run: mcptest
max_parallel: 2
nodes:
  - id: "1"
    kind: DOC
    title: first
    artifacts: [{path: artifacts/a.md, min_bytes: 3}]
  - id: "2"
    kind: DOC
    title: second
    deps: ["1"]
    artifacts: [{path: artifacts/b.md, min_bytes: 3}]
`

// serve drives one session over a static request list and returns the frames.
func serve(t *testing.T, lines []string) []map[string]any {
	t.Helper()
	in := strings.NewReader(strings.Join(lines, "\n") + "\n")
	var out bytes.Buffer
	srv := New(Config{Version: "test"})
	if err := srv.Serve(context.Background(), in, &out); err != nil {
		t.Fatalf("serve: %v", err)
	}
	var frames []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("frame is not JSON: %v\n%s", err, line)
		}
		frames = append(frames, m)
	}
	return frames
}

func TestInitializeAndToolsList(t *testing.T) {
	frames := serve(t, []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	})
	if len(frames) != 2 {
		t.Fatalf("got %d frames, want 2 (the notification must not be answered)", len(frames))
	}
	init, _ := frames[0]["result"].(map[string]any)
	if init["protocolVersion"] != "2025-06-18" {
		t.Errorf("protocolVersion = %v, want the client's", init["protocolVersion"])
	}
	info, _ := init["serverInfo"].(map[string]any)
	if info["name"] != "dagrun" {
		t.Errorf("serverInfo.name = %v", info["name"])
	}
	list, _ := frames[1]["result"].(map[string]any)
	tools, _ := list["tools"].([]any)
	if len(tools) < 9 {
		t.Fatalf("got %d tools, want 9", len(tools))
	}
	seen := map[string]bool{}
	for _, raw := range tools {
		m, _ := raw.(map[string]any)
		seen[m["name"].(string)] = true
		if m["inputSchema"] == nil {
			t.Errorf("tool %v has no inputSchema", m["name"])
		}
	}
	for _, want := range []string{"dagrun_plan", "dagrun_next", "dagrun_claim", "dagrun_done",
		"dagrun_status", "dagrun_validate", "dagrun_explain", "dagrun_fail", "dagrun_skip"} {
		if !seen[want] {
			t.Errorf("missing tool %s", want)
		}
	}
}

func TestUnknownMethodAndMalformedLineDoNotKillTheServer(t *testing.T) {
	frames := serve(t, []string{
		`{"jsonrpc":"2.0","id":1,"method":"nope/nothing"}`,
		`this is not json`,
		`{"jsonrpc":"2.0","id":3,"method":"ping"}`,
	})
	if len(frames) != 3 {
		t.Fatalf("got %d frames, want 3 (server must survive both errors)", len(frames))
	}
	errObj, _ := frames[0]["error"].(map[string]any)
	if code, _ := errObj["code"].(float64); int(code) != -32601 {
		t.Errorf("unknown method code = %v, want -32601", errObj["code"])
	}
	errObj, _ = frames[1]["error"].(map[string]any)
	if code, _ := errObj["code"].(float64); int(code) != -32700 {
		t.Errorf("parse error code = %v, want -32700", errObj["code"])
	}
	if frames[2]["result"] == nil {
		t.Error("ping after errors did not return a result")
	}
}

func TestToolsCallOnARealRun(t *testing.T) {
	root := t.TempDir()
	p, err := plan.Parse([]byte(testPlan))
	if err != nil {
		t.Fatal(err)
	}
	p.Root = root
	dir := filepath.Join(root, ".dagrun")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "run.yaml"), []byte(testPlan), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open(dir, p, []byte(testPlan), true); err != nil {
		t.Fatal(err)
	}
	// make node 1 already satisfied so `next` should not offer it
	if err := os.MkdirAll(filepath.Join(root, "artifacts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "artifacts", "a.md"), []byte("here"), 0o644); err != nil {
		t.Fatal(err)
	}

	wd, _ := os.Getwd()
	defer func() { _ = os.Chdir(wd) }()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}

	frames := serve(t, []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"dagrun_status","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"dagrun_done","arguments":{"node":"1"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"dagrun_done","arguments":{"node":"2"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"dagrun_explain","arguments":{"node":"2"}}}`,
	})
	if len(frames) != 4 {
		t.Fatalf("got %d frames", len(frames))
	}

	// status: two nodes, none done yet
	first := frames[0]["result"].(map[string]any)
	if first["isError"] == true {
		t.Fatalf("status errored: %v", first)
	}
	text := first["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "mcptest") || !strings.Contains(text, `"1"`) {
		t.Errorf("status payload missing run or node: %s", text)
	}

	// done on a satisfied node succeeds
	if frames[1]["result"].(map[string]any)["isError"] == true {
		t.Errorf("done on a verified node signalled an error")
	}
	// done on an unsatisfied node must fail closed
	if frames[2]["result"].(map[string]any)["isError"] != true {
		t.Errorf("done on an unsatisfied node did not fail")
	}
	// explain names the failing predicate
	explain := frames[3]["result"].(map[string]any)
	if explain["isError"] != true {
		t.Errorf("explain on an unsatisfied node should be an error result")
	}
	etext := explain["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(etext, "b.md") || !strings.Contains(etext, "hint") {
		t.Errorf("explain payload lacks the artifact or hint: %s", etext)
	}
}

func TestToolsCallWithoutAPlanIsAnErrorNotACrash(t *testing.T) {
	dir := t.TempDir()
	wd, _ := os.Getwd()
	defer func() { _ = os.Chdir(wd) }()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	frames := serve(t, []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"dagrun_status","arguments":{}}}`,
	})
	if len(frames) != 1 {
		t.Fatalf("got %d frames", len(frames))
	}
	errObj, _ := frames[0]["error"].(map[string]any)
	if errObj == nil {
		t.Fatalf("expected a JSON-RPC error, got %v", frames[0])
	}
	if msg, _ := errObj["message"].(string); !strings.Contains(msg, "no plan installed") {
		t.Errorf("message = %q, want it to name the missing plan", msg)
	}
}
