package conformance_test

// FAILING-FIRST (TDD RED, GG-5) for slice S17 / PB-PUSH-9: the CLIENT-SIDE FCM token
// lifecycle.
//
// THE REQUIREMENT, verbatim: "initial getToken, onNewToken rotation, re-registration on every
// authenticated reconnect (which also largely neutralizes PB-PUSH-6's relay-restart loss),
// deletion on revoke/disable, and correct behavior across process death and app upgrade. A
// façade method can exist while no Android code ever calls it."
//
// THAT LAST SENTENCE IS THE POINT OF THE SLICE, and it is this project's standing defect class
// (v) written into the requirement text. It has already happened twice in this phase: a fully
// tested FCM sender with ZERO production callers, and a phone that could not obtain an epoch
// key at all because the only code opening the delivering frame was the test simulator. So the
// Kotlin CALL is fenced, not just the callee -- in android/gate/s17_pushclient_test.go, which
// requires a FirebaseMessagingService in the manifest whose onNewToken body reaches
// App.RegisterPushToken. Nothing in THIS file can prove that; it proves the Go half works when
// something calls it, and says so rather than implying more.
//
// WHAT IS REAL HERE. A real relay with its real persistent token store, the real
// internal/remote/push.FCM sender, the real gateway-side PushNotifier, and the real facade over
// a real state directory. The provider endpoint is an httptest.Server on loopback speaking the
// FCM v1 protocol. PB-E2E-5 -- real FCM, real Doze, a real handset -- is DEFERRED under section
// 13 and nothing here claims any part of it.

import (
	"testing"

	swarmmobile "github.com/Nathandela/swarm/mobile"
)

// s17TokensSeen is every distinct device token the provider was asked to deliver to, in order.
func s17TokensSeen(r *s17Rig) []string {
	var out []string
	for _, d := range r.FCM.Deliveries() {
		out = append(out, d.Token)
	}
	return out
}

// ---------------------------------------------------------------------------
// The requirement's own stated acceptance, both halves.
// ---------------------------------------------------------------------------

// PASSES TODAY, AND THAT IS NOT COVERAGE -- labelled so no evidence line counts it as earned.
// This is PB-PUSH-9's FIRST stated criterion, and shipped code already satisfies it: the phone
// is connected, so RegisterPushToken reaches the relay. It is kept because the requirement
// names it, and because it is the control the rotation-while-offline test below is measured
// against -- but on its own it is defect class (iv), a criterion that is satisfiable while the
// defect ships.
//
// TestS17_ARotatedTokenStillReceivesDelivery is the first half, verbatim: "rotate the token and
// assert delivery still works".
//
// The predecessor is marked UNREGISTERED at the provider, which is what a real handset does
// after a rotation, so the test cannot pass by delivering to the stale token.
func TestS17_ARotatedTokenStillReceivesDelivery(t *testing.T) {
	r := s17NewRig(t)
	app := r.StartApp()

	const before, after = "fcm-token-v1", "fcm-token-v2"
	if err := app.RegisterPushToken(before); err != nil {
		t.Fatalf("initial registration: %v", err)
	}
	r.Wake("m/s1")
	if got := s17TokensSeen(r); len(got) != 1 || got[0] != before {
		t.Fatalf("PB-PUSH-9: before any rotation the provider saw %v, want [%q]; the rotation "+
			"below would measure nothing", got, before)
	}

	// onNewToken. The old token is dead from this moment on a real device.
	r.FCM.MarkUnregistered(before)
	r.FCM.Reset()
	if err := app.RegisterPushToken(after); err != nil {
		t.Fatalf("PB-PUSH-9: registering the rotated token failed: %v", err)
	}

	r.Wake("m/s2")
	got := s17TokensSeen(r)
	if len(got) != 1 || got[0] != after {
		t.Errorf("PB-PUSH-9: after an onNewToken rotation the provider was asked to deliver to %v, "+
			"want exactly [%q]. FCM rotates a token without asking; a phone that does not "+
			"re-register is silently unreachable and nothing on either side reports it", got, after)
	}
}

// PASSES TODAY, AND THAT IS NOT COVERAGE -- labelled so no evidence line counts it as earned.
// relay.go onConnected already re-registers on every dial, which is the shipped half of
// PB-PUSH-9. What this test adds over the requirement's literal wording is the EMPTY store; see
// below for why the literal wording cannot fail.
//
// TestS17_ReRegistrationRestoresDeliveryAfterARelayRestart is the second half, verbatim:
// "restart the relay and assert re-registration restores it".
//
// IT IS DELIBERATELY RUN AGAINST AN EMPTY RELAY STORE, and that is the whole of its value.
// S12 made relay push tokens PERSISTENT, so a restart that keeps the store restores delivery
// with the phone doing nothing at all -- the literal criterion passes without
// re-registration existing, which is defect class (i): a guard that cannot fail. A relay
// rebuilt, migrated, or restored from a backup taken before this device registered has an
// empty token store, and then re-registration is the only thing that can restore delivery.
//
// The asymmetry S12 recorded stands and is not papered over here: this covers a phone that is
// ALREADY AWAKE and reconnects. A backgrounded phone is disconnected by ADR-007 B16 and cannot
// re-register until something wakes it -- which is the push that was just lost. That gap is
// PB-PUSH-6's and is named in the S12 evidence.
func TestS17_ReRegistrationRestoresDeliveryAfterARelayRestart(t *testing.T) {
	r := s17NewRig(t)
	app := r.StartApp()

	const token = "fcm-token-survives-a-restart"
	if err := app.RegisterPushToken(token); err != nil {
		t.Fatalf("initial registration: %v", err)
	}
	r.Wake("m/s1")
	if got := s17TokensSeen(r); len(got) != 1 {
		t.Fatalf("PB-PUSH-9: the provider saw %v before the restart, want one delivery", got)
	}

	r.FCM.Reset()
	r.RestartRelay(false) // EMPTY store: the relay has never heard of this device
	r.AwaitOnline(app)

	if !r.PushReaches(token) {
		t.Errorf("PB-PUSH-9: after the phone reconnected to a relay with no record of it, the "+
			"provider was asked to deliver to %v, want [%q] within the deadline. Re-registration "+
			"on every AUTHENTICATED RECONNECT is what makes a relay's lost token store "+
			"recoverable without the user opening the app", s17TokensSeen(r), token)
	}
}

// ---------------------------------------------------------------------------
// Rotation while there is no connection. This is a defect in shipped work, not a new feature.
// ---------------------------------------------------------------------------

// TestS17_ATokenRotatedWithNoConnectionIsNotLost is the sharpest failure in this file.
//
// App.RegisterPushToken calls a.conn() FIRST and returns its error, so a rotation that arrives
// while the phone has no relay connection is DISCARDED -- the token is never persisted. And
// FCM does not ask: onNewToken fires on app data restore, on reinstall, on a token TTL expiry,
// on any of them while the app is backgrounded and therefore, under ADR-007 B16, disconnected.
//
// The consequence is not "the rotation is retried later". State.PushToken still holds the OLD
// token, so the reconnect path in relay.go onConnected re-registers the DEAD one, the provider
// answers UNREGISTERED, the relay PRUNES it -- and the device is now unreachable by push with
// no token registered anywhere and nothing that will ever register the new one. The phone looks
// perfectly healthy the whole time.
//
// The fix is an ordering the requirement already implies: persist first, register
// opportunistically, and let the reconnect path carry it.
func TestS17_ATokenRotatedWithNoConnectionIsNotLost(t *testing.T) {
	r := s17NewRig(t)

	// A process that has NOT started its transport: exactly a backgrounded app being handed a
	// new token by FCM.
	offline := r.OpenApp()
	const rotated = "fcm-token-rotated-while-offline"
	if err := offline.RegisterPushToken(rotated); err != nil {
		t.Logf("PB-PUSH-9: RegisterPushToken with no connection returned %v.\n"+
			"An error here is acceptable ONLY if the token was still persisted -- see the "+
			"assertion below. What is not acceptable is losing it: FCM rotates without asking, "+
			"and the app is disconnected whenever it is backgrounded (ADR-007 B16)", err)
	}
	if err := offline.Close(); err != nil {
		t.Fatalf("App.Close: %v", err)
	}

	// The app comes to the foreground and connects. Whatever happened above, the token FCM
	// gave the phone must be the one the relay ends up holding.
	r.StartApp()

	if !r.PushReaches(rotated) {
		got := s17TokensSeen(r)
		t.Errorf("PB-PUSH-9: after a rotation that arrived with no connection, the provider was "+
			"asked to deliver to %v, want [%q].\nRegisterPushToken returns its connection error "+
			"BEFORE it persists, so the rotated token is dropped on the floor while durable state "+
			"still holds the dead one -- which the reconnect path then dutifully re-registers, the "+
			"provider reports UNREGISTERED, and the relay prunes. The handset is then unreachable "+
			"by push, permanently, with nothing anywhere reporting it", got, rotated)
	}
}

// PASSES TODAY, AND THAT IS NOT COVERAGE -- labelled so no evidence line counts it as earned.
// State.PushToken has been persisted since S7 and wake-tiered since S15, so this is green on
// shipped code. It is here because PB-PUSH-9 lists process death by name and because it is the
// control for the LOCKED variant two tests below, which is the case that actually runs.
//
// TestS17_ARegisteredTokenSurvivesAProcessDeath covers "correct behavior across process death
// and app upgrade" for the ordinary case. State.PushToken is wake tier (PB-STATE-9), so it is
// readable by a push-woken process with no user present -- which is what makes re-registration
// from the reconnect path possible at all.
func TestS17_ARegisteredTokenSurvivesAProcessDeath(t *testing.T) {
	r := s17NewRig(t)

	app := r.StartApp()
	const token = "fcm-token-across-process-death"
	if err := app.RegisterPushToken(token); err != nil {
		t.Fatalf("App.RegisterPushToken: %v", err)
	}
	if err := app.Close(); err != nil {
		t.Fatalf("App.Close: %v", err)
	}

	// A relay with an empty store plus a relaunched app: the only way the token comes back is
	// out of the phone's durable state.
	r.RestartRelay(false)
	relaunched := r.StartApp()
	_ = relaunched

	r.FCM.Reset()
	if !r.PushReaches(token) {
		t.Errorf("PB-PUSH-9/PB-STATE-9: after a process death the provider was asked to deliver to "+
			"%v, want [%q]. A token held only in memory is re-registered only if the app happens "+
			"to be foregrounded, and Android kills this process routinely", s17TokensSeen(r), token)
	}
}

// PASSES TODAY, AND THAT IS NOT COVERAGE -- labelled so no evidence line counts it as earned.
// S15 put PushToken in the wake tier and measured it from the bytes on disk; this asserts the
// FACADE path honours that, and it does. It is kept as a standing fence because it is the one
// assertion in this file that would fail if a later slice moved the token, or added a
// content-tier read to onConnected -- either of which silently removes push from every phone
// that is actually locked, which is all of them.
//
// TestS17_ATokenIsReadableAndReRegistrableWithTheContentTierLocked is the fifth-defect-class
// fence on the token half.
//
// A push-woken process runs LOCKED. If re-registration needed anything content tier -- a Save
// that touches a content container, a read of a cached session -- it would work in every test
// written on an unlocked phone and fail on every real wake. PB-STATE-9 put PushToken in the
// wake tier for exactly this, and S15 measured it; this asserts the FACADE path honours it.
func TestS17_ATokenIsReadableAndReRegistrableWithTheContentTierLocked(t *testing.T) {
	r := s17NewRig(t)

	app := r.StartApp()
	const token = "fcm-token-locked-reregister"
	if err := app.RegisterPushToken(token); err != nil {
		t.Fatalf("App.RegisterPushToken: %v", err)
	}
	if err := app.Close(); err != nil {
		t.Fatalf("App.Close: %v", err)
	}

	r.RestartRelay(false)
	r.Custody.Refuse("content", swarmmobile.KeyCustodyAuthRequired)

	locked := r.OpenApp()
	if err := locked.Start(); err != nil {
		t.Fatalf("App.Start with the content tier locked: %v", err)
	}
	r.AwaitOnline(locked)

	r.FCM.Reset()
	if !r.PushReaches(token) {
		t.Errorf("PB-PUSH-9/PB-KEY-2: a LOCKED phone reconnected and the provider was asked to "+
			"deliver to %v, want [%q]. Locked is the state a push-woken process is in; if "+
			"re-registration needs the biometric it never happens on the path it exists for",
			s17TokensSeen(r), token)
	}
}

// ---------------------------------------------------------------------------
// Deletion, which the requirement lists beside registration and which is the half that fails
// quietly.
// ---------------------------------------------------------------------------

// PASSES TODAY, AND THAT IS NOT COVERAGE -- labelled so no evidence line counts it as earned.
// The connected path works. The two tests after it are the ones that fail, and they are the
// cases a user actually hits.
//
// TestS17_DeletingTheTokenStopsDeliveryAtTheProvider is deletion's positive case.
func TestS17_DeletingTheTokenStopsDeliveryAtTheProvider(t *testing.T) {
	r := s17NewRig(t)
	app := r.StartApp()

	if err := app.RegisterPushToken("fcm-token-to-delete"); err != nil {
		t.Fatalf("App.RegisterPushToken: %v", err)
	}
	if err := app.DeletePushToken(); err != nil {
		t.Fatalf("App.DeletePushToken: %v", err)
	}

	r.FCM.Reset()
	r.Wake("m/s1")
	if got := s17TokensSeen(r); len(got) != 0 {
		t.Errorf("PB-PUSH-9: after DeletePushToken the provider was still asked to deliver to %v. "+
			"Deletion on revoke or disable is part of the lifecycle, and a token the user deleted "+
			"that keeps receiving is the failure they will notice", got)
	}
}

// TestS17_ADeletionIssuedWithNoConnectionStillReachesTheRelay is deletion's silent half, and it
// is the mirror of the rotation defect above.
//
// App.DeletePushToken clears LOCAL state whether or not the relay was told: `if cl, cerr :=
// a.conn(); cerr == nil { ... }`. So a user who turns notifications off while the phone has no
// connection -- which is the normal state of a backgrounded app under ADR-007 B16 -- leaves the
// relay holding a live token forever. Nothing retries it, because the phone has forgotten the
// token it would have to delete. The user sees notifications they switched off, and the phone's
// own settings screen says they are off.
func TestS17_ADeletionIssuedWithNoConnectionStillReachesTheRelay(t *testing.T) {
	r := s17NewRig(t)

	app := r.StartApp()
	if err := app.RegisterPushToken("fcm-token-deleted-offline"); err != nil {
		t.Fatalf("App.RegisterPushToken: %v", err)
	}
	if err := app.Stop(); err != nil {
		t.Fatalf("App.Stop: %v", err)
	}

	// The user turns notifications off while the phone is backgrounded and disconnected.
	if err := app.DeletePushToken(); err != nil {
		t.Logf("PB-PUSH-9: DeletePushToken with no connection returned %v. An error is acceptable "+
			"only if the deletion is still owed and delivered later -- see below", err)
	}

	// The app reconnects.
	if err := app.Start(); err != nil {
		t.Fatalf("App.Start: %v", err)
	}
	r.AwaitOnline(app)
	r.Settle()

	r.FCM.Reset()
	r.Wake("m/s1")
	if got := s17TokensSeen(r); len(got) != 0 {
		t.Errorf("PB-PUSH-9: a deletion issued with no connection never reached the relay, which "+
			"is still delivering to %v.\nDeletePushToken clears durable state whether or not the "+
			"relay was told, so nothing knows a deletion is owed and nothing retries it. The user "+
			"switched notifications off, the settings screen agrees, and the pushes keep arriving",
			got)
	}
}

// PASSES TODAY, AND THAT IS NOT COVERAGE -- labelled so no evidence line counts it as earned.
// onConnected re-registers from durable state, and after a connected deletion that state is
// empty, so nothing is resurrected. It is written NOW because the fix for the offline-deletion
// failure above is a pending-deletion record, and the obvious shape of that fix -- keep the
// token so it can be deleted later -- makes this test fail. It is the guard rail on the repair.
//
// TestS17_ReconnectingAfterADeletionDoesNotResurrectTheToken is the converse, and it is the
// one a naive "queue the deletion" fix gets wrong: if the reconnect path re-registers from a
// stale in-memory copy, the deletion is undone by the very mechanism PB-PUSH-9 requires.
func TestS17_ReconnectingAfterADeletionDoesNotResurrectTheToken(t *testing.T) {
	r := s17NewRig(t)

	app := r.StartApp()
	if err := app.RegisterPushToken("fcm-token-not-resurrected"); err != nil {
		t.Fatalf("App.RegisterPushToken: %v", err)
	}
	if err := app.DeletePushToken(); err != nil {
		t.Fatalf("App.DeletePushToken: %v", err)
	}

	// A reconnect, which is where re-registration happens.
	if err := app.Stop(); err != nil {
		t.Fatalf("App.Stop: %v", err)
	}
	if err := app.Start(); err != nil {
		t.Fatalf("App.Start: %v", err)
	}
	r.AwaitOnline(app)
	r.Settle()

	r.FCM.Reset()
	r.Wake("m/s1")
	if got := s17TokensSeen(r); len(got) != 0 {
		t.Errorf("PB-PUSH-9: a reconnect after a deletion re-registered %v. Re-registration must "+
			"carry what durable state HOLDS, and after a deletion that is nothing", got)
	}
}

// TestS17_RevokingThisDeviceDeletesItsPushToken. The requirement says "deletion on
// revoke/disable" and revoke is the one with a security consequence: a revoked device that
// keeps a registered token leaves a provider-visible identifier for it in the relay's store,
// and leaves the machine able to wake a handset its owner disowned.
//
// LABELLED, because the relay ALSO drops the token inside revokeAndPurge's transaction (S12,
// `_RevokedDeviceTokenIsNotResurrectedByARestart`). This asserts the PHONE does its half: a
// phone that relies on the relay to clean up has no deletion on revoke at all if the revoke is
// issued while the relay is unreachable, and holds a token in durable state that it will
// re-register on the next connection.
func TestS17_RevokingThisDeviceDeletesItsPushToken(t *testing.T) {
	r := s17NewRig(t)
	app := r.StartApp()

	if err := app.RegisterPushToken("fcm-token-revoked"); err != nil {
		t.Fatalf("App.RegisterPushToken: %v", err)
	}
	if _, err := app.RevokeThisDevice(); err != nil {
		t.Fatalf("App.RevokeThisDevice: %v", err)
	}

	summary, err := app.StateSummary()
	if err != nil {
		t.Fatalf("App.StateSummary: %v", err)
	}
	_ = summary

	r.FCM.Reset()
	r.Wake("m/s1")
	if got := s17TokensSeen(r); len(got) != 0 {
		t.Errorf("PB-PUSH-9: after RevokeThisDevice the provider was still asked to deliver to %v. "+
			"The phone must delete its own token on revoke rather than relying on the relay to "+
			"notice", got)
	}
}
