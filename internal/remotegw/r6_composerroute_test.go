package remotegw

// FAILING-FIRST (TDD RED, GG-5) for Wave R6's gateway hop of the REAL composer_send --
// Mirror M2.4, IS-LIFE-5. Bead: agents-tracker-hggx.7. RED is undefined-only:
// protocol.RemoteCommand has no ComposerSend field yet.
//
// THE CONTRACT: the R1 arm already forwards the composer_send ACTION by name; what it
// cannot yet do is carry the body. This file freezes:
//
//   - protocol.RemoteCommand.ComposerSend *protocol.ComposerSendReq (wire name
//     `composer_send`): the IS-LIFE-5 body riding the sealed command envelope.
//   - opForAction's body gate, inherited from launch/approve/session_launch verbatim: a
//     composer_send whose body was stripped in transit is REFUSED at the gateway, never
//     forwarded bodyless -- a zero body would reach the daemon as a send naming no text
//     and surface to the user as some other refusal for a frame that merely lost its
//     payload.
//   - ForwardCommand copies the body through UNTOUCHED (the gateway is a blind conduit;
//     content integrity is the phone signature's job via ComposerSendContentHash, not
//     the gateway's).
//   - turn_interrupt keeps riding the existing bodyless arm -- pinned here so nobody
//     "helpfully" grows it a body gate it must not have.

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// r6Forwarder records every forwarded command, composer bodies included.
type r6Forwarder struct {
	mu        sync.Mutex
	ops       []string
	seen      []protocol.DeviceCommandAuth
	composers []*protocol.ComposerSendReq
}

func (f *r6Forwarder) ForwardCommand(op string, rc protocol.RemoteCommand) (protocol.Control, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ops = append(f.ops, op)
	f.seen = append(f.seen, rc.DeviceCommandAuth)
	f.composers = append(f.composers, rc.ComposerSend)
	return protocol.Control{Op: op, SessionID: rc.Session, OperationID: rc.OperationID}, nil
}

// sealedComposerCmd seals a composer_send RemoteCommand the way the phone core does.
func sealedComposerCmd(t *testing.T, key crypto.ContentKey, seq uint64, opID string, body *protocol.ComposerSendReq) []byte {
	t.Helper()
	session := ""
	if body != nil {
		session = body.Session
	}
	rc := protocol.RemoteCommand{
		DeviceCommandAuth: protocol.DeviceCommandAuth{
			DeviceID:    "d1",
			Action:      protocol.ActionComposerSend,
			Machine:     "m",
			Session:     session,
			OperationID: opID,
			ExpiresAt:   time.Now().Add(time.Minute),
			Sig:         "device-signature",
		},
		BodyVersion:  1,
		ComposerSend: body,
	}
	plain, err := json.Marshal(rc)
	if err != nil {
		t.Fatalf("marshal composer command: %v", err)
	}
	env, err := crypto.SealMailbox(key, crypto.EnvelopeHeader{
		Version: crypto.VersionV1, EpochID: 1, Seq: seq, IssuedAt: time.Now().UnixMilli(),
	}, plain)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	return env.Marshal()
}

func r6Bridge(t *testing.T, key crypto.ContentKey, fwd CommandForwarder, envs ...[]byte) (*CommandBridge, *fakeMailbox) {
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

// TestR6ComposerRoute_TheGatewayCarriesTheBodyToTheDaemonUntouched: forwarded as
// protocol.OpComposerSend, signature verbatim, body verbatim, reply sealed back.
func TestR6ComposerRoute_TheGatewayCarriesTheBodyToTheDaemonUntouched(t *testing.T) {
	key := testContentKey()
	fwd := &r6Forwarder{}
	body := &protocol.ComposerSendReq{Session: "m/s1", ExpectedTurn: "01JTURN", Text: "run the linter"}
	b, mb := r6Bridge(t, key, fwd, sealedComposerCmd(t, key, 1, "op-comp-1", body))

	if _, err := b.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	fwd.mu.Lock()
	defer fwd.mu.Unlock()
	if len(fwd.ops) != 1 || fwd.ops[0] != protocol.OpComposerSend {
		t.Fatalf("forwarded ops = %v, want exactly one %q", fwd.ops, protocol.OpComposerSend)
	}
	if len(fwd.seen) != 1 || fwd.seen[0].Sig != "device-signature" || fwd.seen[0].Action != protocol.ActionComposerSend {
		t.Fatalf("forwarded auth tuple = %+v, want the phone's tuple verbatim (blind conduit)", fwd.seen)
	}
	if len(fwd.composers) != 1 || fwd.composers[0] == nil {
		t.Fatalf("forwarded composer bodies = %+v, want the body carried in-envelope", fwd.composers)
	}
	if got := *fwd.composers[0]; got != *body {
		t.Errorf("forwarded composer body = %+v, want %+v verbatim: an altered expected_turn or "+
			"text here would anyway fail the phone's ComposerSendContentHash binding daemon-side, "+
			"but the gateway must not be the first place that finds out", got, *body)
	}
	if len(mb.replies) != 1 {
		t.Fatalf("sealed replies = %d, want 1: an unanswered send leaves the composer's pending "+
			"state in flight forever (ADR-009 (6): visible pending -> sent -> refused)", len(mb.replies))
	}
}

// TestR6ComposerRoute_AStrippedBodyIsRefusedLoudlyNeverForwardedBodyless is
// launch/approve/session_launch's rule inherited by the fourth body-carrying action.
func TestR6ComposerRoute_AStrippedBodyIsRefusedLoudlyNeverForwardedBodyless(t *testing.T) {
	key := testContentKey()
	fwd := &r6Forwarder{}
	b, _ := r6Bridge(t, key, fwd, sealedComposerCmd(t, key, 1, "op-comp-2", nil))

	if _, err := b.PollOnce(context.Background()); err == nil {
		t.Fatalf("PollOnce accepted a bodyless composer_send; want a loud refusal")
	}
	fwd.mu.Lock()
	defer fwd.mu.Unlock()
	if len(fwd.ops) != 0 {
		t.Fatalf("a bodyless composer_send was forwarded as %v; want nothing forwarded", fwd.ops)
	}
}

// TestR6ComposerRoute_TurnInterruptCarriesItsTurnAndForwards pins the neighbour arm.
//
// SUPERSESSION, EXECUTED (Wave R6 review fix-pack, finding B7; recorded in
// docs/verification/r6-red/chat-red.txt §B7). This test was
// TestR6ComposerRoute_TurnInterruptStaysBodylessAndForwards and asserted the opposite: "the
// interrupt op carries no body by design (the signed tuple's session IS its whole subject),
// so the body gate must not grow to cover it."
//
// THAT PREMISE WAS DISPROVED BY PROBE, not by preference. With turnA superseded by turnB, a
// composer_send rendered against turnA was refused stale_turn while an interrupt rendered
// against the SAME turnA returned nil and typed the cancel sequence into turnB. The signed
// tuple's session is the AUTHORIZATION subject; the turn is the OPERATIONAL subject, and the
// op had none. In playbook §8.1 turnB is the turn the owner just started at the terminal, and
// internal/adapter/claude/interaction.go's own note records that the cancel key at an idle
// prompt CLEARS the composer -- so a late phone Stop wipes the terminal user's half-typed
// line. The orchestrator ruled the turn is bound.
//
// WHAT IS KEPT, UNCHANGED: the interrupt FORWARDS and is never gateway-locally refused when
// well-formed, which is this test's whole load-bearing assertion. What changed is only what
// "well-formed" means. The stripped-body refusal it now implies is separately pinned by
// r6fix_forwardassembly_test.go.
func TestR6ComposerRoute_TurnInterruptCarriesItsTurnAndForwards(t *testing.T) {
	rc := protocol.RemoteCommand{
		DeviceCommandAuth: protocol.DeviceCommandAuth{
			Action: protocol.ActionTurnInterrupt, Session: "m/s1", OperationID: "op-int-1",
		},
		TurnInterrupt: &protocol.TurnInterruptReq{Session: "m/s1", ExpectedTurn: "01JTURN"},
	}
	op, err := opForAction(rc)
	if err != nil {
		t.Fatalf("opForAction(turn_interrupt) = %v; a well-formed interrupt must keep forwarding", err)
	}
	if op != protocol.OpTurnInterrupt {
		t.Fatalf("opForAction(turn_interrupt) = %q, want %q", op, protocol.OpTurnInterrupt)
	}
}
