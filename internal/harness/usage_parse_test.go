package harness

import (
	"strings"
	"testing"
)

func TestParseUsageJcodeShape(t *testing.T) {
	raw := `{"session_id":"s1","provider":"openrouter","model":"m1","text":"hi",
	  "usage":{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":5,"cache_creation_input_tokens":2}}`
	u, err := ParseUsage([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if u == nil {
		t.Fatal("expected usage")
	}
	if u.InputTokens != 100 || u.OutputTokens != 20 {
		t.Errorf("tokens = %+v", u)
	}
	if u.CacheReadTokens != 5 || u.CacheWriteTokens != 2 {
		t.Errorf("cache tokens = %+v", u)
	}
	if u.Provider != "openrouter" || u.Model != "m1" {
		t.Errorf("provider/model = %q/%q", u.Provider, u.Model)
	}
}

func TestParseUsageCodexNDJSON(t *testing.T) {
	raw := `{"type":"thread.started"}
{"type":"turn.completed","usage":{"input_tokens":11,"cached_input_tokens":3,"output_tokens":7}}`
	u, err := ParseUsageNDJSON([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if u == nil {
		t.Fatal("expected usage")
	}
	if u.InputTokens != 11 || u.OutputTokens != 7 {
		t.Errorf("tokens = %+v", u)
	}
	if u.CacheReadTokens != 3 {
		t.Errorf("cached = %d, want 3", u.CacheReadTokens)
	}
}

func TestParseUsageCodexTotalTokenUsage(t *testing.T) {
	raw := `{"info":{"total_token_usage":{"input_tokens":40,"output_tokens":9}}}`
	u, err := ParseUsage([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if u == nil || u.InputTokens != 40 || u.OutputTokens != 9 {
		t.Fatalf("usage = %+v", u)
	}
}

func TestParseUsageUnknownShapeIsNotAnError(t *testing.T) {
	for _, raw := range []string{"", "hello, not json at all", `{"totally":"different"}`} {
		u, err := ParseUsage([]byte(raw))
		if err != nil {
			t.Errorf("%q: unexpected error %v", raw, err)
		}
		if u != nil {
			t.Errorf("%q: expected nil usage, got %+v", raw, u)
		}
	}
}

func TestParseUsageMalformedJSONErrors(t *testing.T) {
	if _, err := ParseUsage([]byte(`{"usage": {`)); err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
}

func TestParseUsagePartialObjectDoesNotZeroFill(t *testing.T) {
	raw := `{"usage":{"output_tokens":5}}`
	u, err := ParseUsage([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if u == nil {
		t.Fatal("expected usage")
	}
	if u.OutputTokens != 5 {
		t.Errorf("output = %d", u.OutputTokens)
	}
	if u.InputTokens != 0 {
		t.Errorf("input should be absent, got %d", u.InputTokens)
	}
}

func TestUsageTotalPrefersReportedTotal(t *testing.T) {
	u := Usage{InputTokens: 3, OutputTokens: 4, TotalTokens: 100}
	if u.Total() != 100 {
		t.Errorf("Total() = %d, want 100", u.Total())
	}
	u2 := Usage{InputTokens: 3, OutputTokens: 4}
	if u2.Total() != 7 {
		t.Errorf("Total() = %d, want 7", u2.Total())
	}
}

func TestSplitTemplateKeepsShellEscapes(t *testing.T) {
	// A template is a shell command: `printf 'a\nb'` must reach the shell
	// with the backslash intact. Treating it as an escape produced "ab".
	got, err := splitTemplate(`sh -c "printf 'a\nb' > f.txt"`)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, `\n`) {
		t.Fatalf("backslash-n was eaten: %q", joined)
	}
	if !strings.Contains(joined, "printf 'a\\nb'") {
		t.Fatalf("single-quoted content was mangled: %q", joined)
	}
}

func TestExecCommandSubstitutesPlaceholders(t *testing.T) {
	bin, args, stdin, err := ExecCommand("mytool --prompt {prompt} --dir {dir}", "hello world", "/tmp/x", "m1")
	if err != nil {
		t.Fatal(err)
	}
	if bin != "mytool" {
		t.Errorf("bin = %q", bin)
	}
	if stdin {
		t.Error("no stdin expected")
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "hello world") {
		t.Errorf("prompt not substituted: %q", joined)
	}
	if !strings.Contains(joined, "/tmp/x") {
		t.Errorf("dir not substituted: %q", joined)
	}
}
