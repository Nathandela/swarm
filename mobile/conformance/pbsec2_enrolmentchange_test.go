package conformance_test

// PB-SEC-2's THIRD open half, settled by running it rather than by reading it.
//
// THE FINDING (ADR-007 B61(3), narrowed by B63 and re-graded by B71(5)).
// `KeystorePerUseCiphers.cipherFor` answers `KeyPermanentlyInvalidatedException` by DROPPING the
// alias and provisioning a new gate entry against whatever biometric is now enrolled. That is a
// considered decision with its rationale written beside it -- a gate entry seals nothing, so
// there is no material to lose -- but it discards the one signal
// `setInvalidatedByBiometricEnrollment(true)` exists to raise. On its own it says: an attacker
// who enrols their own fingerprint gets a GREEN prompt for revoke, kill and launch on the next
// attempt.
//
// THE COUNTER-CLAIM, WHICH IS WHAT THIS FILE TESTS. B71(5) argued the same enrolment change
// also destroys the CONTENT KEK, which `KeystoreCustodyBootstrap.ensure` refuses to re-mint by
// explicit design -- and every gated operation needs the content tier, because
// `phonecore.sealedKeyStore` keeps the command-signing seed there (PB-KEY-5/PB-KEY-8's
// COMMAND_SIGN row) and unseals it PER OPERATION with nothing memoized. So the attacker would
// get a prompt they can satisfy for an operation that then fails closed: a UX ordering defect,
// not an authorization bypass. That reviewer stated plainly it had NOT run this and asked that
// its read not be treated as certification.
//
// This file runs it. The scenario is driven end to end -- a paired, reconciled phone, a real
// relay, a real machine-side opener -- with the content tier answering exactly as an
// enrolment-invalidated Keystore key does: `KeyCustody.ContentKEK` throwing the
// `KeyCustodyKeyInvalidated` verdict, which is what `KeystoreKeyCustody.contentKEK` propagates
// from `KeyCustodyException.KeyPermanentlyInvalidated`.
//
// WHAT IT ESTABLISHES AND WHAT IT DOES NOT.
//
//   - IT DOES establish that with the content tier permanently invalidated, no gated operation
//     is authored, none reaches the machine, and each refusal carries the PERMANENT verdict --
//     so the app tells the user to pair again rather than offering a prompt forever.
//   - IT DOES NOT establish that the gate entry is re-minted. That happens in
//     `KeystorePerUseCiphers`, which reaches `AndroidKeyStore` through a hard-coded object with
//     no seam; Robolectric provides no such provider ("AndroidKeyStore not found"), so the
//     re-mint cannot be driven on any JVM in this repository, and introducing a seam for it
//     would be prescribing the fix. The re-mint is ASSUMED here in the attacker's favour: this
//     file asks what a green prompt is WORTH, not whether it is green.
//   - IT DOES NOT establish anything about real biometrics, a real Keystore, or a real
//     enrolment change. PB-E2E-5 is DEFERRED (ADR-007 B31). What is modelled is the CONTRACT --
//     a content-tier unwrap that permanently refuses -- which is the same thing every other
//     custody test in this suite models.
//
// THESE TESTS ARE EXPECTED TO PASS AT HEAD. They are not RED: they are the evidence for a
// severity claim that has so far rested on a code read, and they are what makes the claim
// falsifiable by a later change. Their mutation is stated on each test.

import (
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	swarmmobile "github.com/Nathandela/swarm/mobile"
)

// pbsec2CommandCount is how many commands with this action have reached the machine so far.
//
// A COUNT AND NOT `sawCommand`, because the control arm of these tests deliberately performs the
// same operation BEFORE the enrolment change: a boolean would be true from the control and the
// assertion after it would be unfalsifiable.
func pbsec2CommandCount(h *harness, action string) int {
	h.t.Helper()
	h.Drain()
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, c := range h.Commands {
		if c.Action == action {
			n++
		}
	}
	return n
}

// TestPBSEC2_AnEnrolmentChangeLeavesEveryGatedOperationFailingClosed.
//
// THE MUTATION THAT MUST FAIL IT: memoizing the content tier in `phonecore.sealedKeyStore` --
// caching what `contentStore()` returns instead of unsealing per operation. That is a small,
// natural-looking change, it compiles, and it is the one that would make the green prompt worth
// something: a process that unsealed once at Resume would go on signing kills after the
// enrolment change destroyed the key. `keycustody.go` warns about exactly it in prose ("a store
// that unwrapped once would keep signing after the screen locks... while every restart-based
// test still passed"); this is the test that would notice.
//
// THE CONTROL is the first arm: the same three verbs, on the same phone, before the tier is
// invalidated. Without it, three refusals prove nothing -- a phone that refused everything for
// any unrelated reason would pass.
func TestPBSEC2_AnEnrolmentChangeLeavesEveryGatedOperationFailingClosed(t *testing.T) {
	h := newHarness(t)
	h.PushReconcile()
	eventually(t, "reconcile never adopted", func() bool {
		s, err := h.App.StateSummary()
		return err == nil && s.Reconciled
	})

	// THE CONTROL. These are the per-use operations requirements 6.0 gates, and they work on
	// this phone right now.
	if _, err := h.App.Kill(testSession); err != nil {
		t.Fatalf("the control arm failed: Kill on a healthy phone returned %v. Every refusal "+
			"below would then be proving something other than the enrolment change", err)
	}
	h.AwaitCommand(protocol.ActionKill)
	if _, err := h.App.Launch(&swarmmobile.LaunchSpec{Agent: "claude", Cwd: "/tmp", Prompt: "hi"}); err != nil {
		t.Fatalf("the control arm failed: Launch on a healthy phone returned %v", err)
	}
	h.AwaitCommand(protocol.ActionLaunch)

	before := map[string]int{
		protocol.ActionKill:         pbsec2CommandCount(h, protocol.ActionKill),
		protocol.ActionLaunch:       pbsec2CommandCount(h, protocol.ActionLaunch),
		protocol.ActionDeviceRevoke: pbsec2CommandCount(h, protocol.ActionDeviceRevoke),
	}

	// THE ENROLMENT CHANGE. A fingerprint is added or removed, the platform destroys every key
	// carrying setInvalidatedByBiometricEnrollment(true), and the content KEK is one of them.
	// The wake tier is untouched, deliberately and exactly as on a handset: its KEK sets the
	// flag false, or a re-enrolment would kill background wake (ADR-007 B9/B16).
	h.Custody.Refuse("content", swarmmobile.KeyCustodyKeyInvalidated)

	for _, tc := range []struct {
		verb   string
		action string
		call   func() (*swarmmobile.Op, error)
	}{
		{"Kill", protocol.ActionKill, func() (*swarmmobile.Op, error) {
			return h.App.Kill(testSession)
		}},
		{"Launch", protocol.ActionLaunch, func() (*swarmmobile.Op, error) {
			return h.App.Launch(&swarmmobile.LaunchSpec{Agent: "claude", Cwd: "/tmp", Prompt: "hi"})
		}},
		{"RevokeThisDevice", protocol.ActionDeviceRevoke, func() (*swarmmobile.Op, error) {
			return h.App.RevokeThisDevice()
		}},
	} {
		t.Run(tc.verb, func(t *testing.T) {
			op, err := tc.call()
			if err == nil {
				t.Fatalf("PB-SEC-2: %s SUCCEEDED after the biometric enrolment change (op %+v). "+
					"The per-use gate entry is re-minted against the NEW enrolment, so an "+
					"attacker who adds their own fingerprint passes the prompt -- and if the "+
					"operation behind it also completes, that is an authorization bypass and not "+
					"the UX ordering defect ADR-007 B71(5) re-graded it to",
					tc.verb, op)
			}
			// PERMANENT, not recoverable. A refusal classed as "authenticate again" would be a
			// prompt the user can satisfy that changes nothing, forever -- PB-KEY-6's own
			// failure -- and it would also mean the phone had not understood that the key is
			// gone.
			if !strings.Contains(err.Error(), swarmmobile.ErrClassRepairRequired) {
				t.Fatalf("%s failed with %q, want the %s class. Any other refusal means this "+
					"assertion is passing on an unrelated failure and would go on passing if the "+
					"content tier stopped gating the command at all",
					tc.verb, err, swarmmobile.ErrClassRepairRequired)
			}
			if got := pbsec2CommandCount(h, tc.action); got != before[tc.action] {
				t.Errorf("PB-SEC-2: %s was refused on the phone and a %q command still reached "+
					"the MACHINE (%d, was %d). A refusal that seals anyway is the operation "+
					"happening; the whole fails-closed claim rests on nothing being authored",
					tc.verb, tc.action, got, before[tc.action])
			}
		})
	}
}

// TestPBSEC2_AnEnrolmentChangeIsStillFailedClosedAfterARestart.
//
// The test above invalidates the tier under a RUNNING process, which is one of the two orders a
// handset produces. This is the other, and it is the one the attacker actually walks: enrol a
// fingerprint, then open the app. It matters because a fresh process re-runs `phonecore.Resume`,
// and Resume TOLERATES a locked or invalidated content tier by design (`openSealedDeviceKeys`)
// so that a phone woken by a push can still receive one. That tolerance is correct and it is
// also the thing that could hand a fresh process an unsealed identity if the fatal set were ever
// widened wrongly, so the phone must come up and REFUSE rather than come up and work.
//
// THE MUTATION THAT MUST FAIL IT is the same memoization, plus its sibling: adding
// `crypto.ErrKeyInvalidated` handling that regenerates device material instead of refusing --
// which would silently change the device identity the daemon registry pins (R-DEV.1) and hand
// the attacker a phone that signs.
func TestPBSEC2_AnEnrolmentChangeIsStillFailedClosedAfterARestart(t *testing.T) {
	h := newHarness(t)
	h.PushReconcile()
	eventually(t, "reconcile never adopted", func() bool {
		s, err := h.App.StateSummary()
		return err == nil && s.Reconciled
	})
	if _, err := h.App.Kill(testSession); err != nil {
		t.Fatalf("the control arm failed: Kill on a healthy phone returned %v", err)
	}
	h.AwaitCommand(protocol.ActionKill)
	before := pbsec2CommandCount(h, protocol.ActionKill)

	if err := h.App.Close(); err != nil {
		t.Fatalf("closing the first App: %v", err)
	}
	h.Custody.Refuse("content", swarmmobile.KeyCustodyKeyInvalidated)

	// The phone is opened again over the SAME state directory, which is what a relaunch is.
	// A failure here is not a test failure: it is an even stronger fails-closed answer, and it
	// is reported as the outcome rather than as an error.
	app, err := swarmmobile.NewApp(&swarmmobile.Config{
		StateDir: h.Dir, RelayURL: h.RelayURL, MachineID: h.Machine,
	}, h.Custody)
	if err != nil {
		t.Logf("the phone could not be opened at all after the enrolment change: %v. That is "+
			"fails-closed at the strongest point, and the assertions below have no subject", err)
		return
	}
	t.Cleanup(func() { _ = app.Close() })
	h.App = app
	_ = app.Start()

	op, err := app.Kill(testSession)
	if err == nil {
		t.Fatalf("PB-SEC-2: Kill SUCCEEDED on a phone relaunched after the biometric enrolment "+
			"change (op %+v). The gate entry is re-minted against the attacker's own enrolment, "+
			"so the prompt is theirs to pass; if the command is authored too, the enrolment "+
			"change is a full authorization bypass", op)
	}
	if !strings.Contains(err.Error(), swarmmobile.ErrClassRepairRequired) {
		t.Fatalf("Kill failed with %q, want the %s class: the key is gone and no prompt brings "+
			"it back, so the user must be told to pair again rather than to authenticate",
			err, swarmmobile.ErrClassRepairRequired)
	}
	if got := pbsec2CommandCount(h, protocol.ActionKill); got != before {
		t.Errorf("PB-SEC-2: the relaunched phone refused the Kill and a kill command still "+
			"reached the machine (%d, was %d)", got, before)
	}
}
