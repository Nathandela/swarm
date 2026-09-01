package swarmmobile

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/crypto"
)

type publicationCustody struct{}

func phonecoreTestEpochKeys() crypto.EpochKeys {
	var keys crypto.EpochKeys
	for i := range keys.WakeKey {
		keys.WakeKey[i] = byte(i + 1)
		keys.ContentKey[i] = byte(i + 33)
	}
	return keys
}

func (publicationCustody) WakeKEK() ([]byte, error) {
	sum := sha256.Sum256([]byte("publication-wake-kek"))
	return sum[:], nil
}

func (publicationCustody) ContentKEK() ([]byte, error) {
	sum := sha256.Sum256([]byte("publication-content-kek"))
	return sum[:], nil
}

type scriptedAppender struct {
	mu       sync.Mutex
	failures int
	calls    [][]byte
	called   chan struct{}
	onAppend func()
	before   func()
}

func (s *scriptedAppender) beforeMailboxAppend() {
	if s.before != nil {
		s.before()
	}
}

func (s *scriptedAppender) MailboxAppend(_ context.Context, _ string, envelope []byte) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, bytes.Clone(envelope))
	if s.called != nil {
		select {
		case s.called <- struct{}{}:
		default:
		}
	}
	if s.onAppend != nil {
		s.onAppend()
	}
	if s.failures > 0 {
		s.failures--
		return 0, errors.New("append delivery unknown")
	}
	return uint64(len(s.calls)), nil
}

type publicationMemoryStore struct {
	mu       sync.Mutex
	state    phonecore.State
	failNext bool
}

var errPublicationStore = errors.New("publication test: durable commit failed")

func (s *publicationMemoryStore) Load() phonecore.State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *publicationMemoryStore) Save(st phonecore.State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failNext {
		s.failNext = false
		return errPublicationStore
	}
	s.state = st
	return nil
}

func (s *publicationMemoryStore) PurgeKeys() error {
	s.mu.Lock()
	s.state.PendingPublications = nil
	s.mu.Unlock()
	return nil
}

func (s *publicationMemoryStore) UnsealContent() error { return nil }
func (s *publicationMemoryStore) RewindRelayCursor() error {
	s.mu.Lock()
	s.state.RelayCursor = 0
	s.mu.Unlock()
	return nil
}
func (s *publicationMemoryStore) SetRelayIncarnation(v string) error {
	s.mu.Lock()
	s.state.RelayIncarnation = v
	s.mu.Unlock()
	return nil
}

func publicationAppFromStore(t *testing.T, store *publicationMemoryStore) (*App, sendCtx) {
	t.Helper()
	core, err := phonecore.Resume(phonecore.Config{Machine: "m1", State: store})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	a := &App{core: core, publicationWake: make(chan struct{}, 1)}
	a.setDestination(store.Load().MachineRelayAuthPub)
	st := core.State()
	target, _ := a.destination()
	return a, sendCtx{target: target, key: st.Keys.ContentKey, epoch: st.EpochID}
}

func (s *scriptedAppender) envelopes() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][]byte, len(s.calls))
	for i := range s.calls {
		out[i] = bytes.Clone(s.calls[i])
	}
	return out
}

func publicationApp(t *testing.T, dir string) (*App, sendCtx) {
	t.Helper()
	a, err := NewApp(&Config{StateDir: dir, MachineID: "m1"}, publicationCustody{})
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	st := a.core.State()
	if st.EpochID == 0 {
		keys, err := crypto.NewEpochKeys()
		if err != nil {
			t.Fatalf("NewEpochKeys: %v", err)
		}
		machineRelayPub := bytes.Repeat([]byte{0x44}, 32)
		st.Machine = "m1"
		st.MachineRelayAuthPub = machineRelayPub
		st.RoutingID = "phone-routing-id"
		st.EpochID = 7
		st.Keys = keys
		if err := a.core.Save(st); err != nil {
			t.Fatalf("seed state: %v", err)
		}
		a.setDestination(machineRelayPub)
		st = a.core.State()
	}
	target, _ := a.destination()
	return a, sendCtx{target: target, key: st.Keys.ContentKey, epoch: st.EpochID}
}

func preparedComposer(t *testing.T, a *App, sc sendCtx, operationID, text string) phonecore.PendingPublication {
	t.Helper()
	st := a.core.State()
	body := &schema.ComposerSendReq{
		Session: "m1/s1", SessionInstance: "instance-1", ExpectedTurn: "turn-1", Text: text,
	}
	return phonecore.PendingPublication{
		LogicalID: operationID, OperationID: operationID, Kind: phonecore.PublicationComposer,
		SessionID: body.Session, SessionInstance: body.SessionInstance,
		ExpectedTurn: body.ExpectedTurn, Text: body.Text,
		Machine: st.Machine, EpochID: st.EpochID, Target: sc.target,
		AuthorityPub: st.MachineRelayAuthPub,
		Command: schema.DeviceCommandAuth{
			Action: schema.ActionComposerSend, Machine: st.Machine, Session: body.Session,
			OperationID: operationID, ExpiresAt: time.Now().Add(time.Minute),
		},
		Composer: body, Phase: phonecore.PublicationPrepared, CreatedAt: time.Now(),
	}
}

func flushForTest(a *App, sc sendCtx) error {
	a.bucketMu.Lock()
	defer a.bucketMu.Unlock()
	return a.flushPendingPublicationsLocked(context.Background(), sc)
}

func TestPublicationPump_FailedAppendRestartsWithTheExactEnvelope(t *testing.T) {
	dir := t.TempDir()
	a, sc := publicationApp(t, dir)
	p := preparedComposer(t, a, sc, "op-crash", "ship it")
	if err := a.core.PreparePublication(p); err != nil {
		t.Fatalf("PreparePublication: %v", err)
	}

	firstAppender := &scriptedAppender{failures: 1}
	sc.cl = firstAppender
	if err := flushForTest(a, sc); err == nil {
		t.Fatal("failed append returned nil")
	}
	sealed := a.core.PendingPublications()
	if len(sealed) != 1 || sealed[0].Phase != phonecore.PublicationSealed || len(sealed[0].Envelope) == 0 {
		t.Fatalf("after failed append = %+v, want one exact sealed record", sealed)
	}
	wantEnvelope := bytes.Clone(sealed[0].Envelope)
	wantSequence := sealed[0].Sequence
	if err := a.Close(); err != nil {
		t.Fatalf("close first process: %v", err)
	}

	restarted, restartedSC := publicationApp(t, filepath.Clean(dir))
	secondAppender := &scriptedAppender{}
	restartedSC.cl = secondAppender
	if err := flushForTest(restarted, restartedSC); err != nil {
		t.Fatalf("restart flush: %v", err)
	}
	calls := secondAppender.envelopes()
	if len(calls) != 1 || !bytes.Equal(calls[0], wantEnvelope) {
		t.Fatalf("restart appended %d envelopes, exact bytes match = %v", len(calls), len(calls) == 1 && bytes.Equal(calls[0], wantEnvelope))
	}
	got := restarted.core.PendingPublications()
	if len(got) != 1 || got[0].Phase != phonecore.PublicationAdmitted || got[0].Sequence != wantSequence {
		t.Fatalf("restart projection = %+v", got)
	}
	if err := flushForTest(restarted, restartedSC); err != nil {
		t.Fatalf("idempotent flush: %v", err)
	}
	if n := len(secondAppender.envelopes()); n != 1 {
		t.Fatalf("admitted record was appended again: %d calls", n)
	}
}

func TestPublicationPump_CrashAfterSequenceBeforeSealBurnsOnlyTheReservation(t *testing.T) {
	dir := t.TempDir()
	a, sc := publicationApp(t, dir)
	p := preparedComposer(t, a, sc, "op-prepared-crash", "still send me")
	if err := a.core.PreparePublication(p); err != nil {
		t.Fatalf("PreparePublication: %v", err)
	}
	burned, err := a.core.Seq().NextCommand()
	if err != nil {
		t.Fatalf("NextCommand before crash: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("close first process: %v", err)
	}

	restarted, restartedSC := publicationApp(t, filepath.Clean(dir))
	appender := &scriptedAppender{}
	restartedSC.cl = appender
	if err := flushForTest(restarted, restartedSC); err != nil {
		t.Fatalf("restart flush: %v", err)
	}
	got := restarted.core.PendingPublications()
	if len(got) != 1 || got[0].Phase != phonecore.PublicationAdmitted || got[0].Sequence <= burned {
		t.Fatalf("restart after pre-seal crash = %+v, burned sequence %d", got, burned)
	}
}

func TestPublicationPump_AppendSuccessBeforeAdmissionCommitRetriesTheExactEnvelope(t *testing.T) {
	pub := bytes.Repeat([]byte{0x44}, 32)
	store := &publicationMemoryStore{state: phonecore.State{
		Machine: "m1", MachineRelayAuthPub: pub, EpochID: 7,
		Keys: phonecoreTestEpochKeys(),
	}}
	a, sc := publicationAppFromStore(t, store)
	p := preparedComposer(t, a, sc, "op-commit-unknown", "once")
	if err := a.core.PreparePublication(p); err != nil {
		t.Fatalf("PreparePublication: %v", err)
	}
	first := &scriptedAppender{onAppend: func() {
		store.mu.Lock()
		store.failNext = true
		store.mu.Unlock()
	}}
	sc.cl = first
	if err := flushForTest(a, sc); !errors.Is(err, errPublicationStore) {
		t.Fatalf("admission commit failure = %v, want %v", err, errPublicationStore)
	}
	before := a.core.PendingPublications()
	if len(before) != 1 || before[0].Phase != phonecore.PublicationSealed {
		t.Fatalf("failed admission commit claimed success: %+v", before)
	}

	restarted, restartedSC := publicationAppFromStore(t, store)
	second := &scriptedAppender{}
	restartedSC.cl = second
	if err := flushForTest(restarted, restartedSC); err != nil {
		t.Fatalf("restart flush: %v", err)
	}
	firstCalls, secondCalls := first.envelopes(), second.envelopes()
	if len(firstCalls) != 1 || len(secondCalls) != 1 || !bytes.Equal(firstCalls[0], secondCalls[0]) {
		t.Fatalf("commit-unknown retry was not exact: first=%d second=%d equal=%v",
			len(firstCalls), len(secondCalls), len(firstCalls) == 1 && len(secondCalls) == 1 && bytes.Equal(firstCalls[0], secondCalls[0]))
	}
}

func TestPublicationPump_HeadFailureBlocksOvertakeButAdmissionDoesNotWaitForOutcome(t *testing.T) {
	a, sc := publicationApp(t, t.TempDir())
	for _, p := range []phonecore.PendingPublication{
		preparedComposer(t, a, sc, "op-A", "A"),
		preparedComposer(t, a, sc, "op-B", "B"),
	} {
		if err := a.core.PreparePublication(p); err != nil {
			t.Fatalf("prepare %s: %v", p.OperationID, err)
		}
	}
	appender := &scriptedAppender{failures: 1}
	sc.cl = appender
	if err := flushForTest(a, sc); err == nil {
		t.Fatal("head failure returned nil")
	}
	if calls := appender.envelopes(); len(calls) != 1 {
		t.Fatalf("B overtook failed A: %d append calls", len(calls))
	}
	state := a.core.PendingPublications()
	if state[0].Phase != phonecore.PublicationSealed || state[1].Phase != phonecore.PublicationPrepared {
		t.Fatalf("failed-head phases = %s, %s", state[0].Phase, state[1].Phase)
	}

	if err := flushForTest(a, sc); err != nil {
		t.Fatalf("recovery flush: %v", err)
	}
	calls := appender.envelopes()
	if len(calls) != 3 || !bytes.Equal(calls[0], calls[1]) {
		t.Fatalf("append order/retry = %d calls; exact A retry = %v", len(calls), len(calls) >= 2 && bytes.Equal(calls[0], calls[1]))
	}
	state = a.core.PendingPublications()
	if state[0].Phase != phonecore.PublicationAdmitted || state[1].Phase != phonecore.PublicationAdmitted {
		t.Fatalf("recovered phases = %s, %s; A awaiting outcome must not block B", state[0].Phase, state[1].Phase)
	}
	if state[0].Sequence >= state[1].Sequence {
		t.Fatalf("FIFO sequences = A:%d B:%d", state[0].Sequence, state[1].Sequence)
	}
}

func TestPublicationPump_WrongDerivedTargetNeverLeavesThePhone(t *testing.T) {
	a, sc := publicationApp(t, t.TempDir())
	p := preparedComposer(t, a, sc, "op-wrong-target", "do not leak")
	p.Target = "attacker-mailbox"
	if err := a.core.PreparePublication(p); err != nil {
		t.Fatalf("prepare authority-bound record: %v", err)
	}
	appender := &scriptedAppender{}
	sc.cl = appender
	if err := flushForTest(a, sc); !errors.Is(err, errPublicationIdentityChanged) {
		t.Fatalf("wrong target flush = %v, want identity refusal", err)
	}
	if calls := appender.envelopes(); len(calls) != 0 {
		t.Fatalf("wrong-target publication left the phone: %d appends", len(calls))
	}
}

func testPublicationAuthorityMutationBeforeAppend(
	t *testing.T,
	mutate func(*App) error,
	assertMutated func(*testing.T, phonecore.State),
) {
	t.Helper()
	a, sc := publicationApp(t, t.TempDir())
	if err := a.core.PreparePublication(preparedComposer(t, a, sc, "op-authority-race", "never leak")); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	parked := make(chan struct{})
	release := make(chan struct{})
	appender := &scriptedAppender{before: func() {
		close(parked)
		<-release
	}}
	sc.cl = appender
	flushed := make(chan error, 1)
	go func() { flushed <- flushForTest(a, sc) }()
	select {
	case <-parked:
	case <-time.After(time.Second):
		t.Fatal("publisher did not reach the pre-append authority boundary")
	}

	// The authority mutation must be able to complete while publication is parked before its
	// final fence. Once it returns, releasing the publisher may not enter MailboxAppend.
	if err := mutate(a); err != nil {
		t.Fatalf("authority mutation: %v", err)
	}
	assertMutated(t, a.core.State())
	close(release)
	select {
	case err := <-flushed:
		if !errors.Is(err, errPublicationIdentityChanged) {
			t.Fatalf("flush after authority mutation = %v, want identity refusal", err)
		}
	case <-time.After(time.Second):
		t.Fatal("publisher did not leave the authority fence")
	}
	if calls := appender.envelopes(); len(calls) != 0 {
		t.Fatalf("superseded authority appended %d old envelopes", len(calls))
	}
}

func TestPublicationPump_PairingReplacementFencesAParkedOldEnvelope(t *testing.T) {
	testPublicationAuthorityMutationBeforeAppend(t,
		func(a *App) error { return a.pin(pairedOutcome("m1", 8)) },
		func(t *testing.T, st phonecore.State) {
			if st.EpochID != 8 {
				t.Fatalf("pairing replacement did not land: epoch = %d", st.EpochID)
			}
		})
}

func TestPublicationPump_PurgeFencesAParkedOldEnvelope(t *testing.T) {
	testPublicationAuthorityMutationBeforeAppend(t,
		func(a *App) error { return a.PurgeKeys() },
		func(t *testing.T, st phonecore.State) {
			if !st.Disowned || len(st.PendingPublications) != 0 {
				t.Fatalf("purge did not land: disowned=%v pending=%d", st.Disowned, len(st.PendingPublications))
			}
		})
}

func TestPublicationPump_TerminalUnpairFencesAParkedOldEnvelope(t *testing.T) {
	testPublicationAuthorityMutationBeforeAppend(t,
		func(a *App) error { a.recordUnpaired(); return nil },
		func(t *testing.T, st phonecore.State) {
			if !st.Disowned {
				t.Fatal("terminal unpair did not land")
			}
		})
}

func TestPublicationPump_PersistentFailureBacksOffAndCancellationIsPrompt(t *testing.T) {
	a, sc := publicationApp(t, t.TempDir())
	if err := a.core.PreparePublication(preparedComposer(t, a, sc, "op-backoff", "later")); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	appender := &scriptedAppender{failures: 100, called: make(chan struct{}, 1)}
	sc.cl = appender
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		a.runPublicationPump(ctx, func() (sendCtx, error) { return sc, nil })
		close(done)
	}()
	select {
	case <-appender.called:
	case <-time.After(time.Second):
		t.Fatal("publisher never attempted the durable head")
	}
	// The first retry is deliberately farther away than this window. A tight loop would
	// produce many calls here and compete with inbound mailbox quota.
	time.Sleep(publicationRetryInitial / 4)
	if calls := appender.envelopes(); len(calls) != 1 {
		t.Fatalf("persistent failure busy-looped: %d appends before first backoff elapsed", len(calls))
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("publisher did not stop promptly when its connection was cancelled")
	}
}

func TestPublicationPump_RedriveAdmitsWithoutAnotherUserPress(t *testing.T) {
	a, sc := publicationApp(t, t.TempDir())
	if err := a.core.PreparePublication(preparedComposer(t, a, sc, "op-redrive", "retry me")); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	appender := &scriptedAppender{failures: 1, called: make(chan struct{}, 4)}
	sc.cl = appender
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		a.runPublicationPump(ctx, func() (sendCtx, error) { return sc, nil })
		close(done)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		pending := a.core.PendingPublications()
		if len(pending) == 1 && pending[0].Phase == phonecore.PublicationAdmitted {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	pending := a.core.PendingPublications()
	if len(pending) != 1 || pending[0].Phase != phonecore.PublicationAdmitted {
		t.Fatalf("background redrive never admitted publication: %+v", pending)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("publisher did not stop after successful redrive")
	}
	calls := appender.envelopes()
	if len(calls) != 2 || !bytes.Equal(calls[0], calls[1]) {
		t.Fatalf("redrive calls = %d, exact retry = %v", len(calls), len(calls) == 2 && bytes.Equal(calls[0], calls[1]))
	}
}
