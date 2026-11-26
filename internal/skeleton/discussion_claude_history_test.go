package skeleton

import (
	"bufio"
	"strings"
	"testing"
)

func TestDiscussionClaudeEvidenceRejectsPrivateOrSimilarPrompt(t *testing.T) {
	const session = "019c0000-0000-7000-8000-000000000010"
	const prompt = "019c0000-0000-7000-8000-000000000011"
	const user = "019c0000-0000-7000-8000-000000000012"
	const assistant = "019c0000-0000-7000-8000-000000000013"
	const attachment = "019c0000-0000-7000-8000-000000000014"
	const toolCall = "019c0000-0000-7000-8000-000000000015"
	const toolResponse = "019c0000-0000-7000-8000-000000000016"
	input := `{"sessionId":"` + session + `","type":"user","uuid":"` + user + `","promptId":"` + prompt + `","message":{"role":"user","content":"authorized prompt"}}` + "\n"
	attachmentRow := `{"sessionId":"` + session + `","type":"attachment","uuid":"` + attachment + `","parentUuid":"` + user + `"}` + "\n"
	call := `{"sessionId":"` + session + `","type":"assistant","uuid":"` + toolCall + `","parentUuid":"` + attachment + `","message":{"role":"assistant","stop_reason":"tool_use","content":[{"type":"tool_use","id":"toolu_report","name":"Read","input":{"file_path":"report.txt"}}]}}` + "\n"
	toolResultPrompt := `"promptId":"` + prompt + `",`
	toolResultContent := `[{"type":"tool_result","tool_use_id":"toolu_report","content":"Report data"}]`
	toolResult := `{"sessionId":"` + session + `","type":"user","uuid":"` + toolResponse + `","parentUuid":"` + toolCall + `",` + toolResultPrompt + `"message":{"role":"user","content":` + toolResultContent + `}}` + "\n"
	result := `{"sessionId":"` + session + `","type":"assistant","uuid":"` + assistant + `","parentUuid":"` + toolResponse + `","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"exact evidence"}]}}` + "\n"
	body := input + attachmentRow + call + toolResult + result
	private := strings.ReplaceAll(strings.ReplaceAll(body, "authorized prompt", "private question"), "exact evidence", "private answer")
	for _, test := range []struct{ name, body, text string }{
		{"same prompt tool result", body, "exact evidence"},
		{"untagged tool result", input + attachmentRow + call + strings.Replace(toolResult, toolResultPrompt, "", 1) + result, "exact evidence"},
		{"other prompt tool result", input + attachmentRow + call + strings.Replace(toolResult, prompt, assistant, 1) + result, ""},
		{"new user text under the same prompt", strings.Replace(body, toolResultContent, `[{"type":"text","text":"new instruction"}]`, 1), ""},
		{"authorized then private", body + private, "exact evidence"},
		{"private then authorized", private + body, ""},
		{"similar", strings.Replace(body, "authorized prompt", "authorized prompt!", 1), ""},
		{"mismatched ancestry", strings.Replace(body, `"parentUuid":"`+user, `"parentUuid":"`+session, 1), ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			proof, err := readDiscussionClaudeEvidence(bufio.NewReader(strings.NewReader(test.body)), session, "", discussionInputHash("authorized prompt"))
			wantTurn, wantState := "", ""
			if test.text != "" {
				wantTurn, wantState = prompt, "completed"
			}
			if err != nil || proof.Text != test.text || proof.TurnID != wantTurn || proof.State != wantState {
				t.Fatalf("proof %+v: %v", proof, err)
			}
		})
	}
}
