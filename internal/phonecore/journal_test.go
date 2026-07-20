package phonecore

// Failing-first tests for the phone-core journal-receive path (R-PHC.3/.5): the phone
// opens a mailbox envelope under its epoch content key, decodes the journal record, and
// applies it to a merged session cache whose Group is taken VERBATIM from the wire
// (never derived on-device). This is exercised END TO END against the gateway's
// RelaySink: records sealed by the machine are opened and applied by the phone, so the
// full daemon -> E2EE -> relay -> phone journal path is proven with real crypto.
// RED is undefined-only (OpenJournalEnvelope / NewSessionCache do not exist yet).

import (
	"context"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remotegw"
	"github.com/Nathandela/swarm/internal/status"
)

// memMailbox is a shared in-memory mailbox: the gateway's RelaySink appends to it and
// the phone drains it, standing in for the relay in a same-process E2EE loop test.
type memMailbox struct {
	envs [][]byte
}

func (m *memMailbox) MailboxAppend(_ context.Context, _ string, env []byte) (uint64, error) {
	m.envs = append(m.envs, env)
	return uint64(len(m.envs)), nil
}

func TestPhoneCore_ReceivesGatewayJournalEndToEnd(t *testing.T) {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(0xA0 + i)
	}
	box := &memMailbox{}
	sink := remotegw.NewRelaySink(remotegw.RelayConfig{
		Appender: box, Target: "phone", EpochID: 3, Key: key,
	})

	// The machine (gateway) forwards a roster snapshot + a live launched event.
	sink.Snapshot([]protocol.JournalRecord{
		{Cursor: 10, SessionID: "m/s1", Type: "roster", Group: status.Group("working")},
		{Cursor: 10, SessionID: "m/s2", Type: "roster", Group: status.Group("needs_input")},
	}, 10)
	sink.Event(protocol.JournalRecord{Cursor: 11, SessionID: "m/s3", Type: "launched"})
	sink.Event(protocol.JournalRecord{Cursor: 12, SessionID: "m/s2", Type: "group_transition", Group: status.Group("idle")})
	if err := sink.Err(); err != nil {
		t.Fatalf("relay sink error: %v", err)
	}

	// The phone opens each envelope under its content key and applies it.
	cache := NewSessionCache()
	for i, raw := range box.envs {
		rec, seq, err := OpenJournalEnvelope(key, raw)
		if err != nil {
			t.Fatalf("env %d open: %v", i, err)
		}
		if seq == 0 {
			t.Fatalf("env %d has zero Seq", i)
		}
		cache.Apply(rec)
	}

	// s1 came from the roster with Group working (verbatim).
	if cs, ok := cache.Get("m/s1"); !ok || cs.Group != status.Group("working") {
		t.Fatalf("s1 = %+v ok=%v; want Group working", cs, ok)
	}
	// s2's group was updated by the live group_transition to idle (verbatim).
	if cs, ok := cache.Get("m/s2"); !ok || cs.Group != status.Group("idle") {
		t.Fatalf("s2 = %+v ok=%v; want Group idle (updated by group_transition)", cs, ok)
	}
	// s3 was launched live and is present.
	if cs, ok := cache.Get("m/s3"); !ok || !cs.Present {
		t.Fatalf("s3 = %+v ok=%v; want present", cs, ok)
	}
	if got := cache.Cursor(); got != 12 {
		t.Fatalf("cache cursor = %d; want 12 (highest applied)", got)
	}
	if n := len(cache.List()); n != 3 {
		t.Fatalf("cache has %d sessions; want 3", n)
	}
}

// TestPhoneCore_WrongKeyRejected: an envelope sealed under one epoch key does not open
// under another (the phone rejects, never applies, a record it cannot authenticate).
func TestPhoneCore_WrongKeyRejected(t *testing.T) {
	var key, wrong crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 1)
	}
	box := &memMailbox{}
	sink := remotegw.NewRelaySink(remotegw.RelayConfig{Appender: box, Target: "phone", EpochID: 1, Key: key})
	sink.Event(protocol.JournalRecord{Cursor: 1, SessionID: "m/s1", Type: "launched"})

	if _, _, err := OpenJournalEnvelope(wrong, box.envs[0]); err == nil {
		t.Fatalf("envelope opened under the wrong content key; want rejection")
	}
}
