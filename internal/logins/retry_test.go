package logins

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func committedRows(t *testing.T, s *Store, to Login) Switch {
	t.Helper()
	r := snapshot(t, s)
	op, e := s.BeginSwitch(r.Revision, to.Provider, to.ID, "test", []Restart{{SourceID: "source-1", ConversationID: "native-1"}, {SourceID: "source-2", ConversationID: "native-2"}})
	if e != nil {
		t.Fatal(e)
	}
	op.Phase = "stopping"
	if e = s.SaveSwitch(op); e != nil {
		t.Fatal(e)
	}
	for i := range op.Sessions {
		op.Sessions[i].Phase = "stopped"
	}
	op.Phase = "stopped"
	if e = s.SaveSwitch(op); e != nil {
		t.Fatal(e)
	}
	if e = s.CaptureOutgoing(op.ID); e != nil {
		t.Fatal(e)
	}
	if e = s.InstallIncoming(op.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.CommitSwitch(op.ID); e != nil {
		t.Fatal(e)
	}
	op = snapshot(t, s).Switches[to.Provider]
	op.Phase = "resuming"
	for i := range op.Sessions {
		op.Sessions[i].Phase = "launched"
		op.Sessions[i].ChildID = "child-" + op.Sessions[i].SourceID
	}
	if e = s.SaveSwitch(op); e != nil {
		t.Fatal(e)
	}
	for i := range op.Sessions {
		op.Sessions[i].Phase = "ready"
	}
	if e = s.SaveSwitch(op); e != nil {
		t.Fatal(e)
	}
	return op
}

func TestSameJournalRetryPreservesOriginalObligationsAndIsFinite(t *testing.T) {
	s := testStore(t)
	a := add(t, s, ProviderCodex, "A", codexCredential("a", "a"))
	b := add(t, s, ProviderCodex, "B", codexCredential("b", "b"))
	c := add(t, s, ProviderCodex, "C", codexCredential("c", "c"))
	activate(t, s, a)
	r := snapshot(t, s)
	if _, e := s.UpdateAvailability(r.Revision, a.ID, AuthReady, true, time.Time{}); e != nil {
		t.Fatal(e)
	}
	op := committedRows(t, s, b)
	r = snapshot(t, s)
	if _, e := s.UpdateAvailability(r.Revision, b.ID, AuthNeedsLogin, false, time.Time{}); e != nil {
		t.Fatal(e)
	}
	if e := s.CompleteSwitch(op.ID); !errors.Is(e, ErrIneligible) {
		t.Fatalf("invalid target discharged original obligations: %v", e)
	}
	r = snapshot(t, s)
	if e := s.BeginSwitchRetryAtRevision(r.Revision-1, op.ID, c.ID, "retry"); !errors.Is(e, ErrConflict) {
		t.Fatalf("stale retarget accepted: %v", e)
	}
	if e := s.BeginSwitchRetryAtRevision(r.Revision, op.ID, c.ID, "retry"); e != nil {
		t.Fatal(e)
	}
	if e := s.BeginSwitchRetry(op.ID, c.ID, "same retry"); e != nil {
		t.Fatal(e)
	}
	if e := s.CaptureOutgoing(op.ID); !errors.Is(e, ErrInUse) {
		t.Fatalf("captured before retry stop: %v", e)
	}
	if e := s.InstallIncoming(op.ID); !errors.Is(e, ErrInUse) {
		t.Fatalf("installed before retry stop: %v", e)
	}
	op = snapshot(t, s).Switches[ProviderCodex]
	for _, row := range op.Sessions {
		if row.ChildStopClaimed {
			t.Fatal("retry intent claimed busy child")
		}
	}
	op.Sessions[0].ChildStopClaimed = true
	op.Sessions[1].ChildStopClaimed = true
	op.Sessions[1].Phase = "cancelled"
	if e := s.SaveSwitch(op); e != nil {
		t.Fatal(e)
	}
	forged := op
	forged.ToID = c.ID
	if e := s.SaveSwitch(forged); !errors.Is(e, ErrConflict) {
		t.Fatalf("generic SaveSwitch retargeted: %v", e)
	}
	forged = op
	forged.Sessions = append([]Restart(nil), op.Sessions...)
	forged.Sessions[0].ChildStopClaimed = false
	if e := s.SaveSwitch(forged); !errors.Is(e, ErrConflict) {
		t.Fatalf("generic SaveSwitch unclaimed child: %v", e)
	}
	if e := s.RetargetSwitch(op.ID); e != nil {
		t.Fatal(e)
	}
	retry := snapshot(t, s).Switches[ProviderCodex]
	if retry.ID != op.ID || retry.FromID != b.ID || retry.ToID != c.ID || retry.Phase != "stopped" || retry.ToEpoch != op.ToEpoch+1 {
		t.Fatalf("bad retarget %+v", retry)
	}
	if retry.Sessions[0].SourceID != "source-1" || retry.Sessions[0].ConversationID != "native-1" || retry.Sessions[0].ChildID != "" || retry.Sessions[0].ChildStopClaimed || retry.Sessions[0].Phase != "stopped" {
		t.Fatalf("source obligation changed %+v", retry.Sessions[0])
	}
	if !reflect.DeepEqual(retry.Sessions[1], op.Sessions[1]) {
		t.Fatal("owner-cancelled row changed during retarget")
	}
	if e := s.CaptureOutgoing(op.ID); e != nil {
		t.Fatal(e)
	}
	if e := s.InstallIncoming(op.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := s.CommitSwitch(op.ID); e != nil {
		t.Fatal(e)
	}
	r = snapshot(t, s)
	if _, e := s.UpdateAvailability(r.Revision, c.ID, AuthNeedsLogin, false, time.Time{}); e != nil {
		t.Fatal(e)
	}
	retry = snapshot(t, s).Switches[ProviderCodex]
	if _, e := s.NextEligible(ProviderCodex, retry.AttemptedIDs, time.Now()); !errors.Is(e, ErrNoEligible) {
		t.Fatalf("incident revisited a previous login: %v", e)
	}
	if e := s.BeginSwitchRetry(op.ID, b.ID, "repeat B"); !errors.Is(e, ErrIneligible) {
		t.Fatalf("repeated attempted target: %v", e)
	}
	if len(snapshot(t, s).Switches) != 1 {
		t.Fatal("exhausted incident discarded obligations")
	}
}

func TestManualRetryCanReturnToHealthyOriginalLoginOnce(t *testing.T) {
	s := testStore(t)
	a := add(t, s, ProviderCodex, "A", codexCredential("a", "a"))
	b := add(t, s, ProviderCodex, "B", codexCredential("b", "b"))
	activate(t, s, a)
	op := committedRows(t, s, b)
	r := snapshot(t, s)
	if _, e := s.UpdateAvailability(r.Revision, b.ID, AuthNeedsLogin, false, time.Time{}); e != nil {
		t.Fatal(e)
	}
	next, e := s.NextEligible(ProviderCodex, op.AttemptedIDs, time.Now())
	if e != nil || next.ID != a.ID {
		t.Fatalf("healthy original login excluded: %+v %v", next, e)
	}
	if e = s.BeginSwitchRetry(op.ID, a.ID, "return to original login"); e != nil {
		t.Fatal(e)
	}
	if e = s.RetargetSwitch(op.ID); e != nil {
		t.Fatal(e)
	}
	op = snapshot(t, s).Switches[ProviderCodex]
	if len(op.AttemptedIDs) != 2 {
		t.Fatalf("returned login not recorded: %v", op.AttemptedIDs)
	}
}

func TestSameJournalReauthenticationPromotionSurvivesCrash(t *testing.T) {
	s := testStore(t)
	a := add(t, s, ProviderCodex, "A", codexCredential("a", "a"))
	b := add(t, s, ProviderCodex, "B", codexCredential("b", "old"))
	activate(t, s, a)
	op := committedRows(t, s, b)
	r := snapshot(t, s)
	if _, e := s.UpdateAvailability(r.Revision, b.ID, AuthNeedsLogin, false, time.Time{}); e != nil {
		t.Fatal(e)
	}
	job := strings.Repeat("c", 32)
	r = snapshot(t, s)
	if e := s.BeginSwitchReloginJobAtRevision(r.Revision, op.ID, codexCredential("b", "fresh"), "reauth", job); e != nil {
		t.Fatal(e)
	}
	before, e := s.nativeCredential(ProviderCodex, b.ID)
	if e != nil || !strings.Contains(string(before.Auth), "old") {
		t.Fatal("reauth stage mutated active store before stop")
	}
	s.writeOps.rename = func(old, new string) error {
		if new == "registry.json" {
			return errors.New("crash after stage promotion")
		}
		return s.root.Rename(old, new)
	}
	if e = s.RetargetSwitch(op.ID); e == nil {
		t.Fatal("injected retarget crash not reached")
	}
	s.writeOps = writeOps{}
	if got := snapshot(t, s).Switches[ProviderCodex]; got.Phase != "retry-stopping" || got.NextID != b.ID {
		t.Fatal("lost retry stop authority")
	}
	if e = s.Reconcile(); e != nil {
		t.Fatal(e)
	}
	if e = s.RetargetSwitch(op.ID); e != nil {
		t.Fatal(e)
	}
	if e = s.root.Remove("provider/codex/native/auth.json"); e != nil {
		t.Fatal(e)
	}
	if e = s.CaptureOutgoing(op.ID); e != nil {
		t.Fatal(e)
	}
	if e = s.InstallIncoming(op.ID); e != nil {
		t.Fatal(e)
	}
	rt, e := s.CommitSwitch(op.ID)
	if e != nil {
		t.Fatal(e)
	}
	r = snapshot(t, s)
	if find(&r, b.ID).LastLoginJobID != job || r.Switches[ProviderCodex].ID != op.ID || rt.Epoch != op.ToEpoch+1 {
		t.Fatal("new grant/epoch/journal did not commit together")
	}
	auth, e := s.nativeCredential(ProviderCodex, b.ID)
	if e != nil || !strings.Contains(string(auth.Auth), "fresh") {
		t.Fatalf("fresh grant not installed: %s %v", auth.Auth, e)
	}
	if _, e = s.root.Lstat("pending/" + op.ID + "-retry.json"); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("retry staged grant retained after promotion")
	}
}
