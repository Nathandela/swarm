package voice

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/claudeinput"
	"github.com/Nathandela/swarm/internal/protocol"
)

func TestWorkerNoteDispatchFitsCompleteNativeEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name, provider, kind, note, instructions string
		oldPrepared, recovery, handoff           bool
	}{
		{"observed-size", "claude", "needs-input", strings.Repeat("x", 3210), strings.Repeat("i", 216), false, false, false},
		{"retained-prepared", "claude", "needs-input", strings.Repeat("x", 3210), strings.Repeat("i", 216), true, false, false},
		{"retained-marker", "claude", "needs-input", "Which repository?", claudeinput.ManagerEnvelopePrefix, true, false, false},
		{"claude-recovery", "claude", "needs-input", claudeinput.ManagerEnvelopePrefix + strings.Repeat("界", 1350), claudeinput.ManagerEnvelopePrefix + strings.Repeat("i", maxManagerStartingPrompt-len(claudeinput.ManagerEnvelopePrefix)), false, true, true},
		{"codex-recovery", "codex", "complete", strings.Repeat("<", 4096), strings.Repeat("i", maxManagerStartingPrompt), false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, store, runtime, b := dispatchTextFixture(t, tc.provider)
			c.managerSettingsDir = t.TempDir()
			c.discussion = &DiscussionScope{VMProjectAccess: true}
			if err := SaveManagerStartingPrompt(c.managerSettingsDir, tc.instructions); err != nil {
				t.Fatal(err)
			}
			worker := WorkerAssociation{Mailbox: true, VMChild: true, ConversationID: b.ConversationID,
				Session: "machine/worker", SessionInstance: "worker-instance", Parent: b.SessionID, JobID: "job", AttemptID: "attempt"}
			note := workerNoteMetadata{Session: worker.Session, Instance: worker.SessionInstance, Agent: "claude",
				NoteID: "worker-note", Kind: tc.kind, Text: tc.note, Sequence: 1}
			input := workerMessageInput{ID: note.NoteID, Kind: note.Kind, Text: note.Text}
			queued, err := c.queueWorkerNote(worker, input, note)
			if err != nil {
				t.Fatal(err)
			}
			st, err := store.Snapshot()
			if err != nil || len(st.Wakes) != 1 {
				t.Fatalf("expected a durable wake: %v", err)
			}
			var first Wake
			for _, wake := range st.Wakes {
				first = wake
			}
			a := st.Admissions[admissionKey(b.ConversationID, first.Admission)]
			if tc.recovery {
				if _, err = store.restrictControllerRecovery(b, a.Sequence); err != nil {
					t.Fatal(err)
				}
			}
			if tc.handoff {
				if err := store.update(func(st *State) error {
					conv := st.Conversations[b.ConversationID]
					conv.NeedsHandoff = true
					putRecord(st, "conversations", &st.Conversations, b.ConversationID, conv)
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			if tc.name == "observed-size" || tc.oldPrepared {
				queuedText, err := c.admissionText(st, a)
				if err != nil {
					t.Fatal(err)
				}
				legacy := "Current owner settings (supersede earlier settings)\n" + tc.instructions + "\n\nCurrent input\n" + queuedText
				if tc.name != "retained-marker" && (len(queuedText) > 4006 || len(legacy) <= 4006 || claudeinput.MaxManagerTextBytes != 4006) {
					t.Fatalf("fixture must isolate overflow after the 216-byte owner prompt: base=%d wrapped=%d", len(queuedText), len(legacy))
				}
				if tc.oldPrepared {
					if err := store.observeController(b, 0, false); err != nil {
						t.Fatal(err)
					}
					nonce := strings.Repeat("a", 64)
					req := protocol.ComposerSendReq{Session: b.SessionID, SessionInstance: b.SessionInstance, Text: legacy,
						ManagerAdmission: &protocol.ManagerAdmission{Binding: runtimeBinding(b), Nonce: nonce}}
					if _, err := store.prepareControllerInput(b, a.Sequence, nonce, first.OperationID, req, 0); err != nil {
						t.Fatal(err)
					}
				}
			}

			lostAck := errors.New("synthetic lost acknowledgement")
			runtime.failSend = lostAck
			if err := c.Tick(t.Context()); !errors.Is(err, lostAck) || len(runtime.sent) != 1 {
				t.Fatalf("expected one attempted send with lost acknowledgement: %d %v", len(runtime.sent), err)
			}
			req := runtime.sent[0]
			expectPreview := tc.name != "retained-marker"
			if len(req.Text) > managerDispatchLimit(tc.provider) || !utf8.ValidString(req.Text) ||
				strings.Contains(req.Text, "Preview only.") != expectPreview || strings.Contains(req.Text, "swarm_read_note") != expectPreview ||
				!strings.Contains(req.Text, escapeManagerEnvelopeMarker(tc.instructions)) ||
				!strings.Contains(req.Text, worker.SessionInstance) {
				t.Fatalf("invalid %s prompt with %d bytes: %.120q", tc.provider, len(req.Text), req.Text)
			}
			if strings.HasPrefix(req.Text, recoveryInputNotice) != tc.recovery || strings.Contains(req.Text, discussionHandoffGuidance) != tc.handoff {
				t.Fatalf("recovery or handoff context lost: %q", req.Text)
			}
			if tc.provider == "claude" {
				if _, err := claudeinput.ManagerEnvelope(req.ManagerAdmission.Nonce, req.Text); err != nil {
					t.Fatal(err)
				}
			}
			if (runtime.operations[0] != first.OperationID) != tc.oldPrepared {
				t.Fatal("operation identity must change only when rewriting the retained invalid preparation")
			}
			if err := SaveManagerStartingPrompt(c.managerSettingsDir, "Changed owner instructions."); err != nil {
				t.Fatal(err)
			}
			if again, err := c.queueWorkerNote(worker, input, note); err != nil || again != queued {
				t.Fatalf("retry changed the note receipt: %+v %v", again, err)
			}
			if err := c.Tick(t.Context()); err != nil || len(runtime.sent) != 1 {
				t.Fatalf("lost acknowledgement or settings edit resent the note: %d %v", len(runtime.sent), err)
			}
			exact, err := c.readDiscussionWorkerNote(t.Context(), SessionInput{Session: worker.Session, SessionInstance: worker.SessionInstance}, a)
			var stored workerNoteMetadata
			if err != nil || json.Unmarshal(exact, &stored) != nil || stored.Text != tc.note {
				t.Fatalf("manager could not read the durable original: %v", err)
			}
		})
	}
}

func dispatchTextFixture(t *testing.T, provider string) (*Controller, *Store, *controllerRuntimeFixture, ManagerBinding) {
	t.Helper()
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	binding, err := store.BindManager(ManagerBinding{ConversationID: "conversation", Provider: provider,
		SessionID: "machine/session", SessionInstance: "incarnation", NativeSessionID: "thread",
		NativeGeneration: "generation", ObservationGeneration: "observation", LoginEpoch: "login"})
	if err != nil {
		t.Fatal(err)
	}
	refs := ManagerBridgeRefs{Socket: "/tmp/dispatcher.sock", AuthFile: "/tmp/dispatcher.auth"}
	runtime := &controllerRuntimeFixture{status: protocol.ManagerRuntimeStatus{Binding: runtimeBinding(binding),
		BridgeSocket: refs.Socket, AuthFile: refs.AuthFile, ReadyForInput: true, Turn: protocol.ManagerNativeTurn{State: "idle"}},
		receipts: map[string]protocol.SemanticMessageReceipt{}}
	c, err := NewController(store, runtime, binding, refs)
	if err != nil {
		t.Fatal(err)
	}
	return c, store, runtime, binding
}

func TestWorkerNoteDispatchKeepsPriorFinalAndLateRecoveryGuidance(t *testing.T) {
	c, store, _, b := dispatchTextFixture(t, "claude")
	c.managerSettingsDir = t.TempDir()
	c.discussion = &DiscussionScope{VMProjectAccess: true}
	standing := strings.Repeat("a", maxManagerStartingPrompt)
	if err := SaveManagerStartingPrompt(c.managerSettingsDir, standing); err != nil {
		t.Fatal(err)
	}

	const current, previous = uint64(2), uint64(1)
	worker := WorkerAssociation{ConversationID: b.ConversationID, Session: "machine/worker",
		SessionInstance: "worker-instance", Project: "/project", JobID: "job", ActionID: "attempt", AttemptID: "attempt"}
	note := workerNoteMetadata{Session: worker.Session, Instance: worker.SessionInstance, Agent: "claude",
		NoteID: "worker-note", Kind: "complete", Text: strings.Repeat("x", 3450)}
	if err := store.update(func(st *State) error {
		putRecord(st, "workers", &st.Workers, key(b.ConversationID, key(worker.Session, worker.SessionInstance)), worker)
		prev, err := allocateAdmission(st, b, "wake:earlier")
		if err != nil {
			return err
		}
		prev.SettledAt = time.Now().UTC()
		putRecord(st, "admissions", &st.Admissions, admissionKey(b.ConversationID, prev.Sequence), prev)
		priorMeta := json.RawMessage(`{"session":"machine/worker","instance":"worker-instance","data":{"turn_id":"original-turn","state":"completed","text":"verified result"}}`)
		prior := Event{ID: discussionEvidenceID(worker), ConversationID: b.ConversationID, WakeID: "earlier",
			WorkerProof: true, NativeTurnID: "original-turn", Metadata: priorMeta, JobID: worker.JobID, AttemptID: worker.AttemptID, RecoveryOnly: true}
		putRecord(st, "events", &st.Events, key(b.ConversationID, prior.ID), prior)
		priorWake := Wake{ID: "earlier", ConversationID: b.ConversationID, EventID: prior.ID, Admission: previous, State: "settled"}
		putRecord(st, "wakes", &st.Wakes, key(b.ConversationID, priorWake.ID), priorWake)
		p := Publication{ID: "previous-final", ConversationID: b.ConversationID, Admission: previous, OutputSequence: 1,
			Final: true, DiscussionSource: DiscussionOutputSource{AdmissionID: priorWake.ID, JobID: worker.JobID, AttemptID: worker.AttemptID}}
		putRecord(st, "publications", &st.Publications, key(b.ConversationID, p.ID), p)
		conv := st.Conversations[b.ConversationID]
		conv.NeedsHandoff = true
		putRecord(st, "conversations", &st.Conversations, b.ConversationID, conv)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := c.queueWorkerNote(worker, workerMessageInput{ID: note.NoteID, Kind: note.Kind, Text: note.Text}, note); err != nil {
		t.Fatal(err)
	}

	st, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	a := st.Admissions[admissionKey(b.ConversationID, current)]
	if a.Sequence != current {
		t.Fatal("expected queued worker wake")
	}
	wake := st.Wakes[key(b.ConversationID, strings.TrimPrefix(a.SourceID, "wake:"))]
	nonce := strings.Repeat("a", 64)
	text, err := c.managerDispatchText(st, a, nonce)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "already scheduled") || !strings.Contains(text, discussionHandoffGuidance) ||
		!strings.Contains(text, standing) || !strings.Contains(text, "swarm_read_note") {
		t.Fatalf("wrapped preview did not retain all required instructions: %q", text)
	}
	if err := store.observeController(b, 0, false); err != nil {
		t.Fatal(err)
	}
	req := protocol.ComposerSendReq{Session: b.SessionID, SessionInstance: b.SessionInstance, Text: text,
		ManagerAdmission: &protocol.ManagerAdmission{Binding: runtimeBinding(b), Nonce: nonce}}
	if _, err := store.prepareControllerInput(b, current, nonce, wake.OperationID, req, 0); err != nil {
		t.Fatal(err)
	}
	restricted, err := store.restrictControllerRecovery(b, current)
	if err != nil {
		t.Fatal(err)
	}
	var prepared protocol.ComposerSendReq
	if json.Unmarshal(restricted.Request, &prepared) != nil || prepared.Text != recoveryInputNotice+text || restricted.OperationID != wake.OperationID {
		t.Fatal("late recovery lost its reserved room or original operation ID")
	}
	if _, err := claudeinput.ManagerEnvelope(nonce, prepared.Text); err != nil {
		t.Fatal(err)
	}
}
