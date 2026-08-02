// Package swarmmobile is the ONE gomobile-bound surface of the swarm phone client
// (PB-BIND-1). Everything the Android app can reach is declared here; the core --
// internal/phonecore, the frozen internal/remote/crypto, the relay client and the
// pairing handshake -- stays internal and is never bound.
//
// # Shape of the contract
//
// gomobile's binder maps a narrow slice of Go. This package therefore carries ONLY
// facade-local types and gomobile primitives: no map, no slice except []byte, no
// unsigned integer, no cross-package type. A collection crosses as an opaque HANDLE
// with Count/At (SessionList, JournalPage) because there is no bound list type, a
// terminal grid crosses as Snapshot.Text rather than a []string, and every uint32 /
// uint64 coordinate crosses as int64. The pinned surface lives in
// testdata/exported_surface.golden and every element is traced to a screen in
// screen_coverage.tsv.
//
// EVERY exported function and method returns an error as its LAST result, including
// the ones whose answer is a single bool. That is not decoration: a panic crossing JNI
// is not catchable in Java -- the runtime aborts the app process -- so every entry
// point opens with a deferred recover, and a recovered panic with nowhere to go would
// be indistinguishable from success.
//
// # Threading and lifecycle (PB-BIND-6)
//
// Every exported method is safe to call from any thread. Start and Stop are
// idempotent: calling either twice is a no-op, and they may be called concurrently
// with any other method (Android will call Stop from a lifecycle callback while a UI
// thread is mid-Peek). Close releases the durable state handle for good; a closed App
// refuses every further call.
//
// Callbacks arrive on a Go goroutine that this package owns, never on the caller's
// thread and never on the Android main looper. The UI MUST marshal each event onto its
// own thread (Activity.runOnUiThread / Handler.post / Dispatchers.Main) before touching
// a view. OnEvent must not block: the queue between the core and the listener is bounded
// at CallbackQueueSize = 256 events and overflows DROP-OLDEST, so a listener that stalls
// loses the oldest events rather than stalling the relay drain behind it. An overflow is
// never silent -- the listener is handed an Event with Kind "overflow" whose Dropped
// field counts the events discarded since the previous one.
//
// # Key custody (PB-KEY-1, ADR-007 B8)
//
// Key material crosses this boundary in ONE DIRECTION ONLY -- Java to Go -- in two
// shapes, both of them the same artifact: a transient per-tier data key that the Java
// side unwrapped with an authenticated-Keystore AES KEK.
//
//   - InstallWakeKey and InstallContentKey take the EPOCH key of their tier, as method
//     parameters. No exported method ever RETURNS raw bytes -- Go hands back sealed
//     blobs, public keys and signatures, never key material -- and the caller must
//     zeroize its copy of the byte array as soon as the call returns.
//   - KeyCustody, which NewApp requires, supplies the KEK that seals the phone's own key
//     material AT REST (PB-KEY-9, PB-SEC-1). It is REVERSE-BOUND, so its directions are
//     the mirror image of the above: Go is the caller, a result travels Java to Go
//     (inbound) and a parameter would travel Go to Java (outbound). Its two methods
//     therefore RETURN []byte and accept none, and Go zeroizes what they hand it as soon
//     as the cipher is built. The shape deliberately NOT used is a reverse-bound
//     Seal/Open pair: sealing needs the plaintext device private scalars, so handing them
//     out would be an outbound key crossing however tidy it looks.
//
// There is no constructor that omits KeyCustody. An App with no custody would write the
// phone's key material at rest with nothing over it, and reaching cleartext by forgetting
// an argument is exactly what ADR-007 B18(c) decided must not be possible.
//
// KeyCustody's methods are called PER OPERATION and their answers are never memoized:
// that is what makes the content tier's gate real, because an auth-gated Keystore key
// re-checks authorisation on every unwrap and a cached answer would keep decrypting
// content after the screen locked (PB-KEY-7).
//
// PurgeKeys and UnlockContent are PB-KEY-7's lock and its recovery, and they are a PAIR: a
// lock with no way back is a brick, and a way back with no lock is nothing. PurgeKeys
// zeroizes the live epoch CONTENT key, unbinds the router from it, and drops the decrypted
// session/snapshot/reply caches from memory and from disk. It deliberately leaves the SEALED
// content key at rest and does not touch the wake tier at all (ADR-007 B35): the handset holds
// no other source for either, so destroying the first is PB-KEY-3's terminal state at the
// first screen lock and destroying the second stops the phone being wakeable. UnlockContent is
// the fresh Keystore unwrap that restores content operations, and it is the round trip at
// which PB-SEC-2's 60-second window is enforced -- the content KEK is provisioned with
// setUserAuthenticationParameters(60, AUTH_BIOMETRIC_STRONG), so it answers only while the
// device has authenticated inside that window and otherwise returns the reauth verdict.
//
// An error from PurgeKeys means the decrypted caches AT REST survived (a full disk, a
// read-only data directory); everything in memory is purged either way, because zeroizing
// cannot fail and PB-KEY-7 names it first. Retrying is worthwhile, and re-locking is not
// required.
//
// SendInput is the only other []byte parameter. Keystrokes are sensitive but are not KEY
// material, and they are inbound-only too.
package swarmmobile

// CallbackQueueSize is the bound on the queue between the Go core and the app's
// EventListener (requirements 6.0). It is stated here so the app can size its own
// buffers against the same number the implementation enforces.
const CallbackQueueSize = 256
