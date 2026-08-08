package phonecore

// FAILING-FIRST (TDD RED, GG-5) for IS-LIFE-4's PHONE-CORE half: authoring one approve.
//
// Two things must be true here and nowhere else.
//
// THE BINDING IS ECHOED, NEVER COMPUTED (IS-APR-2). The card's `content_hash` is the daemon's
// SHA-256 over the item as shipped and its `expires_at` is daemon-authoritative; a phone that
// recomputed either would be asserting a fact only the machine holds. So the signer takes the
// hash AS TEXT off the item and decodes it, and refuses a value it would have to invent --
// SHA256("") is a valid 32-byte digest, so signing over an empty hash produces a
// structurally-perfect command bound to nothing, refused at the machine with a message about
// the card rather than about the phone.
//
// THE RULE LIVES HERE, not in the facade. mobile/commands.go says why for the take_control
// token: re-deriving a content hash at the call site is "the same forbidden duplication as
// re-deriving LaunchContentHash -- a divergence produces a signature the daemon rejects, with
// no compile error and no message naming the cause".
//
// RED is undefined-only: SignApprove, ApproveInput, SealApproveEnvelope, ApprovalBinding and
// ItemStore.PendingApproval do not exist.

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remotegw"
)

// approveDigest is a content hash in the shape the daemon ships it: 64 lowercase hex chars
// (internal/skeleton/approval.go's contentHashSlot is exactly that wide).
const approveDigest = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"

// TestIsLife4_SignApproveBindsTheInteractionsOwnContentHash: ADR-007 D7 spends the signed
// tuple's one content slot on the interaction content, so the bytes under the signature are
// the digest the card shipped -- decoded, not re-derived. A gateway that swaps the wire
// content_hash to point the approval at another card then breaks the signature.
func TestIsLife4_SignApproveBindsTheInteractionsOwnContentHash(t *testing.T) {
	ks, err := crypto.NewFileKeyStore(t.TempDir())
	if err != nil {
		t.Fatalf("keystore: %v", err)
	}
	exp := time.Unix(1_700_000_100, 0)

	cmd, err := SignApprove(ks, ApproveInput{
		Machine:     "machine1",
		Session:     "machine1/sess1",
		OperationID: "op-approve-1",
		ExpiresAt:   exp,
		ContentHash: approveDigest,
	})
	if err != nil {
		t.Fatalf("SignApprove: %v", err)
	}
	if cmd.Action != protocol.ActionApprove {
		t.Fatalf("Action = %q; want %q -- IS-LIFE-4 adds no new signed action", cmd.Action, protocol.ActionApprove)
	}
	want, _ := hex.DecodeString(approveDigest)
	if !bytes.Equal(cmd.ContentHash, want) {
		t.Fatalf("ContentHash = %x; want the item's own content_hash %x (IS-APR-2: echoed verbatim)", cmd.ContentHash, want)
	}
	if !cmd.ExpiresAt.Equal(exp) {
		t.Fatalf("ExpiresAt = %v; want %v", cmd.ExpiresAt, exp)
	}
	if cmd.Sig == "" {
		t.Fatal("SignApprove produced no signature; ADR-007 D4 admits no unsigned remote-class mutating op")
	}
}

// TestIsLife4_SignApproveRefusesAHashItWouldHaveToInvent: a content_hash that is not 32 bytes
// of hex cannot be echoed, and the phone may not substitute one. The refusal must be RETURNED
// -- a command signed over an empty hash is refused at the machine as a stale card, which
// reports the phone's own bug as the user's problem.
func TestIsLife4_SignApproveRefusesAHashItWouldHaveToInvent(t *testing.T) {
	ks, err := crypto.NewFileKeyStore(t.TempDir())
	if err != nil {
		t.Fatalf("keystore: %v", err)
	}
	for _, bad := range []string{"", "sha256:abc", "not-hex", approveDigest[:32], approveDigest + "00"} {
		if _, err := SignApprove(ks, ApproveInput{
			Machine: "machine1", Session: "machine1/s1", OperationID: "op-1",
			ExpiresAt: time.Now().Add(time.Minute), ContentHash: bad,
		}); err == nil {
			t.Errorf("SignApprove accepted content_hash %q; want a refusal (IS-APR-2 forbids computing one)", bad)
		}
	}
}

// TestIsLife4_SealApproveEnvelopeCarriesTheBodyBesideTheTuple: the gateway reconstructs
// Control.Approve from the in-envelope body, so it must survive the seal intact -- the
// interaction id, the agent instance, the echoed hash and expiry, and the chosen decision.
func TestIsLife4_SealApproveEnvelopeCarriesTheBodyBesideTheTuple(t *testing.T) {
	ks, err := crypto.NewFileKeyStore(t.TempDir())
	if err != nil {
		t.Fatalf("keystore: %v", err)
	}
	key := testContentKey()
	exp := time.Unix(1_700_000_400, 0).UTC()

	cmd, err := SignApprove(ks, ApproveInput{
		Machine: "machine1", Session: "machine1/sess1", OperationID: "op-approve-2",
		ExpiresAt: time.Now().Add(time.Minute), ContentHash: approveDigest,
	})
	if err != nil {
		t.Fatalf("SignApprove: %v", err)
	}
	body := schema.ApproveReq{
		Session:       "machine1/sess1",
		AgentInstance: schema.AgentInstanceRef{ShimPID: 4242, ShimStartTime: 1_700_000_000},
		InteractionID: "itm_01JAPPROVE",
		ContentHash:   approveDigest,
		ExpiresAt:     &exp,
		Decision:      "acceptWithExecpolicyAmendment",
	}
	env, err := SealApproveEnvelope(key, 7, 3, cmd, body)
	if err != nil {
		t.Fatalf("SealApproveEnvelope: %v", err)
	}

	got, err := remotegw.OpenRemoteCommand(key, env)
	if err != nil {
		t.Fatalf("gateway open: %v", err)
	}
	if got.Action != protocol.ActionApprove || got.Sig != cmd.Sig {
		t.Fatalf("opened tuple = %+v; want the signed approve tuple verbatim", got.DeviceCommandAuth)
	}
	if got.Approve == nil {
		t.Fatal("the sealed approve carried no body; the gateway has nothing to build Control.Approve from")
	}
	// Field-wise, so a failure names which one moved.
	if got.Approve.Session != body.Session {
		t.Errorf("session = %q, want %q", got.Approve.Session, body.Session)
	}
	if got.Approve.InteractionID != body.InteractionID {
		t.Errorf("interaction_id = %q, want %q", got.Approve.InteractionID, body.InteractionID)
	}
	if got.Approve.ContentHash != body.ContentHash {
		t.Errorf("content_hash = %q, want %q", got.Approve.ContentHash, body.ContentHash)
	}
	if got.Approve.Decision != body.Decision {
		t.Errorf("decision = %q, want %q", got.Approve.Decision, body.Decision)
	}
	if got.Approve.AgentInstance != body.AgentInstance {
		t.Errorf("agent_instance = %+v, want %+v", got.Approve.AgentInstance, body.AgentInstance)
	}
	if got.Approve.ExpiresAt == nil || !got.Approve.ExpiresAt.Equal(exp) {
		t.Errorf("expires_at = %v, want %v echoed verbatim", got.Approve.ExpiresAt, exp)
	}
}

// ---- the binding tuple the phone must read off the card it holds --------------------

// bindableApproval is an approval_request in the shape the daemon actually ships it: the three
// §3.5 daemon-authoritative fields present and well-formed.
func bindableApproval(id string) string {
	return `{"v":1,"item_id":"` + id + `","ts":"2026-08-07T10:00:00Z","kind":"approval_request",` +
		`"status":"in_progress","summary":"write src/main.rs",` +
		`"agent_instance":{"shim_pid":4242,"shim_start_time":1700000000},` +
		`"content_hash":"` + approveDigest + `","expires_at":"2026-08-07T10:05:00Z","mode":"card",` +
		`"decisions":[{"id":"accept","label":"Allow"},{"id":"cancel","label":"Deny"}]}`
}

// TestIsLife4_TheBindingTupleIsReadOffTheCardTheHandsetHolds: the facade's Approve verb takes
// three flat strings (gomobile binds no struct argument), so the tuple it echoes has to come
// from the item the phone already stored. This is the read that supplies it, and it must
// answer only for a card that is genuinely still pending.
func TestIsLife4_TheBindingTupleIsReadOffTheCardTheHandsetHolds(t *testing.T) {
	s := NewItemStore()
	s.applyAll([]schema.JournalRecord{{
		Cursor: 5, SessionID: "m1/s1", Type: RecordTypeInteraction,
		Item: json.RawMessage(bindableApproval("itm_a")),
	}})

	b, ok := s.PendingApproval("m1/s1", "itm_a")
	if !ok {
		t.Fatal("PendingApproval found no binding for a card the store holds unresolved")
	}
	if b.ContentHash != approveDigest {
		t.Errorf("content_hash = %q, want the item's own %q verbatim", b.ContentHash, approveDigest)
	}
	if b.ShimPID != 4242 || b.ShimStartTime != 1_700_000_000 {
		t.Errorf("agent instance = {%d,%d}, want {4242,1700000000} (ADR-007 D7's instance binding)", b.ShimPID, b.ShimStartTime)
	}
	want, _ := time.Parse(time.RFC3339, "2026-08-07T10:05:00Z")
	if b.ExpiresAt.IsZero() || !b.ExpiresAt.Equal(want) {
		t.Errorf("expires_at = %v, want %v echoed verbatim (a phone countdown is display-only)", b.ExpiresAt, want)
	}

	if _, ok := s.PendingApproval("m1/s1", "itm_missing"); ok {
		t.Error("PendingApproval answered for an item the store does not hold")
	}
	if _, ok := s.PendingApproval("m1/other", "itm_a"); ok {
		t.Error("PendingApproval answered across sessions; the key is (session, item_id)")
	}
}

// TestIsLife4_AResolvedCardYieldsNoBinding: IS-LIFE-2 gives every request exactly one
// resolution, and once it lands the card is no longer answerable on any surface. Answering
// from a resolved card would seal a command the daemon can only refuse, and would do it from
// a screen that has already stopped showing the card.
func TestIsLife4_AResolvedCardYieldsNoBinding(t *testing.T) {
	s := NewItemStore()
	s.applyAll([]schema.JournalRecord{
		{Cursor: 5, SessionID: "m1/s1", Type: RecordTypeInteraction, Item: json.RawMessage(bindableApproval("itm_a"))},
		{Cursor: 6, SessionID: "m1/s1", Type: RecordTypeInteraction, Item: json.RawMessage(approvalResolved("itm_r", "itm_a", "allowed"))},
	})
	if _, ok := s.PendingApproval("m1/s1", "itm_a"); ok {
		t.Error("PendingApproval still answers for a card that reached its approval_resolved (IS-LIFE-2)")
	}
}

// TestIsLife4_AnUnbindableCardYieldsNoBinding: a card whose §3.5 fields are absent or
// malformed cannot be answered, and the phone must say so rather than send a tuple it made
// up. The daemon would refuse it as stale, which reports a broken card as an out-of-date one.
func TestIsLife4_AnUnbindableCardYieldsNoBinding(t *testing.T) {
	s := NewItemStore()
	// approvalRequest() is the fold fixture: it carries "content_hash":"sha256:abc" and no
	// agent_instance, which is a card no approve can be authored from.
	s.applyAll([]schema.JournalRecord{{
		Cursor: 5, SessionID: "m1/s1", Type: RecordTypeInteraction, Item: json.RawMessage(approvalRequest("itm_b")),
	}})
	if _, ok := s.PendingApproval("m1/s1", "itm_b"); ok {
		t.Error("PendingApproval answered for a card carrying no usable binding tuple")
	}
}
