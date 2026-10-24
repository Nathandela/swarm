package logins

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func unavailableCodexFixture(t *testing.T) (*Store, Login) {
	t.Helper()
	s, _, lease := ownedGrantFixture(t)
	_ = lease.Close()
	missing := Login{ID: "00112233445566778899aabbccddeeff", Provider: ProviderCodex, Label: "needs native login", Identity: identityDigest(ProviderCodex, "missing"), AuthState: AuthNeedsLogin}
	_, missing, e := s.ImportUnavailable(snapshot(t, s).Revision, missing)
	if e != nil {
		t.Fatal(e)
	}
	return s, missing
}
func TestCodexUnavailableReloginBindsOnlyExplicitSameWorkspace(t *testing.T) {
	s, missing := unavailableCodexFixture(t)
	job := "11223344556677889900aabbccddeeff"
	lease, e := s.AcquireCodexRelogin(missing.ID, job)
	if e != nil {
		t.Fatal(e)
	}
	defer lease.Close()
	if _, _, e = lease.ReplaceCredential(snapshot(t, s).Revision, Credential{Auth: grantAuth("u", "foreign", time.Now().Add(time.Hour).Unix())}, job); !errors.Is(e, ErrIdentityChanged) {
		t.Fatalf("foreign workspace bound first owner: %v", e)
	}
	r, out, e := lease.ReplaceCredential(snapshot(t, s).Revision, Credential{Auth: grantAuth("first-user", "missing", time.Now().Add(time.Hour).Unix())}, job)
	if e != nil {
		t.Fatal(e)
	}
	if out.AuthState != AuthReady || out.LastLoginJobID != job || out.CredentialRevision != missing.CredentialRevision+1 {
		t.Fatal("first-owner enrollment receipt lost")
	}
	if _, _, e = lease.ReplaceCredential(r.Revision, Credential{Auth: grantAuth("different-user", "missing", time.Now().Add(time.Hour).Unix())}, "ffeeddccbbaa00998877665544332211"); !errors.Is(e, ErrIdentityChanged) {
		t.Fatalf("known first owner silently replaced: %v", e)
	}
}
func TestCodexUnavailableReloginRefusesUnexpectedAuthority(t *testing.T) {
	s, missing := unavailableCodexFixture(t)
	if e := os.WriteFile(filepath.Join(s.savedNativeHome(missing.ID), "auth.json"), grantAuth("u", "missing", time.Now().Add(time.Hour).Unix()), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := s.AcquireCodexRelogin(missing.ID, "11223344556677889900aabbccddeeff"); e == nil {
		t.Fatal("unexpected canonical authority was inferred as first owner")
	}
}
func TestCodexReloginIntentFailureCutsFenceEveryTokenPath(t *testing.T) {
	for _, cut := range []string{"intent", "auth", "metadata", "registry", "retire"} {
		t.Run(cut, func(t *testing.T) {
			s, missing := unavailableCodexFixture(t)
			job := "11223344556677889900aabbccddeeff"
			lease, e := s.AcquireCodexRelogin(missing.ID, job)
			if e != nil {
				t.Fatal(e)
			}
			target := filepath.Join("saved", missing.ID)
			s.writeOps.rename = func(old, new string) error {
				fail := cut == "intent" && new == filepath.Join(target, grantReloginFile) || cut == "auth" && new == filepath.Join(target, "auth.json") || cut == "metadata" && new == filepath.Join(target, "credential.json") || cut == "registry" && new == "registry.json"
				if fail {
					return errors.New("injected write failure")
				}
				return s.root.Rename(old, new)
			}
			s.writeOps.remove = func(name string) error {
				if cut == "retire" && name == filepath.Join(target, grantReloginFile) {
					return errors.New("injected retirement failure")
				}
				return s.root.Remove(name)
			}
			credential := Credential{Auth: grantAuth("first-user", "missing", time.Now().Add(time.Hour).Unix())}
			if _, _, e = lease.ReplaceCredential(snapshot(t, s).Revision, credential, job); e == nil {
				t.Fatal("failure cut ignored")
			}
			s.writeOps = writeOps{}
			if cut != "intent" {
				if _, e = lease.Access(time.Now()); !errors.Is(e, ErrPending) {
					t.Fatalf("same-daemon token read crossed pending intent: %v", e)
				}
				if _, e = lease.Generation(); !errors.Is(e, ErrPending) {
					t.Fatalf("refresh generation crossed pending intent: %v", e)
				}
			}
			if e = lease.Close(); e != nil {
				t.Fatal(e)
			}
			if cut != "intent" {
				if _, e = s.AcquireCodexGrant(missing.ID); !errors.Is(e, ErrPending) {
					t.Fatalf("normal acquisition crossed pending intent: %v", e)
				}
			}
			if e = s.Reconcile(); e != nil {
				t.Fatal(e)
			}
			r := snapshot(t, s)
			current := find(&r, missing.ID)
			if cut == "metadata" || cut == "registry" || cut == "retire" {
				if current.LastLoginJobID != job || current.AuthState != AuthReady {
					t.Fatal("durable native grant did not recover exact job receipt")
				}
			} else {
				if current.LastLoginJobID != "" {
					t.Fatal("pre-auth failure acknowledged enrollment")
				}
				retry, e := s.AcquireCodexRelogin(missing.ID, job)
				if e != nil {
					t.Fatal(e)
				}
				if _, _, e = retry.ReplaceCredential(r.Revision, credential, job); e != nil {
					t.Fatal(e)
				}
				_ = retry.Close()
			}
		})
	}
}
func TestCodexReloginDuplicateJobNeverReplaysAfterNativeRenewal(t *testing.T) {
	s, missing := unavailableCodexFixture(t)
	job := "11223344556677889900aabbccddeeff"
	lease, e := s.AcquireCodexRelogin(missing.ID, job)
	if e != nil {
		t.Fatal(e)
	}
	defer lease.Close()
	stale := Credential{Auth: grantAuth("u", "missing", time.Now().Add(time.Hour).Unix())}
	if _, _, e = lease.ReplaceCredential(snapshot(t, s).Revision, stale, job); e != nil {
		t.Fatal(e)
	}
	newer := grantAuth("u", "missing", time.Now().Add(4*time.Hour).Unix())
	path := filepath.Join(lease.Home(), "auth.json")
	if e = os.WriteFile(path, newer, 0600); e != nil {
		t.Fatal(e)
	}
	if _, _, e = lease.ReplaceCredential(0, Credential{}, job); e != nil {
		t.Fatalf("durable job ack depended on erased enrollment credential: %v", e)
	}
	got, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(got, newer) {
		t.Fatal("duplicate enrollment replayed stale refresh grant over native renewal")
	}
}

func TestCodexPendingIntentFencesExistingActiveReaderAndNativeCapture(t *testing.T) {
	s, a, lease := ownedGrantFixture(t)
	job := "11223344556677889900aabbccddeeff"
	s.writeOps.rename = func(old, new string) error {
		if new == filepath.Join("saved", a.ID, "auth.json") {
			return errors.New("before auth write")
		}
		return s.root.Rename(old, new)
	}
	if _, _, e := lease.ReplaceCredential(snapshot(t, s).Revision, Credential{Auth: grantAuth("u", "a", time.Now().Add(2*time.Hour).Unix())}, job); e == nil {
		t.Fatal("missing failure cut")
	}
	s.writeOps = writeOps{}
	if _, e := s.Runtime(ProviderCodex); !errors.Is(e, ErrPending) {
		t.Fatalf("runtime crossed pending intent: %v", e)
	}
	if _, e := ReadNativeCredential(ProviderCodex, lease.Home()); !errors.Is(e, ErrPending) {
		t.Fatalf("native capture crossed pending intent: %v", e)
	}
	if e := EraseNativeCredential(ProviderCodex, lease.Home()); !errors.Is(e, ErrPending) {
		t.Fatalf("native erasure crossed pending intent: %v", e)
	}
	if e := s.VerifySaved(a.ID); !errors.Is(e, ErrPending) {
		t.Fatalf("saved verification crossed pending intent: %v", e)
	}
	if e := lease.PrepareAuxiliary("0.163.0"); !errors.Is(e, ErrPending) {
		t.Fatalf("native refresher admitted pending enrollment: %v", e)
	}
	if _, e := s.UpdateAvailability(snapshot(t, s).Revision, a.ID, AuthReady, false, time.Time{}); !errors.Is(e, ErrPending) {
		t.Fatalf("old availability changed pending enrollment: %v", e)
	}
	if e := s.Reconcile(); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(filepath.Join(lease.Home(), grantReloginFile)); e != nil {
		t.Fatal("mismatched old canonical generation lost intent")
	}
	if got := snapshot(t, s).Logins[0].LastLoginJobID; got == job {
		t.Fatal("old canonical grant falsely acknowledged new enrollment")
	}
}
func TestCodexPendingFirstOwnerMismatchStaysPendingWithoutBlockingOtherLogin(t *testing.T) {
	s, missing := unavailableCodexFixture(t)
	job := "11223344556677889900aabbccddeeff"
	lease, e := s.AcquireCodexRelogin(missing.ID, job)
	if e != nil {
		t.Fatal(e)
	}
	s.writeOps.rename = func(old, new string) error {
		if new == "registry.json" {
			return errors.New("before registry")
		}
		return s.root.Rename(old, new)
	}
	if _, _, e = lease.ReplaceCredential(snapshot(t, s).Revision, Credential{Auth: grantAuth("u", "missing", time.Now().Add(time.Hour).Unix())}, job); e == nil {
		t.Fatal("missing failure cut")
	}
	s.writeOps = writeOps{}
	_ = lease.Close()
	unexpected := grantAuth("u", "missing", time.Now().Add(5*time.Hour).Unix())
	path := filepath.Join(s.savedNativeHome(missing.ID), "auth.json")
	if e = os.WriteFile(path, unexpected, 0600); e != nil {
		t.Fatal(e)
	}
	if e = s.Reconcile(); e != nil {
		t.Fatal(e)
	}
	r := snapshot(t, s)
	if find(&r, missing.ID).LastLoginJobID != "" {
		t.Fatal("mismatched generation acknowledged enrollment")
	}
	if _, e = s.AcquireCodexGrant(missing.ID); !errors.Is(e, ErrPending) {
		t.Fatalf("mismatched pending authority became usable: %v", e)
	}
	if _, e = s.Runtime(ProviderCodex); e != nil {
		t.Fatalf("unrelated active login blocked by parked pending enrollment: %v", e)
	}
	actual, e := os.ReadFile(path)
	if e != nil || !bytes.Equal(actual, unexpected) {
		t.Fatal("reconcile replayed or erased unexpected canonical authority")
	}
}
func TestCodexCommittedIntentRetiresWithoutReplayingLaterRenewal(t *testing.T) {
	s, missing := unavailableCodexFixture(t)
	job := "11223344556677889900aabbccddeeff"
	lease, e := s.AcquireCodexRelogin(missing.ID, job)
	if e != nil {
		t.Fatal(e)
	}
	s.writeOps.remove = func(string) error { return errors.New("after registry before retirement") }
	if _, _, e = lease.ReplaceCredential(snapshot(t, s).Revision, Credential{Auth: grantAuth("u", "missing", time.Now().Add(time.Hour).Unix())}, job); e == nil {
		t.Fatal("missing failure cut")
	}
	s.writeOps = writeOps{}
	_ = lease.Close()
	newer := grantAuth("u", "missing", time.Now().Add(4*time.Hour).Unix())
	path := filepath.Join(s.savedNativeHome(missing.ID), "auth.json")
	if e = os.WriteFile(path, newer, 0600); e != nil {
		t.Fatal(e)
	}
	if e = s.Reconcile(); e != nil {
		t.Fatal(e)
	}
	r := snapshot(t, s)
	if find(&r, missing.ID).LastLoginJobID != job {
		t.Fatal("committed job acknowledgement lost")
	}
	actual, e := os.ReadFile(path)
	if e != nil || !bytes.Equal(actual, newer) {
		t.Fatal("committed intent replayed stale enrollment grant")
	}
}
func TestCodexFirstOwnerIntentFencesSchemaBeforeAuthorityChange(t *testing.T) {
	s, missing := unavailableCodexFixture(t)
	_, e := s.mutate(snapshot(t, s).Revision, func(r *Registry) error {
		r.SchemaVersion = 2
		r.Handovers = nil
		p := r.Providers[ProviderCodex]
		p.DesiredCredentialRevision = 0
		r.Providers[ProviderCodex] = p
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	lease, e := s.AcquireCodexRelogin(missing.ID, "11223344556677889900aabbccddeeff")
	if e != nil {
		t.Fatal(e)
	}
	defer lease.Close()
	s.writeOps.rename = func(old, new string) error {
		if new == filepath.Join("saved", missing.ID, grantReloginFile) {
			return errors.New("before intent")
		}
		return s.root.Rename(old, new)
	}
	if _, _, e = lease.ReplaceCredential(snapshot(t, s).Revision, Credential{Auth: grantAuth("u", "missing", time.Now().Add(time.Hour).Unix())}, "11223344556677889900aabbccddeeff"); e == nil {
		t.Fatal("missing intent failure")
	}
	s.writeOps = writeOps{}
	if snapshot(t, s).SchemaVersion != 3 {
		t.Fatal("new authority meaning was not fenced before intent")
	}
	if _, e = os.Stat(filepath.Join(lease.Home(), "auth.json")); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("authority changed before durable schema/intent")
	}
}
