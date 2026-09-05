package skeleton

import (
	"context"

	"github.com/Nathandela/swarm/internal/remote/pairing"
	"github.com/Nathandela/swarm/internal/remote/relayv2"
)

var dialRelayV2MachineControl = relayv2.Dial

// relayRendezvousFactory authenticates the machine control socket before it creates
// a relay-v2 pairing ceremony. PairTransport already implements the pairing package's
// transport contract, so no relay-v1 codec adapter belongs on this path.
func relayRendezvousFactory(profile relayv2.Profile, auth relayv2.Auth) func(context.Context, [16]byte) (pairing.RendezvousTransport, error) {
	return func(ctx context.Context, _ [16]byte) (pairing.RendezvousTransport, error) {
		control, err := dialRelayV2MachineControl(ctx, profile, auth)
		if err != nil {
			return nil, err
		}
		go func() {
			<-ctx.Done()
			control.Close()
		}()
		return relayv2.NewMachinePairTransport(control), nil
	}
}
