package phonecore

import (
	"errors"
	"path/filepath"
	"testing"
)

const testNextPhoneIncarnation = "AQAAAAAAAAAAAAAAAAAAAA"

type checkpointBoundaryStore struct {
	Store
	fail      bool
	committed bool
}

// forwardingCustodyStore models an implementation in another package: it can persist the
// opaque State it receives, but cannot inspect or increment private custody generations.
type forwardingCustodyStore struct{ st State }

func (s *forwardingCustodyStore) Load() State         { return s.st }
func (s *forwardingCustodyStore) Save(st State) error { s.st = st; return nil }
func (s *forwardingCustodyStore) ActivatePhoneBinding(st State) error {
	return s.Save(st)
}
func (s *forwardingCustodyStore) CommitPhonePairing(st State) error {
	return s.Save(st)
}
func (s *forwardingCustodyStore) ReplacePhoneCheckpoint(st State) error {
	return s.Save(st)
}
func (s *forwardingCustodyStore) PurgeKeys() error         { return nil }
func (s *forwardingCustodyStore) UnsealContent() error     { return nil }
func (s *forwardingCustodyStore) RewindRelayCursor() error { return nil }
func (s *forwardingCustodyStore) SetRelayIncarnation(string) error {
	return nil
}

func (s *checkpointBoundaryStore) ReplacePhoneCheckpoint(st State) error {
	if s.fail {
		s.fail = false
		if !s.committed {
			return errStoreDied
		}
	}
	if err := s.Store.ReplacePhoneCheckpoint(st); err != nil {
		return err
	}
	if s.committed {
		s.committed = false
		return &atomicWriteError{err: errStoreDied, committed: true}
	}
	return nil
}

func phoneCheckpointCore(t *testing.T, store Store) (*Core, PhoneBinding) {
	t.Helper()
	core, err := Resume(Config{State: store})
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
	return core, binding
}

func TestRecoverPhoneIncarnation_RequiresExactActiveAuthorityAndOldIncarnation(t *testing.T) {
	for name, tc := range map[string]struct {
		retire  bool
		binding PhoneBinding
		old     string
		wantErr error
	}{
		"wrong binding": {
			binding: phoneBinding(testOtherPhoneHome, testPhoneRID, 7), old: testPhoneIncarnation,
			wantErr: ErrPhoneBindingChanged,
		},
		"wrong old incarnation": {
			binding: phoneBinding(testPhoneHome, testPhoneRID, 7), old: testNextPhoneIncarnation,
			wantErr: ErrRelayIncarnationChanged,
		},
		"retired binding": {
			binding: phoneBinding(testPhoneHome, testPhoneRID, 7), old: testPhoneIncarnation,
			retire:  true,
			wantErr: ErrPhoneBindingChanged,
		},
	} {
		t.Run(name, func(t *testing.T) {
			core, active := phoneCheckpointCore(t, &memStore{})
			binding := tc.binding
			if binding == (PhoneBinding{}) {
				binding = active
			}
			if tc.retire {
				if err := core.CommitPhonePairing(nil, nil); err != nil {
					t.Fatal(err)
				}
			}
			before := core.State()
			if err := core.RecoverPhoneIncarnation(binding, tc.old); !errors.Is(err, tc.wantErr) {
				t.Fatalf("RecoverPhoneIncarnation = %v, want %v", err, tc.wantErr)
			}
			after := core.State()
			if after.RelayCursor != before.RelayCursor || after.RelayIncarnation != before.RelayIncarnation {
				t.Fatalf("refusal changed checkpoint: before=(%d,%q) after=(%d,%q)", before.RelayCursor, before.RelayIncarnation, after.RelayCursor, after.RelayIncarnation)
			}
		})
	}
}

func TestRecoverPhoneIncarnation_EachDurableFailureIsSafelyRetryable(t *testing.T) {
	for _, committed := range []bool{false, true} {
		name := "before commit"
		if committed {
			name = "after commit"
		}
		t.Run(name, func(t *testing.T) {
			inner := &memStore{}
			store := &checkpointBoundaryStore{Store: inner, fail: true, committed: committed}
			core, binding := phoneCheckpointCore(t, store)
			stale := core.State()
			if err := core.RecoverPhoneIncarnation(binding, testPhoneIncarnation); err == nil {
				t.Fatal("injected durable failure was swallowed")
			}
			if err := core.Save(stale); err != nil {
				t.Fatalf("interleaved stale Save: %v", err)
			}
			if err := core.RecoverPhoneIncarnation(binding, testPhoneIncarnation); err != nil {
				t.Fatalf("safe retry: %v", err)
			}
			if st := core.State(); st.RelayCursor != 0 || st.RelayIncarnation != "" {
				t.Fatalf("retry checkpoint = (%d,%q), want (0,blank)", st.RelayCursor, st.RelayIncarnation)
			}
		})
	}
}

func TestRecoverPhoneIncarnation_WrappedStoreFencesInterleavedStaleWriter(t *testing.T) {
	inner := &forwardingCustodyStore{}
	core, binding := phoneCheckpointCore(t, &wrappedPhoneStore{Store: inner})
	stale := core.State()
	if err := core.RecoverPhoneIncarnation(binding, testPhoneIncarnation); err != nil {
		t.Fatal(err)
	}
	if err := core.Save(stale); err != nil {
		t.Fatal(err)
	}
	if st := core.State(); st.RelayCursor != 0 || st.RelayIncarnation != "" {
		t.Fatalf("stale writer restored wrapped checkpoint: (%d,%q)", st.RelayCursor, st.RelayIncarnation)
	}
}

func TestPhoneCustodyTransitions_ExternalForwarderFencesStaleCoreSave(t *testing.T) {
	t.Run("pairing retirement", func(t *testing.T) {
		store := &forwardingCustodyStore{}
		core, binding := phoneCheckpointCore(t, store)
		stale := core.State()
		if err := core.CommitPhonePairing(nil, nil); err != nil {
			t.Fatal(err)
		}
		if err := core.Save(stale); err != nil {
			t.Fatal(err)
		}
		got, _ := core.PhoneBinding()
		if got.Active || got.Generation != binding.Generation {
			t.Fatalf("stale writer restored retired authority: %+v", got)
		}
		if st := core.State(); st.RelayCursor != 0 || st.RelayIncarnation != "" {
			t.Fatalf("stale writer restored pairing checkpoint: (%d,%q)", st.RelayCursor, st.RelayIncarnation)
		}
	})

	t.Run("changed activation", func(t *testing.T) {
		store := &forwardingCustodyStore{}
		core, _ := phoneCheckpointCore(t, store)
		if err := core.CommitPhonePairing(nil, nil); err != nil {
			t.Fatal(err)
		}
		if err := core.Mutate(func(st *State) {
			st.RelayCursor = 41
			st.RelayIncarnation = testPhoneIncarnation
		}); err != nil {
			t.Fatal(err)
		}
		stale := core.State()
		next := phoneBinding(testOtherPhoneHome, testPhoneRID, 1)
		if err := core.ActivatePhoneBinding(next); err != nil {
			t.Fatal(err)
		}
		if err := core.Save(stale); err != nil {
			t.Fatal(err)
		}
		if got, _ := core.PhoneBinding(); got != next {
			t.Fatalf("stale writer restored changed authority: %+v", got)
		}
		if st := core.State(); st.RelayCursor != 0 || st.RelayIncarnation != "" {
			t.Fatalf("stale writer restored activation checkpoint: (%d,%q)", st.RelayCursor, st.RelayIncarnation)
		}
	})
}

func TestPhoneCustodyTransitions_RelayGenerationOverflowFailsClosed(t *testing.T) {
	active := phoneBinding(testPhoneHome, testPhoneRID, 7)
	for name, tc := range map[string]struct {
		retired bool
		act     func(*Core, PhoneBinding) error
	}{
		"recover": {act: func(core *Core, binding PhoneBinding) error {
			return core.RecoverPhoneIncarnation(binding, testPhoneIncarnation)
		}},
		"discard": {act: func(core *Core, binding PhoneBinding) error {
			return core.AdoptPhoneDiscard(binding, testPhoneIncarnation, testNextPhoneIncarnation, 7)
		}},
		"pairing": {act: func(core *Core, _ PhoneBinding) error {
			return core.CommitPhonePairing(nil, nil)
		}},
		"activation": {retired: true, act: func(core *Core, _ PhoneBinding) error {
			return core.ActivatePhoneBinding(phoneBinding(testPhoneHome, testPhoneRID, 8))
		}},
	} {
		t.Run(name, func(t *testing.T) {
			binding := active
			if tc.retired {
				binding.Active = false
			}
			store := &forwardingCustodyStore{st: State{
				phoneBinding: binding, RelayCursor: 41, RelayIncarnation: testPhoneIncarnation,
				relayGen: ^uint64(0),
			}}
			core, err := Resume(Config{State: store})
			if err != nil {
				t.Fatal(err)
			}
			before := core.State()
			if err := tc.act(core, active); !errors.Is(err, errRelayGenerationExhausted) {
				t.Fatalf("transition = %v, want relay generation exhaustion", err)
			}
			after := core.State()
			if after.RelayCursor != before.RelayCursor || after.RelayIncarnation != before.RelayIncarnation {
				t.Fatalf("overflow changed checkpoint: before=(%d,%q) after=(%d,%q)", before.RelayCursor, before.RelayIncarnation, after.RelayCursor, after.RelayIncarnation)
			}
		})
	}
}

func TestRecoverPhoneIncarnation_StaleWriterCannotRestoreCheckpointAfterRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, StateFileName)
	wake, content := s14aNewSealer(t), s14aNewSealer(t)
	store, err := OpenStore(path, "", wake, content)
	if err != nil {
		t.Fatal(err)
	}
	core, binding := phoneCheckpointCore(t, store)
	stale := core.State()
	if err := core.RecoverPhoneIncarnation(binding, testPhoneIncarnation); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(path, "", wake, content)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Save(stale); err != nil {
		t.Fatal(err)
	}
	reopened, err = OpenStore(path, "", wake, content)
	if err != nil {
		t.Fatal(err)
	}
	if st := reopened.Load(); st.RelayCursor != 0 || st.RelayIncarnation != "" {
		t.Fatalf("stale writer restored checkpoint after restart: (%d,%q)", st.RelayCursor, st.RelayIncarnation)
	}
}

func TestAdoptPhoneDiscard_RequiresExactAuthorityAndCanonicalTransition(t *testing.T) {
	for name, tc := range map[string]struct {
		retire  bool
		binding PhoneBinding
		old     string
		next    string
		wantErr error
	}{
		"wrong binding": {
			binding: phoneBinding(testOtherPhoneHome, testPhoneRID, 7), old: testPhoneIncarnation, next: testNextPhoneIncarnation,
			wantErr: ErrPhoneBindingChanged,
		},
		"retired binding": {
			binding: phoneBinding(testPhoneHome, testPhoneRID, 7), old: testPhoneIncarnation, next: testNextPhoneIncarnation,
			retire:  true,
			wantErr: ErrPhoneBindingChanged,
		},
		"wrong old incarnation": {
			binding: phoneBinding(testPhoneHome, testPhoneRID, 7), old: testNextPhoneIncarnation, next: "AgAAAAAAAAAAAAAAAAAAAA",
			wantErr: ErrRelayIncarnationChanged,
		},
		"malformed new incarnation": {
			binding: phoneBinding(testPhoneHome, testPhoneRID, 7), old: testPhoneIncarnation, next: "not-an-incarnation",
			wantErr: ErrRelayIncarnationChanged,
		},
		"unchanged incarnation": {
			binding: phoneBinding(testPhoneHome, testPhoneRID, 7), old: testPhoneIncarnation, next: testPhoneIncarnation,
			wantErr: ErrRelayIncarnationChanged,
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := &checkpointBoundaryStore{Store: &memStore{}}
			core, active := phoneCheckpointCore(t, store)
			binding := tc.binding
			if binding == (PhoneBinding{}) {
				binding = active
			}
			if tc.retire {
				if err := core.CommitPhonePairing(nil, nil); err != nil {
					t.Fatal(err)
				}
			}
			before := core.State()
			if err := core.AdoptPhoneDiscard(binding, tc.old, tc.next, 7); !errors.Is(err, tc.wantErr) {
				t.Fatalf("AdoptPhoneDiscard = %v, want %v", err, tc.wantErr)
			}
			after := core.State()
			if after.RelayCursor != before.RelayCursor || after.RelayIncarnation != before.RelayIncarnation {
				t.Fatalf("refusal changed checkpoint: before=(%d,%q) after=(%d,%q)", before.RelayCursor, before.RelayIncarnation, after.RelayCursor, after.RelayIncarnation)
			}
		})
	}
}

func TestAdoptPhoneDiscard_AtomicBoundaryFailuresAreSafelyRetryable(t *testing.T) {
	for _, committed := range []bool{false, true} {
		name := "before commit"
		if committed {
			name = "after commit"
		}
		t.Run(name, func(t *testing.T) {
			store := &checkpointBoundaryStore{Store: &memStore{}, fail: true, committed: committed}
			core, binding := phoneCheckpointCore(t, store)
			if err := core.AdoptPhoneDiscard(binding, testPhoneIncarnation, testNextPhoneIncarnation, 7); err == nil {
				t.Fatal("injected durable failure was swallowed")
			}
			if err := core.AdoptPhoneDiscard(binding, testPhoneIncarnation, testNextPhoneIncarnation, 7); err != nil {
				t.Fatalf("safe retry: %v", err)
			}
			if st := core.State(); st.RelayCursor != 7 || st.RelayIncarnation != testNextPhoneIncarnation {
				t.Fatalf("adopted checkpoint = (%d,%q), want (7,%q)", st.RelayCursor, st.RelayIncarnation, testNextPhoneIncarnation)
			}
		})
	}
}

func TestAdoptPhoneDiscard_PostRenameFailureIsCommittedAndRetryable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, StateFileName)
	wake, content := s14aNewSealer(t), s14aNewSealer(t)
	store, err := OpenStore(path, "", wake, content)
	if err != nil {
		t.Fatal(err)
	}
	core, binding := phoneCheckpointCore(t, &wrappedPhoneStore{Store: store})
	previousSync := syncPhonecoreDir
	syncPhonecoreDir = func(string) error { return errStoreDied }
	t.Cleanup(func() { syncPhonecoreDir = previousSync })
	if err := core.AdoptPhoneDiscard(binding, testPhoneIncarnation, testNextPhoneIncarnation, 7); !atomicWriteCommitted(err) {
		t.Fatalf("AdoptPhoneDiscard = %v, want committed post-rename error", err)
	}
	syncPhonecoreDir = previousSync
	if err := core.AdoptPhoneDiscard(binding, testPhoneIncarnation, testNextPhoneIncarnation, 7); err != nil {
		t.Fatalf("retry after committed error: %v", err)
	}
	reopened, err := OpenStore(path, "", wake, content)
	if err != nil {
		t.Fatal(err)
	}
	if st := reopened.Load(); st.RelayCursor != 7 || st.RelayIncarnation != testNextPhoneIncarnation {
		t.Fatalf("committed checkpoint after restart = (%d,%q)", st.RelayCursor, st.RelayIncarnation)
	}
}

func TestAdoptPhoneDiscard_ReplacesCheckpointAndFencesStaleWritersAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, StateFileName)
	wake, content := s14aNewSealer(t), s14aNewSealer(t)
	store, err := OpenStore(path, "", wake, content)
	if err != nil {
		t.Fatal(err)
	}
	core, binding := phoneCheckpointCore(t, store)
	stale := core.State()
	if err := core.AdoptPhoneDiscard(binding, testPhoneIncarnation, testNextPhoneIncarnation, 7); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(path, "", wake, content)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Save(stale); err != nil {
		t.Fatal(err)
	}
	reopened, err = OpenStore(path, "", wake, content)
	if err != nil {
		t.Fatal(err)
	}
	if st := reopened.Load(); st.RelayCursor != 7 || st.RelayIncarnation != testNextPhoneIncarnation {
		t.Fatalf("checkpoint after restart = (%d,%q), want (7,%q)", st.RelayCursor, st.RelayIncarnation, testNextPhoneIncarnation)
	}
}
