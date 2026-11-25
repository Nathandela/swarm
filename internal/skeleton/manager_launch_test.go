package skeleton

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/status"
)

func TestManagerLaunchCompositionBindsTrustedBridgeForBothNativeRuntimes(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		spec := daemon.LaunchSpec{AgentType: provider, Cwd: t.TempDir(), Manager: &daemon.ManagerLaunchSpec{ConversationID: "voice-conversation", BridgeSocket: "/private/service/bridge.sock", AuthFile: "/private/service/auth", Command: "/trusted/repo/swarm"}}
		got, err := composeLaunchSpec(spec, testEndpoint, "", srcGetter("", persist.Meta{}), stubLookPath)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.Join(got.Argv, "\n"), "/trusted/repo/swarm") {
			t.Fatalf("%s bridge executable lost: %q", provider, got.Argv)
		}
		if provider == "claude" && !slices.Contains(got.Argv, "--settings") {
			t.Fatal("native hooks lost")
		}
		source := endedCodexSource("source", nil)
		if provider == "claude" {
			source = endedClaudeSource("source")
		}
		source.Cwd = spec.Cwd
		source.Role = "manager"
		source.ManagerConversationID = "voice-conversation"
		spec.Options = map[string]string{protocol.OptionResumeFrom: testEndpoint + "/source"}
		if _, err := composeLaunchSpec(spec, testEndpoint, "", srcGetter(source.ID, source), stubLookPath); err != nil {
			t.Fatal(err)
		}
		spec.Manager = nil
		if _, err := composeLaunchSpec(spec, testEndpoint, "", srcGetter(source.ID, source), stubLookPath); err == nil {
			t.Fatal("manager silently resumed without current bridge binding")
		}
	}
}

func TestFreshVMManagerUsesSameProviderDefaultsAsWorker(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		launch := daemon.LaunchSpec{AgentType: provider, Cwd: t.TempDir()}
		worker, err := composeLaunchSpec(launch, testEndpoint, "", srcGetter("", persist.Meta{}), stubLookPath)
		if err != nil {
			t.Fatal(err)
		}
		launch.Manager = &daemon.ManagerLaunchSpec{NativeCLI: true, ConversationID: "discord-teammate-thread", Command: "/trusted/swarm"}
		manager, err := composeLaunchSpec(launch, testEndpoint, "", srcGetter("", persist.Meta{}), stubLookPath)
		if err != nil {
			t.Fatal(err)
		}
		if !maps.Equal(worker.Options, manager.Options) || provider == "codex" && manager.Options["sandbox"] != "workspace-write" || slices.Contains(manager.Argv, "--dangerously-skip-permissions") {
			t.Fatalf("%s manager has different permissions: worker=%v manager=%v argv=%v", provider, worker.Options, manager.Options, manager.Argv)
		}
	}
}

func TestClaudeDiscordFilesUseOnlyVerifiedDiscussionScope(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("a", 64)
	ledger := filepath.Join(dir, "discussions", id)
	if err := os.MkdirAll(ledger, 0700); err != nil {
		t.Fatal(err)
	}
	auth := filepath.Join(ledger, schema.DiscussionBridgeAuthFile)
	if err := os.WriteFile(auth, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	attachments := filepath.Join(ledger, schema.DiscussionAttachmentsDir)
	if err := os.Mkdir(attachments, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "discussions", strings.Repeat("b", 64))
	if err := os.Symlink(ledger, alias); err != nil {
		t.Fatal(err)
	}
	manager := &daemon.ManagerLaunchSpec{NativeCLI: true, ConversationID: schema.DiscussionConversationPrefix + id, AuthFile: auth, Command: "/trusted/swarm"}
	parent := endedClaudeSource("parent")
	parent.Status.Process = status.ProcessRunning
	parent.Role, parent.ManagerNativeCLI = "manager", true
	parent.ManagerConversationID, parent.ManagerAuthFile = manager.ConversationID, auth
	check := func(spec daemon.LaunchSpec, source persist.Meta, want bool) {
		t.Helper()
		spec.AgentType, spec.Cwd = "claude", dir
		out, err := composeLaunchSpec(spec, testEndpoint, "", srcGetter(source.ID, source), stubLookPath)
		if err != nil {
			t.Fatal(err)
		}
		if have := strings.Contains(strings.Join(out.Argv, "\n"), "additionalDirectories"); have != want {
			t.Fatalf("attachment permission present=%v want=%v: %q", have, want, out.Argv)
		}
		if out.Cwd != dir || want && !strings.Contains(strings.Join(out.Argv, "\n"), attachments) {
			t.Fatalf("launch cwd or scoped path changed: %q %q", out.Cwd, out.Argv)
		}
	}
	check(daemon.LaunchSpec{Manager: manager}, persist.Meta{}, true)
	ended := parent
	ended.Status.Process = status.ProcessExited
	check(daemon.LaunchSpec{Manager: manager, Options: map[string]string{protocol.OptionResumeFrom: testEndpoint + "/" + ended.ID}}, ended, true)
	child := daemon.LaunchSpec{SpawnedFrom: parent.ID, SpawnIntent: schema.SpawnIntentDelegate, DispatcherProof: "fixture-proof", OwnerLaunchHash: "fixture-operation"}
	check(child, parent, true)
	check(child, ended, false)
	aliased := parent
	aliased.ManagerConversationID = schema.DiscussionConversationPrefix + filepath.Base(alias)
	aliased.ManagerAuthFile = filepath.Join(alias, schema.DiscussionBridgeAuthFile)
	check(child, aliased, false)
	foreign := parent
	foreign.ManagerConversationID = schema.DiscussionConversationPrefix + strings.Repeat("b", 64)
	check(child, foreign, false)
	unproven := child
	unproven.DispatcherProof = ""
	check(unproven, parent, false)
	check(daemon.LaunchSpec{}, parent, false)
}
