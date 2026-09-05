package relayv2

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"sync"

	"github.com/Nathandela/swarm/internal/remote/relay"
)

type retainedDelivery struct {
	item relay.Item
	size int
}

// MachineMailbox adapts one authenticated relay-v2 machine subscription to the
// gateway's retained batch mailbox contract.
type MachineMailbox struct {
	sub      *Subscription
	mu       sync.Mutex
	retained []retainedDelivery
}

func NewMachineMailbox(sub *Subscription) (*MachineMailbox, error) {
	if sub == nil || sub.conn == nil || sub.conn.role != RoleMachine || sub.conn.purpose != PurposeStream ||
		sub.binding.PeerRID != sub.peer || !validIncarnation(sub.incarnation) {
		return nil, errors.New("relay v2: invalid machine subscription")
	}
	return &MachineMailbox{sub: sub}, nil
}

func (m *MachineMailbox) Done() <-chan struct{} { return m.sub.conn.Done() }

func (m *MachineMailbox) MailboxRead(ctx context.Context, cursor uint64) ([]relay.Item, error) {
	items, _, err := m.collect(ctx, cursor, false)
	return items, err
}

func (m *MachineMailbox) MailboxWait(ctx context.Context, cursor uint64) ([]relay.Item, bool, error) {
	return m.collect(ctx, cursor, true)
}

func (m *MachineMailbox) collect(ctx context.Context, cursor uint64, wait bool) ([]relay.Item, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.releaseThrough(cursor)
	if wait && len(m.retained) == 0 {
		queued, err := m.sub.take(ctx)
		if err != nil {
			return nil, false, err
		}
		if err := m.retain(queued); err != nil {
			return nil, false, err
		}
	}
	for {
		queued, ok, err := m.sub.tryTake()
		if err != nil {
			return nil, false, err
		}
		if !ok {
			break
		}
		if err := m.retain(queued); err != nil {
			return nil, false, err
		}
	}
	items := make([]relay.Item, len(m.retained))
	for i, retained := range m.retained {
		items[i] = relay.Item{Cursor: retained.item.Cursor, Envelope: append([]byte(nil), retained.item.Envelope...)}
	}
	return items, false, nil
}

func (m *MachineMailbox) retain(queued queuedFrame) error {
	delivery, err := m.sub.decodeDelivery(queued)
	if err != nil {
		m.sub.conn.releaseDelivery(queued.size)
		return err
	}
	m.retained = append(m.retained, retainedDelivery{
		item: relay.Item{Cursor: delivery.Cursor, Envelope: delivery.Ciphertext}, size: queued.size,
	})
	return nil
}

func (m *MachineMailbox) releaseThrough(cursor uint64) {
	kept := m.retained[:0]
	for i := range m.retained {
		if m.retained[i].item.Cursor <= cursor {
			m.sub.conn.releaseDelivery(m.retained[i].size)
			continue
		}
		kept = append(kept, m.retained[i])
	}
	clear(m.retained[len(kept):])
	m.retained = kept
}

func (m *MachineMailbox) MailboxAppend(ctx context.Context, target string, ciphertext []byte) (uint64, error) {
	if target != m.sub.binding.PeerRID {
		return 0, errors.New("relay v2: append target does not match binding")
	}
	digest := sha256.Sum256(ciphertext)
	result, err := m.sub.conn.Append(ctx, m.sub.binding, base64.RawURLEncoding.EncodeToString(digest[:]), ciphertext)
	return result.Cursor, err
}

func (m *MachineMailbox) MailboxAck(ctx context.Context, cursor uint64) error {
	if cursor == 0 {
		return errors.New("relay v2: zero mailbox ack")
	}
	return m.sub.Ack(ctx, cursor)
}
