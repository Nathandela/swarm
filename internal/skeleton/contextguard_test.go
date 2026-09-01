package skeleton

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/codex"
	"github.com/Nathandela/swarm/internal/contextguard"
	"github.com/Nathandela/swarm/internal/protocol"
)

const (
	contextGuardTestThread = "018f47aa-2fcb-7b9f-8e3d-7f8b8a9c0d1e"
	contextGuardTestTurn   = "018f47aa-2fcb-7b9f-8e3d-7f8b8a9c0d1d"
)

func contextGuardTestSource(t *testing.T) (adapter.ContextGuardSource, adapter.ContextGuardAction) {
	t.Helper()
	source, ok := adapter.AsContextGuardSource(codex.New())
	if !ok {
		t.Fatal("Codex ContextGuard source unavailable")
	}
	action, ok := source.ContextGuardAction("0.151.0")
	if !ok || !action.AutomaticDispatch {
		t.Fatalf("characterized action = %#v, %v; want the automatic descriptor (ADR-023 amendment 1)", action, ok)
	}
	return source, action
}

func TestContextGuardCharacterizedInitializeUserAgent(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "contextguard-initialize-0.150.1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var initialized struct {
		UserAgent json.RawMessage `json:"userAgent"`
	}
	if err := json.Unmarshal(raw, &initialized); err != nil {
		t.Fatal(err)
	}
	userAgent := parseContextGuardUserAgent(initialized.UserAgent)
	ad := codex.New()
	version, ok := ad.ParseVersion(userAgent)
	if !ok || version != "0.150.1" {
		t.Fatalf("characterized user agent %q parsed as %q, %v", userAgent, version, ok)
	}
	if got := parseContextGuardUserAgent(json.RawMessage(`{"name":"codex"}`)); got != "" {
		t.Fatalf("unknown object-shaped user agent became %q", got)
	}
}

func contextGuardTestManager(t *testing.T, enabled bool, threshold int) (*contextGuardManager, *contextGuardSettingsStore) {
	t.Helper()
	dir := t.TempDir()
	settings := openContextGuardSettingsStore(dir)
	if enabled || threshold != 80 {
		if _, err := settings.SetContextGuardSettings(0, protocol.ContextGuardAutoCompact{Enabled: enabled, ThresholdPercent: threshold}); err != nil {
			t.Fatal(err)
		}
	}
	manager := newContextGuardManager(nil, dir, settings)
	t.Cleanup(manager.close)
	return manager, settings
}

func contextGuardTestKey(instance, epoch string) contextguard.Key {
	return contextguard.Key{SessionID: "session", SessionInstance: instance, BackendEpoch: epoch, ProviderThreadID: contextGuardTestThread}
}

func contextGuardUsageFrame(used, total, limit int) []byte {
	return []byte(`{"method":"thread/tokenUsage/updated","params":{"threadId":"` + contextGuardTestThread + `","turnId":"` + contextGuardTestTurn + `","tokenUsage":{"last":{"cachedInputTokens":0,"inputTokens":0,"outputTokens":0,"reasoningOutputTokens":0,"totalTokens":` + itoa(used) + `},"total":{"cachedInputTokens":0,"inputTokens":0,"outputTokens":0,"reasoningOutputTokens":0,"totalTokens":` + itoa(total) + `},"modelContextWindow":` + itoa(limit) + `}}}`)
}

func contextGuardLifecycleFrame(method, timestamp string) []byte {
	when := "startedAtMs"
	if method == "item/completed" {
		when = "completedAtMs"
	}
	return []byte(`{"method":"` + method + `","params":{"item":{"id":"compact-1","type":"contextCompaction"},"` + when + `":` + timestamp + `,"threadId":"` + contextGuardTestThread + `","turnId":"` + contextGuardTestTurn + `"}}`)
}

func itoa(v int) string { return strconv.Itoa(v) }

func TestContextGuardManagerExactTelemetryIsObserveOnlyAndNeverPersisted(t *testing.T) {
	manager, _ := contextGuardTestManager(t, true, 80)
	source, action := contextGuardTestSource(t)
	key := contextGuardTestKey("instance-1", "epoch-1")
	manager.register("session", key, source, action)

	manager.ingest("session", contextguard.Key{SessionID: "session", SessionInstance: "instance-1", BackendEpoch: "epoch-1"}, 1, contextGuardUsageMethod, contextGuardUsageFrame(79, 900000, 100), time.Now())
	awaitContextGuardView(t, manager, "session", func(v protocol.ContextGuardView) bool {
		return v.UsagePercent == 79 && v.Phase == string(contextguard.StateArmed)
	})

	// Lifetime total is deliberately huge; only last.totalTokens can cross.
	manager.ingest("session", contextguard.Key{SessionID: "session", SessionInstance: "instance-1", BackendEpoch: "epoch-1"}, 2, contextGuardUsageMethod, contextGuardUsageFrame(80, 900000, 100), time.Now())
	view := awaitContextGuardView(t, manager, "session", func(v protocol.ContextGuardView) bool {
		return v.UsagePercent == 80 && v.Phase == string(contextguard.StatePendingIdle)
	})
	// action_unverified is the observe-only code ("no dispatch will occur");
	// stamping it on a guard that DOES dispatch would invert its meaning, so an
	// automatic guard carries no standing error code.
	if view.Support != string(adapter.ContextGuardAutomatic) || view.ErrorCode != "" {
		t.Fatalf("crossing view = %#v; an automatic guard must carry no standing error code", view)
	}
	if _, err := os.Stat(filepath.Join(manager.stateDir, "session", contextGuardStateFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("telemetry crossing was persisted: %v", err)
	}
}

func TestContextGuardManagerRejectsStaleBackendAndReplacementInstance(t *testing.T) {
	manager, _ := contextGuardTestManager(t, true, 80)
	source, action := contextGuardTestSource(t)
	oldKey := contextGuardTestKey("instance-1", "epoch-old")
	manager.register("session", oldKey, source, action)
	newKey := contextGuardTestKey("instance-2", "epoch-new")
	manager.register("session", newKey, source, action)

	manager.ingest("session", contextguard.Key{SessionID: "session", SessionInstance: oldKey.SessionInstance, BackendEpoch: oldKey.BackendEpoch}, 99, contextGuardUsageMethod, contextGuardUsageFrame(95, 95, 100), time.Now())
	time.Sleep(20 * time.Millisecond)
	if got, _ := manager.view("session"); got.UsagePercent != 0 || got.Phase != string(contextguard.StateArmed) {
		t.Fatalf("stale backend mutated replacement: %#v", got)
	}
	manager.ingest("session", contextguard.Key{SessionID: "session", SessionInstance: newKey.SessionInstance, BackendEpoch: newKey.BackendEpoch}, 1, contextGuardUsageMethod, contextGuardUsageFrame(95, 95, 100), time.Now())
	awaitContextGuardView(t, manager, "session", func(v protocol.ContextGuardView) bool { return v.Phase == string(contextguard.StatePendingIdle) })
}

func TestContextGuardFeedBuffersLifecycleUntilRegistration(t *testing.T) {
	manager, _ := contextGuardTestManager(t, true, 80)
	d := &Daemon{contextGuards: manager}
	feed := newBackendFeed()
	key := contextGuardTestKey("instance-1", feed.epoch)
	at := time.Now()
	d.captureContextGuardFrame("session", key.SessionInstance, feed, "item/started", contextGuardLifecycleFrame("item/started", "1700000000000"), at)
	d.captureContextGuardFrame("session", key.SessionInstance, feed, "item/completed", contextGuardLifecycleFrame("item/completed", "1700000000001"), at.Add(time.Nanosecond))
	if _, ok := manager.view("session"); ok {
		t.Fatal("feed evidence created a guard before backend registration")
	}

	source, action := contextGuardTestSource(t)
	if !manager.register("session", key, source, action) {
		t.Fatal("guard registration failed")
	}
	d.activateContextGuardFeed("session", key, feed)
	view := awaitContextGuardView(t, manager, "session", func(v protocol.ContextGuardView) bool {
		return v.Phase == string(contextguard.StateLatched)
	})
	if view.LastResult != "compacted" {
		t.Fatalf("buffered lifecycle view = %#v; want compacted result", view)
	}
}

func TestContextGuardFeedOverflowBeforeRegistrationFailsClosed(t *testing.T) {
	manager, _ := contextGuardTestManager(t, true, 80)
	d := &Daemon{contextGuards: manager}
	feed := newBackendFeed()
	key := contextGuardTestKey("instance-1", feed.epoch)
	for i := 0; i <= contextGuardPendingLimit; i++ {
		d.captureContextGuardFrame("session", key.SessionInstance, feed, "item/started", contextGuardLifecycleFrame("item/started", strconv.FormatInt(1700000000000+int64(i), 10)), time.Now())
	}
	source, action := contextGuardTestSource(t)
	if !manager.register("session", key, source, action) {
		t.Fatal("guard registration failed")
	}
	d.activateContextGuardFeed("session", key, feed)
	awaitContextGuardView(t, manager, "session", func(v protocol.ContextGuardView) bool {
		return v.Phase == string(contextguard.StateEventLossHold)
	})
}

func TestContextGuardBufferedObservationKeepsCaptureSettingsRevision(t *testing.T) {
	manager, settings := contextGuardTestManager(t, false, 80)
	d := &Daemon{contextGuards: manager}
	feed := newBackendFeed()
	key := contextGuardTestKey("instance-1", feed.epoch)
	d.captureContextGuardFrame("session", key.SessionInstance, feed, contextGuardUsageMethod, contextGuardUsageFrame(80, 80, 100), time.Now())

	next, err := settings.SetContextGuardSettings(0, protocol.ContextGuardAutoCompact{Enabled: true, ThresholdPercent: 80})
	if err != nil {
		t.Fatal(err)
	}
	manager.updateSettings(next)
	source, action := contextGuardTestSource(t)
	if !manager.register("session", key, source, action) {
		t.Fatal("guard registration failed")
	}
	d.activateContextGuardFeed("session", key, feed)
	time.Sleep(20 * time.Millisecond)
	view, _ := manager.view("session")
	if view.Phase != string(contextguard.StateArmed) || view.UsagePercent != 0 {
		t.Fatalf("pre-settings observation was relabeled current: %#v", view)
	}

	d.captureContextGuardFrame("session", key.SessionInstance, feed, contextGuardUsageMethod, contextGuardUsageFrame(80, 80, 100), time.Now())
	awaitContextGuardView(t, manager, "session", func(v protocol.ContextGuardView) bool {
		return v.Phase == string(contextguard.StatePendingIdle) && v.UsagePercent == 80
	})
}

func TestContextGuardSettingsUpdatesCannotRegressRevision(t *testing.T) {
	manager, settings := contextGuardTestManager(t, false, 80)
	source, action := contextGuardTestSource(t)
	key := contextGuardTestKey("instance-1", "epoch-1")
	manager.register("session", key, source, action)

	revisionOne, err := settings.SetContextGuardSettings(0, protocol.ContextGuardAutoCompact{Enabled: true, ThresholdPercent: 80})
	if err != nil {
		t.Fatal(err)
	}
	revisionTwo, err := settings.SetContextGuardSettings(1, protocol.ContextGuardAutoCompact{Enabled: true, ThresholdPercent: 90})
	if err != nil {
		t.Fatal(err)
	}
	// Force the cross-connection completion order: the newer durable write reaches
	// workers first, then the older request resumes late.
	manager.updateSettings(revisionTwo)
	manager.updateSettings(revisionOne)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		manager.mu.Lock()
		session := manager.sessions["session"]
		manager.mu.Unlock()
		session.stateMu.Lock()
		got := session.machine.Config
		session.stateMu.Unlock()
		if got.Revision == 2 {
			if got.Threshold != 90 || manager.captureRevision.Load() != 2 || manager.appliedRevision != 2 {
				t.Fatalf("newest config = %+v capture=%d applied=%d", got, manager.captureRevision.Load(), manager.appliedRevision)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("newest settings revision was not applied")
}

func TestContextGuardRegistrationCannotSkipGlobalSettingsFanout(t *testing.T) {
	manager, settings := contextGuardTestManager(t, false, 80)
	source, action := contextGuardTestSource(t)
	oldKey := contextGuardTestKey("instance-1", "epoch-1")
	manager.register("session", oldKey, source, action)

	revisionOne, err := settings.SetContextGuardSettings(0, protocol.ContextGuardAutoCompact{Enabled: true, ThresholdPercent: 80})
	if err != nil {
		t.Fatal(err)
	}
	manager.updateSettings(revisionOne)
	revisionTwo, err := settings.SetContextGuardSettings(1, protocol.ContextGuardAutoCompact{Enabled: true, ThresholdPercent: 90})
	if err != nil {
		t.Fatal(err)
	}

	// A backend registers in the store-commit/fan-out gap. It reads rev2 itself,
	// but must not make the manager believe rev2 reached the older worker.
	newKey := contextguard.Key{SessionID: "new-session", SessionInstance: "instance-2", BackendEpoch: "epoch-2", ProviderThreadID: contextGuardTestThread}
	if !manager.register("new-session", newKey, source, action) {
		t.Fatal("new session registration failed")
	}
	if manager.captureRevision.Load() != 2 || manager.appliedRevision != 1 {
		t.Fatalf("registration gap revisions capture=%d applied=%d, want 2/1", manager.captureRevision.Load(), manager.appliedRevision)
	}
	manager.updateSettings(revisionTwo)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		manager.mu.Lock()
		oldSession := manager.sessions["session"]
		newSession := manager.sessions["new-session"]
		manager.mu.Unlock()
		oldSession.stateMu.Lock()
		oldConfig := oldSession.machine.Config
		oldSession.stateMu.Unlock()
		newSession.stateMu.Lock()
		newConfig := newSession.machine.Config
		newSession.stateMu.Unlock()
		if oldConfig.Revision == 2 && newConfig.Revision == 2 {
			if oldConfig.Threshold != 90 || newConfig.Threshold != 90 || manager.appliedRevision != 2 {
				t.Fatalf("fan-out configs old=%+v new=%+v applied=%d", oldConfig, newConfig, manager.appliedRevision)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("rev2 did not fan out to both pre-existing and gap-registered workers")
}

func TestContextGuardBackendReplacementJoinsOldPersistenceBeforeRestore(t *testing.T) {
	manager, _ := contextGuardTestManager(t, true, 80)
	source, action := contextGuardTestSource(t)
	oldKey := contextGuardTestKey("instance-1", "epoch-old")
	manager.register("session", oldKey, source, action)
	manager.mu.Lock()
	old := manager.sessions["session"]
	manager.mu.Unlock()
	entered := make(chan struct{})
	release := make(chan struct{})
	old.stateMu.Lock()
	old.persistFn = func(machine contextguard.Machine) error {
		close(entered)
		<-release
		return old.persist(machine)
	}
	old.stateMu.Unlock()
	manager.ingest("session", oldKey, 1, "item/started", contextGuardLifecycleFrame("item/started", "1700000000000"), time.Now())
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("old worker did not reach persistence seam")
	}

	registered := make(chan bool, 1)
	newKey := contextGuardTestKey("instance-1", "epoch-new")
	go func() { registered <- manager.register("session", newKey, source, action) }()
	select {
	case <-registered:
		t.Fatal("replacement registered before old persistence completed")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if !<-registered {
		t.Fatal("replacement registration failed")
	}
	view, _ := manager.view("session")
	if view.Phase != string(contextguard.StateEventLossHold) {
		t.Fatalf("replacement restored phase %q; want event-loss hold for interrupted provider compaction", view.Phase)
	}

	raw, err := os.ReadFile(filepath.Join(manager.stateDir, "session", contextGuardStateFile))
	if err != nil || !strings.Contains(string(raw), `"state":"event_loss_hold"`) {
		t.Fatalf("replacement sidecar = %s, %v; want recovered hold", raw, err)
	}
}

func TestContextGuardNewInstanceJoinsWorkerAndRemovesOldSidecar(t *testing.T) {
	dir := t.TempDir()
	settings := openContextGuardSettingsStore(dir)
	if _, err := settings.SetContextGuardSettings(0, protocol.ContextGuardAutoCompact{Enabled: true, ThresholdPercent: 80}); err != nil {
		t.Fatal(err)
	}
	manager := newContextGuardManager(nil, dir, settings)
	t.Cleanup(manager.close)
	d := &Daemon{contextGuards: manager}
	d.capStore.dir = dir
	if err := d.recordSessionInstance("session", "instance-1", 101); err != nil {
		t.Fatal(err)
	}
	source, action := contextGuardTestSource(t)
	key := contextGuardTestKey("instance-1", "epoch-1")
	manager.register("session", key, source, action)
	manager.ingest("session", key, 1, "item/completed", contextGuardLifecycleFrame("item/completed", "1700000000001"), time.Now())
	awaitContextGuardView(t, manager, "session", func(v protocol.ContextGuardView) bool {
		return v.Phase == string(contextguard.StateLatched)
	})
	path := filepath.Join(dir, "session", contextGuardStateFile)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("old lifecycle sidecar missing before replacement: %v", err)
	}

	if err := d.recordSessionInstance("session", "instance-2", 202); err != nil {
		t.Fatal(err)
	}
	if _, ok := manager.view("session"); ok {
		t.Fatal("old guard remained registered after instance replacement")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old lifecycle sidecar survived replacement: %v", err)
	}
}

func TestBackendFeedLossCannotRemoveReplacement(t *testing.T) {
	d := &Daemon{}
	d.backend.live = make(map[string]*sessionBackend)
	d.backend.requests = make(map[string]map[string]json.RawMessage)
	d.backend.byID = make(map[string]map[string]string)
	d.backend.adopted = make(map[string]string)
	oldFeed := newBackendFeed()
	newFeed := newBackendFeed()
	newBackend := &sessionBackend{conn: newR7FakeBackend(), feed: newFeed}
	d.backend.live["session"] = newBackend
	if d.forgetBackendForFeed("session", oldFeed.epoch) {
		t.Fatal("late old-feed loss removed the replacement backend")
	}
	d.backend.mu.Lock()
	got := d.backend.live["session"]
	d.backend.mu.Unlock()
	if got != newBackend {
		t.Fatalf("replacement backend = %p, want %p", got, newBackend)
	}
}

func TestContextGuardManagerPersistsLifecycleNotTelemetryAndRecoversLatch(t *testing.T) {
	dir := t.TempDir()
	settings := openContextGuardSettingsStore(dir)
	if _, err := settings.SetContextGuardSettings(0, protocol.ContextGuardAutoCompact{Enabled: true, ThresholdPercent: 80}); err != nil {
		t.Fatal(err)
	}
	source, action := contextGuardTestSource(t)
	key := contextGuardTestKey("instance-1", "epoch-1")
	first := newContextGuardManager(nil, dir, settings)
	first.register("session", key, source, action)
	first.ingest("session", contextguard.Key{SessionID: "session", SessionInstance: key.SessionInstance, BackendEpoch: key.BackendEpoch}, 1, contextGuardUsageMethod, contextGuardUsageFrame(80, 80, 100), time.Now())
	first.ingest("session", contextguard.Key{SessionID: "session", SessionInstance: key.SessionInstance, BackendEpoch: key.BackendEpoch}, 2, "item/started", contextGuardLifecycleFrame("item/started", "1700000000000"), time.Now())
	first.ingest("session", contextguard.Key{SessionID: "session", SessionInstance: key.SessionInstance, BackendEpoch: key.BackendEpoch}, 3, "item/completed", contextGuardLifecycleFrame("item/completed", "1700000000001"), time.Now())
	awaitContextGuardView(t, first, "session", func(v protocol.ContextGuardView) bool { return v.Phase == string(contextguard.StateLatched) })
	first.close()

	path := filepath.Join(dir, "session", contextGuardStateFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "token") || strings.Contains(string(raw), "80/100") {
		t.Fatalf("durable lifecycle leaked telemetry: %s", raw)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("state file mode = %v, %v; want 0600", fi, err)
	}

	restarted := newContextGuardManager(nil, dir, settings)
	t.Cleanup(restarted.close)
	restartedKey := contextGuardTestKey("instance-1", "epoch-2")
	restarted.register("session", restartedKey, source, action)
	if got, _ := restarted.view("session"); got.Phase != string(contextguard.StateLatched) {
		t.Fatalf("restart lost latch: %#v", got)
	}
	restarted.ingest("session", contextguard.Key{SessionID: "session", SessionInstance: restartedKey.SessionInstance, BackendEpoch: restartedKey.BackendEpoch}, 1, contextGuardUsageMethod, contextGuardUsageFrame(70, 70, 100), time.Now())
	awaitContextGuardView(t, restarted, "session", func(v protocol.ContextGuardView) bool { return v.Phase == string(contextguard.StateArmed) })
}

func TestContextGuardManagerCorruptStateFailsClosedWithoutOverwrite(t *testing.T) {
	dir := t.TempDir()
	settings := openContextGuardSettingsStore(dir)
	path := filepath.Join(dir, "session", contextGuardStateFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	const corrupt = `{"schema_version":1,"unknown":true}`
	if err := os.WriteFile(path, []byte(corrupt), 0o600); err != nil {
		t.Fatal(err)
	}
	source, action := contextGuardTestSource(t)
	manager := newContextGuardManager(nil, dir, settings)
	t.Cleanup(manager.close)
	manager.register("session", contextGuardTestKey("instance-1", "epoch-1"), source, action)
	got, ok := manager.view("session")
	if !ok || got.Phase != string(contextguard.StateBlockedCorrupt) || got.ErrorCode != "state_corrupt" {
		t.Fatalf("corrupt state view = %#v, %v", got, ok)
	}
	settingsNow, err := settings.SetContextGuardSettings(0, protocol.ContextGuardAutoCompact{Enabled: true, ThresholdPercent: 90})
	if err != nil {
		t.Fatal(err)
	}
	manager.updateSettings(settingsNow)
	after, err := os.ReadFile(path)
	if err != nil || string(after) != corrupt {
		t.Fatalf("blocked store overwrote corrupt evidence: %q, %v", after, err)
	}
}

func TestContextGuardManagerInvalidPersistedThresholdFailsClosed(t *testing.T) {
	dir := t.TempDir()
	settings := openContextGuardSettingsStore(dir)
	path := filepath.Join(dir, "session", contextGuardStateFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	const corrupt = `{"schema_version":1,"session_instance":"instance-1","settings_revision":0,"state":"latched","trigger_threshold":39}`
	if err := os.WriteFile(path, []byte(corrupt), 0o600); err != nil {
		t.Fatal(err)
	}
	source, action := contextGuardTestSource(t)
	manager := newContextGuardManager(nil, dir, settings)
	t.Cleanup(manager.close)
	manager.register("session", contextGuardTestKey("instance-1", "epoch-1"), source, action)
	got, _ := manager.view("session")
	if got.Phase != string(contextguard.StateBlockedCorrupt) || got.ErrorCode != "state_corrupt" {
		t.Fatalf("invalid threshold state view = %#v", got)
	}
}

func TestContextGuardManagerFuturePersistedSettingsRevisionFailsClosed(t *testing.T) {
	dir := t.TempDir()
	settings := openContextGuardSettingsStore(dir)
	path := filepath.Join(dir, "session", contextGuardStateFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	const future = `{"schema_version":1,"session_instance":"instance-1","settings_revision":1,"state":"latched","trigger_threshold":80}`
	if err := os.WriteFile(path, []byte(future), 0o600); err != nil {
		t.Fatal(err)
	}
	source, action := contextGuardTestSource(t)
	manager := newContextGuardManager(nil, dir, settings)
	t.Cleanup(manager.close)
	manager.register("session", contextGuardTestKey("instance-1", "epoch-1"), source, action)
	got, _ := manager.view("session")
	if got.Phase != string(contextguard.StateBlockedCorrupt) || got.ErrorCode != "state_corrupt" {
		t.Fatalf("future settings revision view = %#v", got)
	}
}

func TestContextGuardCallbackDoesNotWaitForSettingsFsync(t *testing.T) {
	manager, settings := contextGuardTestManager(t, false, 80)
	source, action := contextGuardTestSource(t)
	key := contextGuardTestKey("instance-1", "epoch-1")
	manager.register("session", key, source, action)

	manager.mu.Lock()
	session := manager.sessions["session"]
	manager.mu.Unlock()
	entered := make(chan struct{})
	release := make(chan struct{})
	session.stateMu.Lock()
	session.persistFn = func(contextguard.Machine) error {
		close(entered)
		<-release
		return nil
	}
	session.stateMu.Unlock()

	next, err := settings.SetContextGuardSettings(0, protocol.ContextGuardAutoCompact{Enabled: true, ThresholdPercent: 80})
	if err != nil {
		t.Fatal(err)
	}
	manager.updateSettings(next)
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("settings transition never reached persistence seam")
	}

	returned := make(chan struct{})
	go func() {
		manager.ingest("session", contextguard.Key{SessionID: "session", SessionInstance: key.SessionInstance, BackendEpoch: key.BackendEpoch}, 1, contextGuardUsageMethod, contextGuardUsageFrame(80, 80, 100), time.Now())
		close(returned)
	}()
	select {
	case <-returned:
		// The callback copied and woke while the worker remained behind the fsync seam.
	case <-time.After(250 * time.Millisecond):
		close(release)
		t.Fatal("provider callback blocked behind ContextGuard persistence")
	}
	close(release)
	awaitContextGuardView(t, manager, "session", func(v protocol.ContextGuardView) bool {
		return v.Phase == string(contextguard.StatePendingIdle)
	})
}

func TestContextGuardStateWriteFailureFailsClosedWithSanitizedView(t *testing.T) {
	manager, settings := contextGuardTestManager(t, false, 80)
	source, action := contextGuardTestSource(t)
	key := contextGuardTestKey("instance-1", "epoch-1")
	manager.register("session", key, source, action)

	manager.mu.Lock()
	session := manager.sessions["session"]
	manager.mu.Unlock()
	session.stateMu.Lock()
	session.persistFn = func(contextguard.Machine) error { return errors.New("secret filesystem detail") }
	session.stateMu.Unlock()

	next, err := settings.SetContextGuardSettings(0, protocol.ContextGuardAutoCompact{Enabled: true, ThresholdPercent: 80})
	if err != nil {
		t.Fatal(err)
	}
	manager.updateSettings(next)
	view := awaitContextGuardView(t, manager, "session", func(v protocol.ContextGuardView) bool {
		return v.Phase == string(contextguard.StateBlockedCorrupt)
	})
	if view.Support != string(adapter.ContextGuardAutomatic) || view.ErrorCode != "state_write_failed" || strings.Contains(view.ErrorCode, "secret") {
		t.Fatalf("write failure view = %#v; want stable sanitized failure", view)
	}

	manager.ingest("session", contextguard.Key{SessionID: "session", SessionInstance: key.SessionInstance, BackendEpoch: key.BackendEpoch}, 1, contextGuardUsageMethod, contextGuardUsageFrame(95, 95, 100), time.Now())
	time.Sleep(20 * time.Millisecond)
	after, _ := manager.view("session")
	if after != view {
		t.Fatalf("failed-closed session consumed later telemetry: before=%#v after=%#v", view, after)
	}
}

func awaitContextGuardView(t *testing.T, manager *contextGuardManager, id string, pred func(protocol.ContextGuardView) bool) protocol.ContextGuardView {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if view, ok := manager.view(id); ok && pred(view) {
			return view
		}
		time.Sleep(time.Millisecond)
	}
	view, _ := manager.view(id)
	t.Fatalf("timed out waiting for ContextGuard view, last=%#v", view)
	return protocol.ContextGuardView{}
}
