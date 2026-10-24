package logins

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCodexInitialOwnershipRequiresExplicitEligibleTarget(t *testing.T) {
	for _, kind := range []string{"implicit", "missing", "needs-login", "exhausted"} {
		t.Run(kind, func(t *testing.T) {
			s := testStore(t)
			b := add(t, s, ProviderCodex, "B", Credential{Auth: grantAuth("u-b", "b", time.Now().Add(time.Hour).Unix())})
			target := b.ID
			switch kind {
			case "implicit":
				target = ""
			case "missing":
				target = "00112233445566778899aabbccddeeff"
			case "needs-login":
				if _, e := s.UpdateAvailability(snapshot(t, s).Revision, b.ID, AuthNeedsLogin, false, time.Time{}); e != nil {
					t.Fatal(e)
				}
			case "exhausted":
				if _, e := s.UpdateExhaustion(snapshot(t, s).Revision, b.ID, time.Now().Add(time.Hour), false); e != nil {
					t.Fatal(e)
				}
			}
			before := snapshot(t, s)
			if _, e := s.BeginCodexOwnershipMigrationTo(before.Revision, target, nil); !errors.Is(e, ErrIneligible) {
				t.Fatalf("invalid bootstrap target admitted: %v", e)
			}
			after := snapshot(t, s)
			if after.Revision != before.Revision || len(after.Switches) != 0 || after.Providers[ProviderCodex].Epoch != 0 {
				t.Fatal("rejected bootstrap changed authority")
			}
		})
	}
}
func TestCodexInitialOwnershipMigratesWithoutCommonGrantInstall(t *testing.T) {
	s := testStore(t)
	auth := grantAuth("u-b", "b", time.Now().Add(time.Hour).Unix())
	b := add(t, s, ProviderCodex, "B", Credential{Auth: auth})
	saved, e := s.readSaved(b)
	if e != nil {
		t.Fatal(e)
	}
	missing := Login{ID: "00112233445566778899aabbccddeeff", Provider: ProviderCodex, Label: "unavailable", Identity: identityDigest(ProviderCodex, "missing"), AuthState: AuthNeedsLogin}
	if _, _, e := s.ImportUnavailable(snapshot(t, s).Revision, missing); e != nil {
		t.Fatal(e)
	}
	op, e := s.BeginCodexOwnershipMigrationTo(snapshot(t, s).Revision, b.ID, []Restart{{SourceID: "unmanaged", ConversationID: "exact-thread"}})
	if e != nil {
		t.Fatal(e)
	}
	if op.FromID != "" || op.FromEpoch != 0 || op.ToEpoch != 1 || !op.OwnershipMigration {
		t.Fatal("initial selection invented outgoing authority")
	}
	stopOwnershipMigration(t, s, op)
	if e = s.MigrateCodexOwnership(op.ID); e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(filepath.Join(s.savedNativeHome(b.ID), "auth.json"))
	if e != nil || !bytes.Equal(raw, saved.Auth) {
		t.Fatal("initial canonical grant changed")
	}
	identity, e := CodexIdentity(raw)
	if e != nil || identity != b.Identity {
		t.Fatal("canonical identity changed")
	}
	if _, e = os.Stat(filepath.Join(s.Home(ProviderCodex), "auth.json")); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("initial activation installed common refresh authority")
	}
	rt, e := s.CommitSwitch(op.ID)
	if e != nil {
		t.Fatal(e)
	}
	if !rt.ExternalAuth || rt.Epoch != 1 || rt.Identity != b.Identity {
		t.Fatal("bootstrap did not select external runtime")
	}
	r := snapshot(t, s)
	if r.Providers[ProviderCodex].ActiveID != b.ID || r.Providers[ProviderCodex].Epoch != 1 || len(r.Switches[ProviderCodex].Sessions) != 1 {
		t.Fatal("initial commit lost selection or restart obligation")
	}
	if e = s.VerifySaved(missing.ID); e != nil {
		t.Fatal(e)
	}
}
