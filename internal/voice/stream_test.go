package voice

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestVoiceStreamCursorIsIndependentAndReplayLossRequiresSnapshot(t *testing.T) {
	stream, err := NewVoiceStream()
	if err != nil {
		t.Fatal(err)
	}
	first, err := stream.Append("status", json.RawMessage(`{"state":"working"}`))
	if err != nil {
		t.Fatal(err)
	}
	events, gap, cursor := stream.Replay(VoiceCursor{StreamID: first.Cursor.StreamID})
	if gap || len(events) != 1 || cursor.Sequence != 1 {
		t.Fatalf("events=%v gap=%t cursor=%+v", events, gap, cursor)
	}
	events[0].Payload[0] = 'X'
	again, _, _ := stream.Replay(VoiceCursor{StreamID: first.Cursor.StreamID})
	if !json.Valid(again[0].Payload) {
		t.Fatal("replay aliased internal bytes")
	}
	for i := 0; i < MaxStreamEvents; i++ {
		if _, err := stream.Append("status", json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	if _, gap, _ := stream.Replay(VoiceCursor{StreamID: first.Cursor.StreamID}); !gap {
		t.Fatal("eviction hid stream gap")
	}
	if _, gap, _ := stream.Replay(first.Cursor); gap {
		t.Fatal("retained suffix incorrectly gapped")
	}
	restarted, err := NewVoiceStream()
	if err != nil {
		t.Fatal(err)
	}
	if _, gap, current := restarted.Replay(first.Cursor); !gap || current.StreamID == first.Cursor.StreamID {
		t.Fatal("restart reused stream authority")
	}
	s, b, _ := testStore(t)
	if _, err := s.AcceptEvent(b, Event{ID: "event", Cursor: JournalCursor{Binding: "upstream", Sequence: 1}, Metadata: json.RawMessage(`{}`), WakeID: "wake"}, Wake{ID: "wake", OperationID: "operation", Request: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	snap, _ := s.Snapshot()
	if snap.Journal[b.ConversationID].Binding != "upstream" || snap.Journal[b.ConversationID].Sequence != 1 {
		t.Fatal("voice cursor replaced upstream cursor")
	}
}

func TestVoiceStreamBytePressureAndBadFrames(t *testing.T) {
	stream, err := NewVoiceStream()
	if err != nil {
		t.Fatal(err)
	}
	first, _ := stream.Append("status", json.RawMessage(`{}`))
	raw, _ := json.Marshal(strings.Repeat("a", 120<<10))
	for i := 0; i < 10; i++ {
		if _, err := stream.Append("status", raw); err != nil {
			t.Fatal(err)
		}
	}
	if _, gap, _ := stream.Replay(first.Cursor); !gap {
		t.Fatal("byte pressure did not evict")
	}
	if _, err := stream.Append("status", json.RawMessage(`invalid`)); err == nil {
		t.Fatal("accepted invalid JSON")
	}
	oversized, _ := json.Marshal(strings.Repeat("a", 128<<10))
	if _, err := stream.Append("status", oversized); err == nil {
		t.Fatal("accepted oversized control frame")
	}
}
