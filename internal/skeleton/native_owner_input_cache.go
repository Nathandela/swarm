package skeleton

import (
	"context"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

const (
	ownerInputHintLimit   = 256
	ownerInputHintQueue   = 64
	ownerInputHintTTL     = time.Second
	ownerInputHintRefresh = 250 * time.Millisecond
)

type ownerInputHint struct {
	caps      protocol.SessionCapabilities
	present   bool
	shimPID   int
	shimStart int64
	expires   time.Time
}
type ownerInputRefresh struct {
	local string
	token uint64
}

// These snapshots are optional routing hints, never input authority. Exactly one
// daemon worker performs bounded probes; roster/event fanout only reads memory.
type ownerInputHints struct {
	mu      sync.Mutex
	entries map[string]ownerInputHint
	pending map[string]uint64
	next    uint64
	closed  bool
	queue   chan ownerInputRefresh
	cancel  context.CancelFunc
	done    chan struct{}
	once    sync.Once
}

func (d *Daemon) startNativeOwnerHints(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	h := &ownerInputHints{entries: map[string]ownerInputHint{}, pending: map[string]uint64{}, queue: make(chan ownerInputRefresh, ownerInputHintQueue), cancel: cancel, done: make(chan struct{})}
	d.ownerInputHints = h
	go func() {
		defer close(h.done)
		for {
			select {
			case <-ctx.Done():
				return
			case req := <-h.queue:
				h.mu.Lock()
				current := !h.closed && h.pending[req.local] == req.token
				h.mu.Unlock()
				if !current {
					continue
				}
				hint := d.qualifyNativeOwnerHint(ctx, req.local)
				h.mu.Lock()
				if !h.closed && ctx.Err() == nil && h.pending[req.local] == req.token {
					delete(h.pending, req.local)
					if len(h.entries) >= ownerInputHintLimit {
						oldest := ""
						var before time.Time
						for id, entry := range h.entries {
							if oldest == "" || entry.expires.Before(before) {
								oldest = id
								before = entry.expires
							}
						}
						delete(h.entries, oldest)
					}
					hint.expires = time.Now().Add(ownerInputHintTTL)
					h.entries[req.local] = hint
				}
				h.mu.Unlock()
			}
		}
	}()
}
func (h *ownerInputHints) close() {
	if h == nil {
		return
	}
	h.once.Do(func() {
		h.mu.Lock()
		h.closed = true
		clear(h.entries)
		clear(h.pending)
		h.mu.Unlock()
		h.cancel()
		<-h.done
	})
}
func (h *ownerInputHints) invalidate(local string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	delete(h.entries, local)
	delete(h.pending, local)
	h.mu.Unlock()
}
func (h *ownerInputHints) snapshot(local string) (ownerInputHint, bool) {
	if h == nil {
		return ownerInputHint{}, false
	}
	now := time.Now()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return ownerInputHint{}, false
	}
	hint, known := h.entries[local]
	if (!known || time.Until(hint.expires) <= ownerInputHintRefresh) && h.pending[local] == 0 {
		h.next++
		req := ownerInputRefresh{local: local, token: h.next}
		select {
		case h.queue <- req:
			h.pending[local] = req.token
		default:
		}
	}
	return hint, known && hint.present && now.Before(hint.expires)
}
func (d *Daemon) ownerSessionCapabilities(local string, shimPID int, shimStart int64) (protocol.SessionCapabilities, bool) {
	hint, ok := d.ownerInputHints.snapshot(local)
	if !ok || hint.shimPID != shimPID || hint.shimStart != shimStart {
		return protocol.SessionCapabilities{}, false
	}
	return hint.caps, true
}
func (a *coreAPI) OwnerSessionCapabilities(local string, shimPID int, shimStart int64) (protocol.SessionCapabilities, bool) {
	if a.ownerSessionCaps == nil {
		return protocol.SessionCapabilities{}, false
	}
	return a.ownerSessionCaps(local, shimPID, shimStart)
}
func (d *Daemon) qualifyNativeOwnerHint(ctx context.Context, local string) ownerInputHint {
	var hint ownerInputHint
	if d.core == nil {
		return hint
	}
	meta, ok := d.core.Get(local)
	if !ok {
		return hint
	}
	caps, ok := d.sessionCapabilities(local)
	if !ok || caps.Validate() != nil {
		return hint
	}
	hint = ownerInputHint{caps: caps, present: true, shimPID: meta.ShimPID, shimStart: meta.ShimStartTime}
	hint.caps.NativeOwnerInput = d.probeNativeOwnerInput(ctx, local, caps.SessionInstance)
	instance, currentInstance := d.sessionInstance(local)
	if !currentInstance || instance != caps.SessionInstance {
		return ownerInputHint{}
	}
	current, ok := d.core.Get(local)
	if !ok || current.ShimPID != meta.ShimPID || current.ShimStartTime != meta.ShimStartTime {
		return ownerInputHint{}
	}
	if current.Status.Process != status.ProcessRunning || current.ResumeError != "" {
		hint.caps.NativeOwnerInput = false
	}
	return hint
}
