package phonecore

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/device"
)

// CommandInput is the identity of a remote mutating op the phone authors.
type CommandInput struct {
	Action      string    // a schema.Action* constant
	Machine     string    // target machine endpoint id
	Session     string    // namespaced session id (schema.LaunchSessionSentinel for launch)
	OperationID string    // durable client-generated idempotency key (R-PHC.4)
	ExpiresAt   time.Time // command validity horizon
	ContentHash []byte    // optional 32-byte content binding (e.g. protocol.LaunchContentHash)
}

// SignCommand authors and signs a remote command with the device's command-signing key
// (R-PHC authoring side of R-POL.9). It builds the canonical crypto.Command tuple,
// signs it with the KeyStore, and returns the schema.DeviceCommandAuth the phone
// sends to the gateway -> daemon. The DeviceID is derived canonically from the
// command-signing public key, matching how the daemon registry pins it (R-DEV.1), so a
// signature always verifies against exactly the record its id names.
func SignCommand(ks crypto.KeyStore, in CommandInput) (schema.DeviceCommandAuth, error) {
	msg, err := crypto.Command{
		Action:      in.Action,
		Machine:     in.Machine,
		Session:     in.Session,
		OperationID: in.OperationID,
		ExpiresAt:   in.ExpiresAt.Unix(),
		ContentHash: in.ContentHash,
	}.Canonical()
	if err != nil {
		return schema.DeviceCommandAuth{}, err
	}
	sig := ks.SignCommand(msg)
	return schema.DeviceCommandAuth{
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

// TakeControlInput is the identity of a take_control op the phone authors. GateToken is
// hashed into the signed command (ContentHash = SHA256(GateToken)) AND carried on the
// wire, so the daemon can recompute the hash from the wire token and a relay that swaps
// the one-shot token breaks the signature.
type TakeControlInput struct {
	Machine     string    // target machine endpoint id
	Session     string    // namespaced session id to take control of
	OperationID string    // durable client-generated idempotency key (single-use)
	ExpiresAt   time.Time // command validity horizon
	GateToken   string    // one-shot gate token bound via ContentHash and carried on the wire
}

// SignTakeControl authors and signs a take_control command (A7 input), mirroring
// SignCommand but binding the one-shot gate token into the signature the way launch binds
// its spec: ContentHash = SHA256(GateToken), Action = schema.ActionTakeControl. The
// daemon (handleTakeControl) recomputes SHA256 from the WIRE gate token, so a relay that
// swaps the token yields a different content hash and the device signature fails to verify.
func SignTakeControl(ks crypto.KeyStore, in TakeControlInput) (schema.DeviceCommandAuth, error) {
	h := sha256.Sum256([]byte(in.GateToken))
	return SignCommand(ks, CommandInput{
		Action:      schema.ActionTakeControl,
		Machine:     in.Machine,
		Session:     in.Session,
		OperationID: in.OperationID,
		ExpiresAt:   in.ExpiresAt,
		ContentHash: h[:],
	})
}

// SealTakeControlEnvelope seals a signed take_control command together with its wire gate
// token and requested TTL as a mailbox envelope under the epoch content key, mirroring
// SealLaunchEnvelope. The gate token rides alongside the signed tuple (schema.RemoteCommand)
// so the gateway can reconstruct the take_control Control frame; the token is bound into the
// signature via ContentHash = SHA256(gateToken), which the daemon recomputes from this
// forwarded token, so a relay that alters it breaks the signature. TTLSeconds is not signed
// (server-clamped). seq must be unique per epoch.
func SealTakeControlEnvelope(key crypto.ContentKey, epochID uint32, seq uint64, cmd schema.DeviceCommandAuth, gateToken string, ttlSeconds int) ([]byte, error) {
	plaintext, err := json.Marshal(schema.RemoteCommand{
		DeviceCommandAuth: cmd,
		GateToken:         gateToken,
		TTLSeconds:        ttlSeconds,
	})
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

// SealCommandEnvelope seals a signed command as a mailbox envelope under the epoch
// content key (XChaCha20-Poly1305), so it can travel through the untrusted relay to the
// machine as ciphertext. The command's device signature is verified by the daemon after
// the gateway opens the envelope; sealing adds confidentiality, not the command's
// authenticity (which the signature already carries). seq must be unique per epoch.
func SealCommandEnvelope(key crypto.ContentKey, epochID uint32, seq uint64, cmd schema.DeviceCommandAuth) ([]byte, error) {
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

// SealLaunchEnvelope seals a signed launch command together with its LaunchReq spec
// as a mailbox envelope under the epoch content key. The spec rides alongside the
// signed tuple (schema.RemoteCommand) so the gateway can forward it to the daemon;
// the command's ContentHash must be crypto/protocol.LaunchContentHash(launch), which
// the daemon recomputes from the forwarded spec, so a relay or gateway that alters
// the spec breaks the signature. seq must be unique per epoch.
func SealLaunchEnvelope(key crypto.ContentKey, epochID uint32, seq uint64, cmd schema.DeviceCommandAuth, launch *schema.LaunchReq) ([]byte, error) {
	plaintext, err := json.Marshal(schema.RemoteCommand{DeviceCommandAuth: cmd, Launch: launch})
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
func OpenControlReply(key crypto.ContentKey, raw []byte) (schema.Control, error) {
	env, err := crypto.ParseEnvelope(raw)
	if err != nil {
		return schema.Control{}, err
	}
	plain, err := crypto.OpenMailbox(key, env)
	if err != nil {
		return schema.Control{}, err
	}
	var ctrl schema.Control
	if err := json.Unmarshal(plain, &ctrl); err != nil {
		return schema.Control{}, err
	}
	return ctrl, nil
}
