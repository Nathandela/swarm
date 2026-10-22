package skeleton

import (
	"bufio"
	"os"
	"strings"
	"testing"
	"time"
)

// quotaLimits is the native sample shape reported in anthropics/claude-code
// issue #97975. All identities and prompt content in this fixture are synthetic.
func TestClaudeQuotaRequiresCurrentPrimaryPromptAndStructuredRejectedWindow(t *testing.T) {
	raw, err := os.ReadFile("testdata/claude-quota-window.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	fixture := string(raw)
	conversation := "33333333-3333-4333-8333-333333333333"
	prompt := "22222222-2222-4222-8222-222222222222"
	now := time.Unix(1790170000, 0)
	cases := []struct {
		name, body, prompt string
		want               bool
	}{
		{"native window", fixture, prompt, true},
		{"same model subscription switch", strings.ReplaceAll(fixture, "five_hour", "seven_day_opus"), prompt, true},
		{"stale hook prompt", fixture, "55555555-5555-4555-8555-555555555555", false},
		{"generic burst", strings.ReplaceAll(fixture, `"status":"rejected"`, `"status":"allowed"`), prompt, false},
		{"credits required", strings.ReplaceAll(strings.ReplaceAll(fixture, "five_hour", "overage"), "1790175600", "0"), prompt, false},
		{"missing reset", strings.ReplaceAll(fixture, "1790175600", "0"), prompt, false},
		{"past reset", strings.ReplaceAll(fixture, "1790175600", "1790160000"), prompt, false},
		{"unrelated thread", strings.ReplaceAll(fixture, conversation, "66666666-6666-4666-8666-666666666666"), prompt, false},
		{"subagent", strings.ReplaceAll(fixture, `"isApiErrorMessage":true`, `"isSidechain":true,"isApiErrorMessage":true`), prompt, false},
		{"broken ancestry", strings.ReplaceAll(fixture, `"parentUuid":"11111111-1111-4111-8111-111111111111"`, `"parentUuid":"77777777-7777-4777-8777-777777777777"`), prompt, false},
		{"newer owner prompt", fixture + `{"type":"user","uuid":"88888888-8888-4888-8888-888888888888","sessionId":"` + conversation + `","promptId":"99999999-9999-4999-8999-999999999999"}` + "\n", prompt, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reset, _ := readClaudeRejectedQuota(bufio.NewReader(strings.NewReader(tc.body)), conversation, tc.prompt, now)
			if !reset.IsZero() != tc.want {
				t.Fatalf("reset=%v want window=%v", reset, tc.want)
			}
		})
	}
}

func TestClaudeAuthenticationFailureMustBeTheCurrentPrimaryTerminalRecord(t *testing.T) {
	raw, err := os.ReadFile("testdata/claude-quota-window.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	body := strings.ReplaceAll(string(raw), `"error":"rate_limit"`, `"error":"authentication_failed"`)
	conversation := "33333333-3333-4333-8333-333333333333"
	prompt := "22222222-2222-4222-8222-222222222222"
	_, failed, err := readClaudeTerminalEvidence(bufio.NewReader(strings.NewReader(body)), conversation, prompt, time.Unix(1790170000, 0))
	if err != nil || !failed {
		t.Fatalf("current native authentication failure not recognized: %v %v", failed, err)
	}
	_, failed, _ = readClaudeTerminalEvidence(bufio.NewReader(strings.NewReader(body)), conversation, "55555555-5555-4555-8555-555555555555", time.Unix(1790170000, 0))
	if failed {
		t.Fatal("stale authentication failure marked current login invalid")
	}
}
