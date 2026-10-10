package tui

import (
	"context"
	"sync"
)

// Candidates stay owned even if their completion is dropped during shutdown.
type clientLifetime struct {
	mu      sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	clients map[*ownedClient]struct{}
	current *ownedClient
}
type ownedClient struct {
	Client
	ctx    context.Context
	cancel context.CancelFunc
	once   sync.Once
}

func newClientLifetime(c Client) *clientLifetime {
	ctx, cancel := context.WithCancel(context.Background())
	l := &clientLifetime{ctx: ctx, cancel: cancel, clients: make(map[*ownedClient]struct{})}
	l.current = l.track(c)
	return l
}
func (l *clientLifetime) track(c Client) *ownedClient {
	if c == nil {
		return nil
	}
	ctx, cancel := context.WithCancel(l.ctx)
	owned := &ownedClient{Client: c, ctx: ctx, cancel: cancel}
	l.mu.Lock()
	closed := l.ctx.Err() != nil
	if !closed {
		l.clients[owned] = struct{}{}
	}
	l.mu.Unlock()
	if closed {
		owned.close()
	}
	return owned
}
func (c *ownedClient) close() {
	if c != nil {
		c.once.Do(func() { c.cancel(); closeReconnectClient(c.Client) })
	}
}
func (l *clientLifetime) discard(c *ownedClient) {
	if c == nil {
		return
	}
	l.mu.Lock()
	if c == l.current {
		l.mu.Unlock()
		return
	}
	delete(l.clients, c)
	l.mu.Unlock()
	c.close()
}
func (l *clientLifetime) adopt(c *ownedClient) bool {
	l.mu.Lock()
	if l.ctx.Err() != nil {
		l.mu.Unlock()
		l.discard(c)
		return false
	}
	old := l.current
	l.current = c
	l.mu.Unlock()
	if old != c {
		l.discard(old)
	}
	return true
}

// Close cancels recovery and releases both the current client and pending candidates.
func (m rootModel) Close() error {
	if m.connections == nil {
		closeReconnectClient(m.client)
		return nil
	}
	l := m.connections
	l.mu.Lock()
	l.cancel()
	clients := l.clients
	l.clients = make(map[*ownedClient]struct{})
	l.mu.Unlock()
	for c := range clients {
		c.close()
	}
	return nil
}
