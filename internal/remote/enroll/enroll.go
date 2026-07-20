// Package enroll is the machine-side enrollment keystone: it turns an
// affirmatively-confirmed pairing outcome (internal/remote/pairing) into the two
// durable artifacts the runnable remote stack needs.
//
//   - a device.Registry Record the daemon authorizes remote commands against
//     (R-POL.9 / R-DEV.1): the device's pinned identity keys, capability tier,
//     pairing time, and granted epoch.
//   - a sealed, signed crypto.EpochGrant that delivers the epoch's wake/content
//     keys to the just-paired device (F3/A15), so the phone can decrypt journal
//     envelopes and seal commands without any out-of-band key provisioning.
//
// It composes pairing + device + crypto and holds no state; the caller Adds the
// Record to its registry and delivers the Grant to the device over the relay.
package enroll

import (
	"crypto/ed25519"
	"errors"
	"time"

	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remote/pairing"
)

// Enrollment errors are fail-closed: a malformed outcome never yields a
// half-built record or an unsealable grant.
var (
	// ErrNoOutcome is returned when Enroll is handed a nil pairing outcome.
	ErrNoOutcome = errors.New("enroll: nil pairing outcome")
	// ErrNoCommandKey is returned when the outcome carries no device
	// command-signing key: the daemon could never verify a command signature
	// (R-POL.9), so the device must not be admitted.
	ErrNoCommandKey = errors.New("enroll: pairing outcome missing device command-signing key")
	// ErrNoRecipientKey is returned when the outcome carries no device recipient
	// key: the epoch grant would have no seal target (A14).
	ErrNoRecipientKey = errors.New("enroll: pairing outcome missing device recipient key")
)

// Result carries the two artifacts an enrollment produces: the registry Record to
// persist (via device.Registry.Add) and the sealed EpochGrant to deliver.
type Result struct {
	Record device.Record
	Grant  *crypto.EpochGrant
}

// Enroll builds the registry record for an affirmatively-confirmed pairing and
// seals the initial epoch grant to the paired device.
//
// The device id derives canonically from the device's Ed25519 command-signing key
// (device.DeviceIDFor), binding the record's identity to exactly the key R-POL.9
// verifies its commands against. The grant is sealed to the device's recipient
// X25519 key and signed with the machine's Ed25519 grant-signing key, so a relay
// can neither read nor forge it; the phone verifies it against the machine
// grant-signing pub it pinned at pairing (MachinePayload.MachineSignPub).
//
// It fails closed on a nil or malformed outcome (missing command-signing or
// recipient key) rather than admitting an unauthenticatable device.
func Enroll(out *pairing.MachineOutcome, cap device.Capability, machineGrantPriv ed25519.PrivateKey, epochID uint32, grantSeq uint64, keys crypto.EpochKeys, now time.Time) (Result, error) {
	if out == nil {
		return Result{}, ErrNoOutcome
	}
	if len(out.Device.DeviceCommandSignPub) != ed25519.PublicKeySize {
		return Result{}, ErrNoCommandKey
	}
	if len(out.Device.RecipientPub) != 32 {
		return Result{}, ErrNoRecipientKey
	}

	rec := device.Record{
		DeviceID:       device.DeviceIDFor(out.Device.DeviceCommandSignPub),
		Name:           out.Device.DeviceName,
		NoiseStaticPub: out.DeviceStatic,
		RelayAuthPub:   out.Device.DeviceRelayAuthPub,
		CommandSignPub: out.Device.DeviceCommandSignPub,
		RecipientPub:   out.Device.RecipientPub,
		RoutingID:      out.Device.DeviceRoutingID,
		Capability:     cap,
		PairedAt:       now,
		GrantedEpoch:   epochID,
	}

	grant, err := crypto.SealEpochGrant(machineGrantPriv, out.Device.RecipientPub, epochID, grantSeq, keys)
	if err != nil {
		return Result{}, err
	}
	return Result{Record: rec, Grant: grant}, nil
}
