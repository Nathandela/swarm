package teammatemanager

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/discord"
	"github.com/Nathandela/swarm/internal/voice"
	"github.com/Nathandela/swarm/internal/workermail"
)

func TestDiscussionSupervisorIsPinnedAcrossCutover(t *testing.T) {
	r := Runtime{cfg: voice.Config{DaemonSocket: "/home.sock"}, endpoint: "home", managerSettingsDir: "/home/settings", previous: &discord.PreviousSupervisor{SupervisorPaths: discord.SupervisorPaths{StateDir: "/old/settings"}, Endpoint: "old", Socket: "/old.sock"}}
	for _, mark := range []string{"", "/old.sock", "/home.sock", "/foreign.sock"} {
		endpoint, socket, settings, err := r.supervisorForDiscussion(discord.TeammateDiscussion{SupervisorSocket: mark})
		switch mark {
		case "", "/old.sock":
			if err != nil || endpoint != "old" || socket != "/old.sock" || settings != "/old/settings" {
				t.Fatal("historic route moved", mark, err)
			}
		case "/home.sock":
			if err != nil || endpoint != "home" || socket != "/home.sock" || settings != "/home/settings" {
				t.Fatal("new route lost", err)
			}
		default:
			if !errors.Is(err, voice.ErrStale) {
				t.Fatal("unknown owner accepted")
			}
		}
	}
	r.previous = nil
	if _, _, _, err := r.supervisorForDiscussion(discord.TeammateDiscussion{SupervisorSocket: "/old.sock"}); !errors.Is(err, voice.ErrStale) {
		t.Fatal("discarded prior ownership")
	}
}

func TestVMProjectAccessRequiresExistingCanonicalDirectory(t *testing.T) {
	work := t.TempDir()
	other := t.TempDir()
	r := Runtime{cfg: voice.Config{WorkDir: work}, projects: map[string]string{"work": work}}
	if r.projectEnrolled(other) {
		t.Fatal("enrolled grant escaped")
	}
	r.vmProjectAccess = true
	alias := filepath.Join(work, "link")
	if err := os.Symlink(other, alias); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(work, "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if !r.projectEnrolled(other) {
		t.Fatal("canonical existing VM directory refused")
	}
	for _, path := range []string{"relative", filepath.Join(work, "absent"), file, alias, work + "/."} {
		if r.projectEnrolled(path) {
			t.Fatal("accepted invalid VM directory", path)
		}
	}
	if got := r.resolveProject(other, "continue", ""); got != other {
		t.Fatal("lost selected VM directory", got)
	}
	if got := r.resolveProject(other, "switch to work", ""); got != work {
		t.Fatal("lost named alias", got)
	}
}

func TestMailboxDiscoveryKeepsHealthySupervisorWhenHistoricalIndexUnavailable(t *testing.T) {
	current, old := t.TempDir(), t.TempDir()
	r := Runtime{vmProjectAccess: true, cfg: voice.Config{DaemonSocket: "/current.sock"}, endpoint: "current", managerSettingsDir: current, previous: &discord.PreviousSupervisor{SupervisorPaths: discord.SupervisorPaths{StateDir: old}, Endpoint: "old", Socket: "/old.sock"}}
	b := discord.TeammateDiscussion{ID: "thread", InstallationID: "place", SupervisorSocket: "/current.sock", Route: discord.Route{ChannelID: "1"}}
	binding := workermail.Binding{Discussion: voice.DiscussionConversationPrefix + r.mailDiscussionID(b, "current"), Session: "current/child", Instance: "i", Attempt: "a", Parent: "p"}
	if _, err := workermail.Submit(current, binding, "note", "complete", "done", true); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(old, "dispatcher-mail.db"), 0700); err != nil {
		t.Fatal(err)
	}
	match, err := r.PendingWorkerDiscussions()
	if err == nil || match == nil || !match(b) {
		t.Fatalf("healthy discussion lost when old supervisor unavailable: %v", err)
	}
}

func TestTicketDiscussionUsesExistingSupervisorAndKeepsItsRoute(t *testing.T) {
	r := Runtime{
		cfg:      voice.Config{DaemonSocket: "/claudio.sock", Provider: "claude"},
		endpoint: "claudio", managerSettingsDir: "/claudio", ticketProject: "fii",
	}
	ticket := discord.TeammateDiscussion{
		SupervisorSocket: r.cfg.DaemonSocket, TicketEventID: "event", TicketProject: "fii", TicketManagerCLI: "claude",
	}
	checkRoute := func() {
		t.Helper()
		endpoint, socket, dir, err := r.supervisorForDiscussion(ticket)
		if err != nil || endpoint != "claudio" || socket != "/claudio.sock" || dir != "/claudio" {
			t.Fatalf("ticket lost its dispatcher: %s %s %s %v", endpoint, socket, dir, err)
		}
	}
	checkRoute()
	r.previous = &discord.PreviousSupervisor{
		SupervisorPaths: discord.SupervisorPaths{StateDir: r.managerSettingsDir},
		Endpoint:        r.endpoint, Socket: r.cfg.DaemonSocket,
	}
	r.endpoint, r.cfg.DaemonSocket, r.managerSettingsDir = "new", "/new.sock", "/new"
	checkRoute()
	for _, change := range []func(*discord.TeammateDiscussion){
		func(b *discord.TeammateDiscussion) { b.SupervisorSocket = "" },
		func(b *discord.TeammateDiscussion) { b.SupervisorSocket = "/other.sock" },
		func(b *discord.TeammateDiscussion) { b.TicketManagerCLI = "codex" },
		func(b *discord.TeammateDiscussion) { b.TicketProject = "other" },
	} {
		invalid := ticket
		change(&invalid)
		if _, _, _, err := r.supervisorForDiscussion(invalid); !errors.Is(err, voice.ErrStale) {
			t.Fatalf("accepted a changed ticket binding %+v: %v", invalid, err)
		}
	}
	r.ticketProject = ""
	if _, _, _, err := r.supervisorForDiscussion(ticket); !errors.Is(err, voice.ErrStale) {
		t.Fatalf("accepted a ticket after unenrollment: %v", err)
	}
}

func TestTicketManagerPromptRetainsScopedToolsAlongsideVMMode(t *testing.T) {
	r := Runtime{vmProjectAccess: true, projects: map[string]string{"fii": "/ticket/fii", "other": "/shared/other"}}
	ticket := discord.TeammateAdmission{Discussion: discord.TeammateDiscussion{TicketEventID: "event", TicketManagerCLI: "codex"}}
	if prompt := r.prompt(ticket, discord.TeammateHistoryPage{}, "thread", nil); strings.Contains(prompt, "ordinary VM commands") ||
		strings.Contains(prompt, "project choices") || !strings.Contains(prompt, "scoped manager tools") ||
		!strings.Contains(prompt, "Use codex for the assigned ticket worker") {
		t.Fatalf("ticket manager got generic VM-mode tools: %s", prompt)
	}
	if prompt := r.prompt(discord.TeammateAdmission{}, discord.TeammateHistoryPage{}, "thread", nil); !strings.Contains(prompt, "ordinary VM commands") {
		t.Fatalf("ordinary manager lost VM mode: %s", prompt)
	}
}
