package logins

import (
	"errors"
	"testing"
	"time"
)

func TestNonRollingExhaustionSurvivesSparseFeedsAuthUpdatesAndReopen(t *testing.T) {
	s := testStore(t)
	a := add(t, s, ProviderCodex, "A", codexCredential("a", "a"))
	r := snapshot(t, s)
	r, e := s.UpdateExhaustion(r.Revision, a.ID, time.Now().Add(time.Hour), true)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.UpdateExhaustion(r.Revision-1, a.ID, time.Now().Add(-time.Hour), false); !errors.Is(e, ErrConflict) {
		t.Fatalf("stale exhaustion CAS accepted: %v", e)
	}
	r, e = s.UpdateExhaustion(r.Revision, a.ID, time.Now().Add(-time.Minute), false)
	if e != nil {
		t.Fatal(e)
	}
	r, e = s.UpdateAvailability(r.Revision, a.ID, AuthNeedsLogin, true, time.Now().Add(-time.Minute))
	if e != nil {
		t.Fatal(e)
	}
	got := r.Logins[0]
	if !got.LimitKnown || !got.LimitNonRolling || !got.ExhaustedUntil.IsZero() || got.AuthState != AuthNeedsLogin {
		t.Fatalf("sparse feed/auth update removed terminal rejection: %+v", got)
	}
	other, e := Open(s.StateRoot())
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = other.Close() }()
	r = snapshot(t, other)
	got = r.Logins[0]
	if !got.LimitKnown || !got.LimitNonRolling || !got.ExhaustedUntil.IsZero() {
		t.Fatalf("restart forgot terminal rejection: %+v", got)
	}
	ready := got
	ready.AuthState = AuthReady
	if eligible(ready, time.Now().Add(365*24*time.Hour)) {
		t.Fatal("time passing released a nonrolling rejection")
	}
	r, e = other.UpdateAvailability(r.Revision, a.ID, AuthReady, false, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	got = r.Logins[0]
	if got.LimitKnown || got.LimitNonRolling || !got.ExhaustedUntil.IsZero() {
		t.Fatalf("explicit clear retained rejection: %+v", got)
	}
}

func TestFreshParkedReloginCrashClearsNonRollingExhaustion(t *testing.T) {
	s := testStore(t)
	a := add(t, s, ProviderCodex, "A", codexCredential("a", "old"))
	r := snapshot(t, s)
	r, e := s.UpdateExhaustion(r.Revision, a.ID, time.Time{}, true)
	if e != nil {
		t.Fatal(e)
	}
	s.writeOps.rename = func(old, new string) error {
		if new == "registry.json" {
			return errors.New("crash before fresh availability commit")
		}
		return s.root.Rename(old, new)
	}
	if _, _, e = s.Relogin(r.Revision, a.ID, codexCredential("a", "fresh")); e == nil {
		t.Fatal("injected crash not reached")
	}
	s.writeOps = writeOps{}
	if e = s.Reconcile(); e != nil {
		t.Fatal(e)
	}
	got := snapshot(t, s).Logins[0]
	if got.LimitKnown || got.LimitNonRolling || !got.ExhaustedUntil.IsZero() || got.AuthState != AuthReady {
		t.Fatalf("fresh parked grant retained old rejection: %+v", got)
	}
}

func TestActiveCaptureKeepsNonRollingExhaustionUntilFreshGrantInstalled(t *testing.T) {
	s := testStore(t)
	a := add(t, s, ProviderCodex, "A", codexCredential("a", "old"))
	activate(t, s, a)
	r := snapshot(t, s)
	r, e := s.UpdateExhaustion(r.Revision, a.ID, time.Time{}, true)
	if e != nil {
		t.Fatal(e)
	}
	op, e := s.BeginRelogin(r.Revision, a.ID, codexCredential("a", "fresh"), "sign in again", nil)
	if e != nil {
		t.Fatal(e)
	}
	op.Phase = "stopping"
	if e = s.SaveSwitch(op); e != nil {
		t.Fatal(e)
	}
	op.Phase = "stopped"
	if e = s.SaveSwitch(op); e != nil {
		t.Fatal(e)
	}
	if e = s.CaptureOutgoing(op.ID); e != nil {
		t.Fatal(e)
	}
	if !snapshot(t, s).Logins[0].LimitNonRolling {
		t.Fatal("same native grant capture released credit rejection")
	}
	if e = s.InstallIncoming(op.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.CommitSwitch(op.ID); e != nil {
		t.Fatal(e)
	}
	if e = s.CompleteSwitch(op.ID); e != nil {
		t.Fatal(e)
	}
	got := snapshot(t, s).Logins[0]
	if got.LimitKnown || got.LimitNonRolling || !got.ExhaustedUntil.IsZero() {
		t.Fatalf("fresh active grant retained rejection: %+v", got)
	}
}

func TestNonRollingMetadataRequiresKnownLimitAndUnknownReset(t *testing.T) {
	s := testStore(t)
	add(t, s, ProviderCodex, "A", codexCredential("a", "a"))
	r := snapshot(t, s)
	r.Logins[0].LimitNonRolling = true
	if e := validateRegistry(r); !errors.Is(e, ErrIneligible) {
		t.Fatalf("nonrolling without limit accepted: %v", e)
	}
	r.Logins[0].LimitKnown = true
	r.Logins[0].ExhaustedUntil = time.Now().Add(time.Hour)
	if e := validateRegistry(r); !errors.Is(e, ErrIneligible) {
		t.Fatalf("finite nonrolling reset accepted: %v", e)
	}
	r.Logins[0].ExhaustedUntil = time.Time{}
	if e := validateRegistry(r); e != nil {
		t.Fatal(e)
	}
}
