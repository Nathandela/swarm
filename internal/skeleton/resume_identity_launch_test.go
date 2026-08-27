package skeleton

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

const migratedConversationID = "01a00339-a80e-72a0-966f-116427b6b9ce"

type resumeAPIRig struct {
	stateDir string
	sourceID string
	core     *daemon.Daemon
	api      *coreAPI
}

func newResumeAPIRig(t *testing.T, agent, conversationID string, resolver resumeHistoryResolver) *resumeAPIRig {
	return newResumeAPIRigWithProcess(t, agent, conversationID, status.ProcessExited, resolver)
}

func newResumeAPIRigWithProcess(t *testing.T, agent, conversationID string, process status.Process, resolver resumeHistoryResolver) *resumeAPIRig {
	t.Helper()
	stateDir, err := os.MkdirTemp("/tmp", "swra-")
	if err != nil {
		t.Fatalf("state dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(stateDir) })
	store, err := persist.NewStore(stateDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	const sourceID = "resume-api-source"
	now := time.Now().UTC()
	if err := store.Save(persist.Meta{
		ID:             sourceID,
		AgentType:      agent,
		ConversationID: conversationID,
		Cwd:            filepath.Join(stateDir, "work"),
		CreatedAt:      now,
		LastActivity:   now,
		Status:         status.Status{Process: process},
	}); err != nil {
		t.Fatalf("seed source meta: %v", err)
	}
	core, err := daemon.Open(daemon.Config{
		StateDir:    stateDir,
		SocketPath:  filepath.Join(stateDir, "daemon.sock"),
		LockPath:    filepath.Join(stateDir, "daemon.lock"),
		LogPath:     filepath.Join(stateDir, "daemon.log"),
		MaxSessions: 16,
	})
	if err != nil {
		t.Fatalf("daemon.Open: %v", err)
	}
	r := &resumeAPIRig{
		stateDir: stateDir,
		sourceID: sourceID,
		core:     core,
		api: &coreAPI{
			core:            core,
			endpointID:      testEndpoint,
			historyResolver: resolver,
		},
	}
	t.Cleanup(func() { _ = core.Close() })
	return r
}

func (r *resumeAPIRig) launchSpec(agent string) daemon.LaunchSpec {
	return daemon.LaunchSpec{
		AgentType: agent,
		Cwd:       filepath.Join(r.stateDir, "new-work"),
		// A deterministic missing PATH forces composition to stop after any migration, before
		// the daemon can spawn a shim or provider process.
		ClientEnv: []string{"PATH=/definitely/no-provider-binaries-here"},
		Options: map[string]string{
			protocol.OptionResumeFrom: protocol.NamespacedID(testEndpoint, r.sourceID),
		},
	}
}

func (r *resumeAPIRig) meta(t *testing.T) persist.Meta {
	t.Helper()
	m, ok := r.core.Get(r.sourceID)
	if !ok {
		t.Fatalf("source %q disappeared", r.sourceID)
	}
	return m
}

type fakeResumeHistoryResolver struct {
	mu      sync.Mutex
	result  resumeHistoryResult
	calls   int
	entered chan struct{}
	release <-chan struct{}
	once    sync.Once
}

type perSourceBlockingResolver struct {
	entered  chan string
	releases map[string]chan struct{}
	results  map[string]resumeHistoryResult
}

func TestExternalResumeIsIdempotentByProviderIdentity(t *testing.T) {
	rig := newResumeAPIRig(t, "claude", migratedConversationID, nil)
	spec := daemon.LaunchSpec{
		AgentType: "claude",
		Cwd:       filepath.Join(rig.stateDir, "new-work"),
		ClientEnv: []string{"PATH=/definitely/no-provider-binaries-here"},
		Options: map[string]string{
			protocol.OptionResumeConversationID: migratedConversationID,
		},
	}
	for i := 0; i < 2; i++ {
		got, err := rig.api.Launch(spec)
		if err != nil {
			t.Fatalf("Launch %d: %v", i+1, err)
		}
		if got.ID != rig.sourceID {
			t.Fatalf("Launch %d returned %q, want existing %q", i+1, got.ID, rig.sourceID)
		}
	}
	if got := len(rig.core.List()); got != 1 {
		t.Fatalf("roster size = %d, want one idempotently reused session", got)
	}
}

func (r *perSourceBlockingResolver) Resolve(m persist.Meta) resumeHistoryResult {
	r.entered <- m.ID
	<-r.releases[m.ID]
	return r.results[m.ID]
}

// Both fakes exist to control the RECOVERY outcome and nothing else. They locate no
// transcript, which is the fail-closed answer: a hands-off handoff composed against
// one of these rigs refuses by name rather than pointing a successor at a path no
// resolver stood behind.
func (r *perSourceBlockingResolver) LocateTranscript(persist.Meta, string) (string, resumeHistoryOutcome) {
	return "", resumeHistoryUnsupported
}

func (f *fakeResumeHistoryResolver) LocateTranscript(persist.Meta, string) (string, resumeHistoryOutcome) {
	return "", resumeHistoryUnsupported
}

func (f *fakeResumeHistoryResolver) Resolve(persist.Meta) resumeHistoryResult {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.entered != nil {
		f.once.Do(func() { close(f.entered) })
	}
	if f.release != nil {
		<-f.release
	}
	return f.result
}

func (f *fakeResumeHistoryResolver) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func requireStopsBeforeProviderSpawn(t *testing.T, r *resumeAPIRig, err error, before int) {
	t.Helper()
	if err == nil {
		t.Fatal("resume unexpectedly succeeded with no provider binary")
	}
	if got := len(r.core.List()); got != before {
		t.Fatalf("roster grew from %d to %d after rejected resume; migration must never fall through to a fresh launch", before, got)
	}
}

func TestResumeMigration_ExistingIDBypassesHistoryResolver(t *testing.T) {
	resolver := &fakeResumeHistoryResolver{result: resumeHistoryResult{Outcome: resumeHistoryAmbiguous}}
	r := newResumeAPIRig(t, "codex", migratedConversationID, resolver)
	before := len(r.core.List())
	_, err := r.api.Launch(r.launchSpec("codex"))
	requireStopsBeforeProviderSpawn(t, r, err, before)
	if resolver.callCount() != 0 {
		t.Fatalf("resolver called %d time(s) for an already-migrated source", resolver.callCount())
	}
}

// TestResumeMigration_PreexistingConversationIDMustBeCanonical hardens legacy rows written by
// the former permissive terminal extractors. A crafted nonempty token must not be interpreted as
// a provider resume argument (Claude's optional --resume value is especially sensitive). The
// API refuses it generically before history, argv resolution, or spawn and leaves the old row
// untouched for explicit operator repair.
func TestResumeMigration_PreexistingConversationIDMustBeCanonical(t *testing.T) {
	for _, tc := range []struct {
		agent     string
		invalidID string
		canonical string
	}{
		{"codex", "legacy-codex-token-do-not-leak", migratedConversationID},
		{"claude", "--permission-mode=unsafe-do-not-leak", legacyClaudeID},
	} {
		t.Run(tc.agent+" rejects noncanonical", func(t *testing.T) {
			resolver := &fakeResumeHistoryResolver{result: resumeHistoryResult{
				Outcome: resumeHistoryFound, ConversationID: tc.canonical,
			}}
			r := newResumeAPIRig(t, tc.agent, tc.invalidID, resolver)
			before := len(r.core.List())
			beforeMeta := r.meta(t)
			_, err := r.api.Launch(r.launchSpec(tc.agent))
			requireStopsBeforeProviderSpawn(t, r, err, before)
			lower := strings.ToLower(err.Error())
			if !strings.Contains(lower, "resume") || !strings.Contains(lower, "invalid") {
				t.Fatalf("noncanonical stored-id error = %v, want generic invalid-resume refusal", err)
			}
			if strings.Contains(lower, "binary") {
				t.Fatalf("noncanonical stored id reached argv/binary resolution: %v", err)
			}
			if resolver.callCount() != 0 {
				t.Fatalf("noncanonical stored id caused %d history scan(s)", resolver.callCount())
			}
			after := r.meta(t)
			if after.ConversationID != beforeMeta.ConversationID {
				t.Fatalf("noncanonical stored id changed from %q to %q", beforeMeta.ConversationID, after.ConversationID)
			}
			for _, secret := range []string{tc.invalidID, r.stateDir, beforeMeta.Cwd} {
				if secret != "" && strings.Contains(err.Error(), secret) {
					t.Fatalf("noncanonical stored-id error leaked private value %q: %v", secret, err)
				}
			}
		})

		t.Run(tc.agent+" accepts canonical", func(t *testing.T) {
			resolver := &fakeResumeHistoryResolver{result: resumeHistoryResult{Outcome: resumeHistoryAmbiguous}}
			r := newResumeAPIRig(t, tc.agent, tc.canonical, resolver)
			before := len(r.core.List())
			_, err := r.api.Launch(r.launchSpec(tc.agent))
			requireStopsBeforeProviderSpawn(t, r, err, before)
			if !strings.Contains(strings.ToLower(err.Error()), "binary") {
				t.Fatalf("canonical stored id did not reach normal resume argv resolution: %v", err)
			}
			if resolver.callCount() != 0 {
				t.Fatalf("canonical stored id caused %d history scan(s), want bypass", resolver.callCount())
			}
			if got := r.meta(t).ConversationID; got != tc.canonical {
				t.Fatalf("canonical stored id changed to %q, want %q", got, tc.canonical)
			}
		})
	}
}

// TestResumeMigration_UniqueMatchPersistsBeforeComposition proves lazy migration commits at the
// pre-compose seam. The deliberately missing provider binary fails later; that failure must not
// roll back the source identity or stamp a new session.
func TestResumeMigration_UniqueMatchPersistsBeforeComposition(t *testing.T) {
	resolver := &fakeResumeHistoryResolver{result: resumeHistoryResult{
		Outcome: resumeHistoryFound, ConversationID: migratedConversationID,
	}}
	r := newResumeAPIRig(t, "codex", "", resolver)
	before := len(r.core.List())
	_, err := r.api.Launch(r.launchSpec("codex"))
	requireStopsBeforeProviderSpawn(t, r, err, before)
	if !strings.Contains(err.Error(), "binary") {
		t.Fatalf("post-migration failure = %v, want the later provider-binary resolution failure", err)
	}
	if got := r.meta(t).ConversationID; got != migratedConversationID {
		t.Fatalf("ConversationID after later compose failure = %q, want migrated %q", got, migratedConversationID)
	}
	if resolver.callCount() != 1 {
		t.Fatalf("resolver calls = %d, want 1", resolver.callCount())
	}
}

func TestResumeMigration_FailureOutcomesNeverLaunchFresh(t *testing.T) {
	for _, tc := range []struct {
		outcome resumeHistoryOutcome
		words   []string
	}{
		{resumeHistoryNoMatch, []string{"codex", "matching"}},
		{resumeHistoryAmbiguous, []string{"codex", "multiple"}},
		{resumeHistoryUnsafe, []string{"codex", "unsafe"}},
		{resumeHistoryUnreadable, []string{"codex", "read"}},
	} {
		t.Run(fmt.Sprint(tc.outcome), func(t *testing.T) {
			resolver := &fakeResumeHistoryResolver{result: resumeHistoryResult{Outcome: tc.outcome}}
			r := newResumeAPIRig(t, "codex", "", resolver)
			before := len(r.core.List())
			_, err := r.api.Launch(r.launchSpec("codex"))
			requireStopsBeforeProviderSpawn(t, r, err, before)
			lower := strings.ToLower(err.Error())
			for _, word := range tc.words {
				if !strings.Contains(lower, word) {
					t.Errorf("%v error %q omits actionable word %q", tc.outcome, err, word)
				}
			}
			if got := r.meta(t).ConversationID; got != "" {
				t.Fatalf("failure outcome %v stored ConversationID %q", tc.outcome, got)
			}
			for _, secret := range []string{r.stateDir, r.meta(t).Cwd, migratedConversationID} {
				if secret != "" && strings.Contains(err.Error(), secret) {
					t.Errorf("client-facing recovery error leaked private path/id %q: %v", secret, err)
				}
			}
		})
	}
}

func TestResumeMigration_ValidatesSourceBeforeScanningHome(t *testing.T) {
	tests := []struct {
		name    string
		process status.Process
		mutate  func(*resumeAPIRig, *daemon.LaunchSpec)
	}{
		{"malformed id", status.ProcessExited, func(_ *resumeAPIRig, spec *daemon.LaunchSpec) {
			spec.Options[protocol.OptionResumeFrom] = "not-namespaced"
		}},
		{"foreign endpoint", status.ProcessExited, func(r *resumeAPIRig, spec *daemon.LaunchSpec) {
			spec.Options[protocol.OptionResumeFrom] = protocol.NamespacedID("another-endpoint", r.sourceID)
		}},
		{"source not found", status.ProcessExited, func(_ *resumeAPIRig, spec *daemon.LaunchSpec) {
			spec.Options[protocol.OptionResumeFrom] = protocol.NamespacedID(testEndpoint, "missing-source")
		}},
		{"agent mismatch", status.ProcessExited, func(_ *resumeAPIRig, spec *daemon.LaunchSpec) {
			spec.AgentType = "claude"
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resolver := &fakeResumeHistoryResolver{result: resumeHistoryResult{
				Outcome: resumeHistoryFound, ConversationID: migratedConversationID,
			}}
			r := newResumeAPIRigWithProcess(t, "codex", "", tc.process, resolver)
			spec := r.launchSpec("codex")
			tc.mutate(r, &spec)
			before := len(r.core.List())
			_, err := r.api.Launch(spec)
			requireStopsBeforeProviderSpawn(t, r, err, before)
			if resolver.callCount() != 0 {
				t.Fatalf("invalid source caused %d history scan(s); validation must precede HOME access", resolver.callCount())
			}
		})
	}
}

func TestResumeMigration_RunningSourceRejectedBeforeScanningHome(t *testing.T) {
	buildBinaries(t)
	stateDir, err := os.MkdirTemp("/tmp", "swra-running-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(stateDir) })
	// Bare daemon: BackendPlanner is intentionally nil. AgentType remains genuinely Codex in
	// metadata, while the explicit argv is the non-billable fake agent. No code path can resolve
	// or spawn a real codex/app-server binary.
	core, err := daemon.Open(daemon.Config{
		StateDir: stateDir, SocketPath: filepath.Join(stateDir, "daemon.sock"),
		LockPath: filepath.Join(stateDir, "daemon.lock"), LogPath: filepath.Join(stateDir, "daemon.log"),
		ShimBinary: swarmBin, MaxSessions: 16,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = core.Close() })
	script := filepath.Join(t.TempDir(), "running-source.script")
	if err := os.WriteFile(script, []byte("print RUNNING-RESUME-SOURCE\nidle 60s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := core.Launch(daemon.LaunchSpec{
		AgentType: "codex",
		Argv:      []string{fakeAgentBin, script},
		Cwd:       t.TempDir(),
		ClientEnv: []string{"PATH=" + os.Getenv("PATH")},
		Cols:      80,
		Rows:      24,
	})
	if err != nil {
		t.Fatalf("launch genuinely running Codex-shaped source: %v", err)
	}
	t.Cleanup(func() {
		if m.ShimPID > 0 {
			_ = syscall.Kill(m.ShimPID, syscall.SIGTERM)
		}
	})
	if current, ok := core.Get(m.ID); !ok || current.Status.Process != status.ProcessRunning {
		t.Fatalf("test precondition: source status = %+v (ok=%v), want genuinely running", current.Status, ok)
	}
	resolver := &fakeResumeHistoryResolver{result: resumeHistoryResult{
		Outcome: resumeHistoryFound, ConversationID: migratedConversationID,
	}}
	api := &coreAPI{core: core, endpointID: testEndpoint, historyResolver: resolver}
	spec := daemon.LaunchSpec{
		AgentType: "codex",
		Cwd:       t.TempDir(),
		ClientEnv: []string{"PATH=/definitely/no-provider-binaries-here"},
		Options: map[string]string{
			protocol.OptionResumeFrom: protocol.NamespacedID(testEndpoint, m.ID),
		},
	}
	before := len(core.List())
	if _, err := api.Launch(spec); err == nil {
		t.Fatal("running source was accepted for resume")
	} else if !strings.Contains(strings.ToLower(err.Error()), "running") {
		t.Fatalf("running-source error = %v, want actionable running refusal", err)
	}
	if resolver.callCount() != 0 {
		t.Fatalf("running source caused %d history scan(s); validation must precede HOME access", resolver.callCount())
	}
	if got := len(core.List()); got != before {
		t.Fatalf("roster grew from %d to %d after running-source refusal", before, got)
	}
}

func TestResumeMigration_InvalidResolverResultsNeverPersistOrLaunch(t *testing.T) {
	for _, result := range []resumeHistoryResult{
		{Outcome: resumeHistoryFound, ConversationID: ""},
		{Outcome: resumeHistoryFound, ConversationID: "not-a-canonical-uuid"},
		{Outcome: resumeHistoryFound, ConversationID: "01A00339-A80E-72A0-966F-116427B6B9CE"},
		{Outcome: resumeHistoryOutcome(255), ConversationID: migratedConversationID},
	} {
		t.Run(fmt.Sprintf("%v-%s", result.Outcome, result.ConversationID), func(t *testing.T) {
			resolver := &fakeResumeHistoryResolver{result: result}
			r := newResumeAPIRig(t, "codex", "", resolver)
			before := len(r.core.List())
			_, err := r.api.Launch(r.launchSpec("codex"))
			requireStopsBeforeProviderSpawn(t, r, err, before)
			if got := r.meta(t).ConversationID; got != "" {
				t.Fatalf("invalid resolver result stored ConversationID %q", got)
			}
		})
	}
}

func TestResumeMigration_ClaudeUsesTheSamePreComposeMigration(t *testing.T) {
	resolver := &fakeResumeHistoryResolver{result: resumeHistoryResult{
		Outcome: resumeHistoryFound, ConversationID: legacyClaudeID,
	}}
	r := newResumeAPIRig(t, "claude", "", resolver)
	before := len(r.core.List())
	_, err := r.api.Launch(r.launchSpec("claude"))
	requireStopsBeforeProviderSpawn(t, r, err, before)
	if !strings.Contains(err.Error(), "binary") {
		t.Fatalf("Claude post-migration error = %v, want later binary failure", err)
	}
	if got := r.meta(t).ConversationID; got != legacyClaudeID {
		t.Fatalf("Claude migrated ConversationID = %q, want %q", got, legacyClaudeID)
	}
}

// TestResumeMigration_AssemblyWiresTrustedHomeFilesystemResolver proves the VM path, not only
// injected coreAPI unit tests. Serve must derive/wire the real resolver from the trusted user
// home and default limits. Merely placing history there is read-only; metadata changes lazily
// only when the owner explicitly resumes this ended source.
func TestResumeMigration_AssemblyWiresTrustedHomeFilesystemResolver(t *testing.T) {
	buildBinaries(t)
	codexBin := buildFakeCodex(t)
	for _, tc := range []struct {
		name     string
		override bool
	}{
		{"explicit trusted-home override", true},
		{"empty override uses controlled HOME", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			historyHome := t.TempDir()
			if tc.override {
				t.Setenv("HOME", t.TempDir()) // decoy: explicit trusted home must win
			} else {
				t.Setenv("HOME", historyHome) // os.UserHomeDir's controlled production input
			}
			t.Setenv("PATH", filepath.Dir(codexBin)+string(os.PathListSeparator)+os.Getenv("PATH"))
			workDir := filepath.Join(historyHome, "work")
			if err := os.MkdirAll(workDir, 0o700); err != nil {
				t.Fatal(err)
			}
			created := legacyCreatedAt
			const sourceID = "assembly-history-source"
			writeCodexHistory(t, historyHome, legacyCodexRootID, workDir, created.Add(time.Second), "", "cli", "")

			sk := assemble(t, func(cfg *Config) {
				if tc.override {
					cfg.historyHome = historyHome
				}
				store, err := persist.NewStore(cfg.StateDir)
				if err != nil {
					t.Fatalf("assembly seed store: %v", err)
				}
				if err := store.Save(persist.Meta{
					ID: sourceID, AgentType: "codex", Cwd: workDir,
					CreatedAt: created, LastActivity: created,
					Status: status.Status{Process: status.ProcessExited},
				}); err != nil {
					t.Fatalf("assembly seed source: %v", err)
				}
			})
			m, ok := sk.Core().Get(sourceID)
			if !ok {
				t.Fatal("assembly lost seeded source")
			}
			if m.ConversationID != "" {
				t.Fatalf("Serve passively migrated source before explicit resume: %q", m.ConversationID)
			}

			before := len(sk.Core().List())
			_, err := sk.api.Launch(daemon.LaunchSpec{
				AgentType: "codex",
				Cwd:       workDir,
				ClientEnv: []string{"PATH=/definitely/no-provider-binaries-here"},
				Options: map[string]string{
					protocol.OptionResumeFrom: protocol.NamespacedID(sk.api.endpointID, sourceID),
				},
			})
			if err == nil || !strings.Contains(err.Error(), "binary") {
				t.Fatalf("explicit resume error = %v, want post-migration binary failure", err)
			}
			if got := len(sk.Core().List()); got != before {
				t.Fatalf("failed provider spawn changed roster from %d to %d", before, got)
			}
			if current, _ := sk.Core().Get(sourceID); current.ConversationID != legacyCodexRootID {
				t.Fatalf("assembly-wired resolver persisted %q, want %q", current.ConversationID, legacyCodexRootID)
			}
		})
	}
}

func TestResumeMigration_PersistenceFailureNeverLaunches(t *testing.T) {
	resolver := &fakeResumeHistoryResolver{result: resumeHistoryResult{
		Outcome: resumeHistoryFound, ConversationID: migratedConversationID,
	}}
	r := newResumeAPIRig(t, "codex", "", resolver)
	sessionDir := filepath.Join(r.stateDir, r.sourceID)
	hold := sessionDir + ".hold"
	if err := os.Rename(sessionDir, hold); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sessionDir, []byte("blocks Store.Save"), 0o600); err != nil {
		t.Fatal(err)
	}
	restored := false
	t.Cleanup(func() {
		if !restored {
			_ = os.Remove(sessionDir)
			_ = os.Rename(hold, sessionDir)
		}
	})

	before := len(r.core.List())
	_, err := r.api.Launch(r.launchSpec("codex"))
	requireStopsBeforeProviderSpawn(t, r, err, before)
	if strings.Contains(err.Error(), r.stateDir) || strings.Contains(err.Error(), sessionDir) {
		t.Fatalf("persistence failure leaked a private metadata path: %v", err)
	}
	if got := r.meta(t).ConversationID; got != "" {
		t.Fatalf("failed persistence left in-memory ConversationID %q", got)
	}
	if err := os.Remove(sessionDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(hold, sessionDir); err != nil {
		t.Fatal(err)
	}
	restored = true
}

func TestResumeMigration_ConcurrentRequestsConvergeOnOneResolution(t *testing.T) {
	resolver := &fakeResumeHistoryResolver{result: resumeHistoryResult{
		Outcome: resumeHistoryFound, ConversationID: migratedConversationID,
	}}
	r := newResumeAPIRig(t, "codex", "", resolver)
	before := len(r.core.List())
	const workers = 12
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := r.api.Launch(r.launchSpec("codex"))
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err == nil || !strings.Contains(err.Error(), "binary") {
			t.Errorf("concurrent launch error = %v, want post-migration binary failure", err)
		}
	}
	if resolver.callCount() != 1 {
		t.Fatalf("resolver calls = %d, want exactly 1 for one missing-ID source", resolver.callCount())
	}
	if got := r.meta(t).ConversationID; got != migratedConversationID {
		t.Fatalf("stored ConversationID = %q, want %q", got, migratedConversationID)
	}
	if got := len(r.core.List()); got != before {
		t.Fatalf("roster grew from %d to %d", before, got)
	}
}

// TestResumeMigration_DifferentSourcesRecoverInParallel prevents recoveryMu from becoming a
// machine-wide stop-the-world lock. Same-source requests singleflight; two independent source
// IDs must both enter their scans before either scan is released.
func TestResumeMigration_DifferentSourcesRecoverInParallel(t *testing.T) {
	r := newResumeAPIRig(t, "codex", "", nil)
	if err := r.core.Close(); err != nil {
		t.Fatalf("close before adding second fixture: %v", err)
	}
	const secondID = "resume-api-source-two"
	store, err := persist.NewStore(r.stateDir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := store.Save(persist.Meta{
		ID: secondID, AgentType: "codex", Cwd: filepath.Join(r.stateDir, "work-two"),
		CreatedAt: now, LastActivity: now, Status: status.Status{Process: status.ProcessExited},
	}); err != nil {
		t.Fatal(err)
	}
	core, err := daemon.Open(daemon.Config{
		StateDir: r.stateDir, SocketPath: filepath.Join(r.stateDir, "daemon.sock"),
		LockPath: filepath.Join(r.stateDir, "daemon.lock"), LogPath: filepath.Join(r.stateDir, "daemon.log"),
		MaxSessions: 16,
	})
	if err != nil {
		t.Fatalf("reopen with two sources: %v", err)
	}
	t.Cleanup(func() { _ = core.Close() })
	r.core = core
	r.api.core = core
	resolver := &perSourceBlockingResolver{
		entered: make(chan string, 2),
		releases: map[string]chan struct{}{
			r.sourceID: make(chan struct{}, 1),
			secondID:   make(chan struct{}, 1),
		},
		results: map[string]resumeHistoryResult{
			r.sourceID: {Outcome: resumeHistoryFound, ConversationID: legacyCodexRootID},
			secondID:   {Outcome: resumeHistoryFound, ConversationID: legacyCodexOtherID},
		},
	}
	r.api.historyResolver = resolver
	makeSpec := func(source string) daemon.LaunchSpec {
		spec := r.launchSpec("codex")
		spec.Options[protocol.OptionResumeFrom] = protocol.NamespacedID(testEndpoint, source)
		return spec
	}
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, source := range []string{r.sourceID, secondID} {
		source := source
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.api.Launch(makeSpec(source))
			errs <- err
		}()
	}
	seen := map[string]bool{}
	timer := time.NewTimer(2 * time.Second)
	timedOut := false
	for len(seen) < 2 && !timedOut {
		select {
		case id := <-resolver.entered:
			seen[id] = true
		case <-timer.C:
			timedOut = true
		}
	}
	if !timer.Stop() && !timedOut {
		<-timer.C
	}
	resolver.releases[r.sourceID] <- struct{}{}
	resolver.releases[secondID] <- struct{}{}
	wg.Wait()
	close(errs)
	if timedOut {
		t.Fatalf("only sources %v entered before release; recovery is globally serialized instead of per-source", seen)
	}
	for err := range errs {
		if err == nil || !strings.Contains(err.Error(), "binary") {
			t.Errorf("parallel recovery error = %v, want later binary failure", err)
		}
	}
	for source, want := range map[string]string{r.sourceID: legacyCodexRootID, secondID: legacyCodexOtherID} {
		if m, ok := r.core.Get(source); !ok || m.ConversationID != want {
			t.Errorf("source %s ConversationID = %q (ok=%v), want %q", source, m.ConversationID, ok, want)
		}
	}
}

// TestResumeMigration_AuthoritativeCaptureWinsWhileScanReturnsFailure covers the callback/scan
// race. After any resolver outcome the API refetches metadata; a hook or backend capture that
// won during the scan is authoritative and lets composition continue.
func TestResumeMigration_AuthoritativeCaptureWinsWhileScanReturnsFailure(t *testing.T) {
	for _, outcome := range []resumeHistoryOutcome{resumeHistoryNoMatch, resumeHistoryAmbiguous, resumeHistoryUnsafe, resumeHistoryUnreadable} {
		t.Run(fmt.Sprint(outcome), func(t *testing.T) {
			entered := make(chan struct{})
			release := make(chan struct{})
			resolver := &fakeResumeHistoryResolver{
				result: resumeHistoryResult{Outcome: outcome}, entered: entered, release: release,
			}
			r := newResumeAPIRig(t, "codex", "", resolver)
			before := len(r.core.List())
			done := make(chan error, 1)
			go func() {
				_, err := r.api.Launch(r.launchSpec("codex"))
				done <- err
			}()
			<-entered
			if err := r.core.SetConversationID(r.sourceID, migratedConversationID); err != nil {
				t.Fatalf("authoritative capture: %v", err)
			}
			close(release)
			err := <-done
			requireStopsBeforeProviderSpawn(t, r, err, before)
			if !strings.Contains(err.Error(), "binary") {
				t.Fatalf("hook-wins error = %v, want later binary failure rather than stale %v outcome", err, outcome)
			}
			if got := r.meta(t).ConversationID; got != migratedConversationID {
				t.Fatalf("authoritative winner = %q, want %q", got, migratedConversationID)
			}
		})
	}
}

// TestResumeMigration_AuthoritativeCaptureBetweenRefetchAndPersistWins pins the narrowest
// recovery race. beforeRecoveryPersist runs after Resolve and the recovery path's empty-meta
// refetch, immediately before SetConversationID(A). An authenticated producer stores B there;
// write-once plus a mandatory final refetch must compose B, never stale resolver result A.
func TestResumeMigration_AuthoritativeCaptureBetweenRefetchAndPersistWins(t *testing.T) {
	const resolverA = legacyCodexRootID
	const authoritativeB = migratedConversationID
	resolver := &fakeResumeHistoryResolver{result: resumeHistoryResult{
		Outcome: resumeHistoryFound, ConversationID: resolverA,
	}}
	r := newResumeAPIRig(t, "codex", "", resolver)
	called := 0
	r.api.beforeRecoveryPersist = func() {
		called++
		if err := r.core.SetConversationID(r.sourceID, authoritativeB); err != nil {
			t.Fatalf("authoritative capture at pre-persist boundary: %v", err)
		}
	}
	before := len(r.core.List())
	_, err := r.api.Launch(r.launchSpec("codex"))
	requireStopsBeforeProviderSpawn(t, r, err, before)
	if called != 1 {
		t.Fatalf("beforeRecoveryPersist calls = %d, want 1", called)
	}
	if !strings.Contains(err.Error(), "binary") {
		t.Fatalf("pre-persist race error = %v, want later composition failure with authoritative B", err)
	}
	if got := r.meta(t).ConversationID; got != authoritativeB {
		t.Fatalf("resolver A=%q replaced authoritative B; final stored id = %q", resolverA, got)
	}
}

func TestResumeMigration_AuthoritativeDifferentIDWinsWhileScanReturnsFound(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	resolver := &fakeResumeHistoryResolver{
		result:  resumeHistoryResult{Outcome: resumeHistoryFound, ConversationID: legacyCodexRootID},
		entered: entered,
		release: release,
	}
	r := newResumeAPIRig(t, "codex", "", resolver)
	before := len(r.core.List())
	done := make(chan error, 1)
	go func() {
		_, err := r.api.Launch(r.launchSpec("codex"))
		done <- err
	}()
	<-entered
	if err := r.core.SetConversationID(r.sourceID, migratedConversationID); err != nil {
		t.Fatalf("authoritative capture: %v", err)
	}
	close(release)
	err := <-done
	requireStopsBeforeProviderSpawn(t, r, err, before)
	if !strings.Contains(err.Error(), "binary") {
		t.Fatalf("found-race error = %v, want composition with authoritative winner", err)
	}
	if got := r.meta(t).ConversationID; got != migratedConversationID {
		t.Fatalf("resolver result replaced authoritative id with %q; want %q", got, migratedConversationID)
	}
}

func TestResumeMigration_UnsupportedProvidersKeepExistingNoCapturedIDRefusal(t *testing.T) {
	for _, agent := range []string{"opencode", "agy"} {
		t.Run(agent, func(t *testing.T) {
			r := newResumeAPIRig(t, agent, "", nil)
			r.api.historyResolver = newFilesystemResumeHistoryResolver(r.stateDir, generousResumeHistoryLimits())
			before := len(r.core.List())
			_, err := r.api.Launch(r.launchSpec(agent))
			requireStopsBeforeProviderSpawn(t, r, err, before)
			if !strings.Contains(err.Error(), "no captured conversation id") {
				t.Fatalf("unsupported legacy %s resume error = %v, want existing no-captured-id refusal", agent, err)
			}
		})
	}
}
