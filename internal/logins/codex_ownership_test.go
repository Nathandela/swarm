package logins

import (
	"bytes"
	"errors"
	"github.com/Nathandela/swarm/internal/processcontain"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestCodexOwnershipMigrationRequiresStoppedCapturedJournal(t *testing.T) {
	s := testStore(t)
	s.grantWriters = func([]string, []string) ([]processcontain.Identity, error) { return nil, nil }
	a := add(t, s, ProviderCodex, "a", Credential{Auth: grantAuth("u", "a", time.Now().Add(time.Hour).Unix())})
	activate(t, s, a)
	op, err := s.BeginCodexOwnershipMigration(snapshot(t, s).Revision, []Restart{{SourceID: "session", ConversationID: "thread"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.MigrateCodexOwnership(op.ID); !errors.Is(err, ErrInUse) {
		t.Fatalf("migrated live grant: %v", err)
	}
	if _, err = os.Stat(filepath.Join(s.savedNativeHome(a.ID), "auth.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("canonical grant written before capture")
	}
	op.Phase = "stopping"
	if err = s.SaveSwitch(op); err != nil {
		t.Fatal(err)
	}
	op.Phase = "stopped"
	op.Sessions[0].Phase = "stopped"
	if err = s.SaveSwitch(op); err != nil {
		t.Fatal(err)
	}
	// Capture the newest native generation, never the earlier saved snapshot.
	latest := grantAuth("u", "a", time.Now().Add(2*time.Hour).Unix())
	if err = s.installCredential(ProviderCodex, Credential{Auth: latest}); err != nil {
		t.Fatal(err)
	}
	if err = s.CaptureOutgoing(op.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.MigrateCodexOwnership(op.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.MigrateCodexOwnership(op.ID); err != nil {
		t.Fatalf("migration replay: %v", err)
	}
	r := snapshot(t, s)
	if r.SchemaVersion != 2 || r.Providers[ProviderCodex].AuthOwnerMode != CodexNativeGrantMode {
		t.Fatal("ownership not durably versioned")
	}
	if len(r.Switches[ProviderCodex].Sessions) != 1 {
		t.Fatal("restart obligations lost")
	}
	saved, err := os.ReadFile(filepath.Join(s.savedNativeHome(a.ID), "credential.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(saved, []byte("refresh_token")) || bytes.Contains(saved, []byte("access_token")) {
		t.Fatal("metadata retained grant")
	}
	canonical, err := os.ReadFile(filepath.Join(s.savedNativeHome(a.ID), "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(canonical, []byte("refresh_token")) {
		t.Fatal("canonical grant absent")
	}
	if _, err = os.Stat(filepath.Join(s.Home(ProviderCodex), "auth.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("legacy refresh copy survived")
	}
	if err = s.InstallIncoming(op.ID); !errors.Is(err, ErrPending) {
		t.Fatalf("legacy installer accepted owner mode: %v", err)
	}
	if _, err = s.CommitSwitch(op.ID); err != nil {
		t.Fatal(err)
	}
	lease, err := s.AcquireCodexGrant(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Close() }()
	if _, err = s.AcquireCodexGrant(a.ID); !errors.Is(err, ErrInUse) {
		t.Fatalf("overlapping custody: %v", err)
	}
	if _, err = lease.Access(time.Now()); err != nil {
		t.Fatal(err)
	}
}

func ownedGrantFixture(t *testing.T) (*Store, Login, *GrantLease) {
	t.Helper()
	s := testStore(t)
	s.grantWriters = func([]string, []string) ([]processcontain.Identity, error) { return nil, nil }
	a := add(t, s, ProviderCodex, "a", Credential{Auth: grantAuth("u", "a", time.Now().Add(time.Hour).Unix())})
	activate(t, s, a)
	op, e := s.BeginCodexOwnershipMigration(snapshot(t, s).Revision, nil)
	if e != nil {
		t.Fatal(e)
	}
	op.Phase = "stopping"
	if e = s.SaveSwitch(op); e != nil {
		t.Fatal(e)
	}
	op.Phase = "stopped"
	if e = s.SaveSwitch(op); e != nil {
		t.Fatal(e)
	}
	if e = s.CaptureOutgoing(op.ID); e != nil {
		t.Fatal(e)
	}
	if e = s.MigrateCodexOwnership(op.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.CommitSwitch(op.ID); e != nil {
		t.Fatal(e)
	}
	if e = s.CompleteSwitch(op.ID); e != nil {
		t.Fatal(e)
	}
	lease, e := s.AcquireCodexGrant(a.ID)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = lease.Close() })
	r := snapshot(t, s)
	a = *find(&r, a.ID)
	return s, a, lease
}
func TestCodexGrantCustodyStopsLegacyWritersAndForeignNative(t *testing.T) {
	s, a, lease := ownedGrantFixture(t)
	r := snapshot(t, s)
	if _, _, e := s.Relogin(r.Revision, a.ID, Credential{Auth: grantAuth("u", "a", time.Now().Add(time.Hour).Unix())}); !errors.Is(e, ErrPending) {
		t.Fatalf("legacy relogin: %v", e)
	}
	if e := s.installCredential(ProviderCodex, codexCredential("a", "foreign")); !errors.Is(e, ErrPending) {
		t.Fatalf("legacy grant copy: %v", e)
	}
	if _, e := s.BeginSwitch(r.Revision, ProviderCodex, a.ID, "old", nil); !errors.Is(e, ErrPending) {
		t.Fatalf("legacy switch: %v", e)
	}
	if e := lease.PrepareAuxiliary("0.163.0"); e != nil {
		t.Fatal(e)
	}
	s.grantWriters = func([]string, []string) ([]processcontain.Identity, error) {
		return []processcontain.Identity{{PID: 123, StartTime: 456}}, nil
	}
	if e := lease.Close(); !errors.Is(e, ErrInUse) {
		t.Fatalf("live writer released custody: %v", e)
	}
	s.grantWriters = func([]string, []string) ([]processcontain.Identity, error) { return nil, nil }
	if e := lease.Close(); e != nil {
		t.Fatal(e)
	}
	s.grantWriters = func([]string, []string) ([]processcontain.Identity, error) { return nil, processcontain.ErrUnavailable }
	if _, e := s.AcquireCodexGrant(a.ID); !errors.Is(e, ErrInUse) {
		t.Fatalf("unknown startup-gap inventory admitted writer: %v", e)
	}
}
func TestCodexAuxiliaryOwnershipAndRenewalProof(t *testing.T) {
	s, a, lease := ownedGrantFixture(t)
	before, e := lease.Generation()
	if e != nil {
		t.Fatal(e)
	}
	if e = lease.ValidateRenewal(before, time.Now()); !errors.Is(e, ErrInvalidCredentials) {
		t.Fatalf("unchanged credentials counted as renewal: %v", e)
	}
	if e = lease.PrepareAuxiliary("0.163.0"); e != nil {
		t.Fatal(e)
	}
	outside := filepath.Join(t.TempDir(), "keep")
	if e = os.WriteFile(outside, []byte("keep"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.Mkdir(filepath.Join(lease.Home(), "app-server-control"), 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(lease.Home(), "app-server-control", "owned-control"), nil, 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.Mkdir(filepath.Join(lease.Home(), "tmp"), 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink(outside, filepath.Join(lease.Home(), "tmp", "native-helper")); e != nil {
		t.Fatal(e)
	}
	unknown := filepath.Join(lease.Home(), "unknown-history")
	if e = os.WriteFile(unknown, []byte("do not erase"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = lease.CleanupAuxiliary(); !errors.Is(e, errUnknownSavedData) {
		t.Fatalf("unknown bytes accepted: %v", e)
	}
	if _, e = os.Stat(unknown); e != nil {
		t.Fatal("unknown bytes erased")
	}
	if e = os.Remove(unknown); e != nil {
		t.Fatal(e)
	}
	if e = lease.CleanupAuxiliary(); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Lstat(filepath.Join(lease.Home(), "app-server-control")); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("qualified app-server-control auxiliary not retired")
	}
	if _, e = os.Stat(outside); e != nil {
		t.Fatal("followed helper symlink")
	}
	if _, e = s.readSaved(a); e != nil {
		t.Fatalf("cleanup damaged canonical grant: %v", e)
	}
	// Harmless formatting changes do not establish a new credential generation.
	path := filepath.Join(lease.Home(), "auth.json")
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, append(raw, '\n'), 0600); e != nil {
		t.Fatal(e)
	}
	if e = lease.ValidateRenewal(before, time.Now()); !errors.Is(e, ErrInvalidCredentials) {
		t.Fatalf("formatting counted as renewal: %v", e)
	}
	fresh := grantAuth("u", "a", time.Now().Add(2*time.Hour).Unix())
	if e = os.WriteFile(path, fresh, 0600); e != nil {
		t.Fatal(e)
	}
	if e = lease.ValidateRenewal(before, time.Now()); e != nil {
		t.Fatalf("fresh durable credential proof: %v", e)
	}
}
func TestCodexLeaseReloginRequiresExactOwnerAndSerializesRemoval(t *testing.T) {
	s, a, lease := ownedGrantFixture(t)
	job := "00112233445566778899aabbccddeeff"
	r := snapshot(t, s)
	if _, _, e := lease.ReplaceCredential(r.Revision, Credential{Auth: grantAuth("foreign", "a", time.Now().Add(time.Hour).Unix())}, job); !errors.Is(e, ErrIdentityChanged) {
		t.Fatalf("same workspace foreign owner accepted: %v", e)
	}
	r, out, e := lease.ReplaceCredential(r.Revision, Credential{Auth: grantAuth("u", "a", time.Now().Add(3*time.Hour).Unix())}, job)
	if e != nil || out.CredentialRevision != a.CredentialRevision+1 || out.LastLoginJobID != job {
		t.Fatalf("same-owner relogin failed: %v", e)
	}
	if _, _, e = lease.ReplaceCredential(0, Credential{Auth: grantAuth("u", "a", time.Now().Add(time.Hour).Unix())}, job); e != nil {
		t.Fatalf("replayed relogin not idempotent: %v", e)
	}
	// Park the login to test custody independently from the active selection guard.
	_, e = s.mutate(r.Revision, func(r *Registry) error {
		p := r.Providers[ProviderCodex]
		p.ActiveID = ""
		r.Providers[ProviderCodex] = p
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Remove(snapshot(t, s).Revision, a.ID); !errors.Is(e, ErrInUse) {
		t.Fatalf("removed leased grant: %v", e)
	}
	if e = lease.Close(); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Remove(snapshot(t, s).Revision, a.ID); e != nil {
		t.Fatalf("remove stopped parked grant: %v", e)
	}
}

func TestCodexGrantDefaultInventoryClosesStartupIdentityGap(t *testing.T) {
	s, a, lease := ownedGrantFixture(t)
	executable, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	child := exec.Command(executable, "-test.run=^TestCodexGrantStartupGapFixture$")
	child.Env = []string{"CODEX_HOME=" + lease.Home(), "SWARM_TEST_AUTH_STARTUP_GAP=1"}
	if e := child.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	// Simulate the supervisor dying after spawn, before RecordNative publishes.
	// Closing its lock releases flock while the native process remains alive.
	if e := lease.lock.Close(); e != nil {
		t.Fatal(e)
	}
	lease.lock = nil
	s.grantWriters = nil
	writers, e := processcontain.EnvironmentWriters([]string{lease.Home()}, nil)
	if e != nil {
		t.Skip("host process inventory unavailable; real containment remains a host qualification gate")
	}
	found := false
	for _, writer := range writers {
		if writer.PID == child.Process.Pid {
			found = true
		}
	}
	if !found {
		t.Fatal("default inventory missed unrecorded exact-home native writer")
	}
	if _, e = s.AcquireCodexGrant(a.ID); !errors.Is(e, ErrInUse) {
		t.Fatalf("startup-gap writer overlapped new owner: %v", e)
	}
}

func TestCodexGrantStartupGapFixture(t *testing.T) {
	if os.Getenv("SWARM_TEST_AUTH_STARTUP_GAP") != "1" {
		return
	}
	if os.Getenv("CODEX_HOME") == "" {
		t.Fatal("fixture has no auth home")
	}
	time.Sleep(10 * time.Second)
}
func TestCodexAuxiliaryQuotaFailureStillAllowsOwnedCleanup(t *testing.T) {
	_, _, lease := ownedGrantFixture(t)
	if e := lease.PrepareAuxiliary("0.163.0"); e != nil {
		t.Fatal(e)
	}
	f, e := os.OpenFile(filepath.Join(lease.Home(), "state_5.sqlite"), os.O_CREATE|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	e = f.Truncate(maxCodexAuxBytes + 1)
	_ = f.Close()
	if e != nil {
		t.Fatal(e)
	}
	if e = lease.CheckAuxiliary(); !errors.Is(e, ErrIneligible) {
		t.Fatalf("scratch quota not enforced: %v", e)
	}
	if e = lease.CleanupAuxiliary(); e != nil {
		t.Fatalf("over-quota owned scratch cannot be retired: %v", e)
	}
}
func TestCodexLeaseReloginMetadataAheadReconciles(t *testing.T) {
	s, a, lease := ownedGrantFixture(t)
	job := "00112233445566778899aabbccddeeff"
	s.writeOps.rename = func(old, new string) error {
		if new == "registry.json" {
			return errors.New("injected registry commit failure")
		}
		return s.root.Rename(old, new)
	}
	_, _, e := lease.ReplaceCredential(snapshot(t, s).Revision, Credential{Auth: grantAuth("u", "a", time.Now().Add(3*time.Hour).Unix())}, job)
	if e == nil {
		t.Fatal("registry commit failure was ignored")
	}
	s.writeOps = writeOps{}
	if e = s.Reconcile(); e != nil {
		t.Fatalf("metadata-ahead canonical relogin cannot recover: %v", e)
	}
	r := snapshot(t, s)
	current := find(&r, a.ID)
	if current.CredentialRevision != a.CredentialRevision+1 || current.LastLoginJobID != job || current.AuthState != AuthReady {
		t.Fatal("recovery lost relogin receipt or revision")
	}
	if _, e = s.readSaved(*current); e != nil {
		t.Fatal(e)
	}
}

func TestCodexReadOnlyLeaseCloseDoesNotDependOnLaterInventory(t *testing.T) {
	s, a, lease := ownedGrantFixture(t)
	s.grantWriters = func([]string, []string) ([]processcontain.Identity, error) { return nil, processcontain.ErrUnavailable }
	if e := lease.Close(); e != nil {
		t.Fatalf("read-only access leaked custody on unrelated later inventory failure: %v", e)
	}
	s.grantWriters = func([]string, []string) ([]processcontain.Identity, error) { return nil, nil }
	next, e := s.AcquireCodexGrant(a.ID)
	if e != nil {
		t.Fatal(e)
	}
	_ = next.Close()
}
func TestCodexOwnershipCaptureRejectsForeignUserInSameWorkspace(t *testing.T) {
	s := testStore(t)
	a := add(t, s, ProviderCodex, "a", Credential{Auth: grantAuth("original-user", "a", time.Now().Add(time.Hour).Unix())})
	activate(t, s, a)
	r := snapshot(t, s)
	a = *find(&r, a.ID)
	op, e := s.BeginCodexOwnershipMigration(r.Revision, nil)
	if e != nil {
		t.Fatal(e)
	}
	op.Phase = "stopping"
	if e = s.SaveSwitch(op); e != nil {
		t.Fatal(e)
	}
	op.Phase = "stopped"
	if e = s.SaveSwitch(op); e != nil {
		t.Fatal(e)
	}
	if e = s.installCredential(ProviderCodex, Credential{Auth: grantAuth("foreign-user", "a", time.Now().Add(2*time.Hour).Unix())}); e != nil {
		t.Fatal(e)
	}
	if e = s.CaptureOutgoing(op.ID); !errors.Is(e, ErrIdentityChanged) {
		t.Fatalf("foreign user replaced enrolled saved grant: %v", e)
	}
	saved, e := s.readSaved(a)
	if e != nil {
		t.Fatal(e)
	}
	got, e := CodexGrantOwner(saved.Auth)
	if e != nil {
		t.Fatal(e)
	}
	want, _ := CodexGrantOwner(grantAuth("original-user", "a", time.Now().Add(time.Hour).Unix()))
	if got != want {
		t.Fatal("failed ownership migration changed saved owner")
	}
}
func TestCodexQualifiedSQLiteStartupJournalIsOwnedAuxiliary(t *testing.T) {
	_, _, lease := ownedGrantFixture(t)
	if e := lease.PrepareAuxiliary("0.163.0"); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(lease.Home(), "state_5.sqlite-journal")
	if e := os.WriteFile(path, make([]byte, 512), 0600); e != nil {
		t.Fatal(e)
	}
	if e := lease.CheckAuxiliary(); e != nil {
		t.Fatalf("qualified native startup rollback journal rejected: %v", e)
	}
	unknown := filepath.Join(lease.Home(), "other.sqlite-journal")
	if e := os.WriteFile(unknown, []byte("unknown"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := lease.CleanupAuxiliary(); !errors.Is(e, errUnknownSavedData) {
		t.Fatalf("unknown SQLite journal root accepted: %v", e)
	}
	if _, e := os.Stat(unknown); e != nil {
		t.Fatal("unknown journal erased")
	}
	if e := os.Remove(unknown); e != nil {
		t.Fatal(e)
	}
	if e := lease.CleanupAuxiliary(); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(path); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("qualified rollback journal not retired")
	}
}
