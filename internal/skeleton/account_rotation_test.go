package skeleton

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/registry"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

func accountTestStore(t *testing.T, count int) (*accounts.Store, string, []accounts.Binding) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := accounts.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	var bindings []accounts.Binding
	for i := 0; i < count; i++ {
		candidate, err := store.CreateCandidate(accounts.ProviderCodex, accounts.KindNative)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]string{"account_id": "synthetic-account-" + fmtUint(uint64(i)), "access_token": "synthetic-access", "refresh_token": "synthetic-refresh"}})
		if err := os.WriteFile(filepath.Join(candidate.ProfilePath, "auth.json"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		candidate, err = store.VerifyCandidate(candidate)
		if err != nil {
			t.Fatal(err)
		}
		registry, _ := store.Snapshot()
		_, account, err := store.Admit(registry.Revision, candidate, "test")
		if err != nil {
			t.Fatal(err)
		}
		binding, err := store.CurrentBinding(account.ID, 1)
		if err != nil {
			t.Fatal(err)
		}
		bindings = append(bindings, binding)
	}
	registry, _ := store.Snapshot()
	if _, err := store.SetEnabled(registry.Revision, accounts.ProviderCodex, true); err != nil {
		t.Fatal(err)
	}
	return store, root, bindings
}

func accountTestRollout(t *testing.T, store *accounts.Store, binding accounts.Binding, cwd, tail string) string {
	t.Helper()
	profile, err := store.HistoryProfilePath(binding)
	if err != nil {
		t.Fatal(err)
	}
	relative := filepath.Join("sessions", legacyCreatedAt.Format("2006/01/02"), "rollout-"+legacyCreatedAt.Format("2006-01-02T15-04-05")+"-"+legacyCodexRootID+".jsonl")
	path := filepath.Join(profile, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	header, _ := json.Marshal(map[string]any{"type": "session_meta", "timestamp": legacyCreatedAt.Format(time.RFC3339Nano), "payload": map[string]any{"id": legacyCodexRootID, "timestamp": legacyCreatedAt.Format(time.RFC3339Nano), "cwd": cwd, "source": "cli", "parent_thread_id": nil}})
	if err := os.WriteFile(path, append(append(header, '\n'), []byte(tail)...), 0o600); err != nil {
		t.Fatal(err)
	}
	return relative
}

func accountTestSource(t *testing.T, root string, binding accounts.Binding) persist.Meta {
	meta := runningCodex("managed-source", binding.Identity, status.TurnIdle, legacyCodexRootID)
	meta.Cwd = root
	meta.Env = []string{"HOME=" + root, "PATH=/usr/bin"}
	meta.AccountBinding = &binding
	// Publish real, legacy projection metadata: history authority must never
	// infer a native context from a missing synthetic reference.
	raw, _ := json.Marshal(map[string]any{"SchemaVersion": 1, "Provider": "codex", "Cwd": root})
	digest := sha256.Sum256(raw)
	meta.AccountProjectionRef = hex.EncodeToString(digest[:])
	dir := filepath.Join(root, "accounts", "configurations", meta.AccountProjectionRef)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "projection.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	meta.LaunchOptions = map[string]string{"model": "exact-model"}
	meta.CLIIdentity = &persist.CLIIdentity{Path: "/abs/synthetic-codex", Version: "0.160.0", Fingerprint: "synthetic"}
	return meta
}

func rotationTestManager(t *testing.T, store *accounts.Store, root string, source persist.Meta) (*accountRotationManager, *authFake) {
	t.Helper()
	f := newAuthFake(source.AuthIdentity)
	f.add(source)
	w := testWatcher(t, f)
	w.stateDir = root
	w.state.AccountRotations = make(map[string]accountRotationRecord)
	w.state.AccountHalfOpen = make(map[string]accounts.HalfOpenPermit)
	w.state.AccountHistoryOwnership = make(map[string]accountHistoryOwnership)
	w.state.AccountModels = make(map[string]accountModelRecord)
	w.managedOps = make(chan accountOwnerOperation, 128)
	w.resolve = func(string, []string) (string, error) { return source.CLIIdentity.Path, nil }
	w.cliProbe = func(string, string, []string, string) (*persist.CLIIdentity, error) {
		value := *source.CLIIdentity
		return &value, nil
	}
	w.withResumeFence = func(_ string, attempt func() bool) (bool, bool) { return true, attempt() }
	w.launch = func(spec daemon.LaunchSpec) (persist.Meta, error) {
		meta, err := f.launch(spec)
		if err != nil {
			return meta, err
		}
		meta.AccountBinding = spec.AccountBinding
		meta.InputEmbargo = spec.InputEmbargo
		meta.AccountProjectionRef = spec.AccountProjectionRef
		meta.CLIIdentity = source.CLIIdentity
		meta.LaunchOptions = spec.Options
		f.add(meta)
		return meta, nil
	}
	m := &accountRotationManager{w: w, store: store, aliases: make(map[string]string), stopProof: func(persist.Meta) error { return nil }, ready: func(persist.Meta, accountRotationRecord) bool { return true }, release: func(string, string) error { return nil }}
	m.prepareLaunch = func(spec daemon.LaunchSpec) (daemon.LaunchSpec, error) { return spec, nil }
	w.accountRotation = m
	resolver := newAccountResumeHistoryResolver(root, newFilesystemResumeHistoryResolver(root, defaultResumeHistoryLimits))
	m.preflight = func(meta persist.Meta, destination accounts.Binding, ownership accountHistoryOwnership, incident string) (accountHistoryManifest, error) {
		return preflightAccountHistory(store, resolver, meta, destination, ownership, incident)
	}
	m.prepare = func(manifest accountHistoryManifest) (accountHistoryManifest, error) {
		return prepareAccountHistory(root, store, manifest)
	}
	return m, f
}

func TestAccountNativeModelChangeOutranksLaunchDefaultAndAliases(t *testing.T) {
	store, root, bindings := accountTestStore(t, 1)
	source := accountTestSource(t, root, bindings[0])
	source.LaunchOptions["model"] = "gpt-launch-model"
	m, _ := rotationTestManager(t, store, root, source)
	m.w.state.AccountModels[source.ID] = accountModelRecord{Binding: bindings[0], Model: "gpt-stale-observer-model"}
	for _, test := range []struct{ latest, want string }{{"gpt-current-model", "gpt-current-model"}, {"sonnet[1m]", ""}, {"opusplan", ""}} {
		tail := `{"type":"turn_context","payload":{"model":"gpt-older-model"}}` + "\n" + `{"type":"turn_context","payload":{"model":"` + test.latest + `"}}` + "\n"
		accountTestRollout(t, store, bindings[0], root, tail)
		if got := m.effectiveModel(source); got != test.want {
			t.Fatalf("latest native model %q resolved to %q, want %q", test.latest, got, test.want)
		}
	}
	for _, alias := range []string{"opus", "opus[1m]", "sonnet[1m]", "haiku:latest", "opusplan", "default", "auto"} {
		if exactAccountModel(alias) {
			t.Fatalf("alias treated as concrete model: %s", alias)
		}
	}
}

func TestAccountRecoveryMissingStateIsReadOnlyAndUnsafePathsRefuse(t *testing.T) {
	parent := t.TempDir()
	for _, stateDir := range []string{filepath.Join(parent, "absent"), filepath.Join(parent, "nested-absent", "state")} {
		if _, err := loadAuthWatchStateChecked(stateDir); err != nil {
			t.Fatalf("first-run report failed: %v", err)
		}
		if _, err := os.Lstat(stateDir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("report created a state directory: %v", err)
		}
	}
	linked := filepath.Join(parent, "linked")
	if err := os.Symlink(filepath.Join(parent, "missing-link-target"), linked); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAuthWatchStateChecked(filepath.Join(linked, "state")); err == nil {
		t.Fatal("missing state behind a dangling symlink was accepted")
	}
	existing := filepath.Join(parent, "existing")
	if err := os.Mkdir(existing, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(existing, authWatchStateFile), []byte("corrupt existing state"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAuthWatchStateChecked(existing); err == nil {
		t.Fatal("corrupt existing journal became first-run state")
	}
}

func TestAccountHealthyManagedIdentityDriftIsQuarantinedAndHeld(t *testing.T) {
	store, root, bindings := accountTestStore(t, 1)
	source := accountTestSource(t, root, bindings[0])
	m, f := rotationTestManager(t, store, root, source)
	held := 0
	m.w.restoreRecycle = func(string) { held++ }
	candidatePath, err := store.HistoryProfilePath(bindings[0])
	if err != nil {
		t.Fatal(err)
	}
	writeIdentity := func(id, token string) {
		raw, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]string{"account_id": id, "access_token": token, "refresh_token": "synthetic-refresh"}})
		if err := os.WriteFile(filepath.Join(candidatePath, "auth.json"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeIdentity("synthetic-account-0", "refreshed-token")
	m.checkIdentities()
	if len(m.w.state.AccountRotations) != 0 || held != 0 {
		t.Fatal("ordinary refresh scheduled recovery")
	}
	writeIdentity("different-native-account", "refreshed-token")
	for i := 0; i < 3; i++ {
		m.checkIdentities()
	}
	r, _ := store.Snapshot()
	rec := m.w.state.AccountRotations[source.ID]
	if r.Accounts[bindings[0].AccountID].Auth != accounts.AuthNeedsLogin || !rec.IdentityHeld || held != 1 || len(f.killed) != 0 {
		t.Fatalf("identity drift not held/quarantined once: %+v held=%d kills=%v", rec, held, f.killed)
	}
}

func TestAccountClaudeTrialRequiresOwnerPromptAndCompletedAssistant(t *testing.T) {
	store, root, bindings := accountTestStore(t, 1)
	binding := bindings[0]
	binding.Provider = accounts.ProviderClaude
	source := accountTestSource(t, root, binding)
	source.AgentType = accounts.ProviderClaude
	m, _ := rotationTestManager(t, store, root, source)
	initial := accountRotationRecord{Incident: accounts.NewIncident("claude-trial", accounts.ProviderClaude, "claude-sonnet-4-6", 2), OriginalSource: source.ID, SourceID: source.ID, SourceBinding: binding, Destination: &binding, CandidateID: source.ID, ConversationID: source.ConversationID, State: accountCommitted, InputReleased: true, NativeHookSequence: 10}
	apply := func(event, prompt, assistant string, sequence uint64) {
		body, _ := json.Marshal(map[string]any{"session_id": source.ConversationID, "prompt": prompt, "last_assistant_message": assistant})
		if err := m.NoteClaudeTurn(engine.Callback{SessionID: source.ID, Event: event, Sequence: sequence, Raw: body}); err != nil {
			t.Fatal(err)
		}
		select {
		case op := <-m.w.managedOps:
			if err := op.apply(m.w); err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatal("authenticated hook was not observed")
		}
	}
	for _, prompt := range []string{"<task-notification>done</task-notification>", "/help", "/model sonnet", ""} {
		m.w.state.AccountRotations[source.ID] = initial
		apply("UserPromptSubmit", prompt, "", 11)
		apply("Stop", "", "local bookkeeping", 12)
		if m.w.state.AccountRotations[source.ID].State == accountComplete {
			t.Fatalf("non-inference prompt completed trial: %q", prompt)
		}
	}
	m.w.state.AccountRotations[source.ID] = initial
	apply("UserPromptSubmit", "Tell me the answer", "", 13)
	apply("Stop", "", "", 14)
	if m.w.state.AccountRotations[source.ID].State == accountComplete {
		t.Fatal("empty Stop completed trial")
	}
	apply("Stop", "", "The answer", 15)
	if m.w.state.AccountRotations[source.ID].State != accountComplete {
		t.Fatal("correlated completed owner turn did not close trial")
	}
}

func TestAccountRotationDefersApprovalPersistsBeforeKillAndPreservesProjection(t *testing.T) {
	store, root, bindings := accountTestStore(t, 3)
	source := accountTestSource(t, root, bindings[0])
	accountTestRollout(t, store, bindings[0], source.Cwd, "opaque native body\n")
	m, f := rotationTestManager(t, store, root, source)
	if err := m.reportFailure(m.w, source.ID, "quota", "exact-model", "failure-1"); err != nil {
		t.Fatal(err)
	}
	f.sessions[source.ID].Status.Interaction = status.InteractionPermission
	m.step()
	if len(f.killed) != 0 {
		t.Fatal("active approval was interrupted")
	}
	f.sessions[source.ID].Status.Interaction = status.InteractionNone
	m.step()
	rec := m.w.state.AccountRotations[source.ID]
	if rec.State != accountReserved {
		t.Fatalf("not reserved: %+v", rec)
	}
	m.w.writeState = func(string, []byte) (bool, error) { return false, errors.New("synthetic disk failure") }
	m.step()
	if len(f.killed) != 0 {
		t.Fatal("kill occurred before durable claim")
	}
	m.w.writeState = nil
	for i := 0; i < 5; i++ {
		m.step()
	}
	if len(f.launched) != 1 || f.launched[0].InitialPrompt != "" || f.launched[0].AccountProjectionRef != source.AccountProjectionRef || f.launched[0].InputEmbargo == "" {
		t.Fatalf("successor replayed input or lost frozen context: %+v", f.launched)
	}
	if err := validateAuthWatchState(m.w.state); err != nil {
		t.Fatal(err)
	}
}

func TestAccountCommittedReleaseRedrivesAfterRestartAndNeedsCorrelatedTurn(t *testing.T) {
	store, root, bindings := accountTestStore(t, 2)
	source := accountTestSource(t, root, bindings[0])
	accountTestRollout(t, store, bindings[0], source.Cwd, "latest\n")
	m, f := rotationTestManager(t, store, root, source)
	releases := 0
	m.release = func(string, string) error {
		releases++
		if releases == 1 {
			return errors.New("synthetic lost shim reply")
		}
		return nil
	}
	if err := m.reportFailure(m.w, source.ID, "quota", "exact-model", "failure-1"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		m.step()
	}
	rec := m.w.state.AccountRotations[source.ID]
	if rec.State != accountCommitted || rec.InputReleased || releases != 1 {
		t.Fatalf("not retained committed release obligation: %+v releases=%d", rec, releases)
	}
	if rec.Trial == nil {
		t.Fatal("unknown-capacity destination has no lease")
	}
	expired := *rec.Trial
	expired.Deadline = m.w.clock().Add(-time.Second)
	rec.Trial = &expired
	if err := m.persist(rec); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadAuthWatchStateChecked(root)
	if err != nil {
		t.Fatal(err)
	}
	m.w.state = loaded
	if err := m.completeTrial(rec.CandidateID, "replayed-old-turn"); err != nil {
		t.Fatal(err)
	}
	if m.w.state.AccountRotations[source.ID].State != accountCommitted {
		t.Fatal("replayed provider turn completed embargoed incident")
	}
	m.step()
	rec = m.w.state.AccountRotations[source.ID]
	if !rec.InputReleased || releases != 2 || m.w.state.Killed[source.ID] || rec.State != accountUnknown || rec.Trial != nil {
		t.Fatalf("restart did not release durable successor: %+v", rec)
	}
	if err := m.beginTrial(rec.CandidateID, "new-owner-turn"); err != nil {
		t.Fatal(err)
	}
	if err := m.completeTrial(rec.CandidateID, "old-turn"); err != nil {
		t.Fatal(err)
	}
	if m.w.state.AccountRotations[source.ID].State != accountUnknown {
		t.Fatal("uncorrelated turn ended incident")
	}
	if err := m.completeTrial(rec.CandidateID, "new-owner-turn"); err != nil {
		t.Fatal(err)
	}
	if m.w.state.AccountRotations[source.ID].State != accountComplete {
		t.Fatal("correlated successful turn did not complete incident")
	}
	if f.sessions[rec.CandidateID].AccountBinding == nil {
		t.Fatal("candidate lost binding")
	}
}

func TestAccountOwnerEndRetryKeepsDurableSuccessorTarget(t *testing.T) {
	store, root, bindings := accountTestStore(t, 2)
	source := accountTestSource(t, root, bindings[0])
	accountTestRollout(t, store, bindings[0], source.Cwd, "latest\n")
	m, _ := rotationTestManager(t, store, root, source)
	if err := m.reportFailure(m.w, source.ID, "quota", "exact-model", "owner-end-failure"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		m.step()
	}
	rec := m.w.state.AccountRotations[source.ID]
	if rec.State != accountCommitted || rec.CandidateID == "" {
		t.Fatalf("no committed successor: %+v", rec)
	}
	var targets []string
	m.endTarget = func(target, action string) error {
		targets = append(targets, target)
		loaded, err := loadAuthWatchStateChecked(root)
		if err != nil {
			t.Fatal(err)
		}
		persisted := loaded.AccountRotations[source.ID]
		if persisted.State != accountOwnerCanceled || persisted.OwnerTarget != rec.CandidateID {
			t.Fatalf("end effect preceded durable target: %+v", persisted)
		}
		if len(targets) == 1 {
			return errors.New("synthetic lost owner-end reply")
		}
		return nil
	}
	if err := m.ownerEnd(m.w, source.ID, "kill"); err == nil {
		t.Fatal("fixture did not lose first end reply")
	}
	loaded, err := loadAuthWatchStateChecked(root)
	if err != nil {
		t.Fatal(err)
	}
	m.w.state = loaded
	if err := m.ownerEnd(m.w, source.ID, "delete"); err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 || targets[0] != rec.CandidateID || targets[1] != rec.CandidateID {
		t.Fatalf("repeated owner-end changed target: %v", targets)
	}
}

func TestAccountUnknownModelHoldAndCancellationRemainReadable(t *testing.T) {
	for _, reason := range []string{"owner-end", "source-disappeared", "source-binding-changed"} {
		t.Run(reason, func(t *testing.T) {
			store, root, bindings := accountTestStore(t, 2)
			source := accountTestSource(t, root, bindings[0])
			source.LaunchOptions = nil
			m, f := rotationTestManager(t, store, root, source)
			if err := m.reportFailure(m.w, source.ID, "quota", "ignored-unverified-model", "unknown-model-failure"); err != nil {
				t.Fatal(err)
			}
			rec := m.w.state.AccountRotations[source.ID]
			if rec.State != accountBlocked || rec.Incident.Model != "" {
				t.Fatalf("unknown model did not hold: %+v", rec)
			}
			switch reason {
			case "owner-end":
				m.endTarget = func(target, action string) error {
					if target != source.ID || action != "kill" {
						t.Fatal("cancellation targeted unrelated discussion")
					}
					return nil
				}
				if err := m.ownerEnd(m.w, source.ID, "kill"); err != nil {
					t.Fatal(err)
				}
			case "source-disappeared":
				delete(f.sessions, source.ID)
				m.step()
			case "source-binding-changed":
				f.sessions[source.ID].AccountBinding = &bindings[1]
				m.step()
			}
			loaded, err := loadAuthWatchStateChecked(root)
			if err != nil {
				t.Fatalf("safe unknown-model hold froze recovery: %v", err)
			}
			m.w.state = loaded
			m.step()
			if len(f.killed) != 0 || len(f.launched) != 0 || m.w.state.AccountRotations[source.ID].Incident.Model != "" {
				t.Fatal("unknown-model hold fabricated model or authorized execution")
			}
			for _, unsafe := range []string{accountObserved, accountReserved, accountClaimed, accountCommitted, accountUnknown, accountComplete} {
				rec := loaded.AccountRotations[source.ID]
				rec.State, rec.OwnerTarget = unsafe, ""
				loaded.AccountRotations[source.ID] = rec
				if err := validateAccountRecoveryState(loaded); err == nil {
					t.Fatalf("unknown-model execution record %s accepted", unsafe)
				}
			}
		})
	}
}

func TestAccountRepeatedTrialFailureUsesCurrentNativeModelAndRetainsBudgets(t *testing.T) {
	for _, test := range []struct {
		name, latest, want string
		changesNative      bool
	}{
		{name: "owner-native-model-change", latest: "gpt-new-owner-model", want: "gpt-new-owner-model", changesNative: true},
		{name: "stale-failure-hint", want: "gpt-first-model"},
		{name: "unresolved-native-model", latest: "default", changesNative: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, root, bindings := accountTestStore(t, 3)
			source := accountTestSource(t, root, bindings[0])
			source.LaunchOptions["model"] = "gpt-first-model"
			accountTestRollout(t, store, bindings[0], root, `{"type":"turn_context","payload":{"model":"gpt-first-model"}}`+"\n")
			m, f := rotationTestManager(t, store, root, source)
			if err := m.reportFailure(m.w, source.ID, "quota", "untrusted-first-error-hint", "first-model-failure"); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 6; i++ {
				m.step()
			}
			before := m.w.state.AccountRotations[source.ID]
			if before.State != accountCommitted || !before.InputReleased || before.CandidateID == "" || before.Incident.SpawnCount != 1 {
				t.Fatalf("no persisted committed trial: %+v", before)
			}
			firstCandidate := *f.sessions[before.CandidateID]
			if test.changesNative {
				accountTestRollout(t, store, *firstCandidate.AccountBinding, root, `{"type":"turn_context","payload":{"model":"`+test.latest+`"}}`+"\n")
			}
			// Saved options and a stale observer also disagree with the native
			// history. Neither may replace the last authenticated model proof.
			firstCandidate.LaunchOptions = map[string]string{"model": "gpt-stale-launch-hint"}
			f.add(firstCandidate)
			m.w.state.AccountModels[firstCandidate.ID] = accountModelRecord{Binding: *firstCandidate.AccountBinding, Model: "gpt-stale-observer-hint"}
			before.TrialTurnID = "failed-new-owner-turn"
			before.NativeHookSequence, before.TrialHookSequence = 20, 21
			before.ConversationProven = true
			if err := m.persist(before); err != nil {
				t.Fatal(err)
			}
			loaded, err := loadAuthWatchStateChecked(root)
			if err != nil {
				t.Fatal(err)
			}
			m.w.state = loaded
			if err := m.reportFailure(m.w, firstCandidate.ID, "quota", "gpt-stale-error-hint", "repeat-model-failure"); err != nil {
				t.Fatal(err)
			}
			after := m.w.state.AccountRotations[source.ID]
			if after.Incident.Model != test.want || after.Incident.ID != before.Incident.ID || after.Incident.SpawnCount != before.Incident.SpawnCount || after.Incident.SpawnLimit != before.Incident.SpawnLimit || after.Incident.RemainingActiveNanos != before.Incident.RemainingActiveNanos || len(after.Incident.TriedAccounts) != len(before.Incident.TriedAccounts) {
				t.Fatalf("repeat failure lost native model or reset ledger: before=%+v after=%+v", before, after)
			}
			for id := range before.Incident.TriedAccounts {
				if !after.Incident.TriedAccounts[id] {
					t.Fatal("repeat failure forgot a tried logical account")
				}
			}
			if after.Destination != nil || after.Manifest != nil || after.CandidateID != "" || after.Trial != nil || after.InputReleased || after.ConversationProven || after.TrialTurnID != "" || after.NativeHookSequence != 0 || after.TrialHookSequence != 0 {
				t.Fatalf("repeat failure retained previous destination proof: %+v", after)
			}
			loaded, err = loadAuthWatchStateChecked(root)
			if err != nil {
				t.Fatal(err)
			}
			m.w.state = loaded
			for i := 0; i < 6; i++ {
				m.step()
			}
			final := m.w.state.AccountRotations[source.ID]
			if test.want == "" {
				if final.State != accountBlocked || final.LastError != "effective-model-unverified" || len(f.launched) != 1 || len(f.killed) != 1 {
					t.Fatalf("unresolved repeat model was executed: %+v launches=%d kills=%v", final, len(f.launched), f.killed)
				}
				return
			}
			if len(f.launched) != 2 || final.State != accountCommitted || final.Incident.SpawnCount != 2 || final.Incident.RemainingActiveNanos > before.Incident.RemainingActiveNanos {
				t.Fatalf("repeat recovery did not retain finite budget: %+v launches=%d", final, len(f.launched))
			}
			launch := f.launched[1]
			if launch.AccountBinding == nil || launch.AccountBinding.AccountID == firstCandidate.AccountBinding.AccountID || launch.AccountBinding.AccountID == source.AccountBinding.AccountID || launch.Options["model"] != test.want || launch.InitialPrompt != "" || launch.InputEmbargo != before.Incident.ID {
				t.Fatalf("successor binding/model did not follow current proof: %+v", launch)
			}
			ad, _ := registry.New(accounts.ProviderCodex)
			argv, err := ad.Resume(adapter.ResumeSpec{Cwd: launch.Cwd, ConversationID: source.ConversationID, Options: launch.Options})
			if err != nil || !strings.Contains(strings.Join(argv, " "), "--model "+test.want) {
				t.Fatalf("native resume arguments lost current model: %v %v", argv, err)
			}
			if _, err := loadAuthWatchStateChecked(root); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAccountRotationFallbackIsFiniteAndRetainsLatestSource(t *testing.T) {
	store, root, bindings := accountTestStore(t, 3)
	source := accountTestSource(t, root, bindings[0])
	accountTestRollout(t, store, bindings[0], source.Cwd, "latest retained native bytes\n")
	m, f := rotationTestManager(t, store, root, source)
	m.ready = func(persist.Meta, accountRotationRecord) bool { return false }
	if err := m.reportFailure(m.w, source.ID, "quota", "exact-model", "failure-1"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 24; i++ {
		m.step()
		rec := m.w.state.AccountRotations[source.ID]
		if rec.CandidateID != "" {
			f.sessions[rec.CandidateID].Status.Process = status.ProcessExited
		}
	}
	rec := m.w.state.AccountRotations[source.ID]
	if !rec.Incident.Exhausted || rec.State != accountBlocked || len(f.launched) != 2 || rec.Incident.SpawnCount != 2 {
		t.Fatalf("unbounded or missing fallback: %+v launches=%d", rec, len(f.launched))
	}
	if _, ok := f.sessions[source.ID]; !ok {
		t.Fatal("source history row erased")
	}
	for i := 0; i < 5; i++ {
		m.step()
	}
	if len(f.launched) != 2 {
		t.Fatal("exhausted incident retried automatically")
	}
}

func TestAccountHistoryLatestRoundTripAndInterruptedPublication(t *testing.T) {
	store, root, bindings := accountTestStore(t, 2)
	source := accountTestSource(t, root, bindings[0])
	relative := accountTestRollout(t, store, bindings[0], root, "original A history\n")
	resolver := newAccountResumeHistoryResolver(root, nil)
	manifest, err := preflightAccountHistory(store, resolver, source, bindings[1], accountHistoryOwnership{}, "incident-one")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err = prepareAccountHistory(root, store, manifest)
	if err != nil {
		t.Fatal(err)
	}
	ownership := accountHistoryOwnership{Epoch: 1, CurrentProfile: historyProfileKey(bindings[1]), CommittedManifest: manifest.SHA256, ProfileFiles: map[string][]accountHistoryFile{historyProfileKey(bindings[0]): manifest.Files, historyProfileKey(bindings[1]): manifest.Files}}
	accountTestRollout(t, store, bindings[1], root, "COMPACTED latest B history, not an append prefix\n")
	source.AccountBinding = &bindings[1]
	source.AuthIdentity = bindings[1].Identity
	back, err := preflightAccountHistory(store, resolver, source, bindings[0], ownership, "incident-two")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = prepareAccountHistoryWithHook(root, store, back, func(string) error { return errors.New("synthetic crash after file rename") }); err == nil {
		t.Fatal("publication crash fixture did not trigger")
	}
	back, err = prepareAccountHistory(root, store, back)
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := store.HistoryProfilePath(bindings[0])
	bytes, err := os.ReadFile(filepath.Join(profile, relative))
	if err != nil || !strings.Contains(string(bytes), "COMPACTED latest B") {
		t.Fatal("A -> B -> A restored stale A bytes")
	}
	backup, err := os.ReadFile(filepath.Join(root, "accounts", "history-transfer", back.Transaction, "backup", relative))
	if err != nil || !strings.Contains(string(backup), "original A history") {
		t.Fatal("retry overwrote original ancestry backup")
	}
	if err := verifyAccountHistory(store, back); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile, relative), []byte("diverged external history"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareAccountHistory(root, store, back); !errors.Is(err, errAccountHistoryConflict) {
		t.Fatalf("unrecorded destination divergence accepted: %v", err)
	}
}

func TestAccountHistoryUnsafeArtifactsAndVersionRefuseBeforeStop(t *testing.T) {
	store, root, bindings := accountTestStore(t, 2)
	source := accountTestSource(t, root, bindings[0])
	relative := accountTestRollout(t, store, bindings[0], root, "opaque\n")
	resolver := newAccountResumeHistoryResolver(root, nil)
	source.CLIIdentity.Version = "9.9.9"
	if _, err := preflightAccountHistory(store, resolver, source, bindings[1], accountHistoryOwnership{}, "unsupported"); !errors.Is(err, errAccountHistoryUnsupported) {
		t.Fatal("unknown native layout accepted")
	}
	source.CLIIdentity.Version = "0.160.0"
	profile, _ := store.HistoryProfilePath(bindings[0])
	path := filepath.Join(profile, relative)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := preflightAccountHistory(store, resolver, source, bindings[1], accountHistoryOwnership{}, "fifo"); err == nil {
		t.Fatal("FIFO artifact accepted")
	}
}
