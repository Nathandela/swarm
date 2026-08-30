package phonecore

// Failing-first coverage for live capability transitions. A capability transition is an
// ordinary, cursor-ordered journal record: it must update the session already on the phone
// without a reconnect, but only for the incarnation the phone is currently displaying.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

const recordTypeCapabilityTransition = "capability_transition"

func transitionCaps(instance string, structured bool) *schema.SessionCapabilities {
	return &schema.SessionCapabilities{
		Provider:         "claude",
		ProviderVersion:  "1.0.0",
		AdapterRevision:  "test",
		SessionInstance:  instance,
		StructuredChat:   structured,
		TerminalFallback: false,
		TerminalControl:  false,
	}
}

func TestCapabilityTransition_CannotGrantATerminalRouteOrChangeLaunchFacts(t *testing.T) {
	cache := NewSessionCache()
	cache.Apply(capabilityRecord(0, "roster", "instance-1", false))

	terminalGrant := capabilityRecord(2, recordTypeCapabilityTransition, "instance-1", false)
	terminalGrant.Capabilities.TerminalFallback = true
	terminalGrant.Capabilities.TerminalControl = true
	cache.Apply(terminalGrant)
	got, _ := cache.Get("m1/s1")
	if got.Capabilities == nil || got.Capabilities.TerminalFallback || got.Capabilities.TerminalControl {
		t.Fatalf("runtime transition granted a terminal route/control: %+v", got.Capabilities)
	}

	changedProvider := capabilityRecord(3, recordTypeCapabilityTransition, "instance-1", true)
	changedProvider.Capabilities.ProviderVersion = "other-build"
	cache.Apply(changedProvider)
	got, _ = cache.Get("m1/s1")
	if got.Capabilities == nil || got.Capabilities.StructuredChat {
		t.Fatalf("transition that changed immutable launch facts recovered chat: %+v", got.Capabilities)
	}

	cache.Apply(capabilityRecord(4, recordTypeCapabilityTransition, "instance-1", true))
	got, _ = cache.Get("m1/s1")
	if got.Capabilities == nil || !got.Capabilities.StructuredChat ||
		got.Capabilities.TerminalFallback || got.Capabilities.TerminalControl {
		t.Fatalf("exact chat-plane recovery did not land: %+v", got.Capabilities)
	}
}

func capabilityRecord(cursor uint64, typ, instance string, structured bool) schema.JournalRecord {
	return schema.JournalRecord{
		Cursor:       cursor,
		SessionID:    "m1/s1",
		Type:         typ,
		Capabilities: transitionCaps(instance, structured),
	}
}

func TestCapabilityTransition_OnlyTheDisplayedInstanceCanRecover(t *testing.T) {
	cache := NewSessionCache()
	cache.Apply(capabilityRecord(0, "roster", "instance-current", false))

	// A delayed transition from a replaced process is valid in isolation, but it is not
	// authority for the process this row now identifies.
	cache.Apply(capabilityRecord(2, recordTypeCapabilityTransition, "instance-replaced", true))
	got, _ := cache.Get("m1/s1")
	if got.Capabilities == nil || got.Capabilities.StructuredChat {
		t.Fatalf("mismatched transition replaced current capabilities: %+v", got.Capabilities)
	}
	if got.Capabilities.SessionInstance != "instance-current" {
		t.Fatalf("mismatched transition changed instance to %q", got.Capabilities.SessionInstance)
	}
	if cache.Cursor() != 2 {
		t.Fatalf("ignored authority left cursor at %d, want 2 (the record was consumed)", cache.Cursor())
	}

	cache.Apply(capabilityRecord(3, recordTypeCapabilityTransition, "instance-current", true))
	got, _ = cache.Get("m1/s1")
	if got.Capabilities == nil || !got.Capabilities.StructuredChat {
		t.Fatalf("same-instance recovery was not folded: %+v", got.Capabilities)
	}
	if got.Capabilities.TerminalFallback || got.Capabilities.TerminalControl {
		t.Fatalf("recovery granted terminal surface/control: %+v", got.Capabilities)
	}
}

func TestCapabilityTransition_CannotEstablishAuthorityForAnUnknownInstance(t *testing.T) {
	cache := NewSessionCache()
	cache.Apply(schema.JournalRecord{
		Cursor: 0, SessionID: "m1/s1", Type: "roster", Group: "working",
	})
	cache.Apply(capabilityRecord(2, recordTypeCapabilityTransition, "instance-old", true))

	got, ok := cache.Get("m1/s1")
	if !ok || got.Capabilities != nil {
		t.Fatalf("transition granted authority to a present row with no current instance: %+v, ok=%v", got, ok)
	}

	cache.Apply(capabilityRecord(3, recordTypeCapabilityTransition, "instance-never-seen", true))
	if _, ok := cache.Get("m1/s1"); !ok {
		t.Fatal("control row disappeared while consuming ignored transition")
	}

	empty := NewSessionCache()
	empty.Apply(capabilityRecord(1, recordTypeCapabilityTransition, "instance-orphan", true))
	if got, ok := empty.Get("m1/s1"); ok {
		t.Fatalf("orphan transition invented a live session row and authority: %+v", got)
	}
	if empty.Cursor() != 1 {
		t.Fatalf("orphan transition cursor = %d, want 1: ignored authority is still consumed", empty.Cursor())
	}
}

func TestCapabilityTransition_ReplayOrderingAndInvalidRecordFailClosed(t *testing.T) {
	cache := NewSessionCache()
	cache.Apply(capabilityRecord(0, "roster", "instance-1", false))
	cache.Apply(schema.JournalRecord{Cursor: 10, SessionID: "m1/s1", Type: "group_transition"})

	// An older recovery cannot cross the cursor fence.
	cache.Apply(capabilityRecord(9, recordTypeCapabilityTransition, "instance-1", true))
	got, _ := cache.Get("m1/s1")
	if got.Capabilities == nil || got.Capabilities.StructuredChat {
		t.Fatalf("lower-cursor recovery crossed the cursor fence: %+v", got.Capabilities)
	}

	// Equal-cursor redelivery is harmless and produces only the same latest-state value.
	recovered := capabilityRecord(11, recordTypeCapabilityTransition, "instance-1", true)
	cache.Apply(recovered)
	cache.Apply(recovered)
	got, _ = cache.Get("m1/s1")
	if got.Capabilities == nil || !got.Capabilities.StructuredChat {
		t.Fatalf("equal-cursor recovery replay changed the recovered state: %+v", got.Capabilities)
	}

	invalid := capabilityRecord(12, recordTypeCapabilityTransition, "instance-1", true)
	invalid.Capabilities.TerminalFallback = true
	cache.Apply(invalid)
	got, _ = cache.Get("m1/s1")
	if got.Capabilities != nil {
		t.Fatalf("inconsistent transition survived the phone decode seam: %+v", got.Capabilities)
	}
}

func TestCapabilityTransition_GapDegradesAndRecoveryPersistsWithoutErasingGap(t *testing.T) {
	store := &memStore{}
	seedPaired(t, store)
	router := resumeRouter(t, store, &recordingAcker{})

	accept := func(seq, relayCursor uint64, rec schema.JournalRecord) {
		t.Helper()
		plain, err := json.Marshal(rec)
		if err != nil {
			t.Fatalf("marshal record: %v", err)
		}
		raw := sealFrameFrom(t, testContentKey(), machineSender, 7, seq, plain)
		if _, err := router.AcceptCommit(raw, relayCursor); err != nil {
			t.Fatalf("AcceptCommit(seq=%d type=%q): %v", seq, rec.Type, err)
		}
	}

	accept(1, 101, capabilityRecord(0, "roster", "instance-1", true))
	accept(2, 102, capabilityRecord(1, recordTypeCapabilityTransition, "instance-1", false))
	gap := gapRecord("m1/s1", 2, time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC), "older history is missing")
	// The dedicated transition is the sole live capability carrier. Even a valid record on
	// the gap itself cannot smuggle an early recovery past that channel's instance/delta fence.
	gap.Capabilities = transitionCaps("instance-1", true)
	accept(3, 103, gap)

	cs, _ := router.Sessions().Get("m1/s1")
	if cs.Capabilities == nil || cs.Capabilities.StructuredChat {
		t.Fatalf("capability-bearing gap bypassed the dedicated degradation transition: %+v", cs.Capabilities)
	}
	if got := router.Items().Session("m1/s1"); len(got) != 1 || got[0].Kind != KindStructuredGap {
		t.Fatalf("live gap was not retained in transcript: %+v", got)
	}

	accept(4, 104, capabilityRecord(3, recordTypeCapabilityTransition, "instance-1", true))
	cs, _ = router.Sessions().Get("m1/s1")
	if cs.Capabilities == nil || !cs.Capabilities.StructuredChat {
		t.Fatalf("live recovery did not re-enable structured chat: %+v", cs.Capabilities)
	}
	if got := router.Items().Session("m1/s1"); len(got) != 1 || got[0].Kind != KindStructuredGap {
		t.Fatalf("recovery erased the visible history gap: %+v", got)
	}

	// AcceptCommit persisted both halves before ack. A process restart must show the same
	// recovered capability and the same honest transcript tear.
	restarted := resumeRouter(t, store, &recordingAcker{})
	cs, _ = restarted.Sessions().Get("m1/s1")
	if cs.Capabilities == nil || !cs.Capabilities.StructuredChat {
		t.Fatalf("restart lost recovered capability: %+v", cs.Capabilities)
	}
	if got := restarted.Items().Session("m1/s1"); len(got) != 1 || got[0].Kind != KindStructuredGap {
		t.Fatalf("restart lost or duplicated visible gap: %+v", got)
	}
}
