package voice

import (
	"encoding/json"
	"errors"
	"fmt"
	bolt "go.etcd.io/bbolt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReviewStoredActionCanReplayExactPayload(t *testing.T) {
	s, b, _ := testStore(t)
	a := mustAdmit(t, s, b, "human")
	if _, err := s.PrepareAction(b, Action{ID: "action", Admission: a.Sequence, NativeInputID: "input-human", Target: "worker", OperationID: "operation", Request: json.RawMessage(`{"text": "exact <text>"}`)}); err != nil {
		t.Fatal(err)
	}
	st, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	saved := st.Actions[key(b.ConversationID, "action")]
	if saved.RequestHash != hashBytes(saved.Request) {
		t.Errorf("stored request bytes no longer match their durable hash: %s", saved.Request)
	}
	if _, err = s.PrepareAction(b, saved); err != nil {
		t.Fatalf("retry from durable exact request failed: %v", err)
	}
}
func TestReviewFormattedReceiptAndAnswerRepeat(t *testing.T) {
	s, b, _ := testStore(t)
	a := mustAdmit(t, s, b, "human")
	if _, err := s.PrepareAction(b, Action{ID: "action", Admission: a.Sequence, NativeInputID: "input-human", Target: "worker", OperationID: "operation", Request: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	receipt := json.RawMessage(`{"accepted": true}`)
	if err := s.SettleAction(b, "action", receipt); err != nil {
		t.Fatal(err)
	}
	if err := s.SettleAction(b, "action", receipt); err != nil {
		t.Errorf("exact receipt replay failed: %v", err)
	}
	r, err := s.PutRequest(b, Request{ID: "question", Kind: "manager", Payload: json.RawMessage(`{}`), ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	answer := json.RawMessage(`{"answer": "yes"}`)
	if _, err = s.AnswerRequest(b, r.ID, r.Hash, "answer", answer, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AnswerRequest(b, r.ID, r.Hash, "answer", answer, time.Now()); err != nil {
		t.Errorf("exact answer replay failed: %v", err)
	}
}
func TestReviewLateNativeRequestCannotOutliveEndedTurn(t *testing.T) {
	s, b, _ := testStore(t)
	if err := s.EndNativeTurn(b, "ended-turn"); err != nil {
		t.Fatal(err)
	}
	r, err := s.PutRequest(b, Request{ID: "late-native", Kind: "native", NativeInputID: "input", NativeTurnID: "ended-turn", Payload: json.RawMessage(`{}`), ExpiresAt: time.Now().Add(time.Hour)})
	if err == nil {
		t.Fatalf("native request admitted after owning turn ended: %+v", r)
	}
}
func TestReviewLateRequestCannotReopenCancelledTask(t *testing.T) {
	s, b, _ := testStore(t)
	if err := s.CancelTaskRequests(b, "cancelled-task"); err != nil {
		t.Fatal(err)
	}
	r, err := s.PutRequest(b, Request{ID: "late-manager", Kind: "manager", TaskID: "cancelled-task", Payload: json.RawMessage(`{}`), ExpiresAt: time.Now().Add(time.Hour)})
	if err == nil {
		t.Fatalf("question admitted after owning task cancelled: %+v", r)
	}
}

func TestReviewVoiceStreamBoundsSerializedControlFrame(t *testing.T) {
	stream, err := NewVoiceStream()
	if err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`"` + strings.Repeat("a", (128<<10)-2) + `"`)
	event, err := stream.Append("status", payload)
	if err != nil {
		return
	}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > 128<<10 {
		t.Fatalf("accepted control frame is %d bytes, larger than 128 KiB", len(raw))
	}
}

func TestReviewGenerationIncrementCannotCorruptFullStore(t *testing.T) {
	s, b, dir := testStore(t)
	err := s.db.Update(func(tx *bolt.Tx) error {
		st, err := readState(tx)
		if err != nil {
			return err
		}
		st.Generation = 9
		for i := 0; i < 1100; i++ {
			id := fmt.Sprintf("u%04d", i)
			st.Utterances[key(b.ConversationID, id)] = Utterance{ID: id, ConversationID: b.ConversationID, DeviceID: "device", VoiceSessionID: "voice", MediaEpoch: 1, Phase: "committed", Text: "x", TextHash: hashBytes([]byte("x"))}
		}
		raw, err := json.Marshal(st)
		if err != nil {
			return err
		}
		left := MaxStoreBytes - len(raw)
		for k, u := range st.Utterances {
			n := min(left, MaxUtteranceBytes-1)
			u.Text += strings.Repeat("x", n)
			u.TextHash = hashBytes([]byte(u.Text))
			st.Utterances[k] = u
			left -= n
		}
		if left != 0 {
			t.Fatalf("fixture capacity shortfall: %d", left)
		}
		raw, err = json.Marshal(st)
		if err != nil {
			return err
		}
		if len(raw) != MaxStoreBytes {
			t.Fatalf("fixture size: %d", len(raw))
		}
		return writeStateRecord(tx, raw)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(dir)
	if err != nil {
		if !errors.Is(err, ErrCapacity) {
			t.Fatal(err)
		}
		db, openErr := bolt.Open(filepath.Join(dir, "voice.db"), 0600, &bolt.Options{ReadOnly: true})
		if openErr != nil {
			t.Fatal(openErr)
		}
		defer func() { _ = db.Close() }()
		if viewErr := db.View(func(tx *bolt.Tx) error {
			st, e := readState(tx)
			if e == nil && st.Generation != 9 {
				t.Fatalf("failed startup changed generation: %d", st.Generation)
			}
			return e
		}); viewErr != nil {
			t.Fatal(viewErr)
		}
		return
	}
	defer func() { _ = reopened.Close() }()
	if _, err = reopened.Snapshot(); err != nil {
		t.Fatalf("OpenStore successfully wrote an unreadable next generation: %v", err)
	}
}

func TestReviewCanonicalJSONPreservesNumberSpellingAndWakeReplay(t *testing.T) {
	s, b, _ := testStore(t)
	raw := json.RawMessage(`{ "integer":9007199254740993, "decimal":1.000, "exponent":1e+99, "text":"<>&" }`)
	e := Event{ID: "event", Cursor: JournalCursor{Binding: "source", Sequence: 1}, Metadata: raw, WakeID: "wake"}
	w, err := s.AcceptEvent(b, e, Wake{ID: "wake", OperationID: "op", Request: raw})
	if err != nil {
		t.Fatal(err)
	}
	expected := `{"integer":9007199254740993,"decimal":1.000,"exponent":1e+99,"text":"\u003c\u003e\u0026"}`
	if string(w.Request) != expected || w.RequestHash != hashBytes(w.Request) {
		t.Fatalf("changed numeric spelling or hash: %s", w.Request)
	}
	snap, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	savedEvent := snap.Events[key(b.ConversationID, "event")]
	savedWake := snap.Wakes[key(b.ConversationID, "wake")]
	if _, err := s.AcceptEvent(b, savedEvent, savedWake); err != nil {
		t.Fatalf("saved wake cannot reconcile: %v", err)
	}
	if _, err := s.AcceptEvent(b, e, Wake{ID: "wake", OperationID: "op", Request: raw}); err != nil {
		t.Fatalf("original formatted wake cannot reconcile: %v", err)
	}
}

func TestReviewLifetimeFencesSurviveRestartAndPreserveExactRequestReplay(t *testing.T) {
	s, b, dir := testStore(t)
	native := Request{ID: "native", Kind: "native", NativeInputID: "input", NativeTurnID: "turn", Payload: json.RawMessage(`{"prompt": "<question>"}`), ExpiresAt: time.Now().Add(time.Hour)}
	manager := Request{ID: "ask", Kind: "manager", TaskID: "task", Payload: json.RawMessage(`{ "question":"continue?" }`), ExpiresAt: time.Now().Add(time.Hour)}
	if _, err := s.PutRequest(b, native); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutRequest(b, manager); err != nil {
		t.Fatal(err)
	}
	if err := s.EndNativeTurn(b, "turn"); err != nil {
		t.Fatal(err)
	}
	if err := s.CancelTaskRequests(b, "task"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	fresh, err := reopened.BindManager(b)
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []Request{native, manager} {
		replay, err := reopened.PutRequest(fresh, request)
		if err != nil || replay.State != "expired" {
			t.Fatalf("exact request status lost: %+v %v", replay, err)
		}
		request.ID += "-late"
		if _, err := reopened.PutRequest(fresh, request); !errors.Is(err, ErrExpired) {
			t.Fatalf("late request resurrected: %v", err)
		}
	}
}

func TestReviewStreamUsesEscapedEnvelopeBytesAndRejectedAppendDoesNotAdvance(t *testing.T) {
	stream, err := NewVoiceStream()
	if err != nil {
		t.Fatal(err)
	}
	oversized := json.RawMessage(`"` + strings.Repeat("a", MaxControlFrameBytes-2) + `"`)
	if _, err := stream.Append("status", oversized); !errors.Is(err, ErrInvalid) {
		t.Fatal("oversized envelope accepted", err)
	}
	first, err := stream.Append("status", json.RawMessage(`{}`))
	if err != nil || first.Cursor.Sequence != 1 {
		t.Fatal("rejected append consumed sequence", err)
	}
	escaped := json.RawMessage(`"` + strings.Repeat("<", 15000) + `"`)
	for i := 0; i < 12; i++ {
		if _, err := stream.Append("status", escaped); err != nil {
			t.Fatal(err)
		}
	}
	total := 0
	for _, entry := range stream.events {
		raw, err := json.Marshal(entry.event)
		if err != nil {
			t.Fatal(err)
		}
		total += len(raw)
		if entry.bytes != len(raw) {
			t.Fatal("cached stream size differs from encoded bytes")
		}
	}
	if total > MaxStreamBytes || stream.bytes != total {
		t.Fatalf("encoded replay exceeded budget: %d cached=%d", total, stream.bytes)
	}
	if _, gap, _ := stream.Replay(first.Cursor); !gap {
		t.Fatal("encoded-byte eviction hid replay gap")
	}
}

func TestReviewTerminalLifetimeFencesShareTheDurableRecordBound(t *testing.T) {
	s, b, _ := testStore(t)
	if err := s.update(func(st *State) error {
		for len(st.CancelledTasks) < MaxRecords-1 {
			putRecord(st, "cancelled_tasks", &st.CancelledTasks, key(b.ConversationID, fmt.Sprint(len(st.CancelledTasks))), true)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.EndNativeTurn(b, "unrecorded-end"); !errors.Is(err, ErrCapacity) {
		t.Fatalf("terminal fence bypassed capacity: %v", err)
	}
	if err := s.CancelTaskRequests(b, "unrecorded-cancel"); !errors.Is(err, ErrCapacity) {
		t.Fatalf("task fence bypassed capacity: %v", err)
	}
	snap, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.ClosedTurns) != 0 || len(snap.Permits) != 0 || snap.CancelledTasks[key(b.ConversationID, "unrecorded-cancel")] {
		t.Fatal("failed fence transaction partially changed authority")
	}
}
