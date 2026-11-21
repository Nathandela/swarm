package teammatemanager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/claudeinput"
	"github.com/Nathandela/swarm/internal/discord"
)

func privateTestSockets(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "swt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestDiscussionSocketsArePrivateShortStableAndIndependent(t *testing.T) {
	state := t.TempDir()
	socketRoot := privateTestSockets(t)
	for i := 0; i < 4; i++ {
		state = filepath.Join(state, "a-long-installation-directory")
	}
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := discord.Config{InstallationID: "install", Name: "Teammate", ManagerCLI: "codex", WorkDir: t.TempDir(), DaemonSocket: "/unavailable"}
	makeRuntime := func() *Runtime {
		r, err := New(context.Background(), state, "local", "", cfg)
		if err != nil {
			t.Fatal(err)
		}
		r.socketRoot = socketRoot
		return r
	}
	open := func(r *Runtime, thread string) string {
		d := discord.TeammateDiscussion{ID: thread, InstallationID: "install", Route: discord.Route{GuildID: "guild", ChannelID: thread}}
		entry, err := r.discussion(d, nil, false)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(entry.socket)
		if err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0600 || len(entry.socket) >= 104 {
			t.Fatalf("invalid socket %q: %v", entry.socket, err)
		}
		return entry.socket
	}
	r := makeRuntime()
	a, b := open(r, "thread-a"), open(r, "thread-b")
	if a == b {
		t.Fatal("discussion socket shared")
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	r = makeRuntime()
	t.Cleanup(func() { _ = r.Close() })
	if after := open(r, "thread-a"); after != a {
		t.Fatalf("restart changed socket from %q to %q", a, after)
	}
}

func TestDormantDiscussionClosesSocketAndSkipsIdleSync(t *testing.T) {
	dir := t.TempDir()
	cfg := discord.Config{InstallationID: "install", Name: "Teammate", ManagerCLI: "codex", WorkDir: t.TempDir(), DaemonSocket: "/unavailable"}
	r, err := New(context.Background(), dir, "local", "", cfg)
	if err != nil {
		t.Fatal(err)
	}
	r.socketRoot = privateTestSockets(t)
	t.Cleanup(func() { _ = r.Close() })
	thread := discord.TeammateDiscussion{ID: "thread", InstallationID: "install", Route: discord.Route{GuildID: "guild", ChannelID: "thread"}}
	entry, err := r.discussion(thread, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	r.release(entry)
	if _, err := os.Stat(entry.socket); !os.IsNotExist(err) {
		t.Fatalf("idle socket still open: %v", err)
	}
	if entry, err := r.discussion(thread, nil, true); err != nil || entry != nil {
		t.Fatalf("idle sync reopened discussion: %v %v", entry, err)
	}
	reopen, err := r.discussion(thread, nil, false)
	if err != nil || reopen.socket != entry.socket {
		t.Fatalf("new ingress did not reopen the original bridge: %v", err)
	}
	r.release(reopen)
}

func TestProjectSelectionIsRememberedAndSwitchesExplicitly(t *testing.T) {
	r := Runtime{projects: map[string]string{"web app": "/web", "service": "/service"}}
	for _, tc := range []struct{ selected, text, history, want string }{
		{"", "please check the web app build", "", "/web"},
		{"", "web app or service", "service", ""},
		{"/web", "could the service affect this?", "", "/web"},
		{"/web", "let us switch to service", "", "/service"},
		{"", "web application", "", ""},
		{"", "Thoughts?", "The web app is failing this ", "/web"},
		{"", "Thoughts?", "The web app and service both have a build failure", ""},
	} {
		if got := r.resolveProject(tc.selected, tc.text, tc.history); got != tc.want {
			t.Fatalf("%+v selected %q", tc, got)
		}
	}
}

func TestPersonalDiscussionPinnedToOneDMAndOwner(t *testing.T) {
	cfg := discord.Config{Mode: "personal", DMChannelID: "200", OwnerID: "100", InstallationID: "install", Name: "Personal", ManagerCLI: "codex", WorkDir: t.TempDir(), DaemonSocket: "/unavailable"}
	r, err := New(context.Background(), t.TempDir(), "local", "", cfg)
	if err != nil {
		t.Fatal(err)
	}
	r.socketRoot = privateTestSockets(t)
	t.Cleanup(func() { _ = r.Close() })
	d := discord.TeammateDiscussion{ID: "d", InstallationID: "install", Route: discord.Route{ChannelID: "200"}}
	if _, err := r.discussion(d, nil, false); err != nil {
		t.Fatalf("pinned DM: %v", err)
	}
	for _, route := range []discord.Route{{ChannelID: "201"}, {GuildID: "300", ChannelID: "200"}, {ChannelID: "200", ParentID: "300"}} {
		d.Route = route
		if _, err := r.discussion(d, nil, false); err == nil {
			t.Fatalf("accepted wrong destination %+v", route)
		}
	}
}

func TestDiscussionAcknowledgementFitsStandingInstructionsAndHumanText(t *testing.T) {
	r := &Runtime{projects: map[string]string{"fixture": "/work/fixture", "other": "/work/other"}}
	var page discord.TeammateHistoryPage
	for i := range 50 {
		page.Messages = append(page.Messages, discord.TeammateHistoryMessage{ContextMessage: discord.ContextMessage{ID: fmt.Sprint(i), Text: strings.Repeat("prior ", 20)}})
	}
	for _, size := range []int{990, 2000} {
		input := discord.TeammateAdmission{Route: discord.Route{MessageID: "100"}, Author: discord.Actor{ID: "42", Name: "Alice"}, Text: strings.Repeat("x", size)}
		body := r.prompt(input, page, "thread", nil)
		full := "Local manager starting instructions\n" + strings.Repeat("y", 1024) + "\n\nCurrent input\nApproved project for new work \"/work/fixture\"\n" + body
		if !strings.Contains(body, input.Text) || !strings.Contains(body, "final=false") || !strings.Contains(body, "yield when others are talking to each other") {
			t.Fatal("human text or acknowledgement guidance lost")
		}
		if _, err := claudeinput.ManagerEnvelope(strings.Repeat("a", 64), full); err != nil {
			t.Fatalf("message size %d exceeded native budget: %d bytes before envelope", size, len(full))
		}
	}
}
