package remotegw

// FAILING-FIRST (TDD RED, GG-5) tests for IS-LIFE-4's gateway hop.
//
// opForAction has an arm for every signed action the phone can send EXCEPT approve, whose
// comment says so in as many words: "approve is not a daemon remote op (D6/D7)". That was
// true while no daemon op existed. It does now (protocol.OpApprove), and until this route
// lands a correctly-signed approve falls through opForAction's default and is refused
// "unsupported command action" one hop short of the daemon -- WITH NO REPLY SEALED, so the
// card can never resolve either. It is the exact defect S18 found for device_revoke.
//
// The body is the part that makes this more than a table row. Unlike kill and delete, an
// approve carries a payload the daemon validates against (agent_instance, interaction_id,
// content_hash, expires_at, decision), so the route must carry it in-envelope to
// Control.Approve -- and must refuse loudly when it is missing, exactly as launch refuses a
// command with no spec. A stripped body forwarded as a zero ApproveReq would be an approve
// naming no interaction, which the daemon refuses as stale: the same outcome, reported as the
// user's card being out of date rather than as a frame that lost its payload.
//
// RED is undefined-only: protocol.RemoteCommand has no Approve field and fakeForwarder does
// not record one.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// sealedApproveCmd seals an approve RemoteCommand the way the phone core does: the signed
// tuple plus the ApproveReq body it is bound to.
func sealedApproveCmd(t *testing.T, key crypto.ContentKey, seq uint64, opID string, body *protocol.ApproveReq) []byte {
	t.Helper()
	session := ""
	if body != nil {
		session = body.Session
	}
	rc := protocol.RemoteCommand{
		DeviceCommandAuth: protocol.DeviceCommandAuth{
			DeviceID:    "d1",
			Action:      protocol.ActionApprove,
			Machine:     "m",
			Session:     session,
			OperationID: opID,
			ExpiresAt:   time.Now().Add(time.Minute),
			Sig:         "device-signature",
		},
		Approve: body,
	}
	plain, err := json.Marshal(rc)
	if err != nil {
		t.Fatalf("marshal approve command: %v", err)
	}
	env, err := crypto.SealMailbox(key, crypto.EnvelopeHeader{
		Version: crypto.VersionV1, EpochID: 1, Seq: seq, IssuedAt: time.Now().UnixMilli(),
	}, plain)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	return env.Marshal()
}

// approveBody is one well-formed ApproveReq against session.
func approveBody(session string) *protocol.ApproveReq {
	exp := time.Now().Add(2 * time.Minute)
	return &protocol.ApproveReq{
		Session:       session,
		AgentInstance: protocol.AgentInstanceRef{ShimPID: 4242, ShimStartTime: 1700000000},
		InteractionID: "itm_01JAPPROVE",
		ContentHash:   "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
		ExpiresAt:     &exp,
		Decision:      "acceptWithExecpolicyAmendment",
	}
}

// approveBridge assembles a CommandBridge over an inbox holding exactly the given envelopes.
func approveBridge(t *testing.T, key crypto.ContentKey, fwd CommandForwarder, envs ...[]byte) (*CommandBridge, *fakeMailbox) {
	t.Helper()
	mb := &fakeMailbox{}
	for i, e := range envs {
		mb.inbox = append(mb.inbox, relay.Item{Cursor: uint64(i + 1), Envelope: e})
	}
	b := NewCommandBridge(CommandBridgeConfig{
		Mailbox:     mb,
		Forwarder:   fwd,
		Key:         key,
		EpochID:     1,
		ReplyTarget: "phone-routing-id",
	})
	return b, mb
}

// TestIsLife4_TheGatewayRoutesAnApproveToTheDaemon: a sealed, signed approve is forwarded as
// protocol.OpApprove with its device signature untouched and its ApproveReq body carried
// in-envelope, and the daemon's reply is sealed back to the phone.
func TestIsLife4_TheGatewayRoutesAnApproveToTheDaemon(t *testing.T) {
	key := testContentKey()
	fwd := &fakeForwarder{}
	body := approveBody("m/s1")
	b, mb := approveBridge(t, key, fwd, sealedApproveCmd(t, key, 1, "op-approve-1", body))

	if _, err := b.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if len(fwd.ops) != 1 || fwd.ops[0] != protocol.OpApprove {
		t.Fatalf("forwarded ops = %v, want exactly one %q; opForAction refuses an approve one hop short of the daemon until this arm exists",
			fwd.ops, protocol.OpApprove)
	}
	if len(fwd.seen) != 1 || fwd.seen[0].Sig != "device-signature" || fwd.seen[0].Action != protocol.ActionApprove {
		t.Fatalf("forwarded auth tuple = %+v, want the phone's approve tuple verbatim (the gateway is a blind conduit)", fwd.seen)
	}
	if len(fwd.approves) != 1 || fwd.approves[0] == nil {
		t.Fatalf("forwarded approve bodies = %+v, want the ApproveReq carried in-envelope to Control.Approve", fwd.approves)
	}
	got := *fwd.approves[0]
	if got.InteractionID != body.InteractionID || got.ContentHash != body.ContentHash || got.Decision != body.Decision {
		t.Errorf("forwarded approve body = %+v, want %+v verbatim", got, *body)
	}
	if got.AgentInstance != body.AgentInstance {
		t.Errorf("forwarded agent instance = %+v, want %+v (ADR-007 D7's instance binding)", got.AgentInstance, body.AgentInstance)
	}
	if got.ExpiresAt == nil || !got.ExpiresAt.Equal(*body.ExpiresAt) {
		t.Errorf("forwarded expires_at = %v, want the daemon-authoritative value echoed verbatim (IS-APR-2)", got.ExpiresAt)
	}
	if len(mb.replies) != 1 {
		t.Fatalf("sealed replies = %d, want 1: an unanswered approve leaves the card in flight forever", len(mb.replies))
	}
}

// TestIsLife4_AnApproveWithNoBodyIsRefusedLoudly: the ApproveReq is the whole of what the
// daemon validates against, so a command that lost it must fail the item -- never be forwarded
// as a zero body, which reaches the phone as "your card is stale" for a frame that was simply
// stripped. This is launch's "missing its launch spec in-envelope" rule for the one other
// action carrying a payload the daemon reads.
func TestIsLife4_AnApproveWithNoBodyIsRefusedLoudly(t *testing.T) {
	key := testContentKey()
	fwd := &fakeForwarder{}
	b, _ := approveBridge(t, key, fwd, sealedApproveCmd(t, key, 1, "op-approve-2", nil))

	if _, err := b.PollOnce(context.Background()); err == nil {
		t.Fatalf("PollOnce accepted a bodyless approve; want a loud refusal")
	}
	if len(fwd.ops) != 0 {
		t.Fatalf("a bodyless approve was forwarded as %v; want nothing forwarded", fwd.ops)
	}
}
