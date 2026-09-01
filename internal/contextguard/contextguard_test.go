package contextguard

import (
	"math"
	"math/big"
	"testing"
	"time"
)

var (
	testNow = time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	testKey = Key{SessionID: "session", SessionInstance: "instance-1", BackendEpoch: "epoch-1", ProviderThreadID: "thread-1"}
)

func newTestMachine(t *testing.T, enabled bool, threshold int) Machine {
	t.Helper()
	m, err := New(Config{Enabled: enabled, Threshold: threshold, Revision: 1}, testKey)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func observe(m Machine, used, limit, seq uint64, at time.Time) Event {
	return Event{Kind: EventObservation, At: at, Observation: Observation{
		Key: testKey, SettingsRevision: m.Config.Revision, IngestSequence: seq,
		ObservedAt: at, UsedTokens: used, ContextLimit: limit, Quality: QualityExact,
	}}
}

func edge(m Machine, kind EventKind, at time.Time) Event {
	return Event{Kind: kind, At: at, Key: m.Key, SourceSequence: m.LastSourceSequence + 1}
}

func reduce(t *testing.T, m Machine, e Event) (Machine, Decision) {
	t.Helper()
	next, d := Reduce(m, e)
	if d.Rejected != RejectNone {
		t.Fatalf("Reduce(%s): rejected %s", e.Kind, d.Rejected)
	}
	return next, d
}

func TestConfigValidationBoundariesAndDefault(t *testing.T) {
	for _, tc := range []struct {
		threshold int
		wantErr   bool
	}{
		{39, true}, {40, false}, {95, false}, {96, true},
	} {
		_, err := New(Config{Enabled: true, Threshold: tc.threshold, Revision: 1}, testKey)
		if (err != nil) != tc.wantErr {
			t.Errorf("threshold %d: err=%v, wantErr=%v", tc.threshold, err, tc.wantErr)
		}
	}
	if got := defaultConfig().Threshold; got != 80 {
		t.Fatalf("default threshold=%d, want 80", got)
	}
}

func TestTriggerIsInclusiveAndUsesRawIntegerComparison(t *testing.T) {
	m := newTestMachine(t, true, 80)
	m, _ = reduce(t, m, observe(m, 79, 100, 1, testNow))
	if m.State != StateArmed {
		t.Fatalf("79%% state=%s, want armed", m.State)
	}
	m, _ = reduce(t, m, observe(m, 80, 100, 2, testNow))
	if m.State != StatePendingIdle || m.TriggerThreshold != 80 {
		t.Fatalf("80%% state=%s threshold=%d, want pending_idle/80", m.State, m.TriggerThreshold)
	}

	m = newTestMachine(t, true, 95)
	m, _ = reduce(t, m, observe(m, 949, 1000, 1, testNow))
	if m.State != StateArmed {
		t.Fatalf("94.9%% state=%s, want armed", m.State)
	}
	m, _ = reduce(t, m, observe(m, 950, 1000, 2, testNow))
	if m.State != StatePendingIdle {
		t.Fatalf("95%% state=%s, want pending_idle", m.State)
	}
}

func TestOverflowSafeTriggerAndRearm(t *testing.T) {
	m := newTestMachine(t, true, 80)
	limit := uint64(math.MaxUint64)
	m, _ = reduce(t, m, observe(m, limit-1, limit, 1, testNow))
	if m.State != StatePendingIdle {
		t.Fatalf("near-max trigger state=%s, want pending_idle", m.State)
	}
	m, _ = reduce(t, m, edge(m, EventSessionIdle, testNow.Add(time.Nanosecond)))
	m, _ = reduce(t, m, edge(m, EventDispatchStarted, testNow.Add(2*time.Nanosecond)))
	m, _ = reduce(t, m, edge(m, EventActionWritten, testNow.Add(3*time.Nanosecond)))
	m, _ = reduce(t, m, edge(m, EventProviderCompactionStarted, testNow.Add(4*time.Nanosecond)))
	m, _ = reduce(t, m, edge(m, EventProviderCompactionCompleted, testNow.Add(5*time.Nanosecond)))
	// 70% is the inclusive re-arm point for the captured 80% trigger.
	m, _ = reduce(t, m, observe(m, percentageOf(limit, 70), limit, 4, testNow.Add(6*time.Nanosecond)))
	if m.State != StateArmed {
		t.Fatalf("70%% state=%s, want armed", m.State)
	}
}

func TestHysteresisPreventsHighAfterCompactLoop(t *testing.T) {
	m := compactedMachine(t, 80)
	m, _ = reduce(t, m, observe(m, 80, 100, 4, testNow.Add(time.Second)))
	if m.State != StateLatched {
		t.Fatalf("high after compact state=%s, want latched", m.State)
	}
	m, _ = reduce(t, m, observe(m, 71, 100, 5, testNow.Add(2*time.Second)))
	if m.State != StateLatched {
		t.Fatalf("above re-arm point state=%s, want latched", m.State)
	}
	m, _ = reduce(t, m, observe(m, 70, 100, 6, testNow.Add(3*time.Second)))
	if m.State != StateArmed {
		t.Fatalf("re-arm state=%s, want armed", m.State)
	}
}

func compactedMachine(t *testing.T, threshold int) Machine {
	t.Helper()
	base := testNow.Add(-10 * time.Nanosecond)
	m := newTestMachine(t, true, threshold)
	m, _ = reduce(t, m, observe(m, uint64(threshold), 100, 1, base))
	m, _ = reduce(t, m, edge(m, EventSessionIdle, base.Add(time.Nanosecond)))
	m, _ = reduce(t, m, edge(m, EventDispatchStarted, base.Add(2*time.Nanosecond)))
	m, _ = reduce(t, m, edge(m, EventActionWritten, base.Add(3*time.Nanosecond)))
	m, _ = reduce(t, m, edge(m, EventProviderCompactionStarted, base.Add(4*time.Nanosecond)))
	m, _ = reduce(t, m, edge(m, EventProviderCompactionCompleted, base.Add(5*time.Nanosecond)))
	return m
}

func TestSettingsChangeInvalidatesTelemetryAndDisableHoldsPostWrite(t *testing.T) {
	m := newTestMachine(t, true, 80)
	m, _ = reduce(t, m, observe(m, 80, 100, 1, testNow))
	m, _ = reduce(t, m, Event{Kind: EventConfigChanged, At: testNow, Config: Config{Enabled: true, Threshold: 90, Revision: 2}})
	if m.LastObservation != nil || m.LastSourceSequence != 1 || m.State != StateArmed {
		t.Fatalf("config change did not cancel pending while invalidating telemetry: %+v", m)
	}
	// The observation bearing the replaced revision must not be accepted.
	old := observe(m, 90, 100, 2, testNow)
	old.Observation.SettingsRevision = 1
	_, d := Reduce(m, old)
	if d.Rejected != RejectSettingsRevision {
		t.Fatalf("old revision rejection=%s, want settings_revision", d.Rejected)
	}
	if _, d := Reduce(m, edge(m, EventSessionIdle, testNow.Add(time.Nanosecond))); d.Rejected != RejectInvalidTransition {
		t.Fatalf("idle without post-revision telemetry rejection=%s, want invalid transition", d.Rejected)
	}
	m, _ = reduce(t, m, observe(m, 90, 100, 2, testNow.Add(time.Nanosecond)))
	m, _ = reduce(t, m, edge(m, EventSessionIdle, testNow.Add(2*time.Nanosecond)))
	m, _ = reduce(t, m, edge(m, EventDispatchStarted, testNow.Add(3*time.Nanosecond)))
	m, _ = reduce(t, m, Event{Kind: EventConfigChanged, At: testNow.Add(4 * time.Nanosecond), Config: Config{Enabled: false, Threshold: 90, Revision: 3}})
	if m.State != StateExecuting {
		t.Fatalf("disable executing state=%s, want executing hold", m.State)
	}
	m, _ = reduce(t, m, edge(m, EventActionWritten, testNow.Add(5*time.Nanosecond)))
	if m.State != StateAwaitingConfirmation {
		t.Fatalf("written after disable state=%s, want awaiting_confirmation", m.State)
	}
}

func TestDisableCancelsEveryPreWritePhaseAndToggleCannotClearLatch(t *testing.T) {
	for _, phase := range []State{StateArmed, StatePendingIdle, StatePrepared} {
		m := newTestMachine(t, true, 80)
		if phase != StateArmed {
			m, _ = reduce(t, m, observe(m, 80, 100, 1, testNow))
		}
		if phase == StatePrepared {
			m, _ = reduce(t, m, edge(m, EventSessionIdle, testNow.Add(time.Nanosecond)))
		}
		m, _ = reduce(t, m, Event{Kind: EventConfigChanged, At: testNow, Config: Config{Enabled: false, Threshold: 80, Revision: 2}})
		if m.State != StateDisabled {
			t.Errorf("disable %s state=%s, want disabled", phase, m.State)
		}
	}
	m := compactedMachine(t, 80)
	m, _ = reduce(t, m, Event{Kind: EventConfigChanged, At: testNow, Config: Config{Enabled: false, Threshold: 85, Revision: 2}})
	m, _ = reduce(t, m, Event{Kind: EventConfigChanged, At: testNow.Add(time.Nanosecond), Config: Config{Enabled: true, Threshold: 85, Revision: 3}})
	if m.State != StateLatched {
		t.Fatalf("toggle cleared latch: %s", m.State)
	}
}

func TestObservationGuardsIdentitySequenceQualityAndFreshness(t *testing.T) {
	m := newTestMachine(t, true, 80)
	cases := []struct {
		name string
		mut  func(*Event)
		want RejectReason
	}{
		{"wrong-key", func(e *Event) { e.Observation.Key.ProviderThreadID = "other" }, RejectWrongKey},
		{"zero-limit", func(e *Event) { e.Observation.ContextLimit = 0 }, RejectMalformedLimit},
		{"nonexact", func(e *Event) { e.Observation.Quality = QualityCharacterized }, RejectQuality},
		{"stale", func(e *Event) { e.Observation.ObservedAt = testNow.Add(-MaxObservationDispatchAge - time.Nanosecond) }, RejectStaleObservation},
	}
	for _, tc := range cases {
		e := observe(m, 80, 100, 1, testNow)
		tc.mut(&e)
		if _, d := Reduce(m, e); d.Rejected != tc.want {
			t.Errorf("%s rejected=%s, want %s", tc.name, d.Rejected, tc.want)
		}
	}
	m, _ = reduce(t, m, observe(m, 1, 100, 1, testNow))
	if _, d := Reduce(m, observe(m, 1, 100, 1, testNow)); d.Rejected != RejectSourceSequence {
		t.Fatalf("duplicate rejection=%s, want source_sequence", d.Rejected)
	}
	if _, d := Reduce(m, observe(m, 1, 100, 0, testNow)); d.Rejected != RejectSourceSequence {
		t.Fatalf("reordered rejection=%s, want source_sequence", d.Rejected)
	}
	boundary := observe(m, 1, 100, 2, testNow)
	boundary.Observation.ObservedAt = testNow.Add(-MaxObservationDispatchAge)
	if _, d := Reduce(m, boundary); d.Rejected != RejectNone {
		t.Fatalf("30 second boundary rejection=%s, want accept", d.Rejected)
	}
}

func TestManualLifecycleEventLossRecoveryAndIncarnation(t *testing.T) {
	m := newTestMachine(t, true, 80)
	m, _ = reduce(t, m, observe(m, 80, 100, 1, testNow))
	m, _ = reduce(t, m, edge(m, EventSessionIdle, testNow.Add(time.Nanosecond)))
	m, _ = reduce(t, m, edge(m, EventProviderCompactionStarted, testNow.Add(2*time.Nanosecond)))
	if m.State != StateProviderCompacting {
		t.Fatalf("manual start state=%s, want provider_compacting", m.State)
	}
	m, _ = reduce(t, m, edge(m, EventProviderCompactionCompleted, testNow.Add(3*time.Nanosecond)))
	if m.State != StateLatched {
		t.Fatalf("manual complete state=%s, want latched", m.State)
	}
	m, _ = reduce(t, m, edge(m, EventEventLoss, testNow.Add(4*time.Nanosecond)))
	if m.State != StateEventLossHold {
		t.Fatalf("event loss state=%s, want event_loss_hold", m.State)
	}
	m, _ = reduce(t, m, Event{Kind: EventNewInstance, At: testNow, Key: Key{SessionID: "session", SessionInstance: "instance-2", BackendEpoch: "epoch-2", ProviderThreadID: "thread-2"}})
	if m.State != StateArmed || m.LastObservation != nil {
		t.Fatalf("new incarnation = %+v, want armed with no telemetry", m)
	}

	m = newTestMachine(t, true, 80)
	m, _ = reduce(t, m, observe(m, 80, 100, 1, testNow))
	m, _ = reduce(t, m, edge(m, EventSessionIdle, testNow.Add(time.Nanosecond)))
	m, _ = reduce(t, m, edge(m, EventDispatchStarted, testNow.Add(2*time.Nanosecond)))
	m, _ = reduce(t, m, Event{Kind: EventRecovery, At: testNow})
	if m.State != StateOutcomeUnknownHold {
		t.Fatalf("recovery executing=%s, want outcome_unknown_hold", m.State)
	}

	m = newTestMachine(t, true, 80)
	m, _ = reduce(t, m, observe(m, 80, 100, 1, testNow))
	m, _ = reduce(t, m, edge(m, EventSessionIdle, testNow.Add(time.Nanosecond)))
	m, _ = reduce(t, m, edge(m, EventDispatchStarted, testNow.Add(2*time.Nanosecond)))
	m, _ = reduce(t, m, Event{Kind: EventBackendEpochChanged, At: testNow, Key: Key{SessionID: "session", SessionInstance: "instance-1", BackendEpoch: "epoch-2", ProviderThreadID: "thread-1"}})
	if m.State != StateOutcomeUnknownHold || m.LastObservation != nil || m.LastSourceSequence != 0 {
		t.Fatalf("new epoch did not retain unresolved hold and discard telemetry: %+v", m)
	}
}

func TestDisableAndEnablePreservePostWriteHolds(t *testing.T) {
	for _, phase := range []State{
		StateExecuting, StateAwaitingConfirmation, StateProviderCompacting, StateLatched,
		StateOutcomeUnknownHold, StateEventLossHold, StateBlockedCorrupt,
	} {
		m := newTestMachine(t, true, 80)
		m.State = phase
		m, _ = reduce(t, m, Event{Kind: EventConfigChanged, At: testNow, Config: Config{Enabled: false, Threshold: 80, Revision: 2}})
		if m.State != phase {
			t.Errorf("disable changed post-write phase %s -> %s", phase, m.State)
		}
		m, _ = reduce(t, m, Event{Kind: EventConfigChanged, At: testNow.Add(time.Nanosecond), Config: Config{Enabled: true, Threshold: 81, Revision: 3}})
		if m.State != phase {
			t.Errorf("re-enable changed post-write phase %s -> %s", phase, m.State)
		}
	}

	m := newTestMachine(t, true, 80)
	m, _ = reduce(t, m, edge(m, EventProviderUnsupported, testNow))
	m, _ = reduce(t, m, Event{Kind: EventConfigChanged, At: testNow, Config: Config{Enabled: false, Threshold: 80, Revision: 2}})
	m, _ = reduce(t, m, Event{Kind: EventConfigChanged, At: testNow.Add(time.Nanosecond), Config: Config{Enabled: true, Threshold: 80, Revision: 3}})
	if m.State != StateUnsupported {
		t.Fatalf("settings enabled an unsupported provider: %s", m.State)
	}
}

func TestRecoveryMappingAndNoSideEffectRequireFresh(t *testing.T) {
	for _, phase := range []State{StateArmed, StatePendingIdle, StatePrepared} {
		m := newTestMachine(t, true, 80)
		m.State = phase
		m.LastObservation = &Observation{IngestSequence: 5}
		m.LastSourceSequence = 5
		m, _ = reduce(t, m, Event{Kind: EventRecovery, At: testNow})
		if m.State != StateArmed || m.LastObservation != nil || m.LastSourceSequence != 5 {
			t.Errorf("recovery %s = %+v, want armed/fresh", phase, m)
		}
	}
	for _, phase := range []State{StateLatched} {
		m := newTestMachine(t, true, 80)
		m.State = phase
		m, _ = reduce(t, m, Event{Kind: EventRecovery, At: testNow})
		if m.State != phase {
			t.Errorf("recovery changed %s -> %s", phase, m.State)
		}
	}
	m := newTestMachine(t, true, 80)
	m.State = StateProviderCompacting
	m, _ = reduce(t, m, Event{Kind: EventRecovery, At: testNow})
	if m.State != StateEventLossHold {
		t.Fatalf("recovery provider_compacting=%s, want event_loss_hold", m.State)
	}
	m = newTestMachine(t, true, 80)
	m, _ = reduce(t, m, Event{Kind: EventRecovery, At: testNow, Corrupt: true})
	if m.State != StateBlockedCorrupt {
		t.Fatalf("corrupt recovery = %s, want blocked_corrupt", m.State)
	}

	m = newTestMachine(t, true, 80)
	m, _ = reduce(t, m, observe(m, 80, 100, 1, testNow))
	m, _ = reduce(t, m, edge(m, EventSessionIdle, testNow.Add(time.Nanosecond)))
	m, _ = reduce(t, m, edge(m, EventDispatchStarted, testNow.Add(2*time.Nanosecond)))
	m, _ = reduce(t, m, edge(m, EventDispatchNoSideEffect, testNow.Add(3*time.Nanosecond)))
	if m.State != StateArmed || m.LastObservation != nil {
		t.Fatalf("proven-no-effect did not require fresh observation: %+v", m)
	}
}

func TestStandaloneProviderCompletionIsConclusive(t *testing.T) {
	for _, phase := range []State{StateArmed, StatePendingIdle, StatePrepared, StateAwaitingConfirmation} {
		m := newTestMachine(t, true, 80)
		m.State = phase
		m.TriggerThreshold = 80
		m.LastSourceSequence = 7
		at := testNow.Add(time.Duration(len(phase)) * time.Nanosecond)
		m, d := reduce(t, m, edge(m, EventProviderCompactionCompleted, at))
		if m.State != StateLatched || !m.FreshAfter.Equal(at) || !d.Persist {
			t.Errorf("standalone completion from %s = state=%s fresh=%s decision=%+v", phase, m.State, m.FreshAfter, d)
		}
	}
	for _, phase := range []State{StateOutcomeUnknownHold, StateEventLossHold, StateBlockedCorrupt} {
		m := newTestMachine(t, true, 80)
		m.State = phase
		m.LastSourceSequence = 7
		before := m
		if got, d := Reduce(m, edge(m, EventProviderCompactionCompleted, testNow.Add(time.Nanosecond))); d.Rejected != RejectInvalidTransition || got != before {
			t.Errorf("completion escaped %s: machine=%+v rejection=%s", phase, got, d.Rejected)
		}
	}
}

func FuzzThresholdArithmetic(f *testing.F) {
	f.Add(uint64(80), uint64(100), uint8(80))
	f.Add(uint64(math.MaxUint64), uint64(math.MaxUint64), uint8(95))
	f.Fuzz(func(t *testing.T, used, limit uint64, threshold uint8) {
		threshold = 40 + threshold%56
		got := atOrAbove(used, limit, int(threshold))
		want := productCompare(used, 100, limit, uint64(threshold)) >= 0
		if got != want {
			t.Fatalf("atOrAbove(%d,%d,%d)=%v, want %v", used, limit, threshold, got, want)
		}
		got = atOrBelow(used, limit, int(threshold))
		want = productCompare(used, 100, limit, uint64(threshold)) <= 0
		if got != want {
			t.Fatalf("atOrBelow(%d,%d,%d)=%v, want %v", used, limit, threshold, got, want)
		}
	})
}

func productCompare(a, b, c, d uint64) int {
	left := new(big.Int).Mul(new(big.Int).SetUint64(a), new(big.Int).SetUint64(b))
	right := new(big.Int).Mul(new(big.Int).SetUint64(c), new(big.Int).SetUint64(d))
	return left.Cmp(right)
}

func TestLifecycleRequiresCurrentKeyAndFreshTelemetry(t *testing.T) {
	m := newTestMachine(t, true, 80)
	m, _ = reduce(t, m, observe(m, 80, 100, 1, testNow))
	wrong := edge(m, EventSessionIdle, testNow.Add(time.Nanosecond))
	wrong.Key.SessionInstance = "old-instance"
	if got, d := Reduce(m, wrong); d.Rejected != RejectWrongKey || got != m {
		t.Fatalf("wrong-key idle changed machine=%+v rejection=%s", got, d.Rejected)
	}
	staleAt := testNow.Add(MaxObservationDispatchAge + time.Nanosecond)
	if _, d := Reduce(m, edge(m, EventSessionIdle, staleAt)); d.Rejected != RejectStaleObservation {
		t.Fatalf("stale idle rejection=%s, want stale_observation", d.Rejected)
	}
	m, _ = reduce(t, m, observe(m, 80, 100, 2, staleAt.Add(time.Nanosecond)))
	m, _ = reduce(t, m, edge(m, EventSessionIdle, staleAt.Add(2*time.Nanosecond)))
	if _, d := Reduce(m, edge(m, EventDispatchStarted, staleAt.Add(MaxObservationDispatchAge+3*time.Nanosecond))); d.Rejected != RejectStaleObservation {
		t.Fatalf("stale dispatch rejection=%s, want stale_observation", d.Rejected)
	}

	wrong = edge(m, EventProviderCompactionStarted, staleAt.Add(4*time.Nanosecond))
	wrong.Key.BackendEpoch = "old-epoch"
	if got, d := Reduce(m, wrong); d.Rejected != RejectWrongKey || got != m {
		t.Fatalf("wrong-key lifecycle changed machine=%+v rejection=%s", got, d.Rejected)
	}
}

func TestDecisionDoesNotPersistTelemetryButPersistsSafetyBoundaries(t *testing.T) {
	m := newTestMachine(t, true, 80)
	m, d := reduce(t, m, observe(m, 79, 100, 1, testNow))
	if d.Persist {
		t.Fatal("non-crossing telemetry requested persistence")
	}
	m, d = reduce(t, m, observe(m, 80, 100, 2, testNow.Add(time.Nanosecond)))
	if d.Persist || m.State != StatePendingIdle {
		t.Fatalf("crossing telemetry persist=%v state=%s, want false/pending_idle", d.Persist, m.State)
	}
	m, d = reduce(t, m, edge(m, EventSessionIdle, testNow.Add(2*time.Nanosecond)))
	if !d.Persist || !d.RequestDispatch || m.State != StatePrepared {
		t.Fatalf("prepare decision=%+v state=%s", d, m.State)
	}
	m, d = reduce(t, m, edge(m, EventDispatchStarted, testNow.Add(3*time.Nanosecond)))
	if !d.Persist || m.State != StateExecuting {
		t.Fatalf("executing decision=%+v state=%s", d, m.State)
	}
	m, d = reduce(t, m, edge(m, EventActionWritten, testNow.Add(4*time.Nanosecond)))
	if !d.Persist || m.State != StateAwaitingConfirmation {
		t.Fatalf("write-boundary decision=%+v state=%s", d, m.State)
	}
	m, d = reduce(t, m, edge(m, EventProviderCompactionStarted, testNow.Add(5*time.Nanosecond)))
	if !d.Persist || m.State != StateProviderCompacting {
		t.Fatalf("provider start decision=%+v state=%s", d, m.State)
	}
	_, d = reduce(t, m, edge(m, EventProviderCompactionCompleted, testNow.Add(6*time.Nanosecond)))
	if !d.Persist {
		t.Fatal("latch transition did not request persistence")
	}
}

func TestNewInstanceStartsFreshCycleEvenAfterUnresolvedAction(t *testing.T) {
	m := newTestMachine(t, true, 80)
	m, _ = reduce(t, m, observe(m, 80, 100, 1, testNow))
	m, _ = reduce(t, m, edge(m, EventSessionIdle, testNow.Add(time.Nanosecond)))
	m, _ = reduce(t, m, edge(m, EventDispatchStarted, testNow.Add(2*time.Nanosecond)))
	replacement := Key{SessionID: "session", SessionInstance: "instance-2", BackendEpoch: "epoch-2", ProviderThreadID: "thread-2"}
	m, _ = reduce(t, m, Event{Kind: EventNewInstance, At: testNow.Add(3 * time.Nanosecond), Key: replacement})
	if m.State != StateArmed || m.Key != replacement || m.LastObservation != nil {
		t.Fatalf("replacement kept old unresolved action: %+v", m)
	}
}

func TestConfigAndBackendEpochRejectZeroOrNonMonotonicTimes(t *testing.T) {
	m := newTestMachine(t, true, 80)
	if _, d := Reduce(m, Event{Kind: EventConfigChanged, Config: Config{Enabled: true, Threshold: 81, Revision: 2}}); d.Rejected != RejectInvalidConfig {
		t.Fatalf("zero config timestamp rejection=%s, want invalid_config", d.Rejected)
	}
	m, _ = reduce(t, m, Event{Kind: EventConfigChanged, At: testNow, Config: Config{Enabled: true, Threshold: 81, Revision: 2}})
	if _, d := Reduce(m, Event{Kind: EventConfigChanged, At: testNow, Config: Config{Enabled: true, Threshold: 82, Revision: 3}}); d.Rejected != RejectInvalidConfig {
		t.Fatalf("nonmonotonic config timestamp rejection=%s, want invalid_config", d.Rejected)
	}
	nextEpoch := Key{SessionID: "session", SessionInstance: "instance-1", BackendEpoch: "epoch-2", ProviderThreadID: "thread-1"}
	if _, d := Reduce(m, Event{Kind: EventBackendEpochChanged, Key: nextEpoch}); d.Rejected != RejectWrongKey {
		t.Fatalf("zero epoch timestamp rejection=%s, want wrong_key", d.Rejected)
	}
	m, _ = reduce(t, m, Event{Kind: EventBackendEpochChanged, At: testNow.Add(time.Nanosecond), Key: nextEpoch})
	anotherEpoch := nextEpoch
	anotherEpoch.BackendEpoch = "epoch-3"
	if _, d := Reduce(m, Event{Kind: EventBackendEpochChanged, At: testNow.Add(time.Nanosecond), Key: anotherEpoch}); d.Rejected != RejectWrongKey {
		t.Fatalf("nonmonotonic epoch timestamp rejection=%s, want wrong_key", d.Rejected)
	}
}

func TestProviderLifecycleSequenceRejectsReplayAndReordering(t *testing.T) {
	m := newTestMachine(t, true, 80)
	m, _ = reduce(t, m, observe(m, 80, 100, 1, testNow))
	m, _ = reduce(t, m, edge(m, EventSessionIdle, testNow.Add(time.Nanosecond)))
	start := edge(m, EventProviderCompactionStarted, testNow.Add(2*time.Nanosecond))
	m, _ = reduce(t, m, start)
	before := m
	if got, d := Reduce(m, start); d.Rejected != RejectSourceSequence || got != before {
		t.Fatalf("replayed start machine=%+v rejection=%s", got, d.Rejected)
	}
	complete := edge(m, EventProviderCompactionCompleted, testNow.Add(3*time.Nanosecond))
	m, _ = reduce(t, m, complete)
	before = m
	if got, d := Reduce(m, complete); d.Rejected != RejectSourceSequence || got != before {
		t.Fatalf("replayed completion machine=%+v rejection=%s", got, d.Rejected)
	}
	outOfOrder := edge(m, EventProviderCompactionStarted, testNow.Add(4*time.Nanosecond))
	outOfOrder.SourceSequence = 1
	if got, d := Reduce(m, outOfOrder); d.Rejected != RejectSourceSequence || got != before {
		t.Fatalf("reordered lifecycle machine=%+v rejection=%s", got, d.Rejected)
	}

	m = newTestMachine(t, true, 80)
	loss := edge(m, EventEventLoss, testNow.Add(time.Nanosecond))
	m, _ = reduce(t, m, loss)
	before = m
	if got, d := Reduce(m, loss); d.Rejected != RejectSourceSequence || got != before {
		t.Fatalf("replayed event loss machine=%+v rejection=%s", got, d.Rejected)
	}
}

func TestProviderLifecycleRequiresSequenceAndTimestamp(t *testing.T) {
	m := newTestMachine(t, true, 80)
	m, _ = reduce(t, m, observe(m, 80, 100, 1, testNow))
	m, _ = reduce(t, m, edge(m, EventSessionIdle, testNow.Add(time.Nanosecond)))
	zeroSequence := edge(m, EventProviderCompactionStarted, testNow.Add(2*time.Nanosecond))
	zeroSequence.SourceSequence = 0
	if _, d := Reduce(m, zeroSequence); d.Rejected != RejectSourceSequence {
		t.Fatalf("zero lifecycle sequence rejection=%s, want source_sequence", d.Rejected)
	}
	zeroTime := edge(m, EventProviderCompactionStarted, time.Time{})
	if _, d := Reduce(m, zeroTime); d.Rejected != RejectInvalidEvent {
		t.Fatalf("zero lifecycle timestamp rejection=%s, want invalid_event", d.Rejected)
	}
	loss := edge(m, EventEventLoss, time.Time{})
	if _, d := Reduce(m, loss); d.Rejected != RejectInvalidEvent {
		t.Fatalf("zero event-loss timestamp rejection=%s, want invalid_event", d.Rejected)
	}
}

func TestSharedSourceSequenceRejectsCrossKindReordering(t *testing.T) {
	// A lifecycle event delayed behind accepted telemetry must not start a new
	// provider compaction merely because it used a separate counter.
	m := newTestMachine(t, true, 80)
	m, _ = reduce(t, m, observe(m, 80, 100, 100, testNow))
	m, _ = reduce(t, m, edge(m, EventSessionIdle, testNow.Add(time.Nanosecond)))
	lateLifecycle := edge(m, EventProviderCompactionStarted, testNow.Add(2*time.Nanosecond))
	lateLifecycle.SourceSequence = 2
	before := m
	if got, d := Reduce(m, lateLifecycle); d.Rejected != RejectSourceSequence || got != before {
		t.Fatalf("telemetry->lifecycle reorder machine=%+v rejection=%s", got, d.Rejected)
	}

	// Conversely a provider lifecycle frame consumes the same feed cursor, so a
	// delayed telemetry frame cannot rewind its occupancy decision.
	m = newTestMachine(t, true, 80)
	m, _ = reduce(t, m, observe(m, 80, 100, 1, testNow))
	m, _ = reduce(t, m, edge(m, EventSessionIdle, testNow.Add(time.Nanosecond)))
	start := edge(m, EventProviderCompactionStarted, testNow.Add(2*time.Nanosecond))
	start.SourceSequence = 2
	m, _ = reduce(t, m, start)
	staleTelemetry := observe(m, 1, 100, 2, testNow.Add(3*time.Nanosecond))
	before = m
	if got, d := Reduce(m, staleTelemetry); d.Rejected != RejectSourceSequence || got != before {
		t.Fatalf("lifecycle->telemetry reorder machine=%+v rejection=%s", got, d.Rejected)
	}
}

func percentageOf(v uint64, pct uint64) uint64 {
	return v/100*pct + (v%100*pct)/100
}
