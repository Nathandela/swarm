package skeleton

import (
	"context"
	"encoding/hex"

	"github.com/Nathandela/swarm/internal/remote/pairing"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// relayRendezvous adapts a raw relay.Conn to pairing.RendezvousTransport, bound to
// one rendezvous. relay.Conn's Rendezvous* methods do NOT satisfy the interface
// directly: the names differ, and RendezvousSend carries an `id` the interface's Send
// does not (the transport is bound to one rendezvous). This thin wrapper forwards each
// interface method to the matching Conn.Rendezvous* call, injecting the bound label on
// Send. The label is hex(rendezvous id), which is exactly what Machine.Pair passes to
// Create/Complete (rendezvousLabel(id) == hex.EncodeToString(id[:])), so every op
// routes to the same rendezvous.
type relayRendezvous struct {
	conn  *relay.Conn
	label string // hex(rendezvous id) this transport is bound to
}

func (r *relayRendezvous) Create(ctx context.Context, id string) error {
	return r.conn.RendezvousCreate(ctx, id)
}

func (r *relayRendezvous) Claim(ctx context.Context, id string) error {
	return r.conn.RendezvousClaim(ctx, id)
}

func (r *relayRendezvous) Send(ctx context.Context, msg []byte) error {
	return r.conn.RendezvousSend(ctx, r.label, msg)
}

func (r *relayRendezvous) Recv(ctx context.Context) ([]byte, error) {
	return r.conn.RendezvousRecv(ctx)
}

func (r *relayRendezvous) Complete(ctx context.Context, id string) error {
	return r.conn.RendezvousComplete(ctx, id)
}

// Compile-time proof the wrapper satisfies the pairing seam (a raw relay.Conn does not).
var _ pairing.RendezvousTransport = (*relayRendezvous)(nil)

// relayRendezvousFactory returns the pairingConfig.NewRendezvous closure for a
// configured relay URL: it DialRaw's the relay and returns a transport bound to
// hex(id). The conn lives for the pairing window; a watcher closes it when the pairing
// ctx is cancelled — which the server does at the terminal outcome (result ->
// clearPairing -> cancel) or when the owner connection drops — so no conn is leaked on
// any path (Machine.Pair's final Complete runs before that cancellation).
func relayRendezvousFactory(relayURL string) func(context.Context, [16]byte) (pairing.RendezvousTransport, error) {
	return func(ctx context.Context, id [16]byte) (pairing.RendezvousTransport, error) {
		conn, err := relay.DialRaw(ctx, relayURL)
		if err != nil {
			return nil, err
		}
		go func() {
			<-ctx.Done()
			_ = conn.Close()
		}()
		return &relayRendezvous{conn: conn, label: hex.EncodeToString(id[:])}, nil
	}
}
