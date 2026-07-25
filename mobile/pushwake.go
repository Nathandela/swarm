package swarmmobile

// The phone's push RECEIVER (PB-PUSH-4), and the one facade verb the Android
// FirebaseMessagingService calls.
//
// WHAT THE ANDROID SIDE HAS, and why the parameter is a string. The relay's FCM sender emits
// a DATA-ONLY message whose data block carries exactly one key -- `"e"`, the base64 of the
// opaque envelope (internal/remote/push/fcm.go marshalMessage). Data-only is deliberate:
// a `notification` block would be rendered by the SYSTEM, on the lock screen, from text the
// PROVIDER composed, which is precisely the rendering PB-PUSH-4 puts under the app's control.
// So Kotlin holds a String and hands it here unchanged; it does no base64, no parsing and no
// crypto, and there is nothing on this seam for a Kotlin re-implementation to drift from.
//
// WHAT THIS VERB IS FOR. Three things, in order, and every one of them is a refusal the app
// cannot make for itself:
//
//  1. Is this wake genuine? Only the epoch wake key answers, and it is inside the Go core.
//  2. Is it fresh, and is it new? PB-PUSH-3's TTL and replay window (phonecore.AcceptWake).
//  3. May the app render anything beyond the constant? Only if the CONTENT tier is open,
//     which on a wake means the user has authenticated since the last lock (PB-KEY-2).
//
// A refused wake renders NOTHING. That is not tidiness: the relay handles every wake and can
// replay one, so a receiver that notified first and validated later would hand the relay a
// button that puts arbitrary-timing notifications on the owner's lock screen.

import (
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// WakeNotificationText is the CONSTANT the app renders for a wake, and it is a constant in
// the strict sense: it does not vary with the session, the machine, the transition that
// caused the wake, or how many were coalesced into it.
//
// It is owned HERE rather than in Kotlin for the reason PB-SAS-1 gives for the emoji table --
// a second copy in Kotlin is a copy that drifts, and this one drifts towards saying more.
// There is nothing in the payload to render even if the app wanted to: the wake is a constant
// 78 bytes over an EMPTY plaintext with both key ids zeroed (ADR-007 B20). So the reachable
// defect is not a leaky payload, it is an app that goes and FETCHES content to fill the
// notification in, and a constant with no inputs is the shape that cannot.
const WakeNotificationText = "Swarm has an update for you."

// WakeAlert is what one authenticated wake entitles the app to render.
//
// It carries no session id, no machine id, no group and no count, and it must not grow one:
// every field here is provider-adjacent by construction -- it is the thing the app renders
// while the device may still be locked and the notification may still be on the lock screen.
type WakeAlert struct {
	// Text is WakeNotificationText. It is returned rather than merely documented so the
	// Kotlin side has one string to render and no branch to get wrong.
	Text string

	// ContentReady reports whether the CONTENT tier is open -- i.e. whether the user has
	// authenticated. It is FALSE on every wake that arrives with the screen locked, which is
	// the case the wake exists for, and it is the only thing that may unlock a
	// content-bearing notification (PB-PUSH-4, PB-KEY-2).
	//
	// It is a report, not permission granted by this call: the app still has to go and read
	// content through the ordinary verbs, and each of those goes through custody on its own.
	ContentReady bool
}

// WHICH CLASS THE REAL ERRORS TAKE, stated here because getting one of them wrong sends a
// healthy user to their machine and there is no test that can notice prose:
//
//   - a payload that is not a parseable envelope, fails the AEAD, or is a replay or past its
//     TTL -> ErrClassInvalidRequest. Nothing is rendered and nothing is retried; the relay
//     sent something this phone will not act on.
//   - a phone that is PAIRED and has NO EPOCH WAKE KEY YET -> ErrClassAwaitingKey, and never
//     ErrClassGrantLost. PB-APP-10 gained this third state at e1ab559 precisely because the
//     two are indistinguishable on screen otherwise. The wake path is where the confusion is
//     most reachable: a phone woken by a push during the first-pairing window has no key, no
//     user present, and no way to tell the difference from a key that is gone -- and the
//     remedy for this one is WAITING (the gateway re-appends its sidecar every session, so it
//     self-heals), while ErrClassGrantLost's remedy is one the user cannot perform at all.
//   - the content tier refusing -> not an error. It is ContentReady=false on a returned
//     WakeAlert; a locked phone is the NORMAL case for a wake, not a failure.

// HandlePushWake authenticates one push wake and reports what the app may render.
//
// payload is the base64 the FCM data block carried under key "e", unchanged.
//
// IT MUST NOT REQUIRE Start. A push-woken process has no relay connection and has not been
// through a screen: it is a fresh Android process whose only job, for now, is to decide what to
// put on the lock screen. A receiver that refused with errNotRunning would refuse every wake
// that mattered, which is why the conformance tests drive it on an App that was never started.
//
// It must work with the content tier LOCKED -- that is the only state a wake ever arrives in
// -- and it must ask for the content KEK exactly zero times while doing so. The wake key is
// wake tier and opens with no user present; the content tier is biometric-gated and refuses,
// and a receiver that asks anyway either fails on a locked handset or, worse, succeeds on one
// the user happened to unlock a minute ago, which is how the wake path silently acquires a
// dependency nobody notices until it is on a real device.
func (a *App) HandlePushWake(payload string) (alert *WakeAlert, err error) {
	defer barrier(&err)
	core, err := a.ready()
	if err != nil {
		return nil, err
	}
	raw, derr := base64.StdEncoding.DecodeString(payload)
	if derr != nil {
		// ErrClassInvalidRequest and not ErrClassInternal: the bytes come from the push provider
		// by way of Kotlin, so a truncated data field or a message from a stale project id
		// reaches here as ordinary input rather than as a bug in this app.
		return nil, classed(ErrClassInvalidRequest, fmt.Errorf(
			"swarmmobile: the push payload is not the standard base64 the FCM data block carries: %w", derr))
	}
	if werr := core.AcceptWake(raw); werr != nil {
		if errors.Is(werr, phonecore.ErrNoWakeKey) {
			return nil, classed(ErrClassAwaitingKey, werr)
		}
		return nil, classed(ErrClassInvalidRequest, werr)
	}
	// ContentReady is read from state this process ALREADY holds, which is what makes the
	// answer cost ZERO content-tier unwraps (PB-KEY-2). The epoch content key is unsealed once,
	// at Resume, under the auth-gated KEK; a process woken by a push was refused there and holds
	// the zero value, and one the user has authenticated to holds the key. So "is the content
	// tier open" is already answered in memory, and asking custody again would be a second
	// unwrap on the one path that must perform none -- one that fails on a locked handset and,
	// worse, SUCCEEDS on one the user unlocked a minute ago.
	return &WakeAlert{
		Text:         WakeNotificationText,
		ContentReady: core.State().Keys.ContentKey != (crypto.ContentKey{}),
	}, nil
}
