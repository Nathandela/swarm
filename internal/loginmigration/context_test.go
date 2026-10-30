package loginmigration

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
)

func managedCodexMeta(f fixture, id, conversationID string) map[string]any {
	return map[string]any{"schema_version": persist.LoginSchemaVersion, "id": id, "agent_type": "codex", "conversation_id": conversationID, "cwd": f.project, "login_runtime": map[string]any{"provider": "codex", "epoch": 1, "identity": strings.Repeat("c", 64), "home": providerHome(f.state, "codex")}}
}

func TestPlanContextWithMarkerSkipsNativeBoundConversations(t *testing.T) {
	f := newFixture(t)
	managed := providerHome(f.state, "codex")
	rolloutFixture(t, managed, conversation, "", nil)
	put(t, filepath.Join(managed, contextMarker), "{}")
	put(t, filepath.Join(f.state, "managed-discussion", "meta.json"), managedCodexMeta(f, "managed-discussion", conversation))
	// Created after a return to native sign-in: its history lives only in the native home.
	rolloutFixture(t, f.home, otherConversation, "", nil)
	put(t, filepath.Join(f.state, f.id, "meta.json"), map[string]any{"schema_version": 1, "id": f.id, "agent_type": "codex", "conversation_id": otherConversation, "cwd": f.project, "env": []string{"CODEX_HOME=" + f.home}})
	if err := PreflightProviderContext(f.state, "codex", nil); err != nil {
		t.Fatalf("native-bound discussion refused reactivation: %v", err)
	}
	if err := PrepareProviderContext(f.state, "codex", nil); err != nil {
		t.Fatal(err)
	}
	if err := verifyConversations(managed, "codex", []contextSource{{Conversation: otherConversation}}); !errors.Is(err, ErrUnsupported) {
		t.Fatal("native-bound history entered the managed home")
	}
}

func TestPlanContextWithMarkerStillVerifiesManagedConversations(t *testing.T) {
	f := newFixture(t)
	put(t, filepath.Join(providerHome(f.state, "codex"), contextMarker), "{}")
	put(t, filepath.Join(f.state, f.id, "meta.json"), managedCodexMeta(f, f.id, conversation))
	if err := PreflightProviderContext(f.state, "codex", nil); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("managed-bound conversation missing from the managed home was accepted: %v", err)
	}
}

func TestContextImportedReportsMarker(t *testing.T) {
	f := newFixture(t)
	if ok, err := ContextImported(f.state, "codex"); ok || err != nil {
		t.Fatalf("absent marker reported %v, %v", ok, err)
	}
	put(t, filepath.Join(providerHome(f.state, "codex"), contextMarker), "{}")
	if ok, err := ContextImported(f.state, "codex"); !ok || err != nil {
		t.Fatalf("present marker reported %v, %v", ok, err)
	}
	if ok, err := ContextImported(f.state, "claude"); ok || err != nil {
		t.Fatalf("another provider's marker reported %v, %v", ok, err)
	}
	elsewhere := filepath.Join(filepath.Dir(f.state), "elsewhere")
	put(t, filepath.Join(elsewhere, contextMarker), "{}")
	if err := os.MkdirAll(filepath.Dir(providerHome(f.state, "claude")), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, providerHome(f.state, "claude")); err != nil {
		t.Fatal(err)
	}
	if ok, err := ContextImported(f.state, "claude"); ok || !errors.Is(err, ErrUnsupported) {
		t.Fatalf("symlinked managed home reported %v, %v", ok, err)
	}
	if ok, err := ContextImported("relative-state", "codex"); ok || !errors.Is(err, ErrUnsupported) {
		t.Fatalf("relative state reported %v, %v", ok, err)
	}
}

func TestManagedBoundRecognisesMigratedEnvironment(t *testing.T) {
	state := t.TempDir()
	codex := providerHome(state, "codex")
	for _, tc := range []struct {
		name string
		meta persist.Meta
		want bool
	}{
		{"login runtime", persist.Meta{AgentType: "codex", LoginRuntime: &logins.Runtime{Provider: "codex", Home: codex}}, true},
		{"migrated codex", persist.Meta{AgentType: "codex", Env: []string{"CODEX_HOME=" + codex + "/"}}, true},
		{"migrated claude", persist.Meta{AgentType: "claude", Env: commonEnvironment("claude", providerHome(state, "claude"), nil)}, true},
		{"native", persist.Meta{AgentType: "codex", Env: []string{"HOME=/home/u"}}, false},
		{"other codex home", persist.Meta{AgentType: "codex", Env: []string{"CODEX_HOME=/home/u/.codex"}}, false},
		{"other provider's key", persist.Meta{AgentType: "codex", Env: []string{"CLAUDE_CONFIG_DIR=" + codex}}, false},
		{"overridden", persist.Meta{AgentType: "codex", Env: []string{"CODEX_HOME=" + codex, "CODEX_HOME=/home/u/.codex"}}, false},
	} {
		if got := ManagedBound(state, tc.meta); got != tc.want {
			t.Errorf("%s: ManagedBound = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// v0.25's migration pointed legacy discussions' environment at the managed home
// without a login runtime; their only full history is there.
func TestPlanContextWithMarkerVerifiesMigratedConversations(t *testing.T) {
	f := newFixture(t)
	managed := providerHome(f.state, "codex")
	put(t, filepath.Join(managed, contextMarker), "{}")
	put(t, filepath.Join(f.state, f.id, "meta.json"), map[string]any{"schema_version": 1, "id": f.id, "agent_type": "codex", "conversation_id": conversation, "cwd": f.project, "env": commonEnvironment("codex", managed, nil)})
	if err := PreflightProviderContext(f.state, "codex", nil); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("migrated conversation missing from the managed home was accepted: %v", err)
	}
}
