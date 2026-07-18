package relay

import "context"

// GenericPushAlert is the fixed, content-free outer alert the relay attaches to
// every push. It never carries a routing id, session id, or command text — the
// real content is the opaque ciphertext the NSE decrypts on-device (R-REL.5).
const GenericPushAlert = "You have a new secure message."

// APNsPayload is the outer push the relay hands to the APNs sink: a generic
// alert plus the opaque ciphertext envelope. The relay cannot read the
// ciphertext and never derives the alert from it.
type APNsPayload struct {
	Alert      string
	Ciphertext []byte
}

// APNsSink is the push transport (real APNs deferred, R-REL.5). The relay only
// ever hands it a generic outer alert and ciphertext.
type APNsSink interface {
	Push(ctx context.Context, token string, p APNsPayload) error
}
