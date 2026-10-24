package logins

import (
	"errors"
	"github.com/Nathandela/swarm/internal/processcontain"
	"reflect"
	"strings"
	"testing"
	"time"
)

func transferFixture(t *testing.T) (*Store, Login, Login, Switch, NativeMember) {
	t.Helper()
	s := testStore(t)
	s.grantWriters = func([]string, []string) ([]processcontain.Identity, error) { return nil, nil }
	a := add(t, s, ProviderCodex, "A", Credential{Auth: grantAuth("u-a", "a", time.Now().Add(time.Hour).Unix())})
	b := add(t, s, ProviderCodex, "B", Credential{Auth: grantAuth("u-b", "b", time.Now().Add(time.Hour).Unix())})
	activate(t, s, a)
	op, e := s.BeginCodexOwnershipMigrationTo(snapshot(t, s).Revision, b.ID, []Restart{{SourceID: "source", ConversationID: "thread-child"}, {SourceID: "unlaunched", ConversationID: "thread-late"}})
	if e != nil {
		t.Fatal(e)
	}
	stopOwnershipMigration(t, s, op)
	if e = s.MigrateCodexOwnership(op.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.CommitSwitch(op.ID); e != nil {
		t.Fatal(e)
	}
	op = snapshot(t, s).Switches[ProviderCodex]
	op.Phase = "resuming"
	op.Sessions[0].ChildID = "child"
	op.Sessions[0].Phase = "launched"
	if e = s.SaveSwitch(op); e != nil {
		t.Fatal(e)
	}
	child := handoverNative("child", "native-child")
	return s, a, b, op, child
}
func committedTransferFixture(t *testing.T) (*Store, Login, Login, Switch) {
	t.Helper()
	s := testStore(t)
	s.grantWriters = func([]string, []string) ([]processcontain.Identity, error) { return nil, nil }
	a := add(t, s, ProviderCodex, "A", Credential{Auth: grantAuth("u-a", "a", time.Now().Add(time.Hour).Unix())})
	b := add(t, s, ProviderCodex, "B", Credential{Auth: grantAuth("u-b", "b", time.Now().Add(time.Hour).Unix())})
	activate(t, s, a)
	op, e := s.BeginCodexOwnershipMigrationTo(snapshot(t, s).Revision, b.ID, []Restart{{SourceID: "unlaunched", ConversationID: "thread-late"}})
	if e != nil {
		t.Fatal(e)
	}
	stopOwnershipMigration(t, s, op)
	if e = s.MigrateCodexOwnership(op.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.CommitSwitch(op.ID); e != nil {
		t.Fatal(e)
	}
	return s, a, b, snapshot(t, s).Switches[ProviderCodex]
}
func TestCodexTransferPreservesBootstrapAndAllLiveMembers(t *testing.T) {
	s, a, b, op := committedTransferFixture(t)
	child := handoverNative("child", "native-child")
	c := add(t, s, ProviderCodex, "C", Credential{Auth: grantAuth("u-c", "c", time.Now().Add(time.Hour).Unix())})
	other := handoverNative("other", "native-other")
	r, h, e := s.TransferCodexMigration(snapshot(t, s).Revision, op.ID, c.ID, []NativeMember{child, other}, nil)
	if e != nil {
		t.Fatal(e)
	}
	if r.SchemaVersion != 4 || h.Version != 2 || len(r.Switches) != 0 || len(h.Bootstrap) != 1 || len(h.Members) != 2 || h.Bootstrap[0].ChildID != "" || h.Bootstrap[0].Phase != "pending" {
		t.Fatal("transfer lost native or restart obligations")
	}
	for _, id := range []string{a.ID, b.ID, c.ID} {
		if _, e = s.Remove(r.Revision, id); !errors.Is(e, ErrInUse) {
			t.Fatal("transfer released unresolved authority")
		}
	}
	if _, e = s.CompleteCodexHandover(r.Revision, h.ID); !errors.Is(e, ErrPending) {
		t.Fatal("unsettled bootstrap completed")
	}
	launchID := strings.Repeat("a", 32)
	r, h, e = s.ClaimCodexBootstrapLaunch(r.Revision, h.ID, "unlaunched", launchID)
	if e != nil {
		t.Fatal(e)
	}
	claim := *h.Bootstrap[0].Launch
	reopened, e := Open(s.StateRoot())
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	if !reflect.DeepEqual(h, snapshot(t, reopened).Handovers[ProviderCodex]) {
		t.Fatal("launch claim lost across reopen")
	}
	r, h, e = s.RetargetCodexHandover(r.Revision, h.ID, b.ID)
	if e != nil {
		t.Fatal(e)
	}
	if *h.Bootstrap[0].Launch != claim || h.Bootstrap[0].Phase != "launching" {
		t.Fatal("retarget rewrote immutable launch claim")
	}
	if _, _, e = s.RecordCodexBootstrapOutcome(r.Revision, h.ID, "unlaunched", launchID, "cancelled"); !errors.Is(e, ErrPending) {
		t.Fatal("ambiguous launch claim released")
	}
	late := handoverNative("late", "native-late")
	r, h, e = s.RecordCodexBootstrapChild(r.Revision, h.ID, "unlaunched", launchID, late)
	if e != nil {
		t.Fatal(e)
	}
	if len(h.Members) != 3 || h.Members[2].Phase != "pending" || h.Members[2].AdoptedID != "" {
		t.Fatal("late child fabricated current adoption")
	}
	if _, _, e = s.ClaimCodexBootstrapLaunch(r.Revision, h.ID, "unlaunched", strings.Repeat("b", 32)); !errors.Is(e, ErrConflict) {
		t.Fatal("bound launch replayable")
	}
	for _, source := range []string{"unlaunched"} {
		nonce := ""
		if source == "unlaunched" {
			nonce = launchID
		}
		r, h, e = s.RecordCodexBootstrapOutcome(r.Revision, h.ID, source, nonce, "ready")
		if e != nil {
			t.Fatal(e)
		}
	}
	for _, n := range []NativeMember{child, other, late} {
		r, h, e = s.RecordCodexReceipt(r.Revision, handoverReceipt(h, n, "adopted"))
		if e != nil {
			t.Fatal(e)
		}
	}
	if _, e = s.CompleteCodexHandover(r.Revision, h.ID); e != nil {
		t.Fatal(e)
	}
}
func TestCodexTransferRefusesMissingLineageAndCapacityBeforeSelection(t *testing.T) {
	s, _, _, op, child := transferFixture(t)
	c := add(t, s, ProviderCodex, "C", Credential{Auth: grantAuth("u-c", "c", time.Now().Add(time.Hour).Unix())})
	before := snapshot(t, s)
	if _, _, e := s.TransferCodexMigration(before.Revision, op.ID, c.ID, []NativeMember{child}, nil); !errors.Is(e, ErrConflict) {
		t.Fatalf("missing child lineage admitted: %v", e)
	}
	members := make([]NativeMember, MaxHandoverMembers)
	for i := range members {
		members[i] = handoverNative(strings.Repeat("s", i+1), "native")
	}
	members[0] = child
	if _, _, e := s.TransferCodexMigration(before.Revision, op.ID, c.ID, members, []BootstrapChild{{SourceID: "source", Native: child, Phase: "pending"}}); !errors.Is(e, ErrCapacity) {
		t.Fatalf("future child capacity not reserved: %v", e)
	}
	after := snapshot(t, s)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed transfer changed selection or original obligations")
	}
}

func TestCodexTransferSchemaFenceAmbiguityReopenAndWriteFailure(t *testing.T) {
	s, _, _, op, child := transferFixture(t)
	c := add(t, s, ProviderCodex, "C", Credential{Auth: grantAuth("u-c", "c", time.Now().Add(time.Hour).Unix())})
	before := snapshot(t, s)
	s.writeOps.rename = func(old, new string) error {
		if new == "registry.json" {
			return errors.New("injected transfer commit cut")
		}
		return s.root.Rename(old, new)
	}
	if _, _, e := s.TransferCodexMigration(before.Revision, op.ID, c.ID, []NativeMember{child}, []BootstrapChild{{SourceID: "source", Native: child, Phase: "pending"}}); e == nil {
		t.Fatal("transfer commit cut ignored")
	}
	s.writeOps = writeOps{}
	if !reflect.DeepEqual(before, snapshot(t, s)) {
		t.Fatal("failed transfer dropped old restart journal")
	}
	r, h, e := s.TransferCodexMigration(before.Revision, op.ID, c.ID, []NativeMember{child}, []BootstrapChild{{SourceID: "source", Native: child, Phase: "pending"}})
	if e != nil {
		t.Fatal(e)
	}
	oldReaderShape := r
	oldReaderShape.SchemaVersion = 3
	if e = validateRegistry(oldReaderShape); !errors.Is(e, ErrIneligible) {
		t.Fatal("bootstrap obligations permitted without schema fence")
	}
	wrongShape := r
	wrongShape.Handovers = map[string]Handover{ProviderCodex: h}
	bad := h
	bad.Version = 1
	wrongShape.Handovers[ProviderCodex] = bad
	if e = validateRegistry(wrongShape); !errors.Is(e, ErrIneligible) {
		t.Fatal("v1 handover silently accepted bootstrap obligations")
	}
	schemaForHandovers(&r)
	if r.SchemaVersion != 4 {
		t.Fatal("helper downgraded schema4 fence")
	}
	nonce := strings.Repeat("c", 32)
	if h.Bootstrap[1].Phase != "ambiguous" {
		t.Fatal("old resuming source inferred never-launched")
	}
	reopened, e := Open(s.StateRoot())
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	saved := snapshot(t, reopened).Handovers[ProviderCodex]
	if !reflect.DeepEqual(h, saved) {
		t.Fatal("launch claim lost across reopen")
	}
	if _, _, e = s.ClaimCodexBootstrapLaunch(r.Revision, h.ID, "unlaunched", nonce); !errors.Is(e, ErrPending) {
		t.Fatal("same ambiguous launch authorized again")
	}
	if _, _, e = s.RecordCodexBootstrapOutcome(r.Revision, h.ID, "unlaunched", "", "cancelled"); !errors.Is(e, ErrPending) {
		t.Fatal("ambiguous old launch cancelled without lineage")
	}
	r, h, e = s.RetargetCodexHandover(r.Revision, h.ID, op.ToID)
	if e != nil {
		t.Fatal(e)
	}
	if h.Bootstrap[1].Phase != "ambiguous" {
		t.Fatal("retarget cleared old launch ambiguity")
	}
	if _, _, e = s.RecordCodexBootstrapChild(r.Revision, h.ID, "unlaunched", "", handoverNative("wrong", "native-wrong")); !errors.Is(e, ErrConflict) {
		t.Fatal("wrong conversation bound to original source")
	}
}
func TestCodexTransferReservesFutureChildBytesBeforeSelection(t *testing.T) {
	s, _, _, op, child := transferFixture(t)
	c := add(t, s, ProviderCodex, "C", Credential{Auth: grantAuth("u-c", "c", time.Now().Add(time.Hour).Unix())})
	// The old migration admits this bounded source cohort; transfer must reserve
	// worst escaped native tuples for every unlaunched child, not just current JSON.
	if _, e := s.mutate(snapshot(t, s).Revision, func(r *Registry) error {
		old := r.Switches[ProviderCodex]
		for i := 2; i < 80; i++ {
			old.Sessions = append(old.Sessions, Restart{SourceID: strings.Repeat("s", i), ConversationID: strings.Repeat("<", 256), Phase: "stopped"})
		}
		r.Switches[ProviderCodex] = old
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	before := snapshot(t, s)
	if _, _, e := s.TransferCodexMigration(before.Revision, op.ID, c.ID, []NativeMember{child}, []BootstrapChild{{SourceID: "source", Native: child, Phase: "pending"}}); !errors.Is(e, ErrCapacity) {
		t.Fatalf("future escaped child footprint not reserved: %v", e)
	}
	if !reflect.DeepEqual(before, snapshot(t, s)) {
		t.Fatal("byte capacity failure changed selection")
	}
}

func TestCodexTransferAcceptsOnlyExactRecoveredChildForOldAmbiguousSource(t *testing.T) {
	s, _, _, op, child := transferFixture(t)
	c := add(t, s, ProviderCodex, "C", Credential{Auth: grantAuth("u-c", "c", time.Now().Add(time.Hour).Unix())})
	late := handoverNative("late", "native-late")
	r, h, e := s.TransferCodexMigration(snapshot(t, s).Revision, op.ID, c.ID, []NativeMember{child, late}, []BootstrapChild{{SourceID: "source", Native: child, Phase: "pending"}, {SourceID: "unlaunched", Native: late, Phase: "pending"}})
	if e != nil {
		t.Fatal(e)
	}
	if h.Bootstrap[1].ChildID != late.SessionID || h.Bootstrap[1].Phase != "bound" {
		t.Fatal("exact recovered source lineage lost")
	}
	if _, _, e = s.ClaimCodexBootstrapLaunch(r.Revision, h.ID, "unlaunched", strings.Repeat("a", 32)); !errors.Is(e, ErrConflict) {
		t.Fatal("recovered old child authorized another launch")
	}
}
func TestCodexAmbiguousBootstrapCanBindLateExistingMemberWithoutReplay(t *testing.T) {
	s, _, _, op, child := transferFixture(t)
	c := add(t, s, ProviderCodex, "C", Credential{Auth: grantAuth("u-c", "c", time.Now().Add(time.Hour).Unix())})
	late := handoverNative("late", "native-late")
	r, h, e := s.TransferCodexMigration(snapshot(t, s).Revision, op.ID, c.ID, []NativeMember{child, late}, []BootstrapChild{{SourceID: "source", Native: child, Phase: "pending"}})
	if e != nil {
		t.Fatal(e)
	}
	r, h, e = s.RecordCodexBootstrapChild(r.Revision, h.ID, "unlaunched", "", late)
	if e != nil {
		t.Fatal(e)
	}
	if len(h.Members) != 2 || h.Bootstrap[1].ChildID != late.SessionID || h.Bootstrap[1].Launch != nil {
		t.Fatal("recovered original child duplicated member or invented new claim")
	}
}

func TestCodexCommittedMigrationAlwaysTransfersBeforeFirstBootstrapClaim(t *testing.T) {
	s, _, b, op := committedTransferFixture(t)
	before := snapshot(t, s)
	r, h, e := s.TransferCodexMigration(before.Revision, op.ID, b.ID, nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Switches) != 0 || h.ToID != b.ID || h.Epoch != op.ToEpoch+1 || h.DestinationCredentialRevision != b.CredentialRevision || len(h.Bootstrap) != 1 || h.Bootstrap[0].Phase != "pending" || h.Phase != "adopting" {
		t.Fatal("same-target committed transfer skipped durable bootstrap obligations")
	}
	selected := r.Providers[ProviderCodex]
	if _, _, e = s.TransferCodexMigration(r.Revision, op.ID, b.ID, nil, nil); !errors.Is(e, ErrPending) {
		t.Fatalf("consumed migration reused: %v", e)
	}
	after := snapshot(t, s)
	if after.Revision != r.Revision || after.Providers[ProviderCodex] != selected {
		t.Fatal("duplicate transfer advanced selection")
	}
	r, h, e = s.ClaimCodexBootstrapLaunch(r.Revision, h.ID, "unlaunched", strings.Repeat("d", 32))
	if e != nil {
		t.Fatal(e)
	}
	if h.Bootstrap[0].Phase != "launching" || h.Bootstrap[0].Launch.Epoch != selected.Epoch || h.Bootstrap[0].Launch.ToID != b.ID {
		t.Fatal("first launch not bound to transferred desired tuple")
	}
	if _, _, e = s.ClaimCodexBootstrapLaunch(r.Revision, h.ID, "unlaunched", strings.Repeat("d", 32)); !errors.Is(e, ErrPending) {
		t.Fatal("persisted launch replay authorized")
	}
}
func TestCodexResumingMigrationCannotUseUnchangedSameTargetException(t *testing.T) {
	s, _, b, op, child := transferFixture(t)
	before := snapshot(t, s)
	if _, _, e := s.TransferCodexMigration(before.Revision, op.ID, b.ID, []NativeMember{child}, []BootstrapChild{{SourceID: "source", Native: child, Phase: "pending"}}); !errors.Is(e, ErrConflict) {
		t.Fatalf("resuming unchanged target admitted: %v", e)
	}
	if !reflect.DeepEqual(before, snapshot(t, s)) {
		t.Fatal("rejected resuming exception changed obligations or selection")
	}
}

func TestCodexCommittedSameTargetTransferStillRequiresRecordedChildBindings(t *testing.T) {
	s, _, b, op := committedTransferFixture(t)
	// A persisted child is never erased merely because the outer phase says committed.
	op.Sessions[0].ChildID = "late"
	op.Sessions[0].Phase = "launched"
	if e := s.SaveSwitch(op); e != nil {
		t.Fatal(e)
	}
	before := snapshot(t, s)
	if _, _, e := s.TransferCodexMigration(before.Revision, op.ID, b.ID, nil, nil); !errors.Is(e, ErrConflict) {
		t.Fatalf("committed exception dropped recorded lineage: %v", e)
	}
	if !reflect.DeepEqual(before, snapshot(t, s)) {
		t.Fatal("rejected childless transfer changed old journal")
	}
}

func TestCodexCommittedSameTargetNeverInfersLaunchedRowIsUnlaunched(t *testing.T) {
	s, _, b, op := committedTransferFixture(t)
	op.Sessions[0].Phase = "launched"
	if e := s.SaveSwitch(op); e != nil {
		t.Fatal(e)
	}
	r, h, e := s.TransferCodexMigration(snapshot(t, s).Revision, op.ID, b.ID, nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	if h.Bootstrap[0].Phase != "ambiguous" {
		t.Fatal("committed outer phase erased source launch ambiguity")
	}
	if _, _, e = s.ClaimCodexBootstrapLaunch(r.Revision, h.ID, "unlaunched", strings.Repeat("e", 32)); !errors.Is(e, ErrPending) {
		t.Fatal("ambiguous launched row authorized a fresh child")
	}
}

func TestCodexBootstrapNoEffectResetRequiresExactUnboundClaim(t *testing.T) {
	s, a, b, op := committedTransferFixture(t)
	r, h, e := s.TransferCodexMigration(snapshot(t, s).Revision, op.ID, b.ID, nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	old := strings.Repeat("a", 32)
	r, h, e = s.ClaimCodexBootstrapLaunch(r.Revision, h.ID, "unlaunched", old)
	if e != nil {
		t.Fatal(e)
	}
	// Selection can change, but resetting never changes the original source obligation.
	r, h, e = s.RetargetCodexHandover(r.Revision, h.ID, a.ID)
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.ResetCodexBootstrapLaunch(r.Revision, h.ID, "unlaunched", strings.Repeat("b", 32)); !errors.Is(e, ErrConflict) {
		t.Fatal("wrong nonce reset claim")
	}
	r, h, e = s.ResetCodexBootstrapLaunch(r.Revision, h.ID, "unlaunched", old)
	if e != nil {
		t.Fatal(e)
	}
	if h.Bootstrap[0].Launch != nil || h.Bootstrap[0].Phase != "pending" {
		t.Fatal("no-effect reset erased obligation or retained claim")
	}
	r, h, e = s.ClaimCodexBootstrapLaunch(r.Revision, h.ID, "unlaunched", strings.Repeat("c", 32))
	if e != nil {
		t.Fatal(e)
	}
	n := handoverNative("child", "native-child")
	n.ConversationID = "thread-late"
	if _, _, e = s.RecordCodexBootstrapChild(r.Revision, h.ID, "unlaunched", old, n); !errors.Is(e, ErrConflict) {
		t.Fatal("old nonce bound replacement claim")
	}
	r, h, e = s.RecordCodexBootstrapChild(r.Revision, h.ID, "unlaunched", strings.Repeat("c", 32), n)
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.ResetCodexBootstrapLaunch(r.Revision, h.ID, "unlaunched", strings.Repeat("c", 32)); !errors.Is(e, ErrConflict) {
		t.Fatal("bound child reset as no-effect")
	}
}

func TestCodexExternalBirthUsesCanonicalHomeAcrossSelectionChange(t *testing.T) {
	s, a, b, op := committedTransferFixture(t)
	r, h, e := s.TransferCodexMigration(snapshot(t, s).Revision, op.ID, b.ID, nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	claim := strings.Repeat("d", 32)
	r, h, e = s.ClaimCodexBootstrapLaunch(r.Revision, h.ID, "unlaunched", claim)
	if e != nil {
		t.Fatal(e)
	}
	rt, e := s.Runtime(ProviderCodex)
	if e != nil {
		t.Fatal(e)
	}
	_, _, e = s.RetargetCodexHandover(r.Revision, h.ID, a.ID)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.ValidateRuntime(rt); e == nil {
		t.Fatal("legacy exact validation accepted historical runtime")
	}
	env, e := s.PrepareCodexExternalBirth(rt, []string{"PATH=/bin", "OPENAI_API_KEY=synthetic"})
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(strings.Join(env, "\n"), "synthetic") {
		t.Fatal("birth retained credential selector")
	}
	if e = s.ValidateCodexBootstrapBirth(claim, "unlaunched", "thread-late", "child"); e != nil {
		t.Fatal(e)
	}
	if e = s.ValidateCodexBootstrapBirth(claim, "wrong", "thread-late", "child"); e == nil {
		t.Fatal("wrong source accepted")
	}
	for _, bad := range []Runtime{{Provider: ProviderClaude, ExternalAuth: true, Home: rt.Home, Epoch: rt.Epoch, Identity: rt.Identity}, {Provider: ProviderCodex, Home: rt.Home, Epoch: rt.Epoch, Identity: rt.Identity}, {Provider: ProviderCodex, ExternalAuth: true, Home: rt.Home + "/wrong", Epoch: rt.Epoch, Identity: rt.Identity}} {
		if _, e = s.PrepareCodexExternalBirth(bad, nil); e == nil {
			t.Fatal("unowned birth routing accepted")
		}
	}
}
