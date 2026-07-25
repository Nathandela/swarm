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
// Exactly one secret crosses this boundary, and it crosses INBOUND ONLY:
// InstallWakeKey and InstallContentKey take a transient per-tier data key that the Java
// side unwrapped with an authenticated-Keystore AES KEK. No exported method ever
// RETURNS raw bytes -- Go hands back sealed blobs, public keys and signatures, never key
// material -- and the caller must zeroize its copy of the byte array as soon as the call
// returns. PurgeKeys is PB-KEY-7's lock purge: it zeroizes the installed tier keys and
// drops every decrypted cache, and is recoverable by re-installing the tier key (a
// screen lock must not brick the app).
//
// SendInput is the only other []byte parameter. Keystrokes are sensitive but are not KEY
// material, and they are inbound-only too.
package swarmmobile

// CallbackQueueSize is the bound on the queue between the Go core and the app's
// EventListener (requirements 6.0). It is stated here so the app can size its own
// buffers against the same number the implementation enforces.
const CallbackQueueSize = 256
