package hookclient

// E10.1 (G4): the hook transport. `swarm hook <event>` builds an engine.Callback
// from the per-invocation env injected at spawn (session id, token, daemon socket
// path, monotonic sequence) plus the event name and payload the hook wiring
// supplies, and posts it to the daemon socket. A round-trip through
// engine.HandleCallback authenticates it and applies the status.
//
// PIN: hookclient is a THIN poster. Post and Decode are an inverse pair (the wire
// encoding is theirs to choose); the round-trip asserts the end-to-end effect
// (status applied), not any specific bytes. Install is per-invocation: every
// callback carries the session's live token, read from env at spawn time.

import (
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/status"
)

// localRecorder captures engine Emit calls. The engine package's own test
// helpers are not importable across packages, so hookclient carries a minimal one.
type localRecorder struct {
	mu    sync.Mutex
	calls []status.Status
}

func (r *localRecorder) emit(_ string, s status.Status) {
	r.mu.Lock()
	r.calls = append(r.calls, s)
	r.mu.Unlock()
}

func (r *localRecorder) last() (status.Status, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) == 0 {
		return status.Status{}, false
	}
	return r.calls[len(r.calls)-1], true
}

// FromEnv composes a Callback from the injected env plus the event/payload args.
func TestFromEnvComposesCallback(t *testing.T) {
	env := map[string]string{
		EnvSessionID: "sess-1",
		EnvToken:     "tok-abc",
		EnvSocket:    "/run/swarm/daemon.sock",
		EnvSequence:  "7",
	}
	cb, err := FromEnv(func(k string) string { return env[k] }, "Stop", map[string]string{"turn": "idle"})
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if cb.SessionID != "sess-1" {
		t.Errorf("SessionID=%q, want sess-1", cb.SessionID)
	}
	if cb.Token != "tok-abc" {
		t.Errorf("Token=%q, want tok-abc", cb.Token)
	}
	if cb.Sequence != 7 {
		t.Errorf("Sequence=%d, want 7", cb.Sequence)
	}
	if cb.Event != "Stop" {
		t.Errorf("Event=%q, want Stop", cb.Event)
	}
	if cb.Payload["turn"] != "idle" {
		t.Errorf("Payload[turn]=%q, want idle", cb.Payload["turn"])
	}
}

// A hook invocation without its per-session token cannot compose a callback:
// FromEnv fails rather than emit a tokenless callback (S6, client side).
func TestFromEnvRejectsMissingToken(t *testing.T) {
	env := map[string]string{EnvSessionID: "sess-1", EnvSocket: "/x.sock", EnvSequence: "1"}
	if _, err := FromEnv(func(k string) string { return env[k] }, "Stop", nil); err == nil {
		t.Fatalf("FromEnv with no token: got nil error, want failure")
	}
}

// Full round-trip: FromEnv builds a callback, Post writes it to the daemon
// socket, the daemon-side reader Decodes it and feeds engine.HandleCallback,
// which authenticates and applies the status.
func TestPostRoundTripAppliesStatus(t *testing.T) {
	// Short socket dir: t.TempDir() embeds the test name and blows past macOS's
	// 104-byte sun_path limit, so bind under a short os.MkdirTemp prefix instead.
	sockDir, err := os.MkdirTemp("", "sw")
	if err != nil {
		t.Fatalf("mkdir temp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	rec := &localRecorder{}
	e := engine.New(engine.Config{
		Now:                time.Now,
		CPUSampler:         func(int) (float64, error) { return 0, nil },
		StalenessThreshold: time.Minute,
		PollInterval:       time.Second,
		Emit:               rec.emit,
	})
	e.RegisterSession("sess-1", "tok-abc", os.Getpid(), []adapter.SignalSource{{Kind: "hook"}})

	applied := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			applied <- err
			return
		}
		defer conn.Close()
		cb, err := Decode(conn)
		if err != nil {
			applied <- err
			return
		}
		applied <- e.HandleCallback(cb)
	}()

	env := map[string]string{
		EnvSessionID: "sess-1",
		EnvToken:     "tok-abc",
		EnvSocket:    sock,
		EnvSequence:  "1",
	}
	cb, err := FromEnv(func(k string) string { return env[k] }, "Stop", map[string]string{"turn": "idle"})
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if err := Post(sock, cb); err != nil {
		t.Fatalf("Post: %v", err)
	}

	select {
	case err := <-applied:
		if err != nil {
			t.Fatalf("daemon-side HandleCallback: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("daemon-side did not receive the posted callback")
	}

	got, ok := rec.last()
	if !ok {
		t.Fatalf("round-trip applied no status")
	}
	if got.Turn != status.TurnIdle {
		t.Fatalf("round-trip status turn=%s, want idle", got.Turn)
	}
}
