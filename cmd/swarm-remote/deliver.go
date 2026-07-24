package main

import (
	"context"
	"crypto/ed25519"

	"github.com/Nathandela/swarm/internal/remote/grant"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// grantDeliverer is the relay subset the C5 grant bootstrap uses: authorize the paired
// device's mailbox route, then append the sealed grant to it. *relay.Client satisfies it.
type grantDeliverer interface {
	AuthorizeDevice(ctx context.Context, devicePub ed25519.PublicKey) error
	MailboxAppend(ctx context.Context, target string, env []byte) (uint64, error)
}

// The production relay client is a grantDeliverer. Pinned at compile time.
var _ grantDeliverer = (*relay.Client)(nil)

// deliverEpochGrant is the gateway half of ADR-007 decision C5. On connect it (1)
// authorizes the paired device with the relay -- closing the dead-authorize_device gap
// so the machine->device mailbox route exists -- and (2) appends the persisted sealed
// EpochGrant to the DEVICE mailbox as a tagged plaintext bootstrap frame the phone
// consumes BEFORE it can build a ContentKey-keyed router (the grant is what DELIVERS the
// ContentKey). Delivery is idempotent: it appends once per gateway session and the phone
// dedups by grant seq (at-least-once mailbox semantics). A nil grant (no sidecar -- a
// pre-grant pairing) is a no-op.
func deliverEpochGrant(ctx context.Context, d grantDeliverer, p gatewayParams) error {
	if p.Grant == nil {
		return nil
	}
	if err := d.AuthorizeDevice(ctx, p.DeviceRelayAuthPub); err != nil {
		return err
	}
	frame, err := grant.MarshalBootstrap(p.Grant)
	if err != nil {
		return err
	}
	_, err = d.MailboxAppend(ctx, p.PhoneTarget, frame)
	return err
}
