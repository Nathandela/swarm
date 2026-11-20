package voice

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/managerproof"
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

func TestCallDiscussionWorkerPublicationUsesExactAttempt(t *testing.T) {
	_, store, _, b := newControllerFixture(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	scope := DiscussionScope{InstallationID: "install", DiscussionID: "bound", GuildID: "guild", ThreadID: "thread", SupervisorSocket: "/supervisor.sock"}
	hash := hashValue("check worker")
	const body = "Verified worker result"
	var outputs []DiscussionOutput
	var expected []string
	if err := store.update(func(st *State) error {
		for _, suffix := range []string{"first", "second"} {
			origin := schema.MessageOrigin{InstallationID: "install", DiscussionID: "bound", Kind: "external",
				GuildID: "guild", ChannelID: "thread", ActorID: suffix, RequesterID: suffix, RequestID: "dv_" + suffix, Revision: hash,
				Voice: schema.VoiceOrigin{CallID: "call", TurnID: suffix, SpeakerID: suffix, SupervisorSocket: scope.SupervisorSocket, BodyHash: hash, Generation: 1}}
			source := DiscussionOutputSource{AdmissionID: "wake-" + suffix, JobID: "job-" + suffix, AttemptID: "attempt-" + suffix}
			output := DiscussionOutput{DiscussionOutputSource: source, ID: "pub-" + suffix, Text: body, Sequence: uint64(len(outputs) + 1)}
			outputs, expected = append(outputs, output), append(expected, origin.RequestID)
			a, err := allocateAdmission(st, b, "discussion:"+origin.RequestID)
			if err != nil {
				return err
			}
			a.SourceCutover, a.Origin = true, &origin
			putRecord(st, "admissions", &st.Admissions, admissionKey(b.ConversationID, a.Sequence), a)
			event := Event{ID: "evidence-" + suffix, JobID: source.JobID, AttemptID: source.AttemptID}
			putRecord(st, "events", &st.Events, key(b.ConversationID, event.ID), event)
			wake := Wake{ID: source.AdmissionID, EventID: event.ID}
			putRecord(st, "wakes", &st.Wakes, key(b.ConversationID, wake.ID), wake)
			request, _ := json.Marshal(controllerActionRequest{Tool: "swarm_resume", Origin: &origin, Project: root})
			receipt, _ := json.Marshal(createdWorkerResult{State: "resumed", Session: "fixture/worker-" + suffix, SessionInstance: "worker-instance-" + suffix})
			attempt := Action{ID: source.AttemptID, ConversationID: b.ConversationID, Admission: a.Sequence, State: "settled", Request: request, Receipt: receipt}
			putRecord(st, "actions", &st.Actions, key(b.ConversationID, attempt.ID), attempt)
			worker := WorkerAssociation{ConversationID: b.ConversationID, JobID: source.JobID, AttemptID: source.AttemptID,
				ActionID: attempt.ID, SourceID: a.SourceID, InitialSourceID: "discussion:earlier-text-request"}
			putRecord(st, "workers", &st.Workers, key(b.ConversationID, "worker-"+suffix), worker)
			p := Publication{ID: output.ID, Text: body, Exported: true, DiscussionSource: source, OutputSequence: output.Sequence}
			putRecord(st, "publications", &st.Publications, key(b.ConversationID, p.ID), p)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	d := &DiscussionController{store: store, scope: scope, cfg: Config{ConversationID: b.ConversationID}}
	for i := len(outputs) - 1; i >= 0; i-- {
		if id, err := d.PublicationCallAdmission(t.Context(), outputs[i], hashValue(body)); id != expected[i] || err != nil {
			t.Fatalf("result %d resolved to %q, %v, want %q", i, id, err, expected[i])
		}
	}
	if err := store.update(func(st *State) error {
		workerID := key(b.ConversationID, "worker-first")
		worker := st.Workers[workerID]
		worker.ActionID, worker.SourceID = "later-steer", "discussion:newer-text-request"
		putRecord(st, "workers", &st.Workers, workerID, worker)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.PublicationCallAdmission(t.Context(), outputs[0], hashValue(body)); !errors.Is(err, ErrStale) {
		t.Fatalf("result after conflicting steer was eligible for speech: %v", err)
	}
}

func TestCallDiscussionDraftRefusalRequiresFreshGrant(t *testing.T) {
	for _, lost := range []bool{false, true} {
		name := "definitive refusal"
		if lost {
			name = "lost acknowledgement"
		}
		t.Run(name, func(t *testing.T) {
			c, store, fixture, b := newControllerFixture(t)
			runtime := &lateDraftRuntime{controllerRuntimeFixture: fixture, loseReply: lost, hideReceipt: lost}
			c.runtime = runtime
			live := true
			scope := DiscussionScope{VMProjectAccess: true, InstallationID: "install", DiscussionID: "bound", GuildID: "guild", ThreadID: "thread",
				SupervisorSocket: "/historical/supervisor.sock",
				VerifyOrigin: func(context.Context, schema.MessageOrigin) error {
					if !live {
						return ErrDiscussionSourceChanged
					}
					return nil
				},
				VerifyGrant: func(context.Context, schema.MessageOrigin) error { return nil },
			}
			c.discussion = &scope
			d := &DiscussionController{scope: scope, controller: c, store: store, cfg: Config{ConversationID: b.ConversationID}, now: time.Now}
			body := "check the marker"
			digest := hashValue(body)
			origin := schema.MessageOrigin{Kind: "external", InstallationID: scope.InstallationID, DiscussionID: scope.DiscussionID, GuildID: scope.GuildID,
				ChannelID: scope.ThreadID, RequestID: "dv_request", ActorID: "speaker", RequesterID: "speaker", Revision: digest,
				Voice: schema.VoiceOrigin{CallID: "call", TurnID: "turn", SpeakerID: "speaker", SupervisorSocket: scope.SupervisorSocket, BodyHash: digest, Generation: 1}}
			if err := d.Admit(t.Context(), DiscussionInput{Origin: origin, Text: body, Answer: body}); err != nil {
				t.Fatal(err)
			}
			err := c.Tick(t.Context())
			live = false
			if lost {
				if !errors.Is(err, ErrControllerUncertain) {
					t.Fatalf("lost ack bypassed uncertainty: %v", err)
				}
				st, _ := store.Snapshot()
				a, _ := findDiscussionAdmission(st, origin)
				if a.Phase != "uncertain" || !a.SourceCutover || a.OperationID != runtime.held.OperationID {
					t.Fatalf("unproven refusal reset sealed operation: %+v", a)
				}
				runtime.hideReceipt = false
				err = c.Tick(t.Context())
			}
			if !errors.Is(err, managerproof.ErrDirectInputUnresolved) {
				t.Fatalf("draft refusal = %v", err)
			}
			st, _ := store.Snapshot()
			a, _ := findDiscussionAdmission(st, origin)
			if a.Phase != "prepared" || a.SourceCutover || a.OperationID == runtime.held.OperationID {
				t.Fatalf("proven refusal retained stale cutover: %+v", a)
			}
			runtime.draft = false
			if err := c.Tick(t.Context()); err != nil {
				t.Fatal(err)
			}
			if state, err := d.AdmissionState(origin); state != "cancelled" || err != nil || len(fixture.sent) != 0 {
				t.Fatalf("ended call retried provider input: %q, %v, sends %d", state, err, len(fixture.sent))
			}
		})
	}
}
