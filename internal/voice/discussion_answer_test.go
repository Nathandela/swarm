package voice

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

func TestFreeTextAnswerSettlesOnceInsideCurrentHumanTurn(t *testing.T) {
	c, s, _, b := newControllerFixture(t)
	c.discussion = &DiscussionScope{InstallationID: "install", GuildID: "g", ThreadID: "t", DiscussionID: "d"}
	makeQuestion := func(id, kind string) Request {
		r, err := s.PutRequest(b, Request{ID: id, TaskID: "file", Kind: kind, NativeInputID: "prior-input", NativeTurnID: "prior-turn", Payload: json.RawMessage(`{"question":"Which file?"}`), ExpiresAt: time.Now().Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.update(func(st *State) error {
			conv := st.Conversations[b.ConversationID]
			conv.LastOutput++
			r.OutputSequence = conv.LastOutput
			putRecord(st, "conversations", &st.Conversations, b.ConversationID, conv)
			r.Exported = true
			putRecord(st, "requests", &st.Requests, key(b.ConversationID, r.ID), r)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return r
	}
	one, two, native := makeQuestion("one", "manager"), makeQuestion("two", "manager"), makeQuestion("native", "native")
	origin := schema.MessageOrigin{Kind: "external", InstallationID: "install", DiscussionID: "d", GuildID: "g", ChannelID: "t", MessageID: "m", RequestID: "event", ActorID: "bob", RequesterID: "bob", Revision: "revision"}
	a, err := c.submitDiscussionInProject(origin, "decorated context, not the answer", "main.go", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.update(func(st *State) error {
		a.NativeInputID = "current"
		a.NativeTurnID = "turn"
		a.Phase = "running"
		putRecord(st, "admissions", &st.Admissions, admissionKey(b.ConversationID, a.Sequence), a)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, r := range []Request{one, one} {
		out, err := c.settleDiscussionAnswer(a, DiscussionAnswerInput{RequestID: r.ID, Hash: r.Hash})
		if err != nil || out.AnswerID != "event" || string(out.Answer) != `"main.go"` {
			t.Fatal(out, err)
		}
	}
	if _, err := c.settleDiscussionAnswer(a, DiscussionAnswerInput{RequestID: two.ID, Hash: two.Hash}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err := c.settleDiscussionAnswer(a, DiscussionAnswerInput{RequestID: native.ID, Hash: native.Hash}); err == nil {
		t.Fatal("answered a native permission")
	}
	st, err := s.Snapshot()
	if err != nil || len(st.Admissions) != 1 || st.Requests[key(b.ConversationID, "two")].State != "pending" {
		t.Fatal(st.Admissions, err)
	}
}
