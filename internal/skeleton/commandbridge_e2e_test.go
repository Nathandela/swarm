package skeleton

// Integration test for the gateway command-IN loop (agents-tracker-5mm) over a
// REAL relay AND a REAL daemon: a paired phone seals a kill into the machine's
// relay mailbox; the gateway's CommandBridge polls that mailbox, opens the
// command, forwards it to the daemon (which verifies R-POL.9 and executes), and
// seals the reply back to the phone's mailbox -- the whole command-IN + reply
// half driven by the reusable loop, not inlined test steps.

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

func TestCommandBridgeE2E_PhoneKillThroughRelayAndDaemon(t *testing.T) {
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

	// Real daemon + paired phone.
	sk, rsock := assembleWithRemote(t)
	ks := registerPhone(t, sk, device.CapFull)
	meta := launchFake(t, sk, "print HELLO\nidle 60s\n")
	namespaced := protocol.NamespacedID(sk.api.endpointID, meta.ID)

	// Relay connections: machine (gateway) + phone; each authorizes the other so
	// both mailbox directions are permitted.
	mPub, mPriv, _ := ed25519.GenerateKey(nil)
	pPub, pPriv, _ := ed25519.GenerateKey(nil)
	machineRelay, err := relay.Dial(ctx, relaySrv.URL(), relayAuth(mPub, mPriv))
	if err != nil {
		t.Fatalf("machine dial: %v", err)
	}
	defer machineRelay.Close()
	phoneRelay, err := relay.Dial(ctx, relaySrv.URL(), relayAuth(pPub, pPriv))
	if err != nil {
		t.Fatalf("phone dial: %v", err)
	}
	defer phoneRelay.Close()
	if err := machineRelay.AuthorizeDevice(ctx, pPub); err != nil {
		t.Fatalf("machine authorize phone: %v", err)
	}
	if err := phoneRelay.AuthorizeDevice(ctx, mPub); err != nil {
		t.Fatalf("phone authorize machine: %v", err)
	}

	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 5)
	}

	// Phone: sign + seal the kill, append to the machine's mailbox.
	cmd, err := phonecore.SignCommand(ks, phonecore.CommandInput{
		Action:      protocol.ActionKill,
		Machine:     sk.api.endpointID,
		Session:     namespaced,
		OperationID: "op-cb-1",
		ExpiresAt:   time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("phone sign: %v", err)
	}
	env, err := phonecore.SealCommandEnvelope(key, 1, 1, cmd)
	if err != nil {
		t.Fatalf("phone seal: %v", err)
	}
	if _, err := phoneRelay.MailboxAppend(ctx, machineRelay.RoutingID(), env); err != nil {
		t.Fatalf("phone append: %v", err)
	}

	// The gateway's command bridge drives the whole loop.
	bridge := remotegw.NewCommandBridge(remotegw.CommandBridgeConfig{
		Mailbox:     machineRelay,
		Forwarder:   remotegw.New(rsock, nil),
		Key:         key,
		EpochID:     1,
		ReplyTarget: phoneRelay.RoutingID(),
	})
	n, err := bridge.PollOnce(ctx)
	if err != nil {
		t.Fatalf("bridge PollOnce: %v", err)
	}
	if n != 1 {
		t.Fatalf("bridge processed %d commands, want 1", n)
	}

	// The phone reads the sealed reply and decodes it: the daemon accepted the kill.
	pitems, err := phoneRelay.MailboxRead(ctx, 0)
	if err != nil {
		t.Fatalf("phone read reply: %v", err)
	}
	if len(pitems) == 0 {
		t.Fatal("phone mailbox empty; the bridge did not seal a reply")
	}
	reply, err := phonecore.OpenControlReply(key, pitems[len(pitems)-1].Envelope)
	if err != nil {
		t.Fatalf("phone open reply: %v", err)
	}
	if reply.Op == protocol.OpError {
		t.Fatalf("bridge relayed a daemon refusal for a paired phone's kill: %q / %q", reply.Error, reply.ErrorCode)
	}
}
