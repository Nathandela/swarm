package conformance_test

// Slice S9 -- PB-NET-1: the real relay.Client drives the phone core THROUGH the gomobile
// facade, over a real in-process relay, for the whole of the stated criterion:
// pair -> read -> ack -> append.
//
// WHY THIS TEST EXISTS AT ALL, given how much of the tree already looks like it.
//
// internal/phonesim is the phone stand-in almost every integration test in this repo
// drives, and it is NOT the shipped path: phonesim.Phone.Type seals with
// phonecore.SealInputData and calls relay.MailboxAppend DIRECTLY, never entering
// mobile/commands.go. So the coalescer, the lease gate, the durable input-seq reservation
// and sendInputFrame's ordering lock -- everything the real Android app's keystrokes go
// through -- are covered by unit tests and by nothing end to end. That is standing defect
// class (v): a fence guarding a path production does not take. It has already cost this
// project twice (PB-NET-5's latency harness measures phonesim -> PTY and cannot see a
// facade regression at all; the S11 input-inversion defect shipped partly because its
// fence guarded a seam production does not take).
//
// The S8 conformance harness (harness_test.go) already drives the facade over a real
// relay -- but it SEEDS the paired state, by design ("exactly PB-BIND-3's state lifecycle
// (restore) path"), so its MachineRelayAuthPub is a test fixture rather than something a
// handshake produced. And the two pairing tests that DO run a real handshake
// (TestPBSAS2_..., TestPBPAIR5_...) publish MachineRelayAuthPub: make([]byte, 32) -- a
// zero key -- and send nothing afterwards, because what they assert is the SAS and the
// terminal states.
//
// So the four steps have each been driven, and the SEQUENCE never has: nothing anywhere
// proves that what the pairing handshake teaches the phone is what the phone then reads,
// acks and appends over. That composition is the requirement, and it is the whole of what
// is new here.
//
// THE FOUR STEPS, and what each one is proved by:
//
//	pair   -- a real pairing.Machine responder over the real relay rendezvous. The phone
//	          runs App.BeginPairing/SAS/Confirm. The machine publishes its REAL relay-auth
//	          pub, so pin() -> setDestination targets a mailbox that actually exists, and
//	          the machine authorizes the phone using the routing key the HANDSHAKE carried.
//	read   -- App.drain's real relay.Client.MailboxRead, into the core's real AcceptCommit,
//	          surfacing on the facade's own read models (StateSummary, Roster).
//	ack    -- the relay DELETES acked items (store.ackItems), so the phone's mailbox depth
//	          measured on the real server IS the ack. See the staging note at that step for
//	          why the observation is deterministic rather than a race.
//	append -- App.TakeControl and App.SendInput: signed command, lease gate, coalescer,
//	          durable NextInput reservation, sendInputFrame's seal-and-append under
//	          inputMu. Opened machine-side by the real remotegw openers.
//
// This file contains NO implementation.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	swarmmobile "github.com/Nathandela/swarm/mobile"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/pairing"
	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/remotegw"
)

const s9Session = testMachineID + "/sess-s9"

// s9Typed is the keystroke burst the phone types once the lease is confirmed. It is the
// payload whose arrival at the machine proves the facade's OWN input path ran: the
// coalescer buffered it, the lease gate admitted it, and sendInputFrame sealed and
// appended it through the real client.
const s9Typed = "ls -la"

// s9Tail is a second burst issued inside the first one's coalescing window, so it can only
// reach the machine via the coalescer's tail-flush timer. See the send site for why one
// burst alone leaves that machinery unexercised.
const s9Tail = "\r"

func TestPBNET1_TheFacadeDrivesTheRealClientFromPairingThroughAppend(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// ---- the relay: the real server, in process, over a real WebSocket ----------
	rcfg := relay.DefaultConfig()
	rcfg.DBPath = filepath.Join(t.TempDir(), "relay.db")
	srv, err := relay.New(rcfg)
	if err != nil {
		t.Fatalf("relay.New: %v", err)
	}
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("relay start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	// ---- the machine: its own relay identity, its own client, its own epoch keys --
	machineID, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("machine identity: %v", err)
	}
	keys, err := crypto.NewEpochKeys()
	if err != nil {
		t.Fatalf("epoch keys: %v", err)
	}
	mAuthPub, mAuthPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("machine relay-auth key: %v", err)
	}
	mSignPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("machine grant-signing key: %v", err)
	}
	machineTarget := relay.RoutingID(mAuthPub)

	machineCl, err := relay.Dial(ctx, srv.URL(), relay.ClientAuth{
		RelayAuthPub: mAuthPub,
		Sign:         func(c []byte) ([]byte, error) { return ed25519.Sign(mAuthPriv, c), nil },
	})
	if err != nil {
		t.Fatalf("machine dial: %v", err)
	}
	t.Cleanup(func() { _ = machineCl.Close() })

	// ---- the phone: a FRESH state directory and the shipped facade over it -------
	//
	// NOTHING is seeded. Every machine coordinate this test later depends on has to
	// arrive through the pairing handshake, which is the composition the requirement is
	// about.
	app, err := swarmmobile.NewApp(&swarmmobile.Config{
		StateDir:  t.TempDir(),
		RelayURL:  srv.URL(),
		MachineID: testMachineID,
	}, newTestCustody(t))
	if err != nil {
		t.Fatalf("swarmmobile.NewApp: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	if err := app.Start(); err != nil {
		t.Fatalf("App.Start: %v", err)
	}

	// NON-VACUITY, before anything else. If the phone can already reach the machine here,
	// then "pair" is not a step in this test -- it is decoration on a phone that was
	// pre-wired, and every assertion after it would hold with the handshake deleted.
	//
	// TerminalWatch is the probe deliberately: it is an UNSIGNED read, so it is gated by
	// neither the reconcile refusal nor the lease, and it resolves its destination before
	// it touches the connection. What it must fail on is the destination itself.
	if err := app.TerminalWatch(s9Session); err == nil {
		t.Fatalf("an unpaired phone reached the machine: TerminalWatch succeeded before any " +
			"pairing handshake ran. The machine's routing coordinate can only come from the " +
			"handshake (mobile/pairing.go pin -> setDestination), so a phone that already has " +
			"one was pre-wired -- and every step below would then pass with the pairing deleted")
	}

	// ---- STEP 1 of 4: PAIR ------------------------------------------------------
	var secret [32]byte
	var rid [16]byte
	if _, err := rand.Read(secret[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	if _, err := rand.Read(rid[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}

	var (
		mu         sync.Mutex
		machineSAS [6]string
		outcome    *pairing.MachineOutcome
		pairErr    error
		sasSeen    = make(chan struct{})
		pairDone   = make(chan struct{})
	)
	m := pairing.NewMachine(pairing.MachineParams{
		Static:       machineID.NoiseStatic(),
		Secret:       secret,
		RendezvousID: rid,
		LocalConsole: true,
		Confirm: func(_ context.Context, got [6]string, _ string) (bool, error) {
			mu.Lock()
			machineSAS = got
			mu.Unlock()
			close(sasSeen)
			return true, nil
		},
		Payload: pairing.MachinePayload{
			Hostname: "s9.local",
			// THE REAL COORDINATES, and that is the point of this test rather than an
			// incidental choice. The existing pairing conformance tests publish a ZERO
			// MachineRelayAuthPub, which is harmless for a SAS assertion and fatal for
			// anything that then has to SEND: pin() derives the phone's whole destination
			// from this field, so a zero here means the phone appends into a mailbox no
			// one owns and the append half of the criterion cannot be observed at all.
			MachineRoutingID:    []byte(machineTarget),
			MachineRelayAuthPub: mAuthPub,
			RecipientPub:        machineID.RecipientPublic(),
			MachineSignPub:      mSignPub,
			EpochID:             testEpochID,
		},
	})

	rzConn, err := relay.DialRaw(ctx, srv.URL())
	if err != nil {
		t.Fatalf("machine DialRaw for the rendezvous: %v", err)
	}
	t.Cleanup(func() { _ = rzConn.Close() })
	go func() {
		defer close(pairDone)
		out, err := m.Pair(ctx, &relayRendezvous{conn: rzConn, label: hex.EncodeToString(rid[:])})
		mu.Lock()
		outcome, pairErr = out, err
		mu.Unlock()
	}()

	qr, err := pairing.EncodeQR(pairing.QRPayload{
		RelayURL:      srv.URL(),
		RendezvousID:  rid,
		PairingSecret: secret,
	})
	if err != nil {
		t.Fatalf("EncodeQR: %v", err)
	}

	p, err := app.BeginPairing(qr)
	if err != nil {
		t.Fatalf("App.BeginPairing: %v", err)
	}
	// PB-PAIR-6 (S16): the destination is confirmed before anything is joined.
	s16PassOriginGate(t, p)
	var phoneSAS string
	eventually(t, "the phone never derived a SAS", func() bool {
		s, err := p.SAS()
		if err == nil && s != "" {
			phoneSAS = s
		}
		return phoneSAS != ""
	})
	select {
	case <-sasSeen:
	case <-time.After(10 * time.Second):
		t.Fatalf("the machine never reached its SAS confirm gate")
	}
	mu.Lock()
	wantSAS := strings.Join(machineSAS[:], " ")
	mu.Unlock()
	if phoneSAS != wantSAS {
		t.Fatalf("the two ends derived different SAS strings (phone %q, machine %q); the "+
			"handshake did not produce a shared channel binding, so nothing below is pairing "+
			"with this machine", phoneSAS, wantSAS)
	}
	if err := p.Confirm(); err != nil {
		t.Fatalf("Pairing.Confirm: %v", err)
	}
	eventually(t, "the pairing never reached its terminal paired state", func() bool {
		st, err := p.State()
		return err == nil && st == "paired"
	})
	select {
	case <-pairDone:
	case <-time.After(10 * time.Second):
		t.Fatalf("the machine side of the pairing never returned")
	}
	mu.Lock()
	out, perr := outcome, pairErr
	mu.Unlock()
	if perr != nil || out == nil {
		t.Fatalf("the machine side of the pairing failed: outcome=%v err=%v", out, perr)
	}

	// The handshake, not a fixture, is what the phone now holds.
	sum, err := app.StateSummary()
	if err != nil {
		t.Fatalf("StateSummary after pairing: %v", err)
	}
	if sum.EpochID != int64(testEpochID) {
		t.Fatalf("the phone holds epoch %d after pairing, want the %d the machine published "+
			"in its handshake payload; the pairing outcome was not pinned", sum.EpochID, testEpochID)
	}
	// A pairing that leaves the phone without its machine's ENDPOINT identity has not
	// finished the step, however complete the handshake looks. That identity is a signed
	// field of every mutating command (crypto.Command.Machine), so an empty one is not a
	// cosmetic gap: Canonical() refuses the tuple outright and no command can be authored
	// at all. It is reported here, where it is caused, rather than only where it bites --
	// but NOT fatally, so the read and ack steps below still run and stay meaningful.
	if sum.Machine != testMachineID {
		t.Errorf("after a successful pairing the phone's machine endpoint id is %q, want %q.\n"+
			"Config.MachineID reaches phonecore only as phonecore.Config.Machine, which OpenStore "+
			"keeps as a load-time FILTER (state.go: `if machineID != \"\" && f.Machine != machineID`) "+
			"and never as an initialiser, and pin() does not set it either -- so on a FRESH install "+
			"State.Machine stays empty for the life of the install. Two consequences, both "+
			"reachable by a first-launch pair-then-use: every signed command fails "+
			"crypto.ErrBadCommand before it is sealed, and the blob is persisted with Machine=\"\" "+
			"so the NEXT process start discards the whole durable state -- pairing, epoch, content "+
			"key, cursors and send-seq ceilings -- against that same filter.",
			sum.Machine, testMachineID)
	}
	if !sum.Restored {
		t.Errorf("StateSummary.Restored is false after a successful pairing; it is derived from " +
			"State.Machine, so the app cannot tell a paired install from a fresh one")
	}

	// The machine authorizes the phone with the routing key AND THE ROUTE CONSENT the
	// HANDSHAKE carried (msg3's DevicePayload), which is the only place it can have learned
	// either. The consent is what makes the call legal at all (ADR-007 B27/B38) and it records
	// both directed edges, so this one call is what makes appends legal in both directions --
	// and both values are deliberately driven from the pairing outcome rather than from
	// something this test kept on the side, because a fixture here would let the handshake
	// carry nothing and every step below still pass.
	devicePub := out.Device.DeviceRelayAuthPub
	if len(devicePub) != ed25519.PublicKeySize {
		t.Fatalf("the pairing outcome carried a %d-byte device relay-auth pub, want %d; the "+
			"machine cannot address the phone at all", len(devicePub), ed25519.PublicKeySize)
	}
	phoneTarget := relay.RoutingID(ed25519.PublicKey(devicePub))
	if err := machineCl.AuthorizeDevice(ctx, ed25519.PublicKey(devicePub), out.Device.ConsentSig); err != nil {
		t.Fatalf("machine authorize phone: %v", err)
	}

	// The machine's real outbound sealer -- the same code the gateway runs -- pointed at
	// the mailbox the handshake named.
	sink := remotegw.NewRelaySink(remotegw.RelayConfig{
		Appender:       machineCl,
		Target:         phoneTarget,
		Machine:        testMachineID,
		EpochID:        testEpochID,
		Key:            keys.ContentKey,
		RecipientKeyID: crypto.KeyID(out.Device.RecipientPub),
		SenderKeyID:    crypto.KeyID(machineID.RecipientPublic()),
		Authorities:    fixedAuthorities{},
	})

	// ---- STEP 2 and 3 of 4: READ and ACK ----------------------------------------
	//
	// STAGING, and why the depth observation below is deterministic rather than a race.
	// These two frames are published BEFORE the content key is installed. The phone
	// therefore cannot OPEN them: MailboxRouter.AcceptCommit fails at crypto.OpenMailbox,
	// which is neither ErrStaleSeq nor ErrStaleAge, so it returns WITHOUT acking
	// (snapshot.go:620) and the durable cursor does not move. The mailbox depth is then a
	// stable precondition, not something that happens to be observable before a drain
	// races past it -- which matters, because "depth reached zero" is the whole of the ack
	// evidence and an assertion that could pass by never having had anything to ack is
	// exactly standing defect class (i).
	if err := sink.Reconcile(); err != nil {
		t.Fatalf("sink.Reconcile: %v", err)
	}
	if err := sink.Snapshot([]schema.JournalRecord{
		{Cursor: 1, SessionID: s9Session, Type: "roster", Group: "working"},
	}, 1); err != nil {
		t.Fatalf("sink.Snapshot: %v", err)
	}
	if d := srv.MailboxDepth(phoneTarget); d == 0 {
		t.Fatalf("the machine's frames never reached the phone's mailbox on the relay (depth 0). " +
			"Either the append was refused -- the pairing relation the handshake established is " +
			"not there -- or the phone acked frames it cannot possibly have opened")
	}

	if err := app.InstallContentKey(keys.ContentKey[:]); err != nil {
		t.Fatalf("App.InstallContentKey: %v", err)
	}

	// READ. Both read models are produced ONLY by App.drain -> relay.Client.MailboxRead ->
	// MailboxRouter.AcceptCommit; there is no other producer in the facade.
	eventually(t, "the phone never adopted the machine's reconcile record", func() bool {
		s, err := app.StateSummary()
		return err == nil && s.Reconciled
	})
	eventually(t, "the roster the machine published never reached the phone's read model", func() bool {
		list, err := app.Roster()
		if err != nil {
			return false
		}
		n, err := list.Count()
		return err == nil && n > 0
	})
	sum, err = app.StateSummary()
	if err != nil {
		t.Fatalf("StateSummary after the drain: %v", err)
	}
	if sum.RelayCursor == 0 {
		t.Errorf("the phone's DURABLE relay cursor is still 0 after consuming the machine's " +
			"frames. The cursor is the relay's own storage coordinate, so a zero here means the " +
			"read models were populated by something that never went through the mailbox")
	}

	// ACK. The relay DELETES acked items (store.ackItems), so depth returning to zero is
	// the phone's MailboxAck arriving -- issued off the delivery path by the wait drain's
	// transport.AckBatcher (or by App.flushAcks in the poll fallback), from the cursor
	// relayAcker collected inside the core's receive transaction.
	eventually(t, "the phone never acked the items it consumed", func() bool {
		return srv.MailboxDepth(phoneTarget) == 0
	})

	// ---- STEP 4 of 4: APPEND ----------------------------------------------------
	//
	// The machine drains its own mailbox through the real client and opens every frame
	// with the real gateway opener over one shared inbound seq guard -- one Accept per
	// envelope, then dispatch on the decoded discriminator.
	recv := crypto.NewMailboxReceiver()
	var (
		mCursor  uint64
		commands []schema.RemoteCommand
		inputs   []remotegw.InputFrame
	)
	drainMachine := func() {
		t.Helper()
		items, err := machineCl.MailboxRead(ctx, mCursor)
		if err != nil {
			t.Fatalf("machine mailbox read: %v", err)
		}
		for _, it := range items {
			if it.Cursor > mCursor {
				mCursor = it.Cursor
			}
			fr, err := remotegw.OpenMailboxFrame(recv, keys.ContentKey, it.Envelope)
			if err != nil {
				continue
			}
			if fr.Kind == remotegw.FrameInput {
				inputs = append(inputs, fr.Input)
				continue
			}
			commands = append(commands, fr.Command)
		}
		if err := machineCl.MailboxAck(ctx, mCursor); err != nil {
			t.Fatalf("machine mailbox ack: %v", err)
		}
	}
	awaitCommand := func(action string) schema.RemoteCommand {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			drainMachine()
			for _, c := range commands {
				if c.Action == action {
					return c
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("no %q command reached the machine within 5s", action)
		return schema.RemoteCommand{}
	}

	// APPEND, first half: a SIGNED mutating command, over coordinates the handshake
	// taught the phone.
	op, err := app.TakeControl(s9Session)
	if err != nil {
		t.Fatalf("App.TakeControl: %v", err)
	}
	cmd := awaitCommand(schema.ActionTakeControl)
	if cmd.Session != s9Session {
		t.Errorf("the take_control that arrived names session %q, want %q", cmd.Session, s9Session)
	}
	if cmd.OperationID != op.OperationID {
		t.Errorf("the take_control that arrived carries operation id %q, but the facade handed "+
			"the caller %q. The op the app tracks in flight is not the op the machine will "+
			"answer, so its reply can never resolve", cmd.OperationID, op.OperationID)
	}

	// The machine confirms the lease, exactly as remotegw.CommandBridge.confirmLease does.
	// PB-INPUT-2 refuses every keystroke until this is adopted, so without it the input
	// half below would be exercising the refusal path rather than the send path.
	replyEnv, err := remotegw.SealControlReply(keys.ContentKey, testEpochID, 1, schema.Control{
		Op:          protocol.OpLease,
		EndpointID:  testMachineID,
		SessionID:   s9Session,
		OperationID: cmd.OperationID,
		Generation:  1,
	})
	if err != nil {
		t.Fatalf("SealControlReply: %v", err)
	}
	if _, err := machineCl.MailboxAppend(ctx, phoneTarget, replyEnv); err != nil {
		t.Fatalf("append the lease grant: %v", err)
	}

	// The gate opens only once the phone has DRAINED and adopted that grant, which is the
	// read plane feeding the send plane -- the composition this whole test is about. The
	// probe is an empty SendInput: it is precisely the gate, and it adds no bytes.
	gateDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(gateDeadline) {
		if err := app.SendInput(s9Session, nil); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := app.SendInput(s9Session, nil); err != nil {
		t.Fatalf("the lease grant the machine sealed was never adopted by the phone: %v", err)
	}

	// APPEND, second half: the LIVE INPUT path. This is the seam phonesim bypasses
	// entirely -- coalescer, lease gate, durable NextInput reservation, and
	// sendInputFrame's seal-and-append under inputMu -- and it is the reason PB-NET-1 is
	// a requirement rather than a formality.
	if err := app.SendInput(s9Session, []byte(s9Typed)); err != nil {
		t.Fatalf("App.SendInput: %v", err)
	}
	// A SECOND burst, issued immediately -- inside the 125 ms window the first one just
	// opened. It is here because a single burst does NOT exercise the coalescer: with the
	// window already open, phonecore.InputCoalescer.Type releases the bytes synchronously
	// and sendCoalesced appends them on the caller's goroutine, so the pacing machinery is
	// traversed without any of it mattering. (Measured: deleting scheduleDrain's tail-flush
	// timer left a one-burst version of this test GREEN.) These bytes cannot go out that
	// way -- they are buffered, and the only thing that can release them is the tail-flush
	// timer firing on time.AfterFunc's goroutine. That is PB-INPUT-5's pacing and the
	// SECOND producer of the input bucket, which is the whole reason sendInputFrame holds
	// inputMu across allocate-seal-append.
	if err := app.SendInput(s9Session, []byte(s9Tail)); err != nil {
		t.Fatalf("App.SendInput (the held tail): %v", err)
	}

	// Both bursts, IN ORDER. The concatenation is the assertion rather than a per-frame
	// match because the coalescer is free to merge or split what it holds -- what it is
	// never free to do is reorder, drop, or duplicate the user's keystrokes. Comparing the
	// joined stream is what makes this an ordering guard as well as a delivery one, which
	// is the S11 input-inversion defect class.
	want := s9Typed + s9Tail
	var got string
	inputDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(inputDeadline) {
		drainMachine()
		got = ""
		for _, in := range inputs {
			if in.Kind == "data" {
				got += string(in.Data)
			}
		}
		if got == want {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got != want {
		t.Fatalf("the machine received the keystroke stream %q, want %q.\n"+
			"Every byte of live input the Android app sends goes through App.SendInput -> the "+
			"coalescer -> sendInputFrame -> relay.Client.MailboxAppend, and nothing else in this "+
			"tree drives that path end to end (internal/phonesim seals with "+
			"phonecore.SealInputData and calls relay.MailboxAppend directly, bypassing all of "+
			"it). %d command(s) and %d input frame(s) arrived.", got, want, len(commands), len(inputs))
	}
	for _, in := range inputs {
		if in.Kind == "data" && in.Session != s9Session {
			t.Errorf("an input frame arrived naming session %q, want %q. The target is bound "+
				"INSIDE the sealed frame precisely so the machine never routes by mutable focus "+
				"state", in.Session, s9Session)
		}
	}
}
