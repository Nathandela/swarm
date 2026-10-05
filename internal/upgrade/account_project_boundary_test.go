package upgrade

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/accountconfig"
	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

func boundaryCard(version int) CompatManifest {
	card := CurrentManifest("v9.9.9")
	card.AccountConfig = version
	return card
}

func boundaryProjectionFixture(t *testing.T, provider string, withRegistry bool) (string, string, []byte) {
	t.Helper()
	base := t.TempDir()
	state := filepath.Join(base, "state")
	home := filepath.Join(base, "home")
	cwd := filepath.Join(home, "repo", "nested")
	profile := filepath.Join(base, "synthetic-profile")
	for _, dir := range []string{state, home, cwd, profile, filepath.Join(home, "."+provider)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if provider == accounts.ProviderCodex {
		if err := os.Mkdir(filepath.Join(home, "repo", ".git"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, "repo", ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if withRegistry {
		store, err := accounts.Open(state)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
	argv := []string{"synthetic-" + provider, "--model", "synthetic-model"}
	projection, err := accountconfig.PrepareWithModel(state, provider, profile, cwd, []string{"HOME=" + home}, argv, "", "")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(state, "accounts", "configurations", projection.Ref, "projection.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil || doc["ProjectBoundary"] == nil {
		t.Fatal("real writer omitted project boundary contract")
	}
	return state, projection.Ref, raw
}

func replaceBoundaryProjection(t *testing.T, state, oldRef string, raw []byte) string {
	t.Helper()
	digest := sha256.Sum256(raw)
	ref := hex.EncodeToString(digest[:])
	base := filepath.Join(state, "accounts", "configurations")
	if oldRef != "" {
		if err := os.RemoveAll(filepath.Join(base, oldRef)); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(base, ref)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "projection.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return ref
}

func legacyBoundaryProjection(t *testing.T, state, ref string, raw []byte) string {
	t.Helper()
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	delete(doc, "ProjectBoundary")
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return replaceBoundaryProjection(t, state, ref, raw)
}

func TestAccountProjectBoundaryCardUsesCompiledContract(t *testing.T) {
	if CurrentManifest("v9.9.9").AccountConfig != accountconfig.CompatibilityVersion || accountconfig.CompatibilityVersion != 4 {
		t.Fatal("release card lacks current project boundary interpretation")
	}
}

func TestAccountProjectBoundaryGuardIndependentOfRegistryAndSessions(t *testing.T) {
	for _, provider := range []string{accounts.ProviderClaude, accounts.ProviderCodex} {
		for _, registry := range []bool{false, true} {
			t.Run(provider+map[bool]string{false: "/metadata-only", true: "/empty-registry"}[registry], func(t *testing.T) {
				state, _, _ := boundaryProjectionFixture(t, provider, registry)
				if err := accountStateGuard(state, boundaryCard(1)); err == nil {
					t.Fatal("same-card old reader accepted real project boundary")
				}
				if err := accountStateGuard(state, boundaryCard(2)); err != nil {
					t.Fatalf("current reader refused metadata-only contract: %v", err)
				}
			})
		}
	}
}

func TestAccountProjectBoundaryGuardAllowsUnmarkedLegacyProjection(t *testing.T) {
	state, ref, raw := boundaryProjectionFixture(t, accounts.ProviderClaude, false)
	legacyBoundaryProjection(t, state, ref, raw)
	if err := accountStateGuard(state, boundaryCard(1)); err != nil {
		t.Fatal("unmarked legacy projection acquired new requirement", err)
	}
}

func TestAccountProjectBoundaryGuardDoesNotRevalidateMutableSources(t *testing.T) {
	state, _, raw := boundaryProjectionFixture(t, accounts.ProviderClaude, false)
	var doc struct{ Cwd string }
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	// Compatibility belongs to the retained immutable contract. Removal of a
	// former source can hold a later launch, but cannot hide the downgrade need.
	if err := os.RemoveAll(filepath.Dir(filepath.Dir(doc.Cwd))); err != nil {
		t.Fatal(err)
	}
	if err := accountStateGuard(state, boundaryCard(1)); err == nil {
		t.Fatal("source disappearance hid the retained compatibility contract")
	}
	if err := accountStateGuard(state, boundaryCard(2)); err != nil {
		t.Fatal("compatibility guard read a mutable source", err)
	}
}

func TestAccountProjectBoundaryMetadataOnlyExceptionRequiresCompleteInventory(t *testing.T) {
	for _, neighbor := range []string{"profiles", "checks", "jobs", "unknown"} {
		t.Run(neighbor, func(t *testing.T) {
			state, _, _ := boundaryProjectionFixture(t, accounts.ProviderClaude, false)
			if err := os.Mkdir(filepath.Join(state, "accounts", neighbor), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := accountStateGuard(state, boundaryCard(2)); err == nil {
				t.Fatal("unregistered neighboring account state bypassed the guard")
			}
		})
	}
}

func TestAccountProjectBoundaryGuardRetainsOldErasedSourceContract(t *testing.T) {
	state, ref, raw := boundaryProjectionFixture(t, accounts.ProviderClaude, true)
	// A newer unmarked projection/current generation must not hide the older
	// marked source. The source row is ended, hidden, and names an erased binding;
	// the compatibility guard reads no credential file or current binding.
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	delete(doc, "ProjectBoundary")
	legacyRaw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	newRef := replaceBoundaryProjection(t, state, "", legacyRaw)
	accountStore, err := accounts.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = accountStore.Close() }()
	admitCandidate := func() accounts.Candidate {
		candidate, err := accountStore.CreateCandidate(accounts.ProviderClaude, accounts.KindNative)
		if err != nil {
			t.Fatal(err)
		}
		for name, content := range map[string]string{
			".credentials.json": `{"claudeAiOauth":{"accessToken":"synthetic-access","refreshToken":"synthetic-refresh","scopes":["user:inference"],"subscriptionType":"pro"}}`,
			".claude.json":      `{"oauthAccount":{"accountUuid":"synthetic-boundary-owner","organizationUuid":"synthetic-boundary-org"}}`,
		} {
			if err := os.WriteFile(filepath.Join(candidate.ProfilePath, name), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		candidate, err = accountStore.VerifyCandidate(candidate)
		if err != nil {
			t.Fatal(err)
		}
		return candidate
	}
	first := admitCandidate()
	registry, err := accountStore.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	registry, account, err := accountStore.Admit(registry.Revision, first, "synthetic-boundary")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := accountStore.CurrentBinding(account.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	second := admitCandidate()
	registry, account, err = accountStore.Reauthenticate(registry.Revision, account.ID, second)
	if err != nil {
		t.Fatal(err)
	}
	currentBinding, err := accountStore.CurrentBinding(account.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	registry, err = accountStore.SetLifecycle(registry.Revision, account.ID, accounts.LifecycleRetiring)
	if err != nil {
		t.Fatal(err)
	}
	registry, err = accountStore.BeginCredentialErasure(registry.Revision, account.ID, 1, "2.1.288")
	if err != nil {
		t.Fatal(err)
	}
	registry, err = accountStore.EraseCredentials(registry.Revision, account.ID, 1, accounts.ErasureProof{WritersStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	if !registry.Accounts[account.ID].Generations[1].CredentialErased || registry.Accounts[account.ID].CurrentGeneration != 2 {
		t.Fatal("fixture did not retain an actually erased old generation")
	}
	store, err := persist.NewStore(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, meta := range []persist.Meta{{ID: "erased-old-source", AgentType: accounts.ProviderClaude, AccountBinding: &binding, AccountProjectionRef: ref, RosterHidden: true, Status: status.Status{Process: status.ProcessExited}}, {ID: "new-current-source", AgentType: accounts.ProviderClaude, AccountBinding: &currentBinding, AccountProjectionRef: newRef, Status: status.Status{Process: status.ProcessExited}}} {
		if err := store.Save(meta); err != nil {
			t.Fatal(err)
		}
	}
	if err := accountStateGuard(state, boundaryCard(1)); err == nil {
		t.Fatal("current unmarked source hid retained old boundary")
	}
	if err := accountStateGuard(state, boundaryCard(2)); err != nil {
		t.Fatal(err)
	}
}

func TestAccountProjectBoundaryGuardRefusesMalformedPresentContract(t *testing.T) {
	for _, malformed := range []string{"null", "false", `"unknown"`, "{}", `{"Home":null}`, `{"Home":1}`, `{"Home":"relative"}`, `{"Root":false}`, `{"Root":"/synthetic","Evidence":"unknown"}`} {
		t.Run(malformed, func(t *testing.T) {
			state, ref, raw := boundaryProjectionFixture(t, accounts.ProviderClaude, false)
			var doc map[string]json.RawMessage
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
			doc["ProjectBoundary"] = json.RawMessage(malformed)
			raw, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			replaceBoundaryProjection(t, state, ref, raw)
			if err := accountStateGuard(state, boundaryCard(2)); err == nil {
				t.Fatal("malformed present marker hid behind default decode")
			}
		})
	}
}

func TestAccountProjectBoundaryGuardRefusesUnsafeInventoryAndBadHash(t *testing.T) {
	for _, kind := range []string{"directory-alias", "projection-alias", "hardlink", "changed-bytes", "unknown-directory", "invalid-json"} {
		t.Run(kind, func(t *testing.T) {
			state, ref, raw := boundaryProjectionFixture(t, accounts.ProviderClaude, false)
			base := filepath.Join(state, "accounts", "configurations")
			dir := filepath.Join(base, ref)
			path := filepath.Join(dir, "projection.json")
			switch kind {
			case "directory-alias":
				if err := os.Rename(dir, filepath.Join(state, "moved-projection")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(state, "moved-projection"), dir); err != nil {
					t.Fatal(err)
				}
			case "projection-alias":
				target := filepath.Join(state, "projection-copy")
				if err := os.WriteFile(target, raw, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(path, filepath.Join(state, "projection-hardlink")); err != nil {
					t.Fatal(err)
				}
			case "changed-bytes":
				if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
					t.Fatal(err)
				}
			case "unknown-directory":
				if err := os.Mkdir(filepath.Join(base, "unrecognized"), 0o700); err != nil {
					t.Fatal(err)
				}
			case "invalid-json":
				replaceBoundaryProjection(t, state, ref, []byte(`{"SchemaVersion":1`))
			}
			if err := accountStateGuard(state, boundaryCard(2)); err == nil {
				t.Fatal("unsafe immutable configuration inventory accepted")
			}
		})
	}
}

func TestAccountProjectBoundaryPreflightActivationAndRollback(t *testing.T) {
	for _, operation := range []string{"activate", "rollback"} {
		for _, marked := range []bool{true, false} {
			for _, version := range []int{1, 2} {
				t.Run(operation+map[bool]string{true: "/marked", false: "/legacy"}[marked]+map[int]string{1: "/card1", 2: "/card2"}[version], func(t *testing.T) {
					state, ref, raw := boundaryProjectionFixture(t, accounts.ProviderClaude, true)
					if !marked {
						legacyBoundaryProjection(t, state, ref, raw)
					}
					bin := installTarget(t)
					before, err := os.ReadFile(bin)
					if err != nil {
						t.Fatal(err)
					}
					calls := captureExec(t)
					card := boundaryCard(version)
					options := ActivateOptions{StateDir: state, BinPath: bin, DaemonAlive: noDaemon()}
					var result State
					if operation == "activate" {
						stageBuild(t, state, "v9.9.9", &card)
						result, err = Activate(options)
					} else {
						prev := PrevDir(state)
						if err := os.MkdirAll(prev, 0o700); err != nil {
							t.Fatal(err)
						}
						for name, data := range map[string][]byte{"VERSION": []byte("v9.9.9\n"), "swarm": []byte("#!/bin/sh\necho swarm 9.9.9\n")} {
							if err := os.WriteFile(filepath.Join(prev, name), data, 0o700); err != nil {
								t.Fatal(err)
							}
						}
						encoded, marshalErr := json.Marshal(card)
						if marshalErr != nil {
							t.Fatal(marshalErr)
						}
						if err := os.WriteFile(filepath.Join(prev, "compat.json"), encoded, 0o600); err != nil {
							t.Fatal(err)
						}
						result, err = Rollback(options)
					}
					if err != nil {
						t.Fatal(err)
					}
					after, err := os.ReadFile(bin)
					if err != nil {
						t.Fatal(err)
					}
					if marked && version == 1 {
						if result.Outcome != "refused-account-state" || len(*calls) != 0 || string(after) != string(before) {
							t.Fatalf("old same-card reader touched install before boundary refusal: %+v", result)
						}
						return
					}
					want := "activated"
					if operation == "rollback" {
						want = "rolled-back"
					}
					if result.Outcome != want || len(*calls) != 1 || string(after) == string(before) {
						t.Fatalf("compatible project boundary preflight failed: %+v", result)
					}
				})
			}
		}
	}
}
