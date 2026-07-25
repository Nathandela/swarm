package phonecore

// FAILING-FIRST (TDD RED, GG-5) tests for the GRANT channel: PB-KEY-10 (the phone can
// never obtain an epoch key at all) and PB-KEY-3 (a lost grant has no recovery), at the
// core. The facade half is in mobile/conformance/s10_bootstrap_test.go, which drives a
// REAL pairing over a REAL relay; this file pins the durable transaction underneath it.
//
// WHAT IS ACTUALLY BROKEN, verified in the tree:
//
//   - State.Keys is written ONLY by mobile.App.InstallWakeKey / InstallContentKey, which
//     are INBOUND verbs from Kotlin, and nothing supplies those bytes. The custody surface
//     is inbound-only by design (ADR-007 B8) and the golden-pinned facade has no verb that
//     ingests a grant.
//   - The machine DOES deliver the key: cmd/swarm-remote/deliver.go appends the sealed
//     crypto.EpochGrant to the phone's mailbox as a TAGGED PLAINTEXT bootstrap frame, and
//     its own comment says the phone consumes it BEFORE it can build a ContentKey-keyed
//     router ("the grant is what DELIVERS the ContentKey").
//   - Nothing consumes it. phonecore.AcceptGrant's only production caller is
//     internal/phonesim, the test simulator; MailboxRouter.TakeGrant has ZERO production
//     callers and its own comment defers consumption to a work item ("C5") nobody did; and
//     Core.Grants() has zero callers anywhere.
//
// A bootstrap frame is NOT a crypto envelope, so AcceptCommit's ParseEnvelope refuses it,
// commits nothing and acks nothing: the relay cursor never advances past it and the drain
// re-reads the same page forever. That single fact is both halves of PB-KEY-10 -- the key
// never arrives AND the frame is never compacted -- so it is fenced twice below.
//
// THE SEAM THESE TESTS PIN: the bootstrap frame is consumed on the SAME path every other
// inbound item takes, MailboxRouter.AcceptCommit, so the facade needs no new verb and the
// golden exported surface is untouched. Everything the open needs is already in the core:
// the device KeyStore (Core.ks), the pinned machine grant-signing pub (State.MachineSignPub)
// and the replay watermark (Core.Grants(), seeded from State.GrantEpoch/GrantSeq).

import (
	"crypto/ed25519"
	"errors"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/grant"
)

// s10Machine is a machine that can seal grants to a phone: its grant-signing keypair.
type s10Machine struct {
	pub  ed25519.PublicKey
	priv ed25519.PrivateKey
}

func newS10Machine(t *testing.T) s10Machine {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("machine grant-signing key: %v", err)
	}
	return s10Machine{pub: pub, priv: priv}
}

// bootstrapFor seals an epoch grant to the phone's recipient key and wraps it in the exact
// tagged plaintext bootstrap frame the gateway appends (cmd/swarm-remote/deliver.go).
func (m s10Machine) bootstrapFor(t *testing.T, ks crypto.KeyStore, epochID uint32, grantSeq uint64) ([]byte, crypto.EpochKeys) {
	t.Helper()
	keys, err := crypto.NewEpochKeys()
	if err != nil {
		t.Fatalf("epoch keys: %v", err)
	}
	g, err := crypto.SealEpochGrant(m.priv, ks.RecipientPublic(), epochID, grantSeq, keys)
	if err != nil {
		t.Fatalf("seal epoch grant: %v", err)
	}
	frame, err := grant.MarshalBootstrap(g)
	if err != nil {
		t.Fatalf("marshal bootstrap: %v", err)
	}
	return frame, keys
}

// s10JustPaired is a Core in exactly the state a REAL pairing leaves it in: the machine's
// keys are pinned, the epoch id is known -- and State.Keys is ZERO, because mobile.App.pin
// clears it on a new epoch and nothing anywhere fills it in. It is the fixture the rest of
// the suite does not have: every other harness seeds crypto.EpochKeys directly, which is
// precisely why nothing noticed.
func s10JustPaired(t *testing.T, m s10Machine) (*Core, Store) {
	t.Helper()
	st := &memStore{}
	if err := st.Save(State{Machine: "m1", MachineSignPub: m.pub, EpochID: 7}); err != nil {
		t.Fatalf("seed the just-paired state: %v", err)
	}
	c, err := Resume(Config{State: st, Ack: &recordingAcker{}})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if c.State().Keys.ContentKey != (crypto.ContentKey{}) {
		t.Fatalf("precondition: the just-paired fixture already holds a content key; it must be " +
			"ZERO, or these tests prove nothing about how the key ARRIVES")
	}
	return c, st
}

// ---------------------------------------------------------------------------
// PB-KEY-10.
// ---------------------------------------------------------------------------

// TestS10_ABootstrapGrantOnTheMailboxDeliversTheEpochKey is PB-KEY-10 at the core. No test
// here calls InstallWakeKey or InstallContentKey; the ONLY thing that happens is the frame
// the machine really appends arriving on the path the phone really drains.
func TestS10_ABootstrapGrantOnTheMailboxDeliversTheEpochKey(t *testing.T) {
	m := newS10Machine(t)
	c, _ := s10JustPaired(t, m)
	frame, keys := m.bootstrapFor(t, c.KeyStore(), 7, 1)

	if _, err := c.Router().AcceptCommit(frame, 500); err != nil {
		t.Fatalf("AcceptCommit on the machine's bootstrap grant frame: %v.\n\n"+
			"This is the frame cmd/swarm-remote/deliver.go appends to the phone's mailbox on "+
			"every gateway connect. It is a TAGGED PLAINTEXT frame, not a ContentKey-sealed "+
			"envelope -- deliberately, because it is what DELIVERS the ContentKey -- so "+
			"ParseEnvelope refuses it and the phone's one inbound path has no branch that "+
			"recognises it. phonecore.AcceptGrant exists and its only production caller is "+
			"internal/phonesim, the test simulator", err)
	}

	got := c.State()
	if got.Keys.ContentKey != keys.ContentKey {
		t.Errorf("after the bootstrap grant landed the phone's content key is %v, want the granted "+
			"one. Without it resolveSend returns errNoContentKey for EVERY send -- take_control, "+
			"kill, launch, input, paste, resize -- and every inbound frame fails to open, so the "+
			"relay cursor never advances and the drain polls the same page forever. The product "+
			"does not function on a real handset", got.Keys.ContentKey != crypto.ContentKey{})
	}
	if got.Keys.WakeKey != keys.WakeKey {
		t.Errorf("the WAKE key was not installed. PB-KEY-2's two tiers are delivered in ONE grant; " +
			"installing only the content key leaves the push path with nothing to open a wake with")
	}
	if got.GrantEpoch != 7 || got.GrantSeq != 1 {
		t.Errorf("the grant watermark is (epoch %d, seq %d), want (7, 1). It must be persisted with "+
			"the keys or a relay that retained the grant can replay it after the next process "+
			"death -- the hole crypto.NewGrantReceiverAt exists to close",
			got.GrantEpoch, got.GrantSeq)
	}
}

// TestS10_ABootstrapFrameIsAckedSoTheMailboxCompacts is PB-KEY-10's second half. Today
// AcceptCommit acks only on crypto.ErrStaleSeq or crypto.ErrStaleAge, and the bootstrap
// frame fails earlier than either -- so an UNOPENED bootstrap frame is never compacted from
// the relay mailbox and never stops being re-read.
func TestS10_ABootstrapFrameIsAckedSoTheMailboxCompacts(t *testing.T) {
	m := newS10Machine(t)
	st := &memStore{}
	if err := st.Save(State{Machine: "m1", MachineSignPub: m.pub, EpochID: 7}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	ack := &recordingAcker{}
	c, err := Resume(Config{State: st, Ack: ack})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	frame, _ := m.bootstrapFor(t, c.KeyStore(), 7, 1)

	rcpt, err := c.Router().AcceptCommit(frame, 500)
	if err != nil {
		t.Fatalf("AcceptCommit on the bootstrap frame: %v", err)
	}
	if !rcpt.Acked {
		t.Errorf("the bootstrap frame was not acked. The relay mailbox never compacts it, and " +
			"the phone re-reads it on every drain for the whole 7-day retention window")
	}
	if got := c.State().RelayCursor; got != 500 {
		t.Errorf("the relay read cursor is %d after consuming the bootstrap frame at cursor 500. "+
			"mobile.App.drain re-reads from State.RelayCursor and only loops immediately when the "+
			"cursor MOVED, so a bootstrap frame that does not advance it makes every later frame "+
			"unreachable behind it -- the drain polls the same page forever", got)
	}
}

// TestS10_ABootstrapFrameDoesNotBlockTheFramesBehindIt is the consequence stated as its own
// fence, because it is the one a user would report: the phone connects, shows nothing, and
// never recovers.
func TestS10_ABootstrapFrameDoesNotBlockTheFramesBehindIt(t *testing.T) {
	m := newS10Machine(t)
	c, _ := s10JustPaired(t, m)
	frame, keys := m.bootstrapFor(t, c.KeyStore(), 7, 1)

	if _, err := c.Router().AcceptCommit(frame, 500); err != nil {
		t.Fatalf("AcceptCommit on the bootstrap frame: %v", err)
	}
	// The journal frame the gateway sealed right after the grant, under the key the grant
	// just delivered.
	rec := []byte(`{"cursor":1,"session_id":"m1/s-alpha","type":"launched"}`)
	if _, err := c.Router().AcceptCommit(sealFrameFrom(t, keys.ContentKey, machineSender, 7, 1, rec), 501); err != nil {
		t.Fatalf("the first journal frame after the grant: %v. The router must be REBOUND to the "+
			"granted key by the same transaction that installed it, or every frame the machine "+
			"seals under the new epoch is undecodable", err)
	}
	if _, ok := c.Router().Sessions().Get("m1/s-alpha"); !ok {
		t.Errorf("the journal frame behind the bootstrap grant never reached the session cache")
	}
}

// TestS10_AReplayedBootstrapGrantIsRefused: delivery is at-least-once (deliver.go appends
// once per gateway session), so the phone WILL see the same grant again -- and an untrusted
// relay can serve a retained OLD one after a rotation. The watermark is what refuses it.
func TestS10_AReplayedBootstrapGrantIsRefused(t *testing.T) {
	m := newS10Machine(t)
	c, _ := s10JustPaired(t, m)

	first, firstKeys := m.bootstrapFor(t, c.KeyStore(), 7, 1)
	if _, err := c.Router().AcceptCommit(first, 500); err != nil {
		t.Fatalf("first bootstrap: %v", err)
	}
	// A rotation grant: same machine, higher epoch. It must be adopted.
	rotated, rotatedKeys := m.bootstrapFor(t, c.KeyStore(), 8, 2)
	if _, err := c.Router().AcceptCommit(rotated, 501); err != nil {
		t.Fatalf("rotation bootstrap: %v", err)
	}
	if c.State().Keys.ContentKey != rotatedKeys.ContentKey || c.State().EpochID != 8 {
		t.Fatalf("the rotation grant was not adopted (epoch %d); PB-KEY-4 is unreachable without it",
			c.State().EpochID)
	}

	// The relay re-serves the pre-rotation grant. Accepting it would silently downgrade the
	// phone onto a key the machine has already retired.
	if _, err := c.Router().AcceptCommit(first, 502); err == nil {
		t.Errorf("a REPLAYED pre-rotation bootstrap grant was accepted")
	}
	if c.State().Keys.ContentKey == firstKeys.ContentKey {
		t.Errorf("the phone is back on the retired epoch's content key after a replayed grant. A " +
			"retaining relay is the declared adversary here, and this rewinds the phone onto a key " +
			"a revoked device may still hold")
	}
}

// ---------------------------------------------------------------------------
// PB-KEY-3: a lost grant reaches a DEFINED, recoverable end.
// ---------------------------------------------------------------------------

// TestS10_GrantLossIsNotACustodyRefusal is the distinguishability PB-KEY-3 requires against
// S14a's work. The two failures look identical from the send path -- no content key, every
// op refused -- and the remedies are opposites:
//
//	crypto.ErrKeyInvalidated -> this handset's custody is gone.        "Re-pair."
//	ErrGrantLost             -> custody is fine, the grant never came. "The machine must re-grant."
//
// Telling a grant-loss user to re-pair sends them into BeginPairing, which fail-fasts while
// a device is registered -- PB-STATE-10's brick, reachable only by physical access to the
// machine.
func TestS10_GrantLossIsNotACustodyRefusal(t *testing.T) {
	if errors.Is(ErrGrantLost, crypto.ErrKeyInvalidated) {
		t.Errorf("ErrGrantLost matches crypto.ErrKeyInvalidated, the PERMANENT custody refusal. A " +
			"caller cannot then route the user to the right remedy, and the wrong one is a brick: " +
			"re-pairing is refused outright while a device is registered")
	}
	if errors.Is(ErrGrantLost, crypto.ErrKeyAuthRequired) {
		t.Errorf("ErrGrantLost matches crypto.ErrKeyAuthRequired, which is the RECOVERABLE " +
			"unlock-me refusal; a grant that never arrived is not fixed by unlocking")
	}

	m := newS10Machine(t)
	c, _ := s10JustPaired(t, m)
	if c.StreamStale(StreamGrant) {
		t.Fatalf("precondition: a just-paired phone reports the grant channel already stale")
	}
	if err := c.MarkGrantLost(); err != nil {
		t.Fatalf("MarkGrantLost: %v", err)
	}
	if !c.StreamStale(StreamGrant) {
		t.Errorf("after MarkGrantLost the grant channel is not stale. PB-KEY-3 requires a DEFINED "+
			"terminal state, and %q is the state; a phone that merely keeps failing every send "+
			"with errNoContentKey is the indefinite decrypt-failure loop the requirement forbids",
			StreamGrant)
	}
}

// TestS10_ARegrantRecoversAGrantLossDevice is the "recoverable end" half. The relay purged
// the original bootstrap frame (SweepRetention drops items past RetentionCap -- 7 days by
// default -- even when never acked), so the ONLY exit is the machine issuing a fresh grant.
// It must land on the ordinary inbound path and clear the terminal state.
func TestS10_ARegrantRecoversAGrantLossDevice(t *testing.T) {
	m := newS10Machine(t)
	c, _ := s10JustPaired(t, m)
	if err := c.MarkGrantLost(); err != nil {
		t.Fatalf("MarkGrantLost: %v", err)
	}

	frame, keys := m.bootstrapFor(t, c.KeyStore(), 7, 2)
	if _, err := c.Router().AcceptCommit(frame, 600); err != nil {
		t.Fatalf("the machine's re-grant: %v", err)
	}
	if c.State().Keys.ContentKey != keys.ContentKey {
		t.Errorf("the re-granted content key was not installed; the documented machine-side " +
			"unblock does not unblock anything")
	}
	if c.StreamStale(StreamGrant) {
		t.Errorf("the grant channel is still reported lost after a re-grant landed. The terminal " +
			"state must be RECOVERABLE, never latched (PB-STATE-10)")
	}
}
