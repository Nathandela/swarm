package swarmmobile

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remotegw"
)

type pairingPushGateway struct {
	mu                 sync.Mutex
	allocated          int
	revoked            int
	failRevoke         bool
	failRevokes        int
	revokedAddresses   []phonecore.PushAddress
	firstRevokeFailure chan struct{}
	addr               phonecore.PushAddress
}

func (g *pairingPushGateway) serveHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/installations":
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"installation_id":"YWJjZGVmZ2hpamtsbW5vcA","refresh_before":"2030-01-01T00:00:00Z"}`))
	case r.Method == http.MethodPut && r.URL.Path == "/v1/installations/YWJjZGVmZ2hpamtsbW5vcA/token":
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/installations/YWJjZGVmZ2hpamtsbW5vcA/addresses":
		g.allocated++
		g.addr[0] = byte(g.allocated)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w,
			`{"push_address":%q,"submit_capability":%q,"machine_revoke_capability":%q,"unbound_expires_at":%q}`,
			phonecore.EncodePushAddress(g.addr),
			base64.RawURLEncoding.EncodeToString(make([]byte, 32)),
			base64.RawURLEncoding.EncodeToString(append([]byte{1}, make([]byte, 31)...)),
			time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	case r.Method == http.MethodDelete:
		g.revoked++
		raw, _ := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(r.URL.Path, "/v1/addresses/"))
		var revokedAddr phonecore.PushAddress
		copy(revokedAddr[:], raw)
		g.revokedAddresses = append(g.revokedAddresses, revokedAddr)
		if g.failRevoke || g.failRevokes > 0 {
			if g.failRevokes > 0 {
				g.failRevokes--
			}
			if g.firstRevokeFailure != nil {
				close(g.firstRevokeFailure)
				g.firstRevokeFailure = nil
			}
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "unexpected", http.StatusNotFound)
	}
}

func newPairingPushApp(t *testing.T, gateway *pairingPushGateway) (*App, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(gateway.serveHTTP))
	t.Cleanup(server.Close)
	app, err := NewApp(&Config{StateDir: t.TempDir(), PushGatewayURL: server.URL}, platformPushCustody{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })
	signer := newPlatformTestSigner(t)
	if err := app.ConfigurePushRegistration(&testPushAttestor{}, signer); err != nil {
		t.Fatal(err)
	}
	if err := app.EnsurePushRegistration("fcm-token"); err != nil {
		t.Fatal(err)
	}
	return app, server
}

func TestPreparePairingPushBinding_AllocatesFreshKeyStagesAndRevokesOnRollback(t *testing.T) {
	gateway := &pairingPushGateway{}
	app, _ := newPairingPushApp(t, gateway)

	binding, revoke, err := app.preparePairingPushBinding(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if binding == nil || revoke == nil {
		t.Fatal("negotiated registered phone did not prepare a push binding")
	}
	if len(binding.WakeKey) != 32 || len(binding.PushAddress) != 16 ||
		binding.SubmitCapability == "" || binding.MachineRevokeCapability == "" {
		t.Fatalf("incomplete binding: %+v", binding)
	}
	legacyWake := app.core.State().Keys.WakeKey
	if string(binding.WakeKey) == string(legacyWake[:]) {
		t.Fatal("pairing wake key was derived from/reused as the legacy epoch wake key")
	}
	var addr phonecore.PushAddress
	copy(addr[:], binding.PushAddress)
	if got := app.core.PendingPushBindingRevocations(); len(got) != 1 || got[0] != addr {
		t.Fatalf("staged cleanup journal=%x, want address", got)
	}

	revoke()
	gateway.mu.Lock()
	revoked := gateway.revoked
	gateway.mu.Unlock()
	if revoked != 1 {
		t.Fatalf("gateway revokes=%d, want 1", revoked)
	}
	if got := app.core.PendingPushBindingRevocations(); len(got) != 0 {
		t.Fatalf("cleanup journal after confirmed revoke=%x", got)
	}
}

func TestPreparePairingPushBinding_FailedRollbackStaysDurableAndIsDrainedBeforeNextAllocation(t *testing.T) {
	gateway := &pairingPushGateway{}
	app, _ := newPairingPushApp(t, gateway)
	binding, rollback, err := app.preparePairingPushBinding(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var addr phonecore.PushAddress
	copy(addr[:], binding.PushAddress)
	var wake crypto.WakeKey
	copy(wake[:], binding.WakeKey)

	gateway.mu.Lock()
	gateway.failRevoke = true
	gateway.mu.Unlock()
	rollback()
	if got := app.core.PendingPushBindingRevocations(); len(got) != 1 || got[0] != addr {
		t.Fatalf("failed rollback journal=%x, want address", got)
	}
	env, err := remotegw.SealWakeV1(wake, remotegw.PushAddress(addr), 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := app.core.AcceptWakeV1(env); !errors.Is(err, phonecore.ErrNoWakeKey) {
		t.Fatalf("abandoned staged key accepted a wake: %v", err)
	}
	if next, _, err := app.preparePairingPushBinding(context.Background()); err == nil || next != nil {
		t.Fatalf("new allocation passed failed cleanup: binding=%+v err=%v", next, err)
	}

	gateway.mu.Lock()
	gateway.failRevoke = false
	gateway.mu.Unlock()
	next, nextRollback, err := app.preparePairingPushBinding(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if next == nil || string(next.WakeKey) == string(binding.WakeKey) {
		t.Fatal("retry did not allocate a new address with a fresh wake key")
	}
	gateway.mu.Lock()
	allocated := gateway.allocated
	gateway.mu.Unlock()
	if allocated != 2 {
		t.Fatalf("allocations=%d, want old and replacement", allocated)
	}
	nextRollback()
}

func TestPendingPairingPushRevoke_RestartDrainsWithoutAnotherPairing(t *testing.T) {
	gateway := &pairingPushGateway{failRevoke: true}
	stateDir := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(gateway.serveHTTP))
	defer server.Close()
	signer := newPlatformTestSigner(t)
	app, err := NewApp(&Config{StateDir: stateDir, PushGatewayURL: server.URL}, platformPushCustody{})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.ConfigurePushRegistration(&testPushAttestor{}, signer); err != nil {
		t.Fatal(err)
	}
	if err := app.EnsurePushRegistration("fcm-token"); err != nil {
		t.Fatal(err)
	}
	_, rollback, err := app.preparePairingPushBinding(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rollback()
	if got := app.core.PendingPushBindingRevocations(); len(got) != 1 {
		t.Fatalf("failed rollback left %d durable obligations, want 1", len(got))
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}

	gateway.mu.Lock()
	gateway.failRevoke = false
	gateway.mu.Unlock()
	restarted, err := NewApp(&Config{StateDir: stateDir, PushGatewayURL: server.URL}, platformPushCustody{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restarted.Close() }()
	if err := restarted.ConfigurePushRegistration(&testPushAttestor{}, signer); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(restarted.core.PendingPushBindingRevocations()) != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := restarted.core.PendingPushBindingRevocations(); len(got) != 0 {
		t.Fatalf("startup did not drain the durable revoke without a new pairing: %x", got)
	}
	gateway.mu.Lock()
	allocated := gateway.allocated
	gateway.mu.Unlock()
	if allocated != 1 {
		t.Fatalf("startup cleanup allocated another address: %d", allocated)
	}
}

func TestPreparePairingPushBinding_ForegroundOnlyWithoutProductionProviders(t *testing.T) {
	gateway := &pairingPushGateway{}
	server := httptest.NewServer(http.HandlerFunc(gateway.serveHTTP))
	defer server.Close()
	app, err := NewApp(&Config{StateDir: t.TempDir(), PushGatewayURL: server.URL}, platformPushCustody{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close() }()

	binding, revoke, err := app.preparePairingPushBinding(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if binding != nil || revoke != nil {
		t.Fatalf("parked build prepared binding=%+v revoke=%v", binding, revoke != nil)
	}
	gateway.mu.Lock()
	allocated := gateway.allocated
	gateway.mu.Unlock()
	if allocated != 0 {
		t.Fatalf("parked build allocated %d addresses", allocated)
	}
}

func TestPairingPushCommit_CrashAfterPinOwnershipBeforeDispositionRecoversOwnership(t *testing.T) {
	gateway := &pairingPushGateway{}
	stateDir := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(gateway.serveHTTP))
	defer server.Close()
	app, err := NewApp(&Config{StateDir: stateDir, PushGatewayURL: server.URL}, platformPushCustody{})
	if err != nil {
		t.Fatal(err)
	}
	signer := newPlatformTestSigner(t)
	if err := app.ConfigurePushRegistration(&testPushAttestor{}, signer); err != nil {
		t.Fatal(err)
	}
	if err := app.EnsurePushRegistration("fcm-token"); err != nil {
		t.Fatal(err)
	}
	binding, rollback, err := app.preparePairingPushBinding(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var addr phonecore.PushAddress
	copy(addr[:], binding.PushAddress)
	var wake crypto.WakeKey
	copy(wake[:], binding.WakeKey)

	func() {
		defer func() { _ = recover() }()
		_ = app.pinWithStagedPushBinding(pairedOutcome("ep-same-machine", 7), &addr, func() {
			panic("simulated SIGKILL")
		})
	}()
	// RunDevice invokes the rollback arm when the acknowledgement was never sent. The
	// combined pin+ownership write has already classified this exact address as owned, so
	// that generic post-msg4 cleanup must not revoke it.
	rollback()
	_ = app.Close()

	restarted, err := NewApp(&Config{StateDir: stateDir, PushGatewayURL: server.URL}, platformPushCustody{})
	if err != nil {
		t.Fatalf("restart did not recover pin-owned staged binding: %v", err)
	}
	defer func() { _ = restarted.Close() }()
	if got := restarted.core.State(); got.Machine != "ep-same-machine" || got.EpochID != 7 {
		t.Fatalf("restart lost combined machine pin: (%q,%d)", got.Machine, got.EpochID)
	}
	if got := restarted.core.PendingPushBindingRevocations(); len(got) != 0 {
		t.Fatalf("restart left pin-owned address pending revoke: %x", got)
	}
	env, err := remotegw.SealWakeV1(wake, remotegw.PushAddress(addr), 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.core.AcceptWakeV1(env); err != nil {
		t.Fatalf("restart lost the owned wake binding: %v", err)
	}

	// The PB-PAIR-4 phone-pinned/machine-empty outcome is repaired by pairing the same
	// machine again. A fresh allocation can commit normally, and an acknowledgement loss
	// after that local commit still must not revoke the newly owned address.
	if err := restarted.ConfigurePushRegistration(&testPushAttestor{}, signer); err != nil {
		t.Fatal(err)
	}
	if err := restarted.EnsurePushRegistration("fcm-token"); err != nil {
		t.Fatal(err)
	}
	binding2, rollback2, err := restarted.preparePairingPushBinding(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var addr2 phonecore.PushAddress
	copy(addr2[:], binding2.PushAddress)
	if err := restarted.pinWithStagedPushBinding(pairedOutcome("ep-same-machine", 7), &addr2, nil); err != nil {
		t.Fatalf("same-machine repair: %v", err)
	}
	rollback2()
	deadline := time.Now().Add(2 * time.Second)
	for len(restarted.core.PendingPushBindingRevocations()) != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	gateway.mu.Lock()
	revoked := gateway.revoked
	revokedAddresses := append([]phonecore.PushAddress(nil), gateway.revokedAddresses...)
	gateway.mu.Unlock()
	if revoked != 1 || len(revokedAddresses) != 1 || revokedAddresses[0] != addr {
		t.Fatalf("same-machine repair revokes=%d addresses=%x, want old %x exactly once", revoked, revokedAddresses, addr)
	}
	if got := restarted.core.PendingPushBindingRevocations(); len(got) != 0 {
		t.Fatalf("same-process supersession cleanup remained pending: %x", got)
	}
	var wake2 crypto.WakeKey
	copy(wake2[:], binding2.WakeKey)
	env2, err := remotegw.SealWakeV1(wake2, remotegw.PushAddress(addr2), 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.core.AcceptWakeV1(env2); err != nil {
		t.Fatalf("new binding was revoked with its predecessor: %v", err)
	}
}

func TestPairingPushCommit_SupersededRevokeFailureStaysDurableAndRetries(t *testing.T) {
	gateway := &pairingPushGateway{}
	app, _ := newPairingPushApp(t, gateway)
	first, _, err := app.preparePairingPushBinding(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var oldAddr phonecore.PushAddress
	copy(oldAddr[:], first.PushAddress)
	if err := app.pinWithStagedPushBinding(pairedOutcome("ep-same-machine", 7), &oldAddr, nil); err != nil {
		t.Fatal(err)
	}
	second, _, err := app.preparePairingPushBinding(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var newAddr phonecore.PushAddress
	copy(newAddr[:], second.PushAddress)
	gateway.mu.Lock()
	gateway.failRevokes = 1
	gateway.firstRevokeFailure = make(chan struct{})
	failed := gateway.firstRevokeFailure
	gateway.mu.Unlock()
	if err := app.pinWithStagedPushBinding(pairedOutcome("ep-same-machine", 7), &newAddr, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-failed:
	case <-time.After(2 * time.Second):
		t.Fatal("superseded revoke was not attempted")
	}
	if pending := app.core.PendingPushBindingRevocations(); len(pending) != 1 || pending[0] != oldAddr {
		t.Fatalf("failed supersession revoke was not durable: %x", pending)
	}
	deadline := time.Now().Add(3 * time.Second)
	for len(app.core.PendingPushBindingRevocations()) != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if pending := app.core.PendingPushBindingRevocations(); len(pending) != 0 {
		t.Fatalf("retry did not clear superseded revoke: %x", pending)
	}
	gateway.mu.Lock()
	addresses := append([]phonecore.PushAddress(nil), gateway.revokedAddresses...)
	gateway.mu.Unlock()
	if len(addresses) != 2 || addresses[0] != oldAddr || addresses[1] != oldAddr {
		t.Fatalf("retry addresses=%x, want old address twice", addresses)
	}
}
