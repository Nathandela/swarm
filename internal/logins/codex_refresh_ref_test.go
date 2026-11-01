package logins

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodexRefreshRefBindsNativeOwnerRevisionAndGrant(t *testing.T) {
	_, login, lease := ownedGrantFixture(t)
	defer func() { _ = lease.Close() }()
	ref, err := lease.RefreshRef()
	if err != nil {
		t.Fatal(err)
	}
	generation, err := lease.Generation()
	if err != nil || !ref.Valid() || ref.LoginID != login.ID || ref.Identity != login.Identity || ref.Owner != lease.owner || ref.CredentialRevision != login.CredentialRevision || ref.Generation != hex.EncodeToString(generation[:]) {
		t.Fatalf("wrong exact refresh authority: %+v %v", ref, err)
	}
	encoded, _ := json.Marshal(ref)
	if strings.Contains(string(encoded), "access_token") || strings.Contains(string(encoded), "refresh_token") {
		t.Fatal("refresh identity exposed native credentials")
	}
	raw, err := os.ReadFile(filepath.Join(lease.Home(), "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	var native map[string]json.RawMessage
	if err := json.Unmarshal(raw, &native); err != nil {
		t.Fatal(err)
	}
	native["last_refresh"] = json.RawMessage(`"format-only"`)
	formatted, _ := json.MarshalIndent(native, "", "  ")
	if err := os.WriteFile(filepath.Join(lease.Home(), "auth.json"), formatted, 0600); err != nil {
		t.Fatal(err)
	}
	if unchanged, err := lease.RefreshRef(); err != nil || unchanged != ref {
		t.Fatalf("format/last_refresh reset the credential unit: %+v %v", unchanged, err)
	}
	if err := os.WriteFile(filepath.Join(lease.Home(), "auth.json"), grantAuth("u", "a", time.Now().Add(3*time.Hour).Unix()), 0600); err != nil {
		t.Fatal(err)
	}
	next, err := lease.RefreshRef()
	if err != nil || next.Generation == ref.Generation || next.Identity != ref.Identity || next.Owner != ref.Owner || next.CredentialRevision != ref.CredentialRevision {
		t.Fatalf("native renewal was confused with a new login revision: %+v %v", next, err)
	}
}

func TestCodexRefreshRefRefusesClosedOrChangedMetadata(t *testing.T) {
	for _, change := range []string{"closed", "revision", "identity", "mode", "paused", "needs-login", "switch", "owner"} {
		t.Run(change, func(t *testing.T) {
			s, login, lease := ownedGrantFixture(t)
			defer func() { _ = lease.Close() }()
			if change == "closed" {
				if err := lease.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				r := snapshot(t, s)
				switch change {
				case "revision":
					find(&r, login.ID).CredentialRevision++
				case "identity":
					find(&r, login.ID).Identity = strings.Repeat("f", 64)
				case "mode":
					p := r.Providers[ProviderCodex]
					p.AuthOwnerMode = ""
					p.DesiredCredentialRevision = 0
					r.Providers[ProviderCodex] = p
				case "paused":
					find(&r, login.ID).AuthState = AuthPaused
				case "needs-login":
					find(&r, login.ID).AuthState = AuthNeedsLogin
				case "switch":
					p := r.Providers[ProviderCodex]
					r.Switches[ProviderCodex] = Switch{ID: strings.Repeat("f", 32), Provider: ProviderCodex, FromID: login.ID, ToID: login.ID, FromEpoch: p.Epoch, ToEpoch: p.Epoch + 1, Phase: "waiting", Relogin: true}
				case "owner":
					if err := os.WriteFile(filepath.Join(lease.Home(), "auth.json"), grantAuth("other-user", "a", time.Now().Add(time.Hour).Unix()), 0600); err != nil {
						t.Fatal(err)
					}
				}
				raw, _ := json.Marshal(r)
				if err := os.WriteFile(filepath.Join(s.path, "registry.json"), raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := lease.RefreshRef(); err == nil {
				t.Fatalf("%s authority produced a refresh reference", change)
			}
		})
	}
}

func TestCodexRefreshRefIgnoresUnrelatedDesiredSelection(t *testing.T) {
	s, a, lease := ownedGrantFixture(t)
	before, err := lease.RefreshRef()
	if err != nil {
		t.Fatal(err)
	}
	b := add(t, s, ProviderCodex, "B", Credential{Auth: grantAuth("other-user", "b", time.Now().Add(time.Hour).Unix())})
	if _, _, err := s.BeginCodexHandover(snapshot(t, s).Revision, b.ID, nil); err != nil {
		t.Fatal(err)
	}
	after, err := lease.RefreshRef()
	if err != nil || after != before || after.LoginID != a.ID {
		t.Fatalf("desired selection rebound the saved grant: %+v %v", after, err)
	}
}
