//go:build linux

package skeleton

import (
	"encoding/json"
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

func TestClaudeNativeStopRetainedHandleBypassesOwnerAndConsumerLocks(t *testing.T) {
	root, err := os.MkdirTemp("", "cstop-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(root) }()
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	const local = "native-stop"
	if err = os.Mkdir(filepath.Join(root, local), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, local, "shim.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	if err = os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	birth, err := procstart.StartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	identity := strings.Repeat("a", 64)
	rt := logins.Runtime{Provider: "claude", Epoch: 1, Identity: identity, Home: filepath.Join(root, "logins/provider/claude/profiles", identity)}
	meta := persist.Meta{SchemaVersion: 6, ID: local, AgentType: "claude", RequiredRuntime: "claude-chat", ConversationID: nativeA2Session, Cwd: root, LoginRuntime: &rt, CLIIdentity: &persist.CLIIdentity{Path: "/synthetic/claude", Version: "2.1.310", Fingerprint: strings.Repeat("b", 64)}, ShimPID: os.Getpid(), ShimStartTime: birth}
	binding := shimwire.ClaudeStopBinding{Version: 1, SchemaVersion: 6, SessionID: local, RequiredRuntime: "claude-chat", NativeSessionID: meta.ConversationID, Incarnation: fmt.Sprintf("%d:%d", os.Getpid(), birth), Runtime: rt, Cwd: root, CLIIdentity: *meta.CLIIdentity, ShimPID: os.Getpid(), ShimStartTime: birth, ScopeGeneration: strings.Repeat("c", 32)}
	var received atomic.Int32
	done := make(chan error, 1)
	go func() {
		for range 2 {
			c, e := listener.Accept()
			if e != nil {
				done <- e
				return
			}
			_ = c.SetDeadline(time.Now().Add(4 * time.Second))
			typ, body, e := wire.ReadFrame(c)
			hello, decodeErr := shimwire.Decode(body)
			if e != nil || decodeErr != nil || typ != wire.TControl || hello.Type != shimwire.TypeHello || !hello.ClaudeStopV1 {
				_ = c.Close()
				done <- fmt.Errorf("unexpected hello")
				return
			}
			reply, _ := shimwire.Encode(shimwire.Control{Type: shimwire.TypeHello, WireVersion: shimwire.Version, ClaudeStopV1: true, ClaudeStopBinding: &binding})
			if e = wire.WriteFrame(c, wire.TControl, reply); e != nil {
				_ = c.Close()
				done <- e
				return
			}
			_, body, e = wire.ReadFrame(c)
			if e == nil {
				var request shimwire.Control
				if json.Unmarshal(body, &request) != nil || request.Type != shimwire.TypeClaudeStop || request.ClaudeStop == nil || request.ClaudeStop.Binding != binding {
					_ = c.Close()
					done <- fmt.Errorf("wrong Stop request")
					return
				}
				received.Add(1)
				reply, _ = shimwire.Encode(shimwire.Control{Type: shimwire.TypeClaudeStopResult, ClaudeStopResult: &shimwire.ClaudeStopResult{Version: 1, Binding: binding, Status: "delivered"}})
				e = wire.WriteFrame(c, wire.TControl, reply)
			}
			_ = c.Close()
		}
		done <- nil
	}()
	d := &Daemon{stateDir: root, logins: &loginManager{}}
	d.rememberClaudeChatRuntime(meta)
	if err = d.retainClaudeNativeStop(meta, binding.ScopeGeneration); err != nil {
		t.Fatal(err)
	}
	lane := d.composerLaneFor(local)
	lane.mu.Lock()
	lane.endMu.Lock()
	d.itemMu.Lock()
	d.logins.stepMu.Lock()
	result := make(chan error, 1)
	go func() { result <- d.ownerKill(local) }()
	select {
	case err = <-result:
	case <-time.After(3 * time.Second):
		err = fmt.Errorf("Stop blocked behind consumer/owner locks")
	}
	d.logins.stepMu.Unlock()
	d.itemMu.Unlock()
	lane.endMu.Unlock()
	lane.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if received.Load() != 1 {
		t.Fatal("exact retained Stop was not delivered once")
	}
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Stop test server did not join")
	}
}
