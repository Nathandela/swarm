package skeleton

import (
	"sort"

	"github.com/Nathandela/swarm/internal/accountconfig"
	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

func (w *authWatcher) accountEnrollmentEnabled(provider string) bool {
	if w.accountRotation == nil || w.accountRotation.d == nil || w.accountRotation.d.accounts == nil || w.accountRotation.d.accounts.store == nil {
		return false
	}
	r, err := w.accountRotation.d.accounts.store.Snapshot()
	return err == nil && r.Enabled[provider]
}

// Enabling a pool queues compatible running discussions on the existing durable
// recycler. Its settled/idle, composer, controller and owner fences own the stop.
func (w *authWatcher) discoverAccountEnrollments() {
	if w.accountRotation == nil || !w.settled || w.stopping() {
		return
	}
	roster := w.list()
	sort.Slice(roster, func(i, j int) bool { return roster[i].ID < roster[j].ID })
	for _, source := range roster {
		if source.AccountBinding != nil || source.RosterHidden || source.Status.Process != status.ProcessRunning || !w.accountEnrollmentEnabled(source.AgentType) {
			continue
		}
		if _, exists := w.state.CLI[source.ID]; exists {
			continue
		}
		if !validCLIIdentity(source.CLIIdentity) || !accountconfig.SupportedNativeVersion(source.AgentType, source.CLIIdentity.Version) || !adapter.IsCanonicalConversationID(source.ConversationID) {
			continue
		}
		if observed := w.cliObserve(source.ID); observed == nil || *observed != *source.CLIIdentity {
			continue
		}
		if source.Status.Turn != status.TurnIdle || source.Status.Interaction != status.InteractionNone || w.sessionUnsafe(source.ID) {
			continue
		}
		identity, err := w.accountRotation.d.accounts.managedNative(source.AgentType, source.Env, source.ProviderCwd())
		if err != nil {
			continue
		}
		launch, err := w.preflightAccountEnrollment(source, identity, nil)
		if err != nil {
			w.once("account-enroll:"+source.ID, "accounts: discussion %s cannot yet enroll safely: %v", source.ID, err)
			continue
		}
		w.state.CLI[source.ID] = cliRefreshRecord{AgentType: source.AgentType, Target: *identity, Enrollment: &accountEnrollmentPlan{Binding: *launch.AccountBinding, Projection: launch.AccountProjectionRef}, State: cliRefreshPending, UpdatedAt: w.clock()}
		if err := w.saveState(); err != nil {
			w.stateErr = err
		}
		return // One new preflight per tick keeps account/profile work bounded.
	}
}

// Compile and prepare the exact managed successor before claiming its source.
// Only the preview sees an ended source; durable source metadata stays untouched.
func (w *authWatcher) preflightAccountEnrollment(source persist.Meta, identity *persist.CLIIdentity, plan *accountEnrollmentPlan) (daemon.LaunchSpec, error) {
	if source.AccountBinding != nil || !validCLIIdentity(identity) || !validCLIIdentity(source.CLIIdentity) || !accountconfig.SupportedNativeVersion(source.AgentType, source.CLIIdentity.Version) || !accountconfig.SupportedNativeVersion(source.AgentType, identity.Version) || !adapter.IsCanonicalConversationID(source.ConversationID) || w.accountRotation == nil || w.accountRotation.d == nil || w.accountRotation.d.api == nil {
		return daemon.LaunchSpec{}, errAccountSuccessorUnavailable
	}
	if source.AgentType == accounts.ProviderCodex && identity.Version != source.CLIIdentity.Version && !cliIdentityNewer(identity, source.CLIIdentity) {
		return daemon.LaunchSpec{}, errAccountSuccessorUnavailable
	}
	d := w.accountRotation.d
	for _, other := range w.list() {
		if other.ID != source.ID && other.AgentType == source.AgentType && other.ConversationID == source.ConversationID && other.Status.Process == status.ProcessRunning {
			return daemon.LaunchSpec{}, errAccountSuccessorUnavailable
		}
	}
	if !w.accountEnrollmentEnabled(source.AgentType) && !w.state.Killed[source.ID] {
		return daemon.LaunchSpec{}, errAccountSuccessorUnavailable
	}
	if d.api.historyResolver == nil {
		return daemon.LaunchSpec{}, errAccountSuccessorUnavailable
	}
	if _, outcome := d.api.historyResolver.LocateTranscript(source, source.ConversationID); outcome != resumeHistoryFound {
		return daemon.LaunchSpec{}, errAccountSuccessorUnavailable
	}
	preview := source
	preview.Status.Process = status.ProcessExited
	spec := daemon.LaunchSpec{AgentType: source.AgentType, Name: source.Name, Tag: source.Tag, Cwd: source.Cwd, ClientEnv: restoreClaudeOwnerFallback(source, source.Env), Cols: authRecycleCols, Rows: authRecycleRows, Options: map[string]string{protocol.OptionResumeFrom: w.endpointID + "/" + source.ID}, SpawnedFrom: source.SpawnedFrom, SpawnIntent: source.SpawnIntent, Supervision: source.Supervision}
	spec.AccountOriginalConfigurationEnv = daemon.NativeConfigurationEnvironment(spec.ClientEnv)
	// This shared compiler proves Codex's latest model and delegates Claude's
	// restored model to the native conversation, exactly as an explicit Resume.
	var err error
	spec, err = d.api.prepareLegacyAccountResumeSource(spec, preview)
	if err != nil {
		return daemon.LaunchSpec{}, err
	}
	compiled, err := composeLaunchSpec(spec, w.endpointID, "", func(id string) (persist.Meta, bool) { return preview, id == source.ID }, func(string, []string) (string, error) { return identity.Path, nil })
	if err != nil {
		return daemon.LaunchSpec{}, err
	}
	compiled.CLIIdentity, compiled.ExpectedCLIIdentity = identity, identity
	if plan != nil {
		if !w.state.Killed[source.ID] {
			current, err := d.accounts.store.CurrentBinding(plan.Binding.AccountID, plan.Binding.ConfigurationGeneration)
			if err != nil || current != plan.Binding {
				return daemon.LaunchSpec{}, errAccountSuccessorUnavailable
			}
			registry, err := d.accounts.store.Snapshot()
			if err != nil {
				return daemon.LaunchSpec{}, errAccountSuccessorUnavailable
			}
			registry.Accounts = map[string]accounts.Account{current.AccountID: registry.Accounts[current.AccountID]}
			if _, err := accounts.SelectInitial(registry, accounts.SelectionRequest{Provider: source.AgentType, Model: compiled.Options["model"], ConfigurationGeneration: current.ConfigurationGeneration}, w.clock()); err != nil {
				return daemon.LaunchSpec{}, errAccountSuccessorUnavailable
			}
		}
		binding := plan.Binding
		compiled.AccountBinding, compiled.AccountProjectionRef = &binding, plan.Projection
	} else {
		// Source validation was performed against the preview above. Select only the
		// destination here; bindAccountLaunch must not treat the live source as ended.
		selection := compiled
		selection.Options = map[string]string{"model": compiled.Options["model"]}
		if err := d.api.bindAccountLaunch(&selection); err != nil {
			return daemon.LaunchSpec{}, err
		}
		compiled.AccountBinding = selection.AccountBinding
	}
	if compiled.AccountBinding == nil {
		return daemon.LaunchSpec{}, errAccountSuccessorUnavailable
	}
	prepared, err := d.prepareAccountLaunch("", compiled)
	if err != nil {
		return daemon.LaunchSpec{}, err
	}
	spec.AccountBinding, spec.AccountProjectionRef = prepared.AccountBinding, prepared.AccountProjectionRef
	spec.CLIIdentity, spec.ExpectedCLIIdentity = identity, identity
	return spec, nil
}
