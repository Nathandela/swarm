//go:build linux

package daemon

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/procstart"
	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/wire"
)

func claudeStopClientBinding(t *testing.T) shimwire.ClaudeStopBinding {
	t.Helper()
	start, err := procstart.StartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	identity := strings.Repeat("a", 64)
	return shimwire.ClaudeStopBinding{Version: 1, SchemaVersion: 6, SessionID: "native-stop", RequiredRuntime: "claude-chat", NativeSessionID: "1389ef09-4c19-4d50-8fdd-1fc95bdcfd4a", Incarnation: fmt.Sprintf("%d:%d", os.Getpid(), start), Runtime: logins.Runtime{Provider: "claude", Epoch: 1, Identity: identity, Home: "/tmp/stop-state/logins/provider/claude/profiles/" + identity}, Cwd: "/work", CLIIdentity: persist.CLIIdentity{Path: "/tmp/claude", Version: "2.1.310", Fingerprint: strings.Repeat("b", 64)}, ShimPID: os.Getpid(), ShimStartTime: start, ScopeGeneration: strings.Repeat("c", 32)}
}

// Existing wire framing over a real local socket exercises kernel peer proof.
// This synthetic server never signals anything and is not native qualification.
func claudeStopClientSocket(t *testing.T, hello shimwire.Control, delayHello, delayResult time.Duration) (string, *atomic.Int32) {
	t.Helper()
	dir, err := os.MkdirTemp("", "cstop-")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "s")
	listener, err := net.Listen("unix", path)
	if err != nil {
		_ = os.RemoveAll(dir)
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	received := &atomic.Int32{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		typ, raw, err := wire.ReadFrame(conn)
		if err != nil || typ != wire.TControl {
			return
		}
		req, err := shimwire.Decode(raw)
		if err != nil || req.Type != shimwire.TypeHello || !req.ClaudeStopV1 {
			return
		}
		time.Sleep(delayHello)
		raw, _ = shimwire.Encode(hello)
		if wire.WriteFrame(conn, wire.TControl, raw) != nil {
			return
		}
		typ, raw, err = wire.ReadFrame(conn)
		if err != nil || typ != wire.TControl {
			return
		}
		req, err = shimwire.Decode(raw)
		if err != nil || req.Type != shimwire.TypeClaudeStop || req.ClaudeStop == nil {
			return
		}
		received.Add(1)
		time.Sleep(delayResult)
		raw, _ = shimwire.Encode(shimwire.Control{Type: shimwire.TypeClaudeStopResult, ClaudeStopResult: &shimwire.ClaudeStopResult{Version: 1, Binding: req.ClaudeStop.Binding, Status: "delivered"}})
		_ = wire.WriteFrame(conn, wire.TControl, raw)
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		select {
		case <-done:
		case <-time.After(6 * time.Second):
			t.Error("Stop synthetic socket did not join")
		}
		_ = os.RemoveAll(dir)
	})
	return path, received
}

func TestClaudeNativeStopClientDiscoversExactBindingWithoutSnapshot(t *testing.T) {
	binding := claudeStopClientBinding(t)
	path, received := claudeStopClientSocket(t, shimwire.Control{Type: shimwire.TypeHello, WireVersion: shimwire.Version, ClaudeStopV1: true, ClaudeStopBinding: &binding}, 0, 0)
	expected := binding
	expected.ScopeGeneration = ""
	got, err := ReadClaudeNativeStopBinding(path, expected)
	if err != nil || got != binding || received.Load() != 0 {
		t.Fatal("immutable Stop discovery failed or sent signal", err)
	}
}

func TestClaudeNativeStopClientRejectsForeignPeerBirthAndBinding(t *testing.T) {
	for _, kind := range []string{"exact", "old-shim", "snapshot-only", "session", "conversation", "incarnation", "profile", "cwd", "cli", "generation", "pid", "birth"} {
		t.Run(kind, func(t *testing.T) {
			expected := claudeStopClientBinding(t)
			advertised := expected
			hello := shimwire.Control{Type: shimwire.TypeHello, WireVersion: shimwire.Version, ClaudeStopV1: true, ClaudeStopBinding: &advertised}
			switch kind {
			case "old-shim":
				hello.ClaudeStopV1 = false
				hello.ClaudeStopBinding = nil
			case "snapshot-only":
				hello.ClaudeStopV1 = false
				hello.ClaudeStopBinding = nil
				hello.ClaudePrimaryV1 = true
				hello.ClaudePrimary = &shimwire.ClaudePrimarySnapshot{}
			case "session":
				advertised.SessionID = "foreign"
			case "conversation":
				advertised.NativeSessionID = "2389ef09-4c19-4d50-8fdd-1fc95bdcfd4a"
			case "incarnation":
				advertised.Incarnation = "foreign"
			case "profile":
				advertised.Runtime.Identity = strings.Repeat("f", 64)
			case "cwd":
				advertised.Cwd = "/foreign"
			case "cli":
				advertised.CLIIdentity.Fingerprint = strings.Repeat("f", 64)
			case "generation":
				advertised.ScopeGeneration = strings.Repeat("f", 32)
			case "pid":
				expected.ShimPID++
			case "birth":
				expected.ShimStartTime++
			}
			path, received := claudeStopClientSocket(t, hello, 0, 0)
			err := StopClaudeNative(path, expected)
			if kind == "exact" {
				if err != nil || received.Load() != 1 {
					t.Fatal("exact Stop not delivered", err)
				}
			} else if err == nil || received.Load() != 0 {
				t.Fatal("foreign/old peer signalled or used fallback", err)
			}
		})
	}
}

func TestClaudeNativeStopClientOneAbsoluteDeadline(t *testing.T) {
	binding := claudeStopClientBinding(t)
	path, received := claudeStopClientSocket(t, shimwire.Control{Type: shimwire.TypeHello, WireVersion: shimwire.Version, ClaudeStopV1: true, ClaudeStopBinding: &binding}, 1800*time.Millisecond, 1800*time.Millisecond)
	started := time.Now()
	err := StopClaudeNative(path, binding)
	if err == nil || time.Since(started) > 3250*time.Millisecond || received.Load() != 1 {
		t.Fatal("Stop reset deadline after hello or claimed delivery without reply", err, time.Since(started))
	}
}
