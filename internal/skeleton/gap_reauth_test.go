package skeleton

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/journal"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/shim"
)

func awaitSessionCapability(t *testing.T, d *Daemon, local string, wantStructured bool) protocol.SessionCapabilities {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got, ok := d.sessionCapabilities(local); ok && got.StructuredChat == wantStructured {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	got, ok := d.sessionCapabilities(local)
	t.Fatalf("session capability never reached structured_chat=%t: got %+v ok=%t", wantStructured, got, ok)
	return protocol.SessionCapabilities{}
}

func TestGapReauth_RegisteredBackendProofRestoresCurrentInstanceWithoutTerminalRoute(t *testing.T) {
	r := newR7ComposerRig(t, true)
	healthy := awaitSessionCapability(t, r.sk, r.local, true)
	instance := healthy.SessionInstance
	if instance == "" {
		t.Fatal("healthy backend capability has no session instance")
	}

	r.sk.markSessionDegraded(r.local)
	degraded := awaitSessionCapability(t, r.sk, r.local, false)
	if degraded.TerminalFallback || degraded.TerminalControl {
		t.Fatalf("a history/sink degrade routed into terminal authority: %+v", degraded)
	}

	r.sk.forgetBackend(r.local)
	if ok := r.sk.registerBackendForInstance(r.local, instance,
		"01a00339-a80e-72a0-966f-116427b6b9ce", r.backend); !ok {
		t.Fatal("fresh registered backend proof for the current instance was refused")
	}
	restored := awaitSessionCapability(t, r.sk, r.local, true)
	if restored.TerminalFallback || restored.TerminalControl {
		t.Fatalf("restored record = %+v; want exactly chat authority and no terminal route", restored)
	}
	if !r.sk.sessionDegraded(r.local) {
		t.Fatal("restoring the live sink erased the durable history-torn marker")
	}

	res, err := r.sk.Core().JournalReadFrom(0)
	if err != nil {
		t.Fatalf("JournalReadFrom: %v", err)
	}
	var recovered bool
	for _, rec := range res.Events {
		if rec.SessionID != r.local || rec.Type != journal.TypeCapabilityTransition {
			continue
		}
		var got protocol.SessionCapabilities
		if json.Unmarshal(rec.Payload, &got) == nil && got.StructuredChat &&
			!got.TerminalFallback && !got.TerminalControl && got.SessionInstance == instance {
			recovered = true
		}
	}
	if !recovered {
		t.Fatal("durable recovery published no ordered capability_transition for the phone")
	}
}

func TestGapReauth_RecoveredComposerReachesExactBackendWhileMarkerRemains(t *testing.T) {
	r := newR7ComposerRig(t, true)
	instance := awaitSessionCapability(t, r.sk, r.local, true).SessionInstance
	r.sk.markSessionDegraded(r.local)
	awaitSessionCapability(t, r.sk, r.local, false)
	r.sk.forgetBackend(r.local)

	fresh := newR7FakeBackend()
	fresh.reply["turn/start"] = json.RawMessage(
		`{"turn":{"id":"01a0033b-d0be-77e1-88e7-584ddeea562d","items":[],"itemsView":"notLoaded","status":"inProgress"}}`)
	r.backend = fresh
	if ok := r.sk.registerBackendForInstance(r.local, instance,
		"01a00339-a80e-72a0-966f-116427b6b9ce", fresh); !ok {
		t.Fatal("fresh backend proof was refused")
	}
	awaitSessionCapability(t, r.sk, r.local, true)
	code, err := r.send(t, "", "continue after the visible gap", "devA:01JGAPREAUTH000000000000")
	if err != nil || code != "" {
		t.Fatalf("recovered composer refused: code=%q err=%v", code, err)
	}
	if got := methodsOf(fresh); len(got) != 1 || got[0] != "turn/start" {
		t.Fatalf("recovered composer dispatched %v, want one turn/start", got)
	}
	if !r.sk.sessionDegraded(r.local) {
		t.Fatal("successful recovered send erased the visible durable history marker")
	}
}

func TestGapReauth_RecoveryAppendFailureStaysDisabledThenRetriesExactBackend(t *testing.T) {
	r := newR7ComposerRig(t, true)
	instance := awaitSessionCapability(t, r.sk, r.local, true).SessionInstance
	r.sk.markSessionDegraded(r.local)
	awaitSessionCapability(t, r.sk, r.local, false)
	r.sk.forgetBackend(r.local)

	var attempts atomic.Int32
	allowRetry := make(chan struct{})
	r.sk.capStore.publish = func(sessionID string, payload []byte) error {
		if attempts.Add(1) == 1 {
			return errors.New("injected append failure")
		}
		<-allowRetry
		return r.sk.core.EmitCapabilityTransition(sessionID, payload)
	}
	fresh := newR7FakeBackend()
	if ok := r.sk.registerBackendForInstance(r.local, instance,
		"01a00339-a80e-72a0-966f-116427b6b9ce", fresh); !ok {
		t.Fatal("valid initialized backend was discarded after transient transition failure")
	}
	if got := awaitSessionCapability(t, r.sk, r.local, false); got.StructuredChat {
		t.Fatalf("local composer enabled before its ordered phone transition: %+v", got)
	}
	if _, ok := r.sk.sessionBackendFor(r.local); !ok {
		t.Fatal("transient transition failure discarded the exact sink needed for retry")
	}
	close(allowRetry)
	got := awaitSessionCapability(t, r.sk, r.local, true)
	if got.TerminalFallback || got.TerminalControl {
		t.Fatalf("retry recovery granted terminal authority: %+v", got)
	}
}

func TestGapReauth_OldBackendProofCannotCrossSessionReplacement(t *testing.T) {
	r := newR7ComposerRig(t, true)
	old := awaitSessionCapability(t, r.sk, r.local, true).SessionInstance
	r.sk.markSessionDegraded(r.local)
	r.sk.forgetBackend(r.local)

	fresh := mintSessionInstance()
	if err := r.sk.recordSessionInstance(r.local, fresh, 999999); err != nil {
		t.Fatalf("replace session instance: %v", err)
	}
	if ok := r.sk.registerBackendForInstance(r.local, old,
		"01a00339-a80e-72a0-966f-116427b6b9ce", r.backend); ok {
		t.Fatal("an old instance's backend proof registered across replacement")
	}
	got, ok := r.sk.sessionCapabilities(r.local)
	if ok && got.StructuredChat {
		t.Fatalf("old proof reauthorized replacement %q: %+v", fresh, got)
	}
}

func TestGapReauth_SinkDoesNotUpgradeAnIntrinsicallyFallbackSession(t *testing.T) {
	sk := assemble(t)
	m := launchFake(t, sk, "idle 60s\n")
	base := awaitSessionCapability(t, sk, m.ID, false)
	if !base.TerminalFallback {
		t.Fatalf("fake adapter fixture is not intrinsically fallback: %+v", base)
	}
	sk.markSessionDegraded(m.ID)
	b := newR7FakeBackend()
	if ok := sk.registerBackendForInstance(m.ID, base.SessionInstance,
		"01a00339-a80e-72a0-966f-116427b6b9ce", b); ok {
		t.Fatal("a backend-shaped object proved structured chat for an adapter with no structured seam")
	}
	if got := awaitSessionCapability(t, sk, m.ID, false); got.StructuredChat {
		t.Fatalf("intrinsically fallback session was laundered into chat: %+v", got)
	}
	if _, ok := sk.sessionBackendFor(m.ID); ok {
		t.Fatal("refused intrinsic-fallback proof left a registered message sink")
	}
	if ok := sk.registerBackendForInstance(m.ID, "", "thread", b); ok {
		t.Fatal("an empty instance was accepted as a backend proof")
	}
	if _, ok := sk.sessionBackendFor(m.ID); ok {
		t.Fatal("refused empty-instance proof left a registered message sink")
	}
	if ok := sk.registerBackendForInstance(m.ID, "stale-instance", "thread", b); ok {
		t.Fatal("a stale instance was accepted as a backend proof")
	}
	if _, ok := sk.sessionBackendFor(m.ID); ok {
		t.Fatal("refused stale-instance proof left a registered message sink")
	}
}

func TestGapReauth_SameReasonBackendLossCreatesANewBoundary(t *testing.T) {
	r := newR7ComposerRig(t, true)
	instance := awaitSessionCapability(t, r.sk, r.local, true).SessionInstance

	r.sk.noteBackendLost(r.local, "connection closed")
	awaitSessionCapability(t, r.sk, r.local, false)
	if ok := r.sk.registerBackendForInstance(r.local, instance,
		"01a00339-a80e-72a0-966f-116427b6b9ce", newR7FakeBackend()); !ok {
		t.Fatal("first loss could not recover through a fresh current backend")
	}
	awaitSessionCapability(t, r.sk, r.local, true)

	// The human-readable reason is deliberately identical. It is not the boundary
	// identity: this is a second, genuine sink loss and must invalidate the proof.
	r.sk.noteBackendLost(r.local, "connection closed")
	got := awaitSessionCapability(t, r.sk, r.local, false)
	if got.TerminalFallback || got.TerminalControl {
		t.Fatalf("second same-reason loss granted a terminal route: %+v", got)
	}
}

func TestGapReauth_WireTransitionUsesItsOwnPayloadForReadAndSubscribe(t *testing.T) {
	sk := assemble(t)
	const session = "cap-transition-wire"
	degraded := protocol.SessionCapabilities{
		Provider: "claude", ProviderVersion: "1", AdapterRevision: "r1",
		SessionInstance: "instance-a", Interrupt: true,
	}
	recovered := degraded
	recovered.StructuredChat = true

	emit := func(c protocol.SessionCapabilities) {
		t.Helper()
		payload, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		if err := sk.Core().EmitCapabilityTransition(session, payload); err != nil {
			t.Fatalf("emit capability transition: %v", err)
		}
	}
	emit(degraded)
	emit(recovered)

	res, err := sk.api.JournalReadFrom(0)
	if err != nil {
		t.Fatalf("wire journal read: %v", err)
	}
	var read []*protocol.SessionCapabilities
	for _, rec := range res.Events {
		if rec.SessionID == session && rec.Type == string(journal.TypeCapabilityTransition) {
			read = append(read, rec.Capabilities)
		}
	}
	if len(read) != 2 || read[0] == nil || read[1] == nil {
		t.Fatalf("wire read transitions = %#v, want two payload-authored records", read)
	}
	if read[0].StructuredChat || !read[1].StructuredChat ||
		read[0].SessionInstance != degraded.SessionInstance ||
		read[1].SessionInstance != recovered.SessionInstance ||
		read[0].TerminalFallback || read[0].TerminalControl ||
		read[1].TerminalFallback || read[1].TerminalControl {
		t.Fatalf("wire read rewrote ordered transition payloads: first=%+v second=%+v", *read[0], *read[1])
	}

	live, cancel := sk.api.JournalSubscribe()
	defer cancel()
	emit(degraded)
	select {
	case rec := <-live:
		if rec.Type != string(journal.TypeCapabilityTransition) || rec.Capabilities == nil ||
			rec.Capabilities.StructuredChat || rec.Capabilities.SessionInstance != degraded.SessionInstance ||
			rec.Capabilities.TerminalFallback || rec.Capabilities.TerminalControl {
			t.Fatalf("wire subscribe transition = %+v; want exact degraded payload", rec)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("wire subscribe did not deliver capability_transition")
	}

	bad := journal.Record{Type: journal.TypeCapabilityTransition, Payload: []byte(`{"structured_chat":true}`)}
	if got := toWireJournalRecordWith(bad, func(string) (protocol.SessionCapabilities, bool) {
		return recovered, true
	}); got.Capabilities != nil {
		t.Fatalf("malformed transition borrowed current roster authority: %+v", *got.Capabilities)
	}
}

func TestGapReauth_PersistentGapNeverProvesButFirstCleanAfterFlagLossDoesOnce(t *testing.T) {
	sk := assemble(t)
	sk.setAdapterForTest(func(string) (adapter.Adapter, bool) {
		return &r7KeystrokeCaptureAdapter{captureAdapter: newCaptureAdapter()}, true
	})
	m := launchFake(t, sk, "idle 60s\n")
	awaitSessionCapability(t, sk, m.ID, true)
	// This test drives a synthetic drainer; stop the assembly's real loop without a
	// final drain so it cannot independently observe the healthy live spool.
	sk.stopHookDrains()

	cursorPath := filepath.Join(t.TempDir(), "hook.fold")
	hd := NewHookDrainer(sk, m.ID, "", cursorPath)
	gap := shim.HookDrainResponse{Gap: true, GapBoundary: 9, SpoolIncarnation: "spool-a"}
	if _, _, err := hd.applyLocked(gap, 4); !errors.Is(err, ErrHookDrainGap) {
		t.Fatalf("first gap observation = %v, want ErrHookDrainGap", err)
	}
	awaitSessionCapability(t, sk, m.ID, false)
	// The same tail after reset is still unreadable. This clears the process-local
	// recovery flag and models the state a restarted daemon inherits.
	if _, _, err := hd.applyLocked(gap, 0); !errors.Is(err, ErrHookDrainGap) {
		t.Fatalf("persistent reset gap = %v, want ErrHookDrainGap", err)
	}
	if got := awaitSessionCapability(t, sk, m.ID, false); got.StructuredChat {
		t.Fatalf("persistent gap reauthorized chat: %+v", got)
	}

	transitionCount := func() int {
		t.Helper()
		res, err := sk.Core().JournalReadFrom(0)
		if err != nil {
			t.Fatalf("read transitions: %v", err)
		}
		n := 0
		for _, rec := range res.Events {
			if rec.SessionID == m.ID && rec.Type == journal.TypeCapabilityTransition {
				n++
			}
		}
		return n
	}
	before := transitionCount()
	if _, _, err := hd.applyLocked(shim.HookDrainResponse{}, 0); err != nil {
		t.Fatalf("first clean drain: %v", err)
	}
	awaitSessionCapability(t, sk, m.ID, true)
	afterRecovery := transitionCount()
	if afterRecovery != before+1 {
		t.Fatalf("clean recovery transitions = %d -> %d, want one", before, afterRecovery)
	}
	if _, _, err := hd.applyLocked(shim.HookDrainResponse{}, 0); err != nil {
		t.Fatalf("repeated clean drain: %v", err)
	}
	if got := transitionCount(); got != afterRecovery {
		t.Fatalf("already-proven clean drain republished recovery: %d -> %d", afterRecovery, got)
	}
}

func TestGapReauth_CapTrueWithoutProofCommitReadsFailClosedWithoutTerminalRoute(t *testing.T) {
	dir := t.TempDir()
	const session, instance = "crash-prefix", "instance-a"
	sdir := filepath.Join(dir, session)
	if err := os.MkdirAll(sdir, 0o700); err != nil {
		t.Fatal(err)
	}
	rec := protocol.SessionCapabilities{
		Provider: "claude", ProviderVersion: "1", AdapterRevision: "r",
		SessionInstance: instance, StructuredChat: true,
	}
	body, _ := json.Marshal(rec)
	if err := os.WriteFile(filepath.Join(sdir, sessionCapabilityFile), body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sdir, sessionDegradedFile), []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sdir, sessionInstanceFile), []byte(instance+" 1"), 0o600); err != nil {
		t.Fatal(err)
	}

	d := &Daemon{}
	d.capStore.dir = dir
	got, ok := d.sessionCapabilities(session)
	if !ok {
		t.Fatal("valid crash-prefix record disappeared instead of reading fail closed")
	}
	if got.StructuredChat || got.TerminalFallback || got.TerminalControl {
		t.Fatalf("cap=true without its proof commit read with authority: %+v", got)
	}
}

func TestGapReauth_RestartRequiresFreshProcessProofForEveryDurableSidecarShape(t *testing.T) {
	proofs := map[string][]byte{
		"missing": nil,
		"corrupt": []byte(`{"version":`),
		"wrong instance": mustJSONForGapReauth(t, structuredSinkProof{
			Version: structuredSinkProofVersion, SessionInstance: "instance-b",
			GapGeneration: "gap-a", Kind: sinkProofBackend,
		}),
		"wrong generation": mustJSONForGapReauth(t, structuredSinkProof{
			Version: structuredSinkProofVersion, SessionInstance: "instance-a",
			GapGeneration: "gap-b", Kind: sinkProofBackend,
		}),
		"valid durable but no current process proof": mustJSONForGapReauth(t, structuredSinkProof{
			Version: structuredSinkProofVersion, SessionInstance: "instance-a",
			GapGeneration: "gap-a", Kind: sinkProofBackend,
		}),
	}
	for name, proof := range proofs {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			const session, instance = "restart-proof", "instance-a"
			sdir := filepath.Join(dir, session)
			if err := os.MkdirAll(sdir, 0o700); err != nil {
				t.Fatal(err)
			}
			rec := protocol.SessionCapabilities{
				Provider: "claude", ProviderVersion: "1", AdapterRevision: "r",
				SessionInstance: instance, StructuredChat: true, Interrupt: true,
			}
			if err := os.WriteFile(filepath.Join(sdir, sessionCapabilityFile), mustJSONForGapReauth(t, rec), 0o600); err != nil {
				t.Fatal(err)
			}
			marker := historyTornMarker{Version: 1, SessionInstance: instance, Generation: "gap-a"}
			if err := os.WriteFile(filepath.Join(sdir, sessionDegradedFile), mustJSONForGapReauth(t, marker), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(sdir, sessionInstanceFile), []byte(instance+" 1"), 0o600); err != nil {
				t.Fatal(err)
			}
			if proof != nil {
				if err := os.WriteFile(filepath.Join(sdir, sessionSinkProofFile), proof, 0o600); err != nil {
					t.Fatal(err)
				}
			}

			d := &Daemon{}
			d.capStore.dir = dir
			got, ok := d.sessionCapabilities(session)
			if !ok {
				t.Fatal("valid capability record disappeared")
			}
			if got.StructuredChat || got.TerminalFallback || got.TerminalControl {
				t.Fatalf("restart sidecar shape %q read with authority: %+v", name, got)
			}
		})
	}
}

func mustJSONForGapReauth(t *testing.T, value any) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestGapReauth_ReobservingRecoveredBoundaryInvalidatesProof(t *testing.T) {
	r := newR7ComposerRig(t, true)
	instance := awaitSessionCapability(t, r.sk, r.local, true).SessionInstance
	r.sk.markSessionDegradedFor(r.local, "same-boundary")
	awaitSessionCapability(t, r.sk, r.local, false)
	r.sk.forgetBackend(r.local)
	if ok := r.sk.registerBackendForInstance(r.local, instance, "thread", newR7FakeBackend()); !ok {
		t.Fatal("recover exact boundary")
	}
	awaitSessionCapability(t, r.sk, r.local, true)

	r.sk.markSessionDegradedFor(r.local, "same-boundary")
	got := awaitSessionCapability(t, r.sk, r.local, false)
	if got.TerminalFallback || got.TerminalControl {
		t.Fatalf("re-observed boundary granted a terminal route: %+v", got)
	}
}

func TestGapReauth_NewGapCannotPublishBeforeBlockedOldRecovery(t *testing.T) {
	r := newR7ComposerRig(t, true)
	instance := awaitSessionCapability(t, r.sk, r.local, true).SessionInstance
	r.sk.markSessionDegradedFor(r.local, "gap-one")
	awaitSessionCapability(t, r.sk, r.local, false)
	r.sk.forgetBackend(r.local)

	recoveryEntered := make(chan struct{})
	releaseRecovery := make(chan struct{})
	var blocked atomic.Bool
	r.sk.capStore.publish = func(sessionID string, payload []byte) error {
		var c protocol.SessionCapabilities
		if json.Unmarshal(payload, &c) != nil {
			return errors.New("bad test transition")
		}
		if c.StructuredChat && blocked.CompareAndSwap(false, true) {
			close(recoveryEntered)
			<-releaseRecovery
		}
		return r.sk.core.EmitCapabilityTransition(sessionID, payload)
	}
	registered := make(chan bool, 1)
	go func() {
		registered <- r.sk.registerBackendForInstance(r.local, instance, "thread", newR7FakeBackend())
	}()
	select {
	case <-recoveryEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("recovery never reached its ordered publication")
	}
	gapDone := make(chan struct{})
	go func() {
		r.sk.markSessionDegradedFor(r.local, "gap-two")
		close(gapDone)
	}()
	close(releaseRecovery)
	if !<-registered {
		t.Fatal("current backend recovery was refused")
	}
	select {
	case <-gapDone:
	case <-time.After(5 * time.Second):
		t.Fatal("new gap stayed blocked after recovery publication")
	}
	got := awaitSessionCapability(t, r.sk, r.local, false)
	if got.TerminalFallback || got.TerminalControl {
		t.Fatalf("final state after proof-vs-gap race granted terminal authority: %+v", got)
	}

	res, err := r.sk.Core().JournalReadFrom(0)
	if err != nil {
		t.Fatal(err)
	}
	var states []bool
	for _, rec := range res.Events {
		if rec.SessionID != r.local || rec.Type != journal.TypeCapabilityTransition {
			continue
		}
		var c protocol.SessionCapabilities
		if json.Unmarshal(rec.Payload, &c) == nil {
			states = append(states, c.StructuredChat)
		}
	}
	if len(states) < 2 || !states[len(states)-2] || states[len(states)-1] {
		t.Fatalf("proof-vs-gap transition order = %v, want trailing true,false", states)
	}
}

func TestGapReauth_ConcurrentExactProofsPublishOneRecovery(t *testing.T) {
	r := newR7ComposerRig(t, true)
	instance := awaitSessionCapability(t, r.sk, r.local, true).SessionInstance
	r.sk.markSessionDegradedFor(r.local, "concurrent-gap")
	awaitSessionCapability(t, r.sk, r.local, false)

	firstPublish := make(chan struct{})
	releaseFirst := make(chan struct{})
	var blocked atomic.Bool
	r.sk.capStore.publish = func(sessionID string, payload []byte) error {
		var c protocol.SessionCapabilities
		if json.Unmarshal(payload, &c) != nil {
			return errors.New("bad test transition")
		}
		if c.StructuredChat && blocked.CompareAndSwap(false, true) {
			close(firstPublish)
			<-releaseFirst
		}
		return r.sk.core.EmitCapabilityTransition(sessionID, payload)
	}
	results := make(chan error, 2)
	go func() { results <- r.sk.commitStructuredSinkProof(r.local, instance, sinkProofBackend) }()
	select {
	case <-firstPublish:
	case <-time.After(5 * time.Second):
		t.Fatal("first exact proof never reached publication")
	}
	secondStarted := make(chan struct{})
	go func() {
		close(secondStarted)
		results <- r.sk.commitStructuredSinkProof(r.local, instance, sinkProofBackend)
	}()
	<-secondStarted
	close(releaseFirst)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("concurrent exact proof: %v", err)
		}
	}
	awaitSessionCapability(t, r.sk, r.local, true)

	res, err := r.sk.Core().JournalReadFrom(0)
	if err != nil {
		t.Fatal(err)
	}
	recoveries := 0
	for _, rec := range res.Events {
		if rec.SessionID != r.local || rec.Type != journal.TypeCapabilityTransition {
			continue
		}
		var c protocol.SessionCapabilities
		if json.Unmarshal(rec.Payload, &c) == nil && c.StructuredChat {
			recoveries++
		}
	}
	if recoveries != 1 {
		t.Fatalf("concurrent exact proofs published %d recovery transitions, want one", recoveries)
	}
}

func TestGapReauth_RepeatedRetrySchedulesHaveOneWorker(t *testing.T) {
	r := newR7ComposerRig(t, true)
	var attempts atomic.Int32
	retryEntered := make(chan struct{}, 4)
	allowRetry := make(chan struct{})
	r.sk.capStore.publish = func(sessionID string, payload []byte) error {
		var c protocol.SessionCapabilities
		if json.Unmarshal(payload, &c) != nil || c.StructuredChat {
			return errors.New("unexpected test transition")
		}
		if attempts.Add(1) == 1 {
			return errors.New("injected append failure")
		}
		retryEntered <- struct{}{}
		<-allowRetry
		return r.sk.core.EmitCapabilityTransition(sessionID, payload)
	}
	r.sk.markSessionDegradedFor(r.local, "one-gap")
	expected := awaitSessionCapability(t, r.sk, r.local, false)
	// Model repeated observations scheduling the same unpublished state without
	// racing another synchronous publication call against the test hook itself.
	r.sk.scheduleCapabilityTransitionRetry(r.local, expected)
	r.sk.scheduleCapabilityTransitionRetry(r.local, expected)
	select {
	case <-retryEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("degrade transition retry never ran")
	}
	select {
	case <-retryEntered:
		t.Fatal("repeated failed publication accumulated a second retry worker")
	case <-time.After(450 * time.Millisecond):
	}
	close(allowRetry)
	deadline := time.Now().Add(5 * time.Second)
	for {
		res, err := r.sk.Core().JournalReadFrom(0)
		if err != nil {
			t.Fatal(err)
		}
		published := 0
		for _, rec := range res.Events {
			if rec.SessionID != r.local || rec.Type != journal.TypeCapabilityTransition {
				continue
			}
			var c protocol.SessionCapabilities
			if json.Unmarshal(rec.Payload, &c) == nil && !c.StructuredChat {
				published++
			}
		}
		if published == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("degrade retry published %d transitions, want one", published)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestGapReauth_PendingExactDegradeSkipsSynchronousDuplicate(t *testing.T) {
	r := newR7ComposerRig(t, true)
	r.sk.markSessionDegradedFor(r.local, "one-gap")
	expected := awaitSessionCapability(t, r.sk, r.local, false)
	r.sk.capStore.retryMu.Lock()
	if r.sk.capStore.retrying == nil {
		r.sk.capStore.retrying = map[string]protocol.SessionCapabilities{}
	}
	r.sk.capStore.retrying[r.local] = expected
	r.sk.capStore.retryMu.Unlock()
	defer func() {
		r.sk.capStore.retryMu.Lock()
		delete(r.sk.capStore.retrying, r.local)
		r.sk.capStore.retryMu.Unlock()
	}()
	var calls atomic.Int32
	r.sk.capStore.publish = func(string, []byte) error {
		calls.Add(1)
		return errors.New("unexpected duplicate append")
	}
	r.sk.markSessionDegradedFor(r.local, "one-gap")
	if got := calls.Load(); got != 0 {
		t.Fatalf("pending exact degrade synchronously republished %d time(s), want zero", got)
	}
}
