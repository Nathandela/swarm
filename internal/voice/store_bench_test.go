package voice

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Fixtures mix settled receipts and unresolved guards, leaving room for one event.
func benchmarkStore(b *testing.B, records int) (*Store, ManagerBinding) {
	b.Helper()
	s, err := OpenStore(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = s.Close() })
	binding, err := s.BindManager(ManagerBinding{ConversationID: "bench", Provider: "codex", SessionID: "machine/manager", SessionInstance: "instance", NativeSessionID: "thread", NativeGeneration: "native-epoch", LoginEpoch: "login"})
	if err != nil {
		b.Fatal(err)
	}
	err = s.db.Update(func(tx *bolt.Tx) error {
		st, err := readState(tx)
		if err != nil {
			return err
		}
		c := st.Conversations[binding.ConversationID]
		c.LastAdmission = 1
		st.Conversations[binding.ConversationID] = c
		st.Admissions[admissionKey(binding.ConversationID, 1)] = Admission{ConversationID: binding.ConversationID, Sequence: 1, NativeInputID: "input", SourceID: "benchmark"}
		for countRecords(st) < records-3 {
			id := fmt.Sprintf("action-%d", len(st.Actions))
			a := Action{ID: id, ConversationID: binding.ConversationID, Admission: 1, NativeInputID: "input", Target: "machine/worker@instance", OperationID: "operation-" + id, Request: json.RawMessage(`{"text":"worker task","target":"worker"}`), State: "prepared"}
			a.RequestHash = hashBytes(a.Request)
			if len(st.Actions)%2 == 0 {
				a.State = "settled"
				a.Receipt = json.RawMessage(`{"accepted":true}`)
				a.SettledAt = time.Now()
			}
			st.Actions[key(binding.ConversationID, id)] = a
		}
		raw, err := json.Marshal(st)
		if err != nil {
			return err
		}
		return writeStateRecord(tx, raw)
	})
	if err != nil {
		b.Fatal(err)
	}
	if _, err := s.Snapshot(); err != nil {
		b.Fatal(err)
	}
	return s, binding
}

// Restoring the fixture is excluded so Fresh measures one new durable operation,
// rather than growing past capacity or silently measuring exact retries.
func benchmarkRestore(b *testing.B, s *Store) func() {
	b.Helper()
	snapshot, err := s.Snapshot()
	if err != nil {
		b.Fatal(err)
	}
	baseline, err := json.Marshal(snapshot)
	if err != nil {
		b.Fatal(err)
	}
	return func() {
		b.StopTimer()
		if err := s.db.Update(func(tx *bolt.Tx) error { return writeStateRecord(tx, baseline) }); err != nil {
			b.Fatal(err)
		}
		if _, err := s.Snapshot(); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
	}
}

func BenchmarkControllerStore(b *testing.B) {
	for _, records := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("records=%d/PrepareActionRetry", records), func(b *testing.B) {
			s, binding := benchmarkStore(b, records)
			a := Action{ID: "benchmark-action", Admission: 1, NativeInputID: "input", Target: "worker", OperationID: "benchmark-op", Request: json.RawMessage(`{"text":"exact"}`)}
			if _, err := s.PrepareAction(binding, a); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := s.PrepareAction(binding, a); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("records=%d/AcceptEventRetry", records), func(b *testing.B) {
			s, binding := benchmarkStore(b, records)
			e := Event{ID: "event", Cursor: JournalCursor{Binding: "journal", Sequence: 1}, Metadata: json.RawMessage(`{"task":"done"}`), WakeID: "wake"}
			w := Wake{ID: "wake", OperationID: "wake-op", Request: json.RawMessage(`{"text":"worker complete"}`)}
			if _, err := s.AcceptEvent(binding, e, w); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := s.AcceptEvent(binding, e, w); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("records=%d/PrepareActionFresh", records), func(b *testing.B) {
			s, binding := benchmarkStore(b, records)
			restore := benchmarkRestore(b, s)
			a := Action{ID: "fresh-action", Admission: 1, NativeInputID: "input", Target: "worker", OperationID: "fresh-op", Request: json.RawMessage(`{"text":"exact"}`)}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				restore()
				if _, err := s.PrepareAction(binding, a); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("records=%d/AcceptEventFresh", records), func(b *testing.B) {
			s, binding := benchmarkStore(b, records)
			restore := benchmarkRestore(b, s)
			e := Event{ID: "fresh-event", Cursor: JournalCursor{Binding: "journal", Sequence: 1}, Metadata: json.RawMessage(`{"task":"done"}`), WakeID: "fresh-wake"}
			w := Wake{ID: "fresh-wake", OperationID: "fresh-wake-op", Request: json.RawMessage(`{"text":"worker complete"}`)}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				restore()
				if _, err := s.AcceptEvent(binding, e, w); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("records=%d/Snapshot", records), func(b *testing.B) {
			s, _ := benchmarkStore(b, records)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := s.Snapshot(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
