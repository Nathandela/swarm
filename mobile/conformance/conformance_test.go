package conformance_test

// FAILING-FIRST (TDD RED, GG-5) runtime conformance for PB-BIND-3 (every method works
// against a real in-process backend), the S1 LaunchContentHash trap, and PB-SAS-1/-2.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
)

const testSession = "machine-endpoint-0001/sess-1"

// TestPBBIND3_EveryFacadeMethodWorksAgainstARealBackend is PB-BIND-3's second criterion.
// The traceability table in ../screen_coverage.tsv is checked structurally by
// ../coverage_test.go; this is the half that proves the methods DO something, over a
// real relay, with real seals opened by the real gateway openers.
func TestPBBIND3_EveryFacadeMethodWorksAgainstARealBackend(t *testing.T) {
	h := newHarness(t)
	app := h.App

	// -- lifecycle.restore: NewApp resumed the seeded coordinates.
	sum, err := app.StateSummary()
	if err != nil {
		t.Fatalf("StateSummary: %v", err)
	}
	if sum.Machine != h.Machine || sum.EpochID != int64(h.EpochID) {
		t.Fatalf("restore lost the durable coordinates: got machine=%q epoch=%d, want %q/%d",
			sum.Machine, sum.EpochID, h.Machine, h.EpochID)
	}
	if !sum.Restored {
		t.Errorf("StateSummary reports Restored=false over a seeded state dir; a phone that " +
			"silently starts from zero is requirements 4.3, restored")
	}

	// -- lifecycle.start: idempotent.
	if err := app.Start(); err != nil {
		t.Fatalf("second Start must be a no-op, got %v", err)
	}
	if running, err := app.IsRunning(); err != nil || !running {
		t.Fatalf("IsRunning after Start = %v, %v; want true, nil", running, err)
	}

	// -- key_custody.install: the single inbound crossing (ADR-007 B8).
	if err := app.InstallWakeKey(h.Keys.WakeKey[:]); err != nil {
		t.Fatalf("InstallWakeKey: %v", err)
	}
	if err := app.InstallContentKey(h.Keys.ContentKey[:]); err != nil {
		t.Fatalf("InstallContentKey: %v", err)
	}

	// -- reconcile first: phonecore fails CLOSED on every mutating op until the machine
	// publishes its rollback authorities (PB-STATE-4 / PB-SYNC-7).
	h.PushReconcile()
	eventually(t, "the reconcile record never cleared the mutating-op refusal", func() bool {
		s, err := app.StateSummary()
		return err == nil && s.Reconciled
	})

	// -- roster + sessions_with_group.
	h.PushRoster(schema.JournalRecord{Cursor: 1, SessionID: testSession, Type: "roster", Group: "needs_you"})
	eventually(t, "the roster never reached the phone", func() bool {
		list, err := app.Roster()
		if err != nil {
			return false
		}
		n, err := list.Count()
		return err == nil && n > 0
	})
	sess, err := app.Session(testSession)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if sess.Group != "needs_you" {
		t.Errorf("Session.Group = %q, want %q taken VERBATIM from the wire (the phone never "+
			"derives a Group on-device)", sess.Group, "needs_you")
	}
	if _, err := app.Presence(); err != nil {
		t.Fatalf("Presence: %v", err)
	}

	// -- journal.read + journal.subscribe.
	if err := app.SubscribeJournal(); err != nil {
		t.Fatalf("SubscribeJournal: %v", err)
	}
	h.PushEvent(schema.JournalRecord{Cursor: 2, SessionID: testSession, Type: "group_transition", Group: "working"})
	eventually(t, "the journal event never reached the phone", func() bool {
		page, err := app.ReadJournal(0, 100)
		if err != nil {
			return false
		}
		n, err := page.Count()
		return err == nil && n >= 1
	})
	page, err := app.ReadJournal(0, 100)
	if err != nil {
		t.Fatalf("ReadJournal: %v", err)
	}
	if _, err := page.NextCursor(); err != nil {
		t.Fatalf("JournalPage.NextCursor: %v", err)
	}
	if _, err := page.At(0); err != nil {
		t.Fatalf("JournalPage.At(0): %v", err)
	}
	if err := app.UnsubscribeJournal(); err != nil {
		t.Fatalf("UnsubscribeJournal: %v", err)
	}

	// -- terminal_watch / snapshot.peek / terminal_unwatch.
	if err := app.TerminalWatch(testSession); err != nil {
		t.Fatalf("TerminalWatch: %v", err)
	}
	if got := h.AwaitCommand(protocol.ActionTerminalWatch); got.Session != testSession {
		t.Errorf("terminal_watch targeted %q, want %q", got.Session, testSession)
	}
	h.PushTerminal(testSession, []string{"HELLO", "WORLD"}, 80, 24)
	eventually(t, "the terminal snapshot never reached the phone", func() bool {
		snap, err := app.Peek(testSession)
		return err == nil && strings.Contains(snap.Text, "HELLO")
	})
	snap, err := app.Peek(testSession)
	if err != nil {
		t.Fatalf("Peek: %v", err)
	}
	if snap.Cols != 80 || snap.Rows != 24 {
		t.Errorf("Peek geometry = %dx%d, want 80x24", snap.Cols, snap.Rows)
	}
	if strings.Contains(snap.Text, "\x1b") {
		t.Errorf("PB-APP-4: the peek text carries an escape sequence; only daemon-sanitized " +
			"SnapText may be rendered (no VT emulator on device, ADR-007 D2)")
	}
	if err := app.TerminalUnwatch(testSession); err != nil {
		t.Fatalf("TerminalUnwatch: %v", err)
	}
	h.AwaitCommand(protocol.ActionTerminalUnwatch)

	// -- take_control.acquire, input.send, input.resize, take_control.release.
	tc, err := app.TakeControl(testSession)
	if err != nil {
		t.Fatalf("TakeControl: %v", err)
	}
	if tc.OperationID == "" {
		t.Errorf("TakeControl returned no operation id; PB-SYNC-2 cannot attribute its outcome")
	}
	h.AwaitCommand(protocol.ActionTakeControl)

	if err := app.SendInput(testSession, []byte("ls\r")); err != nil {
		t.Fatalf("SendInput: %v", err)
	}
	in := h.AwaitInput("data")
	if in.Session != testSession {
		t.Errorf("input frame bound session %q, want %q -- the machine routes by the id INSIDE "+
			"the sealed frame, never by mutable focus state", in.Session, testSession)
	}
	if string(in.Data) != "ls\r" {
		t.Errorf("input payload = %q, want %q", in.Data, "ls\r")
	}
	if err := app.Resize(testSession, 100, 30); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	if rs := h.AwaitInput("resize"); rs.Cols != 100 || rs.Rows != 30 {
		t.Errorf("resize frame = %dx%d, want 100x30", rs.Cols, rs.Rows)
	}
	if _, err := app.ReleaseControl(testSession); err != nil {
		t.Fatalf("ReleaseControl: %v", err)
	}
	if got := h.AwaitCommand(protocol.OpTakeControlEnd); got.Session != testSession {
		t.Errorf("take_control_end targeted %q, want %q", got.Session, testSession)
	}

	// -- launch, kill, interrupt, revoke, kill_switch.
	launched, err := app.Launch(&swarmmobile.LaunchSpec{Agent: "claude", Cwd: "/tmp", Prompt: "hello"})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	h.AwaitCommand(protocol.ActionLaunch)

	if _, err := app.Kill(testSession); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	h.AwaitCommand(protocol.ActionKill)

	if _, err := app.Interrupt(testSession); err != nil {
		t.Fatalf("Interrupt: %v -- PB-BIND-3 lists interrupt as a required screen element, and "+
			"screen_coverage.tsv records that NO wire verb exists for it today. Either the verb "+
			"lands, or the element is explicitly reassigned like PB-PUSH-8 was", err)
	}

	if _, err := app.RevokeThisDevice(); err != nil {
		t.Fatalf("RevokeThisDevice: %v -- ActionDeviceRevoke is in the signed action set and the "+
			"daemon serves it, but remotegw opForAction does not map it, so the phone-sealed "+
			"command is refused \"unsupported command action\"", err)
	}
	if _, err := app.KillSwitchEngaged(); err != nil {
		t.Fatalf("KillSwitchEngaged: %v", err)
	}

	// -- op.outcome, KEYED. The daemon's reply resolves the launch by operation id.
	h.Reply(schema.Control{Op: "ok", OperationID: launched.OperationID, EndpointID: h.Machine})
	eventually(t, "the launch outcome never resolved", func() bool {
		out, err := app.Outcome(launched.OperationID)
		return err == nil && out.Resolved
	})
	if n, err := app.PendingOpCount(); err != nil {
		t.Fatalf("PendingOpCount: %v", err)
	} else if n < 0 {
		t.Errorf("PendingOpCount = %d", n)
	}

	// -- push.token_register and push.preferences.
	if err := app.RegisterPushToken("fcm-token-abc"); err != nil {
		t.Fatalf("RegisterPushToken: %v", err)
	}
	if _, err := app.SetPushPreference(&swarmmobile.PushPreference{Alerts: true, Mentions: false}); err != nil {
		t.Fatalf("SetPushPreference: %v -- PB-PUSH-8's verb is owned by S12; S8 owns the surface, "+
			"so this must at minimum persist and report the preference", err)
	}
	pref, err := app.PushPreference()
	if err != nil {
		t.Fatalf("PushPreference: %v", err)
	}
	if !pref.Alerts || pref.Mentions {
		t.Errorf("PushPreference round-trip = %+v, want {Alerts:true Mentions:false}", *pref)
	}
	if err := app.DeletePushToken(); err != nil {
		t.Fatalf("DeletePushToken: %v", err)
	}

	// -- connection_state, stale_state, resync.
	if st, err := app.ConnectionState(); err != nil || st == "" {
		t.Fatalf("ConnectionState = %q, %v; want a non-empty state", st, err)
	}
	if st, err := app.StreamState("journal"); err != nil || st == "" {
		t.Fatalf("StreamState(journal) = %q, %v; PB-APP-8 needs PER-STREAM staleness, so a stale "+
			"view is never presented as live", st, err)
	}
	if err := app.Resync("journal"); err != nil {
		t.Fatalf("Resync: %v", err)
	}

	// -- lifecycle.stop: idempotent.
	if err := app.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := app.Stop(); err != nil {
		t.Fatalf("second Stop must be a no-op, got %v", err)
	}
	if err := app.PurgeKeys(); err != nil {
		t.Fatalf("PurgeKeys: %v", err)
	}
}

// TestS8Trap_LaunchContentHashMatchesTheCanonicalEncoding is the test S1 review R2 said
// did not exist: "reimplementing its canonical length-prefixed encoding in the facade is
// NOT PERMITTED, because a one-byte divergence produces silent signature verification
// failures with no compile error AND NO TEST LINKING THE TWO IMPLEMENTATIONS."
//
// This is that test. It drives a launch through the facade, opens the real sealed command
// on the machine side, and asserts the ContentHash the phone signed is BYTE-EQUAL to
// protocol.LaunchContentHash of the spec that travelled with it. A divergent encoding
// fails here instead of failing silently on a real daemon.
func TestS8Trap_LaunchContentHashMatchesTheCanonicalEncoding(t *testing.T) {
	h := newHarness(t)
	h.PushReconcile()
	eventually(t, "reconcile never adopted", func() bool {
		s, err := h.App.StateSummary()
		return err == nil && s.Reconciled
	})

	spec := &swarmmobile.LaunchSpec{Agent: "claude", Cwd: "/tmp", Prompt: "write a haiku", Options: ""}
	if _, err := h.App.Launch(spec); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	cmd := h.AwaitCommand(protocol.ActionLaunch)

	if cmd.Launch == nil {
		t.Fatalf("the sealed launch carried no LaunchReq; the daemon recomputes the hash from the " +
			"FORWARDED spec, so a launch with no spec can never verify")
	}
	want := protocol.LaunchContentHash(cmd.Launch)
	if len(cmd.ContentHash) != len(want) || string(cmd.ContentHash) != string(want) {
		t.Errorf("S8 trap: the facade signed ContentHash %x but the canonical "+
			"protocol.LaunchContentHash of the very spec it sent is %x. This is the silent "+
			"signature-verification failure S1 review R2 predicted: no compile error, no daemon "+
			"error until a real launch is refused", cmd.ContentHash, want)
	}
	if cmd.Session != protocol.LaunchSessionSentinel {
		t.Errorf("launch signed Session %q, want the reserved %q", cmd.Session, protocol.LaunchSessionSentinel)
	}
}

// TestPBSAS2_PhoneSASMatchesTheMachineAndTheKAT is PB-SAS-1 + PB-SAS-2 at runtime: the
// SAS is computed by the shared Go core, surfaced as ONE display string, and equals what
// the machine shows -- the compare-both-screens check. The KAT vectors pin channel
// binding -> six emoji so a Kotlin re-implementation could be caught by a cross-language
// gate; they are written as code points, never as literal emoji.
func TestPBSAS2_PhoneSASMatchesTheMachineAndTheKAT(t *testing.T) {
	// The KAT half, against the frozen core. Six code points per vector, from the 36-bit
	// widening recorded in the 2026-07-23 ADR amendment.
	kats := []struct {
		binding []byte
		want    [6]string
	}{
		{
			[]byte("swarm-phaseB-sas-kat-vector-1"),
			[6]string{"\U00002B50", "\U0001F43C", "\U0001F414", "\U000026A1", "\U0001F4A7", "\U0001F438"},
		},
		{
			[]byte{0x00},
			[6]string{"\U0001F341", "\U0001F351", "\U0001F340", "\U0001F352", "\U0001F986", "\U0001F98A"},
		},
		{
			[]byte("0123456789abcdef0123456789abcdef"),
			[6]string{"\U0001F345", "\U0001F433", "\U0001F34B", "\U0001F388", "\U0001F388", "\U0001F437"},
		},
	}
	for i, kat := range kats {
		got, err := crypto.SAS(kat.binding)
		if err != nil {
			t.Fatalf("KAT %d: crypto.SAS: %v", i, err)
		}
		if got != kat.want {
			t.Fatalf("KAT %d: crypto.SAS drifted. The wordlist and HKDF construction are pinned; "+
				"a change here silently breaks every already-paired device", i)
		}
	}

	// The live half: a real pairing over the real relay rendezvous.
	h := newHarness(t)
	awaitSAS, qr := h.runMachinePairing(t)

	p, err := h.App.BeginPairing(qr)
	if err != nil {
		t.Fatalf("BeginPairing: %v", err)
	}
	origin, err := p.Origin()
	if err != nil {
		t.Fatalf("Pairing.Origin: %v", err)
	}
	if origin == "" {
		t.Errorf("PB-PAIR-6: Pairing.Origin is empty, so nothing can be DISPLAYED before the " +
			"phone joins a relay it learned from a scanned QR")
	}

	var phoneSAS string
	eventually(t, "the phone never derived a SAS", func() bool {
		s, err := p.SAS()
		if err == nil && s != "" {
			phoneSAS = s
		}
		return phoneSAS != ""
	})

	machineSAS := awaitSAS()
	want := strings.Join(machineSAS[:], " ")
	if phoneSAS != want {
		t.Errorf("PB-SAS-2: phone SAS %q != machine SAS %q. The compare-both-screens check is the "+
			"designed anti-MITM defence; if the two ends disagree the user has no way to tell a "+
			"formatting bug from an attack", phoneSAS, want)
	}
	if err := p.Confirm(); err != nil {
		t.Fatalf("Pairing.Confirm: %v", err)
	}
	if st, err := p.State(); err != nil || st == "" {
		t.Fatalf("Pairing.State = %q, %v; PB-PAIR-5 needs user-legible terminal states", st, err)
	}
}

// TestPBPAIR5_CancelIsATerminalStateNotAHang pins the fourth pairing element.
func TestPBPAIR5_CancelIsATerminalStateNotAHang(t *testing.T) {
	h := newHarness(t)
	_, qr := h.runMachinePairing(t)

	p, err := h.App.BeginPairing(qr)
	if err != nil {
		t.Fatalf("BeginPairing: %v", err)
	}
	if err := p.Cancel(); err != nil {
		t.Fatalf("Pairing.Cancel: %v", err)
	}
	st, err := p.State()
	if err != nil {
		t.Fatalf("Pairing.State after Cancel: %v", err)
	}
	if st == "" || st == "pairing" {
		t.Errorf("PB-PAIR-5: after Cancel the pairing state is %q; it must be an explicit, "+
			"user-legible terminal state with the abandoned device keys cleaned up", st)
	}
	if _, err := p.SAS(); err == nil {
		t.Errorf("a cancelled pairing still yields a SAS; the session must be dead")
	}
}

// TestPBBIND3_QRDecodeIsSeparableFromJoining covers the "QR decode" element on its own:
// PB-PAIR-6 requires the destination to be DISPLAYED and confirmed before anything is
// joined, which is impossible if decoding and joining are the same call.
func TestPBBIND3_QRDecodeIsSeparableFromJoining(t *testing.T) {
	var secret [32]byte
	var rid [16]byte
	if _, err := rand.Read(secret[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	if _, err := rand.Read(rid[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	qr, err := pairing.EncodeQR(pairing.QRPayload{
		RelayURL:      "wss://relay.example:8443",
		RendezvousID:  rid,
		PairingSecret: secret,
	})
	if err != nil {
		t.Fatalf("EncodeQR: %v", err)
	}

	got, err := swarmmobile.DecodeQR(qr)
	if err != nil {
		t.Fatalf("DecodeQR: %v", err)
	}
	if got.RelayURL != "wss://relay.example:8443" {
		t.Errorf("DecodeQR RelayURL = %q, want the dialable destination the QR carries (PB-PAIR-7)", got.RelayURL)
	}
	if got.RendezvousID != hex.EncodeToString(rid[:]) {
		t.Errorf("DecodeQR RendezvousID = %q, want %q", got.RendezvousID, hex.EncodeToString(rid[:]))
	}
	if got.HasStaticPub {
		t.Errorf("DecodeQR reports a pinned machine static; S3 decided it is NOT pinned in v1")
	}
	if _, err := swarmmobile.DecodeQR("not-a-swarm-pairing-qr"); err == nil {
		t.Errorf("DecodeQR accepted a malformed payload; it must fail closed")
	}
}

// ---- machine-side pairing over the real relay rendezvous ---------------------

// runMachinePairing starts a real pairing responder and returns the SAS it will display
// plus the QR the phone should scan.
func (h *harness) runMachinePairing(t *testing.T) (func() [6]string, string) {
	t.Helper()

	machineID, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("machine identity: %v", err)
	}
	var secret [32]byte
	var rid [16]byte
	if _, err := rand.Read(secret[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	if _, err := rand.Read(rid[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}

	var (
		mu      sync.Mutex
		sas     [6]string
		sasSeen = make(chan struct{})
	)
	m := pairing.NewMachine(pairing.MachineParams{
		Static:       machineID.NoiseStatic(),
		Secret:       secret,
		RendezvousID: rid,
		LocalConsole: true,
		Confirm: func(_ context.Context, got [6]string, _ string) (bool, error) {
			mu.Lock()
			sas = got
			mu.Unlock()
			close(sasSeen)
			return true, nil
		},
		Payload: pairing.MachinePayload{
			Hostname:            "conformance.local",
			MachineRoutingID:    []byte(h.phoneTarget),
			MachineRelayAuthPub: make([]byte, 32),
			RecipientPub:        machineID.RecipientPublic(),
			MachineSignPub:      h.machineSignPub,
			EpochID:             h.EpochID,
		},
	})

	conn, err := relay.DialRaw(h.ctx, h.RelayURL)
	if err != nil {
		t.Fatalf("machine DialRaw: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	go func() { _, _ = m.Pair(h.ctx, &relayRendezvous{conn: conn, label: hex.EncodeToString(rid[:])}) }()

	qr, err := pairing.EncodeQR(pairing.QRPayload{
		RelayURL:      h.RelayURL,
		RendezvousID:  rid,
		PairingSecret: secret,
	})
	if err != nil {
		t.Fatalf("EncodeQR: %v", err)
	}

	// SCAFFOLDING FIX: hand the QR back BEFORE awaiting the SAS gate. Awaiting it here was
	// unsatisfiable by construction: the machine closes sasSeen inside Confirm, which
	// pairing.Machine.Pair reaches only after Recv(msg1) and Recv(msg3) -- both from the
	// device -- and the device cannot send either until BeginPairing(qr), which happens
	// after this function returns. Every caller therefore timed out at the gate. Returning
	// the wait as a closure preserves each assertion and each failure message verbatim.
	awaitSAS := func() [6]string {
		t.Helper()
		select {
		case <-sasSeen:
		case <-time.After(10 * time.Second):
			t.Fatalf("the machine never reached its SAS confirm gate")
		}
		mu.Lock()
		defer mu.Unlock()
		return sas
	}
	return awaitSAS, qr
}

// relayRendezvous adapts relay.Conn to pairing.RendezvousTransport. relay.Conn's
// Rendezvous* methods do not satisfy the interface directly (the names differ and Send
// carries an id the interface's Send does not, because the transport is bound to one
// rendezvous).
type relayRendezvous struct {
	conn  *relay.Conn
	label string
}

func (r *relayRendezvous) Create(ctx context.Context, id string) error {
	return r.conn.RendezvousCreate(ctx, id)
}
func (r *relayRendezvous) Claim(ctx context.Context, id string) error {
	return r.conn.RendezvousClaim(ctx, id)
}
func (r *relayRendezvous) Send(ctx context.Context, msg []byte) error {
	return r.conn.RendezvousSend(ctx, r.label, msg)
}
func (r *relayRendezvous) Recv(ctx context.Context) ([]byte, error) {
	return r.conn.RendezvousRecv(ctx)
}
func (r *relayRendezvous) Complete(ctx context.Context, id string) error {
	return r.conn.RendezvousComplete(ctx, id)
}

var _ pairing.RendezvousTransport = (*relayRendezvous)(nil)
