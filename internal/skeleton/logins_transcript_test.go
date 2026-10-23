package skeleton

import (
	"bufio"
	"encoding/json"
	"fmt"
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

func claudeAttachmentChain(t *testing.T) []map[string]any {
	t.Helper()
	const conversation = "33333333-3333-4333-8333-333333333333"
	const prompt = "22222222-2222-4222-8222-222222222222"
	parent := "11111111-1111-4111-8111-111111111111"
	rows := []map[string]any{{"type": "user", "uuid": parent, "sessionId": conversation, "promptId": prompt}}
	for i := 1; i <= 6; i++ {
		id := fmt.Sprintf("0000000%d-0000-4000-8000-000000000000", i)
		rows = append(rows, map[string]any{"type": "attachment", "uuid": id, "parentUuid": parent, "sessionId": conversation, "isSidechain": false})
		parent = id
	}
	rows = append(rows, map[string]any{"type": "assistant", "uuid": "44444444-4444-4444-8444-444444444444", "parentUuid": parent, "sessionId": conversation, "isApiErrorMessage": true, "error": "rate_limit", "quotaLimits": map[string]any{"status": "rejected", "rateLimitType": "five_hour", "resetsAt": int64(1790175600)}})
	return rows
}
func readClaudeAttachmentChain(t *testing.T, rows []map[string]any, prompt string) (time.Time, bool, error) {
	t.Helper()
	var body strings.Builder
	for _, row := range rows {
		if err := json.NewEncoder(&body).Encode(row); err != nil {
			t.Fatal(err)
		}
	}
	return readClaudeTerminalEvidence(bufio.NewReader(strings.NewReader(body.String())), "33333333-3333-4333-8333-333333333333", prompt, time.Unix(1790170000, 0))
}
func TestClaudeTerminalFailureBindsThroughPrimaryAttachments(t *testing.T) {
	const prompt = "22222222-2222-4222-8222-222222222222"
	for _, auth := range []bool{false, true} {
		t.Run(fmt.Sprint(auth), func(t *testing.T) {
			rows := claudeAttachmentChain(t)
			if auth {
				rows[len(rows)-1]["error"] = "authentication_failed"
				delete(rows[len(rows)-1], "quotaLimits")
			}
			reset, failed, err := readClaudeAttachmentChain(t, rows, prompt)
			if err != nil || failed != auth || (!auth && reset.Unix() != 1790175600) || (auth && !reset.IsZero()) {
				t.Fatalf("six validated attachment links lost terminal evidence: reset=%v auth=%v err=%v", reset, failed, err)
			}
			rows = append(rows, map[string]any{"type": "attachment", "uuid": "77777777-7777-4777-8777-777777777777", "parentUuid": rows[len(rows)-1]["uuid"], "sessionId": "33333333-3333-4333-8333-333333333333", "promptId": "99999999-9999-4999-8999-999999999999"})
			reset, failed, err = readClaudeAttachmentChain(t, rows, prompt)
			if err != nil || failed != auth || (!auth && reset.IsZero()) {
				t.Fatal("attachment replaced the terminal failure or current prompt")
			}
		})
	}
}
func TestClaudeAttachmentsCannotInventTerminalFailureOrCrossIdentity(t *testing.T) {
	const prompt = "22222222-2222-4222-8222-222222222222"
	cases := []struct {
		name   string
		mutate func([]map[string]any) []map[string]any
		prompt string
	}{
		{"foreign session", func(r []map[string]any) []map[string]any {
			r[3]["sessionId"] = "66666666-6666-4666-8666-666666666666"
			return r
		}, prompt},
		{"sidechain bridge", func(r []map[string]any) []map[string]any { r[3]["isSidechain"] = true; return r }, prompt},
		{"malformed uuid", func(r []map[string]any) []map[string]any { r[3]["uuid"] = "invalid"; return r }, prompt},
		{"malformed parent", func(r []map[string]any) []map[string]any { r[3]["parentUuid"] = "invalid"; return r }, prompt},
		{"missing parent", func(r []map[string]any) []map[string]any { delete(r[3], "parentUuid"); return r }, prompt},
		{"duplicate uuid", func(r []map[string]any) []map[string]any {
			return append(r[:4], append([]map[string]any{r[3]}, r[4:]...)...)
		}, prompt},
		{"cycle", func(r []map[string]any) []map[string]any { r[3]["parentUuid"] = r[4]["uuid"]; return r }, prompt},
		{"missing link", func(r []map[string]any) []map[string]any {
			r[3]["parentUuid"] = "88888888-8888-4888-8888-888888888888"
			return r
		}, prompt},
		{"unknown bridge", func(r []map[string]any) []map[string]any { r[3]["type"] = "unrecognized-native-record"; return r }, prompt},
		{"stale hook prompt", func(r []map[string]any) []map[string]any { return r }, "99999999-9999-4999-8999-999999999999"},
		{"attachment fake quota", func(r []map[string]any) []map[string]any { r[len(r)-1]["type"] = "attachment"; return r }, prompt},
		{"attachment fake auth", func(r []map[string]any) []map[string]any {
			r[len(r)-1]["type"] = "attachment"
			r[len(r)-1]["error"] = "authentication_failed"
			return r
		}, prompt},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reset, failed, _ := readClaudeAttachmentChain(t, tc.mutate(claudeAttachmentChain(t)), tc.prompt)
			if !reset.IsZero() || failed {
				t.Fatal("unbound or attachment-only terminal evidence accepted")
			}
		})
	}
}
