package skeleton

// Interop test closing the R-POL.9 command loop: the phone-core signs a remote command
// with its command-signing KeyStore (R-PHC authoring) and the daemon authenticator
// (authorizeCommand, R-POL.9b) verifies it against the pinned registry record. This
// proves the signer and verifier agree on the canonical tuple end to end -- a tamper of
// any signed field breaks verification. RED is undefined-only (phonecore.SignCommand
// does not exist yet).

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/device"
)

func TestInterop_PhoneSignedCommandAcceptedByDaemon(t *testing.T) {
	ks, err := crypto.NewFileKeyStore(t.TempDir())
	if err != nil {
		t.Fatalf("keystore: %v", err)
	}
	reg, err := device.Open(t.TempDir())
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	id := device.DeviceIDFor(ks.CommandSigningPublic())
	if err := reg.Add(device.Record{
		DeviceID:       id,
		Name:           "phone",
		NoiseStaticPub: make([]byte, 32),
		RelayAuthPub:   make([]byte, 32),
		CommandSignPub: ks.CommandSigningPublic(),
		RecipientPub:   make([]byte, 32),
		Capability:     device.CapFull,
		PairedAt:       time.Unix(1_700_000_000, 0),
		GrantedEpoch:   1,
	}); err != nil {
		t.Fatalf("registry add: %v", err)
	}

	now := time.Unix(1_700_000_100, 0)
	cmd, err := phonecore.SignCommand(ks, phonecore.CommandInput{
		Action:      protocol.ActionKill,
		Machine:     "machine1",
		Session:     "machine1/sess1",
		OperationID: "op-1",
		ExpiresAt:   now.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("phone SignCommand: %v", err)
	}
	if cmd.DeviceID != id {
		t.Fatalf("phone-derived DeviceID %q != registry id %q", cmd.DeviceID, id)
	}

	// The daemon authenticator accepts the phone-signed command.
	if err := authorizeCommand(reg, now, cmd); err != nil {
		t.Fatalf("daemon rejected a valid phone-signed command: %v", err)
	}

	// Tampering any signed field after signing breaks verification.
	tampered := cmd
	tampered.OperationID = "op-2"
	if err := authorizeCommand(reg, now, tampered); err == nil {
		t.Fatalf("daemon accepted a command whose operation_id was altered after signing")
	}
}
