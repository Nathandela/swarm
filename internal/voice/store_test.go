package voice

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func testStore(t *testing.T) (*Store, ManagerBinding, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	b, err := s.BindManager(ManagerBinding{ConversationID: "conversation", Provider: "codex", SessionID: "machine/session", SessionInstance: "incarnation", NativeSessionID: "thread", NativeGeneration: "native-epoch", LoginEpoch: "login-1"})
	if err != nil {
		t.Fatal(err)
	}
	return s, b, dir
}
func mustBegin(t *testing.T, s *Store, b ManagerBinding, id string) Utterance {
	t.Helper()
	u, err := s.BeginUtterance(b, Utterance{ID: id, DeviceID: "device", VoiceSessionID: "voice", MediaEpoch: 1})
	if err != nil {
		t.Fatal(err)
	}
	return u
}
func mustAdmit(t *testing.T, s *Store, b ManagerBinding, id string) Admission {
	t.Helper()
	mustBegin(t, s, b, id)
	if _, err := s.FinalizeUtterance(b, id, "exact text"); err != nil {
		t.Fatal(err)
	}
	a, err := s.AdmitUtterance(b, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.BindNativeInput(b, a.Sequence, "input-"+id); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestStoreUtteranceBindingAndAdmissionSurviveRestart(t *testing.T) {
	s, b, dir := testStore(t)
	u := mustBegin(t, s, b, "utterance")
	if u.Phase != "capturing" {
		t.Fatal(u)
	}
	if _, err := s.BeginUtterance(b, Utterance{ID: u.ID, DeviceID: "other", VoiceSessionID: "voice", MediaEpoch: 1}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err := s.AdmitUtterance(b, u.ID); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := s.FinalizeUtterance(b, u.ID, "hello"); err != nil {
		t.Fatal(err)
	}
	a, err := s.AdmitUtterance(b, u.ID)
	if err != nil || a.Sequence != 1 {
		t.Fatalf("%+v %v", a, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	if _, err := reopened.BeginUtterance(b, Utterance{ID: "stale", DeviceID: "d", VoiceSessionID: "v", MediaEpoch: 1}); !errors.Is(err, ErrStale) {
		t.Fatal(err)
	}
	fresh, err := reopened.BindManager(b)
	if err != nil {
		t.Fatal(err)
	}
	again, err := reopened.AdmitUtterance(fresh, u.ID)
	if err != nil || again.Sequence != a.Sequence {
		t.Fatalf("%+v %v", again, err)
	}
	if _, err := reopened.FinalizeUtterance(fresh, u.ID, "different"); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	b.NativeSessionID = "other"
	if _, err := reopened.BindManager(b); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}

func TestStoreActionsAreExactAndRetirementCannotCrossAnUnsettledHole(t *testing.T) {
	s, b, _ := testStore(t)
	a1 := mustAdmit(t, s, b, "first")
	a2 := mustAdmit(t, s, b, "second")
	action := Action{ID: "create", Admission: a1.Sequence, NativeInputID: "input-first", Target: "machine/worker@instance", OperationID: "operation", Request: json.RawMessage(`{"text":"exact"}`)}
	p, err := s.PrepareAction(b, action)
	if err != nil || p.State != "prepared" || p.RequestHash == "" {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err := s.PrepareAction(b, action); err != nil {
		t.Fatal(err)
	}
	action.Request = json.RawMessage(`{"text":"changed"}`)
	if _, err := s.PrepareAction(b, action); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if err := s.SettleAdmission(b, a1.Sequence); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if err := s.SettleAdmission(b, a2.Sequence); err != nil {
		t.Fatal(err)
	}
	snap, _ := s.Snapshot()
	if snap.Conversations[b.ConversationID].RetiredThrough != 0 {
		t.Fatal("retired across hole")
	}
	if err := s.SettleAction(b, "create", json.RawMessage(`{"accepted":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.SettleAdmission(b, a1.Sequence); err != nil {
		t.Fatal(err)
	}
	snap, _ = s.Snapshot()
	if snap.Conversations[b.ConversationID].RetiredThrough != 2 {
		t.Fatal("floor did not advance")
	}
	action.Request = json.RawMessage(`{"text":"new"}`)
	action.ID = "late"
	if _, err := s.PrepareAction(b, action); !errors.Is(err, ErrExpired) {
		t.Fatal(err)
	}
}

func TestStoreRequestLifetimesAndProducerSpeechRevocation(t *testing.T) {
	s, b, _ := testStore(t)
	a := mustAdmit(t, s, b, "human")
	for _, kind := range []string{"manager", "native"} {
		_, err := s.PutRequest(b, Request{ID: kind, Kind: kind, NativeInputID: "input-human", NativeTurnID: "turn", TaskID: "task", Payload: json.RawMessage(`{"question":"continue?"}`), ExpiresAt: time.Now().Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
	}
	permit, err := s.GrantSpeech(b, a.Sequence, "input-human", "turn", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeSpeech(b, "turn"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GrantSpeech(b, a.Sequence, "input-human", "turn", 2); !errors.Is(err, ErrExpired) {
		t.Fatal(err)
	}
	pub, err := s.PublishSpeech(b, permit, Publication{ID: "phrase", NativeTurnID: "turn", Sequence: 1, Text: "still visible", Final: true})
	if err != nil || pub.Speak {
		t.Fatalf("%+v %v", pub, err)
	}
	if err := s.EndNativeTurn(b, "turn"); err != nil {
		t.Fatal(err)
	}
	snap, _ := s.Snapshot()
	ask := snap.Requests[key(b.ConversationID, "manager")]
	native := snap.Requests[key(b.ConversationID, "native")]
	if ask.State != "pending" || native.State != "expired" {
		t.Fatalf("manager=%+v native=%+v", ask, native)
	}
	if _, err := s.AnswerRequest(b, "manager", ask.Hash, "answer", json.RawMessage(`"yes"`), time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AnswerRequest(b, "native", native.Hash, "answer", json.RawMessage(`"yes"`), time.Now()); !errors.Is(err, ErrExpired) {
		t.Fatal(err)
	}
}

func TestStoreEventCursorInboxAndWakeAreOneTransaction(t *testing.T) {
	s, b, dir := testStore(t)
	event := Event{ID: "event", Cursor: JournalCursor{Binding: "journal-generation", Sequence: 1}, Metadata: json.RawMessage(`{"task":"finished"}`), WakeID: "wake"}
	wake := Wake{ID: "wake", OperationID: "wake-operation", Request: json.RawMessage(`{"text":"worker finished"}`)}
	s.beforeCommit = func() error { return syscall.ENOSPC }
	if _, err := s.AcceptEvent(b, event, wake); !errors.Is(err, syscall.ENOSPC) {
		t.Fatal(err)
	}
	s.beforeCommit = nil
	snap, _ := s.Snapshot()
	if len(snap.Events) != 0 || len(snap.Wakes) != 0 || len(snap.Journal) != 0 {
		t.Fatal("partial accepted event")
	}
	w, err := s.AcceptEvent(b, event, wake)
	if err != nil || w.State != "prepared" {
		t.Fatalf("%+v %v", w, err)
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
	same, err := reopened.AcceptEvent(fresh, event, wake)
	if err != nil || same.OperationID != w.OperationID {
		t.Fatalf("%+v %v", same, err)
	}
	admitted, err := reopened.AdvanceWake(fresh, "wake", "admitted", "")
	if err != nil || admitted.Admission != 1 {
		t.Fatalf("%+v %v", admitted, err)
	}
	if _, err := reopened.AdvanceWake(fresh, "wake", "native_input", "native-wake"); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.AdvanceWake(fresh, "wake", "settled", "native-wake"); err != nil {
		t.Fatal(err)
	}
	event.ID = "foreign"
	event.Cursor = JournalCursor{Binding: "other-journal", Sequence: 2}
	if _, err := reopened.AcceptEvent(fresh, event, wake); !errors.Is(err, ErrStale) {
		t.Fatal(err)
	}
}

func TestStoreRefusesCorruptionFutureSchemaAndUnsafeFiles(t *testing.T) {
	for _, raw := range []string{`{"version":999}`, `not-json`} {
		t.Run(raw, func(t *testing.T) {
			s, _, dir := testStore(t)
			if err := s.db.Update(func(tx *bolt.Tx) error { return tx.Bucket([]byte("controller")).Put([]byte("state"), []byte(raw)) }); err != nil {
				t.Fatal(err)
			}
			_ = s.Close()
			if broken, err := OpenStore(dir); !errors.Is(err, ErrCorrupt) {
				if broken != nil {
					_ = broken.Close()
				}
				t.Fatal(err)
			}
		})
	}
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "voice.db")); err != nil {
		t.Fatal(err)
	}
	if s, err := OpenStore(dir); err == nil {
		_ = s.Close()
		t.Fatal("opened symlink")
	}
}

func TestStorePressureRollsBackWithoutLosingUnresolvedGuard(t *testing.T) {
	s, b, _ := testStore(t)
	a := mustAdmit(t, s, b, "first")
	if _, err := s.PrepareAction(b, Action{ID: "unresolved", Admission: a.Sequence, NativeInputID: "input-first", Target: "worker", OperationID: "op", Request: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Update(func(tx *bolt.Tx) error {
		st, err := readState(tx)
		if err != nil {
			return err
		}
		for countRecords(st) < MaxRecords {
			id := key("pressure", string(rune(len(st.Utterances)+1)))
			st.Utterances[id] = Utterance{ID: id, Phase: "failed"}
		}
		raw, err := json.Marshal(st)
		if err != nil {
			return err
		}
		return writeStateRecord(tx, raw)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginUtterance(b, Utterance{ID: "overflow", DeviceID: "d", VoiceSessionID: "v", MediaEpoch: 1}); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
	snap, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Actions) != 1 || snap.Actions[key(b.ConversationID, "unresolved")].State != "prepared" {
		t.Fatal("lost unresolved guard")
	}
}

func TestStoreCompactionKeepsRetiredInputsDeadAndUnknownInputsCannotMutate(t *testing.T) {
	s, b, _ := testStore(t)
	a := mustAdmit(t, s, b, "old")
	action := Action{ID: "effect", Admission: a.Sequence, NativeInputID: "input-old", Target: "worker", OperationID: "op", Request: json.RawMessage(`{}`)}
	if _, err := s.PrepareAction(b, action); err != nil {
		t.Fatal(err)
	}
	if err := s.SettleAction(b, "effect", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.SettleAdmission(b, a.Sequence); err != nil {
		t.Fatal(err)
	}
	if err := s.Compact(b, time.Now().Add(8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	snap, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Actions) != 0 || len(snap.Admissions) != 0 || snap.Conversations[b.ConversationID].RetiredThrough != 1 {
		t.Fatalf("bad compaction: %+v", snap)
	}
	if _, err := s.AdmitUtterance(b, "old"); !errors.Is(err, ErrExpired) {
		t.Fatal(err)
	}
	if _, err := s.PrepareAction(b, action); !errors.Is(err, ErrExpired) {
		t.Fatal(err)
	}
	action.Admission = 2
	if _, err := s.PrepareAction(b, action); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	next := mustAdmit(t, s, b, "new")
	if next.Sequence != 2 {
		t.Fatal("counter reset")
	}
}

func TestStoreReturnedBytesAreDetachedAndDiskFailureCannotAdmit(t *testing.T) {
	s, b, _ := testStore(t)
	a := mustAdmit(t, s, b, "human")
	raw := json.RawMessage(`{"text":"unchanged"}`)
	action, err := s.PrepareAction(b, Action{ID: "effect", Admission: a.Sequence, NativeInputID: "input-human", Target: "worker", OperationID: "op", Request: raw})
	if err != nil {
		t.Fatal(err)
	}
	raw[0] = 'X'
	action.Request[1] = 'Y'
	snap, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	snap.Actions[key(b.ConversationID, "effect")] = Action{}
	stored, err := s.Snapshot()
	if err != nil || !json.Valid(stored.Actions[key(b.ConversationID, "effect")].Request) {
		t.Fatal("caller changed durable state", err)
	}
	mustBegin(t, s, b, "not-admitted")
	if _, err := s.FinalizeUtterance(b, "not-admitted", "exact"); err != nil {
		t.Fatal(err)
	}
	s.beforeCommit = func() error { return syscall.ENOSPC }
	if _, err := s.AdmitUtterance(b, "not-admitted"); !errors.Is(err, syscall.ENOSPC) {
		t.Fatal(err)
	}
	s.beforeCommit = nil
	snap, _ = s.Snapshot()
	if snap.Conversations[b.ConversationID].LastAdmission != 1 || snap.Utterances[key(b.ConversationID, "not-admitted")].Admission != 0 {
		t.Fatal("failed disk transaction admitted effect")
	}
}

func TestStoreLogicalByteBoundRollsBack(t *testing.T) {
	s, b, _ := testStore(t)
	err := s.update(func(st *State) error {
		putRecord(st, "utterances", &st.Utterances, "large", Utterance{ID: "large", Text: strings.Repeat("a", MaxStoreBytes)})
		return nil
	})
	if !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
	snap, err := s.Snapshot()
	if err != nil || len(snap.Utterances) != 0 {
		t.Fatal("byte limit changed durable authority", err)
	}
	if _, err := s.BeginUtterance(b, Utterance{ID: "still-works", DeviceID: "d", VoiceSessionID: "v", MediaEpoch: 1}); err != nil {
		t.Fatal(err)
	}
}

func TestStoreOperationIDsCannotAliasWakeAndAction(t *testing.T) {
	s, b, _ := testStore(t)
	a := mustAdmit(t, s, b, "human")
	if _, err := s.PrepareAction(b, Action{ID: "effect", Admission: a.Sequence, NativeInputID: "input-human", Target: "worker", OperationID: "op", Request: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptEvent(b, Event{ID: "event", Cursor: JournalCursor{Binding: "journal", Sequence: 1}, Metadata: json.RawMessage(`{}`), WakeID: "wake"}, Wake{ID: "wake", OperationID: "op", Request: json.RawMessage(`{}`)}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}

func TestStoreNeverResetsExistingEmptyOrPartiallyLostNamespace(t *testing.T) {
	for _, erase := range []string{"bucket", "counter"} {
		t.Run(erase, func(t *testing.T) {
			s, _, dir := testStore(t)
			if err := s.db.Update(func(tx *bolt.Tx) error {
				if erase == "bucket" {
					return tx.DeleteBucket([]byte("controller"))
				}
				raw := tx.Bucket([]byte("controller")).Get([]byte("state"))
				var fields map[string]any
				if err := json.Unmarshal(raw, &fields); err != nil {
					return err
				}
				delete(fields, "generation")
				edited, err := json.Marshal(fields)
				if err != nil {
					return err
				}
				return tx.Bucket([]byte("controller")).Put([]byte("state"), edited)
			}); err != nil {
				t.Fatal(err)
			}
			_ = s.Close()
			opened, err := OpenStore(dir)
			if !errors.Is(err, ErrCorrupt) {
				if opened != nil {
					_ = opened.Close()
				}
				t.Fatalf("lost namespace was reset: %v", err)
			}
		})
	}
}

func TestStoreUtteranceCannotBeRetargetedAcrossProviderSwitch(t *testing.T) {
	s, b, _ := testStore(t)
	mustAdmit(t, s, b, "original")
	other := b
	other.ConversationID = "claude-conversation"
	other.Provider = "claude"
	other.NativeSessionID = "claude-native"
	switched, err := s.BindManager(other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginUtterance(switched, Utterance{ID: "original", DeviceID: "device", VoiceSessionID: "voice", MediaEpoch: 1}); !errors.Is(err, ErrConflict) {
		t.Fatalf("retargeted immutable utterance: %v", err)
	}
}

func TestStoreMissingDatabaseNeverResetsAdmissionNamespace(t *testing.T) {
	s, b, dir := testStore(t)
	mustAdmit(t, s, b, "accepted")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "voice.db")); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(dir)
	if !errors.Is(err, ErrCorrupt) {
		if reopened != nil {
			_ = reopened.Close()
		}
		t.Fatalf("lost database reset accepted namespace: %v", err)
	}
}

func TestStorePhysicalGrowthUnderRepeatedExactReplay(t *testing.T) {
	s, b, dir := testStore(t)
	a := mustAdmit(t, s, b, "input")
	action := Action{ID: "effect", Admission: a.Sequence, NativeInputID: "input-input", Target: "worker", OperationID: "op", Request: json.RawMessage(`{"text":"same"}`)}
	var sizes []int64
	for i := 0; i <= 100; i++ {
		if _, err := s.PrepareAction(b, action); err != nil {
			t.Fatal(err)
		}
		if i == 0 || i == 10 || i == 100 {
			info, err := os.Stat(filepath.Join(dir, "voice.db"))
			if err != nil {
				t.Fatal(err)
			}
			sizes = append(sizes, info.Size())
		}
	}
	snap, err := s.Snapshot()
	if err != nil || len(snap.Actions) != 1 {
		t.Fatal("replay accumulated effects", err)
	}
	t.Logf("physical database bytes after 1/11/101 exact action retries: %v; logical state bytes are bounded separately", sizes)
}

func TestStoreRetiredActionReplayReturnsTheOriginalReceiptUntilCompaction(t *testing.T) {
	s, b, _ := testStore(t)
	a := mustAdmit(t, s, b, "first")
	action := Action{ID: "effect", Admission: a.Sequence, NativeInputID: "input-first", Target: "worker", OperationID: "op", Request: json.RawMessage(`{"text":"exact"}`)}
	if _, err := s.PrepareAction(b, action); err != nil {
		t.Fatal(err)
	}
	if err := s.SettleAction(b, "effect", json.RawMessage(`{"result":"accepted"}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.SettleAdmission(b, a.Sequence); err != nil {
		t.Fatal(err)
	}
	replay, err := s.PrepareAction(b, action)
	if err != nil || replay.State != "settled" || string(replay.Receipt) != `{"result":"accepted"}` {
		t.Fatalf("lost settled receipt: %+v %v", replay, err)
	}
	action.Request = json.RawMessage(`{"text":"changed"}`)
	if _, err := s.PrepareAction(b, action); !errors.Is(err, ErrConflict) {
		t.Fatalf("accepted changed payload: %v", err)
	}
}

func TestStoreRefusesSyntacticallyValidCounterCorruption(t *testing.T) {
	s, b, dir := testStore(t)
	mustAdmit(t, s, b, "accepted")
	if err := s.db.Update(func(tx *bolt.Tx) error {
		st, err := readState(tx)
		if err != nil {
			return err
		}
		c := st.Conversations[b.ConversationID]
		c.LastAdmission = 0
		st.Conversations[b.ConversationID] = c
		raw, err := json.Marshal(st)
		if err != nil {
			return err
		}
		return tx.Bucket([]byte("controller")).Put([]byte("state"), raw)
	}); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	reopened, err := OpenStore(dir)
	if !errors.Is(err, ErrCorrupt) {
		if reopened != nil {
			_ = reopened.Close()
		}
		t.Fatalf("counter corruption reset authority: %v", err)
	}
}

func TestStoreTrustedSnapshotReconciliationRetainsGuardsAndCoalescesJournalPositions(t *testing.T) {
	s, b, _ := testStore(t)
	a := mustAdmit(t, s, b, "human")
	if _, err := s.PrepareAction(b, Action{ID: "unresolved", Admission: a.Sequence, NativeInputID: "input-human", Target: "worker", OperationID: "action-op", Request: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	initial := JournalCursor{Binding: "source-1", Sequence: 500}
	if err := s.ReconcileJournal(b, initial, JournalSnapshot{Metadata: json.RawMessage(`{"native_history_available":false}`)}); err != nil {
		t.Fatal(err)
	}
	event := Event{ID: "event", Cursor: JournalCursor{Binding: "source-1", Sequence: 700}, ExaminedThrough: JournalCursor{Binding: "source-1", Sequence: 699}, Metadata: json.RawMessage(`{"task":"changed"}`), WakeID: "wake"}
	wake := Wake{ID: "wake", OperationID: "wake-op", Request: json.RawMessage(`{"text":"worker update"}`)}
	s.beforeCommit = func() error { return syscall.ENOSPC }
	if _, err := s.AcceptEvent(b, event, wake); !errors.Is(err, syscall.ENOSPC) {
		t.Fatal(err)
	}
	s.beforeCommit = nil
	snap, _ := s.Snapshot()
	if snap.Journal[b.ConversationID] != initial || len(snap.Wakes) != 0 {
		t.Fatal("checkpoint survived rejected event")
	}
	if _, err := s.AcceptEvent(b, event, wake); err != nil {
		t.Fatal(err)
	}
	replacement := JournalCursor{Binding: "source-2", Sequence: 42}
	if err := s.ReconcileJournal(b, replacement, JournalSnapshot{Previous: initial, Metadata: json.RawMessage(`{}`)}); !errors.Is(err, ErrStale) {
		t.Fatalf("stale snapshot changed journal: %v", err)
	}
	if err := s.ReconcileJournal(b, replacement, JournalSnapshot{Previous: event.Cursor, Metadata: json.RawMessage(`{"native_history_available":true}`)}); err != nil {
		t.Fatal(err)
	}
	snap, _ = s.Snapshot()
	if snap.Journal[b.ConversationID] != replacement || len(snap.Wakes) != 1 || snap.Wakes[key(b.ConversationID, "wake")].State != "prepared" || snap.Actions[key(b.ConversationID, "unresolved")].State != "prepared" {
		t.Fatal("snapshot settled or lost uncertain work")
	}
	if err := s.ReconcileJournal(b, JournalCursor{Binding: "source-2", Sequence: 100}, JournalSnapshot{Previous: replacement, Metadata: json.RawMessage(`{"unchanged":true}`)}); err != nil {
		t.Fatal(err)
	}
	snap, _ = s.Snapshot()
	if len(snap.Wakes) != 1 {
		t.Fatal("irrelevant journal positions created model work")
	}
}
