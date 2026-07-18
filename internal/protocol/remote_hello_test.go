package protocol

// FAILING-FIRST protocol tests for the remote capability negotiation and the
// additive Control fields (plan R-PROT.1/.2, ADR-007 D2/D4, amendments D.0-A1/A6).
// RED is undefined-only: `go test ./internal/protocol/` fails to compile because the
// production symbols below do not exist yet. Existing codec/handshake/drift tests
// are UNMODIFIED and pass again once the implementer adds these additive,
// byte-compatible fields.
//
// FROZEN API these tests expect (additive to the FROZEN wire surface in
// harness_test.go — every new field omitempty so existing messages serialize
// byte-identically, GG-7):
//
//	// New negotiated capabilities (R-PROT.1).
//	const ( CapRemoteGateway = "remote-gateway"; CapJournal = "journal";
//	        CapActivity = "activity"; CapPolicy = "policy"; CapPairing = "pairing" )
//
//	// New control ops (R-PROT.3/.4/.5).
//	const ( OpJournalSubscribe = "journal_subscribe"; OpJournalRead = "journal_read";
//	        OpJournalEvent = "journal_event" )
//
//	// Additive omitempty Control fields (R-PROT.2/A6). operation_id (idempotency id)
//	// is separated from interaction_id (the agent interaction being approved). times
//	// are daemon-authoritative and MUST be omitempty-representable (pointer or unix
//	// int) so a zero Control emits no new key.
//	//   OperationID   string      `json:"operation_id,omitempty"`
//	//   InteractionID string      `json:"interaction_id,omitempty"`
//	//   DeviceID      string      `json:"device_id,omitempty"`
//	//   DeviceSig     string      `json:"device_sig,omitempty"`   // detached Ed25519 over the canonical op tuple (D4)
//	//   Cursor        uint64      `json:"cursor,omitempty"`
//	//   IssuedAt / ExpiresAt      `json:"issued_at,omitempty"`/`json:"expires_at,omitempty"` (daemon-authoritative)
//	//   Approve       *ApproveReq `json:"approve,omitempty"`
//	//   ErrorCode     ErrorCode   `json:"error_code,omitempty"`   (R-PROT.7)
//	type ApproveReq struct {
//	    Session       string           `json:"session"`
//	    AgentInstance AgentInstanceRef `json:"agent_instance"`
//	    InteractionID string           `json:"interaction_id"`
//	    ContentHash   string           `json:"content_hash"`
//	    // ExpiresAt daemon-authoritative, omitempty
//	}
//	type AgentInstanceRef struct { ShimPID int `json:"shim_pid"`; ShimStartTime int64 `json:"shim_start_time"` }

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestHello_NegotiatesRemoteCaps (R-PROT.1): a client offering the remote caps gets
// them back in the negotiated intersection, alongside the existing attach/subscribe.
func TestHello_NegotiatesRemoteCaps(t *testing.T) {
	sock := serveStub(t, newStubDaemon())
	rc := rawDial(t, sock)

	offered := []string{"attach", "subscribe", CapRemoteGateway, CapJournal, CapActivity, CapPolicy, CapPairing}
	reply := rc.hello(Version, offered)

	got := map[string]bool{}
	for _, c := range reply.Capabilities {
		got[c] = true
	}
	for _, want := range []string{CapRemoteGateway, CapJournal, CapActivity, CapPolicy, CapPairing} {
		if !got[want] {
			t.Fatalf("hello did not negotiate remote capability %q; got %v", want, reply.Capabilities)
		}
	}
}

// TestServer_UnnegotiatedOpRefused (R-PROT.1): an op whose capability was NOT
// negotiated is refused with an error, never actioned. A client that did not
// negotiate `journal` cannot journal_read.
func TestServer_UnnegotiatedOpRefused(t *testing.T) {
	sock := serveStub(t, newStubDaemon())
	rc := rawDial(t, sock)

	// Hello offering ONLY the legacy caps — journal is not negotiated.
	reply := rc.hello(Version, []string{"attach", "subscribe"})

	rc.writeControl(Control{Op: OpJournalRead, EndpointID: reply.EndpointID, Cursor: 0})
	got := rc.readControl()
	if got.Op != OpError {
		t.Fatalf("unnegotiated journal_read got op %q; want an error refusal", got.Op)
	}
}

// TestControl_AdditiveFieldsOmitEmpty (R-PROT.2/A6): every new field is omitempty,
// so an existing-shape Control serializes byte-identically (no new keys), and the
// new fields appear only when set.
func TestControl_AdditiveFieldsOmitEmpty(t *testing.T) {
	// An OLD-shape Control (only pre-remote fields) must round-trip and emit NONE of
	// the new keys — the existing wire bytes are unchanged (GG-7).
	old := Control{Op: OpKill, EndpointID: "ep-1", SessionID: "ep-1/sess1"}
	oldJSON, err := EncodeControl(old)
	if err != nil {
		t.Fatalf("EncodeControl(old): %v", err)
	}
	for _, key := range []string{
		"operation_id", "interaction_id", "device_id", "device_sig",
		"cursor", "issued_at", "expires_at", "approve", "error_code",
	} {
		if strings.Contains(string(oldJSON), `"`+key+`"`) {
			t.Fatalf("old-shape Control emitted new key %q: %s (field must be omitempty; times must be pointer/int)", key, oldJSON)
		}
	}
	back, err := DecodeControl(oldJSON)
	if err != nil {
		t.Fatalf("DecodeControl(old): %v", err)
	}
	// Control carries slice fields, so compare by re-encoded bytes (byte-identity),
	// not ==: an old-shape message must serialize to exactly the same bytes.
	reJSON, err := EncodeControl(back)
	if err != nil {
		t.Fatalf("EncodeControl(round-trip): %v", err)
	}
	if string(reJSON) != string(oldJSON) {
		t.Fatalf("old-shape Control did not round-trip byte-stable:\n got  %s\n want %s", reJSON, oldJSON)
	}

	// When SET, the new fields appear and round-trip.
	c := Control{
		Op:            OpKill,
		EndpointID:    "ep-1",
		SessionID:     "ep-1/sess1",
		OperationID:   "devA:01JOP",
		InteractionID: "int-7",
		DeviceID:      "devA",
		DeviceSig:     "ZmFrZXNpZw==",
		Cursor:        42,
		Approve: &ApproveReq{
			Session:       "ep-1/sess1",
			AgentInstance: AgentInstanceRef{ShimPID: 4321, ShimStartTime: 99887766},
			InteractionID: "int-7",
			ContentHash:   "sha256:deadbeef",
		},
	}
	setJSON, err := EncodeControl(c)
	if err != nil {
		t.Fatalf("EncodeControl(set): %v", err)
	}
	for _, key := range []string{"operation_id", "interaction_id", "device_id", "device_sig", "cursor", "approve"} {
		if !strings.Contains(string(setJSON), `"`+key+`"`) {
			t.Fatalf("set Control missing expected key %q: %s", key, setJSON)
		}
	}
	var round Control
	if err := json.Unmarshal(setJSON, &round); err != nil {
		t.Fatalf("unmarshal set Control: %v", err)
	}
	if round.OperationID != c.OperationID || round.InteractionID != c.InteractionID ||
		round.DeviceID != c.DeviceID || round.DeviceSig != c.DeviceSig || round.Cursor != c.Cursor {
		t.Fatalf("set Control round-trip mismatch: got %+v want %+v", round, c)
	}
	if round.Approve == nil || round.Approve.AgentInstance.ShimPID != 4321 || round.Approve.ContentHash != "sha256:deadbeef" {
		t.Fatalf("approve sub-struct did not round-trip: got %+v", round.Approve)
	}
}
