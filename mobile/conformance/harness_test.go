// Package conformance_test is the RUNTIME half of slice S8: the behavioural conformance
// suite for the gomobile facade. It is a test-only package (no non-test Go files), so it
// is never bindable and never ships.
//
// FAILING-FIRST (TDD RED, GG-5). Today it does not compile, because
// github.com/Nathandela/swarm/mobile does not exist. That is the correct RED for a
// package-that-must-be-created, and it is the pattern internal/remote/crypto and
// internal/remote/pairing already used in this repo ("the package has no implementation
// and `go test` fails to compile with undefined errors, which is the correct TDD RED").
// The SOURCE-level guards live one directory up, in ../, deliberately do NOT import the
// facade, and therefore run and fail individually today.
//
// WHAT "a real in-process backend" MEANS HERE, stated so nobody mistakes the boundary:
// the relay is the real relay.Server over a real WebSocket, the machine->phone sealing is
// the real remotegw.RelaySink (the same code the gateway runs), the phone->machine
// opening is the real remotegw.OpenRemoteCommandGuarded / OpenInputFrame, and the crypto
// is the frozen internal/remote/crypto. What is NOT here is the daemon behind the
// gateway: PB-NET-1 (S9) owns facade<->transport integration and PB-E2E-1 (S19) owns the
// no-fakes end-to-end with a real daemon. S8 owns the SURFACE, and a surface is proved
// against a real wire.
package conformance_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"path/filepath"
	"sync"
	"testing"
	"time"

	swarmmobile "github.com/Nathandela/swarm/mobile"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/remotegw"
)

const (
	testMachineID = "machine-endpoint-0001"
	testEpochID   = uint32(7)
)

// harness is the phone plus a real relay plus a real machine-side sealer/opener.
type harness struct {
	t   *testing.T
	ctx context.Context

	Dir string // the phone's state directory: device keys + phone-state.json
	App *swarmmobile.App
	// AppRelayURL overrides the relay the PHONE dials (the machine side always uses
	// RelayURL). A test that has to observe the phone's own wire traffic points it at a
	// proxy; empty means dial the relay directly.
	AppRelayURL string

	relaySrv     *relay.Server
	RelayURL     string
	machineRelay *relay.Client
	sink         *remotegw.RelaySink

	Keys    crypto.EpochKeys
	EpochID uint32
	Machine string

	machineSignPub      ed25519.PublicKey
	machineSignPriv     ed25519.PrivateKey
	machineRelayAuthPub ed25519.PublicKey
	senderKeyID         [8]byte
	phoneTarget         string

	mu       sync.Mutex
	recv     *crypto.MailboxReceiver // machine-side inbound seq guard
	cursor   uint64
	replySeq uint64
	granted  map[string]bool // take_control operation ids already confirmed
	leaseGen uint64          // the daemon-granted generation counter
	Commands []schema.RemoteCommand
	Inputs   []remotegw.InputFrame
}

// newHarness stands up the relay, seeds a PAIRED phone state directory, and opens the
// facade over it. Seeding the durable state (rather than running a pairing handshake) is
// exactly PB-BIND-3's "state lifecycle (restore)" path, and it is what a real second app
// launch does.
func newHarness(t *testing.T) *harness {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

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

	h := &harness{
		t: t, ctx: ctx,
		relaySrv: srv, RelayURL: srv.URL(),
		Dir: t.TempDir(), EpochID: testEpochID, Machine: testMachineID,
		recv:    crypto.NewMailboxReceiver(),
		granted: map[string]bool{},
	}

	if h.Keys, err = crypto.NewEpochKeys(); err != nil {
		t.Fatalf("epoch keys: %v", err)
	}
	if h.machineSignPub, h.machineSignPriv, err = ed25519.GenerateKey(rand.Reader); err != nil {
		t.Fatalf("machine sign key: %v", err)
	}

	machineID, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("machine identity: %v", err)
	}
	h.senderKeyID = crypto.KeyID(machineID.RecipientPublic())

	// The phone's device custody, created BEFORE the facade so the harness knows the phone's
	// relay-auth pub (phonecore.Resume opens this same directory). It is provisioned through
	// the core under the SAME cleartext sealers the shipped facade uses: an unsealed
	// device.key is no longer adopted -- a layout with no public half cannot be authenticated
	// -- so seeding one would only produce a directory NewApp refuses to open.
	provision, err := phonecore.Resume(phonecore.Config{
		Dir: h.Dir, Machine: h.Machine,
		WakeSealer:    phonecore.InsecureCleartextSealer(),
		ContentSealer: phonecore.InsecureCleartextSealer(),
	})
	if err != nil {
		t.Fatalf("phone keystore: %v", err)
	}
	ks := provision.KeyStore()
	h.phoneTarget = relay.RoutingID(ks.RelayAuthPublic())

	mPub, mPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("machine relay-auth key: %v", err)
	}
	h.machineRelayAuthPub = mPub
	if h.machineRelay, err = relay.Dial(ctx, h.RelayURL, relay.ClientAuth{
		RelayAuthPub: mPub,
		Sign:         func(c []byte) ([]byte, error) { return ed25519.Sign(mPriv, c), nil },
	}); err != nil {
		t.Fatalf("machine dial: %v", err)
	}
	t.Cleanup(func() { _ = h.machineRelay.Close() })
	if err := h.machineRelay.AuthorizeDevice(ctx, ks.RelayAuthPublic()); err != nil {
		t.Fatalf("machine authorize phone: %v", err)
	}

	h.seedState(ks, mPub)

	h.sink = remotegw.NewRelaySink(remotegw.RelayConfig{
		Appender:       h.machineRelay,
		Target:         h.phoneTarget,
		Machine:        h.Machine,
		EpochID:        h.EpochID,
		Key:            h.Keys.ContentKey,
		RecipientKeyID: crypto.KeyID(ks.RecipientPublic()),
		SenderKeyID:    h.senderKeyID,
		Authorities:    fixedAuthorities{},
	})

	h.App = h.openApp()
	return h
}

// seedState writes the durable coordinates a paired phone holds after its first launch.
//
// THE MISSING SEAM, SAID LOUDLY. phonecore.State carries the phone's OWN RoutingID, the
// machine's endpoint id, and the pinned machine Noise-static and grant-signing keys -- but
// it carries NEITHER the machine's relay routing id (where every command is appended) NOR
// the machine's relay-auth pub (which the phone must authorize to receive anything). After
// a process death the restored phone therefore knows who the machine IS and nothing about
// how to REACH it. That is the same class of defect as requirements 4.3, one field lower,
// and the facade is its first consumer. This slice does NOT add the fields (test author,
// not implementer); it fails to compile until they exist, which is the point.
func (h *harness) seedState(ks crypto.KeyStore, machineRelayAuthPub ed25519.PublicKey) {
	h.t.Helper()

	// The SAME custody the facade opens this directory with (mobile/app.go): PB-KEY-9's
	// seam is not reachable from the gomobile Config, so the shipped app is still on the
	// named cleartext sealer until S14 adds the facade verb. A different sealer here would
	// seed a blob NewApp could not open.
	store, err := phonecore.OpenStore(filepath.Join(h.Dir, phonecore.StateFileName), h.Machine,
		phonecore.InsecureCleartextSealer(), phonecore.InsecureCleartextSealer())
	if err != nil {
		h.t.Fatalf("open phone state: %v", err)
	}
	st := phonecore.State{
		Machine:             h.Machine,
		MachineSignPub:      h.machineSignPub,
		MachineRelayAuthPub: machineRelayAuthPub,
		RoutingID:           relay.RoutingID(ks.RelayAuthPublic()),
		EpochID:             h.EpochID,
		Keys:                h.Keys,
	}
	if err := store.Save(st); err != nil {
		h.t.Fatalf("seed phone state: %v", err)
	}
}

// openApp constructs the facade over the seeded state directory. It is separate from
// newHarness so a test can simulate a process death: drop the App and open a new one over
// the SAME directory.
func (h *harness) openApp() *swarmmobile.App {
	h.t.Helper()
	url := h.RelayURL
	if h.AppRelayURL != "" {
		url = h.AppRelayURL
	}
	app, err := swarmmobile.NewApp(&swarmmobile.Config{
		StateDir:  h.Dir,
		RelayURL:  url,
		MachineID: h.Machine,
	})
	if err != nil {
		h.t.Fatalf("swarmmobile.NewApp: %v", err)
	}
	h.t.Cleanup(func() { _ = app.Close() })
	if err := app.Start(); err != nil {
		h.t.Fatalf("App.Start: %v", err)
	}
	return app
}

// ---- machine -> phone --------------------------------------------------------

func (h *harness) PushRoster(recs ...schema.JournalRecord) {
	h.t.Helper()
	if err := h.sink.Snapshot(recs, uint64(len(recs))); err != nil {
		h.t.Fatalf("sink.Snapshot: %v", err)
	}
}

func (h *harness) PushEvent(rec schema.JournalRecord) {
	h.t.Helper()
	if err := h.sink.Event(rec); err != nil {
		h.t.Fatalf("sink.Event: %v", err)
	}
}

func (h *harness) PushTerminal(session string, lines []string, cols, rows int) {
	h.t.Helper()
	if err := h.sink.Terminal(session, lines, cols, rows); err != nil {
		h.t.Fatalf("sink.Terminal: %v", err)
	}
}

// PushReconcile publishes the machine's rollback authorities (PB-SYNC-7). Until one
// lands, phonecore refuses every MUTATING op with ErrUnreconciled -- so a conformance
// test that skips this step is testing the fail-closed path, not the facade.
func (h *harness) PushReconcile() {
	h.t.Helper()
	if err := h.sink.Reconcile(); err != nil {
		h.t.Fatalf("sink.Reconcile: %v", err)
	}
}

// Reply seals a daemon reply on the command-reply bucket (SenderKeyID zero, an
// independent seq space from the journal/terminal bucket -- see phonecore.Bucket).
func (h *harness) Reply(ctrl schema.Control) {
	h.t.Helper()
	h.mu.Lock()
	h.replySeq++
	seq := h.replySeq
	h.mu.Unlock()

	env, err := remotegw.SealControlReply(h.Keys.ContentKey, h.EpochID, seq, ctrl)
	if err != nil {
		h.t.Fatalf("SealControlReply: %v", err)
	}
	if _, err := h.machineRelay.MailboxAppend(h.ctx, h.phoneTarget, env); err != nil {
		h.t.Fatalf("append reply: %v", err)
	}
}

// ---- phone -> machine --------------------------------------------------------

// Drain reads the machine's mailbox and opens every frame the phone sent, through the
// REAL gateway opener and the REAL shared inbound seq guard.
//
// SCAFFOLDING FIX (implementer, S8), NO ASSERTION CHANGED. This previously tried
// OpenInputFrame and then OpenRemoteCommandGuarded on the SAME envelope. Each of those
// calls recv.Accept, so a COMMAND advanced the shared (sender, epoch) high-water on the
// first call, failed it with ErrNotInputFrame, and was then rejected by the second call
// with crypto.ErrStaleSeq -- so NO command could ever reach h.Commands and every
// AwaitCommand assertion was unreachable by construction, for any facade implementation.
// remotegw.OpenMailboxFrame exists precisely to replace that double-Accept; its own doc
// records the same defect ("that advanced the seq twice and spuriously reported
// ErrStaleSeq"). One Accept, then dispatch on the decoded plaintext's `t` discriminator.
func (h *harness) Drain() {
	h.t.Helper()
	for _, g := range h.drainOnce() {
		// The REAL gateway confirms every take_control (remotegw.CommandBridge.confirmLease
		// seals an OpLease carrying the daemon-granted generation), and PB-INPUT-2 gates the
		// phone's keystrokes on that confirmation. A fake machine that granted silently
		// would leave every input assertion in this suite exercising the refusal path.
		h.Reply(schema.Control{
			Op:          protocol.OpLease,
			EndpointID:  h.Machine,
			SessionID:   g.session,
			OperationID: g.operationID,
			Generation:  g.generation,
		})
	}
}

// leaseGrant is one take_control the machine has decided to confirm.
type leaseGrant struct {
	session     string
	operationID string
	generation  uint64
}

// drainOnce reads and opens one page, and reports the take_controls whose lease grant is
// owed. The seal happens OUTSIDE the lock, in Drain, because Reply takes it too.
func (h *harness) drainOnce() []leaseGrant {
	h.t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()

	items, err := h.machineRelay.MailboxRead(h.ctx, h.cursor)
	if err != nil {
		h.t.Fatalf("machine mailbox read: %v", err)
	}
	var grants []leaseGrant
	for _, it := range items {
		if it.Cursor > h.cursor {
			h.cursor = it.Cursor
		}
		fr, err := remotegw.OpenMailboxFrame(h.recv, h.Keys.ContentKey, it.Envelope)
		if err != nil {
			continue
		}
		if fr.Kind == remotegw.FrameInput {
			h.Inputs = append(h.Inputs, fr.Input)
			continue
		}
		h.Commands = append(h.Commands, fr.Command)
		if fr.Command.Action == schema.ActionTakeControl && !h.granted[fr.Command.OperationID] {
			h.granted[fr.Command.OperationID] = true
			h.leaseGen++
			grants = append(grants, leaseGrant{
				session:     fr.Command.Session,
				operationID: fr.Command.OperationID,
				generation:  h.leaseGen,
			})
		}
	}
	if err := h.machineRelay.MailboxAck(h.ctx, h.cursor); err != nil {
		h.t.Fatalf("machine mailbox ack: %v", err)
	}
	return grants
}

// AwaitLease drains until the machine has confirmed the session's take_control AND the
// phone has adopted the confirmation. PB-INPUT-2 refuses every keystroke until then, so a
// test that types first is exercising the refusal rather than the facade.
//
// The probe is an EMPTY SendInput: it is exactly the gate under test, and it adds no bytes
// of its own.
//
// IT IS NOT GUARANTEED TO APPEND NOTHING (review R4). An empty Type still drains whatever
// the coalescer is holding, so with buffered bytes and an elapsed window the probe emits a
// frame carrying THEM. Every call site below is buffer-empty, so today it appends nothing --
// but a future caller with input in flight would inject an append here, and the frame is the
// user's real keystrokes rather than anything this helper invented.
func (h *harness) AwaitLease(session string) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.Drain()
		if err := h.App.SendInput(session, nil); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.t.Fatalf("the lease for %q was never confirmed to the phone within 5s", session)
}

// AwaitCommand drains until a command with the given action arrives, or fails.
func (h *harness) AwaitCommand(action string) schema.RemoteCommand {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.Drain()
		h.mu.Lock()
		for _, c := range h.Commands {
			if c.Action == action {
				h.mu.Unlock()
				return c
			}
		}
		h.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	h.t.Fatalf("no %q command reached the machine within 5s", action)
	return schema.RemoteCommand{}
}

// AwaitInput drains until at least one input frame of the given kind arrives.
func (h *harness) AwaitInput(kind string) remotegw.InputFrame {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.Drain()
		h.mu.Lock()
		for _, in := range h.Inputs {
			if in.Kind == kind {
				h.mu.Unlock()
				return in
			}
		}
		h.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	h.t.Fatalf("no %q input frame reached the machine within 5s", kind)
	return remotegw.InputFrame{}
}

// eventually polls fn until it reports true, or fails with msg.
func eventually(t *testing.T, msg string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out after 5s: %s", msg)
}

// fixedAuthorities is a machine whose durable coordinates are known. The reconcile record
// exists so the phone can clear its fail-closed refusal of mutating ops; the VALUES are
// deliberately conservative zeros-plus-one so no authority can rewind anything.
type fixedAuthorities struct{}

func (fixedAuthorities) InboundHighWater() (uint64, error) { return 0, nil }
func (fixedAuthorities) ReplyCeiling() (uint64, error)     { return 0, nil }
func (fixedAuthorities) GrantWatermark() (uint32, uint64, error) {
	return testEpochID, 1, nil
}
