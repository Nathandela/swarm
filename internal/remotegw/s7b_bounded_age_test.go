// FAILING-FIRST (TDD RED, GG-5) tests for PB-GW-2: the gateway's inbound bounded-age
// check, an age backstop on phone -> machine sealed frames even if the durable replay
// high-water is lost. Binding bound: 10 minutes (§6.0).
//
// THE CONTRACT these tests freeze (undefined symbols -> compile-fail RED):
//   - const InboundMaxAge = 10 * time.Minute -- PB-GW-2's bound from §6.0, named so the
//     budget is not left to implementer discretion.
//   - func OpenMailboxFrameAt(recv *crypto.MailboxReceiver, key crypto.ContentKey,
//     raw []byte, now time.Time) (MailboxFrame, error): OpenMailboxFrame with the clock
//     injected. A bounded-age check is a clock comparison; without a seam the boundary
//     cannot be tested except by waiting ten minutes.
//   - OpenMailboxFrame keeps its EXACT current signature and delegates with time.Now(),
//     so the check is on by default at the production choke point
//     (CommandBridge.handle, command_loop.go:277) and no cross-package caller
//     (phonecore, mobile/conformance) has to change.
//
// WHY THE SEAM IS HERE AND NOT ON crypto.MailboxReceiver. PB-GW-2 is worded as
// "the inbound receiver enables the bounded-age check, which NewMailboxReceiver leaves
// at maxAge == 0". That cannot be done as written: MailboxReceiver.maxAge and .now are
// unexported, NewMailboxReceiver takes no arguments, no setter exists
// (crypto/envelope.go:211-231), and internal/remote/crypto is FROZEN. So these tests pin
// the PROPERTY at the gateway seam, not the field. Two implementations satisfy them:
// (A) an ADR adds a max-age constructor to crypto and the gateway uses it; (B) the
// gateway authenticates the envelope itself (crypto.OpenMailbox, which does not touch
// the receiver), applies the bound, and only then calls recv.Accept. Every assertion
// below is written to hold under BOTH, and the two places where their error precedence
// differs are deliberately avoided -- see the notes on the forgery and layering tests.
//
// PB-GW-6 IS CLOSED. Every phone -> machine seal stamps IssuedAt from the wall clock
// (phonecore/input.go:168,180 and command.go:104,126,149; landed in S7, commit 0ac4fb9),
// and every real producer -- phonesim (phonesim.go:310-450) and the Android binding
// (mobile/commands.go:161-300) -- goes through those five functions. The trap that
// would have rejected every legitimate keystroke as ~56 years old is therefore gone.
// The other half of that proof is cross-package and lives in
// internal/phonecore/s7b_gateway_live_traffic_test.go: real phone seals, opened through
// the real gateway entry point, with the bound enabled.
//
// KNOWN BLAST RADIUS, MEASURED. Enabling the bound turns 29 existing tests across 13
// files in this package red, every one with the same cause: their sealing helpers
// (sealedCmd command_loop_test.go:88, sealRemoteCmd + sealInputEnv mailbox_route_test.go:78,
// and the equivalents in inbound_state_test.go:143 and launch_loop_test.go:29) leave
// IssuedAt unset, so those fixtures are ~56 years old. They are fixtures that lag the real
// producer, not assertions: stamp IssuedAt in the four header literals and every assertion
// stands unchanged. internal/phonecore, internal/skeleton, internal/phonesim and mobile
// were measured GREEN under an enabled bound -- they all seal through phonecore's five
// stamped functions, which is PB-GW-6 doing its job.
//
// ADVERSARY MODEL. The relay is untrusted: it may retain, reorder, delay and replay
// sealed frames, but IssuedAt is AAD-covered, so it cannot forge or shift a timestamp
// without breaking the AEAD. The scenario PB-GW-2 exists for is §4.6's: the per-(sender,
// epoch) replay high-water is gone (a fresh receiver has seen == false and SKIPS the
// staleness check entirely, crypto/envelope.go:255), so the seq guard is blind and the
// age bound is the only thing left. Tests are written from that position, not from a
// cooperative-network one.
package remotegw

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// s7bEpoch is the epoch every frame in this file is sealed under.
const s7bEpoch = 1

// s7bStream is the (sender, epoch) coordinate the gateway's replay high-water is keyed
// by for phone -> machine frames. Phone seals leave SenderKeyID at its zero value
// (phonecore never sets it), so the sender half is the zero key id.
var s7bStream = InboundStream{Sender: [8]byte{}, Epoch: s7bEpoch}

// s7bNow is the fixed instant the injected-clock tests compare against. It is
// millisecond-aligned on purpose: IssuedAt is carried in unix milliseconds
// (crypto/envelope.go:55), so an unaligned base would make "exactly at the bound"
// land a fraction of a millisecond over it and turn the boundary test into a coin flip.
// The value itself is arbitrary.
func s7bNow() time.Time { return time.UnixMilli(1_784_000_000_000) }

func s7bKey() crypto.ContentKey {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 7)
	}
	return key
}

// s7bSealInputAt seals a keystroke frame with an EXPLICIT IssuedAt, which is what lets a
// test place a frame anywhere on the age axis without waiting. The plaintext shape is the
// one phonecore.SealInputData emits; only the timestamp is under test control. Sealing
// under a key the opener does not hold yields a well-formed header with an invalid AEAD
// tag -- a relay forgery.
func s7bSealInputAt(t *testing.T, key crypto.ContentKey, seq uint64, issuedAt time.Time) []byte {
	t.Helper()
	plain, err := json.Marshal(inputFrameWire{T: "data", Session: "m/s1", Data: []byte("ls -la\r")})
	if err != nil {
		t.Fatalf("marshal input frame: %v", err)
	}
	env, err := crypto.SealMailbox(key, crypto.EnvelopeHeader{
		Version:  crypto.VersionV1,
		EpochID:  s7bEpoch,
		Seq:      seq,
		IssuedAt: issuedAt.UnixMilli(),
	}, plain)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	return env.Marshal()
}

// TestS7bInboundMaxAge_IsTheBudgetedTenMinutes pins §6.0's binding value. The budget table
// exists because round 2 objected that "a stated bound" lets an implementation pick
// anything; a bound of a few seconds would refuse legitimate frames after any ordinary
// relay delay, and a bound of days would refuse nothing a retaining relay could do.
func TestS7bInboundMaxAge_IsTheBudgetedTenMinutes(t *testing.T) {
	if InboundMaxAge != 10*time.Minute {
		t.Fatalf("InboundMaxAge = %v, want 10m (§6.0, PB-GW-2)", InboundMaxAge)
	}
}

// TestS7bBoundedAge_RefusesARetainedFrameWhenTheHighWaterIsLost is PB-GW-2's reason to
// exist, in the exact condition it is a backstop for: the durable replay high-water is
// gone, so the receiver has seen == false for the stream and its seq guard passes
// everything. A relay that retained an 11-minute-old keystroke re-injects it; only the
// age bound can refuse it.
//
// The second half is the mutation that must break the first: the SAME frame, same seq,
// same blind receiver, differing ONLY in IssuedAt, is accepted. Without it a bound that
// refused every frame -- the PB-GW-6 brick -- would pass the assertion above.
func TestS7bBoundedAge_RefusesARetainedFrameWhenTheHighWaterIsLost(t *testing.T) {
	key := s7bKey()
	now := s7bNow()

	// The relay retained this frame for eleven minutes before re-injecting it.
	retained := s7bSealInputAt(t, key, 61, now.Add(-11*time.Minute))
	blind := crypto.NewMailboxReceiver() // high-water lost: seen == false, seq guard blind
	if _, err := OpenMailboxFrameAt(blind, key, retained, now); !errors.Is(err, crypto.ErrStaleAge) {
		t.Fatalf("retained 11m-old frame on a blind receiver: err = %v, want crypto.ErrStaleAge -- the seq guard cannot refuse it (seen == false) and the age bound is the only backstop left (§4.6, PB-GW-2)", err)
	}

	// MUTATION CONTROL: identical in every respect except its age.
	fresh := s7bSealInputAt(t, key, 61, now.Add(-1*time.Minute))
	blind2 := crypto.NewMailboxReceiver()
	frame, err := OpenMailboxFrameAt(blind2, key, fresh, now)
	if err != nil {
		t.Fatalf("a 1m-old frame differing from the retained one ONLY in IssuedAt was refused: %v -- the bound rejects live traffic, which is the PB-GW-6 brick, and the assertion above proves nothing", err)
	}
	if frame.Kind != FrameInput || string(frame.Input.Data) != "ls -la\r" {
		t.Fatalf("accepted frame = %+v, want the keystroke input frame", frame)
	}
}

// TestS7bBoundedAge_Boundary pins the three points that decide whether the comparison is
// the right one: just inside, exactly at, and just outside. "Exactly at the bound is
// accepted" is the strict-inequality semantics crypto.MailboxReceiver.Accept already uses
// (envelope.go:263, `> r.maxAge`), pinned here so an implementation that adds the check
// to crypto behind an ADR and one that applies it at the gateway seam agree at the edge.
//
// Each case gets its own receiver: this test is about the age axis, and a shared receiver
// would let the seq guard decide the outcome instead.
func TestS7bBoundedAge_Boundary(t *testing.T) {
	key := s7bKey()
	now := s7bNow()

	cases := []struct {
		name    string
		age     time.Duration
		wantErr error // nil => accepted
	}{
		{"one ms inside the bound", InboundMaxAge - time.Millisecond, nil},
		{"exactly at the bound", InboundMaxAge, nil},
		{"one ms outside the bound", InboundMaxAge + time.Millisecond, crypto.ErrStaleAge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := s7bSealInputAt(t, key, 7, now.Add(-tc.age))
			_, err := OpenMailboxFrameAt(crypto.NewMailboxReceiver(), key, raw, now)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("age %v (bound %v): err = %v, want accepted", tc.age, InboundMaxAge, err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("age %v (bound %v): err = %v, want %v", tc.age, InboundMaxAge, err, tc.wantErr)
			}
		})
	}
}

// TestS7bBoundedAge_RejectionDoesNotPoisonTheStream is the assertion that keeps the cure
// from being worse than the disease. crypto.MailboxReceiver.Accept advances the
// per-(sender, epoch) high-water on the frames it accepts; if a frame refused for age
// advanced it too, a relay could inject ONE retained frame carrying an absurdly high seq
// and permanently silence the real phone, whose next legitimate seq is far below it. That
// converts a failed replay into an unrecoverable denial of typing -- strictly worse than
// the replay PB-GW-2 prevents.
func TestS7bBoundedAge_RejectionDoesNotPoisonTheStream(t *testing.T) {
	key := s7bKey()
	now := s7bNow()
	recv := crypto.NewMailboxReceiver()

	poison := s7bSealInputAt(t, key, 5000, now.Add(-11*time.Minute))
	if _, err := OpenMailboxFrameAt(recv, key, poison, now); !errors.Is(err, crypto.ErrStaleAge) {
		t.Fatalf("retained seq-5000 frame: err = %v, want crypto.ErrStaleAge", err)
	}

	// The real phone's next keystroke, seq 6, on the SAME receiver.
	live := s7bSealInputAt(t, key, 6, now)
	if _, err := OpenMailboxFrameAt(recv, key, live, now); err != nil {
		t.Fatalf("the phone's live seq-6 keystroke was refused with %v after a stale-age frame at seq 5000 -- the age rejection advanced the replay high-water, so one retained frame from the relay permanently bricks typing", err)
	}
}

// TestS7bBoundedAge_DoesNotReplaceTheSeqReplayGuard holds both lines of defence at once.
// PB-GW-2 is a backstop for a LOST high-water, not a substitute for the high-water; an
// implementation that somehow satisfied the age assertions while weakening the seq guard
// would be a regression against PB-GW-1 and §4.6.
//
// Frames that are BOTH stale-seq and stale-age are deliberately absent: crypto checks seq
// before age (envelope.go:255-264) while a gateway-seam check runs age first, so the two
// legitimate implementations disagree on which error wins that overlap. Pinning it would
// pick an implementation rather than a property.
func TestS7bBoundedAge_DoesNotReplaceTheSeqReplayGuard(t *testing.T) {
	key := s7bKey()
	now := s7bNow()
	recv := crypto.NewMailboxReceiver()
	recv.SeedHighWater(s7bStream.Sender, s7bStream.Epoch, 10) // durable high-water restored (PB-GW-1)

	// (a) A perfectly FRESH frame at an already-consumed seq is still a replay.
	replayed := s7bSealInputAt(t, key, 10, now)
	if _, err := OpenMailboxFrameAt(recv, key, replayed, now); !errors.Is(err, crypto.ErrStaleSeq) {
		t.Fatalf("fresh-but-replayed seq 10 against a high-water of 10: err = %v, want crypto.ErrStaleSeq -- enabling the age check must not loosen the seq guard", err)
	}

	// (b) A frame the seq guard alone WOULD accept (seq 11 > high-water 10) but whose
	// authenticated age is outside the bound is refused. This is the seq guard's blind
	// spot -- a retaining relay that also holds a seq-fresh frame -- and it is the case
	// the age check adds.
	staleAge := s7bSealInputAt(t, key, 11, now.Add(-11*time.Minute))
	if _, err := OpenMailboxFrameAt(recv, key, staleAge, now); !errors.Is(err, crypto.ErrStaleAge) {
		t.Fatalf("seq-fresh (11 > 10) but 11m-old frame: err = %v, want crypto.ErrStaleAge -- the seq guard accepts this one, so only the age bound refuses it", err)
	}

	// (c) Neither refusal consumed seq 11: the phone's real frame at that seq still lands.
	live := s7bSealInputAt(t, key, 11, now)
	if _, err := OpenMailboxFrameAt(recv, key, live, now); err != nil {
		t.Fatalf("the phone's live seq-11 frame was refused with %v after the refusals above; both guards must refuse WITHOUT consuming the seq", err)
	}

	// (d) And the seq guard still closes behind it: a replay of that exact envelope,
	// still well inside the age bound, is refused.
	if _, err := OpenMailboxFrameAt(recv, key, live, now); !errors.Is(err, crypto.ErrStaleSeq) {
		t.Fatalf("immediate replay of the accepted seq-11 envelope: err = %v, want crypto.ErrStaleSeq", err)
	}
}

// TestS7bBoundedAge_ToleratesTheSkewBudgetInBothDirections. The bound compares two
// devices' clocks, so it is only safe if it clears §6.0's skew budget (±30 s, PB-TIME-1)
// with room for the untrusted relay's own queueing delay on top. A check that bricked
// typing whenever a handset drifted would be a worse failure than the replay it prevents,
// and drift is certain on a real phone.
//
// The far-future case pins the comparison as ONE-SIDED on purpose. crypto's arithmetic is
// now.Sub(issued) > maxAge (envelope.go:263), which is negative for a future stamp and
// therefore never trips. That is correct here and must not be "improved" into a symmetric
// window: IssuedAt is AAD-covered, so a relay cannot forge a future timestamp -- only the
// phone's own fast clock produces one -- and a symmetric bound would refuse that phone's
// live traffic while adding nothing against a retaining relay, which can only make frames
// OLDER. Replay by a fast-clocked phone is the seq guard's job, not this check's.
func TestS7bBoundedAge_ToleratesTheSkewBudgetInBothDirections(t *testing.T) {
	key := s7bKey()
	now := s7bNow()

	cases := []struct {
		name   string
		offset time.Duration // IssuedAt relative to the machine's now
	}{
		{"phone 30s behind the machine (§6.0 skew budget)", -30 * time.Second},
		{"phone 30s ahead of the machine (§6.0 skew budget)", 30 * time.Second},
		{"phone 30s behind plus a 60s relay delivery delay", -90 * time.Second},
		{"phone an hour ahead: the check is one-sided by design", time.Hour},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := s7bSealInputAt(t, key, 3, now.Add(tc.offset))
			if _, err := OpenMailboxFrameAt(crypto.NewMailboxReceiver(), key, raw, now); err != nil {
				t.Fatalf("IssuedAt offset %v: err = %v, want accepted -- the %v bound must clear the ±30s skew budget and ordinary relay delay, and must not bound the future", tc.offset, err, InboundMaxAge)
			}
		})
	}
}

// TestS7bBoundedAge_AForgeryIsRefusedAsAForgeryNotAsStale keeps the check honest about the
// word PB-GW-2 uses: "an AUTHENTICATED envelope older than the bound". Judging age from a
// header the AEAD has not yet vouched for means deciding on bytes the untrusted relay
// controls. The frame here carries a fresh seq (so the seq guard has no opinion) and a
// stale IssuedAt, and its tag does not verify.
//
// Both legitimate implementations pass: crypto authenticates before it compares ages
// (envelope.go:259-264), and a gateway-seam check must call crypto.OpenMailbox -- which
// does not touch the receiver -- before applying the bound. Only a seam check placed
// straight after ParseEnvelope fails, which is exactly the ordering this forbids.
func TestS7bBoundedAge_AForgeryIsRefusedAsAForgeryNotAsStale(t *testing.T) {
	key := s7bKey()
	var otherKey crypto.ContentKey
	for i := range otherKey {
		otherKey[i] = byte(i + 200)
	}
	now := s7bNow()

	forged := s7bSealInputAt(t, otherKey, 61, now.Add(-11*time.Minute))
	_, err := OpenMailboxFrameAt(crypto.NewMailboxReceiver(), key, forged, now)
	if err == nil {
		t.Fatal("a frame sealed under the wrong content key was accepted; the AEAD must refuse it")
	}
	if errors.Is(err, crypto.ErrStaleAge) {
		t.Fatalf("a frame whose AEAD does not verify was refused as crypto.ErrStaleAge: the bound was applied to an UNAUTHENTICATED IssuedAt, i.e. to bytes the untrusted relay supplied. Authenticate first (crypto.OpenMailbox does not touch the receiver), then compare ages")
	}
}

// TestS7bBoundedAge_IsEnforcedOnTheProductionBridgePath proves the bound is wired where
// production actually reads -- CommandBridge.handle -> OpenMailboxFrame
// (command_loop.go:277) -- and not only in a helper a test calls directly. It runs on the
// REAL clock through the unseamed entry point, so it also pins that OpenMailboxFrame
// delegates with time.Now() rather than leaving the check off by default.
//
// The relay serves two items to a bridge whose high-water is blind: a retained 11-minute-
// old keystroke, then the phone's live one. The stale keystroke must not reach the lease
// plane, and -- the half that makes this honest -- the live one must.
func TestS7bBoundedAge_IsEnforcedOnTheProductionBridgePath(t *testing.T) {
	key := s7bKey()
	wall := time.Now()

	mb := &fakeMailbox{inbox: []relay.Item{
		{Cursor: 1, Envelope: s7bSealInputAt(t, key, 61, wall.Add(-11*time.Minute))},
		{Cursor: 2, Envelope: s7bSealInputAt(t, key, 62, wall)},
	}}
	leases := &fakeLeaseRouter{}
	b := NewCommandBridge(CommandBridgeConfig{
		Mailbox:     mb,
		Forwarder:   &fakeForwarder{},
		Leases:      leases,
		Key:         key,
		EpochID:     s7bEpoch,
		ReplyTarget: "phone-routing-id",
	})

	n, err := b.PollOnce(context.Background())
	if !errors.Is(err, crypto.ErrStaleAge) {
		t.Fatalf("PollOnce err = %v, want it to carry crypto.ErrStaleAge for the retained item -- a per-item refusal is surfaced, not swallowed", err)
	}
	if n != 1 {
		t.Fatalf("processed %d items, want 1: the retained frame refused, the live one processed", n)
	}
	if len(leases.inputs) != 1 {
		t.Fatalf("lease plane saw %d input frames, want exactly 1 (the live keystroke); a retained 11m-old keystroke reached the PTY", len(leases.inputs))
	}
	if got := string(leases.inputs[0].frame.Data); got != "ls -la\r" {
		t.Fatalf("routed keystroke = %q, want the live frame's payload", got)
	}
}
