package skeleton

// FAILING-FIRST (TDD RED, GG-5) test for the DIAGNOSABILITY defect behind slice S19's fourth
// production hole.
//
// LeaseConn.readLoop returns on protocol.OpError without reading the error it just decoded, so
// every daemon refusal of a take_control -- a missing gate token, an unknown device, a forged
// or expired signature, an insufficient capability, the kill switch -- arrives at the phone as
// the single string "remotegw: lease died before it was granted". Six distinct causes, one
// message, and the daemon's own words discarded one frame after they were parsed.
//
// The cost is not hypothetical: it is what the S19 exit demonstration hit. A phone whose
// take_control minted no gate token was refused "take_control requires a gate token" by the
// daemon, and the test reported a lease that "died", pointing at the transport rather than at
// the phone. A failure that discards its own cause costs the next debugger hours, and this one
// is the failure the whole take-control path funnels through.
//
// THE ASSERTION IS THE DAEMON'S OWN WORDS. Anything weaker -- a distinct sentinel error, an
// error class -- would still require the reader to already know which refusal they were
// looking at, which is the property being restored.

import (
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remotegw"
)

// TestS19_ARefusedLeaseReportsTheDaemonsReason.
//
// The refusal driven here is the ABSENT GATE TOKEN, because that is the one the exit
// demonstration actually hit and because handleTakeControl refuses it before any side effect:
// the daemon replies and attaches nothing, so the test observes a pure refusal.
func TestS19_ARefusedLeaseReportsTheDaemonsReason(t *testing.T) {
	sk, rsock := assembleWithRemote(t)
	ks := registerPhone(t, sk, device.CapFull)

	meta := launchFake(t, sk, "idle 600s\n")
	session := protocol.NamespacedID(sk.api.endpointID, meta.ID)

	// A take_control signed exactly as the phone signs one, and then sent with NO gate token
	// on the wire -- the shape App.TakeControl produced before S19. handleTakeControl's
	// present-check refuses it with a specific message.
	cmd, err := phonecore.SignCommand(ks, phonecore.CommandInput{
		Action:      protocol.ActionTakeControl,
		Machine:     sk.api.endpointID,
		Session:     session,
		OperationID: "devS19:01JS19NOGATETOKEN00000",
		ExpiresAt:   time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("sign take_control: %v", err)
	}

	lc, err := remotegw.DialLease(rsock, protocol.RemoteCommand{DeviceCommandAuth: cmd})
	if err != nil {
		t.Fatalf("dial lease: %v", err)
	}
	defer func() { _ = lc.Close() }()

	gen, err := lc.AwaitLease(10 * time.Second)
	if err == nil {
		t.Fatalf("the daemon GRANTED a lease (generation %d) for a take_control carrying no gate "+
			"token; this test's subject is the refusal path", gen)
	}
	if !strings.Contains(err.Error(), "gate token") {
		t.Fatalf("a refused lease reported %q. The daemon said why -- \"take_control requires a "+
			"gate token\" -- and readLoop decoded that OpError and returned without reading it, so "+
			"six different refusals (no token, unknown device, bad signature, expired command, "+
			"insufficient capability, kill switch) all reach the operator as one sentence about "+
			"the transport", err)
	}
}
