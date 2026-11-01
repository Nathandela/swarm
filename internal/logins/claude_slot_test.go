package logins

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const slotTestMode = "claude-native-slot-v1"

func claudeSlotLogins(t *testing.T) (*Store, []Login) {
	t.Helper()
	s := testStore(t)
	s.keychain = nil
	var accounts []Login
	for _, name := range []string{"a", "b", "c"} {
		accounts = append(accounts, add(t, s, ProviderClaude, name, claudeCredential(name, name)))
	}
	return s, accounts
}

// Persist the proposed format directly: production has no slot activation API.
func claudeSlotDocument(t *testing.T, s *Store, accounts []Login) map[string]any {
	t.Helper()
	raw, err := json.Marshal(snapshot(t, s))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	ref := func(login Login, hash string) map[string]any {
		return map[string]any{"login_id": login.ID, "identity": login.Identity, "credential_revision": login.CredentialRevision, "grant_hash": strings.Repeat(hash, 64)}
	}
	doc["schema_version"] = 11
	doc["providers"] = map[string]any{ProviderClaude: map[string]any{"provider": ProviderClaude, "active_id": accounts[2].ID, "epoch": 9, "auth_owner_mode": slotTestMode}}
	doc["claude_slot"] = map[string]any{
		"actual":    ref(accounts[0], "a"),
		"promotion": map[string]any{"id": strings.Repeat("1", 32), "desired_epoch": 8, "source": ref(accounts[0], "a"), "target": ref(accounts[1], "b")},
	}
	return doc
}

func writeClaudeSlotDocument(t *testing.T, s *Store, doc map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.path, "registry.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestClaudeSlotRegistryRetainsActualAndSupersededPromotion(t *testing.T) {
	s, accounts := claudeSlotLogins(t)
	doc := claudeSlotDocument(t, s, accounts)
	writeClaudeSlotDocument(t, s, doc)
	r := snapshot(t, s)
	if r.Providers[ProviderClaude].ActiveID != accounts[2].ID {
		t.Fatal("desired C was replaced by actual A or reserved B")
	}
	for _, auth := range []string{AuthNeedsLogin, AuthPaused} {
		var err error
		r, err = s.UpdateAvailability(r.Revision, accounts[0].ID, auth, false, time.Time{})
		if err != nil {
			t.Fatalf("actual authority cannot retain %s state: %v", auth, err)
		}
	}
	// A registry hash is historical; native renewal cannot invalidate its ownership.
	if err := os.WriteFile(filepath.Join(s.savedNativeHome(accounts[0].ID), ".credentials.json"), claudeCredential("a", "native-A2").Auth, 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(s.StateRoot())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	r = snapshot(t, reopened)
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var after map[string]any
	if err := json.Unmarshal(raw, &after); err != nil {
		t.Fatal(err)
	}
	beforeSlot, _ := json.Marshal(doc["claude_slot"])
	afterSlot, _ := json.Marshal(after["claude_slot"])
	if string(beforeSlot) != string(afterSlot) {
		t.Fatal("Store save/reopen lost actual or unresolved source/target custody")
	}
}

func TestClaudeSlotRejectsMalformedPersistedAuthority(t *testing.T) {
	for _, name := range []string{"old-schema", "old-schema-null", "missing-mode", "missing-slot", "null-slot", "slot-alias", "unknown-slot", "null-actual", "wrong-type", "unknown-actual", "alias-actual", "duplicate-actual", "foreign-login", "wrong-provider", "wrong-identity", "stale-revision", "oversize-hash", "null-promotion", "unknown-promotion", "zero-epoch", "future-epoch", "same-source-target", "unrelated-actual", "null-source", "bad-intent-id", "migration"} {
		t.Run(name, func(t *testing.T) {
			s, accounts := claudeSlotLogins(t)
			doc := claudeSlotDocument(t, s, accounts)
			slot := doc["claude_slot"].(map[string]any)
			actual := slot["actual"].(map[string]any)
			promotion := slot["promotion"].(map[string]any)
			switch name {
			case "old-schema":
				doc["schema_version"] = 10
			case "old-schema-null":
				doc["schema_version"], doc["claude_slot"] = 10, nil
				doc["providers"].(map[string]any)[ProviderClaude].(map[string]any)["auth_owner_mode"] = ""
			case "missing-mode":
				doc["providers"].(map[string]any)[ProviderClaude].(map[string]any)["auth_owner_mode"] = ""
			case "missing-slot":
				delete(doc, "claude_slot")
			case "null-slot":
				doc["claude_slot"] = nil
			case "slot-alias":
				doc["CLAUDE_SLOT"] = slot
				delete(doc, "claude_slot")
			case "unknown-slot":
				slot["adopted"] = true
			case "null-actual":
				slot["actual"] = nil
			case "wrong-type":
				slot["actual"] = "not-an-authority"
			case "unknown-actual":
				actual["ready"] = true
			case "alias-actual":
				actual["Login_ID"] = actual["login_id"]
			case "duplicate-actual":
				raw, _ := json.Marshal(actual)
				slot["actual"] = json.RawMessage(strings.TrimSuffix(string(raw), "}") + `,"login_id":"` + accounts[0].ID + `"}`)
			case "foreign-login":
				actual["login_id"] = strings.Repeat("f", 32)
			case "wrong-provider":
				doc["logins"].([]any)[0].(map[string]any)["provider"] = ProviderCodex
			case "wrong-identity":
				actual["identity"] = accounts[1].Identity
			case "stale-revision":
				actual["credential_revision"] = 2
			case "oversize-hash":
				actual["grant_hash"] = strings.Repeat("a", 4096)
			case "null-promotion":
				slot["promotion"] = nil
			case "unknown-promotion":
				promotion["adopted"] = true
			case "zero-epoch":
				promotion["desired_epoch"] = 0
			case "future-epoch":
				promotion["desired_epoch"] = 10
			case "same-source-target":
				promotion["target"] = promotion["source"]
			case "unrelated-actual":
				actual["login_id"], actual["identity"] = accounts[2].ID, accounts[2].Identity
			case "null-source":
				promotion["source"] = nil
			case "bad-intent-id":
				promotion["id"] = "not-an-intent"
			case "migration":
				doc["migration_pending"] = true
			}
			writeClaudeSlotDocument(t, s, doc)
			if _, err := s.Snapshot(); !errors.Is(err, ErrIneligible) {
				t.Fatalf("malformed %s accepted: %v", name, err)
			}
		})
	}
}

func TestClaudeSlotHelperAdmissionFencesActualAndPromotion(t *testing.T) {
	for _, operation := range []string{"begin", "reuse", "replace", "claim"} {
		for index, name := range []string{"actual-A", "reserved-B"} {
			t.Run(operation+"/"+name, func(t *testing.T) {
				s, accounts := claudeSlotLogins(t)
				var ref ClaudeAuthRef
				if operation != "begin" {
					var err error
					ref, _, err = s.BeginClaudeAuth(snapshot(t, s).Revision, accounts[index].ID)
					if err != nil {
						t.Fatal(err)
					}
					if operation == "replace" {
						if err := s.PrepareClaudeAuthRecovery(ref); err != nil {
							t.Fatal(err)
						}
						if _, err := s.SettleClaudeAuth(ref); err != nil {
							t.Fatal(err)
						}
					}
				}
				writeClaudeSlotDocument(t, s, claudeSlotDocument(t, s, accounts))
				r := snapshot(t, s)
				if operation == "claim" {
					if err := s.BindClaudeAuthScope(ref, claudeAuthTestScope(t, ref)); err != nil {
						t.Fatal(err)
					}
					intent, err := s.readClaudeAuthIntent(ref.LoginID)
					if err != nil {
						t.Fatal(err)
					}
					// Delayed work can acquire new ownership without a selection change.
					intent.Selection = r.Providers[ProviderClaude]
					if err := s.writeClaudeAuthIntent(intent); err != nil {
						t.Fatal(err)
					}
				}
				var err error
				switch operation {
				case "begin", "reuse":
					_, _, err = s.BeginClaudeAuth(r.Revision, accounts[index].ID)
				case "replace":
					_, _, err = s.ReplaceClaudeAuth(r.Revision, ref)
				case "claim":
					_, _, err = s.ClaimClaudeAuthAttempt(ref, currentClaudeAuthWorker(t))
				}
				if !errors.Is(err, ErrInUse) {
					t.Fatalf("%s admitted %s parked refresh: %v", operation, name, err)
				}
				if ref.Nonce != "" {
					if _, statErr := os.Stat(filepath.Join(s.path, claudeAuthPayload(ref.LoginID))); !errors.Is(statErr, os.ErrNotExist) {
						t.Fatal("refused helper created scratch or cleaned retained custody")
					}
					if operation != "replace" {
						if err := s.PrepareClaudeAuthRecovery(ref); err != nil {
							t.Fatalf("ownership fence blocked exact retained cleanup: %v", err)
						}
						if operation == "claim" {
							if _, err := s.SettleClaudeAuth(ref); !errors.Is(err, ErrPending) {
								t.Fatalf("unretired scope lost custody: %v", err)
							}
						} else if result, err := s.SettleClaudeAuth(ref); err != nil || result.Phase != "failed" {
							t.Fatalf("retained budget did not settle: %+v %v", result, err)
						}
					}
				}
			})
		}
	}
}

func TestClaudeSlotUnrelatedParkedHelperStillClaims(t *testing.T) {
	s, accounts := claudeSlotLogins(t)
	writeClaudeSlotDocument(t, s, claudeSlotDocument(t, s, accounts))
	ref, created, err := s.BeginClaudeAuth(snapshot(t, s).Revision, accounts[2].ID)
	if err != nil || !created {
		t.Fatalf("parked C admission: created=%v err=%v", created, err)
	}
	if again, created, err := s.BeginClaudeAuth(snapshot(t, s).Revision, accounts[2].ID); err != nil || created || again != ref {
		t.Fatalf("parked C reuse: created=%v err=%v", created, err)
	}
	if err := s.BindClaudeAuthScope(ref, claudeAuthTestScope(t, ref)); err != nil {
		t.Fatal(err)
	}
	attempt, launch, err := s.ClaimClaudeAuthAttempt(ref, currentClaudeAuthWorker(t))
	if err != nil || attempt.AuthorityHome != s.savedNativeHome(accounts[2].ID) {
		t.Fatalf("parked C claim: %+v %v", attempt, err)
	}
	if err := s.AbortClaudeAuthLaunch(launch, nil); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeSlotRefusesLegacyRuntimeAndMutation(t *testing.T) {
	s, accounts := claudeSlotLogins(t)
	writeClaudeSlotDocument(t, s, claudeSlotDocument(t, s, accounts))
	r := snapshot(t, s)
	before, err := os.ReadFile(filepath.Join(s.path, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	runtime := Runtime{Provider: ProviderClaude, Identity: accounts[2].Identity, Epoch: 9, Home: s.Home(ProviderClaude)}
	for name, operation := range map[string]func() error{
		"runtime":           func() error { _, err := s.Runtime(ProviderClaude); return err },
		"environment":       func() error { _, err := s.PrepareEnvironment(runtime, nil); return err },
		"native-read":       func() error { _, err := s.nativeCredential(ProviderClaude, accounts[0].ID); return err },
		"native-install":    func() error { return s.installCredential(ProviderClaude, claudeCredential("c", "replacement")) },
		"profile-selection": func() error { _, err := s.SelectClaudeProfile(r.Revision, accounts[1].ID); return err },
		"profile-provision": func() error {
			_, err := s.ProvisionClaudeProfile(r.Revision, accounts[1].ID, []string{s.StateRoot()})
			return err
		},
		"profile-birth": func() error {
			profile := runtime
			profile.Home, _ = s.ClaudeProfileHome(profile.Identity)
			return s.AdmitClaudeProfileBirth(profile, s.StateRoot(), func() error {
				t.Fatal("unqualified profile birth callback ran")
				return nil
			})
		},
	} {
		if err := operation(); !errors.Is(err, ErrPending) {
			t.Errorf("unqualified slot legacy %s: %v", name, err)
		}
	}
	if after, err := os.ReadFile(filepath.Join(s.path, "registry.json")); err != nil || string(before) != string(after) {
		t.Fatal("refused legacy entry point changed registry authority")
	}
	for _, name := range []string{"capture", "relogin", "remove", "erase", "credential-write"} {
		for index, label := range []string{"actual-A", "reserved-B"} {
			t.Run(name+"/"+label, func(t *testing.T) {
				s, accounts := claudeSlotLogins(t)
				writeClaudeSlotDocument(t, s, claudeSlotDocument(t, s, accounts))
				r, login := snapshot(t, s), accounts[index]
				var err error
				switch name {
				case "capture":
					_, _, err = s.captureCredential(login)
				case "relogin":
					_, _, err = s.Relogin(r.Revision, login.ID, claudeCredential(login.Label, "replacement"))
				case "remove":
					_, err = s.Remove(r.Revision, login.ID)
				case "erase":
					err = s.eraseSaved(login)
				case "credential-write":
					err = s.writeClaudeAuth(login.ID, claudeCredential(login.Label, "replacement").Auth)
				}
				if !errors.Is(err, ErrInUse) {
					t.Fatalf("%s mutated held %s: %v", name, label, err)
				}
			})
		}
	}
}

func TestClaudeSlotWithoutPromotionHoldsOnlyActualAuthority(t *testing.T) {
	s, accounts := claudeSlotLogins(t)
	doc := claudeSlotDocument(t, s, accounts)
	delete(doc["claude_slot"].(map[string]any), "promotion")
	writeClaudeSlotDocument(t, s, doc)
	r := snapshot(t, s)
	if _, _, err := s.BeginClaudeAuth(r.Revision, accounts[0].ID); !errors.Is(err, ErrInUse) {
		t.Fatalf("actual authority acquired a parked helper: %v", err)
	}
	if _, created, err := s.BeginClaudeAuth(r.Revision, accounts[1].ID); err != nil || !created {
		t.Fatalf("unreserved B blocked: created=%v err=%v", created, err)
	}
}

func TestClaudeSlotSchemaDoesNotRewriteLegacyModes(t *testing.T) {
	for version := 1; version <= 10; version++ {
		s := testStore(t)
		r := snapshot(t, s)
		if _, err := s.mutate(r.Revision, func(r *Registry) error {
			r.SchemaVersion = version
			return nil
		}); err != nil {
			t.Fatalf("legacy schema %d refused: %v", version, err)
		}
		if got := snapshot(t, s); got.SchemaVersion != version {
			t.Fatalf("legacy schema %d promoted without ownership: %d", version, got.SchemaVersion)
		}
	}
}

func TestClaudeSlotPromotionSourceRemainsHeldAfterActualChanges(t *testing.T) {
	s, accounts := claudeSlotLogins(t)
	doc := claudeSlotDocument(t, s, accounts)
	slot := doc["claude_slot"].(map[string]any)
	slot["actual"] = slot["promotion"].(map[string]any)["target"]
	writeClaudeSlotDocument(t, s, doc)
	r := snapshot(t, s)
	if !owned(r, accounts[0].ID) || !owned(r, accounts[1].ID) {
		t.Fatal("unresolved source or actual target lost mutation ownership")
	}
	if _, _, err := s.BeginClaudeAuth(r.Revision, accounts[0].ID); !errors.Is(err, ErrInUse) {
		t.Fatalf("unresolved source acquired parked refresh: %v", err)
	}
	if _, err := s.Remove(r.Revision, accounts[0].ID); !errors.Is(err, ErrInUse) {
		t.Fatalf("unresolved source was removed: %v", err)
	}
}

func TestClaudeSlotUnheldSavedLoginMutationIsPreserved(t *testing.T) {
	s, accounts := claudeSlotLogins(t)
	writeClaudeSlotDocument(t, s, claudeSlotDocument(t, s, accounts))
	d := add(t, s, ProviderClaude, "d", claudeCredential("d", "initial"))
	r, _, err := s.Relogin(snapshot(t, s).Revision, d.ID, claudeCredential("d", "replacement"))
	if err != nil {
		t.Fatalf("unheld D relogin blocked: %v", err)
	}
	if _, err := s.Remove(r.Revision, d.ID); err != nil {
		t.Fatalf("unheld D removal blocked: %v", err)
	}
}

func TestClaudeSlotUnknownAuthorityNeverFallsBackToDesired(t *testing.T) {
	for _, name := range []string{"absent", "foreign"} {
		t.Run(name, func(t *testing.T) {
			s, accounts := claudeSlotLogins(t)
			doc := claudeSlotDocument(t, s, accounts)
			slot := doc["claude_slot"].(map[string]any)
			if name == "absent" {
				delete(slot, "actual")
			} else {
				slot["actual"].(map[string]any)["login_id"] = strings.Repeat("f", 32)
			}
			before := writeClaudeSlotDocument(t, s, doc)
			if _, _, err := s.BeginClaudeAuth(0, accounts[2].ID); !errors.Is(err, ErrIneligible) {
				t.Fatalf("unknown authority admitted desired C helper: %v", err)
			}
			if _, err := s.Remove(0, accounts[2].ID); !errors.Is(err, ErrIneligible) {
				t.Fatalf("unknown authority allowed credential removal: %v", err)
			}
			if _, _, err := s.Relogin(0, accounts[2].ID, claudeCredential("c", "replacement")); !errors.Is(err, ErrIneligible) {
				t.Fatalf("unknown authority allowed credential replacement: %v", err)
			}
			if _, err := os.Stat(filepath.Join(s.path, claudeAuthPath(accounts[2].ID))); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("unknown authority created a new helper intent")
			}
			if after, err := os.ReadFile(filepath.Join(s.path, "registry.json")); err != nil || string(before) != string(after) {
				t.Fatal("unknown authority was rewritten from desired selection")
			}
		})
	}
}
