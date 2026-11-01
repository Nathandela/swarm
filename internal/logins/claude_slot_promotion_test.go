package logins

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/processcontain"
)

func promotionHash(raw []byte) string {
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

func claudePromotionReadyTest(t *testing.T) (*Store, Login, Login, ClaudeAuthRef) {
	t.Helper()
	s, a, b, ref := newSettledReadyClaudeAuthTest(t)
	seedClaudePromotionSlot(t, s, a, b)
	return s, a, b, ref
}

func seedClaudePromotionSlot(t *testing.T, s *Store, a, b Login) {
	t.Helper()
	r := snapshot(t, s)
	raw, err := s.claudeAuth(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	var auth map[string]json.RawMessage
	if err := json.Unmarshal(raw, &auth); err != nil {
		t.Fatal(err)
	}
	// Test-only persisted prior authority; no production bootstrap API exists.
	if _, err := s.mutate(r.Revision, func(r *Registry) error {
		selected := r.Providers[ProviderClaude]
		selected.ActiveID, selected.AuthOwnerMode = b.ID, claudeNativeSlotMode
		selected.Epoch++
		r.Providers[ProviderClaude] = selected
		r.SchemaVersion = claudeSlotSchemaVersion
		r.ClaudeSlot = &claudeSlotState{Actual: claudeSlotAuthority{LoginID: a.ID, Identity: a.Identity, CredentialRevision: a.CredentialRevision, GrantHash: promotionHash(auth["claudeAiOauth"])}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func readPromotionFile(t *testing.T, s *Store, name string) []byte {
	t.Helper()
	raw, err := readPrivate(s.root, name, maxRegistryBytes)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestClaudePromotionReservesSettledHistoricalReadyWithoutNativeWrites(t *testing.T) {
	s, a, b, ref := claudePromotionReadyTest(t)
	intent, err := s.readClaudeAuthIntent(b.ID)
	if err != nil || intent.Selection.ActiveID != a.ID {
		t.Fatalf("fixture lost historical A selection: %v", err)
	}
	intent.StartedAt = intent.StartedAt.Add(-time.Hour)
	intent.Deadline = intent.StartedAt.Add(claudeAuthDeadline)
	if err := s.writeClaudeAuthIntent(intent); err != nil {
		t.Fatal(err)
	}
	before := map[string][]byte{}
	for _, name := range []string{claudeAuthPath(b.ID), filepath.Join("saved", a.ID, ".credentials.json"), filepath.Join("saved", b.ID, ".credentials.json"), filepath.Join("provider", ProviderClaude, "native", ".claude.json")} {
		before[name] = readPromotionFile(t, s, name)
	}
	r := snapshot(t, s)
	actual := r.ClaudeSlot.Actual
	got, err := s.ReserveClaudePromotion(r.Revision, ref)
	if err != nil {
		t.Fatalf("settled current grant rejected because its helper attempt ended: %v", err)
	}
	p := got.ClaudeSlot.Promotion
	if p == nil || p.Target.LoginID != b.ID || p.Target.Identity != b.Identity || p.Target.CredentialRevision != b.CredentialRevision || p.Source != actual || got.ClaudeSlot.Actual != actual || p.DesiredEpoch != r.Providers[ProviderClaude].Epoch || !validID(p.ID) {
		t.Fatalf("reservation changed authority or lost exact target: %+v", p)
	}
	encoded, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil || string(fields["auth_nonce"]) != `"`+ref.Nonce+`"` {
		t.Fatal("reservation did not retain the exact ready ref nonce")
	}
	var grant map[string]json.RawMessage
	if err := json.Unmarshal(before[filepath.Join("saved", b.ID, ".credentials.json")], &grant); err != nil {
		t.Fatal(err)
	}
	if p.Target.GrantHash != promotionHash(grant["claudeAiOauth"]) || p.Target.GrantHash == intent.Generation {
		t.Fatal("provider subtree receipt was confused with native document generation")
	}
	for name, raw := range before {
		if after := readPromotionFile(t, s, name); string(raw) != string(after) {
			t.Fatalf("reservation mutated native/helper file %s", name)
		}
	}
	if _, err := s.Runtime(ProviderClaude); !errors.Is(err, ErrPending) {
		t.Fatalf("reservation enabled an unqualified runtime: %v", err)
	}
	if err := s.PrepareClaudeAuthRecovery(ref); err != nil {
		t.Fatal(err)
	}
	if result, err := s.SettleClaudeAuth(ref); err != nil || result.Phase != "ready" {
		t.Fatalf("pin blocked harmless exact settlement: %+v %v", result, err)
	}
}

func TestClaudePromotionRawProviderHashPreservesNativeBytes(t *testing.T) {
	provider := "{\n  \"accessToken\": \"<>&\",\n  \"nativeUnknown\": { \"label\": \"<native>&future\" }\n}"
	raw := []byte("{\n\"claudeAiOauth\": " + provider + ",\n\"unrelatedMCP\": {\"opaque\":true}\n}")
	got, err := claudeProviderGrantHash(raw)
	if err != nil || got != promotionHash([]byte(provider)) || got == promotionHash(raw) {
		t.Fatalf("raw provider whitespace/HTML/unknown field bytes lost: %v", err)
	}
	for _, raw := range []string{`{}`, `{"claudeAiOauth":null}`, `{"claudeAiOauth":"value"}`, `{"claudeAiOauth":{},"claudeAiOauth":{}}`} {
		if _, err := claudeProviderGrantHash([]byte(raw)); err == nil {
			t.Fatal("invalid native provider snapshot accepted")
		}
	}
}

func TestClaudePromotionRequiresExactSettledReadyAuthority(t *testing.T) {
	for _, name := range []string{"nonce", "credential-revision", "canonical-generation", "expiry", "scopes", "worker-live", "unit-not-retired", "unbound-scope", "missing-stop-proof", "missing-ready", "payload", "legacy-writer-unknown", "stale-cas", "same-account"} {
		t.Run(name, func(t *testing.T) {
			s, a, b, ref := claudePromotionReadyTest(t)
			intent, err := s.readClaudeAuthIntent(b.ID)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "nonce":
				ref.Nonce = strings.Repeat("f", 32)
			case "credential-revision":
				ref.CredentialRevision++
			case "canonical-generation":
				raw, err := s.claudeAuth(b.ID)
				if err != nil {
					t.Fatal(err)
				}
				// Same provider subtree, different whole native document generation.
				raw = []byte(strings.TrimSuffix(string(raw), "}") + `,"nativeNewField":true}`)
				if err := os.WriteFile(filepath.Join(s.savedNativeHome(b.ID), ".credentials.json"), raw, 0600); err != nil {
					t.Fatal(err)
				}
			case "expiry":
				intent.StartedAt = intent.StartedAt.Add(-10 * time.Minute)
				intent.Deadline = intent.StartedAt.Add(claudeAuthDeadline)
				intent.ExpiresAt = time.Now().Add(time.Minute)
				grant := claudeAuthGrant(intent.ExpiresAt, []string{"user:inference", "user:profile"})
				intent.ExpiresAt = time.UnixMilli(intent.ExpiresAt.UnixMilli()).UTC()
				intent.Generation = promotionHash(grant)
				if err := os.WriteFile(filepath.Join(s.savedNativeHome(b.ID), ".credentials.json"), grant, 0600); err != nil {
					t.Fatal(err)
				}
			case "worker-live":
				intent.Worker = currentClaudeAuthWorker(t)
				intent.Scope.Worker = intent.Worker
			case "unit-not-retired":
				intent.UnitRetired = false
			case "unbound-scope":
				intent.Version, intent.Scope, intent.UnitRetired = 1, ClaudeAuthScope{}, false
			case "scopes":
				grant := claudeAuthGrant(intent.ExpiresAt, []string{"user:profile"})
				intent.Generation = promotionHash(grant)
				if err := os.WriteFile(filepath.Join(s.savedNativeHome(b.ID), ".credentials.json"), grant, 0600); err != nil {
					t.Fatal(err)
				}
			case "missing-stop-proof":
				intent.StopProof = ""
			case "missing-ready":
				intent.Phase, intent.ExpiresAt = "failed", time.Time{}
			case "payload":
				if err := os.Mkdir(filepath.Join(s.path, claudeAuthPayload(b.ID), ref.Nonce), 0700); err != nil {
					t.Fatal(err)
				}
			case "legacy-writer-unknown":
				if err := os.Mkdir(filepath.Join(s.StateRoot(), "unknown-writer"), 0700); err != nil {
					t.Fatal(err)
				}
			case "same-account":
				ref.LoginID, ref.Identity, ref.CredentialRevision = a.ID, a.Identity, a.CredentialRevision
			}
			if err := s.writeClaudeAuthIntent(intent); err != nil {
				t.Fatal(err)
			}
			r := snapshot(t, s)
			expected := r.Revision
			if name == "stale-cas" {
				expected--
			}
			before := readPromotionFile(t, s, "registry.json")
			if _, err := s.ReserveClaudePromotion(expected, ref); err == nil {
				t.Fatalf("%s authority reserved", name)
			}
			if after := readPromotionFile(t, s, "registry.json"); string(before) != string(after) {
				t.Fatal("refused reservation changed provider state")
			}
		})
	}
}

func TestClaudePromotionReservationPinsRetireAndPreservesSupersededOwners(t *testing.T) {
	s, a, b, ref := claudePromotionReadyTest(t)
	r, err := s.ReserveClaudePromotion(snapshot(t, s).Revision, ref)
	if err != nil {
		t.Fatal(err)
	}
	before := readPromotionFile(t, s, claudeAuthPath(b.ID))
	if err := s.RetireClaudeAuth(ref); !errors.Is(err, ErrInUse) {
		t.Fatalf("reserved ready target retired: %v", err)
	}
	if after := readPromotionFile(t, s, claudeAuthPath(b.ID)); string(before) != string(after) {
		t.Fatal("Retire changed the pinned ready receipt before refusing")
	}
	c := add(t, s, ProviderClaude, "C", claudeCredential("c", "c"))
	r = snapshot(t, s)
	if _, err := s.mutate(r.Revision, func(r *Registry) error {
		selected := r.Providers[ProviderClaude]
		selected.ActiveID, selected.Epoch = c.ID, selected.Epoch+1
		r.Providers[ProviderClaude] = selected
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r = snapshot(t, s)
	if _, err := s.ReserveClaudePromotion(r.Revision, ref); !errors.Is(err, ErrPending) {
		t.Fatalf("supersession replaced unresolved reservation: %v", err)
	}
	if !owned(r, a.ID) || !owned(r, b.ID) {
		t.Fatal("supersession released source/target ownership")
	}
	cRef, _, err := s.BeginClaudeAuth(r.Revision, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RetireClaudeAuth(cRef); err != nil {
		t.Fatalf("unrelated exact helper cleanup was blocked: %v", err)
	}
}

func TestClaudePromotionRetireFirstCannotReserve(t *testing.T) {
	s, _, _, ref := claudePromotionReadyTest(t)
	if err := s.RetireClaudeAuth(ref); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReserveClaudePromotion(snapshot(t, s).Revision, ref); err == nil {
		t.Fatal("removed ready ref reconstructed reservation authority")
	}
	if snapshot(t, s).ClaudeSlot.Promotion != nil {
		t.Fatal("retired ref left a reservation")
	}
}

func TestClaudePromotionRetireSecondPhaseRechecksExactPin(t *testing.T) {
	s, _, b, ref := claudePromotionReadyTest(t)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var once sync.Once
	s.writeOps.remove = func(name string) error {
		if name == filepath.Join(claudeAuthPayload(b.ID), ref.Nonce) {
			once.Do(func() { close(entered) })
			<-release
			return s.root.RemoveAll(name)
		}
		return s.root.Remove(name)
	}
	go func() { done <- s.RetireClaudeAuth(ref) }()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("retirement did not reach unlocked payload cleanup")
	}
	before := readPromotionFile(t, s, claudeAuthPath(b.ID))
	r := snapshot(t, s)
	// Defensive recovered-pin fixture: normal Reserve refuses the retiring phase.
	if _, err := s.mutate(r.Revision, func(r *Registry) error {
		raw, _ := json.Marshal(map[string]any{"id": strings.Repeat("1", 32), "desired_epoch": r.Providers[ProviderClaude].Epoch, "auth_nonce": ref.Nonce, "source": r.ClaudeSlot.Actual, "target": claudeSlotAuthority{LoginID: b.ID, Identity: b.Identity, CredentialRevision: b.CredentialRevision, GrantHash: strings.Repeat("b", 64)}})
		var promotion claudeSlotPromotion
		if err := json.Unmarshal(raw, &promotion); err != nil {
			return err
		}
		r.ClaudeSlot.Promotion = &promotion
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case err := <-done:
		if !errors.Is(err, ErrInUse) {
			t.Fatalf("late exact pin did not fence final deletion: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("retirement remained blocked")
	}
	if after := readPromotionFile(t, s, claudeAuthPath(b.ID)); string(before) != string(after) {
		t.Fatal("second retirement phase changed the late-pinned intent")
	}
}

func TestClaudePromotionUnboundReservationStaysHeld(t *testing.T) {
	s, _, b, ref := claudePromotionReadyTest(t)
	r := snapshot(t, s)
	if _, err := s.mutate(r.Revision, func(r *Registry) error {
		r.ClaudeSlot.Promotion = &claudeSlotPromotion{ID: strings.Repeat("1", 32), DesiredEpoch: r.Providers[ProviderClaude].Epoch, Source: r.ClaudeSlot.Actual, Target: claudeSlotAuthority{LoginID: b.ID, Identity: b.Identity, CredentialRevision: b.CredentialRevision, GrantHash: strings.Repeat("b", 64)}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before := readPromotionFile(t, s, "registry.json")
	if _, err := s.ReserveClaudePromotion(snapshot(t, s).Revision, ref); !errors.Is(err, ErrPending) {
		t.Fatalf("unbound reservation acquired current helper: %v", err)
	}
	if after := readPromotionFile(t, s, "registry.json"); string(before) != string(after) {
		t.Fatal("legacy reservation was silently bound to the current helper")
	}
	if _, _, err := s.BeginClaudeAuth(snapshot(t, s).Revision, b.ID); !errors.Is(err, ErrInUse) {
		t.Fatalf("unbound target lost its refresh fence: %v", err)
	}
}

func TestClaudePromotionDurabilityFailureNeverPublishes(t *testing.T) {
	for _, afterRename := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-rename", true: "after-rename"}[afterRename], func(t *testing.T) {
			s, a, b, ref := claudePromotionReadyTest(t)
			before := readPromotionFile(t, s, filepath.Join("saved", b.ID, ".credentials.json"))
			if afterRename {
				s.writeOps.syncDir = func(string) error { return syscall.ENOSPC }
			} else {
				s.writeOps.rename = func(string, string) error { return syscall.ENOSPC }
			}
			if _, err := s.ReserveClaudePromotion(snapshot(t, s).Revision, ref); err == nil {
				t.Fatal("failed durable reservation returned success")
			} else if afterRename && !errors.Is(err, ErrDurabilityUncertain) {
				t.Fatalf("post-rename uncertainty lost: %v", err)
			}
			if after := readPromotionFile(t, s, filepath.Join("saved", b.ID, ".credentials.json")); string(before) != string(after) {
				t.Fatal("reservation failure published credentials")
			}
			if actual := snapshot(t, s).ClaudeSlot.Actual; actual.LoginID != a.ID {
				t.Fatal("reservation failure relabeled actual authority")
			}
			if afterRename {
				if _, err := s.ReserveClaudePromotion(snapshot(t, s).Revision, ref); !errors.Is(err, ErrDurabilityUncertain) {
					t.Fatalf("uncertain Store admitted another mutation: %v", err)
				}
			}
		})
	}
}

func TestClaudePromotionWaitsForNativeHelperRetry(t *testing.T) {
	s, _, b, old := claudePromotionReadyTest(t)
	grant := claudeAuthGrant(time.Now().Add(2*time.Hour), []string{"user:inference", "user:profile"})
	if err := os.WriteFile(filepath.Join(s.savedNativeHome(b.ID), ".credentials.json"), grant, 0600); err != nil {
		t.Fatal(err)
	}
	ref, created, err := s.ReplaceClaudeAuth(snapshot(t, s).Revision, old)
	if err != nil || !created {
		t.Fatalf("current desired B helper replacement: %v %v", created, err)
	}
	first := claimClaudeAuthTest(t, s, ref)
	stop := nativeClaudeAuthTest(t, s, ref, first)
	writeClaudeAuthProfileTest(t, first)
	grant = claudeAuthGrant(time.Now().Add(3*time.Hour), []string{"user:inference", "user:profile"})
	if err := os.WriteFile(filepath.Join(first.AuthorityHome, ".credentials.json"), grant, 0600); err != nil {
		t.Fatal(err)
	}
	stop()
	if err := s.RecordClaudeAuthContained(ref, first.Number); err != nil {
		t.Fatal(err)
	}
	if result, err := s.FinishClaudeAuthAttempt(ref, first.Number); !errors.Is(err, ErrPending) || result.Phase != "retry" {
		t.Fatalf("native renewal did not require confirmation: %+v %v", result, err)
	}
	if _, err := s.ReserveClaudePromotion(snapshot(t, s).Revision, ref); !errors.Is(err, ErrPending) {
		t.Fatalf("unsettled helper was reserved: %v", err)
	}
	second := claimClaudeAuthTest(t, s, ref)
	if second.Number != 2 {
		t.Fatal("reservation attempt changed native confirmation authority")
	}
	stop = nativeClaudeAuthTest(t, s, ref, second)
	writeClaudeAuthProfileTest(t, second)
	stop()
	if err := s.RecordClaudeAuthContained(ref, second.Number); err != nil {
		t.Fatal(err)
	}
	if result, err := s.FinishClaudeAuthAttempt(ref, second.Number); err != nil || result.Phase != "ready" {
		t.Fatalf("second attempt did not finish ready: %+v %v", result, err)
	}
	intent, err := s.readClaudeAuthIntent(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	intent.Worker = processcontain.Identity{PID: 999999999, StartTime: 1}
	intent.Scope.Worker = intent.Worker
	if err := s.writeClaudeAuthIntent(intent); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordClaudeAuthUnitRetired(ref, intent.Scope); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SettleClaudeAuth(ref); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReserveClaudePromotion(snapshot(t, s).Revision, ref); err != nil {
		t.Fatalf("fully settled second attempt could not reserve: %v", err)
	}
}

func TestClaudePromotionKeepsActualAuthorityIndependentOfFreshness(t *testing.T) {
	for _, name := range []string{"needs-login", "native-renewal"} {
		t.Run(name, func(t *testing.T) {
			s, a, _, ref := claudePromotionReadyTest(t)
			actual := snapshot(t, s).ClaudeSlot.Actual
			if name == "needs-login" {
				if _, err := s.UpdateAvailability(snapshot(t, s).Revision, a.ID, AuthNeedsLogin, false, time.Time{}); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(filepath.Join(s.savedNativeHome(a.ID), ".credentials.json"), claudeCredential("a", "native-A2").Auth, 0600); err != nil {
				t.Fatal(err)
			}
			r, err := s.ReserveClaudePromotion(snapshot(t, s).Revision, ref)
			if err != nil || r.ClaudeSlot.Actual != actual || r.ClaudeSlot.Promotion.Source != actual {
				t.Fatalf("actual owner was relabeled or treated as current-grant proof: %v", err)
			}
		})
	}
}

func TestClaudePromotionCannotBootstrapOrOverrideDesired(t *testing.T) {
	t.Run("legacy mode", func(t *testing.T) {
		s, _, _, ref := newSettledReadyClaudeAuthTest(t)
		before := readPromotionFile(t, s, "registry.json")
		if _, err := s.ReserveClaudePromotion(snapshot(t, s).Revision, ref); !errors.Is(err, ErrPending) {
			t.Fatalf("legacy mode was activated by reservation: %v", err)
		}
		if after := readPromotionFile(t, s, "registry.json"); string(before) != string(after) {
			t.Fatal("reservation bootstrapped actual owner or slot mode")
		}
	})
	t.Run("desired C before reserve", func(t *testing.T) {
		s, _, _, ref := claudePromotionReadyTest(t)
		c := add(t, s, ProviderClaude, "C", claudeCredential("c", "c"))
		if _, err := s.mutate(snapshot(t, s).Revision, func(r *Registry) error {
			selected := r.Providers[ProviderClaude]
			selected.ActiveID, selected.Epoch = c.ID, selected.Epoch+1
			r.Providers[ProviderClaude] = selected
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ReserveClaudePromotion(snapshot(t, s).Revision, ref); !errors.Is(err, ErrConflict) {
			t.Fatalf("historical B overrode current desired C: %v", err)
		}
		if snapshot(t, s).ClaudeSlot.Promotion != nil {
			t.Fatal("stale target gained reservation")
		}
	})
}

func TestClaudePromotionConcurrentReservationsHaveOneWinner(t *testing.T) {
	s, _, _, ref := claudePromotionReadyTest(t)
	other, err := OpenExisting(s.StateRoot())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	other.keychain = nil
	revision := snapshot(t, s).Revision
	results := make(chan error, 2)
	for _, handle := range []*Store{s, other} {
		go func() { _, err := handle.ReserveClaudePromotion(revision, ref); results <- err }()
	}
	winners := 0
	for range 2 {
		if err := <-results; err == nil {
			winners++
		} else if !errors.Is(err, ErrConflict) {
			t.Fatalf("unexpected competing reservation outcome: %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("reservation issuers = %d", winners)
	}
}

func TestClaudePromotionNonceEncodingIsStrict(t *testing.T) {
	for _, name := range []string{"null", "empty", "invalid", "alias"} {
		t.Run(name, func(t *testing.T) {
			s, _, _, ref := claudePromotionReadyTest(t)
			r, err := s.ReserveClaudePromotion(snapshot(t, s).Revision, ref)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(r)
			var doc map[string]any
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
			p := doc["claude_slot"].(map[string]any)["promotion"].(map[string]any)
			switch name {
			case "null":
				p["auth_nonce"] = nil
			case "empty":
				p["auth_nonce"] = ""
			case "invalid":
				p["auth_nonce"] = "invalid"
			case "alias":
				p["AUTH_NONCE"] = p["auth_nonce"]
				delete(p, "auth_nonce")
			}
			writeClaudeSlotDocument(t, s, doc)
			if _, err := s.Snapshot(); !errors.Is(err, ErrIneligible) {
				t.Fatalf("malformed reservation nonce accepted: %v", err)
			}
		})
	}
}

func TestClaudePromotionPendingReloginRetainsOldReadyGrant(t *testing.T) {
	s, a, b, ref := newSettledReadyClaudeAuthTest(t)
	nativePath := filepath.Join("saved", b.ID, ".credentials.json")
	oldGrant := readPromotionFile(t, s, nativePath)
	oldReady := readPromotionFile(t, s, claudeAuthPath(b.ID))
	s.writeOps.rename = func(from, to string) error {
		if to == nativePath {
			return syscall.ENOSPC
		}
		return s.root.Rename(from, to)
	}
	incoming := claudeCredential("b", "replacement")
	incoming.Auth = claudeAuthGrant(time.Now().Add(2*time.Hour), []string{"user:inference", "user:profile"})
	_, _, err := s.Relogin(snapshot(t, s).Revision, b.ID, incoming)
	s.writeOps.rename = nil
	if !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("public relogin did not stop after staging: %v", err)
	}
	if _, err := s.readRelogin(b); err != nil {
		t.Fatalf("failed credential write lost staged relogin: %v", err)
	}
	if string(oldGrant) != string(readPromotionFile(t, s, nativePath)) || string(oldReady) != string(readPromotionFile(t, s, claudeAuthPath(b.ID))) {
		t.Fatal("fixture changed the old canonical grant or Ready receipt")
	}
	seedClaudePromotionSlot(t, s, a, b)
	before := readPromotionFile(t, s, "registry.json")
	staged := readPromotionFile(t, s, reloginPath(b.ID))
	if _, err := s.ReserveClaudePromotion(snapshot(t, s).Revision, ref); !errors.Is(err, ErrPending) {
		t.Fatalf("old Ready grant reserved over pending relogin: %v", err)
	}
	if string(before) != string(readPromotionFile(t, s, "registry.json")) || string(staged) != string(readPromotionFile(t, s, reloginPath(b.ID))) || string(oldGrant) != string(readPromotionFile(t, s, nativePath)) || string(oldReady) != string(readPromotionFile(t, s, claudeAuthPath(b.ID))) {
		t.Fatal("reservation refusal changed staged relogin, old grant or Ready receipt")
	}
}

func TestClaudePromotionReloginReadFailuresRefuseReservation(t *testing.T) {
	for _, name := range []string{"malformed", "unreadable-directory"} {
		t.Run(name, func(t *testing.T) {
			s, _, b, ref := claudePromotionReadyTest(t)
			path := filepath.Join(s.path, reloginPath(b.ID))
			want := ErrInvalidCredentials
			if name == "malformed" {
				if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				want = ErrUnsafePath
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			before := readPromotionFile(t, s, "registry.json")
			if _, err := s.ReserveClaudePromotion(snapshot(t, s).Revision, ref); !errors.Is(err, want) {
				t.Fatalf("unreadable staged authority was ignored: got %v want %v", err, want)
			}
			if string(before) != string(readPromotionFile(t, s, "registry.json")) {
				t.Fatal("failed relogin read changed reservation state")
			}
		})
	}
}
