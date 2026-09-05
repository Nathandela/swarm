package pushreg

import (
	"crypto/sha256"
	"encoding/base64"
)

// RegistrationProofMessage returns the bytes signed by the installation key when
// registering. It binds the proof to both the retry identity and the exact transmitted
// body without copying secret body contents into the signed transcript.
func RegistrationProofMessage(idempotencyKey string, body []byte) []byte {
	sum := sha256.Sum256(body)
	out := make([]byte, 0, len("swarm-pg-register-v1||")+len(idempotencyKey)+43)
	out = append(out, "swarm-pg-register-v1|"...)
	out = append(out, idempotencyKey...)
	out = append(out, '|')
	return base64.RawURLEncoding.AppendEncode(out, sum[:])
}
