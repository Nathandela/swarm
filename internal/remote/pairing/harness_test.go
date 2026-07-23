// Shared fixtures + helpers for the pairing slice's FAILING-FIRST tests (TDD
// RED, GG-5). Every exported pairing symbol referenced here is the frozen
// contract a separate implementer supplies; the stubs in pairing.go / qr.go
// return ErrUnimplemented, so these tests compile and fail BEHAVIORALLY for the
// right reason (unimplemented seam, an established RED style per plan section B).
//
// Hermeticity: rendezvous is a test double (fakeRendezvous, an in-memory opaque
// two-party byte pipe). R-PAIR.6 rendezvous mechanics are owned + tested by the
// relay package; nothing here spins up the real relay. The only crypto used is
// the frozen public API of internal/remote/crypto.
package pairing

import (
	"bytes"
	"context"
	"sync"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// fill16 / fill32 build recognizable sentinel byte arrays so a leak of a secret
// or a rendezvous id onto the wire is unmistakable.
func fill16(b byte) [16]byte {
	var a [16]byte
	for i := range a {
		a[i] = b
	}
	return a
}

func fill32(b byte) [32]byte {
	var a [32]byte
	for i := range a {
		a[i] = b
	}
	return a
}

// acceptConfirm is a ConfirmFunc that unconditionally allows — the operator
// answering "y" to a device they recognise.
var acceptConfirm ConfirmFunc = func(ctx context.Context, sas [6]string, name string) (bool, error) {
	return true, nil
}

// confirmRecorder captures the SAS + device name the machine passed to its
// desktop confirm, so a test can assert the operator was actually shown a SAS
// (and, in a MITM, that the two ends' SAS diverge).
type confirmRecorder struct {
	mu     sync.Mutex
	sas    [6]string
	name   string
	called int
}

// fn returns a ConfirmFunc that records its inputs then returns (allow, err).
func (c *confirmRecorder) fn(allow bool, err error) ConfirmFunc {
	return func(ctx context.Context, sas [6]string, name string) (bool, error) {
		c.mu.Lock()
		c.sas, c.name, c.called = sas, name, c.called+1
		c.mu.Unlock()
		return allow, err
	}
}

func (c *confirmRecorder) snapshot() (sas [6]string, name string, called int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sas, c.name, c.called
}

// fakeRendezvous is one end of an in-memory, two-party RendezvousTransport. It
// forwards opaque bytes verbatim (faithful to the relay's forward-only role) and
// records everything Sent plus the Create/Claim/Complete lifecycle so tests can
// assert the secret is never on the wire (R-PAIR.1) and that no listener stands
// between attempts (R-PAIR.8).
type fakeRendezvous struct {
	outbox chan []byte // this end's Send target (the peer's inbox)
	inbox  chan []byte // this end's Recv source

	mu        sync.Mutex
	sent      [][]byte
	createIDs []string
	claimIDs  []string
	completed []string
}

var _ RendezvousTransport = (*fakeRendezvous)(nil)

// newRendezvousPipe returns the machine end and the device end of one in-memory
// rendezvous. Channels are buffered so the ping-pong handshake never self-locks.
func newRendezvousPipe() (machine, device *fakeRendezvous) {
	aToB := make(chan []byte, 16)
	bToA := make(chan []byte, 16)
	machine = &fakeRendezvous{outbox: aToB, inbox: bToA}
	device = &fakeRendezvous{outbox: bToA, inbox: aToB}
	return machine, device
}

func (f *fakeRendezvous) Create(ctx context.Context, id string) error {
	f.mu.Lock()
	f.createIDs = append(f.createIDs, id)
	f.mu.Unlock()
	return nil
}

func (f *fakeRendezvous) Claim(ctx context.Context, id string) error {
	f.mu.Lock()
	f.claimIDs = append(f.claimIDs, id)
	f.mu.Unlock()
	return nil
}

func (f *fakeRendezvous) Send(ctx context.Context, msg []byte) error {
	cp := append([]byte(nil), msg...)
	f.mu.Lock()
	f.sent = append(f.sent, cp)
	f.mu.Unlock()
	select {
	case f.outbox <- cp:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *fakeRendezvous) Recv(ctx context.Context) ([]byte, error) {
	select {
	case m := <-f.inbox:
		return m, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (f *fakeRendezvous) Complete(ctx context.Context, id string) error {
	f.mu.Lock()
	f.completed = append(f.completed, id)
	f.mu.Unlock()
	return nil
}

// sentBytes returns a copy of every frame this end put on the wire.
func (f *fakeRendezvous) sentBytes() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]byte, len(f.sent))
	copy(out, f.sent)
	return out
}

func (f *fakeRendezvous) createdIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.createIDs...)
}

func (f *fakeRendezvous) completedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.completed...)
}

// refusingRendezvous is a RendezvousTransport whose Create fails with a fixed
// error — it stands in for a relay refusing a rendezvous (e.g. rate-limited).
type refusingRendezvous struct {
	createErr error
}

var _ RendezvousTransport = (*refusingRendezvous)(nil)

func (r *refusingRendezvous) Create(ctx context.Context, id string) error { return r.createErr }
func (r *refusingRendezvous) Claim(ctx context.Context, id string) error  { return nil }
func (r *refusingRendezvous) Send(ctx context.Context, msg []byte) error  { return nil }
func (r *refusingRendezvous) Recv(ctx context.Context) ([]byte, error) {
	return nil, context.Canceled
}
func (r *refusingRendezvous) Complete(ctx context.Context, id string) error { return nil }

// countingLimiter is a RateLimiter with a fixed budget: Allow succeeds while the
// budget remains, then refuses.
type countingLimiter struct {
	mu        sync.Mutex
	remaining int
}

var _ RateLimiter = (*countingLimiter)(nil)

func (l *countingLimiter) Allow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.remaining <= 0 {
		return false
	}
	l.remaining--
	return true
}

// newMachineParams builds machine params from an identity + secret + rendezvous
// id, with a representative msg2 payload (recipient pub = A14 second X25519 key)
// and a local console present.
func newMachineParams(id *crypto.Identity, secret [32]byte, rid [16]byte, confirm ConfirmFunc) MachineParams {
	return MachineParams{
		Static:       id.NoiseStatic(),
		Secret:       secret,
		RendezvousID: rid,
		LocalConsole: true,
		Confirm:      confirm,
		Payload: MachinePayload{
			Hostname:            "test-machine.local",
			MachineRoutingID:    []byte("machine-routing-id-0001"),
			MachineRelayAuthPub: []byte("machine-relay-auth-pub-ed25519!!"),
			RecipientPub:        id.RecipientPublic(),
			EpochID:             1,
		},
	}
}

// newDeviceParams builds device params from an identity + secret + rendezvous id
// with a representative msg3 payload.
func newDeviceParams(id *crypto.Identity, secret [32]byte, rid [16]byte) DeviceParams {
	return DeviceParams{
		Static:       id.NoiseStatic(),
		Secret:       secret,
		RendezvousID: rid,
		Payload: DevicePayload{
			DeviceName:         "Test iPhone",
			DeviceRoutingID:    []byte("device-routing-id-0001"),
			DeviceRelayAuthPub: []byte("device-relay-auth-pub-ed25519!!!"),
			RecipientPub:       id.RecipientPublic(),
		},
	}
}

// drivePair runs a machine (responder) and a device (initiator) pairing
// concurrently over the two ends of a rendezvous pipe and returns both outcomes
// and errors. The WaitGroup makes the goroutine writes happen-before the read.
func drivePair(t *testing.T, m *Machine, dp DeviceParams, mEnd, dEnd RendezvousTransport) (mo *MachineOutcome, mErr error, do *DeviceOutcome, dErr error) {
	t.Helper()
	ctx := context.Background()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		mo, mErr = m.Pair(ctx, mEnd)
	}()
	go func() {
		defer wg.Done()
		do, dErr = RunDevice(ctx, dp, dEnd)
	}()
	wg.Wait()
	return mo, mErr, do, dErr
}

// driveLiveXX runs a full live (non-PSK) Noise XX handshake between two sessions,
// used to prove a pin from pairing authenticates a later live handshake.
func driveLiveXX(ini, resp *crypto.NoiseSession) error {
	m1, err := ini.WriteMessage(nil)
	if err != nil {
		return err
	}
	if _, err = resp.ReadMessage(m1); err != nil {
		return err
	}
	m2, err := resp.WriteMessage(nil)
	if err != nil {
		return err
	}
	if _, err = ini.ReadMessage(m2); err != nil {
		return err
	}
	m3, err := ini.WriteMessage(nil)
	if err != nil {
		return err
	}
	if _, err = resp.ReadMessage(m3); err != nil {
		return err
	}
	return nil
}

// assertMachinePayload asserts got carries every field of want (msg2 carriage).
func assertMachinePayload(t *testing.T, got, want MachinePayload) {
	t.Helper()
	if got.Hostname != want.Hostname {
		t.Errorf("machine payload Hostname = %q, want %q", got.Hostname, want.Hostname)
	}
	if !bytes.Equal(got.MachineRoutingID, want.MachineRoutingID) {
		t.Errorf("machine payload MachineRoutingID = %x, want %x", got.MachineRoutingID, want.MachineRoutingID)
	}
	if !bytes.Equal(got.MachineRelayAuthPub, want.MachineRelayAuthPub) {
		t.Errorf("machine payload MachineRelayAuthPub = %x, want %x", got.MachineRelayAuthPub, want.MachineRelayAuthPub)
	}
	if !bytes.Equal(got.RecipientPub, want.RecipientPub) {
		t.Errorf("machine payload RecipientPub = %x, want %x (A14: both X25519 keys pinned)", got.RecipientPub, want.RecipientPub)
	}
	if got.EpochID != want.EpochID {
		t.Errorf("machine payload EpochID = %d, want %d", got.EpochID, want.EpochID)
	}
}

// assertDevicePayload asserts got carries every field of want (msg3 carriage).
func assertDevicePayload(t *testing.T, got, want DevicePayload) {
	t.Helper()
	if got.DeviceName != want.DeviceName {
		t.Errorf("device payload DeviceName = %q, want %q", got.DeviceName, want.DeviceName)
	}
	if !bytes.Equal(got.DeviceRoutingID, want.DeviceRoutingID) {
		t.Errorf("device payload DeviceRoutingID = %x, want %x", got.DeviceRoutingID, want.DeviceRoutingID)
	}
	if !bytes.Equal(got.DeviceRelayAuthPub, want.DeviceRelayAuthPub) {
		t.Errorf("device payload DeviceRelayAuthPub = %x, want %x", got.DeviceRelayAuthPub, want.DeviceRelayAuthPub)
	}
	if !bytes.Equal(got.RecipientPub, want.RecipientPub) {
		t.Errorf("device payload RecipientPub = %x, want %x (A14: both X25519 keys pinned)", got.RecipientPub, want.RecipientPub)
	}
}
