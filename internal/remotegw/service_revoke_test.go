// FAILING-FIRST (TDD RED, GG-5) test for the SECURITY-CRITICAL post-revocation
// confidentiality property (codex#1 / opus#2). The running gateway loads its epoch
// ContentKey + PhoneTarget ONCE at startup and has no reload path; its journal loop
// reconnects forever. So after the owner revokes the paired device (which rotates the
// epoch key and severs the gateway's journal subscription) the gateway would RECONNECT
// and resume sealing epoch frames to the revoked device's mailbox under the STALE key.
//
// The fix: on each journal reconnect the runtime re-reads <StateDir>/devices; if its
// paired DeviceID is gone it tears the whole Service down (Run returns ErrDeviceRevoked)
// during the deviceless window, BEFORE any re-pair -- instead of reconnecting-and-
// resealing. A transient disconnect (device still present) must still reconnect as today.
package remotegw

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/device"
)

// newPairedRegistry provisions <stateDir>/devices with exactly one valid device (as the
// daemon does at enroll) and returns its device id.
func newPairedRegistry(t *testing.T, stateDir string) string {
	t.Helper()
	reg, err := device.Open(filepath.Join(stateDir, "devices"))
	if err != nil {
		t.Fatalf("device.Open: %v", err)
	}
	cmdPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	rec := device.Record{
		DeviceID:       device.DeviceIDFor(cmdPub),
		Name:           "phone",
		NoiseStaticPub: randKey(t),
		RelayAuthPub:   randKey(t),
		CommandSignPub: cmdPub,
		RecipientPub:   randKey(t),
		Capability:     device.CapFull,
		PairedAt:       time.Now(),
		GrantedEpoch:   1,
	}
	if err := reg.Add(rec); err != nil {
		t.Fatalf("registry.Add: %v", err)
	}
	return rec.DeviceID
}

func randKey(t *testing.T) []byte {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return b
}

// TestService_RunExitsWhenDeviceRevoked pins the post-revocation exit: with the paired
// device present the reconnect loop keeps reconnecting (transient disconnect, unchanged);
// once the device is removed from the on-disk registry (the revoke path) the next
// reconnect observes it gone and Run returns ErrDeviceRevoked instead of resealing.
func TestService_RunExitsWhenDeviceRevoked(t *testing.T) {
	stateDir := t.TempDir()
	deviceID := newPairedRegistry(t, stateDir)

	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 2)
	}
	svc := NewService(ServiceConfig{
		DaemonSocket:   "/nonexistent/remote.sock", // RunJournal fails fast -> reconnect loop spins
		Relay:          &scriptedMailbox{},
		PhoneTarget:    "phone",
		Key:            key,
		EpochID:        1,
		StateDir:       stateDir,
		DeviceID:       deviceID,
		PollInterval:   10 * time.Millisecond,
		ReconnectDelay: 10 * time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()

	// The device is still paired: a transient disconnect must NOT exit -- the loop keeps
	// reconnecting (as today). Run stays blocked across several reconnect ticks.
	select {
	case err := <-done:
		t.Fatalf("Run returned %v while device still paired; a transient disconnect must reconnect, not exit", err)
	case <-time.After(80 * time.Millisecond):
	}

	// Owner revokes the device: the epoch key has rotated; remove it from the registry.
	reg, err := device.Open(filepath.Join(stateDir, "devices"))
	if err != nil {
		t.Fatalf("reopen registry: %v", err)
	}
	if ok, err := reg.Remove(deviceID); err != nil || !ok {
		t.Fatalf("registry.Remove(%s) = %v, %v; want true, nil", deviceID, ok, err)
	}

	// The next reconnect must see the device gone and exit -- not reseal under the stale key.
	select {
	case err := <-done:
		if !errors.Is(err, ErrDeviceRevoked) {
			t.Fatalf("Run returned %v, want ErrDeviceRevoked", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit after the paired device was revoked (gateway is reconnecting-and-resealing under the stale key)")
	}
}

// TestService_RunSurvivesTransientRegistryError pins Finding 1 (availability regression,
// codex#6 / sonnet#3 / opus#1): the reconnect-time liveness check must distinguish a
// DEFINITIVE revocation (registry read succeeded, device absent -> exit) from a TRANSIENT read
// error (registry momentarily unreadable -> keep running, re-check next cycle). The pre-fix
// check treated ANY device.Open error as "revoked" and exited the whole Service, so a
// coincidental FS hiccup on a routine daemon reconnect would silently, permanently kill remote
// control until a human restarts the sidecar. Here <StateDir>/devices is a FILE, so device.Open
// (which MkdirAll's that path) errors -- standing in for a torn read / transiently-unavailable
// (e.g. network-mounted) stateDir. Run must NOT exit while the registry is unreadable.
func TestService_RunSurvivesTransientRegistryError(t *testing.T) {
	stateDir := t.TempDir()
	// A regular file where the registry directory is expected makes device.Open fail
	// deterministically (mkdir over a non-directory), simulating an unreadable registry.
	if err := os.WriteFile(filepath.Join(stateDir, "devices"), []byte("transient"), 0o600); err != nil {
		t.Fatalf("seed unreadable registry: %v", err)
	}

	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 2)
	}
	svc := NewService(ServiceConfig{
		DaemonSocket:   "/nonexistent/remote.sock", // RunJournal fails fast -> the reconnect loop spins the check
		Relay:          &scriptedMailbox{},
		PhoneTarget:    "phone",
		Key:            key,
		EpochID:        1,
		StateDir:       stateDir,
		DeviceID:       "phone-device", // non-empty enables the check; Open fails before the id is used
		PollInterval:   10 * time.Millisecond,
		ReconnectDelay: 10 * time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()

	// Across many reconnect ticks the transient Open error must NOT exit the Service: an
	// unconfirmable read is retried, never mistaken for a revocation.
	select {
	case err := <-done:
		t.Fatalf("Run exited with %v on a transient registry read error; an unreadable registry must be retried, not treated as a revocation", err)
	case <-time.After(120 * time.Millisecond):
	}
}
