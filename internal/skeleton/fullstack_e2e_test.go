package skeleton

// Full-stack E2E over a REAL relay AND a REAL daemon: a paired phone signs a kill,
// seals it under the epoch content key, and appends it to the machine's relay mailbox;
// the gateway reads the machine mailbox over the relay, opens the command envelope,
// and forwards the phone's device-signed command to the daemon's remote.sock, which
// verifies it (R-POL.9) and executes. This is the remote-control command path across
// every real component: phone-core -> relay -> gateway -> daemon.

import (
	"context"
	"crypto/ed25519"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/remotegw"
)

func TestFullStack_PhoneCommandOverRelayToDaemon(t *testing.T) {
	// Real relay.
	rcfg := relay.DefaultConfig()
	rcfg.DBPath = filepath.Join(t.TempDir(), "relay.db")
	relaySrv, err := relay.New(rcfg)
	if err != nil {
		t.Fatalf("relay.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := relaySrv.Start(ctx); err != nil {
		t.Fatalf("relay start: %v", err)
	}
	defer relaySrv.Close()

	// Real daemon with a remote socket + a paired phone in its registry.
	sk, rsock := assembleWithRemote(t)
	ks := registerPhone(t, sk, device.CapFull)
	meta := launchFake(t, sk, "print HELLO\nidle 60s\n")
	namespaced := protocol.NamespacedID(sk.api.endpointID, meta.ID)

	// Relay connections: the machine (gateway) and the phone.
	mPub, mPriv, _ := ed25519.GenerateKey(nil)
	pPub, pPriv, _ := ed25519.GenerateKey(nil)
	machineRelay, err := relay.Dial(ctx, relaySrv.URL(), relayAuth(mPub, mPriv))
	if err != nil {
		t.Fatalf("machine relay dial: %v", err)
	}
	defer machineRelay.Close()
	phoneRelay, err := relay.Dial(ctx, relaySrv.URL(), relayAuth(pPub, pPriv))
	if err != nil {
		t.Fatalf("phone relay dial: %v", err)
	}
	defer phoneRelay.Close()
	// The machine authorizes the phone so the phone may append commands to the machine
	// mailbox (relay-level pairing).
	if err := machineRelay.AuthorizeDevice(ctx, pPub); err != nil {
		t.Fatalf("authorize phone: %v", err)
	}

	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 3)
	}

	// Phone: sign the kill, seal it, append to the machine's mailbox.
	cmd, err := phonecore.SignCommand(ks, phonecore.CommandInput{
		Action:      protocol.ActionKill,
		Machine:     sk.api.endpointID,
		Session:     namespaced,
		OperationID: "op-fs-1",
		ExpiresAt:   time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("phone sign: %v", err)
	}
	env, err := phonecore.SealCommandEnvelope(key, 4, 1, cmd)
	if err != nil {
		t.Fatalf("seal command: %v", err)
	}
	if _, err := phoneRelay.MailboxAppend(ctx, machineRelay.RoutingID(), env); err != nil {
		t.Fatalf("phone append command: %v", err)
	}

	// Gateway: read the machine mailbox over the relay, open the command, forward it.
	items, err := machineRelay.MailboxRead(ctx, 0)
	if err != nil {
		t.Fatalf("machine mailbox read: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("machine mailbox has %d items; want 1", len(items))
	}
	got, err := remotegw.OpenCommandEnvelope(key, items[0].Envelope)
	if err != nil {
		t.Fatalf("gateway open command: %v", err)
	}
	gw := remotegw.New(rsock, nil)
	reply, err := gw.ForwardCommand(protocol.OpKill, got.Session, got, nil)
	if err != nil {
		t.Fatalf("gateway forward: %v", err)
	}
	if reply.Op == protocol.OpError {
		t.Fatalf("daemon refused the phone's command relayed end to end: %q / %q", reply.Error, reply.ErrorCode)
	}
}

// relayAuth builds a relay.ClientAuth from an ed25519 keypair (skeleton-package copy of
// the remotegw test helper).
func relayAuth(pub ed25519.PublicKey, priv ed25519.PrivateKey) relay.ClientAuth {
	return relay.ClientAuth{
		RelayAuthPub: pub,
		Sign:         func(challenge []byte) []byte { return ed25519.Sign(priv, challenge) },
	}
}
