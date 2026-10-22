package loginmigration

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAstraCredentialOnlyMigrationChecksWriters(t *testing.T) {
	f := newFixture(t)
	if e := os.RemoveAll(filepath.Join(f.state, f.id)); e != nil {
		t.Fatal(e)
	}
	checkExternalWriters = func(string) error { return ErrLiveWriter }
	if e := Run(f.state); !errors.Is(e, ErrLiveWriter) {
		t.Fatalf("credential-only live writer migration admitted: %v", e)
	}
}
func TestAstraCleanupRestartChecksWriters(t *testing.T) {
	f := newFixture(t)
	plan, staged, e := inventory(f.state)
	if e != nil {
		t.Fatal(e)
	}
	plan.Phase = "cleanup"
	for path, b := range staged {
		put(t, filepath.Join(f.state, path), b)
	}
	put(t, filepath.Join(f.state, "logins", "migration.json"), plan)
	checkExternalWriters = func(string) error { return ErrLiveWriter }
	if e = Run(f.state); !errors.Is(e, ErrLiveWriter) {
		t.Fatalf("cleanup erased credential under live writer: %v", e)
	}
}
func TestAstraJournalRejectsEscapingRemove(t *testing.T) {
	f := newFixture(t)
	outside := filepath.Join(filepath.Dir(f.state), "unrelated-private.json")
	put(t, outside, "private unrelated data")
	raw, _ := os.ReadFile(outside)
	plan := migration{SchemaVersion: 1, Phase: "cleanup", Remove: []rewrite{{Path: "../unrelated-private.json", Before: digest(raw)}}}
	put(t, filepath.Join(f.state, "logins", "migration.json"), plan)
	if e := Run(f.state); e == nil {
		t.Fatal("escaping journal path deleted private external file")
	}
	if _, e := os.Stat(outside); e != nil {
		t.Fatalf("external file removed: %v", e)
	}
}
func TestAstraPreservesExecutableNativeAssets(t *testing.T) {
	f := newFixture(t)
	script := filepath.Join(f.home, "skills", "example", "scripts", "run.sh")
	put(t, script, "#!/bin/sh\nexit 0\n")
	if e := os.Chmod(script, 0700); e != nil {
		t.Fatal(e)
	}
	if e := prepareContext(f.state, "codex", []contextSource{{Home: f.home}}); e != nil {
		t.Fatal(e)
	}
	i, e := os.Stat(filepath.Join(providerHome(f.state, "codex"), "skills", "example", "scripts", "run.sh"))
	if e != nil {
		t.Fatal(e)
	}
	if i.Mode().Perm()&0100 == 0 {
		t.Fatal("native executable script lost execution permission")
	}
}
func TestAstraTerminalEnrollmentRequiresContainedChildren(t *testing.T) {
	f := newFixture(t)
	job := "dddddddddddddddddddddddddddddddd"
	put(t, filepath.Join(f.state, "accounts", "enrollment.json"), map[string]any{"schema_version": 1, "jobs": map[string]any{job: map[string]any{"state": "failed", "worker": map[string]int{"pid": 0}}}})
	put(t, filepath.Join(f.state, "accounts", "jobs", job, "worker-progress.json"), map[string]any{"schema_version": 1, "job_id": job, "generation": 1, "phase": "failed", "writers_stopped": false, "native_writers_stopped": false, "runner": map[string]int{"pid": os.Getpid()}})
	if e := inventoryWorkers(f.state); e == nil {
		t.Fatal("terminal worker with live/uncontained native runner accepted")
	}
}

func TestMigrationJournalAllowsOnlyOwnedSourceClasses(t *testing.T) {
	for _, kind := range []string{"phase", "metadata-parent", "metadata-unrelated", "stage-parent", "stage-unrelated", "secret-parent", "secret-unrelated", "auth-parent", "auth-unrelated", "identity-unrelated", "provider"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			plan, _, e := inventory(f.state)
			if e != nil {
				t.Fatal(e)
			}
			switch kind {
			case "phase":
				plan.Phase = "unrecognized"
			case "metadata-parent":
				plan.Rewrites[0].Path = "../outside.json"
			case "metadata-unrelated":
				plan.Rewrites[0].Path = "daemon.env"
			case "stage-parent":
				plan.Rewrites[0].Stage = "../outside.json"
			case "stage-unrelated":
				plan.Rewrites[0].Stage = "accounts/registry.json"
			case "secret-parent":
				plan.Secrets[0].Path = "../outside.json"
			case "secret-unrelated":
				plan.Secrets[0].Path = "accounts/registry.json"
			case "auth-parent":
				plan.Imports[0].AuthPath = filepath.Join(filepath.Dir(f.state), "outside.json")
			case "auth-unrelated":
				plan.Imports[0].AuthPath = filepath.Join(f.state, "accounts", "registry.json")
			case "identity-unrelated":
				plan.Imports[0].IdentityPath = filepath.Join(f.state, "accounts", "registry.json")
			case "provider":
				plan.Contexts["unrecognized"] = nil
			}
			if e = validateMigration(f.state, plan); e == nil {
				t.Fatal("journal crossed source/path/phase boundary")
			}
			put(t, filepath.Join(f.state, "logins", "migration.json"), plan)
			if e = Run(f.state); e == nil {
				t.Fatal("Run accepted invalid persisted journal")
			}
			b, e := os.ReadFile(filepath.Join(f.profile, "auth.json"))
			if e != nil || !strings.Contains(string(b), "synthetic-refresh") {
				t.Fatal("invalid journal mutated source credential")
			}
		})
	}
}
func TestMigrationDirectoryEdgesSyncAndFailureIsRetryable(t *testing.T) {
	f := newFixture(t)
	target := filepath.Join(f.state, "nested", "level", "leaf")
	original := syncPrivateParent
	defer func() { syncPrivateParent = original }()
	calls := map[string]bool{}
	injected := errors.New("directory fsync failed")
	syncPrivateParent = func(path string) error {
		calls[path] = true
		if path == filepath.Dir(target) {
			return injected
		}
		return original(path)
	}
	if e := privateDir(target); !errors.Is(e, injected) {
		t.Fatalf("directory parent sync failure lost: %v", e)
	}
	for _, path := range []string{f.state, filepath.Join(f.state, "nested"), filepath.Join(f.state, "nested", "level")} {
		if !calls[path] {
			t.Fatalf("new directory edge not synced: %s", path)
		}
	}
	retried := false
	syncPrivateParent = func(path string) error {
		if path == filepath.Dir(target) {
			retried = true
		}
		return original(path)
	}
	if e := privateDir(target); e != nil || !retried {
		t.Fatalf("existing mkdir edge was not resynced on retry: %v", e)
	}
}
func TestMigrationRechecksWritersBeforeCleanupMutation(t *testing.T) {
	f := newFixture(t)
	old := migrationCheckpoint
	defer func() { migrationCheckpoint = old }()
	migrationCheckpoint = func(string) error { checkExternalWriters = func(string) error { return ErrLiveWriter }; return nil }
	if e := Run(f.state); !errors.Is(e, ErrLiveWriter) {
		t.Fatalf("writer admitted before source cleanup: %v", e)
	}
	b, e := os.ReadFile(filepath.Join(f.profile, "auth.json"))
	if e != nil || !strings.Contains(string(b), "synthetic-refresh") {
		t.Fatal("new writer's source grant was erased")
	}
}
func TestExecutableModeParticipatesInContextConflictAndRevalidation(t *testing.T) {
	f := newFixture(t)
	script := filepath.Join(f.home, "skills", "run.sh")
	put(t, script, "#!/bin/sh\n")
	other := filepath.Join(filepath.Dir(f.home), "other-context")
	put(t, filepath.Join(other, "skills", "run.sh"), "#!/bin/sh\n")
	if e := os.Chmod(script, 0700); e != nil {
		t.Fatal(e)
	}
	if _, e := planContext(f.state, "codex", []contextSource{{Home: f.home}, {Home: other}}); !errors.Is(e, ErrHistoryConflict) {
		t.Fatalf("same bytes with different owner execute state silently merged: %v", e)
	}
	checkExternalWriters = func(string) error { return os.Chmod(script, 0600) }
	if e := prepareContext(f.state, "codex", []contextSource{{Home: f.home}}); !errors.Is(e, ErrHistoryConflict) {
		t.Fatalf("changed native execute mode copied after preflight: %v", e)
	}
}
