package logins

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func handoverNative(session, incarnation string) NativeMember {
	return NativeMember{SessionID: session, NativeIncarnation: incarnation, ConversationID: "thread-" + session, NativePID: 123, NativeStartTime: 456}
}
func handoverReceipt(op Handover, n NativeMember, phase string) HandoverReceipt {
	return HandoverReceipt{HandoverID: op.ID, ToID: op.ToID, Epoch: op.Epoch, DestinationCredentialRevision: op.DestinationCredentialRevision, Native: n, Phase: phase}
}
func TestCodexLiveHandoverSeparatesSelectionAcknowledgementAndAdoption(t *testing.T) {
	s, a, lease := ownedGrantFixture(t)
	if e := lease.Close(); e != nil {
		t.Fatal(e)
	}
	b := add(t, s, ProviderCodex, "B", Credential{Auth: grantAuth("u-b", "b", time.Now().Add(time.Hour).Unix())})
	native := handoverNative("s", "native-a")
	r, op, e := s.BeginCodexHandover(snapshot(t, s).Revision, b.ID, []NativeMember{native})
	if e != nil {
		t.Fatal(e)
	}
	if r.SchemaVersion != 3 || r.Providers[ProviderCodex].ActiveID != b.ID || op.FromID != a.ID || op.Members[0].Phase != "pending" {
		t.Fatal("selection was not durable or implied native adoption")
	}
	reopened, e := Open(s.StateRoot())
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	r, e = reopened.Snapshot()
	if e != nil {
		t.Fatal(e)
	}
	if r.Handovers[ProviderCodex].ID != op.ID {
		t.Fatal("handover lost across reopen")
	}
	r, op, e = s.RecordCodexReceipt(r.Revision, handoverReceipt(op, native, "acknowledged"))
	if e != nil {
		t.Fatal(e)
	}
	if op.Members[0].AdoptedID != "" || op.Phase != "adopting" {
		t.Fatal("auth RPC acknowledgement fabricated request adoption")
	}
	if _, e = s.CompleteCodexHandover(r.Revision, op.ID); !errors.Is(e, ErrPending) {
		t.Fatalf("ack-only handover completed: %v", e)
	}
	r, op, e = s.RecordCodexReceipt(r.Revision, handoverReceipt(op, native, "adopted"))
	if e != nil {
		t.Fatal(e)
	}
	if op.Phase != "adopted" || op.Members[0].AdoptedID != b.ID {
		t.Fatal("exact adoption receipt lost")
	}
	if _, e = s.CompleteCodexHandover(r.Revision, op.ID); e != nil {
		t.Fatal(e)
	}
}
func TestCodexRetargetKeepsUnresolvedMembersAndRejectsOldReceipts(t *testing.T) {
	s, a, lease := ownedGrantFixture(t)
	_ = lease.Close()
	b := add(t, s, ProviderCodex, "B", Credential{Auth: grantAuth("u-b", "b", time.Now().Add(time.Hour).Unix())})
	c := add(t, s, ProviderCodex, "C", Credential{Auth: grantAuth("u-c", "c", time.Now().Add(time.Hour).Unix())})
	n1, n2 := handoverNative("s1", "native-1"), handoverNative("s2", "native-2")
	r, bop, e := s.BeginCodexHandover(snapshot(t, s).Revision, b.ID, []NativeMember{n1, n2})
	if e != nil {
		t.Fatal(e)
	}
	r, bop, e = s.RecordCodexReceipt(r.Revision, handoverReceipt(bop, n1, "adopted"))
	if e != nil {
		t.Fatal(e)
	}
	r, cop, e := s.RetargetCodexHandover(r.Revision, bop.ID, c.ID)
	if e != nil {
		t.Fatal(e)
	}
	if cop.ID != bop.ID || len(cop.Members) != 2 || cop.Members[0].Native != n1 || cop.Members[1].Native != n2 || cop.Members[0].AdoptedID != b.ID || cop.Members[0].Phase != "pending" {
		t.Fatal("retarget erased old membership or proven application")
	}
	for _, id := range []string{a.ID, b.ID, c.ID} {
		if _, e = s.Remove(r.Revision, id); !errors.Is(e, ErrInUse) {
			t.Fatalf("unresolved authority %s removable: %v", id, e)
		}
	}
	if _, _, e = s.RecordCodexReceipt(r.Revision, handoverReceipt(bop, n2, "adopted")); !errors.Is(e, ErrConflict) {
		t.Fatalf("obsolete B receipt accepted: %v", e)
	}
	stale := handoverReceipt(cop, n2, "adopted")
	stale.Native.NativeStartTime++
	if _, _, e = s.RecordCodexReceipt(r.Revision, stale); !errors.Is(e, ErrConflict) {
		t.Fatalf("recycled native process receipt accepted: %v", e)
	}
	r, cop, e = s.RecordCodexReceipt(r.Revision, handoverReceipt(cop, n1, "adopted"))
	if e != nil {
		t.Fatal(e)
	}
	r, cop, e = s.RecordCodexReceipt(r.Revision, handoverReceipt(cop, n2, "retired"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.CompleteCodexHandover(r.Revision, cop.ID); e != nil {
		t.Fatal(e)
	}
}
func TestCodexHandoverCapacityAndCASBeforeSelection(t *testing.T) {
	s, _, lease := ownedGrantFixture(t)
	_ = lease.Close()
	b := add(t, s, ProviderCodex, "B", Credential{Auth: grantAuth("u-b", "b", time.Now().Add(time.Hour).Unix())})
	before := snapshot(t, s)
	members := make([]NativeMember, MaxHandoverMembers+1)
	if _, _, e := s.BeginCodexHandover(before.Revision, b.ID, members); !errors.Is(e, ErrCapacity) {
		t.Fatalf("over-capacity handover accepted: %v", e)
	}
	after := snapshot(t, s)
	if after.Revision != before.Revision || after.Providers[ProviderCodex] != before.Providers[ProviderCodex] {
		t.Fatal("capacity failure changed desired selection")
	}
	if _, _, e := s.BeginCodexHandover(before.Revision+1, b.ID, nil); !errors.Is(e, ErrConflict) {
		t.Fatalf("stale registry CAS accepted: %v", e)
	}
	r, op, e := s.BeginCodexHandover(before.Revision, b.ID, nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.BeginCodexHandover(r.Revision, b.ID, nil); !errors.Is(e, ErrPending) {
		t.Fatalf("second current handover accepted: %v", e)
	}
	if _, e = s.CompleteCodexHandover(r.Revision, op.ID); e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.BeginCodexHandover(snapshot(t, s).Revision, b.ID, nil); !errors.Is(e, ErrConflict) {
		t.Fatalf("same grant/revision created blind epoch loop: %v", e)
	}
}

func TestCodexHandoverHeldAuthorityCapacityDoesNotDropOldTargets(t *testing.T) {
	s, _, lease := ownedGrantFixture(t)
	_ = lease.Close()
	var targets []Login
	for i := 0; i < MaxHandoverGrantRefs; i++ {
		account := fmt.Sprintf("target-%d", i)
		targets = append(targets, add(t, s, ProviderCodex, account, Credential{Auth: grantAuth(account, account, time.Now().Add(time.Hour).Unix())}))
	}
	r, op, e := s.BeginCodexHandover(snapshot(t, s).Revision, targets[0].ID, []NativeMember{handoverNative("s", "native")})
	if e != nil {
		t.Fatal(e)
	}
	for _, target := range targets[1 : MaxHandoverGrantRefs-1] {
		r, op, e = s.RetargetCodexHandover(r.Revision, op.ID, target.ID)
		if e != nil {
			t.Fatal(e)
		}
	}
	before := snapshot(t, s)
	if _, _, e = s.RetargetCodexHandover(r.Revision, op.ID, targets[MaxHandoverGrantRefs-1].ID); !errors.Is(e, ErrCapacity) {
		t.Fatalf("unresolved authority bound not enforced: %v", e)
	}
	after := snapshot(t, s)
	if after.Revision != before.Revision || len(after.Handovers[ProviderCodex].HeldLoginIDs) != MaxHandoverGrantRefs || after.Providers[ProviderCodex] != before.Providers[ProviderCodex] {
		t.Fatal("capacity failure discarded authority or changed desired epoch")
	}
}
func TestCodexHandoverReservesReceiptBytesBeforeSelection(t *testing.T) {
	s, _, lease := ownedGrantFixture(t)
	_ = lease.Close()
	b := add(t, s, ProviderCodex, "B", Credential{Auth: grantAuth("u-b", "b", time.Now().Add(time.Hour).Unix())})
	claude := add(t, s, ProviderClaude, "Claude", claudeCredential("claude", "c"))
	if _, e := s.BeginSwitch(snapshot(t, s).Revision, ProviderClaude, claude.ID, strings.Repeat("r", maxRegistryBytes-maxHandoverBytes/2), nil); e != nil {
		t.Fatal(e)
	}
	before := snapshot(t, s)
	if _, _, e := s.BeginCodexHandover(before.Revision, b.ID, nil); !errors.Is(e, ErrCapacity) {
		t.Fatalf("selection used receipt byte headroom: %v", e)
	}
	after := snapshot(t, s)
	if after.Revision != before.Revision || after.Providers[ProviderCodex] != before.Providers[ProviderCodex] || len(after.Handovers) != 0 {
		t.Fatal("byte capacity failure changed provider selection")
	}
}
func TestCodexHandoverSelectionWriteFailureHasNoEffect(t *testing.T) {
	s, _, lease := ownedGrantFixture(t)
	_ = lease.Close()
	b := add(t, s, ProviderCodex, "B", Credential{Auth: grantAuth("u-b", "b", time.Now().Add(time.Hour).Unix())})
	before := snapshot(t, s)
	s.writeOps.rename = func(old, new string) error {
		if new == "registry.json" {
			return errors.New("injected ENOSPC")
		}
		return s.root.Rename(old, new)
	}
	if _, _, e := s.BeginCodexHandover(before.Revision, b.ID, []NativeMember{handoverNative("s", "native")}); e == nil {
		t.Fatal("selection intent failure ignored")
	}
	s.writeOps = writeOps{}
	after := snapshot(t, s)
	if after.Revision != before.Revision || after.Providers[ProviderCodex] != before.Providers[ProviderCodex] || len(after.Handovers) != 0 {
		t.Fatal("failed durable intent changed selection")
	}
}

func TestCodexSameActiveReauthHandoverBindsNewCredentialRevision(t *testing.T) {
	s, a, lease := ownedGrantFixture(t)
	before := snapshot(t, s)
	if _, _, e := s.BeginCodexHandover(before.Revision, a.ID, nil); !errors.Is(e, ErrConflict) {
		t.Fatalf("same unchanged grant caused epoch churn: %v", e)
	}
	r, updated, e := lease.ReplaceCredential(before.Revision, Credential{Auth: grantAuth("u", "a", time.Now().Add(4*time.Hour).Unix())}, "11223344556677889900aabbccddeeff")
	if e != nil {
		t.Fatal(e)
	}
	_ = lease.Close()
	if _, e = s.Runtime(ProviderCodex); !errors.Is(e, ErrPending) {
		t.Fatalf("active reauth bypassed new desired credential epoch: %v", e)
	}
	r, op, e := s.BeginCodexHandover(r.Revision, a.ID, []NativeMember{handoverNative("s", "native")})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Runtime(ProviderCodex); e != nil {
		t.Fatalf("desired fresh-bootstrap runtime blocked on old member adoption: %v", e)
	}
	if op.DestinationCredentialRevision != updated.CredentialRevision || op.Epoch != before.Providers[ProviderCodex].Epoch+1 {
		t.Fatal("same-active reauth revision not bound")
	}
	receipt := handoverReceipt(op, op.Members[0].Native, "adopted")
	receipt.DestinationCredentialRevision--
	if _, _, e = s.RecordCodexReceipt(r.Revision, receipt); !errors.Is(e, ErrConflict) {
		t.Fatalf("old credential revision adoption accepted: %v", e)
	}
	relogin, e := s.AcquireCodexRelogin(a.ID, "ffeeddccbbaa00998877665544332211")
	if e != nil {
		t.Fatal(e)
	}
	defer relogin.Close()
	r, latest, e := relogin.ReplaceCredential(r.Revision, Credential{Auth: grantAuth("u", "a", time.Now().Add(5*time.Hour).Unix())}, "ffeeddccbbaa00998877665544332211")
	if e != nil || r.Handovers[ProviderCodex].Epoch != op.Epoch+1 || r.Handovers[ProviderCodex].DestinationCredentialRevision != latest.CredentialRevision {
		t.Fatalf("explicit inflight destination reauth did not rebind pending cohort: %v", e)
	}
}
