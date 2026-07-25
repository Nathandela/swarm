// FAILING-FIRST (TDD RED, GG-5) tests for slice S17's half of PB-PUSH-3: the push envelope
// TTL and replay window, enforced at the RECEIVER.
//
// WHAT IS BEING CLOSED. Section 6.0 requires "Push envelope TTL / replay window | 10 min,
// with the replay coordinate persisted per PB-STATE-1" against PB-PUSH-3, which slice S12
// shipped and claimed. The coordinate is persisted -- State.WakeReplay, wake tier since S15 --
// and S12 covered the SENDER half. Nothing writes it. `grep -rn 'WakeReplay' --include='*.go'`
// at the parent of this commit returns state.go's own persist/merge/load plumbing, one S15
// tier assertion, and nothing else: no producer anywhere in internal/ or mobile/. A wake
// replayed by the relay is not detected, and the relay is the party PB-SYNC-6 declares hostile
// and the party that necessarily handles every wake.
//
// WHAT A REPLAY IS AND IS NOT WORTH. The payload is content-free and a constant 78 bytes with
// zeroed key ids (ADR-007 B20), so a replay discloses nothing that the timing did not already
// disclose. It costs a spurious reconnect and battery, repeatable at the relay's discretion,
// on a device whose only background path from the machine is this one. This file does not
// claim more than that.
//
// NOTHING HERE RUNS AGAINST FCM, GOOGLE, A HANDSET OR DOZE. Every envelope is built in
// process by internal/remote/crypto, which is the same code the gateway seals with. PB-E2E-5,
// the physical-handset gate, is deferred under section 13 and is not touched by this file.
//
// THE ONE CONDITION EVERY TEST HERE RUNS IN. A wake arrives with NO USER PRESENT, so the
// content tier is locked by definition. Where that matters it is made explicit rather than
// assumed -- see TestS17_TheCoordinateAdvancesWithTheContentTierLocked, which is the fence
// against the fifth standing defect class: a guard that works only on a path production never
// takes.

package phonecore

import (
	"errors"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

const (
	s17Machine = "machine-endpoint-s17"
	s17Epoch   = uint32(11)
)

// s17Phone is a provisioned, PAIRED phone: device keys and a state blob holding the epoch
// keys, written through the production writer under the test's own KEKs.
type s17Phone struct {
	dir     string
	wake    *s14aSealer
	content *s14aSealer
	keys    crypto.EpochKeys
}

func s17Provision(t *testing.T) *s17Phone {
	t.Helper()
	p := &s17Phone{dir: t.TempDir(), wake: s14aNewSealer(t), content: s14aNewSealer(t)}

	keys, err := crypto.NewEpochKeys()
	if err != nil {
		t.Fatalf("crypto.NewEpochKeys: %v", err)
	}
	p.keys = keys

	core := p.resume(t)
	st := core.State()
	st.Machine = s17Machine
	st.EpochID = s17Epoch
	st.Keys = keys
	if err := core.Save(st); err != nil {
		t.Fatalf("seeding the phone's epoch keys: %v", err)
	}
	return p
}

// resume opens the state directory exactly as an Android process start does.
func (p *s17Phone) resume(t *testing.T) *Core {
	t.Helper()
	core, err := Resume(Config{
		Dir: p.dir, Machine: s17Machine,
		WakeSealer: p.wake, ContentSealer: p.content,
	})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	return core
}

// s17Wake builds one wake envelope the way internal/remotegw/push.go sealWake does: an EMPTY
// plaintext under the WAKE key, both key ids left zero, a real issued_at, and a seq from the
// gateway's durable source.
//
// It deliberately does not call remotegw: the phone must accept what the wire carries, not
// what one Go helper happens to produce, and importing the gateway into the phone core's
// tests would make a receiver bug indistinguishable from a producer change.
func s17Wake(t *testing.T, key crypto.WakeKey, epoch uint32, seq uint64, issued time.Time) []byte {
	t.Helper()
	env, err := crypto.SealWake(key, crypto.EnvelopeHeader{
		Version:  crypto.VersionV1,
		EpochID:  epoch,
		Seq:      seq,
		IssuedAt: issued.UnixMilli(),
	}, nil)
	if err != nil {
		t.Fatalf("crypto.SealWake: %v", err)
	}
	return env.Marshal()
}

// ---------------------------------------------------------------------------
// The producer that does not exist.
// ---------------------------------------------------------------------------

// TestS17_AnAcceptedWakeAdvancesThePersistedReplayCoordinate is the whole of the finding: a
// persisted field with no writer.
//
// It is asserted on the DURABLE state rather than on a return value, because a coordinate
// that advances only in memory is worth nothing on Android, where the OS SIGKILLs the process
// between one push and the next -- and it is the exact shape the bug already has: the field is
// loaded, merged monotonically and written back, and never set by anybody.
func TestS17_AnAcceptedWakeAdvancesThePersistedReplayCoordinate(t *testing.T) {
	p := s17Provision(t)
	core := p.resume(t)

	if got := core.State().WakeReplay; got != 0 {
		t.Fatalf("a freshly provisioned phone starts at WakeReplay %d, want 0; the assertions "+
			"below would not measure what they claim", got)
	}

	env := s17Wake(t, p.keys.WakeKey, s17Epoch, 5, time.Now())
	if err := core.AcceptWake(env); err != nil {
		t.Fatalf("PB-PUSH-3: a genuine, fresh wake was refused: %v", err)
	}
	if got := core.State().WakeReplay; got != 5 {
		t.Errorf("PB-PUSH-3: after accepting wake seq 5 the persisted replay coordinate is %d, want 5.\n"+
			"Section 6.0 requires the wake replay window WITH THE COORDINATE PERSISTED, and S12 "+
			"shipped the persisted field with no producer: state.go loads it, merges it and writes "+
			"it back, and nothing in internal/ or mobile/ has ever set it. A window nobody advances "+
			"refuses nothing", got)
	}
}

// TestS17_AReplayedWakeIsRefusedAndTheCoordinateDoesNotMove replays the identical bytes, which
// is precisely what a relay does: it holds the envelope it was asked to deliver.
func TestS17_AReplayedWakeIsRefusedAndTheCoordinateDoesNotMove(t *testing.T) {
	p := s17Provision(t)
	core := p.resume(t)

	env := s17Wake(t, p.keys.WakeKey, s17Epoch, 5, time.Now())
	if err := core.AcceptWake(env); err != nil {
		t.Fatalf("PB-PUSH-3: the FIRST delivery of a genuine wake was refused: %v", err)
	}

	err := core.AcceptWake(env)
	if err == nil {
		t.Fatal("PB-PUSH-3: the same wake envelope was accepted TWICE. The relay holds every wake " +
			"it delivers, so re-delivery is one line of code away for the party the design treats " +
			"as hostile (PB-SYNC-6)")
	}
	if !errors.Is(err, ErrWakeReplay) {
		t.Errorf("PB-PUSH-3: a replayed wake was refused with %v, want ErrWakeReplay. The verdict is "+
			"the difference between 'this is a replay' and 'this envelope is broken', and the phone "+
			"reacts differently to each", err)
	}
	if got := core.State().WakeReplay; got != 5 {
		t.Errorf("PB-PUSH-3: the refused replay left the coordinate at %d, want 5 unchanged", got)
	}
}

// TestS17_AWakeBelowTheCoordinateIsRefused covers reorder as well as replay: the relay decides
// delivery order, so "older than the last one I accepted" arrives without malice too.
func TestS17_AWakeBelowTheCoordinateIsRefused(t *testing.T) {
	p := s17Provision(t)
	core := p.resume(t)

	now := time.Now()
	if err := core.AcceptWake(s17Wake(t, p.keys.WakeKey, s17Epoch, 9, now)); err != nil {
		t.Fatalf("PB-PUSH-3: wake seq 9 was refused: %v", err)
	}
	if err := core.AcceptWake(s17Wake(t, p.keys.WakeKey, s17Epoch, 4, now)); !errors.Is(err, ErrWakeReplay) {
		t.Errorf("PB-PUSH-3: wake seq 4 delivered after seq 9 was answered with %v, want ErrWakeReplay", err)
	}
	if got := core.State().WakeReplay; got != 9 {
		t.Errorf("PB-PUSH-3: the coordinate is %d after a refused out-of-order wake, want 9", got)
	}
}

// TestS17_AGapInWakeSeqsIsAcceptedRatherThanTreatedAsLoss is the NEGATIVE control, and it is
// the one that stops this fence from becoming a brick.
//
// Wakes are dropped by design: PB-PUSH-0 coalesces per session over a 30 s window, the FCM
// high-priority quota is finite and android/fcm-priority.tsv resolves exhaustion as COALESCE,
// and the provider gives no delivery guarantee at all. So gaps are NORMAL, and a receiver that
// demanded contiguity -- the shape crypto.MailboxReceiver uses for the mailbox, where a gap
// means lost CONTENT and is reported -- would refuse every wake after the first drop and
// silence the phone permanently.
func TestS17_AGapInWakeSeqsIsAcceptedRatherThanTreatedAsLoss(t *testing.T) {
	p := s17Provision(t)
	core := p.resume(t)

	now := time.Now()
	if err := core.AcceptWake(s17Wake(t, p.keys.WakeKey, s17Epoch, 3, now)); err != nil {
		t.Fatalf("PB-PUSH-3: wake seq 3 was refused: %v", err)
	}
	if err := core.AcceptWake(s17Wake(t, p.keys.WakeKey, s17Epoch, 90, now)); err != nil {
		t.Fatalf("PB-PUSH-3: wake seq 90 was refused after seq 3: %v.\n"+
			"87 coalesced or quota-dropped wakes is an ordinary afternoon; the phone must still "+
			"wake on the next one it does receive", err)
	}
	if got := core.State().WakeReplay; got != 90 {
		t.Errorf("PB-PUSH-3: the coordinate is %d after accepting seq 90, want 90", got)
	}
}

// ---------------------------------------------------------------------------
// Durability. The coordinate is only worth persisting if the check consults it after a
// process death, which on Android is the ordinary case rather than the exception.
// ---------------------------------------------------------------------------

// TestS17_TheReplayCoordinateSurvivesAProcessDeath drops the Core and opens the SAME directory
// again, which is what the next launch does. It is the mirror of the grant-replay-after-restart
// test PB-STATE-1 already requires for crypto/epoch.go's watermark, for the same reason: a
// window held in memory is no window at all on a platform that kills the process between
// events.
func TestS17_TheReplayCoordinateSurvivesAProcessDeath(t *testing.T) {
	p := s17Provision(t)

	env := s17Wake(t, p.keys.WakeKey, s17Epoch, 7, time.Now())
	first := p.resume(t)
	if err := first.AcceptWake(env); err != nil {
		t.Fatalf("PB-PUSH-3: the first delivery was refused: %v", err)
	}

	// SIGKILL, as far as this package can model one: the Core is simply dropped.
	restarted := p.resume(t)
	if got := restarted.State().WakeReplay; got != 7 {
		t.Fatalf("PB-STATE-1: the restarted phone loaded WakeReplay %d, want 7. The coordinate was "+
			"never durable, so the assertion below cannot tell a working guard from a blind one", got)
	}
	if err := restarted.AcceptWake(env); !errors.Is(err, ErrWakeReplay) {
		t.Errorf("PB-PUSH-3/PB-STATE-1: a wake replayed AFTER a process death was answered with %v, "+
			"want ErrWakeReplay. Android kills this process routinely, so 'the app has restarted "+
			"since' is the relay's cheapest way to make a replay land", err)
	}
}

// TestS17_TheCoordinateAdvancesWithTheContentTierLocked is the fifth-defect-class fence, and
// it is the reason this file exists in phonecore rather than somewhere convenient.
//
// A push arrives with NO USER PRESENT. The content tier is biometric-gated and refuses with
// crypto.ErrKeyAuthRequired, so the ONLY condition AcceptWake ever runs in is this one -- and
// a replay guard that works when the phone is unlocked and silently fails when it is not is a
// fence guarding a path production does not take. The failure mode is not hypothetical: S15's
// fileStore REFUSES a Save that would put non-empty content-tier state into a container this
// process could not open (ErrContentTierLocked), so an implementation that advanced the
// coordinate by round-tripping a full State through Save would work in every unlocked test and
// refuse every real wake.
func TestS17_TheCoordinateAdvancesWithTheContentTierLocked(t *testing.T) {
	p := s17Provision(t)

	p.content.openErr = crypto.ErrKeyAuthRequired
	locked := p.resume(t)
	if got := locked.State().Keys.ContentKey; got != (crypto.ContentKey{}) {
		t.Fatal("the content tier is not actually locked in this test; every assertion below would " +
			"measure the unlocked path")
	}

	env := s17Wake(t, p.keys.WakeKey, s17Epoch, 12, time.Now())
	if err := locked.AcceptWake(env); err != nil {
		t.Fatalf("PB-PUSH-4/PB-PUSH-3: a wake was refused because the content tier is locked: %v.\n"+
			"That is the only state a wake ever arrives in", err)
	}
	if got := locked.State().WakeReplay; got != 12 {
		t.Errorf("PB-PUSH-3: a wake accepted with the content tier locked left the coordinate at %d, "+
			"want 12", got)
	}

	// DURABLE, from the locked process. The wake tier is readable and writable with no user
	// present precisely so this can happen (PB-STATE-9).
	p.content.openErr = nil
	if got := p.resume(t).State().WakeReplay; got != 12 {
		t.Errorf("PB-PUSH-3/PB-STATE-9: the coordinate advanced by a LOCKED process reads back as %d "+
			"after a restart, want 12. It was never written, so the next process re-accepts the "+
			"wake it already handled", got)
	}
}

// TestS17_TheWakePathNeverAsksForTheContentKEK is PB-PUSH-4's core-side half, measured at the
// custody seam rather than inferred.
//
// PB-KEY-2 says the phone never decrypts session content with a locked device, and the
// mechanism behind it is real: ContentKEK is auth-gated and refuses. An implementation that
// ASKS anyway is not caught by a refusal -- it just fails, or worse, succeeds on a phone the
// user happened to unlock a minute ago, which is how a wake path acquires a dependency nobody
// notices until it is on a handset. So this counts unwraps: the correct number is zero.
func TestS17_TheWakePathNeverAsksForTheContentKEK(t *testing.T) {
	p := s17Provision(t)
	core := p.resume(t)

	before := p.content.opens
	if err := core.AcceptWake(s17Wake(t, p.keys.WakeKey, s17Epoch, 2, time.Now())); err != nil {
		t.Fatalf("PB-PUSH-3: a genuine wake was refused: %v", err)
	}
	if got := p.content.opens - before; got != 0 {
		t.Errorf("PB-KEY-2/PB-PUSH-4: handling one wake asked the CONTENT tier KEK to open %d "+
			"time(s), want 0. The wake path runs with no user present; every content-tier unwrap "+
			"it performs is either a decrypt that must not happen or a dependency that will refuse "+
			"on a locked handset", got)
	}
	// NON-VACUITY. "Asked for nothing" is satisfied perfectly by a receiver that DID nothing --
	// which is what a permissive no-op stub is, and what a fail-open regression would be. The
	// coordinate having moved is the proof that the zero above was measured over real work.
	if got := core.State().WakeReplay; got != 2 {
		t.Errorf("NON-VACUITY: the coordinate is %d after the wake this test measured, want 2. "+
			"The zero-unwrap assertion above was taken over a receiver that did nothing", got)
	}
}

// ---------------------------------------------------------------------------
// The TTL. Section 6.0 puts a number on it and the number is enforceable, because issued_at
// is AAD-covered and therefore authenticated.
// ---------------------------------------------------------------------------

// TestS17_AWakeOlderThanTenMinutesIsRefused. The seq window alone does not express a TTL: a
// relay that captures the newest wake and simply waits still holds the highest seq the phone
// has seen, so it can wake the device at a moment of its choosing, forever, with one captured
// packet. Ten minutes is section 6.0's number.
func TestS17_AWakeOlderThanTenMinutesIsRefused(t *testing.T) {
	p := s17Provision(t)
	core := p.resume(t)

	stale := s17Wake(t, p.keys.WakeKey, s17Epoch, 4, time.Now().Add(-WakeMaxAge-time.Minute))
	err := core.AcceptWake(stale)
	if err == nil {
		t.Fatal("PB-PUSH-3: a wake issued 11 minutes ago was accepted. Section 6.0 puts a 10-minute " +
			"TTL on the envelope; without it one captured packet wakes the handset at the relay's " +
			"convenience for the life of the epoch")
	}
	if !errors.Is(err, ErrWakeExpired) {
		t.Errorf("PB-PUSH-3: an expired wake was refused with %v, want ErrWakeExpired", err)
	}
	if got := core.State().WakeReplay; got != 0 {
		t.Errorf("PB-PUSH-3: the expired wake advanced the coordinate to %d, want 0. An envelope "+
			"the phone refused must not consume window a genuine wake needs", got)
	}
}

// THIS IS A CONTROL, NOT A FENCE, and it passes against an accept-everything stub BY DESIGN --
// labelled so no evidence line counts it as a covered property. Its whole job is to make the
// test above unable to pass by refusing everything. It is the only test in this file that a
// permissive stub satisfies, and that is what a control is.
//
// TestS17_AWakeInsideTheWindowIsAccepted is the TTL's non-vacuity control: a guard that
// refuses everything passes the test above and is useless. Nine minutes is inside a
// ten-minute window, and a phone that came out of Doze slowly is exactly this case.
func TestS17_AWakeInsideTheWindowIsAccepted(t *testing.T) {
	p := s17Provision(t)
	core := p.resume(t)

	env := s17Wake(t, p.keys.WakeKey, s17Epoch, 4, time.Now().Add(-9*time.Minute))
	if err := core.AcceptWake(env); err != nil {
		t.Fatalf("PB-PUSH-3: a wake issued 9 minutes ago was refused with %v. The window is 10 "+
			"minutes and a Doze-delayed delivery lands late by design", err)
	}
}

// ---------------------------------------------------------------------------
// Authentication before bookkeeping. This is the pair that turns the fence from a convenience
// into something that cannot be used against the phone.
// ---------------------------------------------------------------------------

// TestS17_AForgedWakeNeverAdvancesTheCoordinate is the class-(iv) fence: the requirement
// ("a replay window with a persisted coordinate") is satisfiable by an implementation that
// ships a one-packet permanent outage.
//
// The relay handles every wake and can fabricate one. If the coordinate is advanced from the
// header BEFORE the AEAD is checked, a single envelope carrying seq 2^63 and any ciphertext at
// all pins the window at the top and every genuine wake for the rest of the epoch is refused
// as a replay -- silently, on the device's only background path. So the assertion is not
// merely that the forgery is refused; it is that the phone is still reachable afterwards.
func TestS17_AForgedWakeNeverAdvancesTheCoordinate(t *testing.T) {
	p := s17Provision(t)
	core := p.resume(t)

	var wrong crypto.WakeKey
	for i := range wrong {
		wrong[i] = 0x5c
	}
	forged := s17Wake(t, wrong, s17Epoch, 1<<62, time.Now())
	if err := core.AcceptWake(forged); err == nil {
		t.Fatal("PB-PUSH-3/PB-KEY-2: a wake sealed under a key this phone does not hold was ACCEPTED")
	}
	if got := core.State().WakeReplay; got != 0 {
		t.Fatalf("PB-PUSH-3: an UNAUTHENTICATED envelope moved the replay coordinate to %d. The relay "+
			"can send this; one packet then makes every genuine wake look like a replay for the life "+
			"of the epoch", got)
	}

	// The phone is still reachable, which is what the assertion above is protecting.
	if err := core.AcceptWake(s17Wake(t, p.keys.WakeKey, s17Epoch, 1, time.Now())); err != nil {
		t.Errorf("PB-PUSH-3: after one forged wake, a genuine wake seq 1 was refused with %v. The "+
			"forgery cost this handset its wake path", err)
	}
}

// TestS17_AMailboxFrameIsNeverAcceptedAsAWake pins A15/F10 at the receiver the tier split
// exists for. crypto.OpenWake refuses a type-0x01 envelope by construction, so this is a fence
// on the wake path CALLING it rather than opening the header itself -- and on the coordinate,
// because a content frame that advanced the wake window would let the machine's ordinary
// journal traffic starve the wake path.
func TestS17_AMailboxFrameIsNeverAcceptedAsAWake(t *testing.T) {
	p := s17Provision(t)
	core := p.resume(t)

	env, err := crypto.SealMailbox(p.keys.ContentKey, crypto.EnvelopeHeader{
		Version:  crypto.VersionV1,
		EpochID:  s17Epoch,
		Seq:      50,
		IssuedAt: time.Now().UnixMilli(),
	}, []byte(`{"kind":"","session_id":"build-box-17.local/refactor-the-auth-middleware"}`))
	if err != nil {
		t.Fatalf("crypto.SealMailbox: %v", err)
	}
	if err := core.AcceptWake(env.Marshal()); err == nil {
		t.Error("PB-KEY-2/A15: a type-0x01 MAILBOX frame -- session content, under the content key -- " +
			"was accepted as a wake")
	}
	if got := core.State().WakeReplay; got != 0 {
		t.Errorf("PB-PUSH-3: a mailbox frame moved the wake replay coordinate to %d, want 0", got)
	}
	s17WakePathStillWorks(t, core, p, 1)
}

// s17WakePathStillWorks is the non-vacuity control every purely NEGATIVE test above ends with.
//
// Without it, "refuse this" is satisfied perfectly by a receiver that refuses everything --
// which is exactly what this slice's RED scaffolding does, and exactly what a later
// fail-closed regression would do. Three tests in this file assert only that something is
// refused; each calls this so that a green run means "refused THAT and still accepts a genuine
// wake", never "refuses".
func s17WakePathStillWorks(t *testing.T, core *Core, p *s17Phone, seq uint64) {
	t.Helper()
	if err := core.AcceptWake(s17Wake(t, p.keys.WakeKey, s17Epoch, seq, time.Now())); err != nil {
		t.Errorf("NON-VACUITY: a genuine wake (seq %d) was refused with %v, so the refusal this test "+
			"asserts proves nothing -- a receiver that refuses EVERYTHING passes it", seq, err)
	}
}

// TestS17_AWakeFromAnotherEpochIsRefused. The wake key is per epoch and a revoke rotates it,
// so a wake sealed for the previous epoch cannot open under the current key. What this pins is
// that the refusal happens without the coordinate moving: the gateway's seq source is durable
// across a restart but the phone's coordinate is not reset by an epoch rotation, so an
// old-epoch envelope with a high seq is the same lever as the forgery above.
func TestS17_AWakeFromAnotherEpochIsRefused(t *testing.T) {
	p := s17Provision(t)
	core := p.resume(t)

	// Same key, DIFFERENT epoch id: the epoch is AAD-covered, so the tag fails.
	env := s17Wake(t, p.keys.WakeKey, s17Epoch+1, 1<<40, time.Now())
	if err := core.AcceptWake(env); err == nil {
		t.Error("PB-PUSH-3: a wake stamped with another epoch id was accepted")
	}
	if got := core.State().WakeReplay; got != 0 {
		t.Errorf("PB-PUSH-3: a wake from another epoch moved the coordinate to %d, want 0", got)
	}
	s17WakePathStillWorks(t, core, p, 1)
}

// TestS17_AWakeThatIsNotAnEnvelopeIsRefusedWithoutPanicking. The bytes come from a push
// provider by way of Kotlin, so "garbage" is a reachable input rather than a contrived one: a
// truncated data field, a wrong base64 decode, a message from a stale project id. barrier()
// catches a panic at the facade, but a panic here is still a crash loop on a background wake,
// which the user sees as an app that dies whenever an agent stops.
func TestS17_AWakeThatIsNotAnEnvelopeIsRefusedWithoutPanicking(t *testing.T) {
	p := s17Provision(t)
	core := p.resume(t)

	for name, raw := range map[string][]byte{
		"empty":            {},
		"one byte":         {0x02},
		"header only":      make([]byte, 62),
		"truncated tag":    make([]byte, 70),
		"unknown version":  append([]byte{0x09, 0x02}, make([]byte, 90)...),
		"unknown type":     append([]byte{0x01, 0x7f}, make([]byte, 90)...),
		"random ish bytes": []byte("this is not an envelope, it is a sentence"),
	} {
		t.Run(name, func(t *testing.T) {
			if err := core.AcceptWake(raw); err == nil {
				t.Errorf("PB-PUSH-3: %q was accepted as a wake", name)
			}
			if got := core.State().WakeReplay; got != 0 {
				t.Errorf("PB-PUSH-3: %q moved the replay coordinate to %d", name, got)
			}
		})
	}
	s17WakePathStillWorks(t, core, p, 1)
}
