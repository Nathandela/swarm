package relay

import (
	"context"
	"crypto/rand"
	"errors"
)

// GenericPushAlert is the fixed, content-free outer alert the relay attaches to
// every push. It never carries a routing id, session id, or command text — the
// real content is the opaque ciphertext the app decrypts on-device (R-REL.5).
const GenericPushAlert = "You have a new secure message."

// PushEnvelopeSize is the ONE ciphertext length the relay puts on the push channel.
//
// It is a SCHEMA, not a bound, and that distinction is the requirement. PB-PUSH-3 concedes
// that the provider observes token, timing and SIZE — and a conceded disclosure is benign
// only while it is CONSTANT. That property is quantified over the CHANNEL, so it is a promise
// the RELAY keeps and not one the gateway can keep alone: every producer reaching the provider
// must agree on one number, or the provider separates WHAT happened from THAT something
// happened without touching crypto — the exact defeat the two-tier wake/content key split
// exists to prevent (PB-KEY-2, ADR-007 B87, residual 4.23). A bound would not do it: the
// disclosure is a DIFFERENCE, not a magnitude, so a cap leaves every shape below it separable.
//
// WHY THE RELAY REFUSES RATHER THAN PADS. It forwards the opaque envelope BYTE FOR BYTE and
// has to keep doing so (TestPush_RelaySeesOnlyCiphertext): padding would make the relay a party
// that edits ciphertext, and truncating would silently destroy a wake the phone then cannot
// open — with the loss surfacing as an AEAD failure on the one background path the phone has.
// Refusal is the only normalisation available to a party that holds no key, and a refused push
// puts nothing on the channel and so discloses nothing.
//
// THE NUMBER is crypto's 62-byte cleartext envelope header plus a 16-byte AEAD tag over an
// EMPTY plaintext — the same constant as remotegw.PushWakeEnvelopeSize, which is the
// PRODUCER's side of it. This package cannot import remotegw (remotegw imports this one), so
// the two are pinned to each other by test rather than by the compiler: the reachability arm of
// TestPBPUSH3_AnUnschemadTriggerEnvelopeIsTheSameSizeOnTheChannel drives a
// remotegw.PushWakeEnvelopeSize envelope through push_trigger and requires it DELIVERED, so a
// relay that disagreed with the gateway would refuse every real wake and fail there.
const PushEnvelopeSize = 78

// silentWakeCover is the ciphertext the presence sweep carries.
//
// The sweep is a LIVENESS signal rather than a wake: it says "this machine's socket dropped",
// it carries no epoch coordinate, and the relay holds NO key it could seal one with — the
// two-tier design exists precisely so that it does not. There is therefore nothing to seal and
// nothing to shorten; the only thing left to equalise is the number of bytes, and this is that
// number of bytes. Before this the sweep sent NO ciphertext at all, which is a 104-byte
// separation on the wire from a gateway wake, readable with no adversary and no key, shipping
// in normal operation and meaning exactly "the machine went silent".
//
// THEY ARE RANDOM, and a fixed filler would have been the wrong answer for a reason a size
// fence cannot see: the provider reads the payload, not merely its length, so a constant filler
// separates the sweep from a wake on CONTENT the moment size stops separating them — the same
// disclosure one layer down, with a green test over it.
//
// WHAT IT DOES NOT BUY, stated so nobody reads it as more. A genuine wake's envelope header is
// CLEARTEXT — version, type, epoch id, seq and issued_at (ADR-007 B70-Q1/B77, a recorded trade
// the replay window rests on) — so a provider that PARSES rather than measures can still tell a
// wake from this. Closing that needs either a key the relay must not hold or dropping the
// sweep's push entirely, and both are decisions above this seam.
func silentWakeCover() []byte {
	b := make([]byte, PushEnvelopeSize)
	// crypto/rand.Read fills b entirely or crashes the program; it has returned no error to
	// its caller since Go 1.24. A branch here would be a branch that cannot run.
	_, _ = rand.Read(b)
	return b
}

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
