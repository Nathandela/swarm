package main

import (
	"context"
	"errors"
	"time"

	"github.com/Nathandela/swarm/internal/remote/grant"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// grantDeliverer is the relay subset the C5 grant bootstrap uses after the native
// generation has already authorized the paired device.
type grantDeliverer interface {
	MailboxAppend(ctx context.Context, target string, env []byte) (uint64, error)
}

// grantAppendError identifies the only delivery phase whose clean append-quota refusal may
// be recovered concurrently with Service. Authorization failures must remain fail-closed,
// even when a relay happens to encode one with the same broad quota sentinel.
type grantAppendError struct {
	err   error
	frame []byte
}

func (e *grantAppendError) Error() string { return e.err.Error() }
func (e *grantAppendError) Unwrap() error { return e.err }

// deliverEpochGrant appends the persisted sealed
// EpochGrant to the DEVICE mailbox as a tagged plaintext bootstrap frame the phone
// consumes BEFORE it can build a ContentKey-keyed router (the grant is what DELIVERS the
// ContentKey). Delivery is idempotent: it appends once per gateway session and the phone
// dedups by grant seq (at-least-once mailbox semantics). A nil grant (no sidecar -- a
// pre-grant pairing) is a no-op.
func deliverEpochGrant(ctx context.Context, d grantDeliverer, p gatewayParams) error {
	if p.Grant == nil {
		return nil
	}
	frame, err := grant.MarshalBootstrap(p.Grant)
	if err != nil {
		return err
	}
	if _, err := d.MailboxAppend(ctx, p.PhoneTarget, frame); err != nil {
		return &grantAppendError{err: err, frame: frame}
	}
	return nil
}

// retryEpochGrantAppend preserves the exact bootstrap bytes from the refused first append.
// A mailbox append quota refusal is retried with the shared reconnect backoff, never in a spin.
// Any other result ends the retry: nil means the phone can now receive the grant, while an
// error ends this relay generation so terminal revocation/consent state remains fail-closed
// and a broken link is redialled by run.
func retryEpochGrantAppend(ctx context.Context, d grantDeliverer, target string, frame []byte) error {
	backoff := relay.NewReconnectBackoff()
	for {
		timer := time.NewTimer(backoff.Next())
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if _, err := d.MailboxAppend(ctx, target, frame); err != nil {
			if errors.Is(err, relay.ErrQuotaExceeded) {
				continue
			}
			return err
		}
		return nil
	}
}
