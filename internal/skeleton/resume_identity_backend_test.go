package skeleton

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

const (
	resumeIdentityLocal      = "resume-id-source"
	resumeIdentityCodexID    = "01a00339-a80e-72a0-966f-116427b6b9ce"
	resumeIdentityOtherCodex = "01a00999-dead-beef-0000-000000000000"
)

type resumeIdentityBackendRig struct {
	stateDir string
	cfg      daemon.Config
	core     *daemon.Daemon
	sk       *Daemon
}

func newResumeIdentityBackendRig(t *testing.T) *resumeIdentityBackendRig {
	t.Helper()
	// Keep the Unix socket path below sun_path's platform limit even when the test name is
	// long. t.TempDir inherits that name and can exceed the limit before daemon.Open binds.
	stateDir, err := os.MkdirTemp("/tmp", "swri-")
	if err != nil {
		t.Fatalf("state dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(stateDir) })
	store, err := persist.NewStore(stateDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	now := time.Now().UTC()
	if err := store.Save(persist.Meta{
		ID:           resumeIdentityLocal,
		AgentType:    "codex",
		Cwd:          t.TempDir(),
		CreatedAt:    now,
		LastActivity: now,
		Status:       status.Status{Process: status.ProcessExited},
	}); err != nil {
		t.Fatalf("seed ended Codex meta: %v", err)
	}
	cfg := daemon.Config{
		StateDir:    stateDir,
		SocketPath:  filepath.Join(stateDir, "daemon.sock"),
		LockPath:    filepath.Join(stateDir, "daemon.lock"),
		LogPath:     filepath.Join(stateDir, "daemon.log"),
		MaxSessions: 16,
	}
	r := &resumeIdentityBackendRig{stateDir: stateDir, cfg: cfg}
	r.open(t)
	t.Cleanup(func() {
		if r.core != nil {
			_ = r.core.Close()
		}
	})
	return r
}

func (r *resumeIdentityBackendRig) open(t *testing.T) {
	t.Helper()
	core, err := daemon.Open(r.cfg)
	if err != nil {
		t.Fatalf("daemon.Open: %v", err)
	}
	r.core = core
	r.sk = &Daemon{core: core}
}

func (r *resumeIdentityBackendRig) restart(t *testing.T) {
	t.Helper()
	if err := r.core.Close(); err != nil {
		t.Fatalf("close before restart: %v", err)
	}
	r.core = nil
	r.sk = nil
	r.open(t)
}

func (r *resumeIdentityBackendRig) meta(t *testing.T) persist.Meta {
	t.Helper()
	m, ok := r.core.Get(resumeIdentityLocal)
	if !ok {
		t.Fatalf("source %q disappeared", resumeIdentityLocal)
	}
	return m
}

// TestResumeIdentity_ThreadStartedPersistsAcrossDaemonRestart is the forward-capture RED
// test for ordinary Codex sessions. The app-server's thread/started notification is already
// the authoritative adoption point; the accepted id must also cross the daemon's durable
// SetConversationID seam before the notification returns.
func TestResumeIdentity_ThreadStartedPersistsAcrossDaemonRestart(t *testing.T) {
	r := newResumeIdentityBackendRig(t)
	frame := []byte(fmt.Sprintf(
		`{"method":"thread/started","params":{"thread":{"id":%q}}}`,
		resumeIdentityCodexID,
	))

	r.sk.ingestBackendFrame(resumeIdentityLocal, frame, time.Now().UnixMilli())
	if got := r.meta(t).ConversationID; got != resumeIdentityCodexID {
		t.Fatalf("ConversationID after thread/started = %q, want %q", got, resumeIdentityCodexID)
	}

	r.restart(t)
	if got := r.meta(t).ConversationID; got != resumeIdentityCodexID {
		t.Fatalf("ConversationID after daemon restart = %q, want %q", got, resumeIdentityCodexID)
	}
}

// TestResumeIdentity_RejoinAdoptionUsesTheSameDurableSeam pins the second producer routed
// through adoptBackendThread: discoverLoadedThread on daemon rejoin. Calling the shared seam
// directly keeps this unit test independent of sockets while still proving the persistence
// obligation both producers rely on.
func TestResumeIdentity_RejoinAdoptionUsesTheSameDurableSeam(t *testing.T) {
	r := newResumeIdentityBackendRig(t)
	r.sk.adoptBackendThread(resumeIdentityLocal, resumeIdentityCodexID)

	if got := r.meta(t).ConversationID; got != resumeIdentityCodexID {
		t.Fatalf("ConversationID after rejoin adoption = %q, want %q", got, resumeIdentityCodexID)
	}
}

// TestResumeIdentity_SameIDRetriesPersistenceButDifferentIDCannotRepoint makes the lock/I/O
// split observable. A failed metadata write must not make the in-memory first-wins decision
// flap: a different id stays refused, while a repeated notification carrying the SAME id is
// a persistence retry and eventually makes the accepted identity durable.
func TestResumeIdentity_SameIDRetriesPersistenceButDifferentIDCannotRepoint(t *testing.T) {
	r := newResumeIdentityBackendRig(t)
	sessionDir := filepath.Join(r.stateDir, resumeIdentityLocal)
	holdDir := sessionDir + ".hold"
	if err := os.Rename(sessionDir, holdDir); err != nil {
		t.Fatalf("move session directory aside: %v", err)
	}
	if err := os.WriteFile(sessionDir, []byte("blocks metadata directory creation"), 0o600); err != nil {
		t.Fatalf("install deterministic metadata write blocker: %v", err)
	}
	restored := false
	restore := func() {
		if restored {
			return
		}
		_ = os.Remove(sessionDir)
		_ = os.Rename(holdDir, sessionDir)
	}
	t.Cleanup(restore)

	r.sk.adoptBackendThread(resumeIdentityLocal, resumeIdentityCodexID)
	if got := r.meta(t).ConversationID; got != "" {
		t.Fatalf("failed persistence unexpectedly stored ConversationID %q", got)
	}

	if err := os.Remove(sessionDir); err != nil {
		t.Fatalf("remove metadata write blocker: %v", err)
	}
	if err := os.Rename(holdDir, sessionDir); err != nil {
		t.Fatalf("restore session directory: %v", err)
	}
	restored = true
	r.sk.adoptBackendThread(resumeIdentityLocal, resumeIdentityOtherCodex)
	if got := r.meta(t).ConversationID; got != "" {
		t.Fatalf("different id bypassed first-wins after write failure: %q", got)
	}

	r.sk.adoptBackendThread(resumeIdentityLocal, resumeIdentityCodexID)
	if got := r.meta(t).ConversationID; got != resumeIdentityCodexID {
		t.Fatalf("same-id retry stored %q, want %q", got, resumeIdentityCodexID)
	}
	if got, ok := r.sk.adoptedThread(resumeIdentityLocal); !ok || got != resumeIdentityCodexID {
		t.Fatalf("adopted thread after retry = %q (ok=%v), want first id %q", got, ok, resumeIdentityCodexID)
	}
}

// TestResumeIdentity_ExistingConversationIDRemainsWriteOnce protects a source already fixed by
// hook capture or lazy migration. Backend adoption may populate its live routing map, but it
// must never replace the provider identity already persisted for resume.
func TestResumeIdentity_ExistingConversationIDRemainsWriteOnce(t *testing.T) {
	r := newResumeIdentityBackendRig(t)
	const preexisting = "01a00000-1111-7222-8333-444444444444"
	if err := r.core.SetConversationID(resumeIdentityLocal, preexisting); err != nil {
		t.Fatalf("seed ConversationID: %v", err)
	}

	r.sk.adoptBackendThread(resumeIdentityLocal, resumeIdentityCodexID)
	if got := r.meta(t).ConversationID; got != preexisting {
		t.Fatalf("backend adoption replaced write-once ConversationID with %q; want %q", got, preexisting)
	}
}

// TestResumeIdentity_BackendRejectsNonCanonicalThreadIDs protects the write-once field from a
// weak app-server parse. Validation belongs at adoptBackendThread so both producers -- direct
// rejoin discovery and thread/started -- share it before either memory or metadata changes.
func TestResumeIdentity_BackendRejectsNonCanonicalThreadIDs(t *testing.T) {
	invalid := []struct {
		name string
		id   string
	}{
		{"empty", ""},
		{"arbitrary token", "thread-42"},
		{"uppercase UUID", "01A00339-A80E-72A0-966F-116427B6B9CE"},
		{"truncated UUID", "01a00339-a80e-72a0-966f-116427b6b9c"},
		{"braced UUID", "{01a00339-a80e-72a0-966f-116427b6b9ce}"},
		{"UUID plus prose", "01a00339-a80e-72a0-966f-116427b6b9ce copied"},
	}
	for _, tc := range invalid {
		t.Run(tc.name+" direct adoption", func(t *testing.T) {
			r := newResumeIdentityBackendRig(t)
			r.sk.adoptBackendThread(resumeIdentityLocal, tc.id)
			if got, ok := r.sk.adoptedThread(resumeIdentityLocal); ok || got != "" {
				t.Fatalf("invalid id %q entered adopted map as %q (ok=%v)", tc.id, got, ok)
			}
			if got := r.meta(t).ConversationID; got != "" {
				t.Fatalf("invalid id %q persisted as %q", tc.id, got)
			}
		})
		t.Run(tc.name+" thread started", func(t *testing.T) {
			r := newResumeIdentityBackendRig(t)
			frame := []byte(fmt.Sprintf(`{"method":"thread/started","params":{"thread":{"id":%q}}}`, tc.id))
			r.sk.ingestBackendFrame(resumeIdentityLocal, frame, time.Now().UnixMilli())
			if got, ok := r.sk.adoptedThread(resumeIdentityLocal); ok || got != "" {
				t.Fatalf("thread/started invalid id %q entered adopted map as %q (ok=%v)", tc.id, got, ok)
			}
			if got := r.meta(t).ConversationID; got != "" {
				t.Fatalf("thread/started invalid id %q persisted as %q", tc.id, got)
			}
		})
	}
}

func TestResumeIdentity_ThreadStartedRejectsDuplicateIdentityKeys(t *testing.T) {
	r := newResumeIdentityBackendRig(t)
	frame := []byte(`{"method":"thread/started","params":{"thread":{"id":"thread-decoy","id":"` + resumeIdentityCodexID + `"}}}`)
	r.sk.ingestBackendFrame(resumeIdentityLocal, frame, time.Now().UnixMilli())
	if got, ok := r.sk.adoptedThread(resumeIdentityLocal); ok || got != "" {
		t.Fatalf("duplicate id keys entered adopted map as %q (ok=%v)", got, ok)
	}
	if got := r.meta(t).ConversationID; got != "" {
		t.Fatalf("duplicate id keys persisted ConversationID %q", got)
	}
}

// TestResumeIdentity_DiscoverLoadedThreadRejectsNonCanonicalIDs pins the rejoin producer at
// its earliest structured boundary. Returning an arbitrary thread/loaded/list string lets the
// caller send it through thread/resume and backend registration before adoptBackendThread gets
// a chance to reject persistence. Only a canonical lowercase UUID may leave discovery.
func TestResumeIdentity_DiscoverLoadedThreadRejectsNonCanonicalIDs(t *testing.T) {
	for _, tc := range []struct {
		name    string
		id      string
		wantErr bool
	}{
		{"canonical lowercase UUID", resumeIdentityCodexID, false},
		{"empty", "", true},
		{"arbitrary token", "thread-42", true},
		{"uppercase UUID", "01A00339-A80E-72A0-966F-116427B6B9CE", true},
		{"truncated UUID", "01a00339-a80e-72a0-966f-116427b6b9c", true},
		{"braced UUID", "{01a00339-a80e-72a0-966f-116427b6b9ce}", true},
		{"UUID plus prose", resumeIdentityCodexID + " copied", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := newR7FakeBackend()
			conn.reply["thread/loaded/list"] = []byte(fmt.Sprintf(`{"data":[%q]}`, tc.id))
			got, err := (&Daemon{}).discoverLoadedThread(conn)
			if tc.wantErr {
				if err == nil || got != "" {
					t.Fatalf("discoverLoadedThread(%q) = (%q, %v), want sanitized rejection", tc.id, got, err)
				}
				return
			}
			if err != nil || got != tc.id {
				t.Fatalf("discoverLoadedThread(%q) = (%q, %v), want canonical id", tc.id, got, err)
			}
		})
	}
}
