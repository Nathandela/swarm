// Tests for the two production adapters that turn real subsystems into the
// function-typed dependencies Run consumes: the on-disk session roster
// (SessionsFromStore) and the daemon hello (HelloVia).
package converge_test

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/Nathandela/swarm/internal/converge"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/version"
)

// SessionsFromStore must read the SAME meta.json the daemon writes, and carry
// through the id and the raw three-dimensional status so Run derives the group
// itself. A real persist.Store is used rather than a fixture: the point of the
// adapter is that it agrees with the store.
func TestSessionsFromStoreReadsRealMetaFromDisk(t *testing.T) {
	dir := t.TempDir()
	store, err := persist.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	written := []persist.Meta{
		{ID: "sess-working", AgentType: "claude", Status: status.Status{
			Process: status.ProcessRunning, Turn: status.TurnActive, Interaction: status.InteractionNone}},
		{ID: "sess-needsinput", AgentType: "claude", Status: status.Status{
			Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionPermission}},
		{ID: "sess-done", AgentType: "codex", Status: status.Status{
			Process: status.ProcessExited, Turn: status.TurnIdle, Interaction: status.InteractionNone}},
	}
	for _, m := range written {
		if err := store.Save(m); err != nil {
			t.Fatalf("Save %s: %v", m.ID, err)
		}
	}

	got, err := converge.SessionsFromStore(dir)()
	if err != nil {
		t.Fatalf("SessionsFromStore: %v", err)
	}
	if len(got) != len(written) {
		t.Fatalf("read %d sessions, want %d: %+v", len(got), len(written), got)
	}
	sort.Slice(got, func(i, j int) bool { return got[i].ID < got[j].ID })

	want := []struct {
		id    string
		group status.Group
	}{
		{"sess-done", status.GroupCompleted},
		{"sess-needsinput", status.GroupNeedsInput},
		{"sess-working", status.GroupWorking},
	}
	for i, w := range want {
		if got[i].ID != w.id {
			t.Errorf("session %d id = %q, want %q", i, got[i].ID, w.id)
		}
		if g := status.Derive(got[i].Status); g != w.group {
			t.Errorf("session %s derives to %q, want %q (status %+v)", got[i].ID, g, w.group, got[i].Status)
		}
	}
}

// An empty state dir is a legitimate roster of zero sessions, not an error:
// rule 2 must proceed, not defer, when the daemon holds the lock but has
// launched nothing.
func TestSessionsFromStoreOnAnEmptyDirReturnsNoSessions(t *testing.T) {
	got, err := converge.SessionsFromStore(t.TempDir())()
	if err != nil {
		t.Fatalf("SessionsFromStore on an empty dir: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("read %d sessions from an empty dir, want 0", len(got))
	}
}

// helloStub is the smallest protocol.DaemonAPI that can answer a hello: the
// handshake never touches a session op.
type helloStub struct{}

func (helloStub) List() []persist.Meta { return nil }
func (helloStub) Launch(daemon.LaunchSpec) (persist.Meta, error) {
	return persist.Meta{}, errors.New("helloStub: launch not implemented")
}
func (helloStub) Kill(string) error   { return errors.New("helloStub: kill not implemented") }
func (helloStub) Delete(string) error { return errors.New("helloStub: delete not implemented") }
func (helloStub) Rename(string, string) error {
	return errors.New("helloStub: rename not implemented")
}
func (helloStub) Attach(string) (protocol.SessionStream, error) {
	return nil, errors.New("helloStub: attach not implemented")
}
func (helloStub) Events() <-chan persist.Meta { return nil }

// tmpSock returns a short socket path: a unix socket path is capped near 104
// bytes, and the per-test temp dir is far too long on darwin. Mirrors
// internal/protocol's own test harness.
func tmpSock(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "swcv")
	if err != nil {
		t.Fatalf("mkdir sock dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "d.sock")
}

// HelloVia must report the build version the daemon answered with -- that value
// is what rule 1 compares against Deps.Version, so an adapter that returned the
// caller's own version would make the converge a permanent no-op.
func TestHelloViaReturnsTheServedBuildVersion(t *testing.T) {
	sock := tmpSock(t)
	srv, err := protocol.Serve(helloStub{}, sock)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	got, err := converge.HelloVia(sock)()
	if err != nil {
		t.Fatalf("HelloVia against a live daemon: %v", err)
	}
	if got != version.Version {
		t.Errorf("HelloVia = %q, want the served build version %q", got, version.Version)
	}
}

// A socket nothing is listening on is a dial failure, NOT a protocol bump: rule
// 1 must be able to tell the two apart, because one continues and one defers.
func TestHelloViaOnADeadSocketErrorsButNotAsAProtocolBump(t *testing.T) {
	_, err := converge.HelloVia(tmpSock(t))()
	if err == nil {
		t.Fatal("HelloVia against a socket nobody is listening on: err = nil, want an error")
	}
	if errors.Is(err, protocol.ErrIncompatibleVersion) {
		t.Errorf("HelloVia dial failure = %v, want an error that is NOT ErrIncompatibleVersion: "+
			"rule 1 would spawn against a wedged daemon", err)
	}
}
