package phonecore

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

func stageCore(t *testing.T, dir string, wake, content Sealer) *Core {
	t.Helper()
	c, err := Resume(Config{Dir: dir, WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestPairingPushStage_SurvivesCrashAndCommitsIdempotently(t *testing.T) {
	dir := t.TempDir()
	wake, content := s14aNewSealer(t), s14aNewSealer(t)
	c := stageCore(t, dir, wake, content)
	var addr PushAddress
	addr[0] = 0x51
	var key crypto.WakeKey
	key[0] = 0x52

	if err := c.StagePushBinding(addr, key); err != nil {
		t.Fatal(err)
	}
	restarted := stageCore(t, dir, wake, content)
	if got := restarted.PendingPushBindingRevocations(); len(got) != 1 || got[0] != addr {
		t.Fatalf("pending after restart=%x, want staged address", got)
	}
	if err := restarted.CommitStagedPushBinding(addr); err != nil {
		t.Fatal(err)
	}
	if err := restarted.CommitStagedPushBinding(addr); err != nil {
		t.Fatalf("idempotent commit: %v", err)
	}
	if got := restarted.PendingPushBindingRevocations(); len(got) != 0 {
		t.Fatalf("pending after commit=%x, want none", got)
	}
	if err := restarted.AcceptWakeV1(r3aSeal(t, key, addr, 1, time.Now())); err != nil {
		t.Fatalf("committed binding refused its first wake: %v", err)
	}
}

func TestPairingPushStage_AbandonDropsKeyButRetainsDurableRevokeUntilConfirmed(t *testing.T) {
	dir := t.TempDir()
	wake, content := s14aNewSealer(t), s14aNewSealer(t)
	c := stageCore(t, dir, wake, content)
	var addr PushAddress
	addr[0] = 0x61
	var key crypto.WakeKey
	key[0] = 0x62
	if err := c.StagePushBinding(addr, key); err != nil {
		t.Fatal(err)
	}
	if err := c.AbandonStagedPushBinding(addr); err != nil {
		t.Fatal(err)
	}

	restarted := stageCore(t, dir, wake, content)
	if err := restarted.AcceptWakeV1(r3aSeal(t, key, addr, 1, time.Now())); !errors.Is(err, ErrNoWakeKey) {
		t.Fatalf("wake after abandonment=%v, want ErrNoWakeKey", err)
	}
	if got := restarted.PendingPushBindingRevocations(); len(got) != 1 || got[0] != addr {
		t.Fatalf("pending revoke after abandonment=%x, want address", got)
	}
	if err := restarted.CompleteStagedPushRevoke(addr); err != nil {
		t.Fatal(err)
	}
	if err := restarted.CompleteStagedPushRevoke(addr); err != nil {
		t.Fatalf("idempotent revoke completion: %v", err)
	}
	if got := restarted.PendingPushBindingRevocations(); len(got) != 0 {
		t.Fatalf("pending after confirmed revoke=%x, want none", got)
	}
}

func TestPairingPushOwnership_CommitsWithPinAndRecoversAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	wake, content := s14aNewSealer(t), s14aNewSealer(t)
	core, err := Resume(Config{Dir: dir, WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatal(err)
	}
	addr := PushAddress{0x31, 0x32, 0x33, 0x34}
	key := crypto.WakeKey{0x41, 0x42, 0x43, 0x44}
	if err := core.StagePushBinding(addr, key); err != nil {
		t.Fatal(err)
	}
	if err := core.MutateAndOwnStagedPushBinding(addr, func(st *State) {
		st.Machine = "ep-new-machine"
		st.EpochID = 9
	}); err != nil {
		t.Fatal(err)
	}

	// Simulate SIGKILL immediately after the one durable pin+ownership write and before
	// the separate push-store disposition. A fresh Core must see BOTH sides of that
	// decision, never a pin with no way to classify the staged allocation.
	restarted, err := Resume(Config{Dir: dir, WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatal(err)
	}
	if got := restarted.State(); got.Machine != "ep-new-machine" || got.EpochID != 9 {
		t.Fatalf("restarted pin = (%q,%d)", got.Machine, got.EpochID)
	}
	owned, ok, err := restarted.PairingPushOwnership()
	if err != nil || !ok || owned != addr {
		t.Fatalf("restarted ownership = (%x,%v,%v), want (%x,true,nil)", owned, ok, err, addr)
	}
	if got := restarted.PendingPushBindingRevocations(); len(got) != 1 || got[0] != addr {
		t.Fatalf("pending after crash = %x, want %x", got, addr)
	}
	if err := restarted.CompleteOwnedStagedPushBinding(addr); err != nil {
		t.Fatal(err)
	}

	again, err := Resume(Config{Dir: dir, WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := again.PairingPushOwnership(); err != nil || ok {
		t.Fatalf("completed ownership after second restart = (%v,%v), want absent", ok, err)
	}
	if got := again.PendingPushBindingRevocations(); len(got) != 0 {
		t.Fatalf("completed binding still pending revoke: %x", got)
	}
}

func TestPairingPushOwnership_V19MigratesWithNoInventedOwnership(t *testing.T) {
	dir := t.TempDir()
	wake, content := s14aNewSealer(t), s14aNewSealer(t)
	core, err := Resume(Config{Dir: dir, WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatal(err)
	}
	if err := core.Save(State{Machine: "ep-existing"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, StateFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var blob map[string]any
	if err := json.Unmarshal(data, &blob); err != nil {
		t.Fatal(err)
	}
	blob["schema_version"] = float64(19)
	delete(blob, "pairing_push_owned")
	data, err = json.Marshal(blob)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	restarted, err := Resume(Config{Dir: dir, WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatalf("resume pre-field v19 state: %v", err)
	}
	if _, ok, err := restarted.PairingPushOwnership(); err != nil || ok {
		t.Fatalf("v19 migration invented pairing push ownership: (%v,%v)", ok, err)
	}
}

func TestCommitStagedPushBinding_PostRenameSyncFailureKeepsCommittedStateAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	wake, content := s14aNewSealer(t), s14aNewSealer(t)
	core := stageCore(t, dir, wake, content)
	addr := PushAddress{0x51}
	if err := core.StagePushBinding(addr, crypto.WakeKey{0x52}); err != nil {
		t.Fatal(err)
	}
	previousSync := syncPhonecoreDir
	syncPhonecoreDir = func(string) error { return errors.New("injected post-rename dir sync") }
	defer func() { syncPhonecoreDir = previousSync }()
	if err := core.CommitStagedPushBinding(addr); err == nil || !atomicWriteCommitted(err) {
		t.Fatalf("commit error = %v, want committed post-rename failure", err)
	}
	if core.StagedPushBindingPending(addr) {
		t.Fatal("memory rolled back a push-store commit already renamed onto disk")
	}
	syncPhonecoreDir = previousSync
	restarted := stageCore(t, dir, wake, content)
	if restarted.StagedPushBindingPending(addr) {
		t.Fatal("restart resurrected the committed pending-revoke marker")
	}
}

func TestAbandonStagedPushBinding_PostRenameSyncFailureKeepsErasureAndObligation(t *testing.T) {
	dir := t.TempDir()
	wake, content := s14aNewSealer(t), s14aNewSealer(t)
	core := stageCore(t, dir, wake, content)
	addr := PushAddress{0x61}
	if err := core.StagePushBinding(addr, crypto.WakeKey{0x62}); err != nil {
		t.Fatal(err)
	}
	previousSync := syncPhonecoreDir
	syncPhonecoreDir = func(string) error { return errors.New("injected post-rename dir sync") }
	defer func() { syncPhonecoreDir = previousSync }()
	if err := core.AbandonStagedPushBinding(addr); err == nil || !atomicWriteCommitted(err) {
		t.Fatalf("abandon error = %v, want committed post-rename failure", err)
	}
	if b := core.push.binding(EncodePushAddress(addr)); b == nil || len(b.WakeKey) != 0 || !core.StagedPushBindingPending(addr) {
		t.Fatalf("memory rolled back committed abandonment: %+v", b)
	}
	syncPhonecoreDir = previousSync
	restarted := stageCore(t, dir, wake, content)
	if b := restarted.push.binding(EncodePushAddress(addr)); b == nil || len(b.WakeKey) != 0 || !restarted.StagedPushBindingPending(addr) {
		t.Fatalf("restart lost committed abandonment: %+v", b)
	}
}

func TestCompleteOwnedStagedPushBinding_PostRenameMarkerSyncFailureCannotResurrectPhase(t *testing.T) {
	dir := t.TempDir()
	wake, content := s14aNewSealer(t), s14aNewSealer(t)
	core := stageCore(t, dir, wake, content)
	addr := PushAddress{0x71}
	if err := core.StagePushBinding(addr, crypto.WakeKey{0x72}); err != nil {
		t.Fatal(err)
	}
	if err := core.MutateAndOwnStagedPushBinding(addr, func(st *State) { st.Machine = "ep-owned" }); err != nil {
		t.Fatal(err)
	}
	previousSync := syncPhonecoreDir
	calls := 0
	syncPhonecoreDir = func(string) error {
		calls++
		if calls == 2 {
			return errors.New("injected ownership-clear dir sync")
		}
		return nil
	}
	defer func() { syncPhonecoreDir = previousSync }()
	if err := core.CompleteOwnedStagedPushBinding(addr); err == nil || !atomicWriteCommitted(err) {
		t.Fatalf("complete error = %v, want committed marker-clear failure", err)
	}
	if _, found, err := core.PairingPushOwnership(); err != nil || found {
		t.Fatalf("memory resurrected renamed ownership clear: (%v,%v)", found, err)
	}
	syncPhonecoreDir = previousSync
	restarted := stageCore(t, dir, wake, content)
	if _, found, err := restarted.PairingPushOwnership(); err != nil || found {
		t.Fatalf("restart resurrected ownership phase: (%v,%v)", found, err)
	}
	if restarted.StagedPushBindingPending(addr) {
		t.Fatal("restart resurrected push-store compensation after completed ownership")
	}
}

func TestPairingPushOwnership_SupersedesOldBindingOnlyAfterNewOwnershipIsDurable(t *testing.T) {
	dir := t.TempDir()
	wake, content := s14aNewSealer(t), s14aNewSealer(t)
	core := stageCore(t, dir, wake, content)
	oldAddr, newAddr := PushAddress{0x81}, PushAddress{0x82}
	oldKey, newKey := crypto.WakeKey{0x91}, crypto.WakeKey{0x92}

	if err := core.StagePushBinding(oldAddr, oldKey); err != nil {
		t.Fatal(err)
	}
	if err := core.MutateAndOwnStagedPushBinding(oldAddr, func(st *State) { st.Machine = "same-machine" }); err != nil {
		t.Fatal(err)
	}
	if err := core.CompleteOwnedStagedPushBinding(oldAddr); err != nil {
		t.Fatal(err)
	}
	if err := core.StagePushBinding(newAddr, newKey); err != nil {
		t.Fatal(err)
	}
	if err := core.MutateAndOwnStagedPushBinding(newAddr, func(st *State) { st.Machine = "same-machine" }); err != nil {
		t.Fatal(err)
	}

	// SIGKILL before the new binding's push-store disposition: the old address remains the
	// sole committed fallback and MUST NOT yet be scheduled for revocation.
	crashed := stageCore(t, dir, wake, content)
	if pending := crashed.PendingPushBindingRevocations(); len(pending) != 1 || pending[0] != newAddr {
		t.Fatalf("pending before new ownership disposition=%x, want only new address", pending)
	}
	if err := crashed.AcceptWakeV1(r3aSeal(t, oldKey, oldAddr, 1, time.Now())); err != nil {
		t.Fatalf("old binding was revoked before its replacement committed: %v", err)
	}
	if err := crashed.CompleteOwnedStagedPushBinding(newAddr); err != nil {
		t.Fatal(err)
	}

	restarted := stageCore(t, dir, wake, content)
	if pending := restarted.PendingPushBindingRevocations(); len(pending) != 1 || pending[0] != oldAddr {
		t.Fatalf("pending after replacement commit=%x, want superseded old address", pending)
	}
	if err := restarted.AcceptWakeV1(r3aSeal(t, oldKey, oldAddr, 2, time.Now())); !errors.Is(err, ErrNoWakeKey) {
		t.Fatalf("superseded old binding still opens wakes: %v", err)
	}
	if err := restarted.AcceptWakeV1(r3aSeal(t, newKey, newAddr, 1, time.Now())); err != nil {
		t.Fatalf("new durable binding refused wake: %v", err)
	}
}
