package skeleton

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

// Upgrade the old opaque fixture grants to synthetic exact native identities.
// The process lifecycle remains the existing manager fake, not native evidence.
func codexMigrationFixture(t *testing.T) *loginFixture {
	t.Helper()
	f := newLoginFixture(t)
	for _, l := range []logins.Login{f.first, f.second} {
		path := filepath.Join(f.m.store.StateRoot(), "logins", "saved", l.ID, "credential.json")
		raw, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		var saved map[string]json.RawMessage
		if e = json.Unmarshal(raw, &saved); e != nil {
			t.Fatal(e)
		}
		credential := codexEnrollmentCredential("user-"+l.Label, l.Label, "synthetic-"+l.Label)
		saved["credential"], _ = json.Marshal(credential)
		raw, _ = json.Marshal(saved)
		if e = os.WriteFile(path, raw, 0600); e != nil {
			t.Fatal(e)
		}
		if l.ID == f.first.ID {
			if e = os.WriteFile(filepath.Join(f.runtime.Home, "auth.json"), credential.Auth, 0600); e != nil {
				t.Fatal(e)
			}
		}
	}
	f.m.qualifyCodexOwnership = func(sources []persist.Meta) (bool, error) {
		return true, nil
	}
	return f
}
func TestCodexMigrationManagerExplicitSwitchFromExhaustedActive(t *testing.T) {
	f := codexMigrationFixture(t)
	r, _ := f.m.store.Snapshot()
	if _, e := f.m.store.UpdateExhaustion(r.Revision, f.first.ID, time.Now().Add(time.Hour), false); e != nil {
		t.Fatal(e)
	}
	// An unavailable parked identity has no grant and must not block ready B.
	identity, _ := logins.CodexIdentity(codexEnrollmentCredential("missing-user", "missing", "unused").Auth)
	r, _ = f.m.store.Snapshot()
	if _, _, e := f.m.store.ImportUnavailable(r.Revision, logins.Login{ID: strings.Repeat("f", 32), Provider: logins.ProviderCodex, Label: "missing", Identity: identity, AuthState: logins.AuthNeedsLogin}); e != nil {
		t.Fatal(e)
	}
	op, e := f.m.BeginSwitch(logins.ProviderCodex, f.second.ID, "explicit")
	if e != nil {
		t.Fatal(e)
	}
	if !op.OwnershipMigration || op.ToID != f.second.ID || len(op.Sessions) != 2 {
		t.Fatal("manager did not create exact migration cohort")
	}
	before, _ := os.ReadFile(filepath.Join(f.runtime.Home, "auth.json"))
	if bytes.Contains(before, []byte("synthetic-second")) {
		t.Fatal("destination installed before stops")
	}
	driveLoginSwitch(t, f)
	r, _ = f.m.store.Snapshot()
	if r.Providers[logins.ProviderCodex].AuthOwnerMode != logins.CodexNativeGrantMode || r.Providers[logins.ProviderCodex].ActiveID != f.second.ID {
		t.Fatal("manager failed ownership cutover")
	}
	if len(f.launched) != 2 {
		t.Fatal("migration dropped restart obligations")
	}
	for _, spec := range f.launched {
		if spec.LoginRuntime == nil || !spec.LoginRuntime.ExternalAuth {
			t.Fatal("resumed legacy credential writer")
		}
	}
}
func TestCodexMigrationManagerSameActiveOnlyOnExplicitRequest(t *testing.T) {
	f := codexMigrationFixture(t)
	f.m.step()
	r, _ := f.m.store.Snapshot()
	if len(r.Switches) != 0 || r.Providers[logins.ProviderCodex].AuthOwnerMode != "" {
		t.Fatal("healthy tick migrated provider")
	}
	op, e := f.m.BeginSwitch(logins.ProviderCodex, f.first.ID, "explicit")
	if e != nil || !op.OwnershipMigration {
		t.Fatalf("same-active explicit cutover skipped: %v", e)
	}
	driveLoginSwitch(t, f)
	if _, e = f.m.BeginSwitch(logins.ProviderCodex, f.second.ID, "explicit"); !errors.Is(e, logins.ErrPending) {
		t.Fatalf("owned provider fell back to stopped switching: %v", e)
	}
}
func TestCodexMigrationManagerPreservesExistingLegacyJournal(t *testing.T) {
	f := codexMigrationFixture(t)
	r, _ := f.m.store.Snapshot()
	old, e := f.m.store.BeginSwitch(r.Revision, logins.ProviderCodex, f.second.ID, "old binary", nil)
	if e != nil {
		t.Fatal(e)
	}
	f.m.qualifyCodexOwnership = func([]persist.Meta) (bool, error) {
		t.Fatal("existing legacy operation was requalified")
		return false, nil
	}
	got, e := f.m.BeginSwitch(logins.ProviderCodex, f.second.ID, "retry")
	if e != nil || got.ID != old.ID || got.OwnershipMigration {
		t.Fatalf("legacy journal reclassified: %v", e)
	}
	// No cohort was recorded in this synthetic preexisting operation.
	for id, row := range f.rows {
		row.Status.Process = status.ProcessExited
		f.rows[id] = row
	}
	driveLoginSwitch(t, f)
	r, _ = f.m.store.Snapshot()
	if r.Providers[logins.ProviderCodex].AuthOwnerMode != "" {
		t.Fatal("legacy recovery changed ownership")
	}
}
func TestCodexMigrationManagerNeverInstallsWhileWriterProofFails(t *testing.T) {
	f := codexMigrationFixture(t)
	op, e := f.m.BeginSwitch(logins.ProviderCodex, f.second.ID, "explicit")
	if e != nil {
		t.Fatal(e)
	}
	f.m.stopProof = func(persist.Meta) error { return logins.ErrInUse }
	for i := 0; i < 4; i++ {
		f.m.step()
	}
	r, _ := f.m.store.Snapshot()
	current := r.Switches[logins.ProviderCodex]
	if current.ID != op.ID || current.Phase == "captured" || current.Phase == "installed" || r.Providers[logins.ProviderCodex].AuthOwnerMode != "" || len(f.launched) != 0 {
		t.Fatal("failed stop proof changed authority")
	}
}
func TestCodexMigrationManagerQualificationFailureBeforeJournal(t *testing.T) {
	for _, unexpected := range []bool{false, true} {
		t.Run(map[bool]string{false: "unsupported", true: "probe-error"}[unexpected], func(t *testing.T) {
			f := codexMigrationFixture(t)
			failure := errors.New("synthetic qualification failure")
			f.m.qualifyCodexOwnership = func([]persist.Meta) (bool, error) {
				if unexpected {
					return false, failure
				}
				return false, nil
			}
			op, e := f.m.BeginSwitch(logins.ProviderCodex, f.second.ID, "explicit")
			if unexpected {
				if !errors.Is(e, failure) {
					t.Fatalf("qualification error silently fell back: %v", e)
				}
				r, _ := f.m.store.Snapshot()
				if len(r.Switches) != 0 {
					t.Fatal("failed qualification created journal")
				}
			} else if e != nil || op.OwnershipMigration {
				t.Fatalf("unsupported native lost legacy fallback: %v", e)
			}
		})
	}
}

func TestCodexMigrationQualificationUsesEverySourceEnvironment(t *testing.T) {
	native := func(version string) persist.Meta {
		dir, e := filepath.EvalSymlinks(t.TempDir())
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(dir, "codex"), []byte("#!/bin/sh\nprintf '%s\\n' 'codex-cli "+version+"'\n"), 0700); e != nil {
			t.Fatal(e)
		}
		return persist.Meta{Env: []string{"PATH=" + dir}, Cwd: dir}
	}
	qualified, unsupported := native("0.163.0"), native("0.162.0")
	if ok, e := qualifyCodexOwnershipSources([]persist.Meta{qualified}); e != nil || !ok {
		t.Fatalf("qualified native refused: %v", e)
	}
	if ok, e := qualifyCodexOwnershipSources([]persist.Meta{qualified, unsupported}); e != nil || ok {
		t.Fatalf("unsupported included native ignored: %v", e)
	}
	if ok, e := qualifyCodexOwnershipSources([]persist.Meta{unsupported, {Env: []string{"PATH=/missing"}}}); e == nil || ok {
		t.Fatal("unsupported first source masked later probe failure")
	}
	t.Setenv("PATH", strings.TrimPrefix(qualified.Env[0], "PATH="))
	if ok, e := qualifyCodexOwnershipSources(nil); e != nil || !ok {
		t.Fatalf("empty cohort did not qualify installed native: %v", e)
	}
	if ok, e := qualifyInstalledCodexOwnership(nil); e != nil || ok != (runtime.GOOS == "linux") {
		t.Fatalf("platform admission disagrees with native qualification: %v", e)
	}
	path := filepath.Join(qualified.Cwd, "codex")
	if e := os.Chmod(path, 0777); e != nil {
		t.Fatal(e)
	}
	if ok, e := qualifyCodexOwnershipSources([]persist.Meta{qualified}); e == nil || ok {
		t.Fatal("unsafe native installation allowed")
	}
}
func TestCodexMigrationManagerRechecksBeforeOwnershipEffects(t *testing.T) {
	f := codexMigrationFixture(t)
	op, e := f.m.BeginSwitch(logins.ProviderCodex, f.second.ID, "explicit")
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 8; i++ {
		r, _ := f.m.store.Snapshot()
		if r.Switches[logins.ProviderCodex].Phase == "captured" {
			break
		}
		f.m.step()
	}
	r, _ := f.m.store.Snapshot()
	if r.Switches[logins.ProviderCodex].Phase != "captured" {
		t.Fatal("fixture did not capture stopped grant")
	}
	f.m.qualifyCodexOwnership = func([]persist.Meta) (bool, error) { return false, nil }
	f.m.step()
	r, _ = f.m.store.Snapshot()
	if r.Switches[logins.ProviderCodex].ID != op.ID || r.Switches[logins.ProviderCodex].Phase != "captured" || r.Providers[logins.ProviderCodex].AuthOwnerMode != "" {
		t.Fatal("installation change reinterpreted durable migration")
	}
	f.m.qualifyCodexOwnership = func([]persist.Meta) (bool, error) { return true, nil }
	f.m.step()
	f.m.step()
	r, _ = f.m.store.Snapshot()
	if r.Switches[logins.ProviderCodex].Phase != "committed" {
		t.Fatal("migration failed to commit")
	}
	f.m.qualifyCodexOwnership = func([]persist.Meta) (bool, error) { return false, nil }
	f.m.step()
	if len(f.launched) != 0 {
		t.Fatal("unqualified native resumed with external authentication")
	}
	f.m.qualifyCodexOwnership = func([]persist.Meta) (bool, error) { return true, nil }
	driveLoginSwitch(t, f)
}
