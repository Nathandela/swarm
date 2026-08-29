package phonecore

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

const testRelayDiscardIncarnation = "0123456789abcdef0123456789abcdef"

func TestAdoptRelayDiscard_AdvancesOnlyTheTransportCursor(t *testing.T) {
	bucket := Bucket{Epoch: 7}
	store := &memStore{st: State{
		RelayCursor:      7,
		RelayIncarnation: testRelayDiscardIncarnation,
		Receive:          map[Bucket]uint64{bucket: 41},
		RosterRevision:   9,
	}}
	core, err := Resume(Config{State: store})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	token, err := core.BeginRelayDiscardRecovery()
	if err != nil || token == "" {
		t.Fatalf("BeginRelayDiscardRecovery = %q, %v", token, err)
	}
	if err := core.AdoptRelayDiscard(53, testRelayDiscardIncarnation); err != nil {
		t.Fatalf("AdoptRelayDiscard: %v", err)
	}
	got := core.State()
	if got.RelayCursor != 53 || got.RelayIncarnation != testRelayDiscardIncarnation {
		t.Fatalf("transport checkpoint = cursor %d incarnation %q", got.RelayCursor, got.RelayIncarnation)
	}
	if got.Receive[bucket] != 41 || got.RosterRevision != 9 {
		t.Fatalf("discard rewrote authenticated/application state: receive=%v roster_revision=%d", got.Receive, got.RosterRevision)
	}
	if core.DiscardRecoveryToken() != token {
		t.Fatal("discard adoption cleared its pending replacement roster")
	}
}

func TestAdoptRelayDiscard_RefusesAReplyFromAnotherMailboxGeneration(t *testing.T) {
	store := &memStore{st: State{RelayCursor: 7, RelayIncarnation: testRelayDiscardIncarnation}}
	core, err := Resume(Config{State: store})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if _, err := core.BeginRelayDiscardRecovery(); err != nil {
		t.Fatalf("BeginRelayDiscardRecovery: %v", err)
	}
	if err := core.AdoptRelayDiscard(53, "11111111111111111111111111111111"); !errors.Is(err, ErrRelayIncarnationChanged) {
		t.Fatalf("AdoptRelayDiscard(other incarnation) = %v, want ErrRelayIncarnationChanged", err)
	}
	if got := core.State().RelayCursor; got != 7 {
		t.Fatalf("mismatched discard advanced cursor to %d", got)
	}
}

func TestAdoptRelayDiscard_FailedPersistenceClaimsNothing(t *testing.T) {
	inner := &memStore{st: State{RelayCursor: 7, RelayIncarnation: testRelayDiscardIncarnation}}
	store := &failAfterNStore{inner: inner, n: 0}
	core, err := Resume(Config{State: store})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if _, err := core.BeginRelayDiscardRecovery(); !errors.Is(err, errStoreDied) {
		t.Fatalf("BeginRelayDiscardRecovery persistence failure = %v, want %v", err, errStoreDied)
	}
	// Seed the pending coordinate behind the failing wrapper so this assertion remains about
	// adoption's own atomic transport write.
	inner.st.DiscardRecoveryGeneration = 1
	inner.st.DiscardRecoveryToken = "11111111111111111111111111111111"
	if err := core.AdoptRelayDiscard(53, testRelayDiscardIncarnation); !errors.Is(err, errStoreDied) {
		t.Fatalf("AdoptRelayDiscard persistence failure = %v, want %v", err, errStoreDied)
	}
	if got := core.State().RelayCursor; got != 7 {
		t.Fatalf("failed persist advanced live cursor to %d", got)
	}
	if got := inner.Load().RelayCursor; got != 7 {
		t.Fatalf("failed persist advanced durable cursor to %d", got)
	}
}

func TestAdoptRelayDiscard_SurvivesAFileBackedCoreRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), StateFileName)
	wake, content := s14aNewSealer(t), s14aNewSealer(t)
	store, err := OpenStore(path, "m1", wake, content)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	st := store.Load()
	st.RelayCursor = 7
	st.RelayIncarnation = testRelayDiscardIncarnation
	st.RosterRevision = 9
	st.Sessions = []CachedSession{{SessionID: "m1/cached", Present: true}}
	if err := store.Save(st); err != nil {
		t.Fatalf("seed file state: %v", err)
	}
	core, err := Resume(Config{State: store})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	token, err := core.BeginRelayDiscardRecovery()
	if err != nil {
		t.Fatalf("BeginRelayDiscardRecovery: %v", err)
	}
	if err := core.AdoptRelayDiscard(53, testRelayDiscardIncarnation); err != nil {
		t.Fatalf("AdoptRelayDiscard: %v", err)
	}

	reopened, err := OpenStore(path, "m1", wake, content)
	if err != nil {
		t.Fatalf("reopen durable state: %v", err)
	}
	restarted, err := Resume(Config{State: reopened})
	if err != nil {
		t.Fatalf("restart Core: %v", err)
	}
	got := restarted.State()
	if got.RelayCursor != 53 || got.RelayIncarnation != testRelayDiscardIncarnation {
		t.Fatalf("restart transport checkpoint = cursor %d incarnation %q", got.RelayCursor, got.RelayIncarnation)
	}
	if restarted.DiscardRecoveryToken() != token {
		t.Fatalf("restart recovery token = %q, want %q", restarted.DiscardRecoveryToken(), token)
	}
	if got.RosterRevision != 9 || len(got.Sessions) != 1 || got.Sessions[0].SessionID != "m1/cached" {
		t.Fatalf("restart lost cached roster while adopting transport checkpoint: revision=%d sessions=%#v", got.RosterRevision, got.Sessions)
	}
}

func TestDiscardRecoveryClearsOnlyOnContiguousMatchingTokenReseed(t *testing.T) {
	const token = "11111111111111111111111111111111"
	for _, tc := range []struct {
		name      string
		echo      string
		receive   uint64
		seq       uint64
		wantClear bool
	}{
		{name: "absent echo", seq: 1},
		{name: "wrong echo", echo: "22222222222222222222222222222222", seq: 1},
		{name: "matching but gapped", echo: token, receive: 1, seq: 3},
		{name: "matching and contiguous", echo: token, seq: 1, wantClear: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bucket := Bucket{Epoch: 7}
			store := &memStore{st: State{
				Receive:                   map[Bucket]uint64{bucket: tc.receive},
				DiscardRecoveryGeneration: 1,
				DiscardRecoveryToken:      token,
			}}
			core, err := Resume(Config{State: store})
			if err != nil {
				t.Fatalf("Resume: %v", err)
			}
			_, _, err = core.commitReceive(bucket, inboundFrame{
				kind: kindJournalReseed, seq: tc.seq, issuedAt: time.Now().UnixMilli(),
				reseed: schema.JournalReseed{Roster: []schema.JournalRecord{}, Events: []schema.JournalRecord{}, Cursor: 12, DiscardRecoveryToken: tc.echo},
			}, 20, time.Now())
			if err != nil {
				t.Fatalf("commitReceive: %v", err)
			}
			cleared := core.DiscardRecoveryToken() == ""
			if cleared != tc.wantClear {
				t.Fatalf("recovery cleared = %t, want %t; state=%#v", cleared, tc.wantClear, core.State())
			}
		})
	}
}

func TestDiscardRecoveryMatchingEchoSaveFailureKeepsPending(t *testing.T) {
	const token = "11111111111111111111111111111111"
	bucket := Bucket{Epoch: 7}
	inner := &memStore{st: State{
		DiscardRecoveryGeneration: 1,
		DiscardRecoveryToken:      token,
	}}
	core, err := Resume(Config{State: &failAfterNStore{inner: inner, n: 0}})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	_, _, err = core.commitReceive(bucket, inboundFrame{
		kind: kindJournalReseed, seq: 1, issuedAt: time.Now().UnixMilli(),
		reseed: schema.JournalReseed{Roster: []schema.JournalRecord{}, Events: []schema.JournalRecord{}, Cursor: 12, DiscardRecoveryToken: token},
	}, 20, time.Now())
	if !errors.Is(err, errStoreDied) {
		t.Fatalf("matching recovery echo persistence = %v, want %v", err, errStoreDied)
	}
	if core.DiscardRecoveryToken() != token || inner.Load().DiscardRecoveryToken != token {
		t.Fatal("failed recovery commit cleared pending token")
	}
}
