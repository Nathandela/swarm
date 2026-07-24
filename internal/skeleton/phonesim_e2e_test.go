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
// Reused verbatim from sibling files (same package `skeleton`): assembleWithRemote +
// launchFake (rgw_remote_socket_test.go / serve_test.go), relayAuth (fullstack_e2e_test.go),
// memRendezvous / rendezvousPair / fillKey / fill16 (enroll_e2e_test.go), waitSessionExited
// (lease_conn_test.go).

import (
	"context"
	"crypto/ed25519"
	"path/filepath"
	"strings"
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

// phonesimHarness is the full remote wire stood up on this machine: a real in-process
// relay, a real assembled daemon + running gateway Service (journal-OUT + command-IN +
// the INPUT plane, since NewService wires a LeaseManager off DaemonSocket), and a
// simulated Phone bootstrapped from a REAL pairing + enrollment. Every teardown is
// registered with t.Cleanup, so a test just launches its session and drives the phone.
type phonesimHarness struct {
	ctx      context.Context // long-lived: cancelled only at cleanup (keeps the relay dials + service alive)
	phone    *phonesim.Phone
	sk       *Daemon
	rsock    string
	deviceID string // the enrolled phone's device id, so a test can revoke it to flip the kill switch OFF
}

// newPhonesimHarness performs the pair -> enroll -> gateway-up bootstrap shared by both
// E2E tests. It stops short of launching a session so each test can pick its own fake
// script (an idle session to kill, an ask-blocked session to type into).
func newPhonesimHarness(t *testing.T) phonesimHarness {
	t.Helper()

	// 1. A real, in-process relay -- the untrusted store the phone and machine meet on.
	rcfg := relay.DefaultConfig()
	rcfg.DBPath = filepath.Join(t.TempDir(), "relay.db")
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

	// 3. PAIR (enroll_e2e_test.go verbatim): a REAL Noise handshake over an in-memory
	// rendezvous, then enroll -> registry record + sealed grant.
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

	// The machine mints its OWN epoch keys and enrolls the device at CapFull (the
	// capability take_control requires).
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

	// 4. Register both parties on the relay and start the gateway runtime. NewService
	// wires the LeaseManager off DaemonSocket (Slice 5), so this Service carries the
	// INPUT plane (take_control + keystroke routing), not just journal + commands.
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
	t.Cleanup(func() {
		svcCancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("service did not stop within 2s of cancel")
		}
	})

	// 5. Construct the simulated phone over the REAL phonecore: it AcceptGrants res.Grant
	// internally (verifying it against the pinned machine sign pub it learned at pairing)
	// to recover its ContentKey/EpochID, and holds its own relay client + the machine's
	// routing/endpoint targets.
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

	return phonesimHarness{ctx: ctx, phone: phone, sk: sk, rsock: rsock, deviceID: res.Record.DeviceID}
}

func TestPhonesim_PairObserveKillE2E(t *testing.T) {
	h := newPhonesimHarness(t)
	phone, sk, ctx := h.phone, h.sk, h.ctx

	// A live fake session to observe + kill.
	meta := launchFake(t, sk, "print HELLO\nidle 60s\n")
	// The gateway namespaces roster/journal ids at the remote egress, so the id the
	// phone sees is EXACTLY the id it commands against.
	namespaced := protocol.NamespacedID(sk.api.endpointID, meta.ID)

	// OBSERVE: the gateway delivers a roster/Group card naming the live session to the
	// phone mailbox; the phonesim decodes it (JournalReceiver + SessionCache). Poll
	// Observe until the phone's cache holds the launched session.
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

	// DRIVE: the phonesim signs+seals a KILL for that session and appends it to the
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

// TestPhonesim_TakeControlTypeE2E is the A7 input acceptance milestone: a phone TAKES
// CONTROL of a session and TYPES into it, end to end over the real in-process relay +
// gateway + daemon, with the keystroke proven to reach the session's PTY.
//
// The proof is behavioral, exactly as in the LeaseManager/lease-conn slices: the fake
// agent BLOCKS on `ask`, reading one line off its PTY per directive, and F3 suppresses
// the remote controller's echo -- so the ONLY way the ask-blocked script advances is a
// keystroke reaching its PTY. A two-`ask` script needs TWO DISTINCT keystrokes to exit,
// which lets ONE test carry both the milestone (distinct keystrokes drive the session to
// completion) AND the adversarial check (a replayed input envelope, same seq, is dropped
// by the gateway's single Accept and never counts as a second keystroke).
//
// The full chain exercised: phone.TakeControl -> relay mailbox -> gateway
// (routeCommand -> LeaseManager.Begin: the daemon grants the lease) -> phone.Type ->
// relay mailbox -> gateway (routeInput -> LeaseManager.Input on the SAME lease conn) ->
// daemon handleDataIn -> the session's PTY. Commands AND input share ONE phonecore
// Sequencer, so the gateway's single (sender, epoch) MailboxReceiver accepts them as one
// strictly-increasing seq stream.
func TestPhonesim_TakeControlTypeE2E(t *testing.T) {
	h := newPhonesimHarness(t)
	phone, sk, ctx := h.phone, h.sk, h.ctx

	// A fake session that BLOCKS on stdin twice: each `ask` reads exactly one line, so
	// the script leaves running only after TWO DISTINCT keystrokes have reached its PTY.
	meta := launchFake(t, sk, "ask one?\nask two?\nexit 0\n")
	session := protocol.NamespacedID(sk.api.endpointID, meta.ID)

	// TAKE CONTROL: the phone signs a take_control (real Ed25519 over SHA256(gateToken))
	// and seals it to the machine mailbox; the gateway's command-IN loop routes it to
	// LeaseManager.Begin, which dials the daemon remote.sock and the daemon grants the
	// controller lease. The mailbox is seq-ordered, so this take_control opens the
	// session's lease before any input frame that follows it; each input then routes by
	// the target session id sealed inside its own frame, not by any mutable focus state.
	if err := phone.TakeControl(ctx, session, "devSIM:01JSIM000000000000TAKE1"); err != nil {
		t.Fatalf("phonesim take_control: %v", err)
	}

	// TYPE (first keystroke): the burst travels phone -> relay mailbox -> gateway
	// (LeaseManager.Input) -> the lease conn -> the daemon -> the session's PTY, where the
	// first `ask` consumes it. Capture the raw wire bytes so we can replay them.
	rawFirst, err := phone.Type(ctx, session, []byte("first\n"))
	if err != nil {
		t.Fatalf("phonesim type (first keystroke): %v", err)
	}

	// REPLAY (adversarial): a relay redelivering the captured ciphertext re-appends the
	// SAME sealed envelope -- same (epoch, seq). The gateway opens each mailbox item
	// through ONE MailboxReceiver.Accept, which rejects the replayed seq as stale, so the
	// replay never reaches routeInput and never lands on the PTY.
	if err := phone.Replay(ctx, rawFirst); err != nil {
		t.Fatalf("phonesim replay: %v", err)
	}

	// With only ONE distinct keystroke available (the replay is dropped), a two-`ask`
	// script CANNOT reach `exit`: it consumes the first line and blocks on the second.
	// The session must stay running across a settle window that is several gateway poll
	// cycles long -- if the replay had reached the PTY, its "first\n" would satisfy the
	// second ask and the session would exit here.
	settle := time.Now().Add(1 * time.Second)
	for time.Now().Before(settle) {
		if m, ok := sk.Core().Get(meta.ID); ok && m.Status.Process != status.ProcessRunning {
			t.Fatal("session exited after a single distinct keystroke + a replay; the gateway did NOT drop the replayed input (it reached the PTY a second time)")
		}
		time.Sleep(30 * time.Millisecond)
	}

	// TYPE (second, distinct keystroke): a fresh seq the gateway accepts. Now TWO
	// distinct keystrokes have reached the PTY, so the second `ask` is satisfied and the
	// script exits -- the milestone: the phone typed into the session end to end.
	if _, err := phone.Type(ctx, session, []byte("second\n")); err != nil {
		t.Fatalf("phonesim type (second keystroke): %v", err)
	}
	if !waitSessionExited(t, sk, meta.ID, 5*time.Second) {
		t.Fatal("session never left running after two distinct keystrokes; input did not reach the session's PTY over the take_control lease (the take_control-type chain is broken)")
	}
}

// TestPhonesim_ObserveTerminalE2E is the A7 terminal-PEEK acceptance milestone (cross-model
// review finding #2): a phone WATCHES a session and receives the daemon's SERVER-RENDERED,
// sanitized terminal snapshots end to end over the real in-process relay + gateway + daemon,
// and the kill switch BLANKS an established peek.
//
// The full chain exercised: phone.Watch -> relay mailbox -> gateway (routeCommand ->
// TerminalWatcher.Watch -> Gateway.RunTerminal, which now carries the session id in the
// terminal_subscribe frame so the daemon's resolveSession accepts it) -> daemon
// handleTerminalSubscribe (read-only tap + RenderTerminal) -> RelaySink.Terminal seals each
// snapshot into THIS phone's mailbox on the shared seq stream -> phone.Observe demuxes it via
// the phonecore.MailboxRouter into the snapshot cache. Before this wiring RunTerminal sent no
// session id (the daemon refused it) and nothing ran RunTerminal, so no snapshot ever reached
// the phone.
//
// Two assertions:
//  1. TERMINAL REACHED PHONE: a snapshot whose lines contain the session's first printed
//     marker arrives at the phone (rendered + sanitized server-side).
//  2. KILL SWITCH BLANKS THE PEEK: after the only device is revoked (RemoteControlEnabled ->
//     false, i.e. `swarm remote off`), a marker the session prints AFTERWARD never reaches
//     the phone -- the daemon re-checks the switch before every emission and stops the peek.
func TestPhonesim_ObserveTerminalE2E(t *testing.T) {
	h := newPhonesimHarness(t)
	phone, sk, ctx := h.phone, h.sk, h.ctx

	// A session that prints a first marker, idles long enough for the peek to establish and
	// the kill switch to flip, then prints a SECOND marker that must be blanked once off.
	const (
		firstMark  = "HELLO-PEEK"
		secondMark = "AFTER-OFF-SECRET"
	)
	meta := launchFake(t, sk, "print "+firstMark+"\nidle 2s\nprint "+secondMark+"\nidle 60s\n")
	session := protocol.NamespacedID(sk.api.endpointID, meta.ID)

	// WATCH: the phone asks the machine to open a read-only peek for the session. The
	// unsigned terminal_watch rides the machine mailbox; the gateway routes it to its
	// TerminalWatcher, which runs RunTerminal (a read-only terminal_subscribe) against the
	// daemon and seals each rendered snapshot back to this phone's mailbox.
	if err := phone.Watch(ctx, session); err != nil {
		t.Fatalf("phonesim watch: %v", err)
	}

	// ASSERT 1: the server-rendered snapshot carrying firstMark reaches the phone. Poll
	// Observe (which drains snapshots off the shared mailbox into the phone's snapshot cache)
	// until the marker appears in a rendered line.
	sawFirst := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !sawFirst {
		if _, err := phone.Observe(ctx); err != nil {
			t.Fatalf("phonesim observe: %v", err)
		}
		if lines, ok := phone.Snapshot(session); ok && linesContain(lines, firstMark) {
			sawFirst = true
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if !sawFirst {
		t.Fatalf("phone never received a terminal snapshot containing %q; the terminal peek is not wired end to end (RunTerminal session id / TerminalWatcher / phone MailboxRouter)", firstMark)
	}

	// FLIP THE KILL SWITCH OFF: revoke the only paired device, so RemoteControlEnabled()
	// derives false. This is `swarm remote off`. The flip happens well within the session's
	// 2s idle, so it precedes the render of secondMark.
	if _, err := sk.api.RevokeDevice(h.deviceID); err != nil {
		t.Fatalf("revoke device (flip kill switch off): %v", err)
	}
	if sk.api.RemoteControlEnabled() {
		t.Fatal("kill switch still reports enabled after revoking the only paired device")
	}

	// ASSERT 2: the session prints secondMark at session-time ~2s (after the flip). The
	// daemon re-checks the kill switch before EVERY snapshot emission, so secondMark must
	// NEVER reach the phone. Poll a settle window comfortably longer than when a LIVE peek
	// would have delivered it.
	settle := time.Now().Add(3 * time.Second)
	for time.Now().Before(settle) {
		if _, err := phone.Observe(ctx); err != nil {
			t.Fatalf("phonesim observe (settle): %v", err)
		}
		if lines, ok := phone.Snapshot(session); ok && linesContain(lines, secondMark) {
			t.Fatalf("phone received %q AFTER `swarm remote off`; the kill switch did NOT blank the established peek", secondMark)
		}
		time.Sleep(30 * time.Millisecond)
	}
}

// linesContain reports whether any rendered line contains marker.
func linesContain(lines []string, marker string) bool {
	for _, l := range lines {
		if strings.Contains(l, marker) {
			return true
		}
	}
	return false
}
