package skeleton

// C5 (phone side) E2E: prove the FULL sealed-grant DELIVERY -> BOOTSTRAP chain with NO
// in-process grant injection. The sibling phonesim_e2e_test.go hands res.Grant to the phone
// in-process (phonesim.New); THIS test closes the loop the machine-side landing (b63a640)
// opened: the daemon PERSISTS the sealed grant (grant.Save), the gateway DELIVERS it over
// the relay mailbox as a tagged plaintext bootstrap frame (AuthorizeDevice + MailboxAppend,
// mirroring deliverEpochGrant), and the phone RECOVERS its ContentKey by READING that frame
// off the mailbox (phonesim.NewFromMailbox) -- never by injection.
//
// The proof that the recovered ContentKey is correct end to end is behavioral: the gateway
// seals a roster/journal card under ITS epoch ContentKey; the bootstrapped phone can decode
// that card into its session cache ONLY if the key it opened from the delivered grant matches.
// A green Observe is therefore proof the over-mailbox bootstrap produced the one shared key.
//
// RED-first (GG-5): phonesim.NewFromMailbox does not exist yet, so this file is a compile
// failure until the mailbox-bootstrap path lands. It reuses the same package-level helpers as
// phonesim_e2e_test.go (assembleWithRemote / relayAuth / rendezvousPair / fillKey / fill16 /
// launchFake) and touches NO existing test.

import (
	"context"
	"crypto/ed25519"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonesim"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remote/enroll"
	"github.com/Nathandela/swarm/internal/remote/grant"
	"github.com/Nathandela/swarm/internal/remote/pairing"
	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/remotegw"
)

func TestPhonesim_GrantDeliveredOverMailboxBootstrapsE2E(t *testing.T) {
	// 1. A real, in-process relay -- the untrusted store the phone and machine meet on.
	rcfg := relay.DefaultConfig()
	rcfg.DBPath = t.TempDir() + "/relay.db"
	relaySrv, err := relay.New(rcfg)
	if err != nil {
		t.Fatalf("relay.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := relaySrv.Start(ctx); err != nil {
		t.Fatalf("relay start: %v", err)
	}
	t.Cleanup(func() { _ = relaySrv.Close() })

	// 2. A real assembled daemon with the remote tier.
	sk, rsock := assembleWithRemote(t)

	// 3. PAIR (enroll_e2e_test.go verbatim) then enroll -> registry record + SEALED grant.
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

	// 4. Register both parties on the relay and start the gateway runtime (journal-OUT).
	mPub, mPriv, _ := ed25519.GenerateKey(nil)
	pPub, pPriv, _ := ed25519.GenerateKey(nil)
	machineRelay, err := relay.Dial(ctx, relaySrv.URL(), relayAuth(mPub, mPriv))
	if err != nil {
		t.Fatalf("machine dial: %v", err)
	}
	t.Cleanup(func() { _ = machineRelay.Close() })
	phoneRelay, err := relay.Dial(ctx, relaySrv.URL(), relayAuth(pPub, pPriv))
	if err != nil {
		t.Fatalf("phone dial: %v", err)
	}
	t.Cleanup(func() { _ = phoneRelay.Close() })

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
	t.Cleanup(func() {
		svcCancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("service did not stop within 2s of cancel")
		}
	})

	// 5. DELIVERY (mirror the gateway's deliverEpochGrant): the daemon PERSISTS the sealed
	// grant via grant.Save (addressable by device id), the gateway LOADS it back and, on the
	// route it just authorized, appends it to the DEVICE mailbox as the tagged plaintext
	// bootstrap frame. This is the exact Save -> Load -> AuthorizeDevice -> MailboxAppend chain
	// production runs (grant.go / config.go / deliver.go), with the harness's dial-time relay
	// key (pPub) standing in for rec.RelayAuthPub as the mailbox route the phone actually reads.
	regDir := t.TempDir()
	if err := grant.Save(regDir, res.Record.DeviceID, res.Grant); err != nil {
		t.Fatalf("grant.Save (daemon persist): %v", err)
	}
	loaded, err := grant.Load(regDir, res.Record.DeviceID)
	if err != nil || loaded == nil {
		t.Fatalf("grant.Load (gateway read-back): grant=%v err=%v", loaded, err)
	}
	if err := machineRelay.AuthorizeDevice(ctx, pPub); err != nil {
		t.Fatalf("gateway authorize device (mailbox route): %v", err)
	}
	if err := phoneRelay.AuthorizeDevice(ctx, mPub); err != nil {
		t.Fatalf("phone authorize machine: %v", err)
	}
	frame, err := grant.MarshalBootstrap(loaded)
	if err != nil {
		t.Fatalf("marshal bootstrap frame: %v", err)
	}
	if _, err := machineRelay.MailboxAppend(ctx, phoneRelay.RoutingID(), frame); err != nil {
		t.Fatalf("gateway append bootstrap frame to device mailbox: %v", err)
	}

	// 6. BOOTSTRAP FROM THE MAILBOX (no cfg.Grant): the phone scans its mailbox, finds the
	// bootstrap frame, and AcceptGrants it to recover the epoch ContentKey. This is the C5
	// phone side -- the ContentKey is READ off the relay, NOT injected in-process.
	cfg := phonesim.Config{
		KeyStore:       ks,
		MachineSignPub: do.Machine.MachineSignPub,
		Relay:          phoneRelay,
		MachineTarget:  machineRelay.RoutingID(),
		Machine:        sk.api.endpointID,
		// Grant deliberately UNSET: NewFromMailbox must recover it from the mailbox.
	}
	var phone *phonesim.Phone
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		phone, err = phonesim.NewFromMailbox(ctx, cfg)
		if err == nil {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if phone == nil {
		t.Fatalf("phone never bootstrapped from the mailbox (delivery -> NewFromMailbox broken): %v", err)
	}

	// 7. THE BOOTSTRAPPED PHONE WORKS: launch a session on the machine; the gateway seals its
	// roster card under the epoch ContentKey, and the phone -- whose key came SOLELY from the
	// delivered grant -- decodes it. A green Observe proves the recovered ContentKey matches.
	meta := launchFake(t, sk, "print HELLO\nidle 60s\n")
	namespaced := protocol.NamespacedID(sk.api.endpointID, meta.ID)

	sawSession := false
	deadline = time.Now().Add(5 * time.Second)
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
		t.Fatal("bootstrapped phone never decoded the launched session's card; the ContentKey recovered over the mailbox does not match the gateway's (bootstrap broken)")
	}
}
