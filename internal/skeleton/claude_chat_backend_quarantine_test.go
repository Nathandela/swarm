package skeleton

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/wire"
)

// Count the actual production BackendAttach control, including dial-failure fallback.
func a2BackendGoAheadWitness(t *testing.T, path string) *atomic.Int64 {
	t.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	var count atomic.Int64
	var handlers sync.WaitGroup
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			handlers.Add(1)
			go func() {
				defer handlers.Done()
				defer func() { _ = conn.Close() }()
				_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
				if _, _, err := wire.ReadFrame(conn); err != nil {
					return
				}
				hello, _ := shimwire.Encode(shimwire.Control{Type: shimwire.TypeHello, WireVersion: shimwire.Version})
				if err := wire.WriteFrame(conn, wire.TControl, hello); err != nil {
					return
				}
				typ, body, err := wire.ReadFrame(conn)
				if err != nil || typ != wire.TControl {
					return
				}
				control, err := shimwire.Decode(body)
				if err == nil && control.Type == shimwire.TypeBackendAttach {
					count.Add(1)
				}
				_ = wire.WriteFrame(conn, wire.TControl, hello)
			}()
		}
	}()
	t.Cleanup(func() { _ = ln.Close(); <-done; handlers.Wait() })
	return &count
}

func a2BackendServer(t *testing.T, d *Daemon, id string) *r7r4Server {
	t.Helper()
	d.backendReady = 300 * time.Millisecond
	ad := &r7CodexAdapter{Adapter: newPlainAdapter().Adapter}
	d.setAdapterForTest(func(string) (adapter.Adapter, bool) { return ad, true })
	if err := d.core.SetConversationID(id, fakeCodexThreadID); err != nil {
		t.Fatal(err)
	}
	store, err := persist.NewStore(d.stateDir)
	if err != nil {
		t.Fatal(err)
	}
	disk, err := store.Load(id)
	memory, ok := d.core.Get(id)
	if err != nil || !ok || memory.ConversationID != fakeCodexThreadID || disk.ConversationID != fakeCodexThreadID {
		t.Fatalf("native conversation precondition failed: memory=%q disk=%q err=%v", memory.ConversationID, disk.ConversationID, err)
	}
	if _, ok := d.sessionInstance(id); !ok {
		if _, err := d.adoptOrMintSessionInstance(id, 0); err != nil {
			t.Fatal(err)
		}
	}
	srv := newR7R4Server(t)
	srv.mu.Lock()
	srv.announce, srv.suppressAnnouncement, srv.rolloutExists = fakeCodexThreadID, true, true
	srv.mu.Unlock()
	t.Cleanup(func() { d.forgetBackend(id); d.stopBackendIngress(); srv.die() })
	return srv
}

func TestClaudeChatDirectBackendAdmissionRefusesBeforeDialGoAheadAndRegistration(t *testing.T) {
	for _, method := range []string{"join", "rejoin", "dial"} {
		for _, chat := range []bool{false, true} {
			t.Run(method+map[bool]string{false: "/ordinary", true: "/chat"}[chat], func(t *testing.T) {
				f := a2NewQuarantineFixture(t, chat)
				srv := a2BackendServer(t, f.d, f.m.ID)
				goAhead := a2BackendGoAheadWitness(t, filepath.Join(f.d.stateDir, f.m.ID, "shim.sock"))
				channel := daemon.BackendChannel{SocketPath: srv.sock, AgentArgs: []string{"--remote", "unix://" + srv.sock}}
				switch method {
				case "join":
					f.d.joinSessionBackend(f.m.ID, channel)
				case "rejoin":
					f.d.rejoinSessionBackend(f.m.ID, channel)
				case "dial":
					instance, _ := f.d.sessionInstance(f.m.ID)
					conn, feed, err := f.d.dialSessionBackend(f.m.ID, instance, channel)
					if conn != nil {
						_ = conn.Close()
					}
					if feed != nil {
						feed.retireIngress()
					}
					if chat && err == nil {
						t.Error("direct dial accepted quarantined runtime")
					} else if !chat && err != nil {
						t.Fatalf("ordinary direct dial refused: %v", err)
					}
				}
				srv.mu.Lock()
				dials := len(srv.conns)
				srv.mu.Unlock()
				_, registered := f.d.sessionBackendFor(f.m.ID)
				if chat {
					if dials != 0 || srv.counted("initialize") != 0 || goAhead.Load() != 0 || registered {
						t.Errorf("quarantine crossed backend admission: dials=%d initialize=%d goAhead=%d registered=%t", dials, srv.counted("initialize"), goAhead.Load(), registered)
					}
				} else {
					wantGoAhead := int64(0)
					if method == "join" {
						wantGoAhead = 1
					}
					if dials != 1 || srv.counted("initialize") != 1 || goAhead.Load() != wantGoAhead || registered != (method != "dial") {
						t.Errorf("ordinary backend positive control failed: dials=%d initialize=%d goAhead=%d registered=%t", dials, srv.counted("initialize"), goAhead.Load(), registered)
					}
				}
			})
		}
	}
}

func TestClaudeChatRetainedBackendChannelCannotBypassCatchup(t *testing.T) {
	for _, method := range []string{"catchup", "connect"} {
		for _, chat := range []bool{false, true} {
			t.Run(method+map[bool]string{false: "/ordinary", true: "/chat"}[chat], func(t *testing.T) {
				var d *Daemon
				var id string
				var goAhead *atomic.Int64
				if method == "connect" {
					f := a2NewQuarantineFixture(t, chat)
					d, id = f.d, f.m.ID
					// Fresh-connect needs a shim still awaiting this exact go-ahead,
					// not an already-launched shim that ignores the second control.
					goAhead = a2BackendGoAheadWitness(t, filepath.Join(d.stateDir, id, "shim.sock"))
				} else if chat {
					live, m, _ := a2SurvivingQuarantine(t)
					d, id = live, m.ID
				} else {
					d = assemble(t)
					id = launchFake(t, d, "ask retained-backend-fixture\n").ID
				}
				srv := a2BackendServer(t, d, id)
				path := filepath.Join(d.stateDir, id, "shim-launch.json")
				raw, err := os.ReadFile(path)
				if os.IsNotExist(err) && method == "connect" {
					raw, err = []byte(`{}`), nil
				}
				if err != nil {
					t.Fatal(err)
				}
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(raw, &fields); err != nil {
					t.Fatal(err)
				}
				fields["backend_socket_path"], _ = json.Marshal(srv.sock)
				fields["backend_agent_args"], _ = json.Marshal([]string{"--remote", "unix://" + srv.sock})
				raw, err = json.Marshal(fields)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
				channel, ok := d.core.SessionBackend(id)
				row, present := d.core.Get(id)
				if !ok || channel.SocketPath != srv.sock || !present || method == "catchup" && row.Status.Process != status.ProcessRunning {
					t.Fatal("retained backend channel/phase precondition failed")
				}
				if method == "catchup" {
					d.connectBackendsForRunning()
				} else {
					d.connectSessionBackend(id)
				}
				// Async absence is bounded to the fixture's full join deadline; the
				// synchronous direct tests above fence each eventual worker as well.
				deadline := time.Now().Add(2 * d.backendDeadline())
				for time.Now().Before(deadline) {
					_, registered := d.sessionBackendFor(id)
					if registered {
						break
					}
					time.Sleep(2 * time.Millisecond)
				}
				srv.mu.Lock()
				dials := len(srv.conns)
				srv.mu.Unlock()
				_, registered := d.sessionBackendFor(id)
				var released int64
				if goAhead != nil {
					released = goAhead.Load()
				}
				if chat && (dials != 0 || srv.counted("initialize") != 0 || registered || released != 0) {
					t.Errorf("retained channel bypassed quarantine: dials=%d initialize=%d registered=%t goAhead=%d", dials, srv.counted("initialize"), registered, released)
				}
				if !chat && (dials != 1 || srv.counted("initialize") != 1 || !registered || method == "connect" && released != 1) {
					t.Errorf("ordinary retained backend control failed: dials=%d initialize=%d registered=%t goAhead=%d", dials, srv.counted("initialize"), registered, released)
				}
			})
		}
	}
}
