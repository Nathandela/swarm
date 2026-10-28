package shim

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/procstart"
	"github.com/Nathandela/swarm/internal/shimwire"
)

// Validate once before Store/image preparation; requests use only this copy.
func (s *server) bindClaudeChatStop(cfg Config, scope *managedNativeScope) error {
	if s.claudeStopBinding != nil || cfg.RequiredRuntime != persist.RequiredRuntimeClaudeChat || scope == nil || scope.validateClaudeReservation(cfg) != nil {
		return errLoginResolution
	}
	generation, err := hex.DecodeString(scope.info.Generation)
	start, startErr := procstart.StartTime(os.Getpid())
	if err != nil || len(generation) != 16 || hex.EncodeToString(generation) != scope.info.Generation || startErr != nil || start != scope.info.ShimStartTime {
		return errLoginResolution
	}
	s.claudeStopBinding = &shimwire.ClaudeStopBinding{Version: 1, SchemaVersion: 6, SessionID: cfg.SessionID, RequiredRuntime: cfg.RequiredRuntime, NativeSessionID: cfg.ConversationID, Incarnation: fmt.Sprintf("%d:%d", scope.info.ShimPID, scope.info.ShimStartTime), Runtime: *cfg.LoginRuntime, Cwd: cfg.Cwd, CLIIdentity: *cfg.CLIIdentity, ShimPID: scope.info.ShimPID, ShimStartTime: scope.info.ShimStartTime, ScopeGeneration: scope.info.Generation}
	s.claudeChat = true
	s.managedSignals = scope.subreaper
	return nil
}

func startClaudeChatStopServer(cfg Config, scope *managedNativeScope) (*server, error) {
	srv := newServer(nil, cfg.SocketPath, nil, nil, nil, 0, cfg.GraceTimeout, cfg.Metrics)
	if err := srv.bindClaudeChatStop(cfg, scope); err != nil {
		return nil, err
	}
	listener, err := listen(cfg.SocketPath)
	if err != nil {
		return nil, err
	}
	srv.listener = listener
	srv.claudeStopDone = make(chan struct{})
	go func() { defer close(srv.claudeStopDone); srv.acceptLoop() }()
	return srv, nil
}

func (s *server) stopClaudeChatControl() {
	s.claudeStopClose.Do(func() { s.shutdown(shimwire.Control{}); <-s.claudeStopDone })
}
func (s *server) finishClaudeChatControl() {
	s.stopClaudeChatControl()
	s.claudeStopFinish.Do(s.finishEscalation)
}

func (s *server) stopClaudeChat(req *shimwire.ClaudeStopRequest, raw []byte, negotiated bool) shimwire.ClaudeStopResult {
	result := shimwire.ClaudeStopResult{Version: 1, Status: "unconfirmed"}
	if req != nil {
		result.Binding = req.Binding
	}
	// Compare the exact field tree as well as decoded values: JSON aliases,
	// unknown fields and duplicate keys cannot negotiate a different authority.
	var object map[string]json.RawMessage
	if !negotiated || req == nil || s.claudeStopBinding == nil || req.Version != 1 || req.Binding != *s.claudeStopBinding || !uniqueBirthJSON(raw) || json.Unmarshal(raw, &object) != nil || len(object) != 2 || string(object["type"]) != `"claude_stop"` || !sameBirthJSON(object["claude_stop"], req) {
		return result
	}
	s.onSignal(shimwire.SigTerm)
	result.Status = "delivered"
	return result
}
