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
	input := `{"sessionId":"` + session + `","type":"user","uuid":"` + user + `","promptId":"` + prompt + `","message":{"role":"user","content":"authorized prompt"}}` + "\n"
	result := `{"sessionId":"` + session + `","type":"attachment","uuid":"` + attachment + `","parentUuid":"` + user + `"}` + "\n" + `{"sessionId":"` + session + `","type":"assistant","uuid":"` + assistant + `","parentUuid":"` + attachment + `","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"exact evidence"}]}}` + "\n"
	private := strings.ReplaceAll(strings.ReplaceAll(input+result, "authorized prompt", "private question"), "exact evidence", "private answer")
	for _, test := range []struct{ name, body, text string }{
		{"authorized then private", input + result + private, "exact evidence"},
		{"private then authorized", private + input + result, ""},
		{"similar", strings.Replace(input+result, "authorized prompt", "authorized prompt!", 1), ""},
		{"mismatched ancestry", strings.Replace(input+result, `"parentUuid":"`+user, `"parentUuid":"`+session, 1), ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			proof, _ := readDiscussionClaudeEvidence(bufio.NewReader(strings.NewReader(test.body)), session, "", discussionInputHash("authorized prompt"))
			if proof.Text != test.text || test.text != "" && (proof.TurnID != prompt || proof.State != "completed") {
				t.Fatalf("proof %+v", proof)
			}
		})
	}
}
