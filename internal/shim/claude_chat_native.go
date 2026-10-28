package shim

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/wire"
)

// Immutable provenance exists before the primary PID, including early hooks.
// Opaque posted contents never become primary transport authority.
type claudeChatHookOrigin struct {
	identity claudeChatPrimaryIdentity
	scope    NativeProcessInfo
	lost     *atomic.Bool
	server   *server
	signalMu sync.Mutex
	retired  bool
}

func (h *hookServer) bindClaudeChatOrigin(identity claudeChatPrimaryIdentity, scope NativeProcessInfo, lost *atomic.Bool, srv *server) error {
	if h.claudeOrigin != nil || h.spool == nil || lost == nil || srv == nil || !persist.ValidID(identity.SessionID) || !adapter.IsCanonicalConversationID(identity.NativeSessionID) || scope.SchemaVersion != ClaudeNativeWriterSchemaVersion || scope.Provider != logins.ProviderClaude || identity.ShimPID != os.Getpid() || identity.ShimStartTime <= 0 || scope.PID != 0 || scope.PGID != 0 || scope.StartTime != 0 || scope.BackendPID != 0 || scope.BackendStartTime != 0 || identity.SessionID != scope.SessionID || identity.RequiredRuntime != persist.RequiredRuntimeClaudeChat || identity.ShimPID != scope.ShimPID || identity.ShimStartTime != scope.ShimStartTime || scope.Generation == "" || identity.PrimaryPID != 0 || identity.PrimaryStartTime != 0 {
		return errClaudeChatCapture
	}
	h.claudeOrigin = &claudeChatHookOrigin{identity: identity, scope: scope, lost: lost, server: srv}
	return nil
}

func (o *claudeChatHookOrigin) wrap(raw []byte) ([]byte, error) {
	body, err := json.Marshal(struct {
		Version int                       `json:"v"`
		Origin  string                    `json:"origin"`
		Primary claudeChatPrimaryIdentity `json:"primary"`
		Scope   NativeProcessInfo         `json:"scope"`
		Raw     []byte                    `json:"raw"`
	}{1, "claude_hook", o.identity, o.scope, raw})
	if err != nil || len(body) > wire.MaxFrame-1 {
		return nil, errClaudeChatCapture
	}
	return body, nil
}
func (o *claudeChatHookOrigin) fail() {
	o.lost.Store(true)
	o.signalMu.Lock()
	defer o.signalMu.Unlock()
	if !o.retired {
		o.server.onSignal(shimwire.SigKill)
	}
}
func (o *claudeChatHookOrigin) retire() { o.signalMu.Lock(); o.retired = true; o.signalMu.Unlock() }

// No production caller activates this entry until native ingress/Stop qualify.
// Prepared-object tests use the same lifetime, never a Config-provided factory.
func runClaudeChatManaged(cfg Config, signals <-chan os.Signal) (code int, runErr error) {
	if runtime.GOOS != "linux" || cfg.RequiredRuntime != persist.RequiredRuntimeClaudeChat || cfg.ClaudeChatFixture != "" || cfg.NativeProvider != logins.ProviderClaude || cfg.LoginRuntime == nil || cfg.LoginRuntime.ExternalAuth || !logins.IsClaudeProfileRuntime(cfg.LoginStateRoot, *cfg.LoginRuntime) || cfg.Backend != nil || cfg.OpenCode != nil || cfg.HermesInput != nil || cfg.AgyInput != nil || cfg.ClaudeInput {
		return 0, errLoginResolution
	}
	scope, err := beginManagedNativeScope(cfg)
	if err != nil {
		return 0, err
	}
	defer func() { runErr = errors.Join(runErr, scope.finish()) }()
	srv, err := startClaudeChatStopServer(cfg, scope)
	if err != nil {
		return 0, err
	}
	defer srv.finishClaudeChatControl()
	birth, err := prepareClaudeBirth(cfg, scope)
	if err != nil {
		return 0, err
	}
	defer birth.close()
	if birth == nil {
		return 0, errLoginResolution
	}
	if err := birth.preStartError(signals, resolveLoginEnvironment(&cfg)); err != nil {
		return 0, err
	}
	if err := birth.preStartError(signals, birth.prepareImage(cfg)); err != nil {
		return 0, err
	}
	return runClaudeChatLifetime(cfg, signals, birth, scope, srv)
}
func (b *claudeBirth) runClaudeChatStdio(cfg Config, scope *managedNativeScope, signals <-chan os.Signal) (int, error) {
	if b == nil || scope == nil || cfg.RequiredRuntime != persist.RequiredRuntimeClaudeChat || scope.validateClaudeStart(cfg) != nil {
		return 0, errLoginResolution
	}
	srv, err := startClaudeChatStopServer(cfg, scope)
	if err != nil {
		return 0, err
	}
	defer srv.finishClaudeChatControl()
	return runClaudeChatLifetime(cfg, signals, b, scope, srv)
}

// Fixture Start shares the pending-Stop arbitration of managed startStdio.
func (s *server) startClaudeChatPrimary(cmd *exec.Cmd, signals <-chan os.Signal) error {
	s.pgidMu.Lock()
	defer s.pgidMu.Unlock()
	if s.pendingSig != 0 {
		return context.Canceled
	}
	if err := startClaudeChatPrimary(cmd, signals); err != nil {
		return err
	}
	s.pgid = cmd.Process.Pid
	return nil
}
