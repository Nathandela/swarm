package skeleton

import (
	"context"
	"log"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/protocol"
)

// syncExistingSessionNameToProvider is used when a backend thread becomes
// routable. Empty names are intentionally skipped: they mean Swarm is using its
// display fallback, not that it should erase a provider-generated title.
func (d *Daemon) syncExistingSessionNameToProvider(local string) {
	if d.core == nil {
		return
	}
	m, ok := d.core.Get(local)
	if !ok || m.Name == "" {
		return
	}
	d.syncSessionNameToProvider(local, m.Name)
}

// syncSessionNameToProvider sends a best-effort native rename over the existing
// structured backend. It never types into the PTY and never changes the outcome
// of the already-durable Swarm rename.
func (d *Daemon) syncSessionNameToProvider(local, name string) {
	if d.core == nil {
		return
	}
	m, ok := d.core.Get(local)
	if !ok {
		return
	}
	ad, ok := d.resolveAdapter(m.AgentType)
	if !ok {
		return
	}
	syncer, ok := adapter.AsSessionNameSync(ad)
	if !ok {
		return
	}
	b, ok := d.sessionBackendFor(local)
	if !ok || b.conn == nil {
		return
	}
	req, ok := syncer.SetSessionName(b.threadID, name)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), backendCallTimeout)
	defer cancel()
	if err := b.conn.Call(ctx, req.Method, req.Params, nil); err != nil {
		log.Printf("skeleton: could not sync session name to provider for session %s: %v", local, err)
	}
}

// ingestProviderSessionName applies a provider-originated rename only when it
// names this session's live thread. It writes the core directly so the update is
// not echoed back through coreAPI's Swarm-originated rename callback.
func (d *Daemon) ingestProviderSessionName(local string, payload adapter.HookPayload) {
	if d.core == nil {
		return
	}
	m, ok := d.core.Get(local)
	if !ok {
		return
	}
	ad, ok := d.resolveAdapter(m.AgentType)
	if !ok {
		return
	}
	syncer, ok := adapter.AsSessionNameSync(ad)
	if !ok {
		return
	}
	threadID, name, ok := syncer.SessionNameFromEvent(payload)
	if !ok {
		return
	}
	b, live := d.sessionBackendFor(local)
	if !live || b.threadID != threadID {
		return
	}
	if err := d.core.Rename(local, protocol.SanitizeName(name)); err != nil {
		log.Printf("skeleton: could not persist provider session name for session %s: %v", local, err)
		return
	}
	if d.api != nil {
		d.api.pokeWatch()
	}
}
