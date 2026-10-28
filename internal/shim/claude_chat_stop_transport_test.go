package shim

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/procstart"
	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/wire"
)

type claudeNativeStopBinder interface {
	bindClaudeChatStop(Config, *managedNativeScope) error
}

// Reuses the reservation and pipe fixtures; no child or native authority is invented.
func bindClaudeStopTest(t *testing.T, srv *server) (Config, *managedNativeScope, map[string]any) {
	t.Helper()
	cfg, scope, meta := claudeClaimFixture(t)
	cfg.RequiredRuntime, cfg.ConversationID = persist.RequiredRuntimeClaudeChat, claudeChatTestUUID
	meta.SchemaVersion, meta.RequiredRuntime, meta.ConversationID = persist.ClaudeChatSchemaVersion, cfg.RequiredRuntime, cfg.ConversationID
	raw, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(cfg.SessionDir, "meta.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	binder, ok := any(srv).(claudeNativeStopBinder)
	if !ok {
		t.Fatal("APIabsence: managed Claude has no immutable Stop binding")
	}
	if err := binder.bindClaudeChatStop(cfg, scope); err != nil {
		t.Fatal(err)
	}
	binding := claudeStopTestBinding(cfg, scope)
	return cfg, scope, binding
}

func claudeStopTestBinding(cfg Config, scope *managedNativeScope) map[string]any {
	return map[string]any{"v": 1, "schema_version": 6, "session_id": cfg.SessionID, "required_runtime": cfg.RequiredRuntime, "native_session_id": cfg.ConversationID, "incarnation": fmt.Sprintf("%d:%d", scope.info.ShimPID, scope.info.ShimStartTime), "runtime": *cfg.LoginRuntime, "cwd": cfg.Cwd, "cli_identity": *cfg.CLIIdentity, "shim_pid": scope.info.ShimPID, "shim_start_time": scope.info.ShimStartTime, "scope_generation": scope.info.Generation}
}

func claudeStopExchange(t *testing.T, srv *server, binding map[string]any, generic bool) map[string]json.RawMessage {
	t.Helper()
	a, b := net.Pipe()
	defer func() { _ = a.Close() }()
	_ = a.SetDeadline(time.Now().Add(time.Second))
	done := make(chan struct{})
	go func() { defer close(done); srv.serveConn(b) }()
	defer func() { _ = a.Close(); <-done }()
	send := func(value any) {
		raw, e := json.Marshal(value)
		if e != nil {
			t.Fatal(e)
		}
		if e = wire.WriteFrame(a, wire.TControl, raw); e != nil {
			t.Fatal(e)
		}
	}
	read := func() map[string]json.RawMessage {
		typ, raw, e := wire.ReadFrame(a)
		if e != nil || typ != wire.TControl {
			t.Fatalf("Stop control reply: %v", e)
		}
		var reply map[string]json.RawMessage
		if json.Unmarshal(raw, &reply) != nil {
			t.Fatal("invalid reply")
		}
		return reply
	}
	send(map[string]any{"type": "hello", "wire_version": shimwire.Version, "claude_stop_v1": true})
	hello := read()
	if string(hello["claude_stop_v1"]) != "true" || len(hello["claude_stop_binding"]) == 0 || len(hello["claude_primary"]) != 0 {
		t.Fatal("Stop hello requires snapshot-free retained binding", stringMustJSON(hello))
	}
	if generic {
		send(map[string]any{"type": "signal", "sig": "term"})
		_, _, _ = wire.ReadFrame(a)
		return nil
	}
	send(map[string]any{"type": "claude_stop", "claude_stop": map[string]any{"v": 1, "binding": binding}})
	return read()
}
func stringMustJSON(v any) string { raw, _ := json.Marshal(v); return string(raw) }

func TestClaudeNativeStopHelloAndLatchBypassCaptureLocks(t *testing.T) {
	f := claudeA2PipeFixture(t)
	_, _, binding := bindClaudeStopTest(t, f.c.server)
	f.c.server.claudePrimary.Store(f.c)
	f.c.admission.Lock()
	f.c.mu.Lock()
	started := time.Now()
	var reply map[string]json.RawMessage
	func() {
		defer f.c.mu.Unlock()
		defer f.c.admission.Unlock()
		reply = claudeStopExchange(t, f.c.server, binding, false)
	}()
	if time.Since(started) > time.Second {
		t.Fatal("Stop waited on capture/parser")
	}
	var result struct{ Status string }
	if json.Unmarshal(reply["claude_stop_result"], &result) != nil || result.Status != "delivered" {
		t.Fatal("exact Stop unconfirmed", stringMustJSON(reply))
	}
	f.c.server.pgidMu.Lock()
	pending := f.c.server.pendingSig
	f.c.server.pgidMu.Unlock()
	if pending != syscall.SIGTERM && pending != syscall.SIGKILL {
		t.Fatal("Stop did not latch before birth")
	}
	if err := f.initializer(t).initializeNative(); err == nil || len(f.available(t)) != 0 {
		t.Fatal("initialize wrote after Stop")
	}
}

func TestClaudeNativeStopRefusesEveryForeignBindingAndBareSignal(t *testing.T) {
	for _, kind := range []string{"session_id", "native_session_id", "incarnation", "runtime", "cwd", "cli_identity", "shim_pid", "shim_start_time", "scope_generation", "bare-signal"} {
		t.Run(kind, func(t *testing.T) {
			srv := newServer(nil, "", nil, nil, nil, 0, 20*time.Millisecond, &Metrics{})
			defer srv.finishEscalation()
			_, _, binding := bindClaudeStopTest(t, srv)
			if kind != "bare-signal" {
				switch kind {
				case "runtime", "cli_identity":
					binding[kind] = map[string]any{}
				case "shim_pid", "shim_start_time":
					binding[kind] = 1
				default:
					binding[kind] = "foreign"
				}
			}
			reply := claudeStopExchange(t, srv, binding, kind == "bare-signal")
			if kind != "bare-signal" {
				var result struct{ Status string }
				if json.Unmarshal(reply["claude_stop_result"], &result) != nil || result.Status != "unconfirmed" {
					t.Fatal("foreign Stop delivered", stringMustJSON(reply))
				}
			}
			srv.pgidMu.Lock()
			pending := srv.pendingSig
			srv.pgidMu.Unlock()
			if pending != 0 {
				t.Fatal("foreign request latched or signalled scope")
			}
		})
	}
}

func TestClaudeNativeStopBindingIsCopiedBeforeStoreAndCapture(t *testing.T) {
	srv := newServer(nil, "", nil, nil, nil, 0, time.Second, &Metrics{})
	defer srv.finishEscalation()
	cfg, scope, binding := bindClaudeStopTest(t, srv)
	// No durable preclaim or primary exists yet. Stop must use the retained copy.
	if scope.ownPreclaimDurable || scope.recorded {
		t.Fatal("fixture accidentally required Store custody")
	}
	cfg.LoginRuntime.Identity = "foreign"
	cfg.CLIIdentity.Version = "foreign"
	scope.info.Generation = "foreign"
	reply := claudeStopExchange(t, srv, binding, false)
	var result struct{ Status string }
	if json.Unmarshal(reply["claude_stop_result"], &result) != nil || result.Status != "delivered" {
		t.Fatal("mutable launch/capture state replaced retained Stop authority")
	}
}

func TestClaudeNativeStopContainsOwnedStdioWhilePublicationHeld(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("held-image stdio requires Linux")
	}
	f := newClaudeStdioFixture(t)
	f.cfg.RequiredRuntime, f.cfg.ConversationID = persist.RequiredRuntimeClaudeChat, claudeChatTestUUID
	raw, err := os.ReadFile(filepath.Join(f.cfg.SessionDir, "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta persist.Meta
	if json.Unmarshal(raw, &meta) != nil {
		t.Fatal("metadata")
	}
	meta.SchemaVersion, meta.RequiredRuntime, meta.ConversationID = 6, f.cfg.RequiredRuntime, f.cfg.ConversationID
	raw, _ = json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(f.cfg.SessionDir, "meta.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	binder, ok := any(f.srv).(claudeNativeStopBinder)
	if !ok {
		t.Fatal("APIabsence: managed Claude immutable Stop binding")
	}
	if err := binder.bindClaudeChatStop(f.cfg, f.scope); err != nil {
		t.Fatal(err)
	}
	binding := claudeStopTestBinding(f.cfg, f.scope)
	old := nativeRecordSync
	entered, release := make(chan struct{}), make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	nativeRecordSync = func(*os.Root) error { enterOnce.Do(func() { close(entered) }); <-release; return syscall.ENOSPC }
	done := make(chan error, 1)
	go func() { done <- f.birth.startStdio(f.scope, f.cfg, f.cmd, f.srv, nil) }()
	joined := false
	defer func() {
		releaseOnce.Do(func() { close(release) })
		if !joined {
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Error("Start did not join")
			}
		}
		nativeRecordSync = old
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("Start missed publication fence")
	}
	pid := f.cmd.Process.Pid
	birth, err := procstart.StartTime(pid)
	if err != nil {
		t.Fatal(err)
	}
	reply := claudeStopExchange(t, f.srv, binding, false)
	var result struct{ Status string }
	if json.Unmarshal(reply["claude_stop_result"], &result) != nil || result.Status != "delivered" {
		t.Fatal("Stop blocked on Store/publication")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		current, e := procstart.StartTime(pid)
		if e != nil || current != birth {
			break
		}
		state, e := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		end := strings.LastIndexByte(string(state), ')')
		if e == nil && end >= 0 && strings.HasPrefix(string(state[end+1:]), " Z ") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("exact child still executes while publication held")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-done:
		t.Fatal("publication unexpectedly released")
	default:
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-done:
		joined = true
		if err == nil {
			t.Fatal("failed publication succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("Start did not return")
	}
	_ = f.wait(t)
}

func TestClaudeNativeStopRejectsAliasedDuplicateAndUnnegotiatedRequest(t *testing.T) {
	for _, kind := range []string{"alias", "duplicate", "unknown", "unnegotiated"} {
		t.Run(kind, func(t *testing.T) {
			srv := newServer(nil, "", nil, nil, nil, 0, time.Second, &Metrics{})
			defer srv.finishEscalation()
			_, _, binding := bindClaudeStopTest(t, srv)
			raw, _ := json.Marshal(map[string]any{"type": "claude_stop", "claude_stop": map[string]any{"v": 1, "binding": binding}})
			ctrl, err := shimwire.Decode(raw)
			if err != nil || ctrl.ClaudeStop == nil {
				t.Fatal("typed request")
			}
			switch kind {
			case "alias":
				raw = []byte(strings.Replace(string(raw), `"scope_generation"`, `"Scope_Generation"`, 1))
			case "duplicate":
				raw = []byte(strings.Replace(string(raw), `"scope_generation":`, `"scope_generation":"foreign","scope_generation":`, 1))
			case "unknown":
				raw = []byte(strings.Replace(string(raw), `"scope_generation":`, `"extra":"ignored","scope_generation":`, 1))
			}
			result := srv.stopClaudeChat(ctrl.ClaudeStop, raw, kind != "unnegotiated")
			if result.Status != "unconfirmed" {
				t.Fatal("ambiguous Stop delivered")
			}
			srv.pgidMu.Lock()
			pending := srv.pendingSig
			srv.pgidMu.Unlock()
			if pending != 0 {
				t.Fatal("ambiguous Stop latched")
			}
		})
	}
}
