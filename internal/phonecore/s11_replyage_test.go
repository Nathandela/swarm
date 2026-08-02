package phonecore

// Slice S11 -- FAILING-FIRST (TDD RED, GG-5) tests for PB-TIME-2 in the machine -> phone
// direction: the mirror of PB-GW-6's trap, which the spec records as still open and
// requires closed BEFORE this slice.
//
// THE DEFECT, named. remotegw.SealControlReply (internal/remotegw/command_in.go:112-126)
// seals every command reply -- including PB-SYNC-7's lease confirmation and PB-INPUT-2's
// severance notice -- with EnvelopeHeader{Version, EpochID, Seq} and NO IssuedAt, while
// the journal/terminal/reconcile path on the same machine DOES stamp
// (relaysink.go:418-433). IssuedAt is AAD-covered, so an unset field is AUTHENTICATED AS
// ZERO: any bounded-age check on the phone's receiver computes an age of ~56 years for a
// command reply and refuses it. The phone would then lose exactly the frames it cannot do
// without -- the lease confirmation, so it could never type, and every op outcome, so
// nothing would ever resolve.
//
// This is the second instance of one defect. The first (PB-GW-6) would have rejected every
// legitimate keystroke on the way IN; S7 closed it by stamping at the five phone-side
// producers, and S7b then enabled the bound at the gateway. The residual recorded at
// docs/verification/remote-phaseB-progress.md:277-280 says the two halves of this one must
// land together "or the phone bricks on its own command replies".
//
// THE CONTRACT these tests freeze:
//
//	remotegw.SealControlReply stamps IssuedAt from the wall clock. Its SIGNATURE IS
//	UNCHANGED -- exactly as S7 stamped inside the five phone-side seal functions rather
//	than threading a clock through their callers.
//
//	const phonecore.InboundMaxAge = 10 * time.Minute            // the mirror of PB-GW-2's
//	func (*MailboxRouter) AcceptAt(raw []byte, now time.Time) (bool, error)
//	Accept and AcceptCommit enforce the bound on the real clock.
//
// WHY THE TESTS LIVE HERE AND USE THE REAL PRODUCER. remotegw's own tests hand-roll their
// sealed frames (mailbox_route_test.go notes why: remotegw must not import phonecore), so
// a test written there proves only that the gateway's test helper agrees with the
// gateway's test helper. The whole PB-GW-6 trap was that the real producer and the
// fixtures disagreed about IssuedAt. phonecore's tests already call into remotegw for
// exactly this reason (input_test.go:57, s7b_gateway_live_traffic_test.go), so the frames
// below are sealed by the SAME function production seals with.
//
// ONE-SIDEDNESS, checked rather than assumed. S7b pinned the gateway's inbound bound as
// one-sided (`now.Sub(issued) > maxAge`, never trips on a future stamp) because IssuedAt is
// AAD-covered -- a relay can only make a frame OLDER -- and a symmetric window would refuse
// a fast-clocked handset's live traffic while preventing nothing the seq guard does not
// already catch. Every clause of that reasoning transfers to this direction unchanged: the
// same AAD coverage, the same untrusted relay in the middle, the same seq guard behind it
// (MailboxRouter's shared crypto.MailboxReceiver). Only the roles swap -- it is now the
// MACHINE's clock that may run fast and the PHONE that must not refuse it. So this bound
// is one-sided too, and the far-future case below pins it. (The SKEW bound in
// s11_skew_test.go is symmetric, for reasons that are not these; see its header.)
//
// This file contains NO implementation.

import (
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remotegw"
)

// s11Epoch is the epoch every machine -> phone frame in this slice is sealed under.
const s11Epoch uint32 = 7

// s11SealReply seals ctrl exactly as the gateway does, through the REAL producer. Every
// assertion in this slice that involves a machine-sealed reply goes through here, so a
// producer that stops stamping IssuedAt fails them all rather than only the one test that
// remembered to look.
func s11SealReply(t *testing.T, key crypto.ContentKey, epoch uint32, seq uint64, ctrl schema.Control) []byte {
	t.Helper()
	raw, err := remotegw.SealControlReply(key, epoch, seq, ctrl)
	if err != nil {
		t.Fatalf("remotegw.SealControlReply: %v", err)
	}
	return raw
}

// s11ReplyFor is a minimal machine reply tagged with operationID.
func s11ReplyFor(operationID string) schema.Control {
	return schema.Control{Op: protocol.OpOK, SessionID: s11Session, OperationID: operationID}
}

// s11TamperIssuedAt rewrites the CLEARTEXT IssuedAt field of a marshalled envelope, the
// way an untrusted relay would. The header layout is fixed (crypto/envelope.go:22-24):
// version(1) type(1) epoch(4) seq(8) recipient(8) sender(8) issued_at(8) nonce(24), so
// issued_at occupies bytes [30:38] big-endian. The field is AAD-covered, so the AEAD must
// refuse the result.
func s11TamperIssuedAt(t *testing.T, raw []byte, delta time.Duration) []byte {
	t.Helper()
	const off = 1 + 1 + 4 + 8 + 8 + 8
	if len(raw) < off+8 {
		t.Fatalf("envelope is %d bytes, too short to hold a header", len(raw))
	}
	out := append([]byte(nil), raw...)
	cur := int64(binary.BigEndian.Uint64(out[off : off+8]))
	binary.BigEndian.PutUint64(out[off:off+8], uint64(cur+delta.Milliseconds()))
	return out
}

// s11EnvelopeIssuedAt reads the cleartext IssuedAt of a marshalled envelope.
func s11EnvelopeIssuedAt(t *testing.T, raw []byte) int64 {
	t.Helper()
	env, err := crypto.ParseEnvelope(raw)
	if err != nil {
		t.Fatalf("parse envelope: %v", err)
	}
	return env.Header.IssuedAt
}

// s11ResumedCore returns a file-backed Core bound to a content key and epoch, which is
// what the durable AcceptCommit path needs.
func s11ResumedCore(t *testing.T) (*Core, crypto.ContentKey, uint32) {
	t.Helper()
	c, err := Resume(Config{Dir: t.TempDir(), Machine: "m1", WakeSealer: s14aNewSealer(t), ContentSealer: s14aNewSealer(t)})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	key := testContentKey()
	st := c.State()
	st.Keys.ContentKey = key
	st.EpochID = s11Epoch
	if err := c.Save(st); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return c, key, s11Epoch
}

// ---------------------------------------------------------------------------
// The producer half: the gap PB-TIME-2 names
// ---------------------------------------------------------------------------

// TestS11ReplySeal_StampsANonZeroIssuedAt is PB-GW-6's first criterion, mirrored. The
// bracket is taken around the seal, so the stamp must be a real clock reading and not a
// constant that happens to be non-zero.
//
// The control case at the end is today's tree: an envelope sealed with the header
// SealControlReply builds now. It is what makes the assertion non-vacuous.
func TestS11ReplySeal_StampsANonZeroIssuedAt(t *testing.T) {
	key := testContentKey()

	before := time.Now().Add(-time.Second).UnixMilli()
	raw := s11SealReply(t, key, s11Epoch, 1, s11ReplyFor("op-1"))
	after := time.Now().Add(time.Second).UnixMilli()

	got := s11EnvelopeIssuedAt(t, raw)
	if got == 0 {
		t.Fatalf("remotegw.SealControlReply seals IssuedAt = 0. A bounded-age check on the phone's receiver computes an age of ~56 years for every command reply and refuses it: the lease confirmation (PB-SYNC-7) never lands, so PB-INPUT-2 suppresses input forever, and no mutating op ever resolves")
	}
	if got < before || got > after {
		t.Fatalf("SealControlReply seals IssuedAt = %d, outside [%d, %d]; the stamp must be the wall clock, not a placeholder", got, before, after)
	}
}

// TestS11ReplySeal_CoversEveryMachineToPhoneFrameKind. PB-GW-2's bound applies to a whole
// direction, not to one frame family: one unstamped producer is enough to make the check
// refuse that class forever. The journal, terminal and reconcile frames already stamp
// (relaysink.go); the reply path is the one that does not. Asserting all of them together
// is what stops the next producer added to this direction from repeating the trap a third
// time.
func TestS11ReplySeal_CoversEveryMachineToPhoneFrameKind(t *testing.T) {
	key := testContentKey()
	now := time.Now()

	// The reply path, through the real producer.
	frames := map[string][]byte{
		"SealControlReply (command reply)":      s11SealReply(t, key, s11Epoch, 1, s11ReplyFor("op-1")),
		"SealControlReply (lease confirmation)": s11SealReply(t, key, s11Epoch, 2, s11Confirmation(nil)),
		"SealControlReply (severance notice)":   s11SealReply(t, key, s11Epoch, 3, s11Severance("session exited")),
	}
	for name, raw := range frames {
		issued := s11EnvelopeIssuedAt(t, raw)
		if issued == 0 {
			t.Errorf("%s seals IssuedAt = 0", name)
			continue
		}
		if age := now.Sub(time.UnixMilli(issued)); age > InboundMaxAge {
			t.Errorf("%s: authenticated age %v exceeds the %v bound", name, age, InboundMaxAge)
		}
	}
}

// ---------------------------------------------------------------------------
// The receiver half: the bound, with both directions fenced
// ---------------------------------------------------------------------------

// TestS11InboundMaxAge_IsTheBudgetedTenMinutes pins the phone's mirror at the same §6.0
// value the gateway uses. A different number on the two sides would mean a frame each hop
// judged differently, which is a bug nobody would find until a real delay produced it.
func TestS11InboundMaxAge_IsTheBudgetedTenMinutes(t *testing.T) {
	if InboundMaxAge != 10*time.Minute {
		t.Fatalf("phonecore.InboundMaxAge = %v, want 10m (§6.0)", InboundMaxAge)
	}
	if InboundMaxAge != remotegw.InboundMaxAge {
		t.Fatalf("phonecore.InboundMaxAge (%v) != remotegw.InboundMaxAge (%v); one §6.0 budget, two numbers", InboundMaxAge, remotegw.InboundMaxAge)
	}
}

// TestS11ReplyAge_RealMachineSealsPassTheEnabledBound is PB-TIME-2's acceptance criterion
// verbatim: "the mirror of PB-GW-2's honest half: real machine-sealed replies still pass
// with the bound on".
//
// The GUARD comes first, and it is what S7b's lesson demands: a suite that only asserted
// "stale is rejected" would go green on an implementation that rejects EVERYTHING, and a
// suite that only asserted "fresh is accepted" would go green on one that has no bound at
// all. Both mutations are refused here, in one test, against the same router.
func TestS11ReplyAge_RealMachineSealsPassTheEnabledBound(t *testing.T) {
	key := testContentKey()

	// (a) THE BOUND IS LIVE. A frame past it is refused, so (b) is not vacuous.
	stale, err := crypto.SealMailbox(key, crypto.EnvelopeHeader{
		Version:  crypto.VersionV1,
		EpochID:  s11Epoch,
		Seq:      1,
		IssuedAt: time.Now().Add(-InboundMaxAge - time.Minute).UnixMilli(),
	}, []byte(`{"kind":"command_reply","op":"ok","operation_id":"op-stale"}`))
	if err != nil {
		t.Fatalf("seal the stale control frame: %v", err)
	}
	if _, err := NewMailboxRouter(key).Accept(stale.Marshal()); !errors.Is(err, crypto.ErrStaleAge) {
		t.Fatalf("a frame a minute past the %v bound was not refused (err = %v); the phone's bounded-age check is not enabled, so every assertion below is vacuous", InboundMaxAge, err)
	}

	// (b) AND REAL MACHINE SEALS STILL PASS. This is the brick half: with the bound on
	// and SealControlReply unstamped, a real lease confirmation is ~56 years old.
	replies := map[string]schema.Control{
		"command reply":      s11ReplyFor("op-1"),
		"lease confirmation": s11Confirmation(nil),
		"severance notice":   s11Severance("session exited"),
	}
	var seq uint64
	r := NewMailboxRouter(key)
	for name, ctrl := range replies {
		seq++
		if _, err := r.Accept(s11SealReply(t, key, s11Epoch, seq, ctrl)); err != nil {
			t.Errorf("%s: a freshly machine-sealed reply was refused with %v -- enabling the phone's bound rejects live machine traffic, which is PB-GW-6's brick one direction over", name, err)
		}
	}
	if r.Replies().Len() != len(replies) {
		t.Errorf("reply cache holds %d of %d frames; a refused reply is a lease the phone never learns about", r.Replies().Len(), len(replies))
	}
}

// TestS11ReplyAge_Boundary pins the three points that decide the comparison, matching the
// gateway's semantics exactly (S7b pinned "exactly at the bound is accepted", which is
// crypto.MailboxReceiver.Accept's own `> maxAge`). Each case gets its own router: this
// test is about the age axis, and a shared seq guard would decide the outcome instead.
func TestS11ReplyAge_Boundary(t *testing.T) {
	key := testContentKey()
	now := time.UnixMilli(1_784_000_000_000)

	cases := []struct {
		name    string
		age     time.Duration
		wantErr error
	}{
		{"one ms inside the bound", InboundMaxAge - time.Millisecond, nil},
		{"exactly at the bound", InboundMaxAge, nil},
		{"one ms outside the bound", InboundMaxAge + time.Millisecond, crypto.ErrStaleAge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, err := crypto.SealMailbox(key, crypto.EnvelopeHeader{
				Version:  crypto.VersionV1,
				EpochID:  s11Epoch,
				Seq:      5,
				IssuedAt: now.Add(-tc.age).UnixMilli(),
			}, []byte(`{"kind":"command_reply","op":"ok","operation_id":"op-b"}`))
			if err != nil {
				t.Fatalf("seal: %v", err)
			}
			_, err = NewMailboxRouter(key).AcceptAt(env.Marshal(), now)
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

// TestS11ReplyAge_ToleratesTheSkewBudgetAndIsOneSided. The bound compares two devices'
// clocks, so it is only safe if it clears §6.0's +/-30 s skew budget with room for the
// relay's own delay on top -- and it must not bound the FUTURE, for the reasons in the
// file header. A phone 30 s slow reads every machine stamp as 30 s newer than it is; a
// phone 30 s fast reads them as 30 s older, and it is that direction the bound could
// refuse.
func TestS11ReplyAge_ToleratesTheSkewBudgetAndIsOneSided(t *testing.T) {
	key := testContentKey()
	now := time.UnixMilli(1_784_000_000_000)

	// NON-VACUITY: every case below is an ACCEPT, so a router with no bound at all would
	// pass them all. Prove the bound is live on this exact constructor first.
	past, err := crypto.SealMailbox(key, crypto.EnvelopeHeader{
		Version: crypto.VersionV1, EpochID: s11Epoch, Seq: 1,
		IssuedAt: now.Add(-InboundMaxAge - time.Minute).UnixMilli(),
	}, []byte(`{"kind":"command_reply","op":"ok","operation_id":"op-guard"}`))
	if err != nil {
		t.Fatalf("seal the guard frame: %v", err)
	}
	if _, err := NewMailboxRouter(key).AcceptAt(past.Marshal(), now); !errors.Is(err, crypto.ErrStaleAge) {
		t.Fatalf("a frame a minute past the %v bound was not refused (err = %v); the bound is not enabled, so every acceptance below is vacuous", InboundMaxAge, err)
	}

	cases := []struct {
		name   string
		offset time.Duration // the machine's stamp relative to the phone's now
	}{
		{"machine 30s behind the phone (§6.0 skew budget)", -30 * time.Second},
		{"machine 30s ahead of the phone (§6.0 skew budget)", 30 * time.Second},
		{"machine 30s behind plus a 60s relay delay", -90 * time.Second},
		{"machine an hour ahead: the check is one-sided by design", time.Hour},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, err := crypto.SealMailbox(key, crypto.EnvelopeHeader{
				Version:  crypto.VersionV1,
				EpochID:  s11Epoch,
				Seq:      9,
				IssuedAt: now.Add(tc.offset).UnixMilli(),
			}, []byte(`{"kind":"command_reply","op":"ok","operation_id":"op-s"}`))
			if err != nil {
				t.Fatalf("seal: %v", err)
			}
			if _, err := NewMailboxRouter(key).AcceptAt(env.Marshal(), now); err != nil {
				t.Fatalf("offset %v: err = %v, want accepted -- the %v bound must clear the +/-30s skew budget and ordinary relay delay, and must not bound the future", tc.offset, err, InboundMaxAge)
			}
		})
	}
}

// TestS11ReplyAge_RejectionDoesNotPoisonTheStream is the assertion that keeps the cure
// from being worse than the disease, mirrored from S7b. crypto.MailboxReceiver.Accept
// advances the per-bucket high-water on the frames it accepts; if a frame refused for age
// advanced it too, a relay could inject ONE retained frame carrying an absurd seq and
// permanently silence the machine, whose next legitimate seq is far below it. On this side
// that means no lease confirmation, no op outcome and no journal for the rest of the epoch.
func TestS11ReplyAge_RejectionDoesNotPoisonTheStream(t *testing.T) {
	key := testContentKey()
	now := time.UnixMilli(1_784_000_000_000)
	r := NewMailboxRouter(key)

	poison, err := crypto.SealMailbox(key, crypto.EnvelopeHeader{
		Version: crypto.VersionV1, EpochID: s11Epoch, Seq: 5000,
		IssuedAt: now.Add(-InboundMaxAge - time.Minute).UnixMilli(),
	}, []byte(`{"kind":"command_reply","op":"ok","operation_id":"op-poison"}`))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := r.AcceptAt(poison.Marshal(), now); !errors.Is(err, crypto.ErrStaleAge) {
		t.Fatalf("retained seq-5000 frame: err = %v, want crypto.ErrStaleAge", err)
	}

	live, err := crypto.SealMailbox(key, crypto.EnvelopeHeader{
		Version: crypto.VersionV1, EpochID: s11Epoch, Seq: 6, IssuedAt: now.UnixMilli(),
	}, []byte(`{"kind":"command_reply","op":"ok","operation_id":"op-live"}`))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := r.AcceptAt(live.Marshal(), now); err != nil {
		t.Fatalf("the machine's live seq-6 reply was refused with %v after a stale-age frame at seq 5000 -- the age rejection advanced the receive high-water, so one retained frame from the relay permanently silences the machine", err)
	}
}

// TestS11ReplyAge_IsEnforcedOnTheDurableAcceptPath. Accept and AcceptCommit share
// MailboxRouter.open, but sharing is a property of today's code, not of the requirement:
// a bound added to Accept alone would leave the REAL phone -- which commits durably --
// entirely unfenced, and every test above would still pass. This is the test that refuses
// that shape.
//
// Its second half is the anti-brick control on the same path.
func TestS11ReplyAge_IsEnforcedOnTheDurableAcceptPath(t *testing.T) {
	c, key, epoch := s11ResumedCore(t)

	stale, err := crypto.SealMailbox(key, crypto.EnvelopeHeader{
		Version: crypto.VersionV1, EpochID: epoch, Seq: 1,
		IssuedAt: time.Now().Add(-InboundMaxAge - time.Minute).UnixMilli(),
	}, []byte(`{"kind":"command_reply","op":"ok","operation_id":"op-old"}`))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := c.Router().AcceptCommit(stale.Marshal(), 1); !errors.Is(err, crypto.ErrStaleAge) {
		t.Fatalf("AcceptCommit of a frame past the %v bound: err = %v, want crypto.ErrStaleAge -- the durable path is the one production reads through", InboundMaxAge, err)
	}
	if _, ok := c.State().OpOutcomes["op-old"]; ok {
		t.Fatal("a frame refused for age was still folded into the durable op outcomes; a fail-closed refusal must persist nothing")
	}

	// MUTATION CONTROL: a real machine seal on the same durable path.
	if _, err := c.Router().AcceptCommit(s11SealReply(t, key, epoch, 1, s11ReplyFor("op-fresh")), 2); err != nil {
		t.Fatalf("AcceptCommit of a freshly machine-sealed reply = %v, want nil -- with the bound on and SealControlReply unstamped, the real phone loses every reply it gets", err)
	}
	if _, ok := c.State().OpOutcomes["op-fresh"]; !ok {
		t.Fatal("a fresh machine reply did not reach the durable op outcomes")
	}
}

// TestS11ReplyAge_AForgeryIsRefusedAsAForgeryNotAsStale keeps the check honest about
// whose bytes it is judging. Deciding age from a header the AEAD has not vouched for means
// deciding on bytes the untrusted relay controls -- which is also the precondition for the
// skew monitor's authority (see s11_skew_test.go).
func TestS11ReplyAge_AForgeryIsRefusedAsAForgeryNotAsStale(t *testing.T) {
	key := testContentKey()
	var otherKey crypto.ContentKey
	for i := range otherKey {
		otherKey[i] = byte(i + 200)
	}
	now := time.UnixMilli(1_784_000_000_000)

	forged, err := crypto.SealMailbox(otherKey, crypto.EnvelopeHeader{
		Version: crypto.VersionV1, EpochID: s11Epoch, Seq: 3,
		IssuedAt: now.Add(-InboundMaxAge - time.Minute).UnixMilli(),
	}, []byte(`{"kind":"command_reply","op":"ok","operation_id":"op-forged"}`))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	_, err = NewMailboxRouter(key).AcceptAt(forged.Marshal(), now)
	if err == nil {
		t.Fatal("a frame sealed under the wrong content key was accepted; the AEAD must refuse it")
	}
	if errors.Is(err, crypto.ErrStaleAge) {
		t.Fatal("a frame whose AEAD does not verify was refused as crypto.ErrStaleAge: the bound was applied to an UNAUTHENTICATED IssuedAt, i.e. to bytes the untrusted relay supplied")
	}
}
