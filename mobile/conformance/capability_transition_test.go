package conformance_test

// End-to-end phone consumption for the additive capability_transition journal event. The
// same running App receives the event, updates its bound Session model, and keeps the gap in
// the transcript; no reconnect, process restart, or pairing ceremony is involved.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remotegw"
)

func transitionHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	// Replace the still-unused default sink so the reconcile record advertises the
	// capability-record version the phone requires before routing any record to chat.
	h.sink = remotegw.NewRelaySink(remotegw.RelayConfig{
		Appender:       h.machineRelay,
		Target:         h.phoneTarget,
		Machine:        h.Machine,
		EpochID:        h.EpochID,
		Key:            h.Keys.ContentKey,
		RecipientKeyID: [8]byte{},
		SenderKeyID:    h.senderKeyID,
		Authorities:    fixedAuthorities{},
		Now:            h.sealNow,
		Profile: protocol.RemoteProfileV1{
			Version:                  1,
			InteractionSchemaVersion: 1,
			TerminalViewVersion:      1,
			CapabilityRecordVersion:  1,
		},
	})
	return h
}

func liveCaps(structured bool) *schema.SessionCapabilities {
	return &schema.SessionCapabilities{
		Provider:         "claude",
		ProviderVersion:  "1.0.0",
		AdapterRevision:  "test",
		SessionInstance:  "instance-live",
		StructuredChat:   structured,
		TerminalFallback: false,
		TerminalControl:  false,
	}
}

func TestCapabilityTransition_UpdatesTheCurrentPhoneWithoutReconnectAndKeepsTheGap(t *testing.T) {
	h := transitionHarness(t)
	h.PushReconcile()
	eventually(t, "reconcile profile never reached the current App", func() bool {
		s, err := h.App.StateSummary()
		return err == nil && s.Reconciled
	})

	h.PushRoster(schema.JournalRecord{
		SessionID: testSession, Type: "roster", Group: "working", Capabilities: liveCaps(true),
	})
	eventually(t, "initial structured capability never reached the phone", func() bool {
		s, err := h.App.Session(testSession)
		return err == nil && s.StructuredChat && s.Destination == "chat"
	})

	h.PushEvent(schema.JournalRecord{
		Cursor: 2, SessionID: testSession, Type: phonecore.RecordTypeCapabilityTransition,
		Capabilities: liveCaps(false),
	})
	eventually(t, "live degradation did not disable the current phone session", func() bool {
		s, err := h.App.Session(testSession)
		return err == nil && !s.StructuredChat && !s.TerminalControl
	})

	gapBody, err := json.Marshal(map[string]any{
		"ts":     time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC),
		"reason": "older history is missing",
	})
	if err != nil {
		t.Fatalf("marshal gap: %v", err)
	}
	h.PushEvent(schema.JournalRecord{
		Cursor: 3, SessionID: testSession, Type: phonecore.RecordTypeStructuredGap, Item: gapBody,
	})
	eventually(t, "visible structured gap never reached the current phone transcript", func() bool {
		items := r2Transcript(t, h)
		return len(items) == 1 && items[0].Kind == phonecore.KindStructuredGap
	})

	h.PushEvent(schema.JournalRecord{
		Cursor: 4, SessionID: testSession, Type: phonecore.RecordTypeCapabilityTransition,
		Capabilities: liveCaps(true),
	})
	eventually(t, "same-instance recovery did not re-enable the current phone session", func() bool {
		s, err := h.App.Session(testSession)
		return err == nil && s.StructuredChat && s.Destination == "chat" && !s.TerminalControl
	})

	items := r2Transcript(t, h)
	if len(items) != 1 || items[0].Kind != phonecore.KindStructuredGap || items[0].Text != "older history is missing" {
		t.Fatalf("recovery erased or changed the retained gap: %+v", items)
	}
}
