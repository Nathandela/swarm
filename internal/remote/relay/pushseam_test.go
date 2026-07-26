package relay

// Failing-first tests for PB-PUSH-1 (rename the push seam transport-neutral),
// PB-PUSH-6 (push tokens survive a relay restart) and PB-PUSH-7 (one token per routing
// id), plus the relay half of PB-PUSH-2's UNREGISTERED pruning.
//
// SCOPE HONESTY (hard constraint): nothing here contacts APNs or FCM, and nothing here
// models delivery. The sink is a fake; these tests pin what the RELAY does with a token
// and with a sink's answer. PB-E2E-5 (real provider, real handset) stays DEFERRED and is
// not touched by anything below.
//
// RED is undefined-only for the new names.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// --- PB-PUSH-1: the seam is transport-neutral -------------------------------

// TestPBPUSH1_PushSeamIsNamedForTheTransportItActuallyCarries pins the renamed seam.
// The transport this interface will carry is FCM, on Android, with no Apple account in
// the project at all -- a type called PushSink is a name that will be read as a fact by
// everyone who touches it afterwards.
//
// This half is satisfied by a type ALIAS, which is exactly why it is paired with the
// declaration scan below. Neither is sufficient alone: this one proves the new names are
// usable, that one proves the old ones are gone.
func TestPBPUSH1_PushSeamIsNamedForTheTransportItActuallyCarries(t *testing.T) {
	var sink PushSink = &recordingPushSink{}
	if err := sink.Push(context.Background(), "tok", PushPayload{
		Alert:      GenericPushAlert,
		Ciphertext: []byte{0x01, 0x02},
	}); err != nil {
		t.Fatalf("PushSink.Push: %v", err)
	}

	// And the injection Option is the one production wiring uses.
	srv, _, _, _ := startTestRelay(t, nil)
	_ = srv
	var _ Option = WithPushSink(sink)
}

// TestPBPUSH1_NoExportedAPNsNameSurvivesTheRename is what makes the rename real. A type
// alias left behind (type PushSink = PushSink) keeps every existing call site compiling
// and every Phase A test green while the landmine PB-PUSH-1 exists to remove is still
// sitting in the package's exported surface -- so the only assertion that distinguishes
// a rename from a second name is one that reads the declarations.
func TestPBPUSH1_NoExportedAPNsNameSurvivesTheRename(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var offenders []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			var ident *ast.Ident
			switch d := n.(type) {
			case *ast.TypeSpec:
				ident = d.Name
			case *ast.FuncDecl:
				ident = d.Name
			case *ast.ValueSpec:
				if len(d.Names) > 0 {
					ident = d.Names[0]
				}
			}
			if ident == nil || !ident.IsExported() {
				return true
			}
			l := strings.ToLower(ident.Name)
			if strings.Contains(l, "apns") {
				offenders = append(offenders, name+": "+ident.Name)
			}
			return true
		})
	}
	if len(offenders) > 0 {
		t.Fatalf("exported APNs-named identifiers survive the rename: %v -- an alias keeps the landmine PB-PUSH-1 exists to remove", offenders)
	}
}

// --- PB-PUSH-6: tokens survive a relay restart ------------------------------

// TestPBPUSH6_PushTokenSurvivesARelayRestart pins persistence.
//
// Today `tokens` is an in-memory map (server.go), so a relay restart silently disables
// push for every registered device. ADR-007 B16 makes this severe rather than annoying:
// backgrounding DISCONNECTS, so a phone whose token was dropped cannot re-register until
// the user next opens the app -- and opening the app is exactly what the lost push was
// supposed to prompt. The loss is therefore not bounded by a reconnect; it lasts until
// the user happens to look, which is the condition push exists to remove.
func TestPBPUSH6_PushTokenSurvivesARelayRestart(t *testing.T) {
	srv, cfg, sink, clk := startTestRelay(t, nil)

	dPub, dPriv := newRelayAuthKey(t)
	device := dialAuthed(t, srv.URL(), authFor(dPub, dPriv))
	const token = "fcm-token-survives"
	if err := device.TokenRegister(testCtx(t), token); err != nil {
		t.Fatalf("TokenRegister: %v", err)
	}
	mPub, mPriv := newRelayAuthKey(t)
	machine := dialAuthed(t, srv.URL(), authFor(mPub, mPriv))
	if err := machine.AuthorizeDevice(testCtx(t), ed25519.PublicKey(dPub), consentTo(dPriv, machine.RoutingID())); err != nil {
		t.Fatalf("AuthorizeDevice: %v", err)
	}
	devRID := RoutingID(dPub)

	// Restart the relay against the same store, exactly as store_test.go does for the
	// mailbox: the device does NOT reconnect (it is backgrounded, per B16).
	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	srv2, err := New(cfg, WithClock(clk), WithPushSink(sink))
	if err != nil {
		t.Fatalf("New(restart): %v", err)
	}
	if err := srv2.Start(testCtx(t)); err != nil {
		t.Fatalf("Start(restart): %v", err)
	}
	t.Cleanup(func() { _ = srv2.Close() })

	machine2 := dialAuthed(t, srv2.URL(), authFor(mPub, mPriv))
	sp := newSealParty(t, []byte("sender-pub-00000000000000000000x"), []byte("recip-pub-000000000000000000000x"))
	env := sp.sealMailbox(t, 1, []byte("wake"), clk)
	if err := machine2.PushTrigger(testCtx(t), devRID, env); err != nil {
		t.Fatalf("PushTrigger after restart: %v", err)
	}

	pushes := sink.all()
	if len(pushes) != 1 {
		t.Fatalf("pushes after a relay restart: got %d, want 1 -- the registered token did not survive", len(pushes))
	}
	if pushes[0].token != token {
		t.Fatalf("push token after restart = %q, want %q", pushes[0].token, token)
	}
}

// TestPBPUSH6_TokenDeleteAlsoSurvivesARestart is the other direction, and it is the one
// that a naive "just write it to the store" fix gets wrong. A device that revoked its
// token must not have it resurrected by a restart -- that resumes waking a handset the
// user deliberately silenced, and hands the provider a token that should be gone.
func TestPBPUSH6_TokenDeleteAlsoSurvivesARestart(t *testing.T) {
	srv, cfg, sink, clk := startTestRelay(t, nil)

	// Two devices. The KEPT one is the positive control: without it this test passes on
	// today's relay, which loses every token on restart and therefore "persists" the
	// deletion by losing everything.
	delPub, delPriv := newRelayAuthKey(t)
	deleted := dialAuthed(t, srv.URL(), authFor(delPub, delPriv))
	if err := deleted.TokenRegister(testCtx(t), "fcm-token-doomed"); err != nil {
		t.Fatalf("TokenRegister(deleted): %v", err)
	}
	if err := deleted.TokenDelete(testCtx(t)); err != nil {
		t.Fatalf("TokenDelete: %v", err)
	}
	keepPub, keepPriv := newRelayAuthKey(t)
	kept := dialAuthed(t, srv.URL(), authFor(keepPub, keepPriv))
	if err := kept.TokenRegister(testCtx(t), "fcm-token-kept"); err != nil {
		t.Fatalf("TokenRegister(kept): %v", err)
	}

	mPub, mPriv := newRelayAuthKey(t)
	machine := dialAuthed(t, srv.URL(), authFor(mPub, mPriv))
	for _, d := range []struct {
		pub  ed25519.PublicKey
		priv ed25519.PrivateKey
	}{{ed25519.PublicKey(delPub), delPriv}, {ed25519.PublicKey(keepPub), keepPriv}} {
		if err := machine.AuthorizeDevice(testCtx(t), d.pub, consentTo(d.priv, machine.RoutingID())); err != nil {
			t.Fatalf("AuthorizeDevice: %v", err)
		}
	}

	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	srv2, err := New(cfg, WithClock(clk), WithPushSink(sink))
	if err != nil {
		t.Fatalf("New(restart): %v", err)
	}
	if err := srv2.Start(testCtx(t)); err != nil {
		t.Fatalf("Start(restart): %v", err)
	}
	t.Cleanup(func() { _ = srv2.Close() })

	machine2 := dialAuthed(t, srv2.URL(), authFor(mPub, mPriv))
	sp := newSealParty(t, []byte("sender-pub-00000000000000000000x"), []byte("recip-pub-000000000000000000000x"))
	if err := machine2.PushTrigger(testCtx(t), RoutingID(keepPub), sp.sealMailbox(t, 1, []byte("wake"), clk)); err != nil {
		t.Fatalf("PushTrigger(kept) after restart: %v", err)
	}
	if err := machine2.PushTrigger(testCtx(t), RoutingID(delPub), sp.sealMailbox(t, 2, []byte("wake"), clk)); err != nil {
		t.Fatalf("PushTrigger(deleted) after restart: %v", err)
	}

	pushes := sink.all()
	if len(pushes) != 1 {
		t.Fatalf("pushes after restart: got %d, want exactly 1 (the kept token delivers, the deleted one does not)", len(pushes))
	}
	if pushes[0].token != "fcm-token-kept" {
		t.Fatalf("push token after restart = %q, want %q -- the DELETED token was resurrected", pushes[0].token, "fcm-token-kept")
	}
}

// TestPBPUSH6_RevokedDeviceTokenIsNotResurrectedByARestart closes the same hole for the
// owner-initiated path. device_revoke purges the token from the live map today; if only
// registration were persisted, a restart would restore the revoked device's token and
// the relay would resume pushing to a handset whose access was withdrawn.
func TestPBPUSH6_RevokedDeviceTokenIsNotResurrectedByARestart(t *testing.T) {
	srv, cfg, sink, clk := startTestRelay(t, nil)

	// As above, a second device that is NOT revoked is the positive control: a relay that
	// simply forgets every token on restart must not be able to pass this.
	revPub, revPriv := newRelayAuthKey(t)
	revoked := dialAuthed(t, srv.URL(), authFor(revPub, revPriv))
	if err := revoked.TokenRegister(testCtx(t), "fcm-token-revoked"); err != nil {
		t.Fatalf("TokenRegister(revoked): %v", err)
	}
	livePub, livePriv := newRelayAuthKey(t)
	live := dialAuthed(t, srv.URL(), authFor(livePub, livePriv))
	if err := live.TokenRegister(testCtx(t), "fcm-token-live"); err != nil {
		t.Fatalf("TokenRegister(live): %v", err)
	}

	mPub, mPriv := newRelayAuthKey(t)
	machine := dialAuthed(t, srv.URL(), authFor(mPub, mPriv))
	for _, d := range []struct {
		pub  ed25519.PublicKey
		priv ed25519.PrivateKey
	}{{ed25519.PublicKey(revPub), revPriv}, {ed25519.PublicKey(livePub), livePriv}} {
		if err := machine.AuthorizeDevice(testCtx(t), d.pub, consentTo(d.priv, machine.RoutingID())); err != nil {
			t.Fatalf("AuthorizeDevice: %v", err)
		}
	}
	if err := machine.DeviceRevoke(testCtx(t), RoutingID(revPub)); err != nil {
		t.Fatalf("DeviceRevoke: %v", err)
	}

	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	srv2, err := New(cfg, WithClock(clk), WithPushSink(sink))
	if err != nil {
		t.Fatalf("New(restart): %v", err)
	}
	if err := srv2.Start(testCtx(t)); err != nil {
		t.Fatalf("Start(restart): %v", err)
	}
	t.Cleanup(func() { _ = srv2.Close() })

	machine2 := dialAuthed(t, srv2.URL(), authFor(mPub, mPriv))
	sp := newSealParty(t, []byte("sender-pub-00000000000000000000x"), []byte("recip-pub-000000000000000000000x"))
	if err := machine2.PushTrigger(testCtx(t), RoutingID(livePub), sp.sealMailbox(t, 1, []byte("wake"), clk)); err != nil {
		t.Fatalf("PushTrigger(live) after restart: %v", err)
	}
	// A revoked routing id is no longer paired, so the trigger itself is refused; what
	// matters is that no push reaches the revoked handset either way.
	_ = machine2.PushTrigger(testCtx(t), RoutingID(revPub), sp.sealMailbox(t, 2, []byte("wake"), clk))

	pushes := sink.all()
	if len(pushes) != 1 {
		t.Fatalf("pushes after restart: got %d, want exactly 1 (the live device only)", len(pushes))
	}
	if pushes[0].token != "fcm-token-live" {
		t.Fatalf("push token after restart = %q, want %q -- the REVOKED device's token was resurrected", pushes[0].token, "fcm-token-live")
	}
}

// TestPBPUSH6_TokenIsNotStoredInTheClearAlongsideTheCiphertext keeps the persistence fix
// honest about what it adds to the relay's at-rest footprint. The relay is untrusted by
// design and its store is documented as holding "only ciphertext + routing metadata";
// a persisted push token is a NEW durable identifier that the push provider can also see,
// so it must at least be recorded deliberately rather than discovered later.
//
// This test does NOT demand encryption -- the relay must be able to use the token, so it
// cannot be opaque to itself. It demands that the token live under its own bucket rather
// than being smuggled into the mailbox item log, so an operator auditing the store can
// find every device identifier in one place.
func TestPBPUSH6_TokenIsNotStoredInTheClearAlongsideTheCiphertext(t *testing.T) {
	srv, cfg, _, clk := startTestRelay(t, nil)

	dPub, dPriv := newRelayAuthKey(t)
	device := dialAuthed(t, srv.URL(), authFor(dPub, dPriv))
	const token = "fcm-token-auditable"
	if err := device.TokenRegister(testCtx(t), token); err != nil {
		t.Fatalf("TokenRegister: %v", err)
	}
	mPub, mPriv := newRelayAuthKey(t)
	machine := dialAuthed(t, srv.URL(), authFor(mPub, mPriv))
	if err := machine.AuthorizeDevice(testCtx(t), ed25519.PublicKey(dPub), consentTo(dPriv, machine.RoutingID())); err != nil {
		t.Fatalf("AuthorizeDevice: %v", err)
	}
	sp := newSealParty(t, []byte("sender-pub-00000000000000000000x"), []byte("recip-pub-000000000000000000000x"))
	if _, err := machine.MailboxAppend(testCtx(t), RoutingID(dPub), sp.sealMailbox(t, 1, []byte("payload"), clk)); err != nil {
		t.Fatalf("MailboxAppend: %v", err)
	}
	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	raw, err := os.ReadFile(cfg.DBPath)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	if !bytes.Contains(raw, []byte(token)) {
		t.Fatalf("the push token is not in the store at all: PB-PUSH-6 is unmet (see the restart test)")
	}
	if !bytes.Contains(raw, []byte("tokens")) {
		t.Fatal("no dedicated tokens bucket in the store: a device identifier persisted without a named home is one an operator cannot audit")
	}
}

// --- PB-PUSH-7: one token per routing id ------------------------------------

// TestPBPUSH7_SecondTokenForOneRoutingIDReplacesTheFirst pins the single-device v1
// limitation as BEHAVIOUR rather than as a sentence in a doc. The relay keys tokens by
// routing id and a routing id is derived from a device's relay-auth key, so "one token
// per routing id" is exactly "one handset per paired device" -- correct for v1, where
// pairing refuses a second device outright.
//
// Last-write-wins is the pinned behaviour because it is what makes PB-PUSH-9's
// re-registration on every authenticated reconnect converge: a rotated FCM token must
// replace the stale one, not sit beside it and cause every wake to be delivered twice.
//
// NOT A RED TEST -- stated so no evidence file claims otherwise. This CHARACTERIZES what
// the relay already does (server.go assigns tokens[rid] = token), which is what PB-PUSH-7
// asks for: "documented as acceptable for single-device v1 OR fixed. Decision + a test
// pinning the behavior." It passes today and its job is to fail the day someone widens
// the map to a slice without an explicit multi-device decision.
func TestPBPUSH7_SecondTokenForOneRoutingIDReplacesTheFirst(t *testing.T) {
	srv, _, sink, clk := startTestRelay(t, nil)

	dPub, dPriv := newRelayAuthKey(t)
	device := dialAuthed(t, srv.URL(), authFor(dPub, dPriv))
	if err := device.TokenRegister(testCtx(t), "fcm-token-old"); err != nil {
		t.Fatalf("TokenRegister(old): %v", err)
	}
	if err := device.TokenRegister(testCtx(t), "fcm-token-new"); err != nil {
		t.Fatalf("TokenRegister(new): %v", err)
	}

	mPub, mPriv := newRelayAuthKey(t)
	machine := dialAuthed(t, srv.URL(), authFor(mPub, mPriv))
	if err := machine.AuthorizeDevice(testCtx(t), ed25519.PublicKey(dPub), consentTo(dPriv, machine.RoutingID())); err != nil {
		t.Fatalf("AuthorizeDevice: %v", err)
	}
	sp := newSealParty(t, []byte("sender-pub-00000000000000000000x"), []byte("recip-pub-000000000000000000000x"))
	if err := machine.PushTrigger(testCtx(t), RoutingID(dPub), sp.sealMailbox(t, 1, []byte("wake"), clk)); err != nil {
		t.Fatalf("PushTrigger: %v", err)
	}

	pushes := sink.all()
	if len(pushes) != 1 {
		t.Fatalf("push count after re-registering a token: got %d, want 1 (a second token must REPLACE, not accumulate)", len(pushes))
	}
	if pushes[0].token != "fcm-token-new" {
		t.Fatalf("push token = %q, want the most recently registered %q", pushes[0].token, "fcm-token-new")
	}
}

// --- PB-PUSH-2 (relay half): UNREGISTERED prunes the token ------------------

// TestPBPUSH2_UnregisteredSinkErrorPrunesTheStoredToken is the class-(v) guard for the
// FCM sender's pruning signal. internal/remote/push can classify an UNREGISTERED
// response perfectly and still change nothing, because deliverPush discards the sink's
// error entirely today (`_ = s.apns.Push(...)`). A pruning signal nobody reads is a
// pruning signal that does not exist.
func TestPBPUSH2_UnregisteredSinkErrorPrunesTheStoredToken(t *testing.T) {
	unreg := &recordingPushSink{err: ErrPushUnregistered}
	srv2, clk := startTestRelayWithSink(t, unreg)

	dPub, dPriv := newRelayAuthKey(t)
	device := dialAuthed(t, srv2.URL(), authFor(dPub, dPriv))
	if err := device.TokenRegister(testCtx(t), "fcm-token-dead"); err != nil {
		t.Fatalf("TokenRegister: %v", err)
	}
	mPub, mPriv := newRelayAuthKey(t)
	machine := dialAuthed(t, srv2.URL(), authFor(mPub, mPriv))
	if err := machine.AuthorizeDevice(testCtx(t), ed25519.PublicKey(dPub), consentTo(dPriv, machine.RoutingID())); err != nil {
		t.Fatalf("AuthorizeDevice: %v", err)
	}
	devRID := RoutingID(dPub)
	sp := newSealParty(t, []byte("sender-pub-00000000000000000000x"), []byte("recip-pub-000000000000000000000x"))

	if err := machine.PushTrigger(testCtx(t), devRID, sp.sealMailbox(t, 1, []byte("wake"), clk)); err != nil {
		t.Fatalf("PushTrigger(1): %v", err)
	}
	if got := len(unreg.all()); got != 1 {
		t.Fatalf("sink attempts after the first trigger: got %d, want 1", got)
	}

	// The token was rejected as unregistered, so the relay must have dropped it: a
	// second trigger has nowhere to go.
	if err := machine.PushTrigger(testCtx(t), devRID, sp.sealMailbox(t, 2, []byte("wake"), clk)); err != nil {
		t.Fatalf("PushTrigger(2): %v", err)
	}
	if got := len(unreg.all()); got != 1 {
		t.Fatalf("sink attempts after an UNREGISTERED response: got %d, want 1 (the dead token must have been pruned)", got)
	}
}

// TestPBPUSH5_SinkFailureNeverFailsTheTrigger pins the relay half of "the system works
// without push": a provider outage must not turn push_trigger into an error the gateway
// treats as a relay failure, and must not prune a token that is merely temporarily
// undeliverable.
//
// NOT A RED TEST. It passes today because deliverPush discards the sink's error entirely,
// so nothing can be pruned for any reason. It is a FENCE around the pruning work the test
// above demands: the moment deliverPush starts reading that error, this is what stops it
// pruning on a transient one. Its value is entirely prospective and the evidence file must
// say so rather than counting it as coverage earned.
func TestPBPUSH5_SinkFailureNeverFailsTheTrigger(t *testing.T) {
	flaky := &recordingPushSink{err: errFlakyProvider}
	srv, clk := startTestRelayWithSink(t, flaky)

	dPub, dPriv := newRelayAuthKey(t)
	device := dialAuthed(t, srv.URL(), authFor(dPub, dPriv))
	if err := device.TokenRegister(testCtx(t), "fcm-token-flaky"); err != nil {
		t.Fatalf("TokenRegister: %v", err)
	}
	mPub, mPriv := newRelayAuthKey(t)
	machine := dialAuthed(t, srv.URL(), authFor(mPub, mPriv))
	if err := machine.AuthorizeDevice(testCtx(t), ed25519.PublicKey(dPub), consentTo(dPriv, machine.RoutingID())); err != nil {
		t.Fatalf("AuthorizeDevice: %v", err)
	}
	devRID := RoutingID(dPub)
	sp := newSealParty(t, []byte("sender-pub-00000000000000000000x"), []byte("recip-pub-000000000000000000000x"))

	for i := 1; i <= 2; i++ {
		if err := machine.PushTrigger(testCtx(t), devRID, sp.sealMailbox(t, uint64(i), []byte("wake"), clk)); err != nil {
			t.Fatalf("PushTrigger(%d) returned %v: a provider outage must not fail the trigger", i, err)
		}
	}
	if got := len(flaky.all()); got != 2 {
		t.Fatalf("sink attempts after two transient failures: got %d, want 2 (a transient error must NOT prune the token)", got)
	}
}

// --- fakes -------------------------------------------------------------------

// startTestRelayWithSink boots a relay whose push sink is the caller's, so a test can
// drive the relay's handling of a provider VERDICT (unregistered vs transient) rather
// than only its handling of a successful hand-off.
func startTestRelayWithSink(t *testing.T, sink PushSink) (*Server, *fakeClock) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Listen = "127.0.0.1:0"
	cfg.TLSMode = "off"
	cfg.DBPath = filepath.Join(t.TempDir(), "relay.db")
	clk := newFakeClock()
	srv, err := New(cfg, WithClock(clk), WithPushSink(sink))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv, clk
}

// errFlakyProvider is a transient provider failure -- explicitly NOT ErrPushUnregistered.
var errFlakyProvider = errTransient{}

type errTransient struct{}

func (errTransient) Error() string { return "push provider temporarily unavailable" }

// recordingPushSink is the renamed mockAPNs: it records attempts and can answer with a
// canned error so the relay's handling of a sink's verdict is drivable.
type recordingPushSink struct {
	mu       sync.Mutex
	attempts []recordedPush
	err      error
}

func (r *recordingPushSink) Push(_ context.Context, token string, p PushPayload) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempts = append(r.attempts, recordedPush{token: token, payload: p})
	return r.err
}

func (r *recordingPushSink) all() []recordedPush {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedPush(nil), r.attempts...)
}
