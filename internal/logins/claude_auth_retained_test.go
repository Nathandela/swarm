package logins

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/processcontain"
)

func settleClaudeAuthUnboundTest(t *testing.T, s *Store, ref ClaudeAuthRef) ClaudeAuthResult {
	t.Helper()
	if err := s.PrepareClaudeAuthRecovery(ref); err != nil {
		t.Fatal(err)
	}
	result, err := s.SettleClaudeAuth(ref)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestClaudeAuthRetainedFailureSurvivesRecoveryReopenAndRepeatedBegin(t *testing.T) {
	s, _, b, ref := newClaudeAuthUnboundTest(t)
	issued := snapshot(t, s).Providers[ProviderClaude]
	for range 3 {
		result := settleClaudeAuthUnboundTest(t, s, ref)
		if result.Phase != "failed" || result.Ref != ref || result.Selection != issued || result.Attempt != 1 {
			t.Fatalf("lost budget: %+v", result)
		}
	}
	reopened, err := OpenExisting(s.StateRoot())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	for range 3 {
		got, created, err := reopened.BeginClaudeAuth(snapshot(t, reopened).Revision, b.ID)
		if err != nil || created || got != ref {
			t.Fatalf("reset failed budget: %v %v %v", got, created, err)
		}
	}
	refs, err := reopened.ListClaudeAuth()
	if err != nil || len(refs) != 1 || refs[0].Ref != ref || refs[0].Selection != issued || refs[0].Phase != "failed" {
		t.Fatalf("restart discovery: %+v %v", refs, err)
	}
	if _, _, err := s.ClaimClaudeAuthAttempt(ref, currentClaudeAuthWorker(t)); err == nil {
		t.Fatal("late issuer started revoked job")
	}
	if err := s.RetireClaudeAuth(ref); err != nil {
		t.Fatal(err)
	}
	if refs, err := s.ListClaudeAuth(); err != nil || len(refs) != 0 {
		t.Fatalf("explicit retirement: %+v %v", refs, err)
	}
}

func TestClaudeAuthFailedReplacementNeedsExactSettledRefAndNewDesiredEpoch(t *testing.T) {
	s, _, b, ref := newClaudeAuthUnboundTest(t)
	if _, created, err := s.ReplaceClaudeAuth(snapshot(t, s).Revision, ref); err == nil || created {
		t.Fatal("replaced unadmitted budget")
	}
	settleClaudeAuthUnboundTest(t, s, ref)
	if _, created, err := s.ReplaceClaudeAuth(snapshot(t, s).Revision, ref); !errors.Is(err, ErrConflict) || created {
		t.Fatalf("warm change supplied retry authority: %v %v", created, err)
	}
	activate(t, s, b)
	revision := snapshot(t, s).Revision
	s.writeOps.rename = func(_, _ string) error { return syscall.ENOSPC }
	if _, created, err := s.ReplaceClaudeAuth(revision, ref); !errors.Is(err, syscall.ENOSPC) || created {
		t.Fatalf("failed replacement issued authority: %v %v", created, err)
	}
	s.writeOps.rename = nil
	if held, err := s.ReadClaudeAuth(ref); err != nil || held.Phase != "failed" {
		t.Fatalf("ENOSPC erased budget: %+v %v", held, err)
	}
	if _, created, err := s.ReplaceClaudeAuth(revision-1, ref); !errors.Is(err, ErrConflict) || created {
		t.Fatal("stale registry replaced budget")
	}
	other, err := OpenExisting(s.StateRoot())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	other.keychain = nil
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := 0
	var next ClaudeAuthRef
	for _, handle := range []*Store{s, other} {
		wg.Go(func() {
			got, created, err := handle.ReplaceClaudeAuth(revision, ref)
			mu.Lock()
			defer mu.Unlock()
			if created {
				winners++
				next = got
			}
			if err != nil && !errors.Is(err, ErrConflict) {
				t.Errorf("replace: %v", err)
			}
		})
	}
	wg.Wait()
	if winners != 1 || next.Nonce == ref.Nonce {
		t.Fatalf("replacement issuers=%d", winners)
	}
	if _, created, err := s.ReplaceClaudeAuth(revision, ref); !errors.Is(err, ErrConflict) || created {
		t.Fatal("old ref authorized another replacement")
	}
	result := settleClaudeAuthUnboundTest(t, s, next)
	if result.Selection.ActiveID != b.ID {
		t.Fatal("replacement lost desired epoch")
	}
	if _, created, err := s.ReplaceClaudeAuth(revision, next); !errors.Is(err, ErrConflict) || created {
		t.Fatal("same desired epoch reset failed budget")
	}
}

func TestClaudeAuthRetainedReadyRemainsConsumableAndProspectiveValidationIsPrivate(t *testing.T) {
	s, _, b, ref := newClaudeAuthTest(t)
	attempt := claimClaudeAuthTest(t, s, ref)
	stop := nativeClaudeAuthTest(t, s, ref, attempt)
	writeClaudeAuthProfileTest(t, attempt)
	stop()
	if err := s.RecordClaudeAuthContained(ref, attempt.Number); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishClaudeAuthAttempt(ref, attempt.Number); err != nil {
		t.Fatal(err)
	}
	if err := s.PrepareClaudeAuthRecovery(ref); err != nil {
		t.Fatal(err)
	}
	before, err := s.ReadClaudeAuth(ref)
	if err != nil || before.Phase != "ready" {
		t.Fatalf("recovery discarded ready: %+v %v", before, err)
	}
	if _, err := s.SettleClaudeAuth(ref); err == nil {
		t.Fatal("settled before unit proof")
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
	after, err := s.SettleClaudeAuth(ref)
	if err != nil || after.Phase != "ready" || after.ExpiresAt != before.ExpiresAt || after.Deadline != before.Deadline {
		t.Fatalf("retained receipt changed: %+v %v", after, err)
	}
	r := snapshot(t, s)
	prospective := r.Providers[ProviderClaude]
	prospective.ActiveID = b.ID
	prospective.Epoch++
	r.Providers[ProviderClaude] = prospective
	s.mu.Lock()
	err = s.locked(func() error { return s.validateClaudeAuthReceipt(r, ref, prospective.Epoch) })
	s.mu.Unlock()
	if err != nil {
		t.Fatalf("locked prospective B: %v", err)
	}
	if err := s.ValidateClaudeAuthReceipt(ref, prospective.Epoch); !errors.Is(err, ErrConflict) {
		t.Fatalf("public warm B accepted under A: %v", err)
	}
	runtime := activate(t, s, b)
	if err := s.ValidateClaudeAuthReceipt(ref, runtime.Epoch); err != nil {
		t.Fatalf("actual selected retained receipt: %v", err)
	}
	if _, created, err := s.ReplaceClaudeAuth(snapshot(t, s).Revision, ref); err == nil || created {
		t.Fatal("ready receipt supplied failed retry authority")
	}
}

func TestClaudeAuthEnumerationRejectsOrphanUnknownMalformedAndCapacity(t *testing.T) {
	for _, kind := range []string{"orphan", "unknown", "malformed", "capacity", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			s, _, _, ref := newClaudeAuthUnboundTest(t)
			base := filepath.Join(s.path, "claude-auth")
			switch kind {
			case "orphan":
				if err := os.Remove(filepath.Join(base, ref.LoginID+".json")); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(base, ref.LoginID), 0700); err != nil {
					t.Fatal(err)
				}
			case "unknown":
				if err := os.WriteFile(filepath.Join(base, "unknown"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "malformed":
				if err := os.WriteFile(filepath.Join(base, ref.LoginID+".json"), []byte(`{"version":1}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(filepath.Join(base, ref.LoginID+".json"), filepath.Join(base, "foreign.json")); err != nil {
					t.Fatal(err)
				}
			case "capacity":
				for range 2 * MaxHandoverGrantRefs {
					id, err := newID()
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(base, id+".json"), nil, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if refs, err := s.ListClaudeAuth(); err == nil || len(refs) != 0 {
				t.Fatalf("unsafe inventory returned authority: %+v %v", refs, err)
			}
		})
	}
}

func TestClaudeAuthRetainedCleanupFailureCannotSupplyReplacementAuthority(t *testing.T) {
	s, _, b, ref := newClaudeAuthUnboundTest(t)
	if _, err := s.PrepareClaudeAuthWorker(ref, "/bin/true", "2.1.310", "test"); err != nil {
		t.Fatal(err)
	}
	if err := s.PrepareClaudeAuthRecovery(ref); err != nil {
		t.Fatal(err)
	}
	s.writeOps.remove = func(string) error { return syscall.ENOSPC }
	if _, err := s.SettleClaudeAuth(ref); !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("cleanup error: %v", err)
	}
	s.mu.Lock()
	err := s.locked(func() error {
		r, err := s.load()
		if err != nil {
			return err
		}
		selected := r.Providers[ProviderClaude]
		selected.ActiveID = b.ID
		selected.Epoch++
		r.Providers[ProviderClaude] = selected
		r.Revision++
		return s.save(r)
	})
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := s.ReplaceClaudeAuth(snapshot(t, s).Revision, ref); err == nil || created {
		t.Fatal("incomplete cleanup reset budget")
	}
	s.writeOps.remove = nil
	settleClaudeAuthUnboundTest(t, s, ref)
	if _, created, err := s.ReplaceClaudeAuth(snapshot(t, s).Revision, ref); err != nil || !created {
		t.Fatalf("completed recovery replacement: %v %v", created, err)
	}
}

func TestClaudeAuthRetainedCleanupCannotRaceFailedReplacement(t *testing.T) {
	s, _, b, ref := newClaudeAuthUnboundTest(t)
	if _, err := s.PrepareClaudeAuthWorker(ref, "/bin/true", "2.1.310", "test"); err != nil {
		t.Fatal(err)
	}
	if err := s.PrepareClaudeAuthRecovery(ref); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	s.writeOps.remove = func(name string) error { close(entered); <-release; return s.root.RemoveAll(name) }
	done := make(chan error, 1)
	go func() { _, err := s.SettleClaudeAuth(ref); done <- err }()
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	defer finish()
	<-entered
	s.mu.Lock()
	err := s.locked(func() error {
		r, err := s.load()
		if err != nil {
			return err
		}
		selected := r.Providers[ProviderClaude]
		selected.ActiveID = b.ID
		selected.Epoch++
		r.Providers[ProviderClaude] = selected
		r.Revision++
		return s.save(r)
	})
	s.mu.Unlock()
	if err != nil {
		finish()
		<-done
		t.Fatal(err)
	}
	if _, created, err := s.ReplaceClaudeAuth(snapshot(t, s).Revision, ref); !errors.Is(err, ErrInUse) || created {
		finish()
		<-done
		t.Fatalf("cleanup raced replacement: %v %v", created, err)
	}
	finish()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	s.writeOps.remove = nil
	if _, created, err := s.ReplaceClaudeAuth(snapshot(t, s).Revision, ref); err != nil || !created {
		t.Fatalf("settled failure not replaceable: %v %v", created, err)
	}
}

func TestClaudeAuthEnumerationAcceptsReadOnlyStore(t *testing.T) {
	s, _, _, ref := newClaudeAuthUnboundTest(t)
	readOnly, err := OpenReadOnly(s.StateRoot())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = readOnly.Close() }()
	refs, err := readOnly.ListClaudeAuth()
	if err != nil || len(refs) != 1 || refs[0].Ref != ref {
		t.Fatalf("read-only discovery: %+v %v", refs, err)
	}
}

func TestClaudeAuthEnumerationRetainsRefAfterSavedLoginDisappears(t *testing.T) {
	s, _, b, ref := newClaudeAuthUnboundTest(t)
	s.mu.Lock()
	err := s.locked(func() error {
		r, err := s.load()
		if err != nil {
			return err
		}
		for i, login := range r.Logins {
			if login.ID == b.ID {
				r.Logins = append(r.Logins[:i], r.Logins[i+1:]...)
				break
			}
		}
		r.Revision++
		return s.save(r)
	})
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	refs, err := s.ListClaudeAuth()
	if err != nil || len(refs) != 1 || refs[0].Ref != ref {
		t.Fatalf("orphan intent lost exact custody: %+v %v", refs, err)
	}
}

func TestClaudeAuthRetainedCleanupRefusesOtherNoncePayload(t *testing.T) {
	s, _, _, ref := newClaudeAuthUnboundTest(t)
	if _, err := s.PrepareClaudeAuthWorker(ref, "/bin/true", "2.1.310", "test"); err != nil {
		t.Fatal(err)
	}
	unknown, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(s.path, claudeAuthPayload(ref.LoginID), unknown)
	if err := os.Mkdir(orphan, 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.PrepareClaudeAuthRecovery(ref); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SettleClaudeAuth(ref); !errors.Is(err, ErrPending) {
		t.Fatalf("forgot unknown nonce custody: %v", err)
	}
	held, err := s.ReadClaudeAuth(ref)
	if err != nil || held.Phase != "retiring" {
		t.Fatalf("cleanup uncertainty became settled failure: %+v %v", held, err)
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Fatalf("removed unknown nonce payload: %v", err)
	}
}

func TestClaudeAuthEnumerationWithoutHelperDirectoryIsEmpty(t *testing.T) {
	s := testStore(t)
	refs, err := s.ListClaudeAuth()
	if err != nil || len(refs) != 0 {
		t.Fatalf("new store discovery: %+v %v", refs, err)
	}
	if _, err := s.root.Lstat("claude-auth"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only discovery created helper directory: %v", err)
	}
}

func newSettledReadyClaudeAuthTest(t *testing.T) (*Store, Login, Login, ClaudeAuthRef) {
	t.Helper()
	s, a, b, ref := newClaudeAuthTest(t)
	attempt := claimClaudeAuthTest(t, s, ref)
	stop := nativeClaudeAuthTest(t, s, ref, attempt)
	writeClaudeAuthProfileTest(t, attempt)
	stop()
	if err := s.RecordClaudeAuthContained(ref, attempt.Number); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishClaudeAuthAttempt(ref, attempt.Number); err != nil {
		t.Fatal(err)
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
	if err := s.PrepareClaudeAuthRecovery(ref); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordClaudeAuthUnitRetired(ref, intent.Scope); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SettleClaudeAuth(ref); err != nil {
		t.Fatal(err)
	}
	return s, a, b, ref
}

func TestClaudeAuthSuccessfulStaleReceiptRenewsAtomicallyWithoutSelection(t *testing.T) {
	for _, kind := range []string{"generation", "expiry"} {
		t.Run(kind, func(t *testing.T) {
			s, a, b, ref := newSettledReadyClaudeAuthTest(t)
			before := snapshot(t, s)
			if _, created, err := s.ReplaceClaudeAuth(before.Revision, ref); err == nil || created {
				t.Fatal("valid warm ready reset its budget")
			}
			expires := time.Now().Add(2 * time.Hour)
			if kind == "expiry" {
				expires = time.Now().Add(-time.Minute)
			}
			if err := os.WriteFile(filepath.Join(s.savedNativeHome(b.ID), ".credentials.json"), claudeAuthGrant(expires, []string{"user:profile", "user:inference"}), 0600); err != nil {
				t.Fatal(err)
			}
			if kind == "expiry" {
				intent, err := s.readClaudeAuthIntent(b.ID)
				if err != nil {
					t.Fatal(err)
				}
				intent.Generation, _, err = s.claudeAuthGeneration(b.ID)
				if err != nil {
					t.Fatal(err)
				}
				intent.StartedAt = time.Now().Add(-10 * time.Minute).UTC()
				intent.Deadline = intent.StartedAt.Add(claudeAuthDeadline)
				intent.ExpiresAt = time.UnixMilli(expires.UnixMilli()).UTC()
				if err := s.writeClaudeAuthIntent(intent); err != nil {
					t.Fatal(err)
				}
			}
			held, err := s.ReadClaudeAuth(ref)
			if err != nil {
				t.Fatal(err)
			}
			s.writeOps.rename = func(_, _ string) error { return syscall.ENOSPC }
			if _, created, err := s.ReplaceClaudeAuth(before.Revision, ref); !errors.Is(err, syscall.ENOSPC) || created {
				t.Fatalf("stale success reset without durable replacement: %v %v", created, err)
			}
			s.writeOps.rename = nil
			unchanged, err := s.ReadClaudeAuth(ref)
			if err != nil || unchanged != held {
				t.Fatalf("failed renewal erased ready budget: %+v %v", unchanged, err)
			}
			other, err := OpenExisting(s.StateRoot())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = other.Close() }()
			other.keychain = nil
			type result struct {
				ref     ClaudeAuthRef
				created bool
				err     error
			}
			start, out := make(chan struct{}), make(chan result, 2)
			for _, handle := range []*Store{s, other} {
				go func() {
					<-start
					next, created, err := handle.ReplaceClaudeAuth(before.Revision, ref)
					out <- result{next, created, err}
				}()
			}
			close(start)
			winners := 0
			var next ClaudeAuthRef
			for range 2 {
				r := <-out
				if r.created {
					winners++
					next = r.ref
				}
				if r.err != nil && !errors.Is(r.err, ErrConflict) {
					t.Errorf("renew: %v", r.err)
				}
			}
			if winners != 1 || next.Nonce == ref.Nonce {
				t.Fatalf("stale-success issuers=%d", winners)
			}
			after := snapshot(t, s)
			if after.Revision != before.Revision || after.Providers[ProviderClaude] != before.Providers[ProviderClaude] || after.Providers[ProviderClaude].ActiveID != a.ID {
				t.Fatal("warm renewal changed selection")
			}
			settleClaudeAuthUnboundTest(t, s, next)
			if _, created, err := s.ReplaceClaudeAuth(after.Revision, next); err == nil || created {
				t.Fatal("failed renewal reset under unchanged selection")
			}
		})
	}
}

func TestClaudeAuthValidReadyRefusesReplacementEvenAtNewDesiredEpoch(t *testing.T) {
	s, _, b, ref := newSettledReadyClaudeAuthTest(t)
	activate(t, s, b)
	if _, created, err := s.ReplaceClaudeAuth(snapshot(t, s).Revision, ref); err == nil || created {
		t.Fatal("new desired epoch discarded a valid ready receipt")
	}
	if err := s.ValidateClaudeAuthReceipt(ref, snapshot(t, s).Providers[ProviderClaude].Epoch); err != nil {
		t.Fatalf("valid ready receipt was lost: %v", err)
	}
}

func TestClaudeAuthReadyRenewalRefusesUnknownCanonicalAuthority(t *testing.T) {
	s, _, b, ref := newSettledReadyClaudeAuthTest(t)
	if err := os.Remove(filepath.Join(s.savedNativeHome(b.ID), ".credentials.json")); err != nil {
		t.Fatal(err)
	}
	if _, created, err := s.ReplaceClaudeAuth(snapshot(t, s).Revision, ref); err == nil || created {
		t.Fatal("missing canonical grant supplied renewal authority")
	}
	held, err := s.ReadClaudeAuth(ref)
	if err != nil || held.Phase != "ready" {
		t.Fatalf("unknown canonical authority erased receipt: %+v %v", held, err)
	}
}

func TestClaudeAuthStaleReadyRenewalRequiresSettledUnitAndEmptyPayload(t *testing.T) {
	for _, kind := range []string{"unit", "payload"} {
		t.Run(kind, func(t *testing.T) {
			s, _, b, ref := newSettledReadyClaudeAuthTest(t)
			if err := os.WriteFile(filepath.Join(s.savedNativeHome(b.ID), ".credentials.json"), claudeAuthGrant(time.Now().Add(2*time.Hour), []string{"user:profile", "user:inference"}), 0600); err != nil {
				t.Fatal(err)
			}
			if kind == "unit" {
				intent, err := s.readClaudeAuthIntent(b.ID)
				if err != nil {
					t.Fatal(err)
				}
				intent.UnitRetired = false
				if err := s.writeClaudeAuthIntent(intent); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.MkdirAll(filepath.Join(s.path, claudeAuthPayload(b.ID), ref.Nonce), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if _, created, err := s.ReplaceClaudeAuth(snapshot(t, s).Revision, ref); err == nil || created {
				t.Fatal("stale ready renewed before exact settlement")
			}
			if held, err := s.ReadClaudeAuth(ref); err != nil || held.Phase != "ready" {
				t.Fatalf("failed renewal erased ready: %+v %v", held, err)
			}
		})
	}
}

func TestClaudeAuthReadyRenewalRefusesMalformedCanonicalGrantBeforeGenerationChange(t *testing.T) {
	cases := map[string]string{
		"scopes_number":       `{"accessToken":"synthetic","expiresAt":2000000000000,"scopes":123}`,
		"scopes_null":         `{"accessToken":"synthetic","expiresAt":2000000000000,"scopes":null}`,
		"scopes_null_element": `{"accessToken":"synthetic","expiresAt":2000000000000,"scopes":["user:profile","user:inference",null]}`,
		"expiry_null":         `{"accessToken":"synthetic","expiresAt":null,"scopes":["user:profile","user:inference"]}`,
		"expiry_missing":      `{"accessToken":"synthetic","scopes":["user:profile","user:inference"]}`,
		"expiry_string":       `{"accessToken":"synthetic","expiresAt":"expired","scopes":["user:profile","user:inference"]}`,
		"expiry_fraction":     `{"accessToken":"synthetic","expiresAt":123.5,"scopes":["user:profile","user:inference"]}`,
		"expiry_zero":         `{"accessToken":"synthetic","expiresAt":0,"scopes":["user:profile","user:inference"]}`,
	}
	for name, oauth := range cases {
		t.Run(name, func(t *testing.T) {
			s, _, b, ref := newSettledReadyClaudeAuthTest(t)
			before, err := s.ReadClaudeAuth(ref)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(s.savedNativeHome(b.ID), ".credentials.json"), []byte(`{"claudeAiOauth":`+oauth+`}`), 0600); err != nil {
				t.Fatal(err)
			}
			if _, created, err := s.ReplaceClaudeAuth(snapshot(t, s).Revision, ref); !errors.Is(err, ErrInvalidCredentials) || created {
				t.Fatalf("malformed canonical grant supplied renewal authority: %v %v", created, err)
			}
			after, err := s.ReadClaudeAuth(ref)
			if err != nil || after != before {
				t.Fatalf("malformed canonical grant erased prior ready: %+v %v", after, err)
			}
		})
	}
}
