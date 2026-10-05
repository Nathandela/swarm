package skeleton

import (
	"errors"

	"github.com/Nathandela/swarm/internal/accountconfig"
	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

var errAccountSuccessorUnavailable = errors.New("account recovery: managed successor executable or configuration is unavailable")

// Preview the real resume compiler and final managed preparation without
// changing the source's status or executing a successor. Launch receives the
// minimal request again, so inherited sandbox/worktree options are merged once.
func (m *accountRotationManager) preflightSuccessor(source persist.Meta, destination accounts.Binding, model, incident string, expected *persist.CLIIdentity) (daemon.LaunchSpec, error) {
	if m == nil || m.w == nil || m.prepareLaunch == nil || !exactAccountModel(model) || source.AccountProjectionRef == "" || model != m.effectiveModel(source) {
		return daemon.LaunchSpec{}, errAccountSuccessorUnavailable
	}
	spec := daemon.LaunchSpec{AgentType: source.AgentType, Name: source.Name, Tag: source.Tag, Cwd: source.Cwd, Cols: authRecycleCols, Rows: authRecycleRows, ClientEnv: source.Env, SpawnedFrom: source.SpawnedFrom, SpawnIntent: source.SpawnIntent, Supervision: source.Supervision, Options: map[string]string{"model": model, protocol.OptionResumeFrom: m.w.endpointID + "/" + source.ID}, AccountBinding: &destination, AccountStateRoot: m.w.stateDir, InputEmbargo: incident, AccountProjectionRef: source.AccountProjectionRef}
	spec.AccountNativeModel = model
	spec.ClientEnv = restoreClaudeOwnerFallback(source, spec.ClientEnv)
	spec.AccountOriginalConfigurationEnv = daemon.NativeConfigurationEnvironment(spec.ClientEnv)
	previewSource := source
	previewSource.Status.Process = status.ProcessExited
	compiled, err := composeLaunchSpec(spec, m.w.endpointID, "", func(local string) (persist.Meta, bool) {
		return previewSource, local == source.ID
	}, m.w.resolve)
	if err != nil || len(compiled.Argv) == 0 {
		return daemon.LaunchSpec{}, errAccountSuccessorUnavailable
	}
	probe := m.w.cliProbe
	if probe == nil {
		probe = probeCLIIdentity
	}
	identity, err := probe(compiled.AgentType, compiled.Argv[0], compiled.ClientEnv, compiled.Cwd)
	if err != nil || !validCLIIdentity(identity) || identity.Path != compiled.Argv[0] || !accountconfig.SupportedNativeVersion(compiled.AgentType, identity.Version) || (expected != nil && *identity != *expected) {
		return daemon.LaunchSpec{}, errAccountSuccessorUnavailable
	}
	checked := *identity
	compiled.CLIIdentity, compiled.ExpectedCLIIdentity = &checked, &checked
	prepared, err := m.prepareLaunch(compiled)
	if err != nil || prepared.AccountBinding == nil || *prepared.AccountBinding != destination || prepared.AccountProjectionRef != source.AccountProjectionRef || prepared.Cwd != compiled.Cwd {
		return daemon.LaunchSpec{}, errAccountSuccessorUnavailable
	}
	spec.Cwd = compiled.Cwd
	spec.CLIIdentity, spec.ExpectedCLIIdentity = &checked, &checked
	return spec, nil
}

func (w *authWatcher) validateManagedRefresh(source persist.Meta, identity *persist.CLIIdentity) error {
	if source.AccountBinding == nil {
		return nil
	}
	if !validCLIIdentity(identity) || !accountconfig.SupportedNativeVersion(source.AgentType, identity.Version) || w.accountRotation == nil {
		return errAccountSuccessorUnavailable
	}
	_, err := w.accountRotation.preflightSuccessor(source, *source.AccountBinding, w.accountRotation.effectiveModel(source), "", identity)
	return err
}
