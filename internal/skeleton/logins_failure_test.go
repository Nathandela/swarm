package skeleton

import (
	"encoding/json"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Nathandela/swarm/internal/logins"
)

func codexQuotaFrame(snapshot map[string]any) []byte {
	raw, _ := json.Marshal(map[string]any{"params": map[string]any{"rateLimits": snapshot}})
	return raw
}
func codexFailureFrame(conversation, code string, retry bool) []byte {
	raw, _ := json.Marshal(map[string]any{"params": map[string]any{"threadId": conversation, "willRetry": retry, "error": map[string]string{"codexErrorInfo": code}}})
	return raw
}
func quotaLogin(t *testing.T, f *loginFixture, id string) logins.Login {
	t.Helper()
	r, err := f.m.store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	for _, login := range r.Logins {
		if login.ID == id {
			return login
		}
	}
	t.Fatal("fixture login missing")
	return logins.Login{}
}

func TestCodexNativeResetMakesOriginalLoginEligibleForAutomaticReturn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newLoginFixture(t)
		feed := newBackendFeed()
		reset := time.Now().Add(time.Hour)
		snapshot := map[string]any{"limitId": "codex", "primary": map[string]any{"usedPercent": 100, "resetsAt": reset.Unix()}, "rateLimitReachedType": "rate_limit_reached"}
		f.m.observeNativeFailure("source-1", "instance-A", feed, codexRateLimitsUpdated, codexQuotaFrame(snapshot))
		if quotaLogin(t, f, f.first.ID).LimitKnown {
			t.Fatal("a quota snapshot rotated an otherwise usable login")
		}
		f.m.observeNativeFailure("source-1", "instance-A", feed, "error", codexFailureFrame(f.rows["source-1"].ConversationID, "usageLimitExceeded", false))
		limited := quotaLogin(t, f, f.first.ID)
		if !limited.LimitKnown || !limited.ExhaustedUntil.Equal(reset) {
			t.Fatalf("native reset was lost: %+v", limited)
		}
		f.m.step()
		driveLoginSwitch(t, f)
		before, _ := f.m.store.Snapshot()
		if before.Providers[logins.ProviderCodex].ActiveID != f.second.ID {
			t.Fatal("terminal quota did not switch the fleet to B")
		}
		if _, err := f.m.store.NextEligible(logins.ProviderCodex, nil, reset.Add(-time.Second)); !errors.Is(err, logins.ErrNoEligible) {
			t.Fatalf("original login was eligible before its native reset: %v", err)
		}
		time.Sleep(2 * time.Hour) // synctest advances the shared native/store clock instantly.
		child := f.rows["child-1"]
		f.m.observeNativeFailure(child.ID, "instance-B", newBackendFeed(), "error", codexFailureFrame(child.ConversationID, "usageLimitExceeded", false))
		f.m.step()
		driveLoginSwitch(t, f)
		after, _ := f.m.store.Snapshot()
		if after.Providers[logins.ProviderCodex].ActiveID != f.first.ID || after.Providers[logins.ProviderCodex].Epoch != 3 || len(f.launched) != 4 {
			t.Fatal("A was never reconsidered after its native reset")
		}
	})
}

func TestCodexNativeQuotaUsesOnlyTheFullRollingWindows(t *testing.T) {
	now := time.Now()
	soon, later := now.Add(time.Hour).Unix(), now.Add(7*24*time.Hour).Unix()
	for _, tc := range []struct {
		name     string
		snapshot map[string]any
		want     int64
	}{
		{"two blocking windows", map[string]any{"primary": map[string]any{"usedPercent": 100, "resetsAt": soon}, "secondary": map[string]any{"usedPercent": 100, "resetsAt": later}}, later},
		{"nonfull long window", map[string]any{"primary": map[string]any{"usedPercent": 100, "resetsAt": soon}, "secondary": map[string]any{"usedPercent": 42, "resetsAt": later}}, soon},
		{"unknown blocking reset", map[string]any{"primary": map[string]any{"usedPercent": 100, "resetsAt": soon}, "secondary": map[string]any{"usedPercent": 100}}, 0},
		{"past reset", map[string]any{"primary": map[string]any{"usedPercent": 100, "resetsAt": now.Add(-time.Second).Unix()}}, 0},
		{"milliseconds are not seconds", map[string]any{"primary": map[string]any{"usedPercent": 100, "resetsAt": now.Add(time.Hour).UnixMilli()}}, 0},
		{"other model bucket", map[string]any{"limitId": "codex_other", "primary": map[string]any{"usedPercent": 100, "resetsAt": soon}}, 0},
		{"credit rejection", map[string]any{"primary": map[string]any{"usedPercent": 100, "resetsAt": soon}, "rateLimitReachedType": "workspace_owner_credits_depleted"}, 0},
		{"spend control rejection", map[string]any{"primary": map[string]any{"usedPercent": 100, "resetsAt": soon}, "spendControlReached": true}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLoginFixture(t)
			feed := newBackendFeed()
			f.m.observeNativeFailure("source-1", "instance", feed, codexRateLimitsUpdated, codexQuotaFrame(tc.snapshot))
			f.m.observeNativeFailure("source-1", "instance", feed, "error", codexFailureFrame(f.rows["source-1"].ConversationID, "usageLimitExceeded", false))
			login := quotaLogin(t, f, f.first.ID)
			if !login.LimitKnown || tc.want == 0 && !login.ExhaustedUntil.IsZero() || tc.want != 0 && login.ExhaustedUntil.Unix() != tc.want {
				t.Fatalf("incorrect native reset: %v, want %d", login.ExhaustedUntil, tc.want)
			}
		})
	}
}

func TestCodexLateSparseQuotaCanFillUnknownTerminalReset(t *testing.T) {
	f := newLoginFixture(t)
	feed := newBackendFeed()
	reset := time.Now().Add(time.Hour).Unix()
	f.m.observeNativeFailure("source-1", "instance", feed, "error", codexFailureFrame(f.rows["source-1"].ConversationID, "usageLimitExceeded", false))
	if login := quotaLogin(t, f, f.first.ID); !login.LimitKnown || !login.ExhaustedUntil.IsZero() {
		t.Fatal("missing native reset was invented")
	}
	f.m.observeNativeFailure("source-1", "instance", feed, codexRateLimitsUpdated, codexQuotaFrame(map[string]any{"primary": map[string]any{"usedPercent": 100, "resetsAt": reset}}))
	f.m.observeNativeFailure("source-1", "instance", feed, codexRateLimitsUpdated, codexQuotaFrame(map[string]any{"secondary": map[string]any{"usedPercent": 20, "resetsAt": reset + 3600}}))
	if login := quotaLogin(t, f, f.first.ID); login.ExhaustedUntil.Unix() != reset {
		t.Fatal("late sparse update did not preserve the actual blocking window")
	}
}

func TestCodexSparseQuotaCannotClearAKnownCreditRejection(t *testing.T) {
	used, reset, reason := 100, time.Now().Add(time.Hour).Unix(), "workspace_owner_credits_depleted"
	q := codexQuotaEvidence{}
	q.merge(codexQuotaSnapshot{Primary: &codexQuotaWindow{UsedPercent: &used, ResetsAt: &reset}, ReachedType: &reason})
	q.merge(codexQuotaSnapshot{Primary: &codexQuotaWindow{UsedPercent: &used, ResetsAt: &reset}})
	if !q.limits.reset(time.Now()).IsZero() {
		t.Fatal("unavailable sparse metadata cleared a credit rejection")
	}
}

func TestCodexLateBlockingEvidenceCanRevokeAFiniteReset(t *testing.T) {
	for _, tc := range []struct {
		name string
		late map[string]any
	}{
		{"unknown secondary window", map[string]any{"secondary": map[string]any{"usedPercent": 100}}},
		{"spend control", map[string]any{"spendControlReached": true}},
		{"credit rejection", map[string]any{"rateLimitReachedType": "workspace_owner_credits_depleted"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLoginFixture(t)
			feed := newBackendFeed()
			reset := time.Now().Add(time.Hour).Unix()
			f.m.observeNativeFailure("source-1", "instance", feed, codexRateLimitsUpdated, codexQuotaFrame(map[string]any{"primary": map[string]any{"usedPercent": 100, "resetsAt": reset}}))
			f.m.observeNativeFailure("source-1", "instance", feed, "error", codexFailureFrame(f.rows["source-1"].ConversationID, "usageLimitExceeded", false))
			f.m.observeNativeFailure("source-1", "instance", feed, codexRateLimitsUpdated, codexQuotaFrame(tc.late))
			login := quotaLogin(t, f, f.first.ID)
			if !login.LimitKnown || !login.ExhaustedUntil.IsZero() {
				t.Fatal("a later blocker left a falsely finite retry deadline")
			}
		})
	}
}

func TestCodexNonblockingQuotaDoesNotRevokeAFiniteReset(t *testing.T) {
	f := newLoginFixture(t)
	feed := newBackendFeed()
	reset := time.Now().Add(time.Hour).Unix()
	f.m.observeNativeFailure("source-1", "instance", feed, codexRateLimitsUpdated, codexQuotaFrame(map[string]any{"primary": map[string]any{"usedPercent": 100, "resetsAt": reset}}))
	f.m.observeNativeFailure("source-1", "instance", feed, "error", codexFailureFrame(f.rows["source-1"].ConversationID, "usageLimitExceeded", false))
	f.m.observeNativeFailure("source-1", "instance", feed, codexRateLimitsUpdated, codexQuotaFrame(map[string]any{"primary": map[string]any{"usedPercent": 20, "resetsAt": reset}}))
	if login := quotaLogin(t, f, f.first.ID); !login.LimitKnown || login.ExhaustedUntil.Unix() != reset {
		t.Fatal("a nonblocking snapshot turned the existing deadline into an unknown limit")
	}
}

func TestCodexNonblockingSparseUpdateCannotReviveAnOldUnknownBlocker(t *testing.T) {
	f := newLoginFixture(t)
	oldFeed, currentFeed := newBackendFeed(), newBackendFeed()
	reset := time.Now().Add(time.Hour).Unix()
	failure := codexFailureFrame(f.rows["source-1"].ConversationID, "usageLimitExceeded", false)
	f.m.observeNativeFailure("source-1", "instance", oldFeed, codexRateLimitsUpdated, codexQuotaFrame(map[string]any{"primary": map[string]any{"usedPercent": 100}}))
	f.m.observeNativeFailure("source-1", "instance", oldFeed, "error", failure)
	// Another physical feed for this active account provides the actual reset.
	f.m.observeNativeFailure("source-2", "other-instance", currentFeed, codexRateLimitsUpdated, codexQuotaFrame(map[string]any{"primary": map[string]any{"usedPercent": 100, "resetsAt": reset}}))
	f.m.observeNativeFailure("source-2", "other-instance", currentFeed, "error", codexFailureFrame(f.rows["source-2"].ConversationID, "usageLimitExceeded", false))
	for _, used := range []int{20, 100} {
		f.m.observeNativeFailure("source-1", "instance", oldFeed, codexRateLimitsUpdated, codexQuotaFrame(map[string]any{"secondary": map[string]any{"usedPercent": used, "resetsAt": reset}}))
		if login := quotaLogin(t, f, f.first.ID); login.ExhaustedUntil.Unix() != reset {
			t.Fatal("a current known/nonblocking window revoked a reset using an older unavailable window")
		}
	}
}

func TestCodexOtherFeedCannotInventARollingResetForCreditRejection(t *testing.T) {
	f := newLoginFixture(t)
	creditFeed, rollingFeed := newBackendFeed(), newBackendFeed()
	f.m.observeNativeFailure("source-1", "credit-instance", creditFeed, codexRateLimitsUpdated, codexQuotaFrame(map[string]any{"spendControlReached": true, "rateLimitReachedType": "workspace_owner_credits_depleted"}))
	f.m.observeNativeFailure("source-1", "credit-instance", creditFeed, "error", codexFailureFrame(f.rows["source-1"].ConversationID, "usageLimitExceeded", false))
	f.m.observeNativeFailure("source-2", "rolling-instance", rollingFeed, codexRateLimitsUpdated, codexQuotaFrame(map[string]any{"primary": map[string]any{"usedPercent": 100, "resetsAt": time.Now().Add(time.Hour).Unix()}}))
	if login := quotaLogin(t, f, f.first.ID); !login.LimitKnown || !login.LimitNonRolling || !login.ExhaustedUntil.IsZero() {
		t.Fatal("another feed's sparse rolling metadata cleared a known credit/spend rejection")
	}
}

func TestCodexNonRollingRejectionSurvivesStoreReopenAndFreshBackend(t *testing.T) {
	f := newLoginFixture(t)
	creditFeed := newBackendFeed()
	f.m.observeNativeFailure("source-1", "credit-instance", creditFeed, codexRateLimitsUpdated, codexQuotaFrame(map[string]any{"spendControlReached": true, "rateLimitReachedType": "workspace_owner_credits_depleted"}))
	f.m.observeNativeFailure("source-1", "credit-instance", creditFeed, "error", codexFailureFrame(f.rows["source-1"].ConversationID, "usageLimitExceeded", false))
	if err := f.m.store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := logins.Open(f.m.d.stateDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	f.m.store = store
	newFeed := newBackendFeed()
	f.m.observeNativeFailure("source-2", "after-restart", newFeed, codexRateLimitsUpdated, codexQuotaFrame(map[string]any{"primary": map[string]any{"usedPercent": 100, "resetsAt": time.Now().Add(time.Hour).Unix()}}))
	f.m.observeNativeFailure("source-2", "after-restart", newFeed, "error", codexFailureFrame(f.rows["source-2"].ConversationID, "usageLimitExceeded", false))
	if login := quotaLogin(t, f, f.first.ID); !login.LimitKnown || !login.LimitNonRolling || !login.ExhaustedUntil.IsZero() {
		t.Fatal("a daemon rejoin forgot the prior credit/spend denial")
	}
}

func TestCodexQuotaCannotExtendAnExpiredFactBeforeTheCleanupTick(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newLoginFixture(t)
		feed := newBackendFeed()
		reset := time.Now().Add(time.Hour).Unix()
		f.m.observeNativeFailure("source-1", "instance", feed, codexRateLimitsUpdated, codexQuotaFrame(map[string]any{"primary": map[string]any{"usedPercent": 100, "resetsAt": reset}}))
		f.m.observeNativeFailure("source-1", "instance", feed, "error", codexFailureFrame(f.rows["source-1"].ConversationID, "usageLimitExceeded", false))
		time.Sleep(2 * time.Hour)
		f.m.observeNativeFailure("source-1", "instance", feed, codexRateLimitsUpdated, codexQuotaFrame(map[string]any{"primary": map[string]any{"usedPercent": 100, "resetsAt": time.Now().Add(time.Hour).Unix()}}))
		if login := quotaLogin(t, f, f.first.ID); login.ExhaustedUntil.Unix() != reset {
			t.Fatal("quota-only metadata extended an expired rejection before its cleanup tick")
		}
	})
}

func TestCodexFreshTerminalAfterExpiryStartsANewUnknownFact(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newLoginFixture(t)
		feed := newBackendFeed()
		reset := time.Now().Add(time.Hour).Unix()
		f.m.observeNativeFailure("source-1", "instance", feed, codexRateLimitsUpdated, codexQuotaFrame(map[string]any{"primary": map[string]any{"usedPercent": 100, "resetsAt": reset}}))
		failure := codexFailureFrame(f.rows["source-1"].ConversationID, "usageLimitExceeded", false)
		f.m.observeNativeFailure("source-1", "instance", feed, "error", failure)
		time.Sleep(2 * time.Hour)
		f.m.observeNativeFailure("source-1", "instance", feed, "error", failure)
		if login := quotaLogin(t, f, f.first.ID); !login.LimitKnown || !login.ExhaustedUntil.IsZero() {
			t.Fatal("a fresh terminal rejection retained the expired window deadline")
		}
	})
}

func TestCodexQuotaSnapshotCannotReuseAnExpiredTerminalFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newLoginFixture(t)
		r, _ := f.m.store.Snapshot()
		if _, err := f.m.store.Remove(r.Revision, f.second.ID); err != nil {
			t.Fatal(err)
		}
		feed := newBackendFeed()
		quota := codexQuotaFrame(map[string]any{"primary": map[string]any{"usedPercent": 100, "resetsAt": time.Now().Add(time.Hour).Unix()}})
		f.m.observeNativeFailure("source-1", "instance", feed, codexRateLimitsUpdated, quota)
		f.m.observeNativeFailure("source-1", "instance", feed, "error", codexFailureFrame(f.rows["source-1"].ConversationID, "usageLimitExceeded", false))
		f.m.step()
		time.Sleep(2 * time.Hour)
		f.m.step()
		if quotaLogin(t, f, f.first.ID).LimitKnown {
			t.Fatal("single active login did not become retryable after its reset")
		}
		quota = codexQuotaFrame(map[string]any{"primary": map[string]any{"usedPercent": 100, "resetsAt": time.Now().Add(time.Hour).Unix()}})
		f.m.observeNativeFailure("source-1", "instance", feed, codexRateLimitsUpdated, quota)
		if quotaLogin(t, f, f.first.ID).LimitKnown {
			t.Fatal("a later quota snapshot reused a previous window's terminal failure")
		}
	})
}

func TestCodexQuotaCannotTurnRetriesNetworkOrCreditsErrorsIntoExhaustion(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		retry      bool
	}{
		{"native retry", "usageLimitExceeded", true}, {"network", "httpConnectionFailed", false}, {"credits", "creditsRequired", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLoginFixture(t)
			feed := newBackendFeed()
			f.m.observeNativeFailure("source-1", "instance", feed, "error", codexFailureFrame(f.rows["source-1"].ConversationID, tc.code, tc.retry))
			f.m.observeNativeFailure("source-1", "instance", feed, codexRateLimitsUpdated, codexQuotaFrame(map[string]any{"primary": map[string]any{"usedPercent": 100, "resetsAt": time.Now().Add(time.Hour).Unix()}, "credits": map[string]any{"hasCredits": false, "unlimited": false}}))
			if quotaLogin(t, f, f.first.ID).LimitKnown {
				t.Fatal("nonterminal/nonquota failure exhausted a saved login")
			}
		})
	}
}

func TestCodexQuotaRejectsOldEpochRetiredFeedAndChangedIncarnation(t *testing.T) {
	f := newLoginFixture(t)
	feed := newBackendFeed()
	quota := codexQuotaFrame(map[string]any{"primary": map[string]any{"usedPercent": 100, "resetsAt": time.Now().Add(time.Hour).Unix()}})
	f.m.observeNativeFailure("source-1", "old-instance", feed, codexRateLimitsUpdated, quota)
	f.m.observeNativeFailure("source-1", "new-instance", feed, "error", codexFailureFrame(f.rows["source-1"].ConversationID, "usageLimitExceeded", false))
	if login := quotaLogin(t, f, f.first.ID); !login.LimitKnown || !login.ExhaustedUntil.IsZero() {
		t.Fatal("new incarnation borrowed an old feed's reset")
	}
	f.m.step()
	driveLoginSwitch(t, f)
	f.m.observeNativeFailure("source-1", "old-instance", feed, codexRateLimitsUpdated, quota)
	if quotaLogin(t, f, f.second.ID).LimitKnown {
		t.Fatal("old epoch quota affected the new active login")
	}
	feed.retired.Store(true)
	child := f.rows["child-1"]
	f.m.observeNativeFailure(child.ID, "instance-B", feed, "error", codexFailureFrame(child.ConversationID, "usageLimitExceeded", false))
	if quotaLogin(t, f, f.second.ID).LimitKnown {
		t.Fatal("retired feed exhausted the new active login")
	}
}
