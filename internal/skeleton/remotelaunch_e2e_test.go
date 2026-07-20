package skeleton

// Integration test for remote launch over the command loop (agents-tracker-sev)
// against a REAL relay + REAL daemon: a paired phone signs a launch bound to its
// spec via LaunchContentHash, seals BOTH into one envelope, and the gateway's
// CommandBridge forwards it; the daemon verifies the content-hash binding (R-POL.9)
// and actually spawns the session. A gateway that TAMPERS with the spec after
// signing is refused -- proving the binding a relay/gateway cannot forge around.

import (
	"context"
	"crypto/ed25519"
	"os"
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

func TestRemoteLaunchE2E_PhoneLaunchThroughBridgeSpawnsSession(t *testing.T) {
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

	sk, rsock := assembleWithRemote(t)
	ks := registerPhone(t, sk, device.CapFull)

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
		key[i] = byte(i + 9)
	}

	// A real fake-agent script the remote launch will run.
	scriptPath := filepath.Join(t.TempDir(), "script.txt")
	if err := os.WriteFile(scriptPath, []byte("print REMOTE_LAUNCH\nidle 60s\n"), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	launch := &protocol.LaunchReq{
		Agent:   "fake",
		Cwd:     t.TempDir(),
		Options: map[string]string{"script": scriptPath},
		Cols:    80,
		Rows:    24,
	}

	// Phone signs the launch bound to its spec, seals command+spec into one envelope.
	cmd, err := phonecore.SignCommand(ks, phonecore.CommandInput{
		Action:      protocol.ActionLaunch,
		Machine:     sk.api.endpointID,
		Session:     protocol.LaunchSessionSentinel,
		OperationID: "op-rl-1",
		ExpiresAt:   time.Now().Add(time.Minute),
		ContentHash: protocol.LaunchContentHash(launch),
	})
	if err != nil {
		t.Fatalf("phone sign launch: %v", err)
	}
	env, err := phonecore.SealLaunchEnvelope(key, 1, 1, cmd, launch)
	if err != nil {
		t.Fatalf("seal launch: %v", err)
	}
	if _, err := phoneRelay.MailboxAppend(ctx, machineRelay.RoutingID(), env); err != nil {
		t.Fatalf("phone append launch: %v", err)
	}

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
		t.Fatalf("bridge processed %d, want 1", n)
	}

	// The phone reads the sealed reply: a launch success (OpLaunch), not an error.
	pitems, err := phoneRelay.MailboxRead(ctx, 0)
	if err != nil {
		t.Fatalf("phone read reply: %v", err)
	}
	if len(pitems) == 0 {
		t.Fatal("phone mailbox empty; no launch reply")
	}
	reply, err := phonecore.OpenControlReply(key, pitems[len(pitems)-1].Envelope)
	if err != nil {
		t.Fatalf("phone open reply: %v", err)
	}
	if reply.Op == protocol.OpError {
		t.Fatalf("remote launch refused end to end: %q / %q", reply.Error, reply.ErrorCode)
	}
	if reply.Op != protocol.OpLaunch {
		t.Fatalf("launch reply op = %q, want %q", reply.Op, protocol.OpLaunch)
	}
	if _, err := os.Stat(scriptPath); err != nil {
		t.Fatalf("script vanished: %v", err)
	}

	// TAMPER: forward the SAME signed command but with an altered spec. The daemon
	// recomputes LaunchContentHash from the tampered spec, which no longer matches the
	// signed hash -> refused. This is the binding a gateway cannot forge around.
	tampered := *launch
	tampered.InitialPrompt = "rm -rf / injected by a malicious gateway"
	reply2, err := remotegw.New(rsock, nil).ForwardCommand(protocol.OpLaunch, protocol.LaunchSessionSentinel, cmd, &tampered)
	if err != nil {
		t.Fatalf("forward tampered launch: %v", err)
	}
	if reply2.Op != protocol.OpError || reply2.ErrorCode != protocol.CodeNotAuthorized {
		t.Fatalf("tampered launch was NOT refused as not_authorized: op=%q code=%q", reply2.Op, reply2.ErrorCode)
	}
}
