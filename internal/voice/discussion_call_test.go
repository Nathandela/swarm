package voice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

func TestCallDiscussionAdmissionRechecksGenerationAtNativeCutover(t *testing.T) {
	c, store, runtime, binding := newControllerFixture(t)
	callLive := true
	scope := DiscussionScope{InstallationID: "install", DiscussionID: "thread-binding", GuildID: "guild", ThreadID: "thread",
		SupervisorSocket: "/historic/swarm.sock",
		VerifyOrigin: func(context.Context, schema.MessageOrigin) error {
			if !callLive {
				return ErrDiscussionSourceChanged
			}
			return nil
		},
		VerifyGrant: func(context.Context, schema.MessageOrigin) error { return nil },
	}
	c.discussion = &scope
	d := &DiscussionController{scope: scope, controller: c, store: store, cfg: Config{ConversationID: binding.ConversationID}, now: time.Now}
	transcript := "check the continuity marker"
	hash := hashValue(transcript)
	origin := schema.MessageOrigin{Kind: "external", InstallationID: "install", DiscussionID: "thread-binding", GuildID: "guild",
		ChannelID: "thread", RequestID: "dv_a", ActorID: "speaker", RequesterID: "speaker", Revision: hash,
		Voice: schema.VoiceOrigin{CallID: "call", TurnID: "turn", SpeakerID: "speaker", SupervisorSocket: scope.SupervisorSocket, BodyHash: hash, Generation: 1}}
	input := DiscussionInput{Origin: origin, Text: "Verified transcript: " + transcript, Answer: transcript}
	if err := d.Admit(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	if state, err := d.AdmissionState(origin); state != "accepted" || err != nil {
		t.Fatalf("accepted state = %q, %v", state, err)
	}
	conflict := input
	conflict.Origin.Voice.Generation++
	if err := d.Admit(t.Context(), conflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("generation replay = %v", err)
	}
	conflict = input
	conflict.Answer = "different body"
	if err := d.Admit(t.Context(), conflict); !errors.Is(err, ErrInvalid) {
		t.Fatalf("changed body = %v", err)
	}
	callLive = false
	if err := c.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if state, err := d.AdmissionState(origin); state != "cancelled" || err != nil || len(runtime.sent) != 0 {
		t.Fatalf("invalidated turn = %q, %v, writes=%d", state, err, len(runtime.sent))
	}
	callLive = true
	input.Origin.RequestID, input.Origin.Voice.TurnID = "dv_b", "new-turn"
	if err := d.Admit(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	if err := c.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	callLive = false
	if err := d.Admit(t.Context(), input); err != nil {
		t.Fatalf("cutover replay = %v", err)
	}
	if state, err := d.AdmissionState(input.Origin); state != "cutover" || err != nil || len(runtime.sent) != 1 {
		t.Fatalf("cutover = %q, %v, writes=%d", state, err, len(runtime.sent))
	}
	if runtime.sent[0].Origin == nil || *runtime.sent[0].Origin != input.Origin || runtime.sent[0].Origin.MessageID != "" {
		t.Fatalf("native origin = %#v", runtime.sent[0].Origin)
	}
}

func TestCallDiscussionPublicationRequiresCanonicalReceipt(t *testing.T) {
	_, store, _, b := newControllerFixture(t)
	source := DiscussionOutputSource{AdmissionID: "dv_first"}
	const body = "Verified marker 347."
	output := DiscussionOutput{DiscussionOutputSource: source, ID: "pub", Text: body, Sequence: 1}
	if err := store.update(func(st *State) error {
		a, err := allocateAdmission(st, b, "discussion:"+source.AdmissionID)
		if err != nil {
			return err
		}
		a.SourceCutover = true
		a.Origin = &schema.MessageOrigin{RequestID: source.AdmissionID, Voice: schema.VoiceOrigin{Generation: 1}}
		putRecord(st, "admissions", &st.Admissions, admissionKey(b.ConversationID, a.Sequence), a)
		p := Publication{ID: output.ID, Text: body, Exported: true, DiscussionSource: source, OutputSequence: output.Sequence}
		putRecord(st, "publications", &st.Publications, key(b.ConversationID, p.ID), p)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	d := &DiscussionController{store: store, cfg: Config{ConversationID: b.ConversationID}}
	if origin, err := d.PublicationCallAdmission(t.Context(), output, hashValue(body)); origin != source.AdmissionID || err != nil {
		t.Fatalf("canonical publication = %q, %v", origin, err)
	}
	if _, err := d.PublicationCallAdmission(t.Context(), output, hashValue("fake result")); !errors.Is(err, ErrStale) {
		t.Fatalf("changed body passed publication validation: %v", err)
	}
	output.AttemptID = "unexpected-attempt"
	if _, err := d.PublicationCallAdmission(t.Context(), output, hashValue(body)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("changed attempt passed publication validation: %v", err)
	}
}
