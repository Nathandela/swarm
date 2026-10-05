package accountconfig

import (
	"strings"
	"testing"
)

func TestClaudeFailureDiagnosticRequiresExactMainRequestPair(t *testing.T) {
	main := `{"event":"cli_api_error","data":{"model":"claude-sonnet-4-6","query_source":"repl_main_thread","error_kind":"rate_limit","api_error_type":"rate_limit_error","response":{"status":429,"request_id":"req_current"}}}` + "\n"
	title := strings.ReplaceAll(strings.ReplaceAll(main, "repl_main_thread", "generate_session_title"), "req_current", "req_title")
	proof, err := ReadClaudeFailureModel(strings.NewReader(title+main), "req_current", "rate_limit")
	if err != nil || proof.Model != "claude-sonnet-4-6" || proof.Line != 2 {
		t.Fatal("exact main request pair not recognized")
	}
	for _, style := range []string{"Concise", "Explanatory", "Learning", "Proactive", "custom"} {
		styled := strings.ReplaceAll(main, "repl_main_thread", "repl_main_thread:outputStyle:"+style)
		if _, err := ReadClaudeFailureModel(strings.NewReader(styled), "req_current", "rate_limit"); err != nil {
			t.Fatal("characterized main output style refused", style)
		}
	}
	for name, input := range map[string]string{
		"title-only":        title,
		"different-request": strings.ReplaceAll(main, "req_current", "req_old"),
		"alias":             strings.ReplaceAll(main, "claude-sonnet-4-6", "sonnet"),
		"redacted-model":    strings.ReplaceAll(main, "claude-sonnet-4-6", "nonconforming"),
		"case-override":     strings.ReplaceAll(main, `"model":"claude-sonnet-4-6"`, `"model":"claude-sonnet-4-6","Model":"claude-haiku-4-5"`),
		"wrong-class":       strings.ReplaceAll(main, "rate_limit_error", "invalid_request_error"),
		"duplicate-match":   main + main,
		"duplicate-model":   strings.ReplaceAll(main, `"model":"claude-sonnet-4-6"`, `"model":"claude-haiku-4-5","model":"claude-sonnet-4-6"`),
		"incomplete-tail":   main + `{"event":`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ReadClaudeFailureModel(strings.NewReader(input), "req_current", "rate_limit"); err == nil {
				t.Fatal("ambiguous/missing proof accepted")
			}
		})
	}
}
