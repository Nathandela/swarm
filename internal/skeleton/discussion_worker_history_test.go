package skeleton

import (
	"bufio"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol/schema"
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
	body := strings.NewReader(`{"type":"event_msg","payload":{"type":"task_started","turn_id":"` + turn + `"}}
{"type":"event_msg","payload":{"type":"item_completed","turn_id":"` + turn + `","item":{"type":"UserMessage","id":"` + inputID + `","content":[{"type":"text","text":"` + input + `","text_elements":[]}]}}}
{"type":"event_msg","payload":{"type":"task_complete","turn_id":"` + turn + `","last_agent_message":"actual result"}}
`)
	proof, err := readDiscussionCodexEvidence(bufio.NewReader(body), turn, discussionInputHash(input))
	if err != nil || proof.TurnID != turn || proof.Text != "actual result" {
		t.Fatalf("proof = %+v, %v", proof, err)
	}
}

func TestDiscussionCodexEvidenceResumesBeyondFirstWindow(t *testing.T) {
	const turn = "019c0000-0000-7000-8000-000000000001"
	var rollout strings.Builder
	rollout.WriteString(`{"type":"event_msg","payload":{"type":"task_started","turn_id":"` + turn + `"}}
{"type":"event_msg","payload":{"type":"user_message","message":"authorized prompt"}}
`)
	for i := 0; i < 17; i++ {
		rollout.WriteString(`{"type":"response_item","payload":{"text":"` + strings.Repeat("x", 512<<10) + `"}}` + "\n")
	}
	rollout.WriteString(`{"type":"event_msg","payload":{"type":"task_complete","turn_id":"` + turn + `","last_agent_message":"result after first window"}}
`)
	contents := rollout.String()
	first := &discussionTranscriptScan{windowEnd: loginTranscriptTailBytes}
	proof, err := first.readCodex(bufio.NewReader(strings.NewReader(contents)), "", discussionInputHash("authorized prompt"))
	if err != errDiscussionScanWindow || proof.TurnID != "" || first.offset < loginTranscriptTailBytes || !first.matched {
		t.Fatalf("first window: %+v matched=%t offset=%d err=%v", proof, first.matched, first.offset, err)
	}
	first.windowEnd = first.offset + loginTranscriptTailBytes
	proof, err = first.readCodex(bufio.NewReader(strings.NewReader(contents[first.offset:])), "", discussionInputHash("authorized prompt"))
	if err != nil || proof.TurnID != turn || proof.Text != "result after first window" {
		t.Fatalf("resumed scan: %+v %v", proof, err)
	}
}

func TestDiscussionCodexEvidenceHardLimitIsTerminal(t *testing.T) {
	const turn = "019c0000-0000-7000-8000-000000000001"
	file, err := os.CreateTemp(t.TempDir(), "rollout")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	offset := int64(discussionTranscriptMaxBytes - 16)
	if _, err = file.Seek(offset, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if _, err = file.WriteString(`{"type":"event_msg","payload":{"type":"task_complete","turn_id":"` + turn + `","last_agent_message":"over-limit"}}` + "\n"); err != nil {
		t.Fatal(err)
	}
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	meta := persist.Meta{ID: "worker", ConversationID: "provider-session", AgentType: "codex"}
	scope := schema.WorkerHistoryScope{TurnID: turn, PromptHash: discussionInputHash("authorized prompt")}
	key := discussionScanKey{meta.ID, meta.ConversationID, scope}
	daemon := &Daemon{discussionScans: map[discussionScanKey]*discussionTranscriptScan{
		key: {offset: offset, file: info, turn: turn, matched: true},
	}}
	for i := 0; i < 2; i++ {
		proof, limited, err := daemon.readDiscussionTranscript(file, meta, scope)
		if err != nil || !limited || proof.TurnID != "" || !daemon.discussionScans[key].finished {
			t.Fatalf("scan %d: %+v, limited=%t, err=%v", i, proof, limited, err)
		}
	}
}
