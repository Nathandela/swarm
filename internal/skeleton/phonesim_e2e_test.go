package skeleton

// UNION E2E (remote slice P1+E1): prove the FULL remote wire on this machine with NO
// mobile app -- a simulated phone driving the REAL phonecore, over the REAL in-process
// relay, against the REAL assembled daemon+gateway. This is the composition of the two
// existing keystone tests:
//
//   - enroll_e2e_test.go (TestEnrollmentE2E_PairThenCommandNoManualSetup): the REAL
//     pairing handshake over an in-memory rendezvous + enroll.Enroll -> res.Grant ->
//     phonecore.AcceptGrant recovers the epoch ContentKey with NO hand-provisioned key.
//   - gatewayservice_e2e_test.go (TestGatewayServiceE2E_JournalOutAndCommandIn): a real
//     relay.Server + real daemon (assembleWithRemote) + remotegw.Service over a real
//     relay.Client, asserting journal-OUT (a card reaches the phone mailbox) AND
//     command-IN (a phone-signed kill executes, sealed reply returns).
//
// It does BOTH with ONE simulated phone (internal/phonesim.Phone, a thin composition
// over phonecore): pair -> the gateway seals a journal event to the phone's mailbox ->
// the phonesim OBSERVES+decodes it (JournalReceiver + SessionCache) -> the phonesim
// SIGNS+DRIVES a kill (SignCommand + SealCommandEnvelope) into the machine mailbox ->
// the daemon executes it -> the phonesim reads the sealed OK reply (OpenControlReply).
//
// Content-key discipline (the real grant crypto, end to end): the MACHINE configures
// the gateway with its OWN epoch content key (crypto.NewEpochKeys().ContentKey, the same
// keys enroll seals into the grant); the phonesim independently recovers ITS copy from
// res.Grant via AcceptGrant. If the grant crypto did not deliver a MATCHING key, the
// phone could neither decode the journal nor seal a command the gateway can open -- so a
// green Observe/DriveKill is itself the proof the bootstrap produced one shared key. The
// over-relay grant DELIVERY is deferred (unbuilt); res.Grant is handed to the phonesim
// in-process, exactly as enroll_e2e_test.go hands it to AcceptGrant.
//
// RED (failing-first): internal/phonesim does not exist yet. This test compile-fails on
// the intended missing package/symbols (phonesim.New / Config / Observe / Session /
// DriveKill / ReadReply). GREEN implements EXACTLY that surface as a phonecore
// composition. Reused verbatim from sibling files (same package `skeleton`): assembleWithRemote
// + launchFake (rgw_remote_socket_test.go / serve_test.go), relayAuth (fullstack_e2e_test.go),
// memRendezvous / rendezvousPair / fillKey / fill16 (enroll_e2e_test.go).

import (
	"context"
	"crypto/ed25519"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonesim"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remote/enroll"
	"github.com/Nathandela/swarm/internal/remote/pairing"
	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/remotegw"
	"github.com/Nathandela/swarm/internal/status"
)

func TestPhonesim_PairObserveKillE2E(t *testing.T) {
	// 1. A real, in-process relay -- the untrusted store the phone and machine meet on
	// (mirrors gatewayservice_e2e_test.go's relay bring-up).
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

	// 2. A real assembled daemon with the remote tier, and a live fake session to
	// observe + kill (mirrors both templates: assembleWithRemote + launchFake).
	sk, rsock := assembleWithRemote(t)
	meta := launchFake(t, sk, "print HELLO\nidle 60s\n")
	// The gateway namespaces roster/journal ids at the remote egress, so the id the
	// phone sees is EXACTLY the id it commands against.
	namespaced := protocol.NamespacedID(sk.api.endpointID, meta.ID)

	// 3. PAIR (enroll_e2e_test.go verbatim): a REAL Noise handshake over an in-memory
	// rendezvous, then enroll -> registry record + sealed grant. No hand-built record,
	// no hand-provisioned content key.
	machineID, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("machine identity: %v", err)
	}
	machineSignPub, machineSignPriv, _ := ed25519.GenerateKey(nil)

	ks, err := crypto.NewFileKeyStore(t.TempDir())
	if err != nil {
		t.Fatalf("phone keystore: %v", err)
	}

	const epochID = uint32(1)
	mp := pairing.MachineParams{
		Static:       machineID.NoiseStatic(),
		Secret:       fillKey(0x5A),
		RendezvousID: fill16(0x11),
		LocalConsole: true,
		Confirm:      func(context.Context, [6]string, string) (bool, error) { return true, nil },
		Payload: pairing.MachinePayload{
			Hostname:            "test-machine.local",
			MachineRoutingID:    []byte("machine-routing-id-0001"),
			MachineRelayAuthPub: make([]byte, 32),
			RecipientPub:        machineID.RecipientPublic(),
			MachineSignPub:      machineSignPub,
			EpochID:             epochID,
		},
	}
	dp := pairing.DeviceParams{
		Static:       ks.NoiseStatic(),
		Secret:       fillKey(0x5A),
		RendezvousID: fill16(0x11),
		Payload: pairing.DevicePayload{
			DeviceName:           "Test iPhone",
			DeviceRoutingID:      []byte("device-routing-id-0001"),
			DeviceRelayAuthPub:   ks.RelayAuthPublic(),
			RecipientPub:         ks.RecipientPublic(),
			DeviceCommandSignPub: ks.CommandSigningPublic(),
		},
	}

	mEnd, dEnd := rendezvousPair()
	m := pairing.NewMachine(mp)
	var (
		mo   *pairing.MachineOutcome
		do   *pairing.DeviceOutcome
		mErr error
		dErr error
		wg   sync.WaitGroup
	)
	wg.Add(2)
	go func() { defer wg.Done(); mo, mErr = m.Pair(ctx, mEnd) }()
	go func() { defer wg.Done(); do, dErr = pairing.RunDevice(ctx, dp, dEnd) }()
	wg.Wait()
	if mErr != nil || dErr != nil {
		t.Fatalf("pairing failed: machine=%v device=%v", mErr, dErr)
	}

	// The machine mints its OWN epoch keys and enrolls: the gateway is configured with
	// keys.ContentKey below; the phone recovers its copy from res.Grant. A matching key
	// is what the whole flow proves.
	keys, err := crypto.NewEpochKeys()
	if err != nil {
		t.Fatalf("epoch keys: %v", err)
	}
	res, err := enroll.Enroll(mo, device.CapFull, machineSignPriv, epochID, 1, keys, time.Now())
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if err := sk.api.devices.Add(res.Record); err != nil {
		t.Fatalf("daemon registry rejected the enrolled record: %v", err)
	}

	// 4. Register both parties on the relay so their mailboxes work, and start the
	// gateway runtime sealing journal events to the phone under keys.ContentKey
	// (mirrors gatewayservice_e2e_test.go's relay dial + ServiceConfig).
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

	svc := remotegw.NewService(remotegw.ServiceConfig{
		DaemonSocket:   rsock,
		Relay:          machineRelay,
		PhoneTarget:    phoneRelay.RoutingID(),
		Key:            keys.ContentKey,
		EpochID:        epochID,
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

	// Construct the simulated phone over the REAL phonecore: it AcceptGrants res.Grant
	// internally (verifying it against the pinned machine sign pub it learned at pairing)
	// to recover its ContentKey/EpochID, and holds its own relay client + the machine's
	// routing/endpoint targets. This is the surface GREEN must build (the RED anchor).
	phone, err := phonesim.New(phonesim.Config{
		KeyStore:       ks,
		MachineSignPub: do.Machine.MachineSignPub,
		Grant:          res.Grant,
		Relay:          phoneRelay,
		MachineTarget:  machineRelay.RoutingID(),
		Machine:        sk.api.endpointID,
	})
	if err != nil {
		t.Fatalf("phonesim.New (AcceptGrant bootstrap): %v", err)
	}

	// 5. OBSERVE: the gateway delivers a roster/Group card naming the live session to
	// the phone mailbox; the phonesim decodes it (JournalReceiver + SessionCache). Poll
	// Observe until the phone's cache holds the launched session (the test owns the
	// deadline loop, mirroring gatewayservice_e2e_test.go's journal-OUT loop).
	sawSession := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !sawSession {
		if _, err := phone.Observe(ctx); err != nil {
			t.Fatalf("phonesim observe: %v", err)
		}
		if cs, ok := phone.Session(namespaced); ok && cs.Present {
			sawSession = true
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if !sawSession {
		t.Fatal("phonesim never observed the live session over the relay (journal-OUT broken)")
	}

	// 6. DRIVE: the phonesim signs+seals a KILL for that session and appends it to the
	// machine mailbox; the gateway command-IN loop forwards it and the daemon executes.
	if err := phone.DriveKill(ctx, namespaced, "op-phonesim-1"); err != nil {
		t.Fatalf("phonesim drive kill: %v", err)
	}

	// The daemon-side session is killed (the real lifecycle effect, not just a reply)
	// AND the phonesim reads a sealed OK reply (the round-trip closes over the relay).
	killed := false
	gotReply := false
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && (!killed || !gotReply) {
		if !killed {
			if m, ok := sk.Core().Get(meta.ID); ok && m.Status.Process != status.ProcessRunning {
				killed = true
			}
		}
		if !gotReply {
			reply, ok, err := phone.ReadReply(ctx)
			if err != nil {
				t.Fatalf("phonesim read reply: %v", err)
			}
			if ok {
				if reply.Op == protocol.OpError {
					t.Fatalf("daemon refused the phonesim kill: %q / %q", reply.Error, reply.ErrorCode)
				}
				if reply.Op == protocol.OpOK {
					gotReply = true
				}
			}
		}
		if !killed || !gotReply {
			time.Sleep(30 * time.Millisecond)
		}
	}
	if !killed {
		t.Fatal("daemon-side session never left running after the phonesim kill (command-IN did not execute)")
	}
	if !gotReply {
		t.Fatal("phonesim never received the sealed OK reply (command round-trip broken)")
	}
}
