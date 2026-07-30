package relay

import (
	"context"
	"errors"
)

// GenericPushAlert is the fixed, content-free outer alert the relay attaches to
// every push. It never carries a routing id, session id, or command text — the
// real content is the opaque ciphertext the app decrypts on-device (R-REL.5).
const GenericPushAlert = "You have a new secure message."

// PushPayload is the outer push the relay hands to the push sink: a generic
// alert plus the opaque ciphertext envelope. The relay cannot read the
// ciphertext and never derives the alert from it.
type PushPayload struct {
	Alert      string
	Ciphertext []byte
}

// maxPushTokenLen bounds the push token ONE registration may store (round-4 threat
// review C1). It is ADR-007 B61's rule applied to the bucket B61 did not reach.
//
// WHY A BOUND AT ALL, AND WHY IT IS THE SEVERE ONE. handleTokenRegister applied no length
// or format check, so the only bound was MaxFrame — one registration wrote up to a
// megabyte durably, and bbolt never returns freed pages to the OS. The op limit does not
// bind it either: meterOp keys by "rid:"+rid and relay auth is OPEN REGISTRATION, so a
// fresh identity per call gets a fresh window. Measured at ~1.79 MiB of unreclaimable
// relay.db per call, ~1.5 TB/day from one source, with a strictly cheaper precondition
// than B61's ship blocker — no consent signature, no pairing, no victim.
//
// The disk is the lesser half. New -> loadTokens hydrates the ENTIRE tokens bucket into a
// map at construction and fails CLOSED on an unreadable bucket (deliberately: booting with
// an empty map looks exactly like a fleet that never registered). So a filled store is not
// a disk problem that degrades — it is an OOM on every START, a crash loop whose only
// recovery is deleting relay.db, which destroys every legitimate pairing edge, consent and
// token on that relay. A bound on the row is what makes the resident cost of a boot
// (rows x a constant the relay picked) instead of (rows x whatever was sent).
//
// IT IS A LENGTH BOUND AND NOT A FORMAT BOUND, ON B61's PRECEDENT AND FOR ITS REASON. A
// push token is an OPAQUE PROVIDER LABEL: the relay hands it back to the sink verbatim and
// has no business having an opinion about its alphabet. Nothing here is FCM-specific
// either — PushSink is transport-neutral (see its own note), and an APNs token is 64 or
// 200 hex characters where an FCM registration token is ~152-184 today.
//
// WHY 4096 AND NOT THE ~200 THAT PRODUCTION SENDS. Google publishes no maximum for a
// registration token; the largest figure ever named for one is 4K, in the same GCM thread
// where an engineer points out that 4K is the PAYLOAD limit and the token's size "has no
// relation" to it, and the practical advice there is to allocate 255-256 bytes. So the
// honest state of the art is: nobody upstream promises a ceiling, and the observed range
// is an order of magnitude below this one. Refusing a token a provider legitimately issued
// would silently disable push for a live handset until the user next opens the app
// (ADR-007 B16: backgrounding disconnects, so a rejected token cannot re-register on its
// own) — the failure an operator only hears about from a user who missed a hand-off. This
// bound is therefore set at the largest number anyone has ever attached to the thing,
// which refuses only sizes no provider has been reported to produce, and still converts an
// attacker-chosen megabyte into a relay-chosen 4 KiB.
//
// WHAT IT DOES NOT DO, stated because the next reader will otherwise assume it. It bounds
// the ROW, not the number of rows: one routing id still costs one row, and a routing id is
// free to mint. That is the standing root (C1/H1/M4/B69(4) all rest on it) and it is a
// larger decision than this bound; what this removes is the AMPLIFICATION — the multiplier
// the CALLER chose — leaving growth under the connection/op rate that already governs
// every other write the relay accepts. Fenced by TestC1_AnOversizedPushTokenNeverReaches-
// TheStore and TestC1_ARestartCannotBeMadeToHydrateAnUnboundedTokenMap.
const maxPushTokenLen = 4096

// PushSink is the push transport. The relay only ever hands it a generic outer
// alert and ciphertext.
//
// The name is transport-NEUTRAL on purpose (PB-PUSH-1). This seam was called
// APNsSink through Phase A while no sender existed; the transport that actually
// implements it is FCM, on Android, and there is no Apple account in the project
// at all — so the old name was a fact-shaped claim every later reader would have
// inherited. internal/remote/push.FCM is the implementation.
type PushSink interface {
	Push(ctx context.Context, token string, p PushPayload) error
}

// ErrPushUnregistered is the ONE verdict a sink returns that the relay acts on: the
// provider reports this token belongs to no live installation (FCM `UNREGISTERED`). The
// relay then PRUNES the token, since retrying it burns quota against a handset that no
// longer exists.
//
// It is a sentinel rather than a status code so a sender can wrap it with context and the
// relay still classifies via errors.Is, and it is deliberately narrow: every OTHER failure
// — a 5xx, a network drop, a bad credential — leaves the token in place. Pruning on a
// transient error silently disables push for a LIVE handset, and nothing surfaces until a
// user misses a hand-off.
var ErrPushUnregistered = errors.New("relay: push token is no longer registered with the provider")
