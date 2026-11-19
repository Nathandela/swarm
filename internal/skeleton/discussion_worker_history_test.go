package skeleton

import (
	"bufio"
	"strings"
	"testing"
)

func TestDiscussionCodexEvidenceUsesOnlyExactFirstTurn(t *testing.T) {
	const turn = "019c0000-0000-7000-8000-000000000001"
	const private = "019c0000-0000-7000-8000-000000000002"
	const input = "authorized prompt"
	body := strings.NewReader(`{"type":"event_msg","payload":{"type":"task_started","turn_id":"` + turn + `"}}
{"type":"event_msg","payload":{"type":"user_message","message":"` + input + `"}}
{"type":"event_msg","payload":{"type":"agent_message","message":"actual result"}}
{"type":"event_msg","payload":{"type":"task_complete","turn_id":"` + turn + `","last_agent_message":"actual result"}}
{"type":"event_msg","payload":{"type":"task_started","turn_id":"` + private + `"}}
{"type":"event_msg","payload":{"type":"user_message","message":"private text"}}
{"type":"event_msg","payload":{"type":"task_complete","turn_id":"` + private + `","last_agent_message":"private result"}}
`)
	proof, err := readDiscussionCodexEvidence(bufio.NewReader(body), "", discussionInputHash(input))
	if err != nil || proof.TurnID != turn || proof.State != "completed" || proof.Text != "actual result" {
		t.Fatalf("proof = %+v, %v", proof, err)
	}
	body.Reset(`{"type":"event_msg","payload":{"type":"task_started","turn_id":"` + private + `"}}
{"type":"event_msg","payload":{"type":"user_message","message":"private text"}}
{"type":"event_msg","payload":{"type":"task_complete","turn_id":"` + private + `","last_agent_message":"private result"}}
{"type":"event_msg","payload":{"type":"task_started","turn_id":"` + turn + `"}}
{"type":"event_msg","payload":{"type":"user_message","message":"` + input + `"}}
{"type":"event_msg","payload":{"type":"task_complete","turn_id":"` + turn + `","last_agent_message":"actual result"}}
`)
	proof, err = readDiscussionCodexEvidence(bufio.NewReader(body), "", discussionInputHash(input))
	if err != nil || proof.TurnID != "" || proof.Text != "" {
		t.Fatalf("a later matching prompt gained launch attribution: %+v, %v", proof, err)
	}
}

func TestDiscussionCodexEvidenceRejectsAnotherUserInExactTurn(t *testing.T) {
	const turn = "019c0000-0000-7000-8000-000000000001"
	body := strings.NewReader(`{"type":"event_msg","payload":{"type":"task_started","turn_id":"` + turn + `"}}
{"type":"event_msg","payload":{"type":"user_message","message":"authorized prompt"}}
{"type":"event_msg","payload":{"type":"user_message","message":"private steering"}}
{"type":"event_msg","payload":{"type":"task_complete","turn_id":"` + turn + `","last_agent_message":"private result"}}
`)
	proof, err := readDiscussionCodexEvidence(bufio.NewReader(body), turn, discussionInputHash("authorized prompt"))
	if err != nil || proof.TurnID != "" || proof.Text != "" {
		t.Fatalf("mixed native turn leaked evidence: %+v, %v", proof, err)
	}
}

func TestDiscussionCodex170RolloutChecksAdmittedUserItem(t *testing.T) {
	const turn = "019c0000-0000-7000-8000-000000000001"
	const inputID = "019c0000-0000-7000-8000-000000000002"
	const input = "authorized prompt"
	body := strings.NewReader(`{"type":"event_msg","payload":{"type":"task_started","turn_id":"`+turn+`"}}
{"type":"event_msg","payload":{"type":"item_completed","turn_id":"`+turn+`","item":{"type":"UserMessage","id":"`+inputID+`","content":[{"type":"text","text":"`+input+`","text_elements":[]}]}}}
{"type":"event_msg","payload":{"type":"task_complete","turn_id":"`+turn+`","last_agent_message":"actual result"}}
`)
	proof, err := readDiscussionCodexEvidence(bufio.NewReader(body), turn, discussionInputHash(input))
	if err != nil || proof.TurnID != turn || proof.Text != "actual result" {
		t.Fatalf("proof = %+v, %v", proof, err)
	}
}
