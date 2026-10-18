package skeleton

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Nathandela/swarm/internal/adapter/opencode"
	"github.com/Nathandela/swarm/internal/opencodebridge"
	"testing"
)

type childTranscriptReader struct {
	*ocReaderFake
	row             json.RawMessage
	err             error
	target, message string
	calls           int
}

func (r *childTranscriptReader) MessageForSession(_ context.Context, target, id string) (json.RawMessage, error) {
	r.calls++
	r.target, r.message = target, id
	return r.row, r.err
}
func TestOpenCodeTranscriptReadsLateChildThroughExactAncestryProof(t *testing.T) {
	f := newOpenCodeSendFixture(t)
	meta, _ := f.d.core.Get(f.binding.WorkerID)
	source, err := newOpenCodeTranscript(f.d, meta, f.identity)
	if err != nil {
		t.Fatal(err)
	}
	child := "ses_child"
	view := OpenCodeView{Root: meta.ConversationID, Generation: meta.OpenCodeBinding.Generation, CurrentScopeKnown: true, Descendants: []OpenCodeDescendant{{SessionID: child, ParentID: meta.ConversationID}}}
	if err = source.view(view); err != nil {
		t.Fatal(err)
	}
	view.Descendants = nil
	if err = source.view(view); err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"info":{"id":"msg_child","sessionID":"ses_child","role":"assistant","parentID":"msg_child_user","time":{"created":1000,"completed":1100},"finish":"stop"},"parts":[{"id":"prt_child","messageID":"msg_child","sessionID":"ses_child","type":"text","text":"late child final"}]}`)
	reader := &childTranscriptReader{ocReaderFake: newOCReaderFake(), row: raw}
	event := opencode.Event{MessageUpdated: &opencode.MessageUpdatedEvent{SessionID: child, Info: opencode.Message{ID: "msg_child", SessionID: child, Role: "assistant"}}}
	got, err := source.event(context.Background(), reader, event)
	if err != nil || got == nil || reader.calls != 1 || reader.target != child || reader.message != "msg_child" {
		t.Fatalf("late child not read with exact native scope: calls=%d row=%s err=%v", reader.calls, got, err)
	}
	if err = source.row(got, "live"); err != nil {
		t.Fatal(err)
	}
	partEvent := opencode.Event{MessagePartUpdated: &opencode.MessagePartUpdatedEvent{SessionID: child, Part: opencode.Part{ID: "prt_child", SessionID: child, MessageID: "msg_child", Type: "text"}}}
	if got, err = source.event(context.Background(), reader, partEvent); err != nil || got == nil || reader.calls != 2 {
		t.Fatal("child part update bypassed full exact read")
	}
	reader.err = &opencodebridge.Error{Code: 422, Reason: "request_unrelated"}
	if got, err = source.event(context.Background(), reader, event); err == nil || got != nil {
		t.Fatal("retained child ID bypassed fresh native ancestry refusal")
	}
	reader.err = nil
	reader.row = json.RawMessage(`{"info":{"id":"msg_other","sessionID":"ses_child","role":"assistant","parentID":"msg_child_user","time":{"created":1000}},"parts":[]}`)
	if _, err = source.event(context.Background(), reader, event); err == nil {
		t.Fatal("different message accepted as exact child read")
	}
	calls := reader.calls
	event.MessageUpdated.Info.SessionID = "ses_foreign"
	event.MessageUpdated.SessionID = "ses_foreign"
	if got, err = source.event(context.Background(), reader, event); err != nil || got != nil || reader.calls != calls {
		t.Fatal("unverified foreign event entered child projection")
	}
	view.Descendants = nil
	for n := 0; n < 100; n++ {
		view.Descendants = append(view.Descendants, OpenCodeDescendant{SessionID: fmt.Sprintf("ses_child%d", n), ParentID: meta.ConversationID})
	}
	if err = source.view(view); err != nil {
		t.Fatal(err)
	}
	if len(source.priorDescendants) != 64 || !source.inScope("ses_child0") {
		t.Fatal("candidate cache unbounded or active scope lost")
	}
	view.Descendants = nil
	if err = source.view(view); err != nil {
		t.Fatal(err)
	}
	if source.inScope("ses_child0") || !source.inScope("ses_child99") {
		t.Fatal("prior descendant bound did not evict oldest candidate")
	}

}
