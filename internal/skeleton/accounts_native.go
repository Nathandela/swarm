package skeleton

import (
	"crypto/sha256"
	"debug/elf"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/Nathandela/swarm/internal/accountconfig"
	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/enrollment"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
)

const retainedClaudeName = "claude-" + accountconfig.CharacterizedClaudeVersion
const maxNativeExecutableBytes = 1 << 30

type retainedClaudeManifest struct {
	Version       string              `json:"version"`
	Path          string              `json:"path"`
	ContentSHA256 string              `json:"content_sha256"`
	Source        persist.CLIIdentity `json:"source"`
}

func nativePrivateInfo(info os.FileInfo, directory bool) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Uid == uint32(os.Getuid()) && info.Mode().Perm()&0o077 == 0 && ((directory && info.IsDir() && info.Mode()&os.ModeSymlink == 0) || (!directory && info.Mode().IsRegular() && st.Nlink == 1))
}

func (m *accountManager) nativeRoot(create bool) (*os.Root, error) {
	if m.jobsRoot == nil {
		return nil, errAccountLaunch
	}
	anchor, err := os.Lstat(filepath.Join(m.stateRoot, "accounts"))
	if err != nil || !nativePrivateInfo(anchor, true) || !os.SameFile(anchor, m.jobsAnchor) {
		return nil, errAccountLaunch
	}
	if create {
		if err := m.jobsRoot.Mkdir("native", 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if err := syncNativeRoot(m.jobsRoot); err != nil {
			return nil, err
		}
	}
	info, err := m.jobsRoot.Lstat("native")
	if err != nil {
		return nil, err
	}
	if !nativePrivateInfo(info, true) {
		return nil, errAccountLaunch
	}
	root, err := m.jobsRoot.OpenRoot("native")
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		_ = root.Close()
		return nil, errAccountLaunch
	}
	return root, nil
}

func (m *accountManager) retainedClaude() (*persist.CLIIdentity, error) {
	root, err := m.nativeRoot(false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	info, err := root.Lstat(retainedClaudeName)
	if err != nil {
		return nil, err
	}
	if !nativePrivateInfo(info, true) {
		return nil, errAccountLaunch
	}
	version, err := root.OpenRoot(retainedClaudeName)
	if err != nil {
		return nil, errAccountLaunch
	}
	defer func() { _ = version.Close() }()
	opened, err := version.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		return nil, errAccountLaunch
	}
	info, err = version.Lstat("manifest.json")
	if err != nil || !nativePrivateInfo(info, false) || info.Size() > 8<<10 {
		return nil, errAccountLaunch
	}
	f, err := version.OpenFile("manifest.json", os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errAccountLaunch
	}
	defer func() { _ = f.Close() }()
	opened, err = f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errAccountLaunch
	}
	raw, err := io.ReadAll(io.LimitReader(f, 8<<10+1))
	if err != nil || len(raw) > 8<<10 || rejectDuplicateJSONKeys(raw) != nil {
		return nil, errAccountLaunch
	}
	var manifest retainedClaudeManifest
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if dec.Decode(&manifest) != nil {
		return nil, errAccountLaunch
	}
	var extra any
	path := filepath.Join(m.stateRoot, "accounts", "native", retainedClaudeName, "claude")
	if dec.Decode(&extra) != io.EOF || manifest.Version != accountconfig.CharacterizedClaudeVersion || manifest.Source.Version != manifest.Version || !validCLIIdentity(&manifest.Source) || manifest.Path != path || !persist.IsCLIContentFingerprint("sha256:"+manifest.ContentSHA256+":"+strings.Repeat("0", 64)) {
		return nil, errAccountLaunch
	}
	fingerprint, err := persist.CLIContentFingerprint(path)
	if err != nil || !strings.HasPrefix(fingerprint, "sha256:"+manifest.ContentSHA256+":") {
		return nil, errAccountLaunch
	}
	// Reconcile an interrupted parent-directory sync before granting execution.
	if f.Sync() != nil || syncNativeRoot(version) != nil || syncNativeRoot(root) != nil {
		return nil, errAccountLaunch
	}
	return &persist.CLIIdentity{Path: path, Version: manifest.Version, Fingerprint: fingerprint}, nil
}

func nativeVersionsDir(env []string) string {
	var home string
	for _, item := range env {
		if value, ok := strings.CutPrefix(item, "HOME="); ok {
			home = value
		}
	}
	if !filepath.IsAbs(home) || filepath.Clean(home) != home {
		return ""
	}
	return filepath.Join(home, ".local", "share", "claude", "versions")
}

// Relocation is limited to the characterized vendor-native installation. A
// custom executable, including an ELF wrapper, keeps its original path.
func (m *accountManager) retainClaude(source *persist.CLIIdentity, env []string, cwd string) (*persist.CLIIdentity, error) {
	m.nativeMu.Lock()
	defer m.nativeMu.Unlock()
	if source == nil || source.Version != accountconfig.CharacterizedClaudeVersion {
		return nil, errAccountLaunch
	}
	if cached, err := m.retainedClaude(); err == nil {
		return cached, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	path, err := filepath.EvalSymlinks(source.Path)
	if err != nil || enrollment.ValidateNativePath(path) != nil || !persist.MatchCLIFingerprint(source.Path, source.Fingerprint) {
		return nil, errAccountLaunch
	}
	versions := nativeVersionsDir(env)
	if versions == "" || path != filepath.Join(versions, source.Version) {
		return source, nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, errAccountLaunch
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Getuid()) || st.Nlink != 1 || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 || info.Size() <= 0 || info.Size() > maxNativeExecutableBytes {
		return nil, errAccountLaunch
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errAccountLaunch
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errAccountLaunch
	}
	binary, err := elf.NewFile(f)
	if err != nil || (binary.Type != elf.ET_EXEC && binary.Type != elf.ET_DYN) || (runtime.GOARCH == "amd64" && binary.Machine != elf.EM_X86_64) || (runtime.GOARCH == "arm64" && binary.Machine != elf.EM_AARCH64) {
		return nil, errAccountLaunch
	}
	beforeDigest, err := nativeFileDigest(f)
	if err != nil {
		return nil, err
	}
	root, err := m.nativeRoot(true)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	lock, err := root.OpenFile(".lock", os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.Close() }()
	lockInfo, err := lock.Stat()
	if err != nil || !nativePrivateInfo(lockInfo, false) || lockInfo.Size() != 0 {
		return nil, errAccountLaunch
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return nil, err
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()
	currentLock, err := root.Lstat(".lock")
	if err != nil || !os.SameFile(lockInfo, currentLock) {
		return nil, errAccountLaunch
	}
	if cached, err := m.retainedClaude(); err == nil {
		return cached, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	stageName := ".stage-" + newItemID()
	if err := root.Mkdir(stageName, 0o700); err != nil {
		return nil, err
	}
	defer func() { _ = root.RemoveAll(stageName) }()
	stage, err := root.OpenRoot(stageName)
	if err != nil {
		return nil, err
	}
	defer func() { _ = stage.Close() }()
	tmp, err := stage.OpenFile("claude", os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tmp.Close() }()
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	h := sha256.New()
	count, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(f, maxNativeExecutableBytes+1))
	if err != nil || count != info.Size() || fmt.Sprintf("%x", h.Sum(nil)) != beforeDigest {
		return nil, errAccountLaunch
	}
	afterDigest, err := nativeFileDigest(f)
	if err != nil || afterDigest != beforeDigest || !persist.MatchCLIFingerprint(source.Path, source.Fingerprint) {
		return nil, errAccountLaunch
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, current) {
		return nil, errAccountLaunch
	}
	if err := tmp.Chmod(0o500); err != nil {
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	stagePath := filepath.Join(m.stateRoot, "accounts", "native", stageName, "claude")
	observed, err := probeCLIIdentity(accounts.ProviderClaude, stagePath, env, cwd)
	if err != nil || observed.Version != source.Version {
		return nil, errAccountLaunch
	}
	fingerprint, err := persist.CLIContentFingerprint(stagePath)
	if err != nil || !strings.HasPrefix(fingerprint, "sha256:"+beforeDigest+":") || !persist.MatchCLIFingerprint(source.Path, source.Fingerprint) {
		return nil, errAccountLaunch
	}
	manifestPath := filepath.Join(m.stateRoot, "accounts", "native", retainedClaudeName, "claude")
	raw, err := json.Marshal(retainedClaudeManifest{Version: source.Version, Path: manifestPath, ContentSHA256: beforeDigest, Source: *source})
	if err != nil {
		return nil, err
	}
	manifest, err := stage.OpenFile("manifest.json", os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	defer func() { _ = manifest.Close() }()
	if _, err := manifest.Write(raw); err != nil {
		return nil, err
	}
	if err := manifest.Sync(); err != nil {
		return nil, err
	}
	if err := manifest.Close(); err != nil {
		return nil, err
	}
	if err := syncNativeRoot(stage); err != nil {
		return nil, err
	}
	if _, err := root.Lstat(retainedClaudeName); !errors.Is(err, os.ErrNotExist) {
		return nil, errAccountLaunch
	}
	publish := m.nativePublish
	if publish == nil {
		publish = func(root *os.Root, old, next string) error { return root.Rename(old, next) }
	}
	if err := publish(root, stageName, retainedClaudeName); err != nil {
		return nil, err
	}
	sync := m.nativeSync
	if sync == nil {
		sync = syncNativeRoot
	}
	if err := sync(root); err != nil {
		return nil, err
	}
	return m.retainedClaude()
}

func nativeFileDigest(file *os.File) (string, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	h := sha256.New()
	count, err := io.Copy(h, io.LimitReader(file, maxNativeExecutableBytes+1))
	if err != nil || count > maxNativeExecutableBytes {
		return "", errAccountLaunch
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

func syncNativeRoot(root *os.Root) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}

// Only a qualified sibling of an observed standard native installation may
// bootstrap a new cache after the ambient updater has moved ahead.
func qualifiedClaudeSource(env []string, cwd string) (*persist.CLIIdentity, error) {
	ambient, err := probeCLIIdentity(accounts.ProviderClaude, "", env, cwd)
	if err != nil {
		return nil, err
	}
	if ambient.Version == accountconfig.CharacterizedClaudeVersion {
		return ambient, nil
	}
	canonical, err := filepath.EvalSymlinks(ambient.Path)
	versions := nativeVersionsDir(env)
	if err != nil || versions == "" || filepath.Dir(canonical) != versions {
		return nil, errAccountLaunch
	}
	info, err := os.Lstat(versions)
	if err != nil {
		return nil, errAccountLaunch
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Getuid()) || !info.IsDir() || info.Mode().Perm()&0o022 != 0 {
		return nil, errAccountLaunch
	}
	path := filepath.Join(versions, accountconfig.CharacterizedClaudeVersion)
	if enrollment.ValidateNativePath(path) != nil {
		return nil, errAccountLaunch
	}
	qualified, err := probeCLIIdentity(accounts.ProviderClaude, path, env, cwd)
	if err != nil || qualified.Version != accountconfig.CharacterizedClaudeVersion {
		return nil, errAccountLaunch
	}
	return qualified, nil
}

func (m *accountManager) managedNative(provider string, env []string, cwd string) (identity *persist.CLIIdentity, resultErr error) {
	if provider != accounts.ProviderClaude {
		return probeCLIIdentity(provider, "", env, cwd)
	}
	defer func() { m.recordNativeSelection(identity, resultErr) }()
	if cached, err := m.retainedClaude(); err == nil {
		return cached, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	source, err := qualifiedClaudeSource(env, cwd)
	if err != nil {
		return nil, err
	}
	return m.retainClaude(source, env, cwd)
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
	if !managed || spec.AgentType != accounts.ProviderClaude || a.accounts == nil {
		return lookPathIn
	}
	return func(name string, env []string) (string, error) {
		identity, err := a.accounts.managedNative(spec.AgentType, env, spec.Cwd)
		if err != nil {
			return "", err
		}
		return identity.Path, nil
	}
}

func (m *accountManager) startupManagedClaude() {
	env := daemon.PolicyEnv(nil)
	if identity, err := m.retainedClaude(); err == nil {
		m.recordNativeSelection(identity, nil)
		m.native[accounts.ProviderClaude] = identity
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		m.recordNativeSelection(nil, err)
		return
	}
	registry, err := m.store.Snapshot()
	if err != nil {
		return
	}
	intent := registry.Enabled[accounts.ProviderClaude]
	for _, account := range registry.Accounts {
		intent = intent || account.Provider == accounts.ProviderClaude
	}
	for _, job := range m.jobs.Jobs {
		intent = intent || job.Provider == accounts.ProviderClaude
	}
	if !intent {
		// Advertise first enrollment from qualified source metadata without
		// retaining a large executable on an otherwise unmanaged machine.
		if identity, err := qualifiedClaudeSource(env, m.stateRoot); err == nil {
			m.native[accounts.ProviderClaude] = identity
		}
		return
	}
	if identity, err := m.managedNative(accounts.ProviderClaude, env, m.stateRoot); err == nil {
		m.native[accounts.ProviderClaude] = identity
	}
}

func (m *accountRotationManager) managedCheckIdentity(source persist.Meta) (*persist.CLIIdentity, error) {
	if source.CLIIdentity == nil {
		return nil, errAccountAvailabilityUnsafe
	}
	if m.d != nil && m.d.accounts != nil {
		return m.d.accounts.managedNative(source.AgentType, source.Env, source.Cwd)
	}
	if !persist.MatchCLIFingerprint(source.CLIIdentity.Path, source.CLIIdentity.Fingerprint) {
		return nil, errAccountAvailabilityUnsafe
	}
	return source.CLIIdentity, nil
}

func (w *authWatcher) managedRefreshIdentity(source persist.Meta, candidate *persist.CLIIdentity) (*persist.CLIIdentity, error) {
	if source.AccountBinding == nil || source.AgentType != accounts.ProviderClaude || w.accountRotation == nil || w.accountRotation.d == nil || w.accountRotation.d.accounts == nil {
		return candidate, nil
	}
	if candidate == nil || candidate.Version != accountconfig.CharacterizedClaudeVersion {
		return nil, errAccountSuccessorUnavailable
	}
	m := w.accountRotation.d.accounts
	identity, err := m.retainClaude(candidate, source.Env, source.Cwd)
	m.recordNativeSelection(identity, err)
	return identity, err
}

func (w *authWatcher) refreshTargetIdentity(source persist.Meta, target persist.CLIIdentity) (*persist.CLIIdentity, error) {
	if source.AccountBinding != nil && source.AgentType == accounts.ProviderClaude && strings.HasPrefix(target.Fingerprint, "sha256:") {
		if !persist.MatchCLIFingerprint(target.Path, target.Fingerprint) {
			return nil, errAccountSuccessorUnavailable
		}
		return &target, nil
	}
	candidate, err := w.cliProbe(source.AgentType, "", source.Env, source.Cwd)
	if err != nil {
		return nil, err
	}
	return w.managedRefreshIdentity(source, candidate)
}
