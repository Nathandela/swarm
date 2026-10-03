package skeleton

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Nathandela/swarm/internal/accountcheck"
	"github.com/Nathandela/swarm/internal/accountconfig"
	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/processcontain"
	"github.com/Nathandela/swarm/internal/procstart"
	"github.com/Nathandela/swarm/internal/protocol"
)

func isolatedAccountArguments(model string) ([]string, error) {
	return accountcheck.AvailabilityArguments(model)
}

func isolatedAccountEnvironment(env []string) []string {
	out := make([]string, 0, len(env)+2)
	for _, item := range env {
		key, _, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		switch key {
		case "CLAUDE_CODE_MAX_RETRIES", "CLAUDE_CODE_MAX_OUTPUT_TOKENS", "CLAUDE_CODE_NONSTREAMING_TIMEOUT_RETRIES":
			continue
		}
		out = append(out, item)
	}
	return append(out, "CLAUDE_CODE_MAX_RETRIES=0", "CLAUDE_CODE_MAX_OUTPUT_TOKENS=16", "CLAUDE_CODE_NONSTREAMING_TIMEOUT_RETRIES=0")
}

// RetryAvailability is admitted only by an explicit owner action. The requested
// model comes from a blocked discussion, never from a daemon-wide default.
func (m *accountRotationManager) RetryAvailability(accountID string) error {
	var source persist.Meta
	var permit accounts.HalfOpenPermit
	err := m.submit(func(w *authWatcher) error {
		if w.stateErr != nil {
			return protocol.ErrAccountsUnavailable
		}
		registry, err := m.store.Snapshot()
		if err != nil {
			return err
		}
		account, ok := registry.Accounts[accountID]
		if !ok {
			return accounts.ErrIneligible
		}
		if account.Provider == accounts.ProviderCodex {
			return errAccountAvailabilityUnsafe
		}
		keys := make([]string, 0, len(w.state.AccountRotations))
		for key := range w.state.AccountRotations {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool {
			left, right := w.state.AccountRotations[keys[i]], w.state.AccountRotations[keys[j]]
			if !left.UpdatedAt.Equal(right.UpdatedAt) {
				return left.UpdatedAt.After(right.UpdatedAt)
			}
			return keys[i] < keys[j]
		})
		for _, key := range keys {
			rec := w.state.AccountRotations[key]
			if rec.State == accountOwnerCanceled {
				continue
			}
			if rec.SourceBinding.AccountID != accountID && (rec.Destination == nil || rec.Destination.AccountID != accountID) && !rec.Incident.TriedAccounts[accountID] {
				continue
			}
			if !exactAccountModel(rec.Incident.Model) {
				continue
			}
			meta, ok := w.get(rec.SourceID)
			if !ok || meta.AgentType != account.Provider || meta.AccountBinding == nil {
				continue
			}
			source = meta
			source.LaunchOptions = cloneLaunchOptions(meta.LaunchOptions)
			source.LaunchOptions["model"] = rec.Incident.Model
			break
		}
		if source.ID == "" || source.CLIIdentity == nil || !accountconfig.SupportedNativeVersion(source.AgentType, strings.TrimPrefix(source.CLIIdentity.Version, "v")) {
			return errAccountAvailabilityUnsafe
		}
		binding, err := m.store.CurrentBinding(accountID, source.AccountBinding.ConfigurationGeneration)
		if err != nil {
			return err
		}
		source.AccountBinding = &binding
		generation := account.Generations[account.CurrentGeneration]
		if generation.Kind != accounts.KindNative || account.Lifecycle != accounts.LifecycleEnabled || !registry.Enabled[account.Provider] {
			return accounts.ErrIneligible
		}
		operationID := newItemID()
		scratch := filepath.Join(w.stateDir, "accounts", "jobs", "access-"+operationID)
		if err := os.Mkdir(scratch, 0o700); err != nil {
			return errAccountAvailabilityUnsafe
		}
		admitted := false
		defer func() {
			if !admitted {
				_ = os.RemoveAll(scratch)
			}
		}()
		source.Cwd = scratch
		profile, err := m.store.ProfilePath(binding)
		if err != nil {
			return err
		}
		env, err := m.store.ResolveEnvironment(binding, source.Env)
		if err != nil {
			return err
		}
		if err = accountconfig.ValidateAvailabilityCheck(w.stateDir, profile, source.Cwd, isolatedAccountEnvironment(env)); err != nil {
			return errAccountAvailabilityUnsafe
		}
		previous, occupied := w.state.AccountHalfOpen[accountID]
		if occupied && previous.Spent {
			if previous.WorkerPID <= 0 {
				return errAccountAvailabilityUnsafe
			}
			if start, err := procstart.StartTime(previous.WorkerPID); err == nil && start == previous.WorkerStartTime {
				return accounts.ErrInUse
			}
			if !accountcheck.CustodyStopped(w.stateDir, processcontain.Identity{PID: previous.WorkerPID, StartTime: previous.WorkerStartTime}, previous.Stamp.Binding) {
				return errAccountAvailabilityUnsafe
			}
		}
		var prior *accounts.HalfOpenPermit
		if occupied {
			prior = &previous
		}
		permit, err = accounts.AcquireHalfOpen(binding, account.Quota, source.LaunchOptions["model"], operationID, true, w.clock(), prior)
		if err != nil {
			return err
		}
		if err = permit.Spend(binding, account.Quota, w.clock()); err != nil {
			return err
		}
		oldSchema := w.state.AccountSchemaVersion
		w.state.AccountSchemaVersion = 1
		w.state.AccountHalfOpen[accountID] = permit
		visible, err := w.persistState()
		if err != nil && !visible {
			w.state.AccountSchemaVersion = oldSchema
			if occupied {
				w.state.AccountHalfOpen[accountID] = previous
			} else {
				delete(w.state.AccountHalfOpen, accountID)
			}
		}
		admitted = err == nil || visible
		return err
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithDeadline(context.Background(), permit.Deadline)
	m.accessMu.Lock()
	if m.accessClosed {
		m.accessMu.Unlock()
		cancel()
		_ = os.RemoveAll(source.Cwd)
		return protocol.ErrAccountsUnavailable
	}
	m.accessCancel[permit.OperationID] = cancel
	m.accessWG.Add(1)
	m.accessMu.Unlock()
	go func() {
		defer m.accessWG.Done()
		defer func() { _ = os.RemoveAll(source.Cwd) }()
		defer cancel()
		defer func() { m.accessMu.Lock(); delete(m.accessCancel, permit.OperationID); m.accessMu.Unlock() }()
		success, checkErr := m.accessCheck(ctx, source, permit)
		// No crash/timeout without custody proof may clear the spent admission.
		// The next owner retry must still reconcile the exact worker incarnation.
		if errors.Is(checkErr, accountcheck.ErrCustodyUnknown) {
			return
		}
		_ = m.submit(func(w *authWatcher) error {
			current, ok := w.state.AccountHalfOpen[accountID]
			if !ok || current.OperationID != permit.OperationID {
				return accounts.ErrIneligible
			}
			registry, err := m.store.Snapshot()
			if err != nil {
				return err
			}
			if _, err = m.store.CompleteAvailability(registry.Revision, permit.Stamp.Binding, current, success && checkErr == nil, false); err != nil {
				return err
			}
			delete(w.state.AccountHalfOpen, accountID)
			return w.saveState()
		})
	}()
	return nil
}

func (m *accountRotationManager) closeAccessChecks() {
	m.accessMu.Lock()
	m.accessClosed = true
	for _, cancel := range m.accessCancel {
		cancel()
	}
	m.accessMu.Unlock()
	m.accessWG.Wait()
}

func (m *accountRotationManager) nativeAccessCheck(ctx context.Context, source persist.Meta, permit accounts.HalfOpenPermit) (bool, error) {
	if source.CLIIdentity == nil || source.AccountBinding == nil || *source.AccountBinding != permit.Stamp.Binding || source.AgentType != accounts.ProviderClaude {
		return false, errAccountAvailabilityUnsafe
	}
	fingerprint, err := persist.CLIFingerprint(source.CLIIdentity.Path)
	if err != nil || fingerprint != source.CLIIdentity.Fingerprint {
		return false, errAccountAvailabilityUnsafe
	}
	profile, err := m.store.ProfilePath(*source.AccountBinding)
	if err != nil {
		return false, err
	}
	env, err := m.store.ResolveEnvironment(*source.AccountBinding, source.Env)
	if err != nil {
		return false, err
	}
	if err = accountconfig.ValidateAvailabilityCheck(m.w.stateDir, profile, source.Cwd, isolatedAccountEnvironment(env)); err != nil {
		return false, errAccountAvailabilityUnsafe
	}
	admit := func(ref accountcheck.Ref) error { return m.admitAccountCheck(source, permit, profile, env, ref) }
	authenticated, err := m.claudeAuthStatusCheck(ctx, source, env, admit)
	if err != nil {
		return false, err
	}
	if !authenticated {
		return false, accounts.ErrInvalidCredentials
	}
	if accountconfig.ValidateAvailabilityCheck(m.w.stateDir, profile, source.Cwd, isolatedAccountEnvironment(env)) != nil {
		return false, errAccountAvailabilityUnsafe
	}
	if _, err := isolatedAccountArguments(permit.Model); err != nil {
		return false, err
	}
	if m.checkExecutable() == "" {
		return false, errAccountAvailabilityUnsafe
	}
	raw, err := accountcheck.Run(ctx, m.checkExecutable(), accountcheck.Config{StateRoot: m.w.stateDir, Binding: permit.Stamp.Binding, CLI: *source.CLIIdentity, Mode: accountcheck.ModeAvailability, Model: permit.Model, Cwd: source.Cwd, Env: env, Deadline: permit.Deadline}, admit)
	if errors.Is(err, accountcheck.ErrCustodyUnknown) {
		return false, err
	}
	if err != nil || rejectDuplicateJSONKeys(raw) != nil {
		return false, accounts.ErrNoCapacity
	}
	var result struct {
		Type       string                     `json:"type"`
		Subtype    string                     `json:"subtype"`
		IsError    bool                       `json:"is_error"`
		ModelUsage map[string]json.RawMessage `json:"modelUsage"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Type != "result" || result.Subtype != "success" || result.IsError || len(result.ModelUsage) != 1 {
		return false, accounts.ErrNoCapacity
	}
	if _, ok := result.ModelUsage[permit.Model]; !ok {
		return false, accounts.ErrNoCapacity
	}
	if identity, err := m.store.NativeIdentity(permit.Stamp.Binding); err != nil || identity != permit.Stamp.Binding.Identity {
		return false, accounts.ErrIdentityChanged
	}
	return true, nil
}

func (m *accountRotationManager) checkExecutable() string {
	if m.d == nil || m.d.accounts == nil {
		return ""
	}
	return m.d.accounts.executable
}

func (m *accountRotationManager) admitAccountCheck(source persist.Meta, permit accounts.HalfOpenPermit, profile string, env []string, ref accountcheck.Ref) error {
	registry, err := m.store.Snapshot()
	if err != nil {
		return err
	}
	quota := registry.Accounts[permit.Stamp.Binding.AccountID].Quota
	for scope, revision := range permit.Stamp.DenialRevisions {
		if quota.Scopes[scope].DenialRevision != revision {
			return accounts.ErrIneligible
		}
	}
	if m.store.ValidateBinding(permit.Stamp.Binding) != nil {
		return accounts.ErrIneligible
	}
	if accountconfig.ValidateAvailabilityCheck(m.w.stateDir, profile, source.Cwd, isolatedAccountEnvironment(env)) != nil {
		return errAccountAvailabilityUnsafe
	}
	return m.submit(func(w *authWatcher) error {
		current, ok := w.state.AccountHalfOpen[permit.Stamp.Binding.AccountID]
		if !ok || current.OperationID != permit.OperationID {
			return accounts.ErrIneligible
		}
		current.WorkerPID, current.WorkerPGID, current.WorkerStartTime = ref.Worker.PID, ref.Worker.PID, ref.Worker.StartTime
		w.state.AccountHalfOpen[permit.Stamp.Binding.AccountID] = current
		_, err := w.persistState()
		return err
	})
}

func (m *accountRotationManager) claudeAuthStatus(ctx context.Context, source persist.Meta, env []string) bool {
	ok, err := m.claudeAuthStatusCheck(ctx, source, env, nil)
	return ok && err == nil
}

func (m *accountRotationManager) claudeAuthStatusCheck(ctx context.Context, source persist.Meta, env []string, admit func(accountcheck.Ref) error) (bool, error) {
	if source.CLIIdentity == nil || source.AccountBinding == nil || m.w == nil || m.checkExecutable() == "" {
		return false, errAccountAvailabilityUnsafe
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	raw, err := accountcheck.Run(ctx, m.checkExecutable(), accountcheck.Config{StateRoot: m.w.stateDir, Binding: *source.AccountBinding, CLI: *source.CLIIdentity, Mode: accountcheck.ModeAuthStatus, Cwd: source.Cwd, Env: env, Deadline: deadline}, admit)
	if err != nil {
		return false, err
	}
	if rejectDuplicateJSONKeys(raw) != nil {
		return false, errAccountAvailabilityUnsafe
	}
	var result struct {
		LoggedIn     bool   `json:"loggedIn"`
		AuthMethod   string `json:"authMethod"`
		Subscription string `json:"subscriptionType"`
	}
	if json.Unmarshal(raw, &result) != nil {
		return false, errAccountAvailabilityUnsafe
	}
	return result.LoggedIn && result.AuthMethod == "claude.ai" && (result.Subscription == "pro" || result.Subscription == "max"), nil
}
