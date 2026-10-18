package daemon

import (
	"errors"
	"path/filepath"

	"github.com/Nathandela/swarm/internal/opencodebridge"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shim"
)

// SessionOpenCodeBridge returns private channel custody only for this exact durable generation.
func (d *Daemon) SessionOpenCodeBridge(id string) (opencodebridge.Binding, error) {
	m, ok := d.Get(id)
	if !ok || m.RequiredRuntime != persist.RequiredRuntimeOpenCode || m.OpenCodeBinding == nil {
		return opencodebridge.Binding{}, errors.New("OpenCode runtime: binding unavailable")
	}
	dir, err := filepath.EvalSymlinks(d.sessionDir(id))
	if err != nil {
		return opencodebridge.Binding{}, errors.New("OpenCode runtime: private directory unavailable")
	}
	b, err := opencodebridge.ReadBinding(filepath.Join(dir, shim.OpenCodeBindingFile))
	if err != nil {
		return opencodebridge.Binding{}, err
	}
	bound := m.OpenCodeBinding
	if b.WorkerID != m.ID || b.RootID != m.ConversationID || b.Directory != m.ProviderCwd() || b.BackendGeneration != bound.Generation || b.SessionInstance != bound.Generation || b.Mode != bound.Mode || b.InitialPrompt != (bound.Initial != nil) || b.NativeCreatedAtMS > 0 && b.NativeCreatedAtMS != bound.NativeCreatedAtMS {
		return opencodebridge.Binding{}, errors.New("OpenCode runtime: private and durable binding differ")
	}
	b.NativeCreatedAtMS = bound.NativeCreatedAtMS
	return b, nil
}
