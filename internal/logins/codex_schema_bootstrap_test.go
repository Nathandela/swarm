package logins

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/processcontain"
)

// This writes the former schema-two native-owned layout, then reopens it. It
// deliberately avoids current CommitSwitch, which already records the baseline.
func persistedSchemaTwoOwner(t *testing.T) (*Store, Login) {
	t.Helper()
	s := testStore(t)
	a := add(t, s, ProviderCodex, "schema-two A", Credential{Auth: grantAuth("u", "a", time.Now().Add(time.Hour).Unix())})
	activate(t, s, a)
	r := snapshot(t, s)
	a = *find(&r, a.ID)
	record, e := s.readSavedRecord(a)
	if e != nil {
		t.Fatal(e)
	}
	owner, e := CodexGrantOwner(record.Credential.Auth)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(s.savedNativeHome(a.ID), "auth.json"), record.Credential.Auth, 0600); e != nil {
		t.Fatal(e)
	}
	record.AuthOwner = &grantOwnership{Version: 1, Owner: owner}
	record.Credential.Auth = nil
	encoded, e := json.Marshal(record)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(s.savedNativeHome(a.ID), "credential.json"), encoded, 0600); e != nil {
		t.Fatal(e)
	}
	if e = EraseNativeCredential(ProviderCodex, s.Home(ProviderCodex)); e != nil {
		t.Fatal(e)
	}
	r.SchemaVersion = 2
	r.Handovers = nil
	p := r.Providers[ProviderCodex]
	p.AuthOwnerMode = CodexNativeGrantMode
	p.DesiredCredentialRevision = 0
	r.Providers[ProviderCodex] = p
	encoded, e = json.Marshal(r)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(s.path, "registry.json"), encoded, 0600); e != nil {
		t.Fatal(e)
	}
	stateRoot := s.StateRoot()
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	reopened, e := Open(stateRoot)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reopened.grantWriters = func([]string, []string) ([]processcontain.Identity, error) { return nil, nil }
	return reopened, a
}
func TestCodexPersistedSchemaTwoReloginCapturesPrechangeBaseline(t *testing.T) {
	s, a := persistedSchemaTwoOwner(t)
	before := snapshot(t, s)
	if before.SchemaVersion != 2 || before.Providers[ProviderCodex].DesiredCredentialRevision != 0 {
		t.Fatal("fixture did not preserve old on-disk layout")
	}
	job := "11223344556677889900aabbccddeeff"
	lease, e := s.AcquireCodexRelogin(a.ID, job)
	if e != nil {
		t.Fatal(e)
	}
	r, updated, e := lease.ReplaceCredential(before.Revision, Credential{Auth: grantAuth("u", "a", time.Now().Add(3*time.Hour).Unix())}, job)
	if e != nil {
		t.Fatal(e)
	}
	_ = lease.Close()
	if r.SchemaVersion != 3 || r.Providers[ProviderCodex].DesiredCredentialRevision != a.CredentialRevision {
		t.Fatal("upgrade failed to retain the pre-relogin desired credential baseline")
	}
	if _, e = s.Runtime(ProviderCodex); !errors.Is(e, ErrPending) {
		t.Fatalf("active reauth escaped under old epoch: %v", e)
	}
	_, op, e := s.BeginCodexHandover(r.Revision, a.ID, []NativeMember{handoverNative("s", "native")})
	if e != nil {
		t.Fatal(e)
	}
	if op.DestinationCredentialRevision != updated.CredentialRevision || op.Epoch != before.Providers[ProviderCodex].Epoch+1 {
		t.Fatal("same-ID desired revision handover lost upgrade baseline")
	}
}
func TestCodexSchemaThreeZeroBaselineIsExplicitlyUnbootstrapped(t *testing.T) {
	s, _ := persistedSchemaTwoOwner(t)
	if _, e := s.mutate(snapshot(t, s).Revision, func(r *Registry) error { schemaForHandovers(r); return nil }); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Runtime(ProviderCodex); !errors.Is(e, ErrPending) {
		t.Fatalf("zero baseline was treated as current authority: %v", e)
	}
}

func TestCodexZeroBaselineBootstrapIsAnExplicitPendingCohort(t *testing.T) {
	s, a := persistedSchemaTwoOwner(t)
	before := snapshot(t, s)
	if _, _, e := s.BeginCodexHandover(before.Revision, a.ID, nil); !errors.Is(e, ErrPending) {
		t.Fatalf("ordinary same-ID call inferred unknown baseline: %v", e)
	}
	r, op, e := s.BootstrapCodexHandover(before.Revision, []NativeMember{handoverNative("s", "native")})
	if e != nil {
		t.Fatal(e)
	}
	if r.SchemaVersion != 3 || op.Epoch != before.Providers[ProviderCodex].Epoch+1 || op.DestinationCredentialRevision != a.CredentialRevision || op.Members[0].Phase != "pending" {
		t.Fatal("bootstrap implied adoption or reused old epoch")
	}
	if _, e = s.Runtime(ProviderCodex); e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.BootstrapCodexHandover(r.Revision, nil); !errors.Is(e, ErrPending) {
		t.Fatalf("duplicate bootstrap bypassed current cohort: %v", e)
	}
}
func TestCodexSchemaThreeZeroReloginAlsoCapturesPrechangeBaseline(t *testing.T) {
	s, a := persistedSchemaTwoOwner(t)
	_, e := s.mutate(snapshot(t, s).Revision, func(r *Registry) error { schemaForHandovers(r); return nil })
	if e != nil {
		t.Fatal(e)
	}
	job := "11223344556677889900aabbccddeeff"
	lease, e := s.AcquireCodexRelogin(a.ID, job)
	if e != nil {
		t.Fatal(e)
	}
	defer lease.Close()
	r, _, e := lease.ReplaceCredential(snapshot(t, s).Revision, Credential{Auth: grantAuth("u", "a", time.Now().Add(3*time.Hour).Unix())}, job)
	if e != nil {
		t.Fatal(e)
	}
	if r.Providers[ProviderCodex].DesiredCredentialRevision != a.CredentialRevision {
		t.Fatal("schema-three zero skipped baseline repair")
	}
	if _, e = s.Runtime(ProviderCodex); !errors.Is(e, ErrPending) {
		t.Fatalf("old epoch authorized new revision: %v", e)
	}
}
func TestCodexCachedTokenPendingCheckIsMetadataOnlyAndRestartCompatible(t *testing.T) {
	s, a, lease := ownedGrantFixture(t)
	_ = lease.Close()
	if e := s.CheckCodexGrantPending(a.ID); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(s.savedNativeHome(a.ID), grantReloginFile), []byte("corrupt intent"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := s.CheckCodexGrantPending(a.ID); !errors.Is(e, ErrPending) {
		t.Fatalf("cached credential ignored corrupt/pending intent: %v", e)
	}
}
func TestCodexLegacyCommittedIntentKeepsBaselineBeforeRetirement(t *testing.T) {
	for _, reconcile := range []bool{false, true} {
		t.Run(map[bool]string{false: "explicit-job-retry", true: "reconcile"}[reconcile], func(t *testing.T) {
			s, a := persistedSchemaTwoOwner(t)
			job := "11223344556677889900aabbccddeeff"
			lease, e := s.AcquireCodexRelogin(a.ID, job)
			if e != nil {
				t.Fatal(e)
			}
			s.writeOps.remove = func(string) error { return errors.New("before intent retirement") }
			if _, _, e = lease.ReplaceCredential(snapshot(t, s).Revision, Credential{Auth: grantAuth("u", "a", time.Now().Add(3*time.Hour).Unix())}, job); e == nil {
				t.Fatal("missing retirement cut")
			}
			s.writeOps = writeOps{}
			_ = lease.Close()
			_, e = s.mutate(snapshot(t, s).Revision, func(r *Registry) error {
				p := r.Providers[ProviderCodex]
				p.DesiredCredentialRevision = 0
				r.Providers[ProviderCodex] = p
				return nil
			})
			if e != nil {
				t.Fatal(e)
			}
			if reconcile {
				if e = s.Reconcile(); e != nil {
					t.Fatal(e)
				}
			} else {
				retry, e := s.AcquireCodexRelogin(a.ID, job)
				if e != nil {
					t.Fatal(e)
				}
				if _, _, e = retry.ReplaceCredential(0, Credential{}, job); e != nil {
					t.Fatal(e)
				}
				_ = retry.Close()
			}
			r := snapshot(t, s)
			if r.Providers[ProviderCodex].DesiredCredentialRevision != a.CredentialRevision {
				t.Fatal("retirement discarded the only pre-change baseline proof")
			}
			if _, e = s.Runtime(ProviderCodex); !errors.Is(e, ErrPending) {
				t.Fatalf("recovered new grant escaped old epoch: %v", e)
			}
			if _, _, e = s.BeginCodexHandover(r.Revision, a.ID, []NativeMember{handoverNative("s", "native")}); e != nil {
				t.Fatal(e)
			}
		})
	}
}
func TestCodexPendingMetadataCheckAllowsOwnedLegacyRestart(t *testing.T) {
	s := testStore(t)
	a := add(t, s, ProviderCodex, "A", Credential{Auth: grantAuth("u", "a", time.Now().Add(time.Hour).Unix())})
	activate(t, s, a)
	op, e := s.BeginCodexOwnershipMigration(snapshot(t, s).Revision, []Restart{{SourceID: "source", ConversationID: "thread"}})
	if e != nil {
		t.Fatal(e)
	}
	stopOwnershipMigration(t, s, op)
	if e = s.MigrateCodexOwnership(op.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.CommitSwitch(op.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Runtime(ProviderCodex); !errors.Is(e, ErrPending) {
		t.Fatal("fixture did not preserve legacy restart barrier")
	}
	if e = s.CheckCodexGrantPending(a.ID); e != nil {
		t.Fatalf("metadata token gate blocked owned committed restart: %v", e)
	}
}
