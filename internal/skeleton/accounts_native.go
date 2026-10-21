package skeleton

import (
	"os"
	"path/filepath"
	"syscall"

	"github.com/Nathandela/swarm/internal/accountconfig"
	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/enrollment"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
)

func nativePrivateInfo(info os.FileInfo, directory bool) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Uid == uint32(os.Getuid()) && info.Mode().Perm()&0o077 == 0 && ((directory && info.IsDir() && info.Mode()&os.ModeSymlink == 0) || (!directory && info.Mode().IsRegular() && st.Nlink == 1))
}

// Managed launches use the owner's installed CLI at or above the provider
// minimum; retained private copies are no longer selected.
func installedNativeSource(provider string, env []string, cwd string) (*persist.CLIIdentity, error) {
	ambient, err := probeCLIIdentity(provider, "", env, cwd)
	if err != nil || !accountconfig.SupportedNativeVersion(provider, ambient.Version) {
		return nil, errAccountLaunch
	}
	path, err := filepath.EvalSymlinks(ambient.Path)
	if err != nil || enrollment.ValidateNativePath(path) != nil {
		return nil, errAccountLaunch
	}
	return ambient, nil
}

func (m *accountManager) managedNative(provider string, env []string, cwd string) (identity *persist.CLIIdentity, resultErr error) {
	if provider == accounts.ProviderClaude {
		defer func() { m.recordNativeSelection(identity, resultErr) }()
	} else if provider != accounts.ProviderCodex {
		return nil, errAccountLaunch
	}
	return installedNativeSource(provider, env, cwd)
}

func (m *accountManager) recordNativeSelection(identity *persist.CLIIdentity, err error) {
	m.nativeMu.Lock()
	defer m.nativeMu.Unlock()
	m.nativeFailed = err != nil
	if err == nil && identity != nil {
		selected := *identity
		m.nativeSelected = &selected
	}
}

func (a *coreAPI) accountLaunchResolver(spec daemon.LaunchSpec) func(string, []string) (string, error) {
	managed := spec.AccountBinding != nil
	if !managed && spec.Options[protocol.OptionResumeFrom] != "" {
		_, source, err := validateResumeSource(spec.Options[protocol.OptionResumeFrom], spec.AgentType, a.endpointID, a.core.Get)
		managed = err == nil && source.AccountBinding != nil
	}
	if !managed && a.accounts != nil && a.accounts.store != nil {
		registry, err := a.accounts.store.Snapshot()
		managed = err == nil && registry.Enabled[spec.AgentType]
	}
	if !managed || (spec.AgentType != accounts.ProviderClaude && spec.AgentType != accounts.ProviderCodex) || a.accounts == nil {
		return lookPathIn
	}
	return func(name string, env []string) (string, error) {
		identity, err := a.accounts.managedNative(spec.AgentType, env, spec.Cwd)
		if err != nil {
			// launchResolved re-checks every bound attempt, so a frozen binding is
			// still refused and a selector-chosen one falls back (ADR-032 Amendment 4).
			return lookPathIn(name, env)
		}
		return identity.Path, nil
	}
}

func (m *accountManager) startupManagedNative(provider string) {
	delete(m.native, provider)
	if identity, err := m.managedNative(provider, daemon.PolicyEnv(nil), m.stateRoot); err == nil {
		m.native[provider] = identity
	}
}

func (m *accountRotationManager) managedCheckIdentity(source persist.Meta) (*persist.CLIIdentity, error) {
	if source.CLIIdentity == nil {
		return nil, errAccountAvailabilityUnsafe
	}
	if m.d != nil && m.d.accounts != nil {
		identity, err := m.d.accounts.managedNative(source.AgentType, source.Env, source.Cwd)
		if err == nil && identity.Version != source.CLIIdentity.Version && !cliIdentityNewer(identity, source.CLIIdentity) {
			return nil, errAccountAvailabilityUnsafe
		}
		return identity, err
	}
	if !persist.MatchCLIFingerprint(source.CLIIdentity.Path, source.CLIIdentity.Fingerprint) {
		return nil, errAccountAvailabilityUnsafe
	}
	return source.CLIIdentity, nil
}

func (w *authWatcher) managedRefreshIdentity(source persist.Meta, candidate *persist.CLIIdentity) (*persist.CLIIdentity, error) {
	if source.AccountBinding == nil || (source.AgentType != accounts.ProviderClaude && source.AgentType != accounts.ProviderCodex) || w.accountRotation == nil || w.accountRotation.d == nil || w.accountRotation.d.accounts == nil {
		return candidate, nil
	}
	if candidate == nil || !accountconfig.SupportedNativeVersion(source.AgentType, candidate.Version) {
		return nil, errAccountSuccessorUnavailable
	}
	return w.accountRotation.d.accounts.managedNative(source.AgentType, source.Env, source.Cwd)
}

func (w *authWatcher) refreshTargetIdentity(source persist.Meta, target persist.CLIIdentity) (*persist.CLIIdentity, error) {
	if (w.state.CLI[source.ID].Enrollment != nil || source.AccountBinding == nil && w.accountEnrollmentEnabled(source.AgentType)) && w.accountRotation != nil && w.accountRotation.d != nil && w.accountRotation.d.accounts != nil {
		return w.accountRotation.d.accounts.managedNative(source.AgentType, source.Env, source.ProviderCwd())
	}
	candidate, err := w.cliProbe(source.AgentType, "", source.Env, source.Cwd)
	if err != nil {
		return nil, err
	}
	return w.managedRefreshIdentity(source, candidate)
}
