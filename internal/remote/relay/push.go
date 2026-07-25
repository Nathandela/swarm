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
