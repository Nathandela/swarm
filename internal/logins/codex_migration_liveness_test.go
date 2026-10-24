package logins

import (
	"errors"
	"github.com/Nathandela/swarm/internal/processcontain"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func stopOwnershipMigration(t *testing.T, s *Store, op Switch) {
	t.Helper()
	op.Phase = "stopping"
	if e := s.SaveSwitch(op); e != nil {
		t.Fatal(e)
	}
	op.Phase = "stopped"
	for i := range op.Sessions {
		op.Sessions[i].Phase = "stopped"
	}
	if e := s.SaveSwitch(op); e != nil {
		t.Fatal(e)
	}
	if e := s.CaptureOutgoing(op.ID); e != nil {
		t.Fatal(e)
	}
}
func TestCodexOwnershipMigrationPreservesUnavailableWithoutAuthority(t *testing.T) {
	s := testStore(t)
	s.grantWriters = func([]string, []string) ([]processcontain.Identity, error) { return nil, nil }
	a := add(t, s, ProviderCodex, "active", Credential{Auth: grantAuth("u", "a", time.Now().Add(time.Hour).Unix())})
	activate(t, s, a)
	missing := Login{ID: "00112233445566778899aabbccddeeff", Provider: ProviderCodex, Label: "needs browser login", Identity: identityDigest(ProviderCodex, "missing"), AuthState: AuthNeedsLogin}
	_, missing, e := s.ImportUnavailable(snapshot(t, s).Revision, missing)
	if e != nil {
		t.Fatal(e)
	}
	op, e := s.BeginCodexOwnershipMigration(snapshot(t, s).Revision, nil)
	if e != nil {
		t.Fatal(e)
	}
	stopOwnershipMigration(t, s, op)
	if e = s.MigrateCodexOwnership(op.ID); e != nil {
		t.Fatalf("unavailable parked login blocked provider migration: %v", e)
	}
	if _, e = s.CommitSwitch(op.ID); e != nil {
		t.Fatal(e)
	}
	if e = s.CompleteSwitch(op.ID); e != nil {
		t.Fatal(e)
	}
	r := snapshot(t, s)
	got := find(&r, missing.ID)
	if got == nil || *got != missing {
		t.Fatal("migration changed unavailable login metadata")
	}
	if e = s.VerifySaved(missing.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(s.savedNativeHome(missing.ID), "auth.json")); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("unavailable login gained grant authority")
	}
	if _, e = s.Remove(r.Revision, missing.ID); e != nil {
		t.Fatalf("unavailable parked login cannot be removed: %v", e)
	}
}
func TestCodexOwnershipMigrationSelectsRequestedDestination(t *testing.T) {
	s := testStore(t)
	a := add(t, s, ProviderCodex, "A", Credential{Auth: grantAuth("u-a", "a", time.Now().Add(time.Hour).Unix())})
	b := add(t, s, ProviderCodex, "B", Credential{Auth: grantAuth("u-b", "b", time.Now().Add(time.Hour).Unix())})
	activate(t, s, a)
	op, e := s.BeginCodexOwnershipMigrationTo(snapshot(t, s).Revision, b.ID, []Restart{{SourceID: "session", ConversationID: "thread"}})
	if e != nil {
		t.Fatal(e)
	}
	if op.FromID != a.ID || op.ToID != b.ID || !op.OwnershipMigration || op.Relogin {
		t.Fatal("requested target lost or misclassified")
	}
	stopOwnershipMigration(t, s, op)
	if e = s.MigrateCodexOwnership(op.ID); e != nil {
		t.Fatal(e)
	}
	rt, e := s.CommitSwitch(op.ID)
	if e != nil {
		t.Fatal(e)
	}
	r := snapshot(t, s)
	if !rt.ExternalAuth || r.Providers[ProviderCodex].ActiveID != b.ID || len(r.Switches[ProviderCodex].Sessions) != 1 {
		t.Fatal("initial target selection or original restart obligations lost")
	}
}
func TestCodexRemoveRetiresOnlyStoppedOwnedAuxiliary(t *testing.T) {
	s, a, lease := ownedGrantFixture(t)
	if e := lease.PrepareAuxiliary("0.163.0"); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(lease.Home(), "state_5.sqlite"), []byte("owned helper state"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := lease.Close(); e != nil {
		t.Fatal(e)
	}
	_, e := s.mutate(snapshot(t, s).Revision, func(r *Registry) error {
		p := r.Providers[ProviderCodex]
		p.ActiveID = ""
		r.Providers[ProviderCodex] = p
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Remove(snapshot(t, s).Revision, a.ID); e != nil {
		t.Fatalf("stale owned helper auxiliary blocked removal: %v", e)
	}
}
func TestCodexOwnershipMigrationNeverDowngradesSchemaFence(t *testing.T) {
	s := testStore(t)
	a := add(t, s, ProviderCodex, "A", Credential{Auth: grantAuth("u", "a", time.Now().Add(time.Hour).Unix())})
	activate(t, s, a)
	r, e := s.mutate(snapshot(t, s).Revision, func(r *Registry) error { schemaForHandovers(r); return nil })
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.BeginCodexOwnershipMigrationTo(r.Revision, a.ID, nil); e != nil {
		t.Fatal(e)
	}
	if snapshot(t, s).SchemaVersion != 3 {
		t.Fatal("ownership migration reopened an older binary schema")
	}
}
