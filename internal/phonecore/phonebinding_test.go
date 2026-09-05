package phonecore

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

type wrappedPhoneStore struct{ Store }

type blockingPhoneAcker struct {
	entered chan struct{}
	release chan struct{}
}

func (a *blockingPhoneAcker) Ack(uint64) error {
	close(a.entered)
	<-a.release
	return nil
}

const (
	testPhoneHome        = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testOtherPhoneHome   = "1123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testPhoneRID         = "0123456789abcdef0123456789abcdef"
	testOtherPhoneRID    = "1123456789abcdef0123456789abcdef"
	testPhoneIncarnation = "AAAAAAAAAAAAAAAAAAAAAA"
)

func testPhoneCore(t *testing.T) (*Core, *memStore) {
	t.Helper()
	store := &memStore{}
	core, err := Resume(Config{State: store})
	if err != nil {
		t.Fatal(err)
	}
	return core, store
}

func phoneBinding(home, rid string, generation uint64) PhoneBinding {
	return PhoneBinding{Home: home, PhoneRID: rid, Generation: generation, Active: true}
}

func TestPhoneBinding_ActiveReconnectAndRetiredGenerationFloor(t *testing.T) {
	core, store := testPhoneCore(t)
	binding := phoneBinding(testPhoneHome, testPhoneRID, 7)
	if err := core.ActivatePhoneBinding(binding); err != nil {
		t.Fatalf("first activation: %v", err)
	}
	if got, ok := core.PhoneBinding(); !ok || got != binding {
		t.Fatalf("PhoneBinding = (%+v,%v), want (%+v,true)", got, ok, binding)
	}
	if got := store.Load().phoneBinding; got != binding {
		t.Fatalf("activation returned before binding was durable: %+v", got)
	}
	resumed, err := Resume(Config{State: store})
	if err != nil {
		t.Fatal(err)
	}
	core = resumed
	if err := core.ActivatePhoneBinding(binding); err != nil {
		t.Fatalf("ordinary reconnect at the active generation after process restart: %v", err)
	}

	stale := core.State()
	if err := core.CommitPhonePairing(nil, func(*State) {}); err != nil {
		t.Fatalf("retire binding with pairing: %v", err)
	}
	if err := core.Save(stale); err != nil {
		t.Fatal(err)
	}
	if got, _ := core.PhoneBinding(); got.Active {
		t.Fatal("ordinary Save restored a pairing-retired active binding")
	}
	for _, generation := range []uint64{6, 7} {
		if err := core.ActivatePhoneBinding(phoneBinding(testPhoneHome, testPhoneRID, generation)); !errors.Is(err, ErrPhoneBindingStale) {
			t.Fatalf("generation %d after retired floor 7 = %v, want ErrPhoneBindingStale", generation, err)
		}
	}
	if err := core.ActivatePhoneBinding(phoneBinding(testPhoneHome, testPhoneRID, 8)); err != nil {
		t.Fatalf("generation above retired floor: %v", err)
	}
}

func TestPhoneBinding_FloorIsScopedToExactHomeAndPhoneRID(t *testing.T) {
	for name, next := range map[string]PhoneBinding{
		"home":  phoneBinding(testOtherPhoneHome, testPhoneRID, 1),
		"phone": phoneBinding(testPhoneHome, testOtherPhoneRID, 1),
	} {
		t.Run(name, func(t *testing.T) {
			core, _ := testPhoneCore(t)
			if err := core.ActivatePhoneBinding(phoneBinding(testPhoneHome, testPhoneRID, 99)); err != nil {
				t.Fatal(err)
			}
			if err := core.CommitPhonePairing(nil, func(*State) {}); err != nil {
				t.Fatal(err)
			}
			if err := core.ActivatePhoneBinding(next); err != nil {
				t.Fatalf("new scope retained generation floor: %v", err)
			}
			if got, _ := core.PhoneBinding(); got != next {
				t.Fatalf("new scope = %+v, want %+v", got, next)
			}
		})
	}
}

func TestPhoneBinding_NativeIncarnationIsCanonicalRawURLAndRecoveryTokenStaysHex(t *testing.T) {
	core, _ := testPhoneCore(t)
	binding := phoneBinding(testPhoneHome, testPhoneRID, 1)
	if err := core.ActivatePhoneBinding(binding); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		"0123456789abcdef0123456789abcdef",
		"AAAAAAAAAAAAAAAAAAAAAB",
		"AAAAAAAAAAAAAAAAAAAAAA=",
		"short",
	} {
		if err := core.SetPhoneIncarnation(binding, bad); err == nil {
			t.Fatalf("accepted noncanonical native incarnation %q", bad)
		}
	}
	if err := core.SetPhoneIncarnation(binding, testPhoneIncarnation); err != nil {
		t.Fatalf("canonical native incarnation: %v", err)
	}
	if got := core.State().RelayIncarnation; got != testPhoneIncarnation {
		t.Fatalf("persisted native incarnation = %q", got)
	}
	token, err := core.BeginRelayDiscardRecovery()
	if err != nil {
		t.Fatal(err)
	}
	if len(token) != 32 || !validRecoveryToken(token) {
		t.Fatalf("recovery token = %q, want 32 lowercase hex characters", token)
	}
}

func TestCommitPhonePairing_AtomicallyPinsOwnsPushAndResetsOnlyRelayCheckpoint(t *testing.T) {
	dir := t.TempDir()
	wake, content := s14aNewSealer(t), s14aNewSealer(t)
	core := stageCore(t, dir, wake, content)
	addr, key := PushAddress{0x31}, crypto.WakeKey{0x41}
	if err := core.StagePushBinding(addr, key); err != nil {
		t.Fatal(err)
	}
	binding := phoneBinding(testPhoneHome, testPhoneRID, 7)
	if err := core.ActivatePhoneBinding(binding); err != nil {
		t.Fatal(err)
	}
	if err := core.SetPhoneIncarnation(binding, testPhoneIncarnation); err != nil {
		t.Fatal(err)
	}
	if err := core.Mutate(func(st *State) {
		st.Machine = "m1"
		st.MachineRelayAuthPub = bytes.Repeat([]byte{0x44}, 32)
		st.OperatorNamespace = "owner"
		st.RoutingID = "rid-m1"
		st.EpochID = 7
		st.RelayCursor = 41
		st.Receive = map[Bucket]uint64{journalBucket(7): 19}
		st.GrantEpoch, st.GrantSeq = 7, 5
		st.PendingOps = []QueuedOp{{Op: "kill", SessionID: "m/s"}}
		st.PendingPublications = []PendingPublication{testComposerPublication("pending")}
	}); err != nil {
		t.Fatal(err)
	}
	if err := core.CommitPhonePairing(&addr, func(st *State) { st.MachineName = "new pin" }); err != nil {
		t.Fatal(err)
	}

	got := core.State()
	if got.MachineName != "new pin" || got.RelayCursor != 0 || got.RelayIncarnation != "" {
		t.Fatalf("pairing transaction pin/checkpoint = (%q,%d,%q)", got.MachineName, got.RelayCursor, got.RelayIncarnation)
	}
	if got.Receive[journalBucket(7)] != 19 || got.GrantEpoch != 7 || got.GrantSeq != 5 ||
		len(got.PendingOps) != 1 || len(got.PendingPublications) != 1 {
		t.Fatalf("pairing discarded semantic/replay custody: %+v", got)
	}
	if durable, ok := core.PhoneBinding(); !ok || durable.Active || durable.Generation != 7 {
		t.Fatalf("pairing did not retire old binding as its generation floor: (%+v,%v)", durable, ok)
	}
	if owned, ok, err := core.PairingPushOwnership(); err != nil || !ok || owned != addr {
		t.Fatalf("pairing push ownership = (%x,%v,%v)", owned, ok, err)
	}

	restarted := stageCore(t, dir, wake, content)
	if state := restarted.State(); state.RelayCursor != 0 || state.RelayIncarnation != "" ||
		state.Receive[journalBucket(7)] != 19 || len(state.PendingOps) != 1 || len(state.PendingPublications) != 1 {
		t.Fatalf("pairing transaction did not survive restart: %+v", state)
	}
	if durable, ok := restarted.PhoneBinding(); !ok || durable.Active || durable.Generation != 7 {
		t.Fatalf("retired generation floor did not survive restart: (%+v,%v)", durable, ok)
	}
}

func TestAcceptPhoneDelivery_RechecksBindingUnderCommitLock(t *testing.T) {
	core, _ := testPhoneCore(t)
	old := phoneBinding(testPhoneHome, testPhoneRID, 7)
	if err := core.ActivatePhoneBinding(old); err != nil {
		t.Fatal(err)
	}
	if err := core.CommitPhonePairing(nil, func(*State) {}); err != nil {
		t.Fatal(err)
	}
	if err := core.ActivatePhoneBinding(phoneBinding(testPhoneHome, testPhoneRID, 8)); err != nil {
		t.Fatal(err)
	}

	before := core.State()
	receipt, err := core.AcceptPhoneDelivery(old, []byte("stale transport bytes"), 99)
	if !errors.Is(err, ErrPhoneBindingChanged) {
		t.Fatalf("old connection delivery = (%+v,%v), want ErrPhoneBindingChanged", receipt, err)
	}
	after := core.State()
	if after.RelayCursor != before.RelayCursor || len(after.Receive) != len(before.Receive) ||
		after.GrantEpoch != before.GrantEpoch || after.GrantSeq != before.GrantSeq {
		t.Fatalf("refused old connection made durable progress: before=%+v after=%+v", before, after)
	}
}

func TestCommitPhonePairing_RebindsTheRouterToThePinnedContentKey(t *testing.T) {
	core, _ := testPhoneCore(t)
	oldKey, newKey := testContentKey(), testContentKey()
	oldKey[0], newKey[0] = 0x31, 0x41
	if err := core.Mutate(func(st *State) {
		st.EpochID = 7
		st.Keys.ContentKey = oldKey
	}); err != nil {
		t.Fatal(err)
	}
	oldBinding := phoneBinding(testPhoneHome, testPhoneRID, 7)
	if err := core.ActivatePhoneBinding(oldBinding); err != nil {
		t.Fatal(err)
	}
	if err := core.CommitPhonePairing(nil, func(st *State) { st.Keys.ContentKey = newKey }); err != nil {
		t.Fatal(err)
	}
	newBinding := phoneBinding(testPhoneHome, testPhoneRID, 8)
	if err := core.ActivatePhoneBinding(newBinding); err != nil {
		t.Fatal(err)
	}
	plain := marshalReply(t, takeControlReply())
	if _, err := core.AcceptPhoneDelivery(newBinding, sealFrame(t, oldKey, 1, plain), 1); err == nil {
		t.Fatal("router still accepted the content key retired by pairing")
	}
	if _, err := core.AcceptPhoneDelivery(newBinding, sealFrame(t, newKey, 1, plain), 1); err != nil {
		t.Fatalf("router was not rebound to the newly pinned content key: %v", err)
	}
}

func TestPhoneBinding_WrappedFileStoreForwardsCustodyCheckpointResets(t *testing.T) {
	for name, change := range map[string]func(*Core) error{
		"changed binding": func(core *Core) error {
			if err := core.CommitPhonePairing(nil, nil); err != nil {
				return err
			}
			if err := core.Mutate(func(st *State) {
				st.RelayCursor = 41
				st.RelayIncarnation = testPhoneIncarnation
			}); err != nil {
				return err
			}
			return core.ActivatePhoneBinding(phoneBinding(testOtherPhoneHome, testPhoneRID, 1))
		},
		"pairing retirement": func(core *Core) error {
			return core.CommitPhonePairing(nil, nil)
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			wake, content := s14aNewSealer(t), s14aNewSealer(t)
			inner, err := OpenStore(filepath.Join(dir, StateFileName), "", wake, content)
			if err != nil {
				t.Fatal(err)
			}
			core, err := Resume(Config{State: &wrappedPhoneStore{Store: inner}})
			if err != nil {
				t.Fatal(err)
			}
			binding := phoneBinding(testPhoneHome, testPhoneRID, 7)
			if err := core.ActivatePhoneBinding(binding); err != nil {
				t.Fatal(err)
			}
			if err := core.SetPhoneIncarnation(binding, testPhoneIncarnation); err != nil {
				t.Fatal(err)
			}
			if err := core.Mutate(func(st *State) { st.RelayCursor = 41 }); err != nil {
				t.Fatal(err)
			}
			if err := change(core); err != nil {
				t.Fatal(err)
			}
			if st := core.State(); st.RelayCursor != 0 || st.RelayIncarnation != "" {
				t.Fatalf("custody transition restored old checkpoint: (%d,%q)", st.RelayCursor, st.RelayIncarnation)
			}
		})
	}
}

func TestFileStore_OrdinarySaveCannotEraseBindingAfterReopen(t *testing.T) {
	for _, retire := range []bool{false, true} {
		name := "active"
		if retire {
			name = "retired"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, StateFileName)
			wake, content := s14aNewSealer(t), s14aNewSealer(t)
			store, err := OpenStore(path, "", wake, content)
			if err != nil {
				t.Fatal(err)
			}
			stale := store.Load()
			core, err := Resume(Config{State: store})
			if err != nil {
				t.Fatal(err)
			}
			binding := phoneBinding(testPhoneHome, testPhoneRID, 7)
			if err := core.ActivatePhoneBinding(binding); err != nil {
				t.Fatal(err)
			}
			if retire {
				if err := core.CommitPhonePairing(nil, nil); err != nil {
					t.Fatal(err)
				}
				binding.Active = false
			}
			reopened, err := OpenStore(path, "", wake, content)
			if err != nil {
				t.Fatal(err)
			}
			if err := reopened.Save(stale); err != nil {
				t.Fatal(err)
			}
			if got := reopened.Load().phoneBinding; got != binding {
				t.Fatalf("ordinary Save changed binding after reopen: %+v, want %+v", got, binding)
			}
		})
	}
}

func TestCommitPhonePairing_WaitsForDeliveryTransaction(t *testing.T) {
	store := &memStore{}
	acker := &blockingPhoneAcker{entered: make(chan struct{}), release: make(chan struct{})}
	core, err := Resume(Config{State: store, Ack: acker})
	if err != nil {
		t.Fatal(err)
	}
	key := testContentKey()
	if err := core.Mutate(func(st *State) {
		st.EpochID = 7
		st.Keys.ContentKey = key
	}); err != nil {
		t.Fatal(err)
	}
	binding := phoneBinding(testPhoneHome, testPhoneRID, 7)
	if err := core.ActivatePhoneBinding(binding); err != nil {
		t.Fatal(err)
	}
	raw := sealFrame(t, key, 1, marshalReply(t, takeControlReply()))
	deliveryDone := make(chan error, 1)
	go func() {
		_, err := core.AcceptPhoneDelivery(binding, raw, 1)
		deliveryDone <- err
	}()
	<-acker.entered

	pairingStarted := make(chan struct{})
	pairingDone := make(chan error, 1)
	go func() {
		close(pairingStarted)
		pairingDone <- core.CommitPhonePairing(nil, nil)
	}()
	<-pairingStarted
	select {
	case err := <-pairingDone:
		t.Fatalf("pairing crossed the blocked delivery transaction: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(acker.release)
	if err := <-deliveryDone; err != nil {
		t.Fatalf("delivery: %v", err)
	}
	if err := <-pairingDone; err != nil {
		t.Fatalf("pairing: %v", err)
	}
	if _, err := core.AcceptPhoneDelivery(binding, []byte("old transport"), 2); !errors.Is(err, ErrPhoneBindingChanged) {
		t.Fatalf("delivery under retired binding = %v, want ErrPhoneBindingChanged", err)
	}
}
