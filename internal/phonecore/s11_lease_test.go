package phonecore

// Slice S11 -- FAILING-FIRST (TDD RED, GG-5) tests for PB-INPUT-2 (the lease lifecycle,
// and "no keystroke is ever sent without a confirmed current lease generation") and
// PB-INPUT-3 (TTL expiry mid-use has a defined UX).
//
// THE CONTRACT these tests freeze (undefined symbols -> compile-level RED):
//
//	var ErrNoLease      error  // no confirmed lease for this session
//	var ErrLeaseExpired error  // the confirmed lease's horizon has passed
//
//	type Lease struct{ Session string; Generation uint64; ExpiresAt time.Time }
//
//	type LeaseState struct{ ... }
//	func NewLeaseState() *LeaseState
//	func (*LeaseState) Requested(session, operationID string, expiresAt time.Time)
//	func (*LeaseState) Apply(ctrl schema.Control)
//	func (*LeaseState) Sever(session, reason string)
//	func (*LeaseState) SeverAll(reason string)
//	func (*LeaseState) Lease(session string) (Lease, bool)
//	func (*LeaseState) Require(session string, now time.Time) error
//	func (*LeaseState) Reason(session string) string
//
//	func (*Core) Leases() *LeaseState   // fed automatically from the inbound path
//
//	const CommandTTL     = 1 * time.Minute   // §6.0, ordinary commands
//	const TakeControlTTL = 15 * time.Minute  // §6.0, the take_control exception
//	func CommandTTLFor(action string) time.Duration
//
// WHY A LEASE STATE HAS TO EXIST AT ALL. Before this slice the phone had NO notion of a
// lease. mobile.SendInput (commands.go:156-176) seals and appends a keystroke with no
// check of any kind, and PB-INPUT-2's "input is suppressed until a new lease is visibly
// confirmed" had nothing to suppress against. PB-SYNC-7 supplied the CONFIRMATION half
// (remotegw/lease_confirm.go seals an OpLease carrying the daemon-granted generation);
// this is the consumer.
//
// WHY THE LEASE IS NEVER PERSISTED. A lease is a live daemon connection
// (remotegw/lease.go). Nothing on the phone can keep one alive across a process death,
// and a lease restored from disk is by construction a lease the machine does not hold --
// which is precisely the "assume the lease" failure PB-INPUT-2 forbids. So process death
// is fenced by asserting the lease is ABSENT from durable state, not by asserting it is
// cleared on load.
//
// RECORDED (see the S11 report): the daemon's OpLease grant carries no ExpiresAt
// (internal/protocol/server.go:620-626 encodes Op/EndpointID/SessionID/Generation/
// SnapshotLen only), while the lease deadline it computes is the earliest of three bounds
// (:1500-1533). The phone therefore cannot observe the authoritative expiry today. The
// contract below takes the machine's value when a confirmation carries one and falls back
// to the horizon the phone SIGNED, which is an upper bound on the truth -- so the phone can
// only ever believe the lease ends LATER than it does, never earlier, and the severance
// notice (not the countdown) stays the authority. Changing the daemon reply is outside
// S11's scope fence.

import (
	"errors"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// s11Gen is the daemon-granted lease generation used throughout.
const s11Gen uint64 = 42

// s11TakeOp is the take_control operation id every confirmation in this file answers.
const s11TakeOp = "op-take-s11"

// s11Confirmation is the gateway's lease confirmation exactly as
// remotegw.CommandBridge.confirmLease seals it: OpLease, tagged with the take_control's
// operation id, carrying the daemon-granted generation.
func s11Confirmation(expiresAt *time.Time) schema.Control {
	return schema.Control{
		Op:          protocol.OpLease,
		SessionID:   s11Session,
		OperationID: s11TakeOp,
		Generation:  s11Gen,
		ExpiresAt:   expiresAt,
	}
}

// s11Severance is the gateway's lease-death notice, tagged with the same operation id so
// it is attributable to the take_control that opened the lease.
func s11Severance(reason string) schema.Control {
	return schema.Control{
		Op:          protocol.OpDetach,
		SessionID:   s11Session,
		OperationID: s11TakeOp,
		Generation:  s11Gen,
		Error:       reason,
	}
}

// s11Leased returns a state holding one confirmed lease valid until expiresAt.
func s11Leased(t *testing.T, expiresAt time.Time) *LeaseState {
	t.Helper()
	l := NewLeaseState()
	l.Requested(s11Session, s11TakeOp, expiresAt)
	l.Apply(s11Confirmation(&expiresAt))
	if err := l.Require(s11Session, expiresAt.Add(-time.Minute)); err != nil {
		t.Fatalf("setup: a freshly confirmed lease is not usable: %v", err)
	}
	return l
}

// ---------------------------------------------------------------------------
// PB-INPUT-2 -- nothing is sent without a CONFIRMED generation
// ---------------------------------------------------------------------------

// TestS11Lease_InputIsRefusedWithoutAConfirmedLease is PB-INPUT-2's headline. The
// mutation control is the second half: the same call must SUCCEED once a real
// confirmation lands, or a gate that refuses everything would satisfy the first half
// while making typing impossible -- the shape PB-GW-6 shipped and S7b had to fence.
func TestS11Lease_InputIsRefusedWithoutAConfirmedLease(t *testing.T) {
	now := time.Now()
	l := NewLeaseState()

	if err := l.Require(s11Session, now); !errors.Is(err, ErrNoLease) {
		t.Fatalf("Require with no lease = %v, want ErrNoLease -- mobile.SendInput seals and appends a keystroke with no lease check at all today (commands.go:156-176)", err)
	}
	// A take_control the phone merely AUTHORED is not a lease. This is the exact
	// assumption PB-INPUT-2 forbids, and it is what the phone would have to do if the
	// gateway sealed nothing for take_control (the PB-SYNC-7 gap).
	l.Requested(s11Session, s11TakeOp, now.Add(TakeControlTTL))
	if err := l.Require(s11Session, now); !errors.Is(err, ErrNoLease) {
		t.Fatalf("Require after authoring take_control but before its confirmation = %v, want ErrNoLease -- an authored command is not a granted lease", err)
	}

	// MUTATION CONTROL: the confirmation makes it usable.
	exp := now.Add(TakeControlTTL)
	l.Apply(s11Confirmation(&exp))
	if err := l.Require(s11Session, now); err != nil {
		t.Fatalf("Require after a real lease confirmation = %v, want nil -- a gate that refuses a CONFIRMED lease refuses all typing, and the assertions above then prove nothing", err)
	}
	got, ok := l.Lease(s11Session)
	if !ok || got.Generation != s11Gen {
		t.Fatalf("Lease() = %+v (ok=%v), want generation %d -- PB-INPUT-2 gates on the confirmed GENERATION, so it has to be readable", got, ok, s11Gen)
	}
}

// TestS11Lease_AGenerationOfZeroIsNotAConfirmation. remotegw.confirmLease already refuses
// to seal OpLease with generation 0, because LeaseManager.Generation reports 0 for a
// session holding no conn -- exactly the state the watcher leaves behind the moment a
// lease dies. This is the receiving half of that rule: if a zero-generation OpLease ever
// reaches the phone (an older gateway, a future refactor), it must not open the gate.
func TestS11Lease_AGenerationOfZeroIsNotAConfirmation(t *testing.T) {
	now := time.Now()
	exp := now.Add(TakeControlTTL)
	l := NewLeaseState()
	l.Requested(s11Session, s11TakeOp, exp)

	zero := s11Confirmation(&exp)
	zero.Generation = 0
	l.Apply(zero)

	if err := l.Require(s11Session, now); !errors.Is(err, ErrNoLease) {
		t.Fatalf("Require after an OpLease carrying generation 0 = %v, want ErrNoLease -- generation 0 names a lease the daemon does not hold", err)
	}
}

// TestS11Lease_ARefusalIsNotAConfirmationAndCarriesItsReason. confirmLease seals an
// OpError when Begin failed, precisely so a refusal is distinguishable from a slow grant.
// The phone must keep the gate shut AND keep the reason, or the user sees a dead keyboard
// with no explanation.
func TestS11Lease_ARefusalIsNotAConfirmationAndCarriesItsReason(t *testing.T) {
	now := time.Now()
	l := NewLeaseState()
	l.Requested(s11Session, s11TakeOp, now.Add(TakeControlTTL))

	l.Apply(schema.Control{
		Op:          protocol.OpError,
		SessionID:   s11Session,
		OperationID: s11TakeOp,
		Error:       "take_control: kill switch is off",
	})

	if err := l.Require(s11Session, now); !errors.Is(err, ErrNoLease) {
		t.Fatalf("Require after an OpError refusal = %v, want ErrNoLease", err)
	}
	if got := l.Reason(s11Session); got == "" {
		t.Fatal("Reason() is empty after a refusal; the refusal reason is the only thing that distinguishes a refused lease from a slow one, and PB-INPUT-2 requires the state be VISIBLE")
	}
}

// TestS11Lease_EveryLifecycleEventSeversTheLease walks PB-INPUT-2's enumerated list.
// "Test per event" is the acceptance criterion, so each event is its own subtest, and each
// asserts BOTH halves: the lease is gone, and a reason is available to show.
//
// Process death is deliberately absent here -- it cannot be expressed as a call on a live
// LeaseState, and is fenced separately below by asserting the lease is not durable at all.
func TestS11Lease_EveryLifecycleEventSeversTheLease(t *testing.T) {
	now := time.Now()
	exp := now.Add(TakeControlTTL)

	events := []struct {
		name  string
		event func(l *LeaseState)
	}{
		{
			// The gateway process is gone, so it can seal nothing. The phone learns only
			// from its own transport losing the connection -- which is why a disconnect
			// must sever, not merely pause.
			"gateway restart (no notice can arrive; the transport drops)",
			func(l *LeaseState) { l.SeverAll("relay connection lost") },
		},
		{
			// The daemon died under a live gateway: the lease conn's readLoop errors and
			// LeaseManager.watch evicts it, so the gateway CAN and MUST seal a notice.
			"daemon restart (gateway seals the severance notice)",
			func(l *LeaseState) { l.Apply(s11Severance("daemon connection closed")) },
		},
		{
			// The daemon detaches the controller when the session ends.
			"session exit under the user",
			func(l *LeaseState) { l.Apply(s11Severance("session exited")) },
		},
		{
			// PB-RUN-3's backgrounding policy: the app stops being allowed to type.
			// §6.0 forbids silently continuing.
			"app backgrounding",
			func(l *LeaseState) { l.SeverAll("app backgrounded") },
		},
	}

	for _, ev := range events {
		t.Run(ev.name, func(t *testing.T) {
			l := s11Leased(t, exp)
			ev.event(l)

			if err := l.Require(s11Session, now); !errors.Is(err, ErrNoLease) {
				t.Fatalf("Require after %q = %v, want ErrNoLease -- a lease that survives this event lets the phone type against a lease the machine no longer holds", ev.name, err)
			}
			if _, ok := l.Lease(s11Session); ok {
				t.Fatalf("Lease() still reports a lease after %q", ev.name)
			}
			if l.Reason(s11Session) == "" {
				t.Fatalf("Reason() is empty after %q; PB-INPUT-2 requires input be suppressed until a new lease is VISIBLY confirmed, and an invisible suppression is a dead keyboard", ev.name)
			}
		})
	}
}

// TestS11Lease_ASeveredLeaseIsNotResurrectedByAStaleConfirmation. The relay may reorder,
// so a confirmation sealed BEFORE the severance can arrive after it. Re-opening the gate
// on it would let a keystroke ride a lease the daemon released -- and the daemon's own
// F11 generation check would then drop it silently, so the phone would show a live
// keyboard typing into nothing.
func TestS11Lease_ASeveredLeaseIsNotResurrectedByAStaleConfirmation(t *testing.T) {
	now := time.Now()
	exp := now.Add(TakeControlTTL)
	l := s11Leased(t, exp)

	l.Apply(s11Severance("session exited"))
	l.Apply(s11Confirmation(&exp)) // the SAME generation, re-delivered

	if err := l.Require(s11Session, now); !errors.Is(err, ErrNoLease) {
		t.Fatalf("Require after a severance followed by a replayed confirmation for the SAME generation = %v, want ErrNoLease -- a dead generation may never be reconfirmed", err)
	}

	// MUTATION CONTROL: a genuinely NEW lease (a fresh take_control, a new generation)
	// must reopen the gate, or recovery after any severance is impossible.
	const nextOp = "op-take-s11-2"
	l.Requested(s11Session, nextOp, exp)
	l.Apply(schema.Control{
		Op: protocol.OpLease, SessionID: s11Session, OperationID: nextOp,
		Generation: s11Gen + 1, ExpiresAt: &exp,
	})
	if err := l.Require(s11Session, now); err != nil {
		t.Fatalf("Require after a NEW take_control was confirmed at generation %d = %v, want nil -- a severance that cannot be recovered from is a permanent brick (PB-STATE-10's rule)", s11Gen+1, err)
	}
}

// TestS11Lease_ASupersededGenerationsSeveranceDoesNotKillTheLiveLease. A second
// take_control for a session SUPERSEDES the first: LeaseManager.Begin stores the new conn
// and closes the old one (leasemanager.go:56-64), so the old conn's death fires a
// severance notice for the OLD generation -- after the new lease is already live. Keyed
// only by session, that notice shuts the gate on a lease the daemon is holding open, and
// the user's keyboard dies for no reason they can see. The gateway carries the dead
// generation (s11_lease_notice_test.go) precisely so this side can tell them apart.
//
// The relay makes this worse than a race: it may reorder, so the stale notice can arrive
// long after the new confirmation.
func TestS11Lease_ASupersededGenerationsSeveranceDoesNotKillTheLiveLease(t *testing.T) {
	now := time.Now()
	exp := now.Add(TakeControlTTL)
	l := s11Leased(t, exp) // generation 42

	// A second take_control supersedes it.
	const nextOp = "op-take-s11-2"
	l.Requested(s11Session, nextOp, exp)
	l.Apply(schema.Control{
		Op: protocol.OpLease, SessionID: s11Session, OperationID: nextOp,
		Generation: s11Gen + 1, ExpiresAt: &exp,
	})

	// The OLD conn's death is announced, late.
	l.Apply(s11Severance("superseded")) // carries generation s11Gen

	if err := l.Require(s11Session, now); err != nil {
		t.Fatalf("Require after a severance for the SUPERSEDED generation %d = %v, want nil -- the live generation is %d, and killing it here is a keyboard that dies for no visible reason every time the user re-takes control", s11Gen, err, s11Gen+1)
	}

	// MUTATION CONTROL: a severance for the LIVE generation must still shut the gate, or
	// the generation check has simply disabled the notice.
	l.Apply(schema.Control{
		Op: protocol.OpDetach, SessionID: s11Session, OperationID: nextOp,
		Generation: s11Gen + 1, Error: "session exited",
	})
	if err := l.Require(s11Session, now); !errors.Is(err, ErrNoLease) {
		t.Fatalf("Require after a severance for the LIVE generation = %v, want ErrNoLease -- the generation check must discriminate, not suppress", err)
	}
}

// TestS11Lease_IsNeverDurable is PB-INPUT-2's process-death event. A lease restored from
// disk names a daemon connection that cannot exist, so the only correct durable
// representation is none at all.
func TestS11Lease_IsNeverDurable(t *testing.T) {
	dir := t.TempDir()

	// PB-KEY-9: Resume fails closed with no sealer, and the simulated process death below
	// must present the SAME KEKs -- a different KEK is a different device.
	wake, content := s14aNewSealer(t), s14aNewSealer(t)
	c1, err := Resume(Config{Dir: dir, Machine: "m1", WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	exp := time.Now().Add(TakeControlTTL)
	c1.Leases().Requested(s11Session, s11TakeOp, exp)
	c1.Leases().Apply(s11Confirmation(&exp))
	if err := c1.Leases().Require(s11Session, time.Now()); err != nil {
		t.Fatalf("setup: the lease is not held before the simulated process death: %v", err)
	}

	// SIGKILL: a second Resume over the same directory is the new process.
	c2, err := Resume(Config{Dir: dir, Machine: "m1", WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatalf("Resume after process death: %v", err)
	}
	if err := c2.Leases().Require(s11Session, time.Now()); !errors.Is(err, ErrNoLease) {
		t.Fatalf("Require after process death = %v, want ErrNoLease -- the lease was persisted, so the restarted phone would type against a daemon connection that died with the old process", err)
	}
}

// TestS11Lease_TheInboundPathFeedsTheLeaseState is the wiring fence, and it is the
// assertion that keeps every test above from being a fence around an unreachable subject.
// The gateway seals its confirmation and its severance notice onto the SHARED
// machine -> phone mailbox as command_reply frames; if nothing routes them into the lease
// state, the state machine is correct and permanently empty, and PB-INPUT-2's suppression
// never lifts (or never engages).
//
// It runs through AcceptCommit -- the DURABLE production entry point -- not through
// Accept, because a wiring that existed only on the non-durable path would leave the real
// phone unwired.
func TestS11Lease_TheInboundPathFeedsTheLeaseState(t *testing.T) {
	c, key, epoch := s11ResumedCore(t)

	exp := time.Now().Add(TakeControlTTL)
	c.Leases().Requested(s11Session, s11TakeOp, exp)

	// The gateway's confirmation, sealed exactly as remotegw does it.
	if _, err := c.Router().AcceptCommit(s11SealReply(t, key, epoch, 1, s11Confirmation(&exp)), 1); err != nil {
		t.Fatalf("AcceptCommit(confirmation): %v", err)
	}
	if err := c.Leases().Require(s11Session, time.Now()); err != nil {
		t.Fatalf("Require after the gateway's lease confirmation arrived on the mailbox = %v, want nil -- the inbound path does not feed the lease state, so PB-INPUT-2's suppression never lifts and no keystroke is ever sendable", err)
	}

	// ... and the severance notice shuts it again, on the same path.
	if _, err := c.Router().AcceptCommit(s11SealReply(t, key, epoch, 2, s11Severance("daemon connection closed")), 2); err != nil {
		t.Fatalf("AcceptCommit(severance): %v", err)
	}
	if err := c.Leases().Require(s11Session, time.Now()); !errors.Is(err, ErrNoLease) {
		t.Fatalf("Require after the severance notice arrived = %v, want ErrNoLease -- the notice is delivered and ignored, so the phone keeps typing into a released lease", err)
	}
}

// ---------------------------------------------------------------------------
// PB-INPUT-3 -- TTL by op class, and expiry with a defined UX
// ---------------------------------------------------------------------------

// TestS11TTL_ByOpClassResolvesTheThreeWayCollision pins §6.0's exception. PB-INPUT-3
// records that the lease is the EARLIEST of now+maxControlSessionTTL (30 m),
// now+TTLSeconds and the device-signed ExpiresAt -- so a blanket 1-minute signed horizon
// makes the real lease 60 s, colliding with PB-INPUT-5's >= 60 s sustained-typing test and
// §6.0's 60 s biometric freshness. §6.0 resolves it by signing take_control at 15 minutes
// while ordinary commands stay at 1 minute.
//
// Today mobile/app.go:33 signs EVERY command, take_control included, at commandTTL =
// 2 minutes: over §6.0's 1-minute command TTL and far under its 15-minute take_control
// exception. Both halves are wrong, in opposite directions.
func TestS11TTL_ByOpClassResolvesTheThreeWayCollision(t *testing.T) {
	if CommandTTL != time.Minute {
		t.Errorf("CommandTTL = %v, want 1m (§6.0, PB-TIME-1)", CommandTTL)
	}
	if TakeControlTTL != 15*time.Minute {
		t.Errorf("TakeControlTTL = %v, want 15m (§6.0's stated exception, PB-INPUT-3)", TakeControlTTL)
	}
	// The exception only works if it clears the walls it was written to clear.
	if TakeControlTTL <= time.Minute {
		t.Errorf("TakeControlTTL %v does not clear the 60s biometric-freshness and sustained-typing walls it exists to clear", TakeControlTTL)
	}
	// ... and stays under the server cap, or it is silently clamped and the phone's
	// countdown is wrong from the first second.
	const maxControlSessionTTL = 30 * time.Minute // internal/protocol/server.go:156 (unexported)
	if TakeControlTTL > maxControlSessionTTL {
		t.Errorf("TakeControlTTL %v exceeds the daemon's %v cap, so the daemon clamps and the phone's displayed expiry is wrong", TakeControlTTL, maxControlSessionTTL)
	}

	cases := []struct {
		action string
		want   time.Duration
	}{
		{schema.ActionTakeControl, TakeControlTTL},
		{schema.ActionKill, CommandTTL},
		{schema.ActionDelete, CommandTTL},
		{schema.ActionLaunch, CommandTTL},
		{schema.ActionDeviceRevoke, CommandTTL},
		{schema.ActionTerminalWatch, CommandTTL},
	}
	for _, tc := range cases {
		if got := CommandTTLFor(tc.action); got != tc.want {
			t.Errorf("CommandTTLFor(%q) = %v, want %v", tc.action, got, tc.want)
		}
	}
	// Non-vacuity: the function must actually DISCRIMINATE. A constant function returning
	// TakeControlTTL for everything would satisfy the take_control row above.
	if CommandTTLFor(schema.ActionTakeControl) == CommandTTLFor(schema.ActionKill) {
		t.Fatal("CommandTTLFor returns the same TTL for take_control and kill; §6.0's whole point is that they differ")
	}
}

// TestS11Lease_SurvivesWellPastSixtySeconds is PB-INPUT-3's acceptance criterion. The
// number 60 is not incidental: it is where three independent walls used to collide, and
// PB-INPUT-5's sustained-typing test sits exactly on it.
func TestS11Lease_SurvivesWellPastSixtySeconds(t *testing.T) {
	now := time.Now()
	exp := now.Add(TakeControlTTL)
	l := s11Leased(t, exp)

	for _, elapsed := range []time.Duration{61 * time.Second, 2 * time.Minute, 10 * time.Minute} {
		if err := l.Require(s11Session, now.Add(elapsed)); err != nil {
			t.Fatalf("Require %v into the session = %v, want nil -- a typing session must survive well past the 60s wall (PB-INPUT-3)", elapsed, err)
		}
	}

	// MUTATION CONTROL: a Require that never looks at the clock passes every assertion
	// above. A lease whose horizon really is short must really expire.
	short := NewLeaseState()
	short.Requested(s11Session, s11TakeOp, now.Add(30*time.Second))
	shortExp := now.Add(30 * time.Second)
	short.Apply(s11Confirmation(&shortExp))
	if err := short.Require(s11Session, now.Add(61*time.Second)); !errors.Is(err, ErrLeaseExpired) {
		t.Fatalf("Require 61s into a 30s lease = %v, want ErrLeaseExpired -- Require is not reading the clock, so the survival assertions above prove nothing", err)
	}
}

// TestS11Lease_ExpiryIsDistinctFromAbsenceAndFromSilentLoss is PB-INPUT-3's other half.
// The requirement's words are "defined UX rather than silent keystroke loss", so the two
// things a caller must be able to tell apart are asserted directly: an EXPIRED lease is
// not the same condition as never having had one, and neither is silence.
func TestS11Lease_ExpiryIsDistinctFromAbsenceAndFromSilentLoss(t *testing.T) {
	now := time.Now()
	exp := now.Add(TakeControlTTL)
	l := s11Leased(t, exp)

	err := l.Require(s11Session, exp.Add(time.Millisecond))
	if !errors.Is(err, ErrLeaseExpired) {
		t.Fatalf("Require one millisecond past the lease horizon = %v, want ErrLeaseExpired -- an expired lease that reports nothing loses the user's keystrokes silently", err)
	}
	if errors.Is(err, ErrNoLease) {
		t.Fatal("ErrLeaseExpired matches ErrNoLease; the two conditions have different UX (re-authorize vs. take control again) and must be distinguishable")
	}
	// Exactly AT the horizon is still live: the daemon's own comparison is `expiry`
	// as a deadline, and a phone that gave up a millisecond early would refuse a
	// keystroke the machine would have accepted.
	if err := l.Require(s11Session, exp); err != nil {
		t.Fatalf("Require exactly at the horizon = %v, want nil", err)
	}
}

// TestS11Lease_TheMachinesExpiryWinsOverThePhonesSignedHorizon. The phone signs an
// ExpiresAt; the daemon may clamp the lease shorter (TTLSeconds, the 30-minute cap). If a
// confirmation ever carries the machine's value, it is the authority -- the phone's own
// horizon is only ever an upper bound. Pinning the precedence now is what stops a later
// slice from wiring the daemon's expiry through and having it silently ignored.
func TestS11Lease_TheMachinesExpiryWinsOverThePhonesSignedHorizon(t *testing.T) {
	now := time.Now()
	signed := now.Add(TakeControlTTL)
	clamped := now.Add(2 * time.Minute) // what the machine actually granted

	l := NewLeaseState()
	l.Requested(s11Session, s11TakeOp, signed)
	l.Apply(s11Confirmation(&clamped))

	if err := l.Require(s11Session, now.Add(3*time.Minute)); !errors.Is(err, ErrLeaseExpired) {
		t.Fatalf("Require 3m in, against a machine-granted 2m lease = %v, want ErrLeaseExpired -- the phone's own 15m signed horizon must not override the machine's authority", err)
	}
	got, ok := l.Lease(s11Session)
	if !ok || !got.ExpiresAt.Equal(clamped) {
		t.Fatalf("Lease().ExpiresAt = %v (ok=%v), want the machine's %v", got.ExpiresAt, ok, clamped)
	}
}

// ---------------------------------------------------------------------------
// Ctrl-C -- Stop is a keystroke, and it inherits every rule above
// ---------------------------------------------------------------------------

// TestS11Stop_IsAKeystrokeAndInheritsBothInputRules pins the resolution recorded in
// docs/verification/remote-phaseB-progress.md:228-239: there is deliberately NO interrupt
// wire verb, because an interrupt IS byte 0x03 through the input plane, which a PTY in
// default ISIG mode turns into SIGINT. The two consequences that resolution names are
// exactly the two asserted here.
//
// It matters that this is a test and not a comment: mobile.Interrupt currently records a
// local refusal ("interrupt has no signed wire action"), so the surface exists and does
// nothing. Whatever slice wires it must wire it to THESE two rules.
func TestS11Stop_IsAKeystrokeAndInheritsBothInputRules(t *testing.T) {
	const ctrlC = 0x03

	// (i) Stop requires the lease, like any other keystroke.
	l := NewLeaseState()
	if err := l.Require(s11Session, time.Now()); !errors.Is(err, ErrNoLease) {
		t.Fatalf("Stop with no lease = %v, want ErrNoLease -- Ctrl-C rides the input plane, so it is gated by the lease exactly as typing is", err)
	}

	// (ii) Stop rides the LIVE-ONLY path: offline it resolves to "delivery unknown / not
	// sent" and is NEVER queued. A Stop that lands minutes later signals a process the
	// user has long since given up on -- possibly a different one at that PTY.
	clk := s11NewClock()
	c := NewInputCoalescer(clk.now)
	frames := c.Type(s11Session, []byte{ctrlC})
	if len(frames) != 1 || len(frames[0].Data) != 1 || frames[0].Data[0] != ctrlC {
		t.Fatalf("Ctrl-C emitted %+v, want a single 1-byte 0x03 data frame", frames)
	}
	c.Fail(frames[0], "transport: not delivered; no live connection")

	if got := c.Undelivered(); len(got) != 1 {
		t.Fatalf("Undelivered() = %d entries after an offline Stop, want 1 -- the resolution is \"delivery unknown / not sent\", surfaced, never silent", len(got))
	}
	clk.advance(5 * time.Minute)
	if late := append(c.Due(), c.Flush()...); len(late) != 0 {
		t.Fatalf("an offline Stop was replayed %d frames later; SIGINT must never be queued (ADR-007 D7)", len(late))
	}
}
