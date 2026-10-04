package skeleton

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Nathandela/swarm/internal/accountcheck"
	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/procstart"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/status"
)

const (
	accountObserved        = "observed"
	accountReserved        = "candidate-reserved"
	accountClaimed         = "claimed"
	accountStopped         = "old-stopped"
	accountPrepared        = "history-prepared"
	accountLaunched        = "successor-launched"
	accountCommitted       = "restored-awaiting-trial"
	accountUnknown         = "restored-quota-unknown"
	accountCandidateFailed = "candidate-failed"
	accountBlocked         = "manual-recovery"
	accountComplete        = "complete"
	accountOwnerCanceled   = "owner-canceled"
)

type accountRotationRecord struct {
	Incident            accounts.Incident       `json:"incident"`
	OriginalSource      string                  `json:"original_source"`
	SourceID            string                  `json:"source_id"`
	SourceBinding       accounts.Binding        `json:"source_binding"`
	Destination         *accounts.Binding       `json:"destination,omitempty"`
	RegistryRevision    uint64                  `json:"registry_revision"`
	State               string                  `json:"state"`
	CandidateID         string                  `json:"candidate_id,omitempty"`
	ConversationID      string                  `json:"conversation_id"`
	Manifest            *accountHistoryManifest `json:"manifest,omitempty"`
	Trial               *accounts.TrialLease    `json:"trial,omitempty"`
	FailureClass        string                  `json:"failure_class"`
	TargetAccountID     string                  `json:"target_account_id,omitempty"`
	ConversationProven  bool                    `json:"conversation_proven,omitempty"`
	LastError           string                  `json:"last_error,omitempty"`
	LastActiveAt        time.Time               `json:"last_active_at,omitempty"`
	PhaseDeadline       time.Time               `json:"phase_deadline,omitempty"`
	UpdatedAt           time.Time               `json:"updated_at"`
	InputReleased       bool                    `json:"input_released,omitempty"`
	TrialTurnID         string                  `json:"trial_turn_id,omitempty"`
	NativeHookSequence  uint64                  `json:"native_hook_sequence,omitempty"`
	TrialHookSequence   uint64                  `json:"trial_hook_sequence,omitempty"`
	IdentityHeld        bool                    `json:"identity_held,omitempty"`
	OwnerTarget         string                  `json:"owner_target,omitempty"`
	ExpectedCLIIdentity *persist.CLIIdentity    `json:"expected_cli_identity,omitempty"`
}

type accountOwnerOperation struct {
	apply func(*authWatcher) error
	done  chan error
}

// This component has no state-writing goroutine. Every transition runs on the
// existing auth-watch writer, under its existing owner/composer lifecycle fences.
type accountRotationManager struct {
	d               *Daemon
	w               *authWatcher
	store           *accounts.Store
	viewMu          sync.RWMutex
	aliases         map[string]string
	refreshMu       sync.Mutex
	refreshing      map[string]chan struct{}
	refreshAt       map[string]time.Time
	stopProof       func(persist.Meta) error
	preflight       func(persist.Meta, accounts.Binding, accountHistoryOwnership, string) (accountHistoryManifest, error)
	prepare         func(accountHistoryManifest) (accountHistoryManifest, error)
	ready           func(persist.Meta, accountRotationRecord) bool
	release         func(string, string) error
	endTarget       func(string, string) error
	prepareLaunch   func(daemon.LaunchSpec) (daemon.LaunchSpec, error)
	accessCheck     func(context.Context, persist.Meta, accounts.HalfOpenPermit) (bool, error)
	accessMu        sync.Mutex
	accessCancel    map[string]context.CancelFunc
	accessWG        sync.WaitGroup
	accessClosed    bool
	inboxMu         sync.Mutex
	inboxModels     map[string]string
	inboxErrors     map[string]bool
	inboxApplying   string
	inboxQuotaFeeds map[string]accountInboxQuotaStamp
	inboxHolds      map[string]bool
	inboxFatal      bool
	checkCollector  *accountcheck.Collector
}

func newAccountRotationManager(d *Daemon, w *authWatcher, store *accounts.Store) *accountRotationManager {
	m := &accountRotationManager{d: d, w: w, store: store, aliases: make(map[string]string), refreshing: make(map[string]chan struct{}), refreshAt: make(map[string]time.Time)}
	m.accessCancel = make(map[string]context.CancelFunc)
	m.accessCheck = m.nativeAccessCheck
	m.restoreInboxHolds()
	w.accountRotation = m
	if w.state.AccountRotations == nil {
		w.state.AccountRotations = make(map[string]accountRotationRecord)
	}
	if w.state.AccountHalfOpen == nil {
		w.state.AccountHalfOpen = make(map[string]accounts.HalfOpenPermit)
	}
	if w.state.AccountHistoryOwnership == nil {
		w.state.AccountHistoryOwnership = make(map[string]accountHistoryOwnership)
	}
	m.stopProof = m.verifyStopped
	m.preflight = func(source persist.Meta, destination accounts.Binding, ownership accountHistoryOwnership, incident string) (accountHistoryManifest, error) {
		return preflightAccountHistory(store, d.api.historyResolver, source, destination, ownership, incident)
	}
	m.prepare = func(manifest accountHistoryManifest) (accountHistoryManifest, error) {
		return prepareAccountHistory(w.stateDir, store, manifest)
	}
	m.ready = m.nativeReady
	m.release = d.core.ReleaseAccountEmbargo
	m.prepareLaunch = func(spec daemon.LaunchSpec) (daemon.LaunchSpec, error) { return d.prepareAccountLaunch("", spec) }
	m.endTarget = func(target, action string) error {
		return d.withOwnerSessionEnd(target, func() error {
			if action == "kill" {
				return d.core.Kill(target)
			}
			if d.api != nil {
				return d.api.deleteDiscussion(target)
			}
			return d.core.Delete(target)
		})
	}
	// Confirm the visible pre-crash document before any destructive transition.
	if w.stateErr == nil {
		if err := syncAuthWatchStateDir(w.stateDir); err != nil {
			w.stateErr = err
		}
	}
	for _, rec := range w.state.AccountRotations {
		m.publishAliases(rec)
		if (rec.IdentityHeld && (rec.State == accountObserved || rec.State == accountBlocked)) || accountActive(rec.State) || ((rec.State == accountCommitted || rec.State == accountUnknown) && !rec.InputReleased) {
			if w.restoreRecycle != nil {
				w.restoreRecycle(rec.SourceID)
				if rec.CandidateID != "" {
					w.restoreRecycle(rec.CandidateID)
				}
			}
		}
	}
	return m
}

func accountActive(state string) bool {
	return state == accountReserved || state == accountClaimed || state == accountStopped || state == accountPrepared || state == accountLaunched || state == accountCandidateFailed
}
func accountExecuting(state string) bool {
	return state == accountClaimed || state == accountStopped || state == accountPrepared || state == accountLaunched || state == accountCandidateFailed
}

func validateAccountRecoveryState(st authWatchState) error {
	if len(st.AccountRotations) > 4096 || len(st.AccountHalfOpen) > 256 || len(st.AccountHistoryOwnership) > 4096 || len(st.AccountModels) > 4096 {
		return errors.New("authwatch: account recovery inventory exceeds bound")
	}
	if st.AccountSchemaVersion < 0 || st.AccountSchemaVersion > accounts.RecoverySchemaVersion {
		return errors.New("authwatch: unsupported account recovery schema")
	}
	if st.AccountSchemaVersion == 0 && (len(st.AccountRotations) > 0 || len(st.AccountHalfOpen) > 0 || len(st.AccountHistoryOwnership) > 0 || len(st.AccountModels) > 0) {
		return errors.New("authwatch: account recovery schema missing")
	}
	for key, rec := range st.AccountRotations {
		if key != rec.OriginalSource || !persist.ValidID(key) || !persist.ValidID(rec.SourceID) || !validManagedBinding(rec.SourceBinding) || !persist.ValidID(rec.Incident.ID) || rec.SourceBinding.Provider != rec.Incident.Provider || (rec.Incident.Model == "" && rec.State != accountBlocked && rec.State != accountOwnerCanceled) || len(rec.Incident.Model) > 128 || len(rec.Incident.TriedAccounts) > 256 || rec.Incident.SpawnCount < 0 || rec.Incident.SpawnLimit < 0 || rec.Incident.SpawnLimit > 3 || rec.Incident.SpawnCount > rec.Incident.SpawnLimit || rec.Incident.RemainingActiveNanos < 0 || rec.Incident.RemainingActiveNanos > int64(accounts.IncidentActiveBudget) {
			return errors.New("authwatch: invalid account rotation")
		}
		switch rec.State {
		case accountObserved, accountReserved, accountClaimed, accountStopped, accountPrepared, accountLaunched, accountCommitted, accountUnknown, accountCandidateFailed, accountBlocked, accountComplete, accountOwnerCanceled:
		default:
			return errors.New("authwatch: invalid account rotation phase")
		}
		if rec.CandidateID != "" && !persist.ValidID(rec.CandidateID) {
			return errors.New("authwatch: invalid account successor")
		}
		if accountActive(rec.State) && (rec.Destination == nil || rec.Manifest == nil) {
			return errors.New("authwatch: missing account rotation obligation")
		}
		if rec.Destination != nil && !validManagedBinding(*rec.Destination) {
			return errors.New("authwatch: invalid destination binding")
		}
		if rec.ExpectedCLIIdentity != nil && !validCLIIdentity(rec.ExpectedCLIIdentity) {
			return errors.New("authwatch: invalid account successor executable")
		}
		if rec.OwnerTarget != "" && (rec.State != accountOwnerCanceled || !persist.ValidID(rec.OwnerTarget) || (rec.OwnerTarget != rec.SourceID && rec.OwnerTarget != rec.CandidateID)) {
			return errors.New("authwatch: invalid account owner-end target")
		}
		for id, tried := range rec.Incident.TriedAccounts {
			if !accountHex(id, 32) || !tried {
				return errors.New("authwatch: invalid logical trial ledger")
			}
		}
		if rec.Trial != nil && (rec.Destination == nil || rec.Trial.Binding != *rec.Destination || rec.Trial.ID != rec.Incident.ID || rec.Trial.Model != rec.Incident.Model) {
			return errors.New("authwatch: invalid account trial lease")
		}
		if rec.Manifest != nil && (rec.Manifest.SchemaVersion != 1 || rec.Manifest.SHA256 != accountManifestHash(*rec.Manifest)) {
			return errors.New("authwatch: invalid history manifest")
		}
		if rec.Manifest != nil && validateAccountManifest(*rec.Manifest) != nil {
			return errors.New("authwatch: invalid bounded history inventory")
		}
	}
	for id, permit := range st.AccountHalfOpen {
		if id != permit.Stamp.Binding.AccountID || !validManagedBinding(permit.Stamp.Binding) || !persist.ValidID(permit.OperationID) || !permit.OwnerRequested || permit.Model == "" || len(permit.Model) > 128 || permit.Deadline.IsZero() || len(permit.Stamp.DenialRevisions) > 128 || permit.WorkerPID < 0 || permit.WorkerPGID < 0 || (permit.WorkerPID > 0 && (permit.WorkerPID != permit.WorkerPGID || permit.WorkerStartTime <= 0)) {
			return errors.New("authwatch: invalid half-open account permit")
		}
	}
	for key, ownership := range st.AccountHistoryOwnership {
		if len(key) > 128 || ownership.Epoch == 0 || !accountHex(ownership.CommittedManifest, 64) || len(ownership.ProfileFiles) > 256 {
			return errors.New("authwatch: invalid account history ownership")
		}
		for _, files := range ownership.ProfileFiles {
			if validateAccountHistoryFiles(files) != nil {
				return errors.New("authwatch: invalid account history artifacts")
			}
		}
	}
	for local, observed := range st.AccountModels {
		if !persist.ValidID(local) || !validManagedBinding(observed.Binding) || (observed.Model != "" && !exactAccountModel(observed.Model)) {
			return errors.New("authwatch: invalid native model evidence")
		}
	}
	return nil
}

func accountHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}
func validManagedBinding(b accounts.Binding) bool {
	return b.SchemaVersion == accounts.SchemaVersion && (b.Provider == accounts.ProviderCodex || b.Provider == accounts.ProviderClaude) && accountHex(b.AccountID, 32) && accountHex(b.Identity, 64) && b.CredentialGeneration > 0 && b.ConfigurationGeneration > 0
}

func (m *accountRotationManager) publishAliases(rec accountRotationRecord) {
	m.viewMu.Lock()
	defer m.viewMu.Unlock()
	m.aliases[rec.OriginalSource] = rec.OriginalSource
	m.aliases[rec.SourceID] = rec.OriginalSource
	if rec.CandidateID != "" {
		m.aliases[rec.CandidateID] = rec.OriginalSource
	}
}

func (m *accountRotationManager) handles(local string) bool {
	m.viewMu.RLock()
	defer m.viewMu.RUnlock()
	return m.aliases[local] != ""
}

func (m *accountRotationManager) submit(apply func(*authWatcher) error) error {
	if m == nil || m.w == nil || apply == nil {
		return protocol.ErrAccountsUnavailable
	}
	op := accountOwnerOperation{apply: apply, done: make(chan error, 1)}
	select {
	case m.w.managedOps <- op:
	case <-m.w.stop:
		return protocol.ErrAccountsUnavailable
	}
	select {
	case err := <-op.done:
		return err
	case <-m.w.stop:
		return protocol.ErrAccountsUnavailable
	}
}

func (m *accountRotationManager) persist(rec accountRotationRecord) error {
	return m.persistOwnership(rec, "", accountHistoryOwnership{})
}

func (m *accountRotationManager) persistOwnership(rec accountRotationRecord, ownershipKey string, ownership accountHistoryOwnership) error {
	w := m.w
	old := w.state
	rec = cloneAccountRotation(rec)
	rec.UpdatedAt = w.clock()
	w.state.AccountRotations = maps.Clone(old.AccountRotations)
	w.state.AccountSchemaVersion = accounts.RecoverySchemaVersion
	w.state.AccountRotations[rec.OriginalSource] = rec
	if ownershipKey != "" {
		w.state.AccountHistoryOwnership = maps.Clone(old.AccountHistoryOwnership)
		w.state.AccountHistoryOwnership[ownershipKey] = cloneAccountHistoryOwnership(ownership)
	}
	visible, err := w.persistState()
	if err != nil && !visible {
		w.state = old
	}
	if visible && err != nil {
		w.markClaimUnconfirmed(rec.OriginalSource)
	}
	if err == nil {
		w.clearClaimUnconfirmed(rec.OriginalSource)
	}
	if visible || err == nil {
		m.publishAliases(rec)
	}
	return err
}

func (m *accountRotationManager) ReportFailure(local, class, model, eventID string, willRetry bool) error {
	if willRetry || (class != "quota" && class != "auth-invalid") {
		return nil
	}
	return m.submit(func(w *authWatcher) error { return m.reportFailure(w, local, class, model, eventID) })
}

func (m *accountRotationManager) reportFailure(w *authWatcher, local, class, _, eventID string) error {
	if w.stateErr != nil || m.store == nil {
		return protocol.ErrAccountsUnavailable
	}
	source, ok := w.get(local)
	if !ok || source.AccountBinding == nil || source.RosterHidden || source.Status.Process != status.ProcessRunning {
		return accounts.ErrIneligible
	}
	model := m.effectiveModel(source)
	registry, err := m.store.Snapshot()
	if err != nil {
		return err
	}
	account, ok := registry.Accounts[source.AccountBinding.AccountID]
	if !ok {
		return accounts.ErrIneligible
	}
	generation, found := account.Generations[source.AccountBinding.CredentialGeneration]
	if !found || generation.Kind != accounts.KindNative || generation.Verification == accounts.VerificationUnverified || !validManagedBinding(*source.AccountBinding) {
		return accounts.ErrIneligible
	}
	if (class == "auth-invalid" || class == "identity-drift") && source.AccountBinding.CredentialGeneration == account.CurrentGeneration {
		if _, err = m.store.SetAuth(registry.Revision, account.ID, accounts.AuthNeedsLogin); err != nil {
			return err
		}
	} else if class == "quota" {
		feed := account.Quota.FeedGeneration
		if feed == 0 {
			feed = 1
		}
		observation := accounts.Observation{FeedGeneration: feed, Sequence: account.Quota.LastSequence + 1, EventID: eventID, ReceivedAt: w.clock(), Terminal: true, Scopes: []accounts.ScopeObservation{{Scope: accounts.ScopeGlobal, Authority: accounts.AuthorityDenied}}}
		if _, err = m.store.Observe(registry.Revision, *source.AccountBinding, observation); err != nil && !errors.Is(err, accounts.ErrIneligible) {
			return err
		}
	}
	var rec accountRotationRecord
	for _, existing := range w.state.AccountRotations {
		if existing.SourceID == local || existing.CandidateID == local {
			rec = existing
			break
		}
	}
	if rec.OriginalSource == "" || rec.State == accountComplete {
		poolSize := 0
		for _, a := range registry.Accounts {
			if a.Provider == source.AgentType && a.Lifecycle == accounts.LifecycleEnabled && a.Auth == accounts.AuthValid && a.Generations[a.CurrentGeneration].Kind == accounts.KindNative {
				poolSize++
			}
		}
		rec = accountRotationRecord{OriginalSource: local, Incident: accounts.NewIncident(newItemID(), source.AgentType, model, poolSize)}
		rec.Incident.TriedAccounts[account.ID] = true
	}
	if rec.State == accountOwnerCanceled {
		return errAuthOwnerEnding
	}
	if accountActive(rec.State) {
		return nil
	}
	// A new failed owner turn can use a later native /model selection. Keep
	// the same finite incident ledger while replacing its model authority,
	// including clearing it when the native evidence no longer resolves one.
	rec.Incident.Model = model
	rec.SourceID = local
	rec.SourceBinding = *source.AccountBinding
	rec.ConversationID = source.ConversationID
	rec.FailureClass = class
	rec.IdentityHeld = class == "identity-drift"
	rec.State = accountObserved
	rec.LastError = ""
	if model == "" {
		rec.State = accountBlocked
		rec.LastError = "effective-model-unverified"
	}
	rec.Destination = nil
	rec.Manifest = nil
	rec.CandidateID = ""
	rec.Trial = nil
	rec.InputReleased = false
	rec.ConversationProven = false
	rec.TrialTurnID = ""
	rec.NativeHookSequence = 0
	rec.TrialHookSequence = 0
	rec.PhaseDeadline = time.Time{}
	rec.ExpectedCLIIdentity = nil
	rec.LastActiveAt = time.Time{}
	err = m.persist(rec)
	if err == nil && rec.IdentityHeld && w.restoreRecycle != nil {
		w.restoreRecycle(rec.SourceID)
	}
	return err
}

func (m *accountRotationManager) activeTrials() []accounts.TrialLease {
	var trials []accounts.TrialLease
	for _, rec := range m.w.state.AccountRotations {
		if rec.Trial != nil && rec.State != accountComplete && rec.State != accountOwnerCanceled && rec.State != accountBlocked {
			trials = append(trials, *rec.Trial)
		}
	}
	return trials
}

func (m *accountRotationManager) step() {
	if m.store == nil || m.w.stateErr != nil {
		return
	}
	if err := m.drainObservationHolds(); err != nil {
		return
	}
	if err := m.drainInbox(); err != nil {
		// Each affected source stays held by its pending normalized evidence.
		if m.w.stateErr != nil {
			return
		}
	}
	if m.store == nil || m.w.stateErr != nil {
		return
	}
	keys := make([]string, 0, len(m.w.state.AccountRotations))
	for key := range m.w.state.AccountRotations {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if m.w.stopping() {
			return
		}
		rec := m.w.state.AccountRotations[key]
		if rec.State == accountComplete || rec.State == accountOwnerCanceled {
			continue
		}
		m.stepRecord(rec)
	}
	m.collectChecks()
	m.stepRetirements()
}

func (m *accountRotationManager) block(rec accountRotationRecord, code string) {
	rec.State = accountBlocked
	rec.LastError = code
	rec.Trial = nil
	_ = m.persist(rec)
}

func (m *accountRotationManager) stepRecord(rec accountRotationRecord) {
	if m.modelInboxHeld(rec.SourceID) || (rec.CandidateID != "" && m.modelInboxHeld(rec.CandidateID)) {
		return
	}
	rec = cloneAccountRotation(rec)
	w := m.w
	now := w.clock()
	if w.claimUnconfirmed(rec.OriginalSource) {
		if m.persist(rec) != nil {
			return
		}
	}
	if rec.State == accountCommitted || rec.State == accountUnknown {
		if !m.reconcileCommitted(rec) {
			return
		}
		rec = w.state.AccountRotations[rec.OriginalSource]
		if rec.Trial != nil && !rec.Trial.Deadline.After(now) {
			rec.Trial = nil
			rec.State = accountUnknown
			_ = m.persist(rec)
		}
		return
	}
	if rec.Incident.Exhausted {
		return
	}
	if accountExecuting(rec.State) && !rec.LastActiveAt.IsZero() {
		elapsed := now.Sub(rec.LastActiveAt)
		if elapsed < 0 || elapsed > accounts.IncidentActiveBudget {
			rec.Incident.RemainingActiveNanos = 0
			rec.Incident.Exhausted = true
			m.block(rec, "active-budget-unconfirmed")
			return
		}
		if rec.Incident.SpendActive(elapsed) != nil {
			m.block(rec, "active-budget-exhausted")
			return
		}
		rec.LastActiveAt = now
	}
	source, exists := w.get(rec.SourceID)
	if !exists || source.RosterHidden {
		rec.State = accountOwnerCanceled
		_ = m.persist(rec)
		return
	}
	if source.AccountBinding == nil || *source.AccountBinding != rec.SourceBinding || source.ConversationID != rec.ConversationID {
		m.block(rec, "source-binding-changed")
		return
	}
	if cli, ok := w.state.CLI[rec.SourceID]; ok && cli.State != cliRefreshComplete && cli.State != cliRefreshBlocked {
		return
	}
	if rec.Incident.Model == "" {
		model := m.effectiveModel(source)
		if model == "" {
			return
		}
		rec.Incident.Model = model
		rec.LastError = ""
		_ = m.persist(rec)
	}
	switch rec.State {
	case accountObserved, accountBlocked:
		if !m.claimableAccountSource(source, rec) || !w.settled || source.ConversationID == "" {
			return
		}
		registry, err := m.store.Snapshot()
		if err != nil {
			return
		}
		active := map[string]int{}
		for _, session := range w.list() {
			if session.AccountBinding != nil && session.Status.Process == status.ProcessRunning {
				active[session.AccountBinding.AccountID]++
			}
			if session.ID != source.ID && session.AgentType == source.AgentType && session.ConversationID == source.ConversationID && session.Status.Process == status.ProcessRunning {
				m.block(rec, "conversation-writer-conflict")
				return
			}
		}
		selection, err := accounts.Select(registry, accounts.SelectionRequest{Provider: source.AgentType, Model: rec.Incident.Model, ConfigurationGeneration: source.AccountBinding.ConfigurationGeneration, Failed: true, TriedAccounts: rec.Incident.TriedAccounts, ActiveCounts: active, Trials: m.activeTrials()}, now)
		if err != nil {
			if rec.State != accountBlocked || rec.LastError != "capacity-unavailable" {
				m.block(rec, "capacity-unavailable")
			}
			return
		}
		if rec.TargetAccountID != "" && selection.Binding.AccountID != rec.TargetAccountID {
			binding, err := m.store.CurrentBinding(rec.TargetAccountID, rec.SourceBinding.ConfigurationGeneration)
			if err != nil {
				return
			}
			a := registry.Accounts[rec.TargetAccountID]
			single := registry
			single.Accounts = map[string]accounts.Account{a.ID: a}
			selection, err = accounts.Select(single, accounts.SelectionRequest{Provider: source.AgentType, Model: rec.Incident.Model, ConfigurationGeneration: rec.SourceBinding.ConfigurationGeneration, Failed: true, TriedAccounts: rec.Incident.TriedAccounts, Trials: m.activeTrials()}, now)
			if err != nil || selection.Binding != binding {
				return
			}
		}
		if err := m.store.ValidateBinding(selection.Binding); err != nil {
			return
		}
		launch, err := m.preflightSuccessor(source, selection.Binding, rec.Incident.Model, rec.Incident.ID, nil)
		if err != nil {
			m.block(rec, "successor-preflight-refused")
			return
		}
		ownership := w.state.AccountHistoryOwnership[source.AgentType+":"+source.ConversationID]
		manifest, err := m.preflight(source, selection.Binding, ownership, rec.Incident.ID)
		if err != nil {
			m.block(rec, "history-preflight-refused")
			return
		}
		if err := rec.Incident.ReserveAccount(selection.Binding.AccountID); err != nil {
			m.block(rec, "logical-account-budget-exhausted")
			return
		}
		rec.ConversationProven = false
		rec.InputReleased = false
		rec.TrialTurnID = ""
		rec.Destination = &selection.Binding
		rec.RegistryRevision = registry.Revision
		rec.Manifest = &manifest
		rec.ExpectedCLIIdentity = launch.ExpectedCLIIdentity
		rec.State = accountReserved
		rec.LastError = ""
		if selection.Unknown {
			rec.Trial = &accounts.TrialLease{ID: rec.Incident.ID, Binding: selection.Binding, Model: rec.Incident.Model, Deadline: now.Add(accounts.IncidentActiveBudget)}
		}
		_ = m.persist(rec)
	case accountReserved:
		latest, err := m.store.Snapshot()
		if err != nil {
			return
		}
		if !m.reservationEligible(latest, rec) {
			m.block(rec, "reservation-no-longer-eligible")
			return
		}
		if err := m.store.ValidateBinding(*rec.Destination); err != nil {
			m.block(rec, "destination-binding-invalid")
			return
		}
		err = w.fencedRecycleAttempt(source.ID, rec.IdentityHeld, func() error {
			current, ok := w.get(source.ID)
			if !ok || !m.claimableAccountSource(current, rec) || current.AccountBinding == nil || *current.AccountBinding != rec.SourceBinding {
				return errAuthRecycleUnsafe
			}
			registry, err := m.store.Snapshot()
			if err != nil || !registry.Enabled[source.AgentType] {
				return errAuthRecycleUnsafe
			}
			if !m.reservationEligible(registry, rec) {
				return errAuthRecycleUnsafe
			}
			launch, err := m.preflightSuccessor(current, *rec.Destination, rec.Incident.Model, rec.Incident.ID, rec.ExpectedCLIIdentity)
			if err != nil {
				rec.LastError = "successor-preflight-refused"
				_ = m.persist(rec)
				return errAuthRecycleUnsafe
			}
			current, ok = w.get(source.ID)
			if !ok || !m.claimableAccountSource(current, rec) || current.AccountBinding == nil || *current.AccountBinding != rec.SourceBinding {
				return errAuthRecycleUnsafe
			}
			rec.ExpectedCLIIdentity = launch.ExpectedCLIIdentity
			rec.RegistryRevision = registry.Revision
			rec.State = accountClaimed
			rec.LastActiveAt = now
			rec.PhaseDeadline = now.Add(time.Duration(rec.Incident.RemainingActiveNanos))
			w.state.Killed[source.ID] = true
			if err := m.persist(rec); err != nil {
				if !w.claimUnconfirmed(rec.OriginalSource) {
					delete(w.state.Killed, source.ID)
					return err
				}
				return errAuthRecycleObligationRetained
			}
			if current.Status.Process == status.ProcessRunning {
				if err := w.kill(source.ID); err != nil {
					return errAuthRecycleObligationRetained
				}
			}
			return nil
		})
		if errors.Is(err, errAuthOwnerEnding) {
			rec.State = accountOwnerCanceled
			_ = m.persist(rec)
		}
	case accountClaimed:
		if source.Status.Process == status.ProcessRunning {
			if !w.settled {
				return
			}
			_ = w.fencedRecycleAttempt(source.ID, true, func() error {
				current, ok := w.get(source.ID)
				if !ok || current.AccountBinding == nil || *current.AccountBinding != rec.SourceBinding || current.Status.Process != status.ProcessRunning || current.Status.Turn != status.TurnIdle || current.Status.Interaction != status.InteractionNone || w.sessionUnsafe(source.ID) {
					return errAuthRecycleUnsafe
				}
				launch, err := m.preflightSuccessor(current, *rec.Destination, rec.Incident.Model, rec.Incident.ID, rec.ExpectedCLIIdentity)
				if err != nil {
					rec.LastError = "successor-preflight-refused"
					_ = m.persist(rec)
					return errAuthRecycleObligationRetained
				}
				rec.ExpectedCLIIdentity = launch.ExpectedCLIIdentity
				if err := m.persist(rec); err != nil {
					return errAuthRecycleObligationRetained
				}
				current, ok = w.get(source.ID)
				if !ok || current.Status.Process != status.ProcessRunning || current.Status.Turn != status.TurnIdle || current.Status.Interaction != status.InteractionNone || w.sessionUnsafe(source.ID) || current.AccountBinding == nil || *current.AccountBinding != rec.SourceBinding {
					return errAuthRecycleObligationRetained
				}
				if err := w.kill(source.ID); err != nil {
					return errAuthRecycleObligationRetained
				}
				return nil
			})
			return
		}
		if m.stopProof(source) != nil {
			m.block(rec, "native-writer-death-unconfirmed")
			return
		}
		if _, err := m.preflightSuccessor(source, *rec.Destination, rec.Incident.Model, rec.Incident.ID, rec.ExpectedCLIIdentity); err != nil {
			rec.LastError = "successor-preflight-refused"
			_ = m.persist(rec)
			return
		}
		ownership := w.state.AccountHistoryOwnership[source.AgentType+":"+source.ConversationID]
		manifest, err := m.preflight(source, *rec.Destination, ownership, rec.Incident.ID)
		if err != nil {
			m.block(rec, "stopped-history-invalid")
			return
		}
		rec.Manifest = &manifest
		rec.State = accountStopped
		_ = m.persist(rec)
	case accountStopped:
		if m.stopProof(source) != nil {
			m.block(rec, "native-writer-death-unconfirmed")
			return
		}
		manifest, err := m.prepare(*rec.Manifest)
		if err != nil {
			rec.LastError = "history-publish-pending"
			_ = m.persist(rec)
			return
		}
		rec.Manifest = &manifest
		rec.State = accountPrepared
		_ = m.persist(rec)
	case accountPrepared:
		latest, err := m.store.Snapshot()
		if err != nil {
			return
		}
		if !m.reservationEligible(latest, rec) {
			rec.State = accountCandidateFailed
			rec.LastError = "destination-no-longer-eligible"
			_ = m.persist(rec)
			return
		}
		if err := verifyAccountHistory(m.store, *rec.Manifest); err != nil {
			m.block(rec, "history-launch-proof-failed")
			return
		}
		if err := m.store.ValidateBinding(*rec.Destination); err != nil {
			rec.State = accountCandidateFailed
			rec.LastError = "destination-binding-invalid"
			_ = m.persist(rec)
			return
		}
		if rec.CandidateID == "" {
			for _, child := range w.list() {
				if ownedAccountCandidate(rec, child) {
					rec.CandidateID = child.ID
					break
				}
			}
		}
		if rec.CandidateID != "" {
			rec.State = accountLaunched
			_ = m.persist(rec)
			return
		}
		launch, err := m.preflightSuccessor(source, *rec.Destination, rec.Incident.Model, rec.Incident.ID, rec.ExpectedCLIIdentity)
		if err != nil {
			rec.LastError = "successor-preflight-refused"
			_ = m.persist(rec)
			return
		}
		if err := rec.Incident.RecordSpawn(rec.Destination.AccountID); err != nil {
			m.block(rec, "spawn-budget-exhausted")
			return
		}
		if err := m.persist(rec); err != nil {
			return
		}
		_, _ = w.withResumeFence(source.ID, func() bool {
			fresh, err := w.launch(launch)
			if fresh.ID != "" && !ownedAccountCandidate(rec, fresh) {
				rec.CandidateID = ""
				m.block(rec, "successor-launch-not-owned")
				return true
			}
			if fresh.ID != "" {
				rec.CandidateID = fresh.ID
				rec.State = accountLaunched
			} else {
				rec.State = accountCandidateFailed
			}
			if err != nil {
				rec.LastError = "successor-launch-failed"
			}
			_ = m.persist(rec)
			return true
		})
	case accountLaunched:
		candidate, ok := w.get(rec.CandidateID)
		if !ok || candidate.Status.Process != status.ProcessRunning {
			rec.State = accountCandidateFailed
			rec.LastError = "successor-exited"
			_ = m.persist(rec)
			return
		}
		if !ownedAccountCandidate(rec, candidate) {
			rec.CandidateID = ""
			m.block(rec, "successor-ownership-lost")
			return
		}
		if !m.ready(candidate, rec) {
			return
		}
		_, _ = w.withResumeFence(source.ID, func() bool {
			rec.State = accountCommitted
			rec.LastActiveAt = time.Time{}
			if rec.Trial != nil {
				rec.Trial.Deadline = now.Add(accounts.TrialLeaseDuration)
			}
			key := source.AgentType + ":" + source.ConversationID
			ownership := cloneAccountHistoryOwnership(w.state.AccountHistoryOwnership[key])
			if ownership.ProfileFiles == nil {
				ownership.ProfileFiles = make(map[string][]accountHistoryFile)
			}
			ownership.Epoch = rec.Manifest.Epoch
			ownership.CurrentProfile = historyProfileKey(*rec.Destination)
			ownership.CommittedManifest = rec.Manifest.SHA256
			ownership.ProfileFiles[historyProfileKey(rec.SourceBinding)] = append([]accountHistoryFile(nil), rec.Manifest.Files...)
			ownership.ProfileFiles[historyProfileKey(*rec.Destination)] = append([]accountHistoryFile(nil), rec.Manifest.Files...)
			if err := m.persistOwnership(rec, key, ownership); err != nil {
				return true
			}
			if !m.reconcileCommitted(rec) {
				return true
			}
			return false
		})
	case accountCandidateFailed:
		if rec.CandidateID != "" {
			candidate, ok := w.get(rec.CandidateID)
			if ok {
				if !ownedAccountCandidate(rec, candidate) {
					rec.CandidateID = ""
					m.block(rec, "successor-ownership-lost")
					return
				}
				if candidate.Status.Process == status.ProcessRunning {
					_ = w.kill(candidate.ID)
					return
				}
				if m.stopProof(candidate) != nil {
					m.block(rec, "failed-successor-writer-unconfirmed")
					return
				}
			} else {
				m.block(rec, "failed-successor-missing")
				return
			}
		}
		rec.CandidateID = ""
		rec.Destination = nil
		rec.Manifest = nil
		rec.Trial = nil
		rec.State = accountObserved
		// Source is already stopped: fallback selects another destination while
		// retaining the same incident, source history and spent budget.
		m.reserveStoppedFallback(rec, source)
	}
}

func cloneLaunchOptions(options map[string]string) map[string]string {
	copy := make(map[string]string, len(options)+1)
	for key, value := range options {
		copy[key] = value
	}
	delete(copy, protocol.OptionHandoffFrom)
	return copy
}

func (m *accountRotationManager) reserveStoppedFallback(rec accountRotationRecord, source persist.Meta) {
	registry, err := m.store.Snapshot()
	if err != nil {
		return
	}
	selection, err := accounts.Select(registry, accounts.SelectionRequest{Provider: source.AgentType, Model: rec.Incident.Model, ConfigurationGeneration: rec.SourceBinding.ConfigurationGeneration, Failed: true, TriedAccounts: rec.Incident.TriedAccounts, Trials: m.activeTrials()}, m.w.clock())
	if err != nil {
		rec.Incident.Exhausted = true
		m.block(rec, "fallback-capacity-exhausted")
		return
	}
	launch, err := m.preflightSuccessor(source, selection.Binding, rec.Incident.Model, rec.Incident.ID, rec.ExpectedCLIIdentity)
	if err != nil {
		m.block(rec, "successor-preflight-refused")
		return
	}
	ownership := m.w.state.AccountHistoryOwnership[source.AgentType+":"+source.ConversationID]
	manifest, err := m.preflight(source, selection.Binding, ownership, rec.Incident.ID)
	if err != nil {
		m.block(rec, "fallback-history-refused")
		return
	}
	if rec.Incident.ReserveAccount(selection.Binding.AccountID) != nil {
		m.block(rec, "fallback-budget-exhausted")
		return
	}
	rec.ConversationProven = false
	rec.InputReleased = false
	rec.TrialTurnID = ""
	rec.Destination = &selection.Binding
	rec.RegistryRevision = registry.Revision
	rec.Manifest = &manifest
	rec.ExpectedCLIIdentity = launch.ExpectedCLIIdentity
	rec.State = accountStopped
	rec.LastActiveAt = m.w.clock()
	if selection.Unknown {
		rec.Trial = &accounts.TrialLease{ID: rec.Incident.ID, Binding: selection.Binding, Model: rec.Incident.Model, Deadline: m.w.clock().Add(time.Duration(rec.Incident.RemainingActiveNanos))}
	}
	_ = m.persist(rec)
}

func (m *accountRotationManager) verifyStopped(meta persist.Meta) error {
	return verifyAccountWritersStopped(m.w.stateDir, meta)
}

// Shared by guarded recovery and owner resume under their existing lifecycle
// fences. PID disappearance cannot replace the shim's containment proof.
func verifyAccountWritersStopped(stateDir string, meta persist.Meta) error {
	if meta.Status.Process == status.ProcessRunning || meta.AccountBinding == nil || !validManagedBinding(*meta.AccountBinding) {
		return accounts.ErrInUse
	}
	root, err := openAccountRecoveryRoot(stateDir)
	if err != nil {
		return accounts.ErrInUse
	}
	defer func() { _ = root.Close() }()
	var native shim.NativeProcessInfo
	if readAccountWriterProof(root, meta.ID, shim.NativeProcessFile, &native) != nil {
		return accounts.ErrInUse
	}
	nativeAbsent := native.PID == 0 && native.PGID == 0 && native.StartTime == 0
	nativeValid := native.PID > 0 && native.PGID == native.PID && native.StartTime > 0
	if native.SchemaVersion != shim.ManagedWriterSchemaVersion || !accountHex(native.Generation, 32) || (!nativeAbsent && !nativeValid) || native.ShimPID <= 0 || native.ShimStartTime <= 0 || native.ShimPID != meta.ShimPID || native.ShimStartTime != meta.ShimStartTime || native.Binding != *meta.AccountBinding || native.IncidentID != meta.InputEmbargo || native.BackendPID < 0 || (native.BackendPID == 0) != (native.BackendStartTime == 0) || native.BackendStartTime < 0 {
		return accounts.ErrInUse
	}
	var proof shim.NativeStoppedInfo
	if readAccountWriterProof(root, meta.ID, shim.NativeStoppedFile, &proof) != nil || proof.SchemaVersion != shim.ManagedWriterSchemaVersion || !proof.WritersStopped || proof.Native != native {
		return accounts.ErrInUse
	}
	if start, err := procstart.StartTime(native.ShimPID); err == nil && start == native.ShimStartTime {
		return accounts.ErrInUse
	}
	if !nativeAbsent {
		if start, err := procstart.StartTime(native.PID); err == nil && start == native.StartTime {
			return accounts.ErrInUse
		}
		if syscall.Kill(-native.PGID, 0) != syscall.ESRCH {
			return accounts.ErrInUse
		}
	}
	if native.BackendPID > 0 {
		if start, err := procstart.StartTime(native.BackendPID); err == nil && start == native.BackendStartTime {
			return accounts.ErrInUse
		}
		if syscall.Kill(-native.BackendPID, 0) != syscall.ESRCH {
			return accounts.ErrInUse
		}
	}
	if info, ok := shim.ReadBackendInfo(filepath.Join(stateDir, meta.ID)); ok && info.PGID > 0 {
		if syscall.Kill(-info.PGID, 0) != syscall.ESRCH {
			return accounts.ErrInUse
		}
	}
	if !accountcheck.WritersStoppedForBinding(stateDir, *meta.AccountBinding) {
		return accounts.ErrInUse
	}
	return nil
}

func readAccountWriterProof(root *os.Root, local, name string, value any) error {
	if !persist.ValidID(local) {
		return accounts.ErrUnsafePath
	}
	f, err := historyOpenFile(root, local+"/"+name)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || info.Mode().Perm()&0o077 != 0 || info.Size() > 16<<10 {
		return accounts.ErrUnsafePath
	}
	raw, err := io.ReadAll(io.LimitReader(f, (16<<10)+1))
	if err != nil || len(raw) > 16<<10 || rejectDuplicateJSONKeys(raw) != nil {
		return accounts.ErrUnsafePath
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if dec.Decode(value) != nil || dec.Decode(&struct{}{}) != io.EOF {
		return accounts.ErrUnsafePath
	}
	return nil
}

func (m *accountRotationManager) nativeReady(meta persist.Meta, rec accountRotationRecord) bool {
	if meta.AccountBinding == nil || m.store.ValidateBinding(*meta.AccountBinding) != nil {
		return false
	}
	if meta.AgentType == accounts.ProviderClaude {
		if !rec.ConversationProven {
			return false
		}
		env, err := m.store.ResolveEnvironment(*meta.AccountBinding, meta.Env)
		if err != nil {
			return false
		}
		return m.claudeAuthStatus(context.Background(), meta, env)
	}
	if meta.AgentType != accounts.ProviderCodex {
		return false
	}
	if !m.d.authRecoveryReady(meta) {
		return false
	}
	backend, ok := m.d.sessionBackendFor(meta.ID)
	if !ok {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var result struct {
		Account *struct {
			Type string `json:"type"`
		} `json:"account"`
	}
	if backend.conn.Call(ctx, "account/read", map[string]any{"refreshToken": false}, &result) != nil {
		return false
	}
	return result.Account != nil && result.Account.Type == "chatgpt"
}

// OwnerEnd executes on the recovery writer BEFORE taking the owner end fence.
// This avoids a goroutine waiting on the writer while holding the writer's lock.
func (m *accountRotationManager) OwnerEnd(local, action string) error {
	return m.submit(func(w *authWatcher) error { return m.ownerEnd(w, local, action) })
}

func (m *accountRotationManager) ownerEnd(w *authWatcher, local, action string) error {
	if action != "kill" && action != "delete" {
		return accounts.ErrIneligible
	}
	m.viewMu.RLock()
	key := m.aliases[local]
	m.viewMu.RUnlock()
	rec, ok := w.state.AccountRotations[key]
	if !ok {
		return accounts.ErrIneligible
	}
	target := rec.OwnerTarget
	if target == "" {
		target = rec.SourceID
	}
	if rec.OwnerTarget == "" && (rec.State == accountCommitted || rec.State == accountUnknown || rec.State == accountComplete) && rec.CandidateID != "" {
		if candidate, ok := w.get(rec.CandidateID); ok && ownedAccountCandidate(rec, candidate) {
			target = rec.CandidateID
		}
	}
	if rec.State != accountOwnerCanceled || rec.OwnerTarget == "" {
		rec.State = accountOwnerCanceled
		rec.OwnerTarget = target
		rec.Trial = nil
		if err := m.persist(rec); err != nil {
			return err
		}
	}
	if rec.CandidateID != "" && rec.CandidateID != target {
		if candidate, ok := w.get(rec.CandidateID); ok && ownedAccountCandidate(rec, candidate) && candidate.Status.Process == status.ProcessRunning {
			if err := w.kill(candidate.ID); err != nil {
				return err
			}
		}
	}
	if w.clearRecycle != nil {
		w.clearRecycle(target)
	}
	return m.endTarget(target, action)
}

// Commit is the durable authorization to release, so restart reconciliation
// repeats the idempotent release before dropping either lifecycle fence.
func (m *accountRotationManager) reconcileCommitted(rec accountRotationRecord) bool {
	if rec.CandidateID == "" || rec.Destination == nil {
		return false
	}
	candidate, ok := m.w.get(rec.CandidateID)
	if !ok || !ownedAccountCandidate(rec, candidate) {
		return false
	}
	if candidate.Status.Process != status.ProcessRunning {
		return false
	}
	if !rec.InputReleased {
		if err := m.release(candidate.ID, rec.Incident.ID); err != nil {
			if rec.LastError != "committed-input-release-pending" {
				rec.LastError = "committed-input-release-pending"
				_ = m.persist(rec)
			}
			return false
		}
	}
	if rec.LastError != "" || !rec.InputReleased {
		rec.InputReleased = true
		rec.LastError = ""
		if m.persist(rec) != nil {
			return false
		}
	}
	if rec.InputReleased && !m.w.state.Killed[rec.SourceID] {
		return true
	}
	if m.w.clearRecycle != nil {
		m.w.clearRecycle(candidate.ID)
		m.w.clearRecycle(rec.SourceID)
	}
	delete(m.w.state.Killed, rec.SourceID)
	return m.w.saveState() == nil
}

func (m *accountRotationManager) RequestMove(local string, destination accounts.Binding) error {
	return m.submit(func(w *authWatcher) error {
		if w.stateErr != nil {
			return protocol.ErrAccountsUnavailable
		}
		source, ok := w.get(local)
		if ok && source.AccountBinding == nil {
			return protocol.ErrAccountMoveUnmanaged
		}
		if !ok || source.AccountBinding == nil || source.RosterHidden || source.AgentType != destination.Provider || source.AccountBinding.AccountID == destination.AccountID {
			return accounts.ErrIneligible
		}
		registry, err := m.store.Snapshot()
		if err != nil {
			return err
		}
		if !registry.Enabled[destination.Provider] {
			return accounts.ErrIneligible
		}
		binding, err := m.store.CurrentBinding(destination.AccountID, source.AccountBinding.ConfigurationGeneration)
		if err != nil || binding != destination {
			return accounts.ErrIneligible
		}
		for _, rec := range w.state.AccountRotations {
			if (rec.SourceID == local || rec.CandidateID == local) && rec.State != accountComplete && rec.State != accountOwnerCanceled {
				return accounts.ErrInUse
			}
		}
		rec := accountRotationRecord{OriginalSource: local, SourceID: local, SourceBinding: *source.AccountBinding, ConversationID: source.ConversationID, State: accountObserved, FailureClass: "owner-move", TargetAccountID: destination.AccountID, Incident: accounts.NewIncident(newItemID(), source.AgentType, m.effectiveModel(source), 1)}
		if rec.Incident.Model == "" {
			return errors.New("account move: effective native model cannot be verified")
		}
		rec.Incident.TriedAccounts[source.AccountBinding.AccountID] = true
		return m.persist(rec)
	})
}

// Owner movement can consume a retained, ended source after credential erasure.
// Automatic failure recovery still claims only an idle running source. Every
// retained managed attempt for that conversation must have exact stopped proof
// before a stopped owner's source can reserve a new history writer.
func (m *accountRotationManager) claimableAccountSource(source persist.Meta, rec accountRotationRecord) bool {
	if m.w.sessionUnsafe(source.ID) {
		return false
	}
	if source.Status.Process == status.ProcessRunning {
		return source.Status.Turn == status.TurnIdle && source.Status.Interaction == status.InteractionNone
	}
	if rec.FailureClass != "owner-move" || m.stopProof(source) != nil || source.AccountBinding == nil {
		return false
	}
	if _, err := m.store.HistoryProfilePath(*source.AccountBinding); err != nil {
		return false
	}
	for _, retained := range m.w.list() {
		if retained.AccountBinding != nil && retained.AgentType == source.AgentType && retained.ConversationID == source.ConversationID && m.stopProof(retained) != nil {
			return false
		}
	}
	return true
}

func (m *accountRotationManager) reservationEligible(registry accounts.Registry, rec accountRotationRecord) bool {
	if rec.Destination == nil {
		return false
	}
	a, ok := registry.Accounts[rec.Destination.AccountID]
	if !ok || a.CurrentGeneration != rec.Destination.CredentialGeneration {
		return false
	}
	single := registry
	single.Accounts = map[string]accounts.Account{a.ID: a}
	trials := m.activeTrials()
	filtered := trials[:0]
	for _, trial := range trials {
		if trial.ID != rec.Incident.ID {
			filtered = append(filtered, trial)
		}
	}
	selected, err := accounts.Select(single, accounts.SelectionRequest{Provider: rec.Incident.Provider, Model: rec.Incident.Model, ConfigurationGeneration: rec.Destination.ConfigurationGeneration, Failed: true, Trials: filtered}, m.w.clock())
	return err == nil && selected.Binding == *rec.Destination
}
