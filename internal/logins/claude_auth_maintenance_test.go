package logins

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func claudeMaintenanceFixture(t *testing.T) (*Store, Login, Login, ClaudeAuthRef) {
	t.Helper()
	s, a, b, old := newClaudeAuthUnboundTest(t)
	if err := s.RetireClaudeAuth(old); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SelectClaudeProfile(snapshot(t, s).Revision, a.ID); err != nil {
		t.Fatal(err)
	}
	ref, _, err := s.BeginClaudeAuth(snapshot(t, s).Revision, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.BindClaudeAuthScope(ref, claudeAuthTestScope(t, ref)); err != nil {
		t.Fatal(err)
	}
	return s, a, b, ref
}

func TestClaudeAuthMaintenanceSurvivesUnrelatedSelectionAndNativeRetry(t *testing.T) {
	s, a, _, ref := claudeMaintenanceFixture(t)
	first := claimClaudeAuthTest(t, s, ref)
	stop := nativeClaudeAuthTest(t, s, ref, first)
	c := add(t, s, ProviderClaude, "C", claudeCredential("c", "c"))
	if _, err := s.SelectClaudeProfile(snapshot(t, s).Revision, c.ID); err != nil {
		t.Fatal(err)
	}
	writeClaudeAuthProfileTest(t, first)
	grant := claudeAuthGrant(time.Now().Add(2*time.Hour), []string{"user:inference", "user:profile"})
	if err := os.WriteFile(filepath.Join(first.AuthorityHome, ".credentials.json"), grant, 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.ObserveClaudeAuthProfile(ref, first.Number); err != nil {
		t.Errorf("parked profile observation lost its grant identity on selection: %v", err)
	}
	stop()
	if err := s.RecordClaudeAuthContained(ref, first.Number); err != nil {
		t.Fatal(err)
	}
	if result, err := s.FinishClaudeAuthAttempt(ref, first.Number); !errors.Is(err, ErrPending) || result.Phase != "retry" {
		t.Fatalf("parked native renewal lost confirmation authority: %+v %v", result, err)
	}
	second := claimClaudeAuthTest(t, s, ref)
	if second.Number != 2 || second.Deadline != first.Deadline {
		t.Fatal("selection reset the helper's attempt budget")
	}
	stop = nativeClaudeAuthTest(t, s, ref, second)
	writeClaudeAuthProfileTest(t, second)
	if err := s.ObserveClaudeAuthProfile(ref, second.Number); err != nil {
		t.Fatal(err)
	}
	stop()
	if err := s.RecordClaudeAuthContained(ref, second.Number); err != nil {
		t.Fatal(err)
	}
	result, err := s.FinishClaudeAuthAttempt(ref, second.Number)
	if err != nil || result.Phase != "ready" || result.Selection.ActiveID != a.ID {
		t.Fatalf("parked receipt changed history or failed: %+v %v", result, err)
	}
}

func TestClaudeAuthMaintenanceDelayedClaimUsesGrantNotDesiredEpoch(t *testing.T) {
	s, _, _, ref := claudeMaintenanceFixture(t)
	c := add(t, s, ProviderClaude, "C", claudeCredential("c", "c"))
	if _, err := s.SelectClaudeProfile(snapshot(t, s).Revision, c.ID); err != nil {
		t.Fatal(err)
	}
	_, launch, err := s.ClaimClaudeAuthAttempt(ref, currentClaudeAuthWorker(t))
	if err != nil {
		t.Fatalf("unrelated desired account canceled parked claim: %v", err)
	}
	if err := s.AbortClaudeAuthLaunch(launch, nil); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeAuthMaintenanceParkedWriterCensusAtBirthAndClaim(t *testing.T) {
	for _, when := range []string{"birth", "claim", "selected"} {
		t.Run(when, func(t *testing.T) {
			s, a, b, ref := claudeMaintenanceFixture(t)
			if when == "birth" {
				if err := retireClaudeAuthTest(t, s, ref); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Mkdir(filepath.Join(s.StateRoot(), "unknown-writer"), 0700); err != nil {
				t.Fatal(err)
			}
			switch when {
			case "birth":
				if _, _, err := s.BeginClaudeAuth(snapshot(t, s).Revision, b.ID); !errors.Is(err, ErrPending) {
					t.Fatalf("unknown parked writer admitted new helper: %v", err)
				}
			case "claim":
				if _, _, err := s.ClaimClaudeAuthAttempt(ref, currentClaudeAuthWorker(t)); !errors.Is(err, ErrPending) {
					t.Fatalf("delayed claim bypassed parked writer census: %v", err)
				}
			case "selected":
				if _, created, err := s.BeginClaudeAuth(snapshot(t, s).Revision, a.ID); err != nil || !created {
					t.Fatalf("selected shared-authority behavior was blocked: %v %v", created, err)
				}
			}
		})
	}
}

func TestClaudeAuthMaintenanceStillRefusesReloginModeAndMigration(t *testing.T) {
	for _, reason := range []string{"relogin", "mode", "migration"} {
		t.Run(reason, func(t *testing.T) {
			s, a, b, ref := claudeMaintenanceFixture(t)
			switch reason {
			case "relogin":
				if _, _, err := s.Relogin(snapshot(t, s).Revision, b.ID, claudeCredential("b", "replacement")); !errors.Is(err, ErrPending) {
					t.Fatalf("public relogin did not stage pending owner intent: %v", err)
				}
			case "mode":
				if _, err := s.mutate(snapshot(t, s).Revision, func(r *Registry) error {
					p := r.Providers[ProviderClaude]
					p.AuthOwnerMode = claudeNativeSlotMode
					r.Providers[ProviderClaude] = p
					r.SchemaVersion = claudeSlotSchemaVersion
					r.ClaudeSlot = &claudeSlotState{Actual: claudeSlotAuthority{LoginID: a.ID, Identity: a.Identity, CredentialRevision: a.CredentialRevision, GrantHash: strings.Repeat("a", 64)}}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			case "migration":
				if _, err := s.SetMigrationPending(snapshot(t, s).Revision, true); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := s.ClaimClaudeAuthAttempt(ref, currentClaudeAuthWorker(t)); err == nil {
				t.Fatalf("%s allowed stale helper launch", reason)
			}
		})
	}
}
