package swarmmobile

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remotegw"
)

type pairingPushGateway struct {
	mu         sync.Mutex
	allocated  int
	revoked    int
	failRevoke bool
	addr       phonecore.PushAddress
}

func (g *pairingPushGateway) serveHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/installations":
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"installation_id":"abcdefghijklmnopqrstuv","refresh_before":"2030-01-01T00:00:00Z"}`))
	case r.Method == http.MethodPost && r.URL.Path == "/v1/installations/abcdefghijklmnopqrstuv/addresses":
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
		if g.failRevoke {
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

func TestPreparePairingPushBinding_ForegroundOnlyWithoutProductionProviders(t *testing.T) {
	gateway := &pairingPushGateway{}
	server := httptest.NewServer(http.HandlerFunc(gateway.serveHTTP))
	defer server.Close()
	app, err := NewApp(&Config{StateDir: t.TempDir(), PushGatewayURL: server.URL}, platformPushCustody{})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

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
	if err := app.ConfigurePushRegistration(&testPushAttestor{}, newPlatformTestSigner(t)); err != nil {
		t.Fatal(err)
	}
	if err := app.EnsurePushRegistration("fcm-token"); err != nil {
		t.Fatal(err)
	}
	binding, _, err := app.preparePairingPushBinding(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var addr phonecore.PushAddress
	copy(addr[:], binding.PushAddress)
	var wake crypto.WakeKey
	copy(wake[:], binding.WakeKey)

	func() {
		defer func() { _ = recover() }()
		_ = app.ownStagedPushBindingAfterPin(addr, func() { panic("simulated SIGKILL") })
	}()
	_ = app.Close()

	restarted, err := NewApp(&Config{StateDir: stateDir, PushGatewayURL: server.URL}, platformPushCustody{})
	if err != nil {
		t.Fatalf("restart did not recover pin-owned staged binding: %v", err)
	}
	defer restarted.Close()
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
}
