package skeleton

// Enrollment-keystone E2E (agents-tracker-qo4) against a REAL daemon: a phone and
// machine complete a REAL pairing handshake over an in-memory rendezvous; the
// machine enrolls the device (registry record + sealed epoch grant) WITHOUT any
// hand-provisioned Record or ContentKey; the phone accepts the grant to recover
// the epoch content key; then the phone signs a kill, seals it under THAT accepted
// key, the gateway opens it, forwards it, and the daemon authorizes it against the
// enrolled record and executes. This proves the pairing outcome alone bootstraps
// both halves of R-POL.9 -- the pinned command key AND the shared content key --
// with no manual setup (contrast TestE2E_* which hand-build the record and key).

import (
	"context"
	"crypto/ed25519"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remote/enroll"
	"github.com/Nathandela/swarm/internal/remote/pairing"
	"github.com/Nathandela/swarm/internal/remotegw"
)

// memRendezvous is an in-memory two-party RendezvousTransport: Send pushes to the
// peer's Recv. It stands in for the relay rendezvous (whose mechanics relay tests
// own), so this test exercises the real Noise pairing without a network.
type memRendezvous struct {
	out chan []byte
	in  chan []byte
}

func (r *memRendezvous) Create(context.Context, string) error { return nil }
func (r *memRendezvous) Claim(context.Context, string) error  { return nil }
func (r *memRendezvous) Send(ctx context.Context, m []byte) error {
	select {
	case r.out <- m:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (r *memRendezvous) Recv(ctx context.Context) ([]byte, error) {
	select {
	case m := <-r.in:
		return m, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (r *memRendezvous) Complete(context.Context, string) error { return nil }

func rendezvousPair() (machine, dev *memRendezvous) {
	a := make(chan []byte, 8)
	b := make(chan []byte, 8)
	return &memRendezvous{out: a, in: b}, &memRendezvous{out: b, in: a}
}

func fillKey(b byte) [32]byte {
	var k [32]byte
	for i := range k {
		k[i] = b
	}
	return k
}

func fill16(b byte) [16]byte {
	var k [16]byte
	for i := range k {
		k[i] = b
	}
	return k
}

func TestEnrollmentE2E_PairThenCommandNoManualSetup(t *testing.T) {
	sk, rsock := assembleWithRemote(t)

	// Machine identity + a machine grant-signing key (the daemon-held Ed25519 key
	// the phone pins at pairing and verifies epoch grants against).
	machineID, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("machine identity: %v", err)
	}
	machineSignPub, machineSignPriv, _ := ed25519.GenerateKey(nil)

	// Phone key custody.
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

	// Run both pairing legs concurrently over the in-memory rendezvous.
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
	ctx := context.Background()
	go func() { defer wg.Done(); mo, mErr = m.Pair(ctx, mEnd) }()
	go func() { defer wg.Done(); do, dErr = pairing.RunDevice(ctx, dp, dEnd) }()
	wg.Wait()
	if mErr != nil || dErr != nil {
		t.Fatalf("pairing failed: machine=%v device=%v", mErr, dErr)
	}

	// Machine side: enroll -> registry record + sealed grant. No hand-built record.
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

	// Phone side: accept the grant using the machine sign pub it pinned at pairing.
	// No hand-provisioned ContentKey -- it comes out of the grant.
	_, _, phoneKeys, err := phonecore.AcceptGrant(ks, do.Machine.MachineSignPub, res.Grant)
	if err != nil {
		t.Fatalf("phone accept grant: %v", err)
	}
	contentKey := phoneKeys.ContentKey

	// The phone signs a kill, seals it under the ACCEPTED content key; the gateway
	// opens it under the same key; the daemon authorizes it against the enrolled
	// record and executes.
	meta := launchFake(t, sk, "print HELLO\nidle 60s\n")
	namespaced := protocol.NamespacedID(sk.api.endpointID, meta.ID)
	cmd, err := phonecore.SignCommand(ks, phonecore.CommandInput{
		Action:      protocol.ActionKill,
		Machine:     sk.api.endpointID,
		Session:     namespaced,
		OperationID: "op-enroll-1",
		ExpiresAt:   time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("phone sign: %v", err)
	}
	env, err := phonecore.SealCommandEnvelope(contentKey, epochID, 1, cmd)
	if err != nil {
		t.Fatalf("phone seal command: %v", err)
	}
	got, err := remotegw.OpenCommandEnvelope(contentKey, env)
	if err != nil {
		t.Fatalf("gateway open under accepted content key: %v", err)
	}

	gw := remotegw.New(rsock, nil)
	reply, err := gw.ForwardCommand(protocol.OpKill, got.Session, got, nil)
	if err != nil {
		t.Fatalf("gateway forward: %v", err)
	}
	if reply.Op == protocol.OpError {
		t.Fatalf("enrolled phone's kill was refused end to end: %q / %q", reply.Error, reply.ErrorCode)
	}
}
