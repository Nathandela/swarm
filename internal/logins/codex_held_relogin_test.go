package logins

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func heldReloginFixture(t *testing.T) (*Store, Login, Handover) {
	t.Helper()
	s, _, lease := ownedGrantFixture(t)
	_ = lease.Close()
	b := add(t, s, ProviderCodex, "B", Credential{Auth: grantAuth("u-b", "b", time.Now().Add(time.Hour).Unix())})
	_, h, e := s.BeginCodexHandover(snapshot(t, s).Revision, b.ID, []NativeMember{handoverNative("s", "native")})
	if e != nil {
		t.Fatal(e)
	}
	_, h, e = s.RecordCodexReceipt(snapshot(t, s).Revision, handoverReceipt(h, h.Members[0].Native, "adopted"))
	if e != nil {
		t.Fatal(e)
	}
	return s, b, h
}
func TestCodexDesiredHeldReloginAdvancesSelectionOnce(t *testing.T) {
	s, b, old := heldReloginFixture(t)
	for _, login := range snapshot(t, s).Logins {
		if _, e := s.UpdateAvailability(snapshot(t, s).Revision, login.ID, AuthNeedsLogin, true, time.Now().Add(time.Hour)); e != nil {
			t.Fatal(e)
		}
	}
	job := "11223344556677889900aabbccddeeff"
	lease, e := s.AcquireCodexRelogin(b.ID, job)
	if e != nil {
		t.Fatal(e)
	}
	defer lease.Close()
	r, updated, e := lease.ReplaceCredential(snapshot(t, s).Revision, Credential{Auth: grantAuth("u-b", "b", time.Now().Add(2*time.Hour).Unix())}, job)
	if e != nil {
		t.Fatal(e)
	}
	h := r.Handovers[ProviderCodex]
	if updated.AuthState != AuthReady || updated.LimitKnown {
		t.Fatal("explicit desired reauth did not restore availability")
	}
	if r.SchemaVersion != 4 || h.ID != old.ID || h.Epoch != old.Epoch+1 || h.DestinationCredentialRevision != updated.CredentialRevision || h.Members[0].Phase != "pending" || h.Members[0].AdoptedEpoch != old.Epoch {
		t.Fatal("held reauth lost selection or prior adoption")
	}
	if _, _, e = s.RecordCodexReceipt(r.Revision, handoverReceipt(old, old.Members[0].Native, "adopted")); !errors.Is(e, ErrConflict) {
		t.Fatal("old revision receipt accepted")
	}
	renewed := grantAuth("u-b", "b", time.Now().Add(4*time.Hour).Unix())
	if e = os.WriteFile(filepath.Join(lease.Home(), "auth.json"), renewed, 0600); e != nil {
		t.Fatal(e)
	}
	r, _, e = lease.ReplaceCredential(0, Credential{}, job)
	if e != nil {
		t.Fatal(e)
	}
	if r.Handovers[ProviderCodex].Epoch != h.Epoch {
		t.Fatal("duplicate job reset cohort")
	}
	raw, _ := os.ReadFile(filepath.Join(lease.Home(), "auth.json"))
	if !bytes.Equal(raw, renewed) {
		t.Fatal("duplicate job replayed enrollment")
	}
}
func TestCodexHeldReloginFaultCutsRecoverSelectionAtomically(t *testing.T) {
	for _, cut := range []string{"intent", "auth", "metadata", "registry", "retire"} {
		t.Run(cut, func(t *testing.T) {
			s, b, h := heldReloginFixture(t)
			if _, e := s.mutate(snapshot(t, s).Revision, func(r *Registry) error { r.SchemaVersion = 4; return nil }); e != nil {
				t.Fatal(e)
			}
			job := "11223344556677889900aabbccddeeff"
			lease, e := s.AcquireCodexRelogin(b.ID, job)
			if e != nil {
				t.Fatal(e)
			}
			path := filepath.Join("saved", b.ID)
			s.writeOps.rename = func(old, new string) error {
				if cut == "intent" && new == filepath.Join(path, grantReloginFile) || cut == "auth" && new == filepath.Join(path, "auth.json") || cut == "metadata" && new == filepath.Join(path, "credential.json") || cut == "registry" && new == "registry.json" {
					return errors.New("injected cut")
				}
				return s.root.Rename(old, new)
			}
			s.writeOps.remove = func(name string) error {
				if cut == "retire" && name == filepath.Join(path, grantReloginFile) {
					return errors.New("injected cut")
				}
				return s.root.Remove(name)
			}
			credential := Credential{Auth: grantAuth("u-b", "b", time.Now().Add(2*time.Hour).Unix())}
			if _, _, e = lease.ReplaceCredential(snapshot(t, s).Revision, credential, job); e == nil {
				t.Fatal("cut ignored")
			}
			s.writeOps = writeOps{}
			_ = lease.Close()
			if cut != "intent" {
				r := snapshot(t, s)
				if _, _, e = s.RetargetCodexHandover(r.Revision, h.ID, b.ID); !errors.Is(e, ErrPending) {
					t.Fatalf("mutation crossed pending intent: %v", e)
				}
				if _, _, e = s.RecordCodexReceipt(r.Revision, handoverReceipt(r.Handovers[ProviderCodex], h.Members[0].Native, "adopted")); !errors.Is(e, ErrPending) {
					t.Fatal("receipt crossed intent")
				}
				if _, e = s.CompleteCodexHandover(r.Revision, h.ID); !errors.Is(e, ErrPending) {
					t.Fatal("completion crossed intent")
				}
			}
			reopened, e := Open(s.StateRoot())
			if e != nil {
				t.Fatal(e)
			}
			defer reopened.Close()
			reopened.grantWriters = s.grantWriters
			if e = reopened.Reconcile(); e != nil {
				t.Fatal(e)
			}
			r := snapshot(t, reopened)
			current := find(&r, b.ID)
			if current.LastLoginJobID != job {
				retry, e := reopened.AcquireCodexRelogin(b.ID, job)
				if e != nil {
					t.Fatal(e)
				}
				r, _, e = retry.ReplaceCredential(r.Revision, credential, job)
				if e != nil {
					t.Fatal(e)
				}
				_ = retry.Close()
			}
			current = find(&r, b.ID)
			next := r.Handovers[ProviderCodex]
			if current.CredentialRevision != next.DestinationCredentialRevision || next.Epoch != h.Epoch+1 || r.Providers[ProviderCodex].DesiredCredentialRevision != current.CredentialRevision {
				t.Fatal("recovery split credential and handover revision")
			}
		})
	}
}
func TestCodexMigrationDestinationReloginWaitsForTransfer(t *testing.T) {
	s, _, b, op, child := transferFixture(t)
	job := "11223344556677889900aabbccddeeff"
	lease, e := s.AcquireCodexRelogin(b.ID, job)
	if e != nil {
		t.Fatal(e)
	}
	r, updated, e := lease.ReplaceCredential(snapshot(t, s).Revision, Credential{Auth: grantAuth("u-b", "b", time.Now().Add(2*time.Hour).Unix())}, job)
	if e != nil {
		t.Fatal(e)
	}
	_ = lease.Close()
	if _, e = s.RuntimeForSwitch(op.ID, "unlaunched"); !errors.Is(e, ErrPending) {
		t.Fatal("migration reauth used new credential under old epoch")
	}
	if _, e = s.CommitSwitch(op.ID); !errors.Is(e, ErrPending) {
		t.Fatal("commit retry rebound reauth under old migration epoch")
	}
	if e = s.CompleteSwitch(op.ID); !errors.Is(e, ErrPending) {
		t.Fatal("migration reauth discarded barrier before transfer")
	}
	_, h, e := s.TransferCodexMigration(r.Revision, op.ID, b.ID, []NativeMember{child}, []BootstrapChild{{SourceID: "source", Native: child, Phase: "pending"}})
	if e != nil {
		t.Fatal(e)
	}
	if h.DestinationCredentialRevision != updated.CredentialRevision || h.Epoch != op.ToEpoch+1 {
		t.Fatal("same-target transfer lost explicit reauth revision")
	}
}

func TestCodexBootstrapHeldReloginPreservesHistoricalClaim(t *testing.T) {
	s, _, _, op := committedTransferFixture(t)
	child := handoverNative("other", "native-other")
	c := add(t, s, ProviderCodex, "C", Credential{Auth: grantAuth("u-c", "c", time.Now().Add(time.Hour).Unix())})
	r, h, e := s.TransferCodexMigration(snapshot(t, s).Revision, op.ID, c.ID, []NativeMember{child}, nil)
	if e != nil {
		t.Fatal(e)
	}
	nonce := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	r, h, e = s.ClaimCodexBootstrapLaunch(r.Revision, h.ID, "unlaunched", nonce)
	if e != nil {
		t.Fatal(e)
	}
	claim := *h.Bootstrap[0].Launch
	job := "11223344556677889900aabbccddeeff"
	lease, e := s.AcquireCodexRelogin(c.ID, job)
	if e != nil {
		t.Fatal(e)
	}
	r, _, e = lease.ReplaceCredential(r.Revision, Credential{Auth: grantAuth("u-c", "c", time.Now().Add(2*time.Hour).Unix())}, job)
	if e != nil {
		t.Fatal(e)
	}
	_ = lease.Close()
	h = r.Handovers[ProviderCodex]
	if *h.Bootstrap[0].Launch != claim || h.Epoch != claim.Epoch+1 {
		t.Fatal("reauth changed old launch issuance tuple")
	}
	r, h, e = s.RecordCodexBootstrapChild(r.Revision, h.ID, "unlaunched", nonce, handoverNative("late", "native-late"))
	if e != nil {
		t.Fatal(e)
	}
	if h.Members[len(h.Members)-1].Phase != "pending" || h.Members[len(h.Members)-1].AdoptedEpoch != 0 {
		t.Fatal("late child inherited obsolete adoption")
	}
}
func TestCodexHeldReloginSchemaFencePrecedesAuthorityAndMismatchRetainsIntent(t *testing.T) {
	s, b, h := heldReloginFixture(t)
	job := "11223344556677889900aabbccddeeff"
	lease, e := s.AcquireCodexRelogin(b.ID, job)
	if e != nil {
		t.Fatal(e)
	}
	defer lease.Close()
	before, _ := os.ReadFile(filepath.Join(lease.Home(), "auth.json"))
	s.writeOps.rename = func(old, new string) error {
		if new == "registry.json" {
			return errors.New("schema publication cut")
		}
		return s.root.Rename(old, new)
	}
	credential := Credential{Auth: grantAuth("u-b", "b", time.Now().Add(2*time.Hour).Unix())}
	if _, _, e = lease.ReplaceCredential(snapshot(t, s).Revision, credential, job); e == nil {
		t.Fatal("schema cut ignored")
	}
	s.writeOps = writeOps{}
	after, _ := os.ReadFile(filepath.Join(lease.Home(), "auth.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("authority changed before schema fence")
	}
	if _, e = os.Stat(filepath.Join(lease.Home(), grantReloginFile)); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("new intent preceded schema fence")
	}
	s.writeOps.rename = func(old, new string) error {
		if new == filepath.Join("saved", b.ID, "credential.json") {
			return errors.New("metadata cut")
		}
		return s.root.Rename(old, new)
	}
	if _, _, e = lease.ReplaceCredential(snapshot(t, s).Revision, credential, job); e == nil {
		t.Fatal("metadata cut ignored")
	}
	s.writeOps = writeOps{}
	// Deliberate valid-registry interference models corrupt/stale selection state;
	// normal public mutations are fenced and cannot produce it.
	if _, e = s.mutate(snapshot(t, s).Revision, func(r *Registry) error {
		wrong := r.Handovers[ProviderCodex]
		wrong.Epoch++
		for i := range wrong.Members {
			wrong.Members[i].Phase = "pending"
		}
		wrong.Phase = handoverPhase(wrong)
		p := r.Providers[ProviderCodex]
		p.Epoch = wrong.Epoch
		r.Providers[ProviderCodex] = p
		r.Handovers[ProviderCodex] = wrong
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if e = s.Reconcile(); !errors.Is(e, ErrConflict) {
		t.Fatalf("mismatched selection repaired by inference: %v", e)
	}
	if _, e = os.Stat(filepath.Join(lease.Home(), grantReloginFile)); e != nil {
		t.Fatal("mismatch wiped durable intent")
	}
	if _, _, e = s.RecordCodexReceipt(snapshot(t, s).Revision, handoverReceipt(h, h.Members[0].Native, "adopted")); !errors.Is(e, ErrPending) {
		t.Fatal("mismatch intent no longer fences receipts")
	}
}

func TestCodexHeldReauthRequiresExplicitDesiredJobLease(t *testing.T) {
	s, b, h := heldReloginFixture(t)
	job := "11223344556677889900aabbccddeeff"
	ordinary, e := s.AcquireCodexGrant(b.ID)
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = ordinary.ReplaceCredential(snapshot(t, s).Revision, Credential{Auth: grantAuth("u-b", "b", time.Now().Add(2*time.Hour).Unix())}, job); !errors.Is(e, ErrPending) {
		t.Fatal("normal refresh lease initiated selection-changing reauth")
	}
	_ = ordinary.Close()
	old := h.FromID
	lease, e := s.AcquireCodexRelogin(old, job)
	if e != nil {
		t.Fatal(e)
	}
	defer lease.Close()
	if _, _, e = lease.ReplaceCredential(snapshot(t, s).Revision, Credential{Auth: grantAuth("u", "a", time.Now().Add(2*time.Hour).Unix())}, job); !errors.Is(e, ErrPending) {
		t.Fatal("historically held grant replaced while other target desired")
	}
}

func TestCodexHeldIntentRetryPreservesUnexpectedCanonicalGeneration(t *testing.T) {
	s, b, _ := heldReloginFixture(t)
	job := "11223344556677889900aabbccddeeff"
	lease, e := s.AcquireCodexRelogin(b.ID, job)
	if e != nil {
		t.Fatal(e)
	}
	defer lease.Close()
	s.writeOps.rename = func(old, new string) error {
		if new == filepath.Join("saved", b.ID, "auth.json") {
			return errors.New("pre-auth cut")
		}
		return s.root.Rename(old, new)
	}
	credential := Credential{Auth: grantAuth("u-b", "b", time.Now().Add(2*time.Hour).Unix())}
	if _, _, e = lease.ReplaceCredential(snapshot(t, s).Revision, credential, job); e == nil {
		t.Fatal("cut ignored")
	}
	s.writeOps = writeOps{}
	unexpected := grantAuth("u-b", "b", time.Now().Add(5*time.Hour).Unix())
	if e = os.WriteFile(filepath.Join(lease.Home(), "auth.json"), unexpected, 0600); e != nil {
		t.Fatal(e)
	}
	if e = s.Reconcile(); e != nil {
		t.Fatal(e)
	}
	if _, _, e = lease.ReplaceCredential(snapshot(t, s).Revision, credential, job); !errors.Is(e, ErrConflict) {
		t.Fatalf("unexpected generation overwritten by retry: %v", e)
	}
	raw, _ := os.ReadFile(filepath.Join(lease.Home(), "auth.json"))
	if !bytes.Equal(raw, unexpected) {
		t.Fatal("newer canonical authority replaced")
	}
	if e = s.CheckCodexGrantPending(b.ID); !errors.Is(e, ErrPending) {
		t.Fatal("ambiguous generation released pending intent")
	}
}
func TestCodexMigrationReloginFaultCutsRemainBlockedUntilTransfer(t *testing.T) {
	for _, cut := range []string{"intent", "auth", "metadata", "registry", "retire"} {
		t.Run(cut, func(t *testing.T) {
			s, _, b, op, child := transferFixture(t)
			if _, e := s.mutate(snapshot(t, s).Revision, func(r *Registry) error { r.SchemaVersion = 4; return nil }); e != nil {
				t.Fatal(e)
			}
			job := "11223344556677889900aabbccddeeff"
			lease, e := s.AcquireCodexRelogin(b.ID, job)
			if e != nil {
				t.Fatal(e)
			}
			dir := filepath.Join("saved", b.ID)
			s.writeOps.rename = func(old, new string) error {
				if cut == "intent" && new == filepath.Join(dir, grantReloginFile) || cut == "auth" && new == filepath.Join(dir, "auth.json") || cut == "metadata" && new == filepath.Join(dir, "credential.json") || cut == "registry" && new == "registry.json" {
					return errors.New("injected cut")
				}
				return s.root.Rename(old, new)
			}
			s.writeOps.remove = func(name string) error {
				if cut == "retire" && name == filepath.Join(dir, grantReloginFile) {
					return errors.New("retirement cut")
				}
				return s.root.Remove(name)
			}
			credential := Credential{Auth: grantAuth("u-b", "b", time.Now().Add(2*time.Hour).Unix())}
			if _, _, e = lease.ReplaceCredential(snapshot(t, s).Revision, credential, job); e == nil {
				t.Fatal("cut ignored")
			}
			s.writeOps = writeOps{}
			_ = lease.Close()
			reopened, e := Open(s.StateRoot())
			if e != nil {
				t.Fatal(e)
			}
			defer reopened.Close()
			reopened.grantWriters = s.grantWriters
			if e = reopened.Reconcile(); e != nil {
				t.Fatal(e)
			}
			r := snapshot(t, reopened)
			if find(&r, b.ID).LastLoginJobID != job {
				retry, e := reopened.AcquireCodexRelogin(b.ID, job)
				if e != nil {
					t.Fatal(e)
				}
				r, _, e = retry.ReplaceCredential(r.Revision, credential, job)
				if e != nil {
					t.Fatal(e)
				}
				_ = retry.Close()
			}
			if _, e = reopened.RuntimeForSwitch(op.ID, "unlaunched"); !errors.Is(e, ErrPending) {
				t.Fatal("new grant escaped under old migration epoch")
			}
			if _, e = reopened.CommitSwitch(op.ID); !errors.Is(e, ErrPending) {
				t.Fatal("commit retry advanced old epoch")
			}
			if _, _, e = reopened.TransferCodexMigration(r.Revision, op.ID, b.ID, []NativeMember{child}, []BootstrapChild{{SourceID: "source", Native: child, Phase: "pending"}}); e != nil {
				t.Fatal(e)
			}
		})
	}
}

func TestCodexHandoverBeginCannotStrandOutgoingReloginIntent(t *testing.T) {
	s, a, lease := ownedGrantFixture(t)
	_ = lease.Close()
	b := add(t, s, ProviderCodex, "B", Credential{Auth: grantAuth("u-b", "b", time.Now().Add(time.Hour).Unix())})
	job := "11223344556677889900aabbccddeeff"
	reauth, e := s.AcquireCodexRelogin(a.ID, job)
	if e != nil {
		t.Fatal(e)
	}
	defer reauth.Close()
	s.writeOps.rename = func(old, new string) error {
		if new == filepath.Join("saved", a.ID, "auth.json") {
			return errors.New("pre-auth cut")
		}
		return s.root.Rename(old, new)
	}
	if _, _, e = reauth.ReplaceCredential(snapshot(t, s).Revision, Credential{Auth: grantAuth("u", "a", time.Now().Add(2*time.Hour).Unix())}, job); e == nil {
		t.Fatal("cut ignored")
	}
	s.writeOps = writeOps{}
	if _, _, e = s.BeginCodexHandover(snapshot(t, s).Revision, b.ID, nil); !errors.Is(e, ErrPending) {
		t.Fatal("new handover stranded outgoing explicit relogin")
	}
}
