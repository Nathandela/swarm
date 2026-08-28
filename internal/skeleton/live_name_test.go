package skeleton

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/claude"
	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/persist"
)

// ADR-022: the name a running Claude Code session shows in its own prompt box is
// adopted as the swarm session's name, newest wins. Claude publishes it in
// ~/.claude/sessions/<pid>.json; the assembly reads that registry on every
// authenticated hook callback for the session and applies a newer name through the
// core, the way ingestProviderSessionName applies a Codex rename.

const liveNameConv = "fff31caf-df8b-416f-8990-ecc58eb0dcaf"

// liveNameRig assembles a daemon whose trusted home is a temp dir, launches one
// fake-agent session that the claude adapter governs, and pins its conversation id.
func liveNameRig(t *testing.T) (*Daemon, persist.Meta, string) {
	t.Helper()
	home := t.TempDir()
	sk := assemble(t, func(c *Config) { c.historyHome = home })
	sk.adapterFor = func(string) (adapter.Adapter, bool) { return claude.New(), true }
	m := launchFake(t, sk, r7StdinScript)
	if err := sk.core.SetConversationID(m.ID, liveNameConv); err != nil {
		t.Fatalf("SetConversationID: %v", err)
	}
	m, _ = sk.core.Get(m.ID)
	return sk, m, home
}

// writeLiveName writes one registry file in the shape Claude Code 2.1.250 writes.
func writeLiveName(t *testing.T, home string, pid int, sessionID, name string, since time.Time) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	rec := map[string]any{"pid": pid, "sessionId": sessionID, "name": name, "nameSource": "user",
		"nameSince": since.UnixMilli(), "status": "busy", "kind": "interactive"}
	raw, _ := json.Marshal(rec)
	if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(pid)+".json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// hook delivers one authenticated Claude callback for the session, the trigger the
// adoption rides on. Stop is the turn-boundary event every session emits.
func hook(sk *Daemon, local string) {
	sk.serveHookInteractions(engine.Callback{SessionID: local, Event: "Stop",
		Raw: json.RawMessage(`{"session_id":"` + liveNameConv + `","hook_event_name":"Stop"}`)})
}

func TestLaunchStampsNameSetAt(t *testing.T) {
	_, m, _ := liveNameRig(t)
	if m.NameSetAt.IsZero() || m.NameSetAt.Before(m.CreatedAt) {
		t.Fatalf("NameSetAt = %v at launch (created %v); the newest-wins clock must start at launch", m.NameSetAt, m.CreatedAt)
	}
}

func TestClaudeRenameIsAdoptedWithItsOwnTimestamp(t *testing.T) {
	sk, m, home := liveNameRig(t)
	since := time.Now().Add(time.Second).Truncate(time.Millisecond)
	writeLiveName(t, home, 4242, liveNameConv, "renamed in claude", since)
	hook(sk, m.ID)
	got, _ := sk.core.Get(m.ID)
	if got.Name != "renamed in claude" {
		t.Fatalf("name = %q, want the name Claude published", got.Name)
	}
	if !got.NameSetAt.Equal(since) {
		t.Fatalf("NameSetAt = %v, want Claude's nameSince %v", got.NameSetAt, since)
	}
}

func TestSwarmsLaterRenameBeatsClaudesOlderName(t *testing.T) {
	sk, m, home := liveNameRig(t)
	writeLiveName(t, home, 4242, liveNameConv, "stale claude name", time.Now().Add(-time.Minute))
	if err := sk.core.Rename(m.ID, "renamed in swarm"); err != nil {
		t.Fatal(err)
	}
	hook(sk, m.ID)
	if got, _ := sk.core.Get(m.ID); got.Name != "renamed in swarm" {
		t.Fatalf("name = %q; an older Claude name overrode a newer swarm rename", got.Name)
	}
}

func TestNewestRegistryFileWinsAmongSeveral(t *testing.T) {
	sk, m, home := liveNameRig(t)
	base := time.Now().Add(time.Second)
	writeLiveName(t, home, 1, liveNameConv, "older", base)
	writeLiveName(t, home, 2, liveNameConv, "newest", base.Add(2*time.Second))
	writeLiveName(t, home, 3, liveNameConv, "middle", base.Add(time.Second))
	hook(sk, m.ID)
	if got, _ := sk.core.Get(m.ID); got.Name != "newest" {
		t.Fatalf("name = %q, want the newest of three registry files", got.Name)
	}
}

func TestAnotherSessionsRegistryFileIsIgnored(t *testing.T) {
	sk, m, home := liveNameRig(t)
	writeLiveName(t, home, 4242, "00000000-0000-4000-8000-000000000000", "someone else", time.Now().Add(time.Hour))
	hook(sk, m.ID)
	if got, _ := sk.core.Get(m.ID); got.Name != m.Name {
		t.Fatalf("name = %q, want %q untouched", got.Name, m.Name)
	}
}

func TestAdoptedNameIsSanitizedLikeEveryOtherRename(t *testing.T) {
	sk, m, home := liveNameRig(t)
	writeLiveName(t, home, 4242, liveNameConv, "line\nbreak\x1b[31m", time.Now().Add(time.Second))
	hook(sk, m.ID)
	if got, _ := sk.core.Get(m.ID); got.Name != "linebreak[31m" {
		t.Fatalf("name = %q, want control characters stripped", got.Name)
	}
}

func TestUnreadableRegistryFilesAreSkipped(t *testing.T) {
	sk, m, home := liveNameRig(t)
	dir := filepath.Join(home, ".claude", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	big := `{"sessionId":"` + liveNameConv + `","name":"` + strings.Repeat("x", 1<<20) + `","nameSince":` +
		strconv.FormatInt(time.Now().Add(time.Hour).UnixMilli(), 10) + `}`
	if err := os.WriteFile(filepath.Join(dir, "1.json"), []byte(big), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "2.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "3.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeLiveName(t, home, 4, liveNameConv, "the good one", time.Now().Add(time.Second))
	hook(sk, m.ID)
	if got, _ := sk.core.Get(m.ID); got.Name != "the good one" {
		t.Fatalf("name = %q, want the one readable file to win", got.Name)
	}
}

func TestNoConversationIDMeansNoLookup(t *testing.T) {
	home := t.TempDir()
	sk := assemble(t, func(c *Config) { c.historyHome = home })
	sk.adapterFor = func(string) (adapter.Adapter, bool) { return claude.New(), true }
	m := launchFake(t, sk, r7StdinScript)
	writeLiveName(t, home, 4242, liveNameConv, "unowned", time.Now().Add(time.Hour))
	// A callback whose body carries no session_id leaves the conversation id unset.
	sk.serveHookInteractions(engine.Callback{SessionID: m.ID, Event: "Stop", Raw: json.RawMessage(`{"hook_event_name":"Stop"}`)})
	if got, _ := sk.core.Get(m.ID); got.Name != m.Name {
		t.Fatalf("name = %q, want %q: nothing identifies which registry file is ours", got.Name, m.Name)
	}
}
