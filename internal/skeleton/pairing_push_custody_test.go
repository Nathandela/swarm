package skeleton

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remote/grant"
)

type machinePushRoundTripFunc func(*http.Request) (*http.Response, error)

func (f machinePushRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func machinePushRecord(gatewayURL string, addressByte, submitByte, revokeByte byte) device.PushBinding {
	fill := func(b byte, n int) []byte {
		out := make([]byte, n)
		for i := range out {
			out[i] = b
		}
		return out
	}
	return device.PushBinding{
		GatewayURL:              gatewayURL,
		Address:                 fill(addressByte, 16),
		SubmitCapability:        base64.RawURLEncoding.EncodeToString(fill(submitByte, 32)),
		MachineRevokeCapability: base64.RawURLEncoding.EncodeToString(fill(revokeByte, 32)),
		WakeKey:                 fill(addressByte+1, 32),
		CapabilityRecordVersion: schema.CurrentCapabilityRecordVersion,
		Transport:               device.PushTransportGateway,
	}
}

func TestMachinePushCustody_CrashAfterAcceptanceBeforeRegistryRevokesOnRestart(t *testing.T) {
	var gotPath, gotAuth, gotMethod string
	var gotBody []byte
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth, gotMethod = r.URL.Path, r.Header.Get("Authorization"), r.Method
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	stateDir := t.TempDir()
	push := machinePushRecord(server.URL, 0x31, 0x41, 0x51)
	store, err := openMachinePushCustody(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Stage("device-before-registry", push); err != nil {
		t.Fatal(err)
	}

	// A process death loses memory but not custody. There is no registry row: this is the
	// exact post-ACK/pre-AddSole (and AddSole/grant rollback) restart shape.
	restarted, err := openMachinePushCustody(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := device.Open(filepath.Join(stateDir, "devices"))
	if err != nil {
		t.Fatal(err)
	}
	if err := reconcileMachinePushCustody(context.Background(), restarted, registry, server.Client()); err != nil {
		t.Fatal(err)
	}
	if _, ok := restarted.Pending(); ok {
		t.Fatal("confirmed revoke left machine custody pending")
	}
	if !strings.HasSuffix(gotPath, base64.RawURLEncoding.EncodeToString(push.Address)) {
		t.Fatalf("DELETE path = %q, want exact staged address", gotPath)
	}
	if gotMethod != http.MethodDelete {
		t.Fatalf("revoke method = %q, want DELETE", gotMethod)
	}
	if gotAuth != "Swarm-Revoke "+push.MachineRevokeCapability {
		t.Fatalf("Authorization = %q, want exact staged revoke capability", gotAuth)
	}
	if len(gotBody) != 0 {
		t.Fatalf("revoke body = %q, want empty", gotBody)
	}
}

func TestMachinePushCustody_HTTPFailuresRetainAuthorityAcrossRestart(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.WriteHeader(status)
			}))
			defer server.Close()

			stateDir := t.TempDir()
			push := machinePushRecord(server.URL, byte(status), 0x42, 0x52)
			store, err := openMachinePushCustody(stateDir)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Stage("pending-device", push); err != nil {
				t.Fatal(err)
			}
			if err := reconcileMachinePushCustody(context.Background(), store, nil, server.Client()); err == nil {
				t.Fatalf("gateway %d cleared custody", status)
			}
			if calls != 1 {
				t.Fatalf("gateway %d saw %d requests, want 1", status, calls)
			}

			restarted, err := openMachinePushCustody(stateDir)
			if err != nil {
				t.Fatal(err)
			}
			got, ok := restarted.Pending()
			if !ok || !samePushBinding(got.Push, push) {
				t.Fatalf("gateway %d lost or changed revoke authority after restart: %+v, %v", status, got, ok)
			}
		})
	}
}

func TestMachinePushCustody_CrashAfterRegistryAndGrantTreatsMatchingRecordAsOwned(t *testing.T) {
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	stateDir := t.TempDir()
	push := machinePushRecord(server.URL, 0x32, 0x42, 0x52)
	rec := validDeviceRecord(t)
	rec.Push = &push
	store, err := openMachinePushCustody(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Stage(rec.DeviceID, push); err != nil {
		t.Fatal(err)
	}
	registry, err := device.Open(filepath.Join(stateDir, "devices"))
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.AddSole(rec); err != nil {
		t.Fatal(err)
	}
	if err := grant.Save(filepath.Join(stateDir, "devices"), rec.DeviceID, &crypto.EpochGrant{}); err != nil {
		t.Fatal(err)
	}

	if err := reconcileMachinePushCustody(context.Background(), store, registry, server.Client()); err != nil {
		t.Fatal(err)
	}
	if requests != 0 {
		t.Fatalf("owned push authority was revoked after commit: %d request(s)", requests)
	}
	if _, ok := store.Pending(); ok {
		t.Fatal("owned stage was not cleared")
	}
}

func TestMachinePushCustody_CrashAfterAddSoleBeforeGrantRevokesInsteadOfClaimingOwned(t *testing.T) {
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	stateDir := t.TempDir()
	push := machinePushRecord(server.URL, 0x36, 0x46, 0x56)
	rec := validDeviceRecord(t)
	rec.Push = &push
	store, err := openMachinePushCustody(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Stage(rec.DeviceID, push); err != nil {
		t.Fatal(err)
	}
	registry, err := device.Open(filepath.Join(stateDir, "devices"))
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.AddSole(rec); err != nil {
		t.Fatal(err)
	}
	// No grant sidecar: this is the exact SIGKILL after AddSole and before grant.Save.
	if err := reconcileMachinePushCustody(context.Background(), store, registry, server.Client()); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("AddSole-only crash made %d revoke request(s), want 1", requests)
	}
	if _, ok := store.Pending(); ok {
		t.Fatal("confirmed AddSole-only cleanup remained pending")
	}
}

func TestMachinePushCustody_AddSoleRefusalRetainsExistingDeviceAndRevokesCandidate(t *testing.T) {
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	stateDir := t.TempDir()
	store, err := openMachinePushCustody(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	candidate := machinePushRecord(server.URL, 0x37, 0x47, 0x57)
	if err := store.Stage("candidate-device", candidate); err != nil {
		t.Fatal(err)
	}
	registry, err := device.Open(filepath.Join(stateDir, "devices"))
	if err != nil {
		t.Fatal(err)
	}
	existing := validDeviceRecord(t)
	if err := registry.AddSole(existing); err != nil {
		t.Fatal(err)
	}
	if err := reconcileMachinePushCustody(context.Background(), store, registry, server.Client()); err != nil {
		t.Fatal(err)
	}
	if requests != 1 || registry.Count() != 1 {
		t.Fatalf("AddSole-refused candidate cleanup: requests=%d registry=%d", requests, registry.Count())
	}
}

func TestMachinePushCustody_RefusesToOverwriteDifferentLiveAuthority(t *testing.T) {
	store, err := openMachinePushCustody(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	one := machinePushRecord("https://push-swarm.dsfactory.org", 0x33, 0x43, 0x53)
	two := machinePushRecord("https://push-swarm.dsfactory.org", 0x34, 0x44, 0x54)
	if err := store.Stage("device-one", one); err != nil {
		t.Fatal(err)
	}
	if err := store.Stage("device-one", one); err != nil {
		t.Fatalf("exact idempotent restage: %v", err)
	}
	if err := store.Stage("device-two", two); err == nil {
		t.Fatal("different authority overwrote unresolved revoke custody")
	}
	got, ok := store.Pending()
	if !ok || got.DeviceID != "device-one" || !samePushBinding(got.Push, one) {
		t.Fatalf("pending custody changed after refused overwrite: %+v, %v", got, ok)
	}
}

func TestMachinePushCustody_DelayedClearCannotEraseNewerStage(t *testing.T) {
	store, err := openMachinePushCustody(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	one := machinePushCustodyRecord{DeviceID: "device-one", Push: machinePushRecord("https://push-swarm.dsfactory.org", 0x39, 0x49, 0x59)}
	two := machinePushCustodyRecord{DeviceID: "device-two", Push: machinePushRecord("https://push-swarm.dsfactory.org", 0x3a, 0x4a, 0x5a)}
	if err := store.Stage(one.DeviceID, one.Push); err != nil {
		t.Fatal(err)
	}
	if err := store.ClearExact(one); err != nil {
		t.Fatal(err)
	}
	if err := store.Stage(two.DeviceID, two.Push); err != nil {
		t.Fatal(err)
	}
	if err := store.ClearExact(one); err == nil {
		t.Fatal("delayed cleanup erased a newer staged obligation")
	}
	if got, ok := store.Pending(); !ok || !sameMachinePushCustodyRecord(got, two) {
		t.Fatalf("new stage after delayed clear = %+v, %v", got, ok)
	}
}

func TestMachinePushCustody_PostRenameSyncErrorsAdoptCommittedStageAndClear(t *testing.T) {
	stateDir := t.TempDir()
	store, err := openMachinePushCustody(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	rec := machinePushCustodyRecord{DeviceID: "device", Push: machinePushRecord("https://push-swarm.dsfactory.org", 0x3b, 0x4b, 0x5b)}
	previous := syncMachinePushCustodyDir
	defer func() { syncMachinePushCustodyDir = previous }()
	syncMachinePushCustodyDir = func(string) error { return errors.New("injected post-rename sync") }
	if err := store.Stage(rec.DeviceID, rec.Push); err == nil {
		t.Fatal("stage returned nil after injected post-rename sync failure")
	}
	if got, ok := store.Pending(); !ok || !sameMachinePushCustodyRecord(got, rec) {
		t.Fatalf("memory rolled back renamed stage: %+v, %v", got, ok)
	}
	syncMachinePushCustodyDir = previous
	restarted, err := openMachinePushCustody(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := restarted.Pending(); !ok || !sameMachinePushCustodyRecord(got, rec) {
		t.Fatalf("restart lost renamed stage: %+v, %v", got, ok)
	}
	syncMachinePushCustodyDir = func(string) error { return errors.New("injected post-rename sync") }
	if err := restarted.ClearExact(rec); err == nil {
		t.Fatal("clear returned nil after injected post-rename sync failure")
	}
	if _, ok := restarted.Pending(); ok {
		t.Fatal("memory resurrected renamed clear")
	}
	syncMachinePushCustodyDir = previous
	again, err := openMachinePushCustody(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := again.Pending(); ok {
		t.Fatal("restart resurrected renamed clear")
	}
}

func TestRevokeDevice_RegistryOnlyPushStagesBeforeDeletionAndPresentsExactRevoke(t *testing.T) {
	var gotAuth string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	stateDir := t.TempDir()
	registry, err := device.Open(filepath.Join(stateDir, "devices"))
	if err != nil {
		t.Fatal(err)
	}
	rec := validDeviceRecord(t)
	push := machinePushRecord(server.URL, 0x35, 0x45, 0x55)
	rec.Push = &push
	if err := registry.AddSole(rec); err != nil {
		t.Fatal(err)
	}
	custody, err := openMachinePushCustody(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	api := &coreAPI{stateDir: stateDir, devices: registry, pushRevokeCustody: custody, pushHTTPClient: server.Client()}
	removed, err := api.RevokeDevice(rec.DeviceID)
	if err != nil || !removed {
		t.Fatalf("RevokeDevice = %v, %v", removed, err)
	}
	if registry.Count() != 0 {
		t.Fatal("registry row survived explicit revoke")
	}
	if gotAuth != "Swarm-Revoke "+push.MachineRevokeCapability {
		t.Fatalf("Authorization = %q, want registry-conveyed revoke capability", gotAuth)
	}
	if _, ok := custody.Pending(); ok {
		t.Fatal("successful explicit revoke left custody pending")
	}
}

func TestRevokeDevice_OfflineAfterRegistryDeletionRedrivesSelfContainedCustodyOnRestart(t *testing.T) {
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	stateDir := t.TempDir()
	registry, err := device.Open(filepath.Join(stateDir, "devices"))
	if err != nil {
		t.Fatal(err)
	}
	rec := validDeviceRecord(t)
	push := machinePushRecord(server.URL, 0x38, 0x48, 0x58)
	rec.Push = &push
	if err := registry.AddSole(rec); err != nil {
		t.Fatal(err)
	}
	custody, err := openMachinePushCustody(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	offline := &http.Client{Transport: machinePushRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("gateway offline")
	})}
	api := &coreAPI{stateDir: stateDir, devices: registry, pushRevokeCustody: custody, pushHTTPClient: offline}
	removed, err := api.RevokeDevice(rec.DeviceID)
	if !removed || err == nil {
		t.Fatalf("offline revoke = (%v,%v), want committed local removal plus retryable error", removed, err)
	}
	if _, ok := custody.Pending(); !ok {
		t.Fatal("offline cleanup lost exact revoke custody")
	}
	restarted, err := openMachinePushCustody(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconcileMachinePushCustody(context.Background(), restarted, registry, server.Client()); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("restart presented %d revoke request(s), want 1", requests)
	}
	if _, ok := restarted.Pending(); ok {
		t.Fatal("confirmed restart revoke remained pending")
	}
}

// A gateway 5xx is a failed delivery, never permission to discard the self-contained
// revoke authority. The retry must use the exact binding staged before epoch rotation.
func TestRevokeDevice_Gateway5xxRetainsExactCustodyAcrossEpochRotation(t *testing.T) {
	type request struct {
		method, path, auth string
		body               []byte
	}
	var mu sync.Mutex
	status := http.StatusServiceUnavailable
	var requests []request
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read revoke body: %v", err)
		}
		mu.Lock()
		requests = append(requests, request{r.Method, r.URL.Path, r.Header.Get("Authorization"), body})
		code := status
		mu.Unlock()
		w.WriteHeader(code)
	}))
	defer server.Close()

	stateDir := t.TempDir()
	before := writeTestIdentity(t, stateDir, "push-custody-retry")
	registry, err := device.Open(filepath.Join(stateDir, "devices"))
	if err != nil {
		t.Fatal(err)
	}
	rec := validDeviceRecord(t)
	push := machinePushRecord(server.URL, 0x3C, 0x4C, 0x5C)
	rec.Push = &push
	if err := registry.AddSole(rec); err != nil {
		t.Fatal(err)
	}
	custody, err := openMachinePushCustody(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	api := &coreAPI{stateDir: stateDir, devices: registry, pushRevokeCustody: custody, pushHTTPClient: server.Client()}
	removed, err := api.RevokeDevice(rec.DeviceID)
	if !removed || err == nil {
		t.Fatalf("5xx revoke = (%v,%v), want committed local removal plus retryable error", removed, err)
	}
	if api.pairing == nil || api.pairing.EpochID <= before.EpochID() {
		t.Fatalf("revoke did not rotate the epoch before preserving retry custody")
	}
	if _, ok := custody.Pending(); !ok {
		t.Fatal("5xx cleanup discarded the exact revoke custody")
	}

	mu.Lock()
	if len(requests) != 1 {
		mu.Unlock()
		t.Fatalf("5xx revoke made %d requests, want 1", len(requests))
	}
	first := requests[0]
	status = http.StatusNoContent
	mu.Unlock()

	restarted, err := openMachinePushCustody(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconcileMachinePushCustody(context.Background(), restarted, registry, server.Client()); err != nil {
		t.Fatal(err)
	}
	if _, ok := restarted.Pending(); ok {
		t.Fatal("confirmed 204 retry remained pending")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("retry made %d requests, want 2", len(requests))
	}
	second := requests[1]
	if first.method != second.method || first.path != second.path || first.auth != second.auth || !bytes.Equal(first.body, second.body) {
		t.Fatalf("retry changed staged revoke authority: first=%+v second=%+v", first, second)
	}
	if first.method != http.MethodDelete || len(first.body) != 0 {
		t.Fatalf("current revoke wire = method %q body %q, want bodyless DELETE", first.method, first.body)
	}
	if want := "/v1/addresses/" + base64.RawURLEncoding.EncodeToString(push.Address); first.path != want {
		t.Fatalf("revoke path = %q, want %q", first.path, want)
	}
	if want := "Swarm-Revoke " + push.MachineRevokeCapability; first.auth != want {
		t.Fatalf("revoke authorization = %q, want %q", first.auth, want)
	}
}

func TestRevokeDevice_PreCommitPushCustodyStageFailureKeepsRegistryAndEpoch(t *testing.T) {
	stateDir := t.TempDir()
	before := writeTestIdentity(t, stateDir, "push-custody-stage-failure")
	registry, err := device.Open(filepath.Join(stateDir, "devices"))
	if err != nil {
		t.Fatal(err)
	}
	rec := validDeviceRecord(t)
	push := machinePushRecord("https://push.example", 0x3D, 0x4D, 0x5D)
	rec.Push = &push
	if err := registry.AddSole(rec); err != nil {
		t.Fatal(err)
	}
	custody, err := openMachinePushCustody(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	// Force persistLocked to fail before it can create its temporary file: machine.key is
	// a regular file, not a custody directory. This exercises RevokeDevice's pre-commit
	// Stage failure path rather than a post-rename durability ambiguity.
	custody.path = filepath.Join(stateDir, "remote", remoteIdentityFile, "revoke.json")
	api := &coreAPI{stateDir: stateDir, devices: registry, pushRevokeCustody: custody}
	removed, err := api.RevokeDevice(rec.DeviceID)
	if removed || err == nil {
		t.Fatalf("stage failure revoke = (%v,%v), want retained registry and error", removed, err)
	}
	if got, ok := registry.Get(rec.DeviceID); !ok || got.Push == nil || !samePushBinding(*got.Push, push) {
		t.Fatalf("stage failure changed registry authority: %+v, %v", got, ok)
	}
	after, err := loadPairingConfig(stateDir)
	if err != nil || after == nil {
		t.Fatalf("load identity after stage failure: cfg=%v err=%v", after, err)
	}
	if after.EpochID != before.EpochID() {
		t.Fatalf("stage failure rotated epoch from %d to %d", before.EpochID(), after.EpochID)
	}
	if _, ok := custody.Pending(); ok {
		t.Fatal("pre-commit stage failure left partial revoke custody")
	}
}
