package skeleton

// Integration test for the gateway RUNTIME (agents-tracker-6rn) against a REAL relay
// AND a REAL daemon: remotegw.Service composes both loops over one relay connection.
// With a session already live, the journal bridge seals the roster to the phone's
// mailbox over the relay; then a phone-authored sealed kill flows back through the
// command loop, executes on the daemon, and its sealed reply returns to the phone --
// all driven by Service.Run, the body of the cmd/swarm-remote sidecar.

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
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

func TestGatewayServiceE2E_JournalOutAndCommandIn(t *testing.T) {
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
	meta := launchFake(t, sk, "print HELLO\nidle 60s\n")
	// The journal/roster carries the RAW local session id over the wire (the protocol
	// layer namespaces views + commands, but not journal records -- tracked separately);
	// commands still target the namespaced id.
	rosterID := meta.ID
	namespaced := protocol.NamespacedID(sk.api.endpointID, meta.ID)

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
		key[i] = byte(i + 11)
	}

	svc := remotegw.NewService(remotegw.ServiceConfig{
		DaemonSocket:   rsock,
		Relay:          machineRelay,
		PhoneTarget:    phoneRelay.RoutingID(),
		Key:            key,
		EpochID:        1,
		PollInterval:   20 * time.Millisecond,
		ReconnectDelay: 50 * time.Millisecond,
	})
	svcCtx, svcCancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- svc.Run(svcCtx) }()
	defer func() {
		svcCancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("service did not stop within 2s of cancel")
		}
	}()

	// Journal-OUT: the phone's mailbox receives the sealed roster snapshot naming the
	// live session (delivered over the real relay by the runtime).
	sawSession := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !sawSession {
		items, err := phoneRelay.MailboxRead(ctx, 0)
		if err != nil {
			t.Fatalf("phone mailbox read: %v", err)
		}
		for _, it := range items {
			e, err := crypto.ParseEnvelope(it.Envelope)
			if err != nil {
				continue
			}
			plain, err := crypto.OpenMailbox(key, e)
			if err != nil {
				continue
			}
			var rec protocol.JournalRecord
			if err := json.Unmarshal(plain, &rec); err != nil {
				continue
			}
			if rec.SessionID == rosterID {
				sawSession = true
				break
			}
		}
		if !sawSession {
			time.Sleep(30 * time.Millisecond)
		}
	}
	if !sawSession {
		t.Fatal("phone never received the live session over the relay (journal-OUT runtime broken)")
	}

	// Command-IN: the phone signs+seals a kill and appends it to the machine mailbox;
	// the runtime's command loop forwards it, the daemon executes, and a sealed reply
	// returns to the phone mailbox.
	cmd, err := phonecore.SignCommand(ks, phonecore.CommandInput{
		Action:      protocol.ActionKill,
		Machine:     sk.api.endpointID,
		Session:     namespaced,
		OperationID: "op-svc-1",
		ExpiresAt:   time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("phone sign: %v", err)
	}
	env, err := phonecore.SealCommandEnvelope(key, 1, 100, cmd)
	if err != nil {
		t.Fatalf("phone seal: %v", err)
	}
	if _, err := phoneRelay.MailboxAppend(ctx, machineRelay.RoutingID(), env); err != nil {
		t.Fatalf("phone append command: %v", err)
	}

	gotReply := false
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !gotReply {
		items, err := phoneRelay.MailboxRead(ctx, 0)
		if err != nil {
			t.Fatalf("phone mailbox read: %v", err)
		}
		for _, it := range items {
			reply, err := phonecore.OpenControlReply(key, it.Envelope)
			if err != nil {
				continue // journal envelopes decode to a record, not a control -> skip
			}
			if reply.Op == protocol.OpOK {
				gotReply = true
				break
			}
			if reply.Op == protocol.OpError {
				t.Fatalf("runtime relayed a daemon refusal for the phone kill: %q / %q", reply.Error, reply.ErrorCode)
			}
		}
		if !gotReply {
			time.Sleep(30 * time.Millisecond)
		}
	}
	if !gotReply {
		t.Fatal("phone never received the command reply (command-IN runtime broken)")
	}
}
