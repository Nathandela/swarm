package skeleton

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/opencodebridge"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

func TestHandoffStorageImmutableVersionsOutsideSource(t *testing.T) {
	f := newOpenCodeSendFixture(t)
	source, _ := f.d.core.Get(f.binding.WorkerID)
	resolver := &openCodeHistoryResolver{core: f.d.core}
	first, outcome := resolver.LocateTranscript(source, source.ConversationID)
	if outcome != resumeHistoryFound {
		t.Fatalf("export: %v", outcome)
	}
	original, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	second, outcome := resolver.LocateTranscript(source, source.ConversationID)
	if outcome != resumeHistoryFound || first != second {
		t.Fatalf("identical history produced duplicate input: %q %q (%v)", first, second, outcome)
	}
	after, _ := os.ReadFile(first)
	if !bytes.Equal(original, after) {
		t.Fatal("reused immutable history rewritten")
	}
	if filepath.Dir(first) == filepath.Dir(f.binding.SocketPath) {
		t.Fatal("successor input belongs to deletable source attempt")
	}
	successor, err := f.d.api.Launch(storageHandoffSpec(t, f))
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(f.d.stateDir, successor.ID, "shim-launch.json")
	launchRaw, err := os.ReadFile(configPath)
	if err != nil || !bytes.Contains(launchRaw, []byte(first)) {
		t.Fatalf("successor did not persist exact export reference: %v", err)
	}
	f.nativeVersion.Store("2.0.0")
	changed, outcome := resolver.LocateTranscript(source, source.ConversationID)
	if outcome != resumeHistoryFound || changed == first {
		t.Fatal("changed history provenance replaced original version")
	}
	if err := f.d.api.Delete(source.ID); err != nil {
		t.Fatal(err)
	}
	after, err = os.ReadFile(first)
	if err != nil || !bytes.Equal(original, after) {
		t.Fatalf("source deletion destroyed successor input: %v", err)
	}
	if err := f.d.Close(); err != nil {
		t.Fatal(err)
	}
	core, err := daemon.Open(daemon.Config{StateDir: f.d.stateDir, SocketPath: filepath.Join(f.d.stateDir, "d.sock"), LockPath: filepath.Join(f.d.stateDir, "d.lock"), LogPath: filepath.Join(f.d.stateDir, "d.log"), ShimBinary: swarmBin, MaxSessions: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = core.Delete(successor.ID); _ = core.Close() }()
	if _, ok := core.Get(successor.ID); !ok {
		t.Fatal("successor lost across restart")
	}
	launchAfter, err := os.ReadFile(configPath)
	if err != nil || !bytes.Equal(launchRaw, launchAfter) {
		t.Fatal("restart rewrote successor reference")
	}
	after, err = os.ReadFile(first)
	if err != nil || !bytes.Equal(original, after) {
		t.Fatal("restart lost referenced immutable content")
	}
}

func TestHandoffStorageValidatesDirectoryBeforeExport(t *testing.T) {
	source := handsOffSource("source", filepath.Join(t.TempDir(), "missing"), legacyClaudeID)
	source.AgentType = "opencode"
	source.ConversationID = "ses_" + strings.Repeat("a", 32)
	source.RequiredRuntime = persist.RequiredRuntimeOpenCode
	source.OpenCodeBinding = &persist.OpenCodeBinding{NativeCreatedAtMS: 123}
	resolver := &nativeTranscriptFixture{path: filepath.Join(t.TempDir(), "provider-history")}
	_, err := composeHandsOffLaunch(handsOffSpec(testEndpoint, source.ID, t.TempDir()), testEndpoint, func(string) (persist.Meta, bool) { return source, true }, resolver)
	if err == nil || !strings.Contains(err.Error(), "directory") {
		t.Fatalf("missing cwd accepted: %v", err)
	}
	if resolver.called != "" {
		t.Fatal("export was requested before cwd validation")
	}
}

func TestHandoffStorageLegacyReferenceBlocksSourceDeletion(t *testing.T) {
	source := endedCodexSource("source", nil)
	child := endedCodexSource("child", nil)
	child.SpawnedFrom = source.ID
	api := continuityAPI(t, source, child)
	legacy := filepath.Join(api.stateDir, source.ID, "native-history-legacy.json")
	if err := os.WriteFile(legacy, []byte("legacy immutable input"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(api.stateDir, child.ID, "shim-launch.json"), []byte(`{"session_id":"child","argv":["agent","read `+legacy+`"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := api.Delete(source.ID); err == nil {
		t.Fatal("deleted source with retained legacy handoff dependency")
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatal("legacy history was removed")
	}
}

func storageHandoffSpec(t *testing.T, f *openCodeSendFixture) daemon.LaunchSpec {
	t.Helper()
	binDir := t.TempDir()
	bin := filepath.Join(binDir, "claude")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nif [ \"$1\" = --version ]; then echo 2.1.0; exit 0; fi\nexec /bin/sleep 0.5\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return daemon.LaunchSpec{AgentType: "claude", Cwd: f.binding.Directory, Options: map[string]string{protocol.OptionHandoffFrom: protocol.NamespacedID(f.d.api.endpointID, f.binding.WorkerID)}, ClientEnv: []string{"PATH=" + binDir + ":/usr/bin:/bin", "HOME=" + t.TempDir()}, Cols: 80, Rows: 24}
}

func storageExports(t *testing.T, stateDir string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(stateDir, handoffExportDirectory))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestHandoffStorageDefiniteFailureCleansOnlyNewlyOwned(t *testing.T) {
	f := newOpenCodeSendFixture(t, func(f *openCodeSendFixture) { f.d = assemble(t, func(c *Config) { c.MaxSessions = 1 }) })
	spec := storageHandoffSpec(t, f)
	if _, err := f.d.api.Launch(spec); !errors.Is(err, daemon.ErrMaxSessions) {
		t.Fatalf("expected definite cap refusal: %v", err)
	}
	if entries := storageExports(t, f.d.stateDir); len(entries) != 0 {
		t.Fatal("definite failure leaked newly owned input")
	}
	source, _ := f.d.core.Get(f.binding.WorkerID)
	retained, outcome := f.d.api.historyResolver.LocateTranscript(source, source.ConversationID)
	if outcome != resumeHistoryFound {
		t.Fatal("direct export failed")
	}
	original, _ := os.ReadFile(retained)
	if _, err := f.d.api.Launch(spec); !errors.Is(err, daemon.ErrMaxSessions) {
		t.Fatal(err)
	}
	after, err := os.ReadFile(retained)
	if err != nil || !bytes.Equal(after, original) {
		t.Fatal("failed reuser removed existing export")
	}
	provider := filepath.Join(t.TempDir(), "native-provider-history")
	if err := os.WriteFile(provider, []byte("provider-owned"), 0600); err != nil {
		t.Fatal(err)
	}
	f.d.api.historyResolver = &nativeTranscriptFixture{path: provider}
	if _, err := f.d.api.Launch(spec); !errors.Is(err, daemon.ErrMaxSessions) {
		t.Fatal(err)
	}
	if _, err := os.Stat(provider); err != nil {
		t.Fatal("arbitrary resolver provider path was removed")
	}
}

func TestHandoffStoragePrelaunchValidationDoesNotExport(t *testing.T) {
	f := newOpenCodeSendFixture(t)
	spec := storageHandoffSpec(t, f)
	spec.ClientEnv = []string{"PATH=" + t.TempDir()}
	if _, err := f.d.api.Launch(spec); err == nil {
		t.Fatal("unresolvable target accepted")
	}
	if _, err := os.Lstat(filepath.Join(f.d.stateDir, handoffExportDirectory)); !os.IsNotExist(err) {
		t.Fatal("target binary validation exported history")
	}
}

func TestHandoffStorageCreatorFailureCannotRemoveConcurrentSuccess(t *testing.T) {
	f := newOpenCodeSendFixture(t)
	page := ocHistoryPage(f.identity, "", "")
	page.Session = json.RawMessage(strings.Replace(string(page.Session), `"version":"1.19.3"`, `"version":"1.19.3","agent":"build"`, 1))
	f.historyPage.Store(func(int, string) (opencodebridge.Snapshot, error) { return page, nil })
	published, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	var calls atomic.Int32
	resolver := f.d.api.historyResolver.(*openCodeHistoryResolver)
	resolver.afterPublish = func() {
		if calls.Add(1) == 1 {
			close(published)
			<-release
		}
	}
	failing := storageHandoffSpec(t, f)
	failing.AgentType = "opencode"
	failing.Argv = []string{fakeAgentBin, mustScript(t, "idle 1s\n")}
	firstResult := make(chan error, 1)
	go func() { _, err := f.d.api.Launch(failing); firstResult <- err }()
	<-published
	if f.d.api.handoffMu.TryLock() {
		f.d.api.handoffMu.Unlock()
		t.Fatal("publication did not retain handoff lock")
	}
	succeeding := storageHandoffSpec(t, f)
	secondStarted, secondResult := make(chan struct{}), make(chan error, 1)
	go func() { close(secondStarted); _, err := f.d.api.Launch(succeeding); secondResult <- err }()
	<-secondStarted
	select {
	case err := <-secondResult:
		t.Fatalf("concurrent launch escaped creator custody: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-firstResult; err == nil {
		t.Fatal("malformed inherited selection should refuse creator launch")
	}
	if err := <-secondResult; err != nil {
		t.Fatal(err)
	}
	entries := storageExports(t, f.d.stateDir)
	if len(entries) != 1 {
		t.Fatalf("successful concurrent handoff input missing: %d", len(entries))
	}
	raw, err := os.ReadFile(filepath.Join(f.d.stateDir, handoffExportDirectory, entries[0].Name()))
	if err != nil || !bytes.Contains(raw, []byte(`"agent":"build"`)) && !bytes.Contains(raw, []byte(`"agent": "build"`)) {
		t.Fatalf("successor input was removed: %v", err)
	}
}

func TestHandoffStorageAmbiguousLaunchRetainsInput(t *testing.T) {
	f := newOpenCodeSendFixture(t)
	source, _ := f.d.core.Get(f.binding.WorkerID)
	shim := filepath.Join(t.TempDir(), "unconfirmed-shim")
	if err := os.WriteFile(shim, []byte("#!/bin/sh\nexec /bin/sleep 0.5\n"), 0700); err != nil {
		t.Fatal(err)
	}
	api := continuityAPIWithConfig(t, func(c *daemon.Config) { c.ShimBinary = shim }, source)
	api.historyResolver = f.d.api.historyResolver
	spec := storageHandoffSpec(t, f)
	spec.Options[protocol.OptionHandoffFrom] = protocol.NamespacedID(api.endpointID, source.ID)
	m, err := api.Launch(spec)
	if err == nil || m.ID == "" {
		t.Fatalf("expected persisted unconfirmed launch, got %+v %v", m, err)
	}
	if entries := storageExports(t, f.d.stateDir); len(entries) != 1 {
		t.Fatal("ambiguous acceptance lost handoff input")
	}
}

func TestHandoffStorageHostileCandidatesAndDirectory(t *testing.T) {
	id := opencodebridge.Identity{Version: 1, WorkerID: "worker", RootID: "ses_" + strings.Repeat("a", 32), Directory: "/native", SessionInstance: strings.Repeat("b", 32), BackendGeneration: strings.Repeat("b", 32), NativeCreatedAtMS: 123}
	page := ocHistoryPage(id, "", "")
	result := openCodeHistoryExport{Info: page.Session, Messages: page.Messages, Provenance: openCodeHistoryProvenance{Identity: id, CapturedAt: time.Now()}}
	for _, kind := range []string{"symlink", "hardlink", "contents", "mode", "directory-symlink"} {
		t.Run(kind, func(t *testing.T) {
			state := t.TempDir()
			if kind == "directory-symlink" {
				outside := t.TempDir()
				if err := os.Symlink(outside, filepath.Join(state, handoffExportDirectory)); err != nil {
					t.Fatal(err)
				}
				if _, _, err := publishHandoffExport(state, result); err == nil {
					t.Fatal("symlink export directory accepted")
				}
				files, _ := os.ReadDir(outside)
				if len(files) != 0 {
					t.Fatal("wrote through hostile directory")
				}
				return
			}
			path, owned, err := publishHandoffExport(state, result)
			if err != nil {
				t.Fatal(err)
			}
			_ = owned.root.Close()
			sentinel := filepath.Join(t.TempDir(), "sentinel")
			if err := os.WriteFile(sentinel, []byte("do not touch"), 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "symlink":
				_ = os.Remove(path)
				err = os.Symlink(sentinel, path)
			case "hardlink":
				err = os.Link(path, sentinel+"-link")
			case "contents":
				err = os.WriteFile(path, []byte(`{"info":null}`), 0600)
			case "mode":
				err = os.Chmod(path, 0644)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := publishHandoffExport(state, result); err == nil {
				t.Fatal("hostile existing candidate accepted")
			}
			raw, _ := os.ReadFile(sentinel)
			if string(raw) != "do not touch" {
				t.Fatal("outside file changed")
			}
		})
	}
}

func TestHandoffStorageLegacyUnknownDependencyRefusesDeletion(t *testing.T) {
	for _, kind := range []string{"missing", "invalid", "symlink", "empty", "array"} {
		t.Run(kind, func(t *testing.T) {
			source := endedCodexSource("source", nil)
			child := endedCodexSource("child", nil)
			api := continuityAPI(t, source, child)
			legacy := filepath.Join(api.stateDir, source.ID, "native-history-legacy.json")
			if err := os.WriteFile(legacy, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			cfg := filepath.Join(api.stateDir, child.ID, "shim-launch.json")
			switch kind {
			case "empty":
				_ = os.WriteFile(cfg, []byte(`{}`), 0600)
			case "array":
				_ = os.WriteFile(cfg, []byte(`[]`), 0600)
			case "invalid":
				_ = os.WriteFile(cfg, []byte("invalid"), 0600)
			case "symlink":
				_ = os.Symlink(legacy, cfg)
			}
			if err := api.Delete(source.ID); err == nil {
				t.Fatal("ambiguous dependency allowed source removal")
			}
			if _, err := os.Stat(legacy); err != nil {
				t.Fatal("legacy input removed")
			}
		})
	}
}

func TestHandoffStorageCanonicalConcurrentExportsAndChangedRows(t *testing.T) {
	f := newOpenCodeSendFixture(t)
	source, _ := f.d.core.Get(f.binding.WorkerID)
	var reads atomic.Int32
	f.historyPage.Store(func(int, string) (opencodebridge.Snapshot, error) {
		page := ocHistoryPage(f.identity, "", "")
		if reads.Add(1)%2 == 0 {
			raw, _ := json.Marshal(f.identity.Directory)
			page.Session = json.RawMessage(`{"version":"1.19.3","time":{"created":123},"id":"` + f.identity.RootID + `","directory":` + string(raw) + `}`)
		}
		return page, nil
	})
	results := make(chan string, 8)
	for range 8 {
		go func() {
			resolver := &openCodeHistoryResolver{core: f.d.core}
			path, _ := resolver.LocateTranscript(source, source.ConversationID)
			results <- path
		}()
	}
	first := <-results
	if first == "" {
		t.Fatal("concurrent export failed")
	}
	for range 7 {
		if path := <-results; path != first {
			t.Fatalf("equivalent canonical history duplicated or failed: %q %q", first, path)
		}
	}
	if entries := storageExports(t, f.d.stateDir); len(entries) != 1 {
		t.Fatal("equivalent native JSON duplicated")
	}
	f.historyPage.Store(func(int, string) (opencodebridge.Snapshot, error) {
		return ocHistoryPage(f.identity, "msg_"+strings.Repeat("a", 26), ""), nil
	})
	resolver := &openCodeHistoryResolver{core: f.d.core}
	next, outcome := resolver.LocateTranscript(source, source.ConversationID)
	if outcome != resumeHistoryFound || next == first {
		t.Fatal("changed native rows overwrote previous input")
	}
	if entries := storageExports(t, f.d.stateDir); len(entries) != 2 {
		t.Fatal("distinct history version was not retained")
	}
}

func TestHandoffStoragePartialFamilyFailureRetainsLegacyInput(t *testing.T) {
	source := endedCodexSource("source", nil)
	consumer := endedCodexSource("consumer", nil)
	consumer.ResumedFrom = source.ID
	consumer.CreatedAt = time.Now()
	consumer.Status.Process = status.ProcessExited
	failed := false
	api := continuityAPIWithConfig(t, func(c *daemon.Config) {
		c.PreDelete = func(m persist.Meta) error {
			if m.ID == consumer.ID {
				failed = true
				return errors.New("later consumer removal failed")
			}
			return nil
		}
	}, source, consumer)
	legacy := filepath.Join(api.stateDir, source.ID, "native-history-legacy.json")
	if err := os.WriteFile(legacy, []byte("required immutable input"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, _ := json.Marshal(map[string]any{"session_id": consumer.ID, "argv": []string{"agent", "read " + legacy}})
	if err := os.WriteFile(filepath.Join(api.stateDir, consumer.ID, "shim-launch.json"), cfg, 0600); err != nil {
		t.Fatal(err)
	}
	if err := api.Delete(consumer.ID); err == nil {
		t.Fatal("expected dependency or consumer cleanup refusal")
	}
	if _, ok := api.core.Get(consumer.ID); !ok {
		t.Fatal("fixture did not retain consumer")
	}
	if raw, err := os.ReadFile(legacy); err != nil || string(raw) != "required immutable input" {
		t.Fatalf("retained consumer lost legacy input (later failure=%v): %v", failed, err)
	}
}

func TestHandoffStorageLegacyAliasWithoutLineageRefusesDeletion(t *testing.T) {
	source := endedCodexSource("source", nil)
	consumer := endedCodexSource("consumer", nil)
	api := continuityAPI(t, source, consumer)
	legacy := filepath.Join(api.stateDir, source.ID, "native-history-legacy.json")
	if err := os.WriteFile(legacy, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(filepath.Join(api.stateDir, source.ID), alias); err != nil {
		t.Fatal(err)
	}
	cfg, _ := json.Marshal(map[string]any{"session_id": consumer.ID, "argv": []string{"agent", "read " + alias + "/./" + filepath.Base(legacy)}})
	if err := os.WriteFile(filepath.Join(api.stateDir, consumer.ID, "shim-launch.json"), cfg, 0600); err != nil {
		t.Fatal(err)
	}
	if err := api.Delete(source.ID); err == nil {
		t.Fatal("alias legacy reference evaded deletion guard")
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatal("aliased legacy input removed")
	}
}

func storageCrashResult(t *testing.T) openCodeHistoryExport {
	t.Helper()
	id := opencodebridge.Identity{Version: 1, WorkerID: "worker", RootID: "ses_" + strings.Repeat("a", 32), Directory: "/native", SessionInstance: strings.Repeat("b", 32), BackendGeneration: strings.Repeat("b", 32), NativeCreatedAtMS: 123}
	page := ocHistoryPage(id, "", "")
	return openCodeHistoryExport{Info: page.Session, Messages: page.Messages, Provenance: openCodeHistoryProvenance{Identity: id, CapturedAt: time.Now()}}
}

func TestHandoffStoragePublicationInterruptedRecovery(t *testing.T) {
	for _, point := range []string{"before-link", "after-link"} {
		t.Run(point, func(t *testing.T) {
			state := t.TempDir()
			result := storageCrashResult(t)
			canonical, err := canonicalHandoffExport(result)
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(canonical)
			dir := filepath.Join(state, handoffExportDirectory)
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			name := "native-history-" + hex.EncodeToString(digest[:]) + ".json"
			pending := filepath.Join(dir, ".pending-"+name)
			raw, _ := json.MarshalIndent(result, "", "  ")
			if err := os.WriteFile(pending, append(raw, '\n'), 0600); err != nil {
				t.Fatal(err)
			}
			if point == "after-link" {
				if err := os.Link(pending, filepath.Join(dir, name)); err != nil {
					t.Fatal(err)
				}
			}
			path, owned, err := publishHandoffExport(state, result)
			if err != nil {
				t.Fatalf("restart could not recover %s crash: %v", point, err)
			}
			if owned != nil {
				_ = owned.root.Close()
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatal(err)
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 1 {
				t.Fatalf("interrupted staging accumulated after recovery: %d", len(entries))
			}
		})
	}
}

func TestHandoffStoragePublicationCrashHelper(t *testing.T) {
	state := os.Getenv("SWARM_STORAGE_CRASH_STATE")
	if state == "" {
		return
	}
	point := os.Getenv("SWARM_STORAGE_CRASH_POINT")
	_, _, err := publishHandoffExportWithProbe(state, storageCrashResult(t), func(at string) error {
		if at == point {
			os.Exit(79)
		}
		return nil
	})
	t.Fatalf("crash boundary not reached: %v", err)
}

func TestHandoffStoragePublicationRealCrashAndUnlinkFailure(t *testing.T) {
	for _, point := range []string{"before-link", "after-link"} {
		t.Run(point, func(t *testing.T) {
			state := t.TempDir()
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(exe, "-test.run=^TestHandoffStoragePublicationCrashHelper$")
			cmd.Env = append(os.Environ(), "SWARM_STORAGE_CRASH_STATE="+state, "SWARM_STORAGE_CRASH_POINT="+point)
			raw, err := cmd.CombinedOutput()
			var exited *exec.ExitError
			if !errors.As(err, &exited) || exited.ExitCode() != 79 {
				t.Fatalf("crash helper: %s %v", raw, err)
			}
			result := storageCrashResult(t)
			for range 3 {
				path, owned, err := publishHandoffExport(state, result)
				if err != nil {
					t.Fatalf("recovery after real %s crash: %v", point, err)
				}
				if owned != nil {
					_ = owned.root.Close()
				}
				content, err := os.ReadFile(path)
				if err != nil || !json.Valid(content) {
					t.Fatalf("recovered input unreadable: %v", err)
				}
			}
			if entries := storageExports(t, state); len(entries) != 1 {
				t.Fatalf("recovery retained staging: %d", len(entries))
			}
		})
	}
	t.Run("unlink-error", func(t *testing.T) {
		state := t.TempDir()
		result := storageCrashResult(t)
		injected := errors.New("injected staging unlink failure")
		_, owned, err := publishHandoffExportWithProbe(state, result, func(point string) error {
			if point == "after-link" {
				return injected
			}
			return nil
		})
		if owned != nil || !errors.Is(err, injected) {
			t.Fatalf("unlink fault lost: %v", err)
		}
		entries := storageExports(t, state)
		if len(entries) != 2 {
			t.Fatal("fault did not retain both publication links")
		}
		path, owned, err := publishHandoffExport(state, result)
		if err != nil {
			t.Fatal(err)
		}
		if owned != nil {
			t.Fatal("recovered published input incorrectly grants cleanup ownership")
		}
		info, err := os.Stat(path)
		if err != nil || !safeHandoffFile(info) {
			t.Fatal("published input remained poisoned")
		}
		if entries := storageExports(t, state); len(entries) != 1 {
			t.Fatal("unlink retry leaked staging")
		}
	})
}

func TestHandoffStorageRecoveryRetainsUnprovenStaging(t *testing.T) {
	for _, kind := range []string{"contents", "symlink", "foreign-link"} {
		t.Run(kind, func(t *testing.T) {
			state := t.TempDir()
			result := storageCrashResult(t)
			canonical, _ := canonicalHandoffExport(result)
			digest := sha256.Sum256(canonical)
			dir := filepath.Join(state, handoffExportDirectory)
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			name := "native-history-" + hex.EncodeToString(digest[:]) + ".json"
			pending := filepath.Join(dir, ".pending-"+name)
			raw, _ := json.MarshalIndent(result, "", "  ")
			switch kind {
			case "contents":
				raw = []byte("unproven partial staging")
				if err := os.WriteFile(pending, raw, 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				outside := filepath.Join(t.TempDir(), "keep")
				if err := os.WriteFile(outside, raw, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, pending); err != nil {
					t.Fatal(err)
				}
			case "foreign-link":
				if err := os.WriteFile(pending, raw, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(pending, filepath.Join(t.TempDir(), "keep")); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := publishHandoffExport(state, result); err == nil {
				t.Fatal("unproven staging was adopted")
			}
			after, err := os.ReadFile(pending)
			if err != nil || !bytes.Equal(raw, after) {
				t.Fatal("unproven staging was changed or removed")
			}
		})
	}
}

func TestHandoffStorageStagingCleanupFailureIsVisible(t *testing.T) {
	state := t.TempDir()
	result := storageCrashResult(t)
	injected := errors.New("prepublication failure")
	_, _, err := publishHandoffExportWithProbe(state, result, func(point string) error {
		if point != "before-link" {
			return nil
		}
		entries := storageExports(t, state)
		if len(entries) != 1 {
			t.Fatal("expected exactly one owned staging file")
		}
		if err := os.Chmod(filepath.Join(state, handoffExportDirectory, entries[0].Name()), 0644); err != nil {
			t.Fatal(err)
		}
		return injected
	})
	if !errors.Is(err, injected) || !strings.Contains(err.Error(), "staging cleanup failed") {
		t.Fatalf("cleanup failure hidden: %v", err)
	}
	if entries := storageExports(t, state); len(entries) != 1 {
		t.Fatal("changed staging file removed without ownership proof")
	}
}

func TestHandoffStorageRecoveredDurabilityBarriers(t *testing.T) {
	for _, crash := range []string{"after-write", "after-unlink"} {
		for _, barrier := range []string{"file-sync", "directory-sync"} {
			t.Run(crash+"/"+barrier, func(t *testing.T) {
				state := t.TempDir()
				exe, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command(exe, "-test.run=^TestHandoffStoragePublicationCrashHelper$")
				cmd.Env = append(os.Environ(), "SWARM_STORAGE_CRASH_STATE="+state, "SWARM_STORAGE_CRASH_POINT="+crash)
				output, err := cmd.CombinedOutput()
				var exited *exec.ExitError
				if !errors.As(err, &exited) || exited.ExitCode() != 79 {
					t.Fatalf("crash helper: %s %v", output, err)
				}
				injected := errors.New("injected " + barrier + " failure")
				result := storageCrashResult(t)
				path, owned, err := publishHandoffExportWithProbe(state, result, func(point string) error {
					if point == barrier {
						return injected
					}
					return nil
				})
				if owned != nil {
					_ = owned.root.Close()
				}
				if !errors.Is(err, injected) || path != "" || owned != nil {
					t.Fatalf("recovered input bypassed failed %s: %q %v", barrier, path, err)
				}
				counts := map[string]int{}
				path, owned, err = publishHandoffExportWithProbe(state, result, func(point string) error { counts[point]++; return nil })
				if owned != nil {
					_ = owned.root.Close()
				}
				if err != nil || path == "" || counts["file-sync"] == 0 || counts["directory-sync"] == 0 {
					t.Fatalf("retry omitted durability barriers: %v %v", counts, err)
				}
				if entries := storageExports(t, state); len(entries) != 1 {
					t.Fatal("barrier retry retained staging")
				}
			})
		}
	}
}

func TestHandoffStoragePublicationFailureDiagnostic(t *testing.T) {
	f := newOpenCodeSendFixture(t)
	source, _ := f.d.core.Get(f.binding.WorkerID)
	resolver := &openCodeHistoryResolver{core: f.d.core}
	path, outcome := resolver.LocateTranscript(source, source.ConversationID)
	if outcome != resumeHistoryFound {
		t.Fatal("initial export failed")
	}
	pending := filepath.Join(filepath.Dir(path), ".pending-"+filepath.Base(path))
	secret := "private history body must not appear in logs"
	if err := os.WriteFile(pending, []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.CreateTemp(t.TempDir(), "diagnostic")
	if err != nil {
		t.Fatal(err)
	}
	previous := log.Writer()
	log.SetOutput(file)
	defer func() { log.SetOutput(previous); _ = file.Close() }()
	if path, outcome := resolver.LocateTranscript(source, source.ConversationID); path != "" || outcome != resumeHistoryUnreadable {
		t.Fatal("unproven staging did not fail closed")
	}
	output, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(output, []byte(filepath.Base(path))) || !bytes.Contains(output, []byte(filepath.Dir(path))) || !bytes.Contains(output, []byte("invalid existing export")) {
		t.Fatalf("publication diagnostic lacks owned location or cause: %s", output)
	}
	if bytes.Contains(output, []byte(secret)) {
		t.Fatal("diagnostic exposed history body")
	}
}
