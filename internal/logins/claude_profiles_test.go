package logins

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/Nathandela/swarm/internal/processcontain"
)

func profileFixture(t *testing.T) (*Store, Login, string) {
	t.Helper()
	s := testStore(t)
	s.grantWriters = func([]string, []string) ([]processcontain.Identity, error) { return nil, nil }
	l := add(t, s, ProviderClaude, "A", claudeCredential("a", "secret-grant"))
	common := s.Home(ProviderClaude)
	for _, dir := range []string{"projects", "commands", "skills", "agents", "rules", "output-styles", "plugins"} {
		if err := os.Mkdir(filepath.Join(common, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{"settings.json": `{"model":"opus"}`, "CLAUDE.md": "instructions", ".claude.json": `{"oauthAccount":{"accountUuid":"legacy","organizationUuid":"org"},"userID":"legacy","mcpServers":{"user":{"command":"server","env":{"SECRET":"mcp-secret"}}},"projects":{"/work":{"mcpServers":{"local":{"command":"local"}},"hasTrustDialogAccepted":true}},"mcpOAuth":{"token":"oauth-secret"}}`} {
		if err := os.WriteFile(filepath.Join(common, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return s, l, common
}

func TestClaudeProfileIdentityPathAndNativeAliases(t *testing.T) {
	s, l, common := profileFixture(t)
	home, err := s.ClaudeProfileHome(l.Identity)
	if err != nil || home != filepath.Join(s.path, "provider", "claude", "profiles", l.Identity) || strings.Contains(home, "saved") {
		t.Fatalf("home=%s err=%v", home, err)
	}
	for _, bad := range []string{"", "../escape", strings.ToUpper(l.Identity), strings.Repeat("x", 64)} {
		if _, err = s.ClaudeProfileHome(bad); !errors.Is(err, ErrIneligible) {
			t.Fatalf("unsafe identity accepted: %q %v", bad, err)
		}
	}
	before, err := os.ReadFile(filepath.Join(common, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.ProvisionClaudeProfile(snapshot(t, s).Revision, l.ID, []string{"/work"})
	if err != nil {
		t.Fatal(err)
	}
	if r.SchemaVersion != claudeProfileSchemaVersion || !r.ClaudeProfiles[l.Identity].Provisioned {
		t.Fatalf("profile not durable: %+v", r)
	}
	for _, name := range []string{"projects", "settings.json", "CLAUDE.md", "commands", "skills", "agents", "rules", "output-styles", "plugins"} {
		target, err := os.Readlink(filepath.Join(home, name))
		if err != nil || target != filepath.Join(common, name) {
			t.Fatalf("alias %s=%q %v", name, target, err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "oauthAccount") || strings.Contains(string(raw), "userID") || strings.Contains(string(raw), "oauth-secret") || strings.Contains(string(raw), "hasTrustDialogAccepted") || strings.Contains(string(raw), "secret-grant") {
		t.Fatalf("copied unrelated native authority: %s", raw)
	}
	var got map[string]any
	if err = json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["mcpServers"] == nil || got["projects"] == nil || !strings.Contains(string(raw), "mcp-secret") || !strings.Contains(string(raw), "local") {
		t.Fatalf("missing explicit definitions: %s", raw)
	}
	after, err := os.ReadFile(filepath.Join(common, ".claude.json"))
	if err != nil || string(before) != string(after) {
		t.Fatal("common profile changed")
	}
	if _, err = os.Stat(filepath.Join(home, ".credentials.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("copied grant: %v", err)
	}
	r2, err := s.ProvisionClaudeProfile(r.Revision, l.ID, []string{"/work"})
	if err != nil || !reflect.DeepEqual(r.ClaudeProfiles, r2.ClaudeProfiles) {
		t.Fatalf("repeat: %v", err)
	}
	if _, err = s.ProvisionClaudeProfile(r2.Revision, l.ID, []string{"/new-work"}); !errors.Is(err, ErrPending) {
		t.Fatalf("new live scope accepted: %v", err)
	}
}

func TestClaudeProfileRefusesUnownedAndUnsafeDestinations(t *testing.T) {
	for _, kind := range []string{"nonempty", "empty-unowned", "profile-link", "parent-link", "asset-link", "asset-writable", "foreign-alias", "bad-cwd", "invalid-cwd"} {
		t.Run(kind, func(t *testing.T) {
			s, l, common := profileFixture(t)
			home, _ := s.ClaudeProfileHome(l.Identity)
			parent := filepath.Dir(home)
			if err := os.MkdirAll(parent, 0700); err != nil {
				t.Fatal(err)
			}
			cwd := []string{"/work"}
			var err error
			switch kind {
			case "nonempty", "empty-unowned":
				err = os.Mkdir(home, 0700)
				if err == nil && kind == "nonempty" {
					err = os.WriteFile(filepath.Join(home, "unknown"), []byte("preserve"), 0600)
				}
			case "profile-link":
				err = os.Symlink(common, home)
			case "parent-link":
				err = os.Remove(parent)
				if err == nil {
					err = os.Symlink(common, parent)
				}
			case "asset-link":
				err = os.Rename(filepath.Join(common, "settings.json"), filepath.Join(common, "settings.original"))
				if err == nil {
					err = os.Symlink(filepath.Join(common, "settings.original"), filepath.Join(common, "settings.json"))
				}
			case "asset-writable":
				err = os.Chmod(filepath.Join(common, "settings.json"), 0660)
			case "foreign-alias":
				err = os.Symlink("/private/tmp", home)
			case "bad-cwd":
				cwd = []string{"/work/../escape"}
			case "invalid-cwd":
				cwd = []string{"/work/\xff"}
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.ProvisionClaudeProfile(snapshot(t, s).Revision, l.ID, cwd); err == nil {
				t.Fatal("unsafe destination accepted")
			}
			if _, err = os.Stat(filepath.Join(common, ".claude.json")); err != nil {
				t.Fatalf("common damaged: %v", err)
			}
		})
	}
}

func TestClaudeProfileCrashAndDiskFullRemainPending(t *testing.T) {
	s, l, common := profileFixture(t)
	before, err := os.ReadFile(filepath.Join(common, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	s.writeOps.rename = func(old, new string) error {
		if strings.HasSuffix(new, ".claude.json") {
			return syscall.ENOSPC
		}
		return s.root.Rename(old, new)
	}
	if _, err = s.ProvisionClaudeProfile(snapshot(t, s).Revision, l.ID, []string{"/work"}); !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("expected full disk: %v", err)
	}
	r := snapshot(t, s)
	if r.ClaudeProfiles[l.Identity].Provisioned {
		t.Fatal("failed provisioning marked ready")
	}
	after, _ := os.ReadFile(filepath.Join(common, ".claude.json"))
	if string(after) != string(before) {
		t.Fatal("common damaged")
	}
	s.writeOps = writeOps{}
	if _, err = s.ProvisionClaudeProfile(r.Revision, l.ID, []string{"/work"}); err != nil {
		t.Fatalf("crash retry: %v", err)
	}
}

func TestClaudeProfileSchemaFenceRetentionAndCapacity(t *testing.T) {
	s, l, _ := profileFixture(t)
	r, err := s.ProvisionClaudeProfile(snapshot(t, s).Revision, l.ID, []string{"/work"})
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []int{1, 2, 3, 4, 5, 6} {
		old := r
		old.SchemaVersion = version
		if validateRegistry(old) == nil {
			t.Fatalf("profile accepted by schema %d", version)
		}
	}
	home, _ := s.ClaudeProfileHome(l.Identity)
	r, err = s.Remove(r.Revision, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !r.ClaudeProfiles[l.Identity].Provisioned {
		t.Fatal("removed retained reference")
	}
	if _, err = os.Readlink(filepath.Join(home, "projects")); err != nil {
		t.Fatal("removed history alias")
	}
	again := add(t, s, ProviderClaude, "A again", claudeCredential("a", "new-grant"))
	if again.ID == l.ID {
		t.Fatal("fixture reused saved login ID")
	}
	againHome, _ := s.ClaudeProfileHome(again.Identity)
	if againHome != home {
		t.Fatal("readd changed profile")
	}
	r, err = s.ProvisionClaudeProfile(snapshot(t, s).Revision, again.ID, []string{"/work"})
	if err != nil {
		t.Fatal(err)
	}
	if r.SchemaVersion != claudeProfileSchemaVersion {
		t.Fatal("schema downgraded")
	}
	r, err = s.mutate(r.Revision, func(r *Registry) error { schemaForHandovers(r); return nil })
	if err != nil || r.SchemaVersion != claudeProfileSchemaVersion {
		t.Fatalf("handover floor downgraded profile schema: %v", err)
	}
	for len(r.ClaudeProfiles) < MaxClaudeProfiles {
		identity := identityDigest(ProviderClaude, string(rune(len(r.ClaudeProfiles))))
		r, err = s.mutate(r.Revision, func(r *Registry) error { r.ClaudeProfiles[identity] = ClaudeProfile{}; return nil })
		if err != nil {
			t.Fatal(err)
		}
	}
	b := add(t, s, ProviderClaude, "B", claudeCredential("b", "b"))
	if _, err = s.ProvisionClaudeProfile(snapshot(t, s).Revision, b.ID, nil); !errors.Is(err, ErrCapacity) {
		t.Fatalf("capacity accepted: %v", err)
	}
	if _, err = os.Readlink(filepath.Join(home, "projects")); err != nil {
		t.Fatal("capacity removed retained alias")
	}
}

func TestClaudeProfilePendingRequiresPositiveWriterInventory(t *testing.T) {
	for _, kind := range []string{"writer", "unavailable", "unknown-file", "alias-conflict", "mcp-conflict"} {
		t.Run(kind, func(t *testing.T) {
			s, l, _ := profileFixture(t)
			s.writeOps.rename = func(old, new string) error {
				if strings.HasSuffix(new, ".claude.json") {
					return syscall.ENOSPC
				}
				return s.root.Rename(old, new)
			}
			if _, err := s.ProvisionClaudeProfile(snapshot(t, s).Revision, l.ID, []string{"/work"}); !errors.Is(err, syscall.ENOSPC) {
				t.Fatal(err)
			}
			s.writeOps = writeOps{}
			home, _ := s.ClaudeProfileHome(l.Identity)
			scans := 0
			s.grantWriters = func(homes, ids []string) ([]processcontain.Identity, error) {
				scans++
				if !reflect.DeepEqual(homes, []string{home}) || len(ids) != 0 {
					t.Fatal("wrong writer scope")
				}
				if kind == "writer" {
					return []processcontain.Identity{{PID: 123, StartTime: 456}}, nil
				}
				if kind == "unavailable" {
					return nil, processcontain.ErrUnavailable
				}
				return nil, nil
			}
			switch kind {
			case "unknown-file":
				if err := os.WriteFile(filepath.Join(home, "unknown-native-file"), []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
			case "alias-conflict":
				if err := os.Remove(filepath.Join(home, "projects")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("/private/tmp", filepath.Join(home, "projects")); err != nil {
					t.Fatal(err)
				}
			case "mcp-conflict":
				if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"mcpServers":{"different":{"command":"existing"}}}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.ProvisionClaudeProfile(snapshot(t, s).Revision, l.ID, []string{"/work"}); err == nil {
				t.Fatal("unproven/conflicting pending profile accepted")
			}
			if scans != 1 || snapshot(t, s).ClaudeProfiles[l.Identity].Provisioned {
				t.Fatal("missing scan or falsely ready")
			}
		})
	}
}

func TestClaudeProfileMCPMergePreservesPrivateNativeFields(t *testing.T) {
	s, l, _ := profileFixture(t)
	s.writeOps.rename = func(old, new string) error {
		if strings.HasSuffix(new, ".claude.json") {
			return syscall.ENOSPC
		}
		return s.root.Rename(old, new)
	}
	if _, err := s.ProvisionClaudeProfile(snapshot(t, s).Revision, l.ID, []string{"/work"}); !errors.Is(err, syscall.ENOSPC) {
		t.Fatal(err)
	}
	s.writeOps = writeOps{}
	home, _ := s.ClaudeProfileHome(l.Identity)
	body := `{"oauthAccount":{"accountUuid":"a","organizationUuid":"org"},"unknown":{"preserve":true},"mcpServers":{"user":{"env":{"SECRET":"mcp-secret"},"command":"server"}},"projects":{"/work":{"nativeField":"keep"},"/other":{"mcpServers":{"other":{"command":"other"}}}}}`
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ProvisionClaudeProfile(snapshot(t, s).Revision, l.ID, []string{"/work"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"oauthAccount", "unknown", "nativeField", "/other", "local", "mcp-secret"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("lost %s: %s", want, raw)
		}
	}
}

func TestClaudeProfileModeIsFencedAndCannotLaunchLegacyRuntime(t *testing.T) {
	s, l, _ := profileFixture(t)
	rt := activate(t, s, l)
	r, err := s.ProvisionClaudeProfile(snapshot(t, s).Revision, l.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.mutate(r.Revision, func(r *Registry) error {
		p := r.Providers[ProviderClaude]
		p.AuthOwnerMode = ClaudeNativeProfileMode
		r.Providers[ProviderClaude] = p
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []int{1, 2, 3, 4, 5, 6} {
		old := r
		old.SchemaVersion = version
		old.ClaudeProfiles = nil
		if validateRegistry(old) == nil {
			t.Fatalf("mode accepted in schema%d", version)
		}
	}
	if _, err = s.Runtime(ProviderClaude); !errors.Is(err, ErrPending) {
		t.Fatalf("profile mode launched common native home: %v", err)
	}
	if _, err = s.PrepareEnvironment(rt, nil); !errors.Is(err, ErrPending) {
		t.Fatalf("old runtime passed mode fence: %v", err)
	}
	if _, err = s.nativeCredential(ProviderClaude, l.ID); !errors.Is(err, ErrPending) {
		t.Fatalf("captured common native authority: %v", err)
	}
	if err = s.installCredential(ProviderClaude, claudeCredential("a", "new")); !errors.Is(err, ErrPending) {
		t.Fatalf("installed common native authority: %v", err)
	}
	homes, err := s.ProtectedHomes(ProviderClaude)
	if err != nil {
		t.Fatal(err)
	}
	home, _ := s.ClaudeProfileHome(l.Identity)
	if !slices.Contains(homes, home) || !slices.Contains(homes, s.savedNativeHome(l.ID)) {
		t.Fatalf("missing protected paths: %v", homes)
	}
}

func TestClaudeProfileCrashBeforeOwnerMarkerStaysPending(t *testing.T) {
	s, l, _ := profileFixture(t)
	s.writeOps.rename = func(old, new string) error {
		if strings.HasSuffix(new, claudeProfileOwner) {
			return syscall.ENOSPC
		}
		return s.root.Rename(old, new)
	}
	if _, err := s.ProvisionClaudeProfile(snapshot(t, s).Revision, l.ID, nil); !errors.Is(err, syscall.ENOSPC) {
		t.Fatal(err)
	}
	s.writeOps = writeOps{}
	if _, err := s.ProvisionClaudeProfile(snapshot(t, s).Revision, l.ID, nil); !errors.Is(err, ErrPending) {
		t.Fatalf("unmarked directory adopted: %v", err)
	}
}

func TestClaudeProfileMissingAssetsKeepExactAliasesAndOneHistoryTree(t *testing.T) {
	s := testStore(t)
	l := add(t, s, ProviderClaude, "A", claudeCredential("a", "grant"))
	if _, err := s.ProvisionClaudeProfile(snapshot(t, s).Revision, l.ID, nil); err != nil {
		t.Fatal(err)
	}
	home, _ := s.ClaudeProfileHome(l.Identity)
	if err := os.WriteFile(filepath.Join(home, "projects", "transcript"), []byte("one copy"), 0600); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(s.Home(ProviderClaude), "projects", "transcript"))
	if err != nil || string(raw) != "one copy" {
		t.Fatalf("history not shared: %v", err)
	}
	for _, name := range claudeProfileAssets {
		if target, err := os.Readlink(filepath.Join(home, name)); err != nil || target != filepath.Join(s.Home(ProviderClaude), name) {
			t.Fatalf("missing alias %s: %v", name, err)
		}
	}
}

func TestClaudeProfileConcurrentBirthUsesRegistryCAS(t *testing.T) {
	s, l, _ := profileFixture(t)
	second, err := Open(s.StateRoot())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close() }()
	revision := snapshot(t, s).Revision
	start := make(chan struct{})
	outcomes := make(chan error, 2)
	for _, store := range []*Store{s, second} {
		go func(store *Store) {
			<-start
			_, err := store.ProvisionClaudeProfile(revision, l.ID, []string{"/work"})
			outcomes <- err
		}(store)
	}
	close(start)
	a, b := <-outcomes, <-outcomes
	expected := a == nil && errors.Is(b, ErrConflict) || b == nil && errors.Is(a, ErrConflict)
	if !expected {
		t.Fatalf("birth results: %v %v", a, b)
	}
	if !snapshot(t, s).ClaudeProfiles[l.Identity].Provisioned {
		t.Fatal("winner not provisioned")
	}
}

func TestClaudeProfileAggregateRegistryCapacityRefusesBeforeBirth(t *testing.T) {
	s, l, _ := profileFixture(t)
	cwds := make([]string, 64)
	for i := range cwds {
		cwds[i] = fmt.Sprintf("/%02d/", i) + strings.Repeat("x", 4091)
	}
	r, err := s.mutate(snapshot(t, s).Revision, func(r *Registry) error {
		r.SchemaVersion = claudeProfileSchemaVersion
		r.ClaudeProfiles = map[string]ClaudeProfile{}
		for i := 0; i < MaxClaudeProfiles-1; i++ {
			r.ClaudeProfiles[identityDigest(ProviderClaude, fmt.Sprint(i))] = ClaudeProfile{CWDs: cwds}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ProvisionClaudeProfile(r.Revision, l.ID, cwds); !errors.Is(err, ErrCapacity) {
		t.Fatalf("aggregate bytes accepted: %v", err)
	}
	after := snapshot(t, s)
	if after.Revision != r.Revision || len(after.ClaudeProfiles) != len(r.ClaudeProfiles) {
		t.Fatal("capacity failure persisted new profile")
	}
	home, _ := s.ClaudeProfileHome(l.Identity)
	if _, err = os.Lstat(home); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("capacity failure touched destination: %v", err)
	}
}

func TestClaudeProfileMCPMatchesQualifiedNativeWriter(t *testing.T) {
	// The selected 2.1.310 add-json receipt; only the exact CWD key is normalized.
	raw := claudeProfileNativeMCPFixture
	var err error
	var fixture struct {
		Source       json.RawMessage `json:"source"`
		NativeWriter json.RawMessage `json:"native_writer"`
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	s, l, common := profileFixture(t)
	if err = os.WriteFile(filepath.Join(common, ".claude.json"), fixture.Source, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ProvisionClaudeProfile(snapshot(t, s).Revision, l.ID, []string{"/work"}); err != nil {
		t.Fatal(err)
	}
	home, _ := s.ClaudeProfileHome(l.Identity)
	actual, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got, want any
	if err = json.Unmarshal(actual, &got); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(fixture.NativeWriter, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("private map provisioning differs from qualified native writer")
	}
}

//go:embed testdata/claude-profile-native-mcp.json
var claudeProfileNativeMCPFixture []byte

func TestClaudeProfileSchemaSurvivesCodexDeleteAuthorization(t *testing.T) {
	s, a, _, op, native := transferFixture(t)
	l := add(t, s, ProviderClaude, "Claude", claudeCredential("claude", "grant"))
	r, err := s.ProvisionClaudeProfile(snapshot(t, s).Revision, l.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	r, h, err := s.TransferCodexMigration(r.Revision, op.ID, a.ID, []NativeMember{native}, []BootstrapChild{{SourceID: "source", Native: native, Phase: "pending"}})
	if err != nil {
		t.Fatal(err)
	}
	r, h, err = s.RecordCodexReceipt(r.Revision, handoverReceipt(h, native, "retired"))
	if err != nil {
		t.Fatal(err)
	}
	r, h, err = s.RecordCodexBootstrapOutcome(r.Revision, h.ID, "source", "", "cancelled")
	if err != nil {
		t.Fatal(err)
	}
	r, h, err = s.AuthorizeCodexBootstrapDelete(r.Revision, h.ID, "source", "", native)
	if err != nil {
		t.Fatal(err)
	}
	if r.SchemaVersion != claudeProfileSchemaVersion || !h.Bootstrap[0].OwnerDeleteAuthorized || !r.ClaudeProfiles[l.Identity].Provisioned {
		t.Fatal("Codex authorization downgraded schema or lost profile reference")
	}
}

func TestClaudeProfileOversizedPendingDirectoryRefusesWithoutPruning(t *testing.T) {
	s, l, _ := profileFixture(t)
	s.writeOps.rename = func(old, new string) error {
		if strings.HasSuffix(new, ".claude.json") {
			return syscall.ENOSPC
		}
		return s.root.Rename(old, new)
	}
	if _, err := s.ProvisionClaudeProfile(snapshot(t, s).Revision, l.ID, []string{"/work"}); !errors.Is(err, syscall.ENOSPC) {
		t.Fatal(err)
	}
	s.writeOps = writeOps{}
	home, _ := s.ClaudeProfileHome(l.Identity)
	// Valid leftover temporary names must not make a recovery scan unbounded.
	for i := 0; i < maxClaudeProfileEntries+1; i++ {
		name := fmt.Sprintf(".login-tmp-%032x", i+1)
		if err := os.WriteFile(filepath.Join(home, name), []byte("partial"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	before := snapshot(t, s)
	if _, err := s.ProvisionClaudeProfile(before.Revision, l.ID, []string{"/work"}); !errors.Is(err, ErrCapacity) {
		t.Fatalf("oversized pending directory accepted: %v", err)
	}
	after := snapshot(t, s)
	if after.Revision != before.Revision || after.ClaudeProfiles[l.Identity].Provisioned {
		t.Fatal("oversized directory changed pending ownership")
	}
	for i := 0; i < maxClaudeProfileEntries+1; i++ {
		name := fmt.Sprintf(".login-tmp-%032x", i+1)
		if _, err := os.Lstat(filepath.Join(home, name)); err != nil {
			t.Fatalf("recovery pruned residue: %v", err)
		}
	}
	if target, err := os.Readlink(filepath.Join(home, "projects")); err != nil || target != filepath.Join(s.Home(ProviderClaude), "projects") {
		t.Fatalf("recovery changed history alias: %v", err)
	}
}
