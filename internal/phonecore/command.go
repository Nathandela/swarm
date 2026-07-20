package phonecore

import (
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/device"
)

// CommandInput is the identity of a remote mutating op the phone authors.
type CommandInput struct {
	Action      string    // a protocol.Action* constant
	Machine     string    // target machine endpoint id
	Session     string    // namespaced session id (protocol.LaunchSessionSentinel for launch)
	OperationID string    // durable client-generated idempotency key (R-PHC.4)
	ExpiresAt   time.Time // command validity horizon
	ContentHash []byte    // optional 32-byte content binding (e.g. protocol.LaunchContentHash)
}

// SignCommand authors and signs a remote command with the device's command-signing key
// (R-PHC authoring side of R-POL.9). It builds the canonical crypto.Command tuple,
// signs it with the KeyStore, and returns the protocol.DeviceCommandAuth the phone
// sends to the gateway -> daemon. The DeviceID is derived canonically from the
// command-signing public key, matching how the daemon registry pins it (R-DEV.1), so a
// signature always verifies against exactly the record its id names.
func SignCommand(ks crypto.KeyStore, in CommandInput) (protocol.DeviceCommandAuth, error) {
	msg, err := crypto.Command{
		Action:      in.Action,
		Machine:     in.Machine,
		Session:     in.Session,
		OperationID: in.OperationID,
		ExpiresAt:   in.ExpiresAt.Unix(),
		ContentHash: in.ContentHash,
	}.Canonical()
	if err != nil {
		return protocol.DeviceCommandAuth{}, err
	}
	sig := ks.SignCommand(msg)
	return protocol.DeviceCommandAuth{
		DeviceID:    device.DeviceIDFor(ks.CommandSigningPublic()),
		Action:      in.Action,
		Machine:     in.Machine,
		Session:     in.Session,
		OperationID: in.OperationID,
		ExpiresAt:   in.ExpiresAt,
		ContentHash: in.ContentHash,
		Sig:         base64.StdEncoding.EncodeToString(sig),
	}, nil
}

// SealCommandEnvelope seals a signed command as a mailbox envelope under the epoch
// content key (XChaCha20-Poly1305), so it can travel through the untrusted relay to the
// machine as ciphertext. The command's device signature is verified by the daemon after
// the gateway opens the envelope; sealing adds confidentiality, not the command's
// authenticity (which the signature already carries). seq must be unique per epoch.
func SealCommandEnvelope(key crypto.ContentKey, epochID uint32, seq uint64, cmd protocol.DeviceCommandAuth) ([]byte, error) {
	plaintext, err := json.Marshal(cmd)
	if err != nil {
		return nil, err
	}
	env, err := crypto.SealMailbox(key, crypto.EnvelopeHeader{
		Version: crypto.VersionV1,
		EpochID: epochID,
		Seq:     seq,
	}, plaintext)
	if err != nil {
		return nil, err
	}
	return env.Marshal(), nil
}

// OpenControlReply opens a daemon reply Control the gateway sealed and returned via the
// phone's mailbox (the response half of the command round-trip). Fail-closed on a
// malformed/wrong-key envelope or non-Control plaintext.
func OpenControlReply(key crypto.ContentKey, raw []byte) (protocol.Control, error) {
	env, err := crypto.ParseEnvelope(raw)
	if err != nil {
		return protocol.Control{}, err
	}
	plain, err := crypto.OpenMailbox(key, env)
	if err != nil {
		return protocol.Control{}, err
	}
	var ctrl protocol.Control
	if err := json.Unmarshal(plain, &ctrl); err != nil {
		return protocol.Control{}, err
	}
	return ctrl, nil
}
