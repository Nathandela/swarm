package voice

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"syscall"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestStoreExactReplayDoesNotWrite(t *testing.T) {
	s, b, _ := testStore(t)
	a := mustAdmit(t, s, b, "human")
	action := Action{ID: "effect", Admission: a.Sequence, NativeInputID: "input-human", Target: "worker", OperationID: "op", Request: json.RawMessage(`{"text":"exact"}`)}
	if _, err := s.PrepareAction(b, action); err != nil {
		t.Fatal(err)
	}

	event := Event{ID: "event", Cursor: JournalCursor{Binding: "journal", Sequence: 1}, Metadata: json.RawMessage(`{}`), WakeID: "wake"}
	wake := Wake{ID: "wake", OperationID: "wake-op", Request: json.RawMessage(`{}`)}
	if _, err := s.AcceptEvent(b, event, wake); err != nil {
		t.Fatal(err)
	}
	for name, retry := range map[string]func() error{
		"action": func() error { _, err := s.PrepareAction(b, action); return err },
		"event":  func() error { _, err := s.AcceptEvent(b, event, wake); return err },
	} {
		t.Run(name, func(t *testing.T) {
			var before, after int
			if err := s.db.View(func(tx *bolt.Tx) error { before = tx.ID(); return nil }); err != nil {
				t.Fatal(err)
			}
			if err := retry(); err != nil {
				t.Fatal(err)
			}
			if err := s.db.View(func(tx *bolt.Tx) error { after = tx.ID(); return nil }); err != nil {
				t.Fatal(err)
			}
			if before != after {
				t.Fatalf("exact replay committed a new transaction: %d -> %d", before, after)
			}
		})
	}

}

func TestStoreRecordSetIntegritySurvivesCacheAndRestart(t *testing.T) {
	for _, damage := range []string{"delete", "substitute", "insert", "counter"} {
		t.Run(damage, func(t *testing.T) {
			s, b, dir := testStore(t)
			mustBegin(t, s, b, "one")
			if _, err := s.SetUtterancePhase(b, "one", "failed"); err != nil {
				t.Fatal(err)
			}
			mustBegin(t, s, b, "two")
			if err := s.db.Update(func(tx *bolt.Tx) error {
				root := tx.Bucket([]byte("controller"))
				records := root.Bucket([]byte("records"))
				one := recordKey("utterances", key(b.ConversationID, "one"))
				two := recordKey("utterances", key(b.ConversationID, "two"))
				switch damage {
				case "delete":
					return records.Delete(one)
				case "substitute":
					return records.Put(one, bytes.Clone(records.Get(two)))
				case "insert":
					return records.Put(recordKey("utterances", "unaccounted"), bytes.Clone(records.Get(two)))
				case "counter":
					var h storeHeader
					if err := json.Unmarshal(root.Get([]byte("state")), &h); err != nil {
						return err
					}
					h.Generation++
					raw, err := json.Marshal(h)
					if err != nil {
						return err
					}
					return root.Put([]byte("state"), raw)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Snapshot(); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("live cache hid damaged record set: %v", err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := OpenStore(dir)
			if reopened != nil {
				_ = reopened.Close()
			}
			if !errors.Is(err, ErrCorrupt) {
				t.Fatalf("restart accepted damaged record set: %v", err)
			}
		})
	}
}

func TestStoreChangedTablesAndHeaderRollBackTogether(t *testing.T) {
	s, _, _ := testStore(t)
	before, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	beforeRaw, err := json.Marshal(before)
	if err != nil {
		t.Fatal(err)
	}
	s.beforeCommit = func() error { return syscall.ENOSPC }
	err = s.update(func(st *State) error {
		st.CurrentConversation = "changed"
		putRecord(st, "conversations", &st.Conversations, "new", Conversation{})
		putRecord(st, "utterances", &st.Utterances, "new", Utterance{})
		putRecord(st, "admissions", &st.Admissions, "new", Admission{})
		putRecord(st, "actions", &st.Actions, "new", Action{Request: json.RawMessage(`{}`)})
		putRecord(st, "requests", &st.Requests, "new", Request{Payload: json.RawMessage(`{}`)})
		putRecord(st, "closed_turns", &st.ClosedTurns, "new", true)
		putRecord(st, "cancelled_tasks", &st.CancelledTasks, "new", true)
		putRecord(st, "permits", &st.Permits, "new", SpeechPermit{})
		putRecord(st, "publications", &st.Publications, "new", Publication{})
		putRecord(st, "journal", &st.Journal, "new", JournalCursor{})
		putRecord(st, "journal_snapshots", &st.JournalSnapshots, "new", JournalSnapshot{Metadata: json.RawMessage(`{}`)})
		putRecord(st, "events", &st.Events, "new", Event{Metadata: json.RawMessage(`{}`)})
		putRecord(st, "wakes", &st.Wakes, "new", Wake{Request: json.RawMessage(`{}`)})
		deleteRecord(st, "conversations", &st.Conversations, before.CurrentConversation)
		return nil
	})
	if !errors.Is(err, syscall.ENOSPC) {
		t.Fatal(err)
	}
	s.beforeCommit = nil
	after, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	afterRaw, err := json.Marshal(after)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeRaw, afterRaw) {
		t.Fatal("failed commit changed cached authority")
	}
	if err := s.db.View(func(tx *bolt.Tx) error {
		persisted, err := readState(tx)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(persisted)
		if err != nil {
			return err
		}
		if !bytes.Equal(beforeRaw, raw) {
			t.Fatal("failed commit changed durable authority")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestStoreIncrementalBytesMatchEscapedFullState(t *testing.T) {
	s, _, _ := testStore(t)
	for _, op := range []string{"add", "replace", "add-two", "delete", "delete-two"} {
		if err := s.update(func(st *State) error {
			k := "key<>&\"\x00"
			switch op {
			case "add":
				putRecord(st, "utterances", &st.Utterances, k, Utterance{ID: k, Text: "hello <>&"})
			case "replace":
				putRecord(st, "utterances", &st.Utterances, k, Utterance{ID: k, Text: "longer <>&\"\n"})
			case "add-two":
				putRecord(st, "utterances", &st.Utterances, "second", Utterance{Text: "two"})
			case "delete":
				deleteRecord(st, "utterances", &st.Utterances, k)
			case "delete-two":
				deleteRecord(st, "utterances", &st.Utterances, "second")
			}
			return nil
		}); err != nil {
			t.Fatal(op, err)
		}
		if err := s.db.View(func(tx *bolt.Tx) error { _, err := readState(tx); return err }); err != nil {
			t.Fatalf("%s: full serialization disagrees with incremental storage: %v", op, err)
		}
	}
}

func TestStoreCachedRawMessagesNeverEscape(t *testing.T) {
	s, b, _ := testStore(t)
	r, err := s.PutRequest(b, Request{ID: "q", Kind: "manager", Payload: json.RawMessage(`{"question":"yes?"}`), ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	originalPayload := bytes.Clone(r.Payload)
	r.Payload[0] = 'X'
	r, err = s.AnswerRequest(b, "q", r.Hash, "answer", json.RawMessage(`{"yes":true}`), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	originalAnswer := bytes.Clone(r.Answer)
	r.Payload[0] = 'X'
	r.Answer[0] = 'X'
	w, err := s.AcceptEvent(b, Event{ID: "event", Cursor: JournalCursor{Binding: "journal", Sequence: 1}, Metadata: json.RawMessage(`{"done":true}`), WakeID: "wake"}, Wake{ID: "wake", OperationID: "wake-op", Request: json.RawMessage(`{"run":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	originalWake := bytes.Clone(w.Request)
	w.Request[0] = 'X'
	for i := 0; i < 2; i++ {
		snapshot, err := s.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		saved := snapshot.Requests[key(b.ConversationID, "q")]
		wake := snapshot.Wakes[key(b.ConversationID, "wake")]
		event := snapshot.Events[key(b.ConversationID, "event")]
		if !json.Valid(event.Metadata) {
			t.Fatal("caller mutated cached event metadata")
		}
		if !bytes.Equal(saved.Payload, originalPayload) || !bytes.Equal(saved.Answer, originalAnswer) || !bytes.Equal(wake.Request, originalWake) {
			t.Fatal("caller mutated cached bytes")
		}
		saved.Payload[0] = 'X'
		saved.Answer[0] = 'X'
		wake.Request[0] = 'X'
		event.Metadata[0] = 'X'
	}
}

func TestStoreOversizedRecordKeyFailsWithoutChangingAuthority(t *testing.T) {
	s, b, _ := testStore(t)
	before, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	beforeRaw, err := json.Marshal(before)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.BeginUtterance(b, Utterance{ID: strings.Repeat("x", 32768), DeviceID: "device", VoiceSessionID: "voice", MediaEpoch: 1}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized record ID: %v", err)
	}
	after, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	afterRaw, err := json.Marshal(after)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeRaw, afterRaw) {
		t.Fatal("oversized key changed cached authority")
	}
	if err = s.db.View(func(tx *bolt.Tx) error {
		durable, err := readState(tx)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(durable)
		if err != nil {
			return err
		}
		if !bytes.Equal(beforeRaw, raw) {
			t.Fatal("oversized key changed durable authority")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestStoreNativeGenerationIsRequiredAndImmutable(t *testing.T) {
	s, b, _ := testStore(t)
	missing := b
	missing.NativeGeneration = ""
	if _, err := s.BindManager(missing); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing native generation: %v", err)
	}
	replacement := b
	replacement.NativeGeneration = "replacement-native-epoch"
	if _, err := s.BindManager(replacement); !errors.Is(err, ErrConflict) {
		t.Fatalf("native authority retargeted: %v", err)
	}
	if _, err := s.BeginUtterance(replacement, Utterance{ID: "wrong-native-epoch", DeviceID: "device", VoiceSessionID: "voice", MediaEpoch: 1}); !errors.Is(err, ErrStale) {
		t.Fatalf("replacement native epoch used old controller authority: %v", err)
	}
	if _, err := s.BeginUtterance(b, Utterance{ID: "original-native-epoch", DeviceID: "device", VoiceSessionID: "voice", MediaEpoch: 1}); err != nil {
		t.Fatal(err)
	}
}
