package pairing

// ADR-007 B64 -- THE ABORT IS SENT ON THE CTX THAT WAS JUST CANCELLED, SO IT IS NEVER SENT.
//
// B52 made msg4 the device's answer and gave the refusal a frame of its own, so a machine
// that has already spent its operator's confirm is told "no" rather than left on a receive.
// The line that does it is pairing.go:665:
//
//	_ = sendConsent(ctx, sess, rt, nil) // an ANSWERED refusal, not silence
//
// and `ctx` there is the ctx whose cancellation is the ONLY way the shipped phone ever
// reaches that line. mobile/pairing.go's DeviceSAS closure returns exactly two things --
// nil, or ctx.Err() -- so every refusal this package can receive from a real build arrives
// with the ctx already dead. RejectSAS and Cancel both go through cancelHandshake(), which
// cancels that ctx; the 60 s pairingTTL cancels the same ctx. A relay Send on a done ctx
// delivers nothing (relay/client.go writeFrame -> ws.Write(ctx, ...)), so the frame whose
// whole purpose is to unpark the machine is the one frame that cannot go out.
//
// The existing fence for this claim, TestB52_ARefusalIsAnAnsweredAbortNotSilence, sets
// DeviceSAS to a closure returning a non-nil error on a LIVE ctx. No shipped phone produces
// that shape. It therefore tests a rejection production cannot make and is silent on the one
// it can. It is left standing deliberately -- it pins a real property of the frame format --
// and these tests add the shape production actually reaches.
//
// The transport here is ctx-faithful ON PURPOSE. The package's own fakeRendezvous appends to
// f.sent BEFORE selecting, and its outbox is buffered, so on a dead ctx Go picks a ready case
// at random and the frame is DELIVERED about half the time: a defect that is a coin flip in
// the harness and a certainty in production.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// observeWindow is how long these tests WATCH the machine before calling it parked. It is an
// observation bound on the assertion, never a deadline handed to production: every Machine.Pair
// below is given context.Background(), exactly as internal/protocol/server.go:2098 constructs
// it. Injecting the deadline into Pair is what made the B52 silence fence vacuous -- changing
// its 2*time.Second literal to 10*time.Minute turns it red with production untouched.
const observeWindow = 5 * time.Second

// ctxFaithfulRendezvous is a RendezvousTransport that refuses a Send on a done ctx, as
// relay.Conn does: writeFrame passes the caller's ctx into ws.Write, which fails outright
// once that ctx is cancelled. It counts the refusals so a failure can report whether the
// abort was attempted-and-dropped rather than never attempted.
type ctxFaithfulRendezvous struct {
	*fakeRendezvous

	mu      sync.Mutex
	refused int
}

var _ RendezvousTransport = (*ctxFaithfulRendezvous)(nil)

func (c *ctxFaithfulRendezvous) Send(ctx context.Context, msg []byte) error {
	if err := ctx.Err(); err != nil {
		c.mu.Lock()
		c.refused++
		c.mu.Unlock()
		return err
	}
	return c.fakeRendezvous.Send(ctx, msg)
}

func (c *ctxFaithfulRendezvous) refusedSends() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.refused
}

// shippedDeviceSAS is mobile/pairing.go's DeviceSAS closure, byte-for-byte in shape: it
// returns nil when the phone operator matched the code, and ctx.Err() otherwise. There is no
// third return. Every abort the shipped phone can produce -- RejectSAS, Cancel, the 60 s
// pairingTTL -- arrives here as a cancellation of the very ctx RunDevice then tries to send
// the refusal on.
func shippedDeviceSAS(shown chan<- struct{}, matched <-chan struct{}) DeviceSASFunc {
	return func(ctx context.Context, _ [6]string) error {
		select {
		case shown <- struct{}{}:
		default:
		}
		select {
		case <-matched:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// TestB64_TheAbortAShippedPhoneProducesReachesTheMachine is the defect at the boundary the
// operator lives on: the desktop confirm has ALREADY been given -- that prompt was in front
// of them, so they answered it -- and only then does the phone abort. The machine is inside
// recvConsent by that point, and recvConsent has no clock.
//
// No attacker is required. This is the natural order of the ceremony.
func TestB64_TheAbortAShippedPhoneProducesReachesTheMachine(t *testing.T) {
	for _, tc := range []struct {
		name string
		// abort cancels the device ctx the way the shipped phone does, once its SAS is
		// on screen. It returns the ctx RunDevice runs under.
		abort func(t *testing.T, shown <-chan struct{}) context.Context
	}{
		{
			// mobile/pairing.go RejectSAS and Cancel both call cancelHandshake(), which
			// cancels the pairing ctx (and CloseNow()s the socket, which this transport
			// does not even need to model for the frame to be lost).
			name: "the operator sees a mismatch and rejects (cancelHandshake)",
			abort: func(t *testing.T, shown <-chan struct{}) context.Context {
				t.Helper()
				ctx, cancel := context.WithCancel(context.Background())
				t.Cleanup(cancel)
				go func() {
					<-shown
					cancel()
				}()
				return ctx
			},
		},
		{
			// pairingTTL (mobile/pairing.go:110, 60 s) is a deadline on the SAME ctx. The
			// short value here is the PHONE's own clock scaled down, not a bound this test
			// gives the machine -- the machine still gets context.Background().
			name: "the phone's own pairingTTL elapses while the operator compares",
			abort: func(t *testing.T, _ <-chan struct{}) context.Context {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				t.Cleanup(cancel)
				return ctx
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mID, _ := crypto.GenerateIdentity()
			dID, _ := crypto.GenerateIdentity()
			secret, rid := fill32(0x63), fill16(0x63)

			confirmed := make(chan struct{})
			mp := newMachineParams(mID, secret, rid, func(context.Context, [6]string, string) (bool, error) {
				close(confirmed) // the desktop operator answers the prompt in front of them
				return true, nil
			})

			shown := make(chan struct{}, 1)
			dp := newDeviceParams(dID, secret, rid)
			dp.DeviceSAS = shippedDeviceSAS(shown, nil) // nil matched: this operator never matches

			mEndRaw, dEndRaw := newRendezvousPipe()
			dEnd := &ctxFaithfulRendezvous{fakeRendezvous: dEndRaw}

			type res struct {
				out *MachineOutcome
				err error
			}
			done := make(chan res, 1)
			go func() {
				// context.Background(): the production ctx (internal/protocol/server.go:2098)
				// carries no deadline, and nothing else on the Pair path supplies one.
				out, err := NewMachine(mp).Pair(context.Background(), mEndRaw)
				done <- res{out, err}
			}()

			devCtx := tc.abort(t, shown)
			if _, err := RunDevice(devCtx, dp, dEnd); err == nil {
				t.Fatal("RunDevice returned nil for an aborted pairing; the device leg must fail closed")
			}

			select {
			case <-confirmed:
			case <-time.After(observeWindow):
				t.Fatal("the machine never reached the desktop confirm; this test is not measuring what it claims")
			}

			select {
			case r := <-done:
				if r.out != nil {
					t.Fatalf("the machine enrolled a device whose operator ABORTED (err=%v)", r.err)
				}
				if !errors.Is(r.err, ErrNoConsent) {
					t.Fatalf("machine err = %v; want ErrNoConsent (the abort is an answer)", r.err)
				}
			case <-time.After(observeWindow):
				t.Fatalf("the machine is STILL parked in recvConsent %s after an AFFIRMATIVE desktop confirm, "+
					"with the phone long since aborted. The device attempted %d sends that the dead ctx "+
					"REFUSED -- one of them is the msg4 abort. pairing.go:665 calls sendConsent on the ctx "+
					"whose cancellation is the only way a shipped phone reaches that line, so the frame that "+
					"exists to unpark this machine is the one frame that never leaves.",
					observeWindow, dEnd.refusedSends())
			}

			// The device is not left holding an unburned rendezvous either.
			if got := len(mEndRaw.completedIDs()); got == 0 {
				t.Error("the machine never burned the rendezvous on an aborted pairing")
			}
		})
	}
}

// TestB64_ControlAnOrdinaryPairingCompletesOverTheSameTransport keeps the tests above from
// being vacuous: on the ctx-faithful transport, with a phone whose operator MATCHES the code,
// the pairing still completes and the consent still arrives. If this ever fails, the transport
// is the defect, not production.
func TestB64_ControlAnOrdinaryPairingCompletesOverTheSameTransport(t *testing.T) {
	mID, _ := crypto.GenerateIdentity()
	dID, _ := crypto.GenerateIdentity()
	secret, rid := fill32(0x64), fill16(0x64)

	mp := newMachineParams(mID, secret, rid, acceptConfirm)

	matched := make(chan struct{})
	close(matched) // this operator compared the codes and they agree
	dp := newDeviceParams(dID, secret, rid)
	dp.DeviceSAS = shippedDeviceSAS(make(chan struct{}, 1), matched)

	mEndRaw, dEndRaw := newRendezvousPipe()
	dEnd := &ctxFaithfulRendezvous{fakeRendezvous: dEndRaw}

	type res struct {
		out *MachineOutcome
		err error
	}
	done := make(chan res, 1)
	go func() {
		out, err := NewMachine(mp).Pair(context.Background(), mEndRaw)
		done <- res{out, err}
	}()

	do, dErr := RunDevice(context.Background(), dp, dEnd)
	if dErr != nil || do == nil {
		t.Fatalf("device leg: outcome=%v err=%v; want a completed pairing", do, dErr)
	}

	select {
	case r := <-done:
		if r.err != nil || r.out == nil {
			t.Fatalf("machine: outcome=%v err=%v; want a completed pairing", r.out, r.err)
		}
		if len(r.out.Device.ConsentSig) == 0 {
			t.Fatal("the machine completed the pairing holding no relay-route consent")
		}
	case <-time.After(observeWindow):
		t.Fatal("the machine hung on the HAPPY path over the ctx-faithful transport; the transport is broken")
	}
	if dEnd.refusedSends() != 0 {
		t.Fatalf("the transport refused %d sends on a pairing that never cancelled anything", dEnd.refusedSends())
	}
}
