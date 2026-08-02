package skeleton

// End-to-end command path against a REAL daemon: a paired phone signs a kill, the
// gateway forwards it (blind conduit) to the daemon's remote.sock, and the daemon
// verifies the device signature + capability (R-POL.9) and executes it. An unpaired
// phone's identical command is refused. This exercises the full phone -> gateway ->
// daemon mutating path with real crypto and the real assembled daemon.

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remotegw"
)

func registerPhone(t *testing.T, sk *Daemon, cap device.Capability) crypto.KeyStore {
	t.Helper()
	ks, err := crypto.NewFileKeyStore(t.TempDir())
	if err != nil {
		t.Fatalf("keystore: %v", err)
	}
	if err := sk.api.devices.Add(device.Record{
		DeviceID:       device.DeviceIDFor(ks.CommandSigningPublic()),
		Name:           "phone",
		NoiseStaticPub: make([]byte, 32),
		RelayAuthPub:   make([]byte, 32),
		CommandSignPub: ks.CommandSigningPublic(),
		RecipientPub:   make([]byte, 32),
		Capability:     cap,
		PairedAt:       time.Now(),
		GrantedEpoch:   1,
	}); err != nil {
		t.Fatalf("register phone: %v", err)
	}
	return ks
}

func TestE2E_PhoneSignedKillThroughGatewayExecuted(t *testing.T) {
	sk, rsock := assembleWithRemote(t)
	ks := registerPhone(t, sk, device.CapFull)

	meta := launchFake(t, sk, "print HELLO\nidle 60s\n")
	namespaced := protocol.NamespacedID(sk.api.endpointID, meta.ID)

	cmd, err := phonecore.SignCommand(ks, phonecore.CommandInput{
		Action:      protocol.ActionKill,
		Machine:     sk.api.endpointID,
		Session:     namespaced,
		OperationID: "op-kill-1",
		ExpiresAt:   time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("phone sign: %v", err)
	}

	gw := remotegw.New(rsock, nil)
	reply, err := gw.ForwardCommand(protocol.OpKill, namespaced, cmd, nil)
	if err != nil {
		t.Fatalf("gateway forward: %v", err)
	}
	if reply.Op == protocol.OpError {
		t.Fatalf("paired phone's kill was refused: %q / %q", reply.Error, reply.ErrorCode)
	}

	// An UNPAIRED phone signing an identical command is refused (not_authorized).
	other, err := crypto.NewFileKeyStore(t.TempDir())
	if err != nil {
		t.Fatalf("other keystore: %v", err)
	}
	bad, err := phonecore.SignCommand(other, phonecore.CommandInput{
		Action:      protocol.ActionKill,
		Machine:     sk.api.endpointID,
		Session:     namespaced,
		OperationID: "op-kill-2",
		ExpiresAt:   time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("other sign: %v", err)
	}
	reply, err = gw.ForwardCommand(protocol.OpKill, namespaced, bad, nil)
	if err != nil {
		t.Fatalf("gateway forward (unpaired): %v", err)
	}
	if reply.Op != protocol.OpError || reply.ErrorCode != protocol.CodeNotAuthorized {
		t.Fatalf("unpaired phone's kill = op %q code %q; want error/not_authorized", reply.Op, reply.ErrorCode)
	}
}
