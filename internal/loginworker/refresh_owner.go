package loginworker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/Nathandela/swarm/internal/appserver"
	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/processcontain"
	"github.com/Nathandela/swarm/internal/procstart"
)

const refreshLaunchEnv = "SWARM_CODEX_REFRESH_LAUNCH"
const refreshConfigFile = "refresh-config.json"
const refreshProgressFile = "refresh-progress.json"
const maxRefreshDeadline = 30 * time.Second

// RefreshConfig is nonsecret. One fixed worker directory per saved login keeps
// refresh receipts bounded; the dedicated worker owns the full native lifetime.
type RefreshConfig struct {
	LaunchNonce       string    `json:"launch_nonce"`
	StateRoot         string    `json:"state_root"`
	LoginID           string    `json:"login_id"`
	Generation        uint64    `json:"generation"`
	NativePath        string    `json:"native_path"`
	NativeVersion     string    `json:"native_version"`
	NativeFingerprint string    `json:"native_fingerprint"`
	Deadline          time.Time `json:"deadline"`
}
type RefreshRef struct {
	Config RefreshConfig
	Worker ProcessIdentity
}
type RefreshProgress struct {
	LaunchNonce    string          `json:"launch_nonce"`
	Generation     uint64          `json:"generation"`
	Worker         ProcessIdentity `json:"worker"`
	Native         ProcessIdentity `json:"native,omitempty"`
	Phase          string          `json:"phase"`
	WritersStopped bool            `json:"writers_stopped"`
	ErrorCode      string          `json:"error_code,omitempty"`
}

func validateRefresh(c RefreshConfig) error {
	if !validID(c.LaunchNonce) || !validID(c.LoginID) || c.Generation == 0 || c.NativeVersion != "0.163.0" {
		return ErrUnsupported
	}
	if c.Deadline.IsZero() || !c.Deadline.After(time.Now()) || c.Deadline.After(time.Now().Add(maxRefreshDeadline+time.Second)) {
		return ErrInvalid
	}
	if _, e := safeAbsolute(c.StateRoot, true, false); e != nil {
		return e
	}
	if e := ValidateNativePath(c.NativePath); e != nil {
		return e
	}
	if c.NativeFingerprint == "" || !persist.MatchCLIFingerprint(c.NativePath, c.NativeFingerprint) {
		return ErrStale
	}
	return nil
}
func openRefreshJob(stateRoot, id string, create bool) (*jobFiles, error) {
	if !validID(id) {
		return nil, ErrInvalid
	}
	base := filepath.Join(stateRoot, "logins")
	if _, e := safeAbsolute(base, true, true); e != nil {
		return nil, e
	}
	root, e := os.OpenRoot(base)
	if e != nil {
		return nil, e
	}
	defer root.Close()
	if create {
		if e = root.Mkdir("refresh", 0700); e != nil && !errors.Is(e, os.ErrExist) {
			return nil, e
		}
	}
	if _, e = safeAbsolute(filepath.Join(base, "refresh"), true, true); e != nil {
		return nil, e
	}
	parent, e := root.OpenRoot("refresh")
	if e != nil {
		return nil, e
	}
	defer parent.Close()
	if create {
		if e = parent.Mkdir(id, 0700); e != nil && !errors.Is(e, os.ErrExist) {
			return nil, e
		}
	}
	if create {
		for _, dir := range []*os.Root{root, parent} {
			f, e := dir.Open(".")
			if e != nil {
				return nil, e
			}
			e = f.Sync()
			_ = f.Close()
			if e != nil {
				return nil, e
			}
		}
	}
	path := filepath.Join(base, "refresh", id)
	anchor, e := safeAbsolute(path, true, true)
	if e != nil {
		return nil, e
	}
	opened, e := parent.OpenRoot(id)
	if e != nil {
		return nil, e
	}
	return &jobFiles{root: opened, path: path, anchor: anchor}, nil
}
func refreshJobLock(f *jobFiles, nonblock bool) (*os.File, error) {
	lock, e := f.root.OpenFile(".worker-lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if e != nil {
		return nil, e
	}
	info, e := lock.Stat()
	if e != nil || !privateInfo(info, false) {
		_ = lock.Close()
		return nil, ErrUnsafe
	}
	pathInfo, err := f.root.Lstat(".worker-lock")
	if err != nil || !os.SameFile(info, pathInfo) {
		_ = lock.Close()
		return nil, ErrUnsafe
	}
	flags := syscall.LOCK_EX
	if nonblock {
		flags |= syscall.LOCK_NB
	}
	if e = syscall.Flock(int(lock.Fd()), flags); e != nil {
		_ = lock.Close()
		return nil, ErrUnavailable
	}
	return lock, nil
}

func StartCodexRefresh(ctx context.Context, executable string, c RefreshConfig) (RefreshRef, error) {
	if e := ctx.Err(); e != nil {
		return RefreshRef{}, e
	}
	if c.Deadline.IsZero() {
		c.Deadline = time.Now().Add(maxRefreshDeadline).UTC()
	}
	var nonce [16]byte
	if _, e := rand.Read(nonce[:]); e != nil {
		return RefreshRef{}, e
	}
	c.LaunchNonce = hex.EncodeToString(nonce[:])
	if e := validateRefresh(c); e != nil {
		return RefreshRef{}, e
	}
	if e := ValidateNativePath(executable); e != nil {
		return RefreshRef{}, e
	}
	f, e := openRefreshJob(c.StateRoot, c.LoginID, true)
	if e != nil {
		return RefreshRef{}, e
	}
	defer f.Close()
	lock, e := refreshJobLock(f, true)
	if e != nil {
		return RefreshRef{}, e
	}
	defer lock.Close()
	if old, e := readRefreshProgress(f); e == nil && old.Worker.Alive() {
		return RefreshRef{}, ErrUnavailable
	} else if e != nil && !errors.Is(e, os.ErrNotExist) {
		return RefreshRef{}, e
	}
	if e = f.write(refreshConfigFile, c); e != nil {
		return RefreshRef{}, e
	}
	cmd := exec.Command(executable, "internal", "login-refresh", filepath.Join(f.path, refreshConfigFile))
	cmd.Dir = f.path
	cmd.Env = append(cleanEnvironment(os.Environ()), refreshLaunchEnv+"="+c.LaunchNonce)
	cmd.SysProcAttr = detachedAttrs()
	if e = cmd.Start(); e != nil {
		return RefreshRef{}, ErrUnavailable
	}
	start, e := procstart.StartTime(cmd.Process.Pid)
	if e != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return RefreshRef{}, ErrUnavailable
	}
	ref := RefreshRef{Config: c, Worker: ProcessIdentity{PID: cmd.Process.Pid, StartTime: start}}
	if e = f.write(refreshProgressFile, RefreshProgress{LaunchNonce: c.LaunchNonce, Generation: c.Generation, Worker: ref.Worker, Phase: PhaseStarting}); e != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return RefreshRef{}, e
	}
	go func() { _ = cmd.Wait() }()
	return ref, nil
}
func readRefreshProgress(f *jobFiles) (RefreshProgress, error) {
	var p RefreshProgress
	raw, e := readPrivate(f.root, refreshProgressFile, 4096)
	if e != nil {
		return p, e
	}
	if json.Unmarshal(raw, &p) != nil || p.Generation == 0 || p.Worker.PID <= 0 || p.Worker.StartTime <= 0 {
		return p, ErrInvalid
	}
	return p, nil
}

// ReadCodexRefresh returns only process/stop evidence, never native credentials.
func ReadCodexRefresh(ref RefreshRef) (RefreshProgress, error) {
	f, e := openRefreshJob(ref.Config.StateRoot, ref.Config.LoginID, false)
	if e != nil {
		return RefreshProgress{}, e
	}
	defer f.Close()
	p, e := readRefreshProgress(f)
	if e != nil {
		return p, e
	}
	if p.Generation != ref.Config.Generation || p.Worker != ref.Worker || p.LaunchNonce != ref.Config.LaunchNonce {
		return p, ErrStale
	}
	return p, nil
}

// RunCodexRefreshConfig is the dedicated `internal login-refresh CONFIG`
// entrypoint. It survives daemon exit and holds grant custody until containment.
func RunCodexRefreshConfig(ctx context.Context, path string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if filepath.Base(path) != refreshConfigFile {
		return ErrInvalid
	}
	id := filepath.Base(filepath.Dir(path))
	stateRoot := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(path))))
	f, e := openRefreshJob(stateRoot, id, false)
	if e != nil {
		return e
	}
	defer f.Close()
	if filepath.Join(f.path, refreshConfigFile) != path {
		return ErrUnsafe
	}
	lock, e := refreshJobLock(f, false)
	if e != nil {
		return e
	}
	defer lock.Close()
	raw, e := readPrivate(f.root, refreshConfigFile, 16<<10)
	if e != nil {
		return e
	}
	var c RefreshConfig
	if json.Unmarshal(raw, &c) != nil || c.StateRoot != stateRoot || c.LoginID != id {
		return ErrInvalid
	}
	if c.LaunchNonce == "" || os.Getenv(refreshLaunchEnv) != c.LaunchNonce {
		return ErrStale
	}
	if e = validateRefresh(c); e != nil {
		return e
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	ctx, cancel := context.WithDeadline(ctx, c.Deadline)
	defer cancel()
	start, e := procstart.StartTime(os.Getpid())
	if e != nil {
		return ErrUnavailable
	}
	p := RefreshProgress{LaunchNonce: c.LaunchNonce, Generation: c.Generation, Worker: ProcessIdentity{PID: os.Getpid(), StartTime: start}, Phase: PhaseStarting}
	published, e := readRefreshProgress(f)
	if e != nil || published.Worker != p.Worker || published.Generation != p.Generation || published.LaunchNonce != p.LaunchNonce || published.Phase != PhaseStarting {
		return ErrStale
	}
	if e = enableSubreaper(); e != nil {
		return e
	}
	syscall.Umask(0077)
	if e = f.write(refreshProgressFile, p); e != nil {
		return e
	}
	s, e := logins.Open(c.StateRoot)
	if e != nil {
		return e
	}
	defer s.Close()
	lease, e := s.AcquireCodexGrant(c.LoginID)
	if e == nil {
		e = refreshCodexNative(ctx, c, f, lease, processcontain.EnvironmentWriters, func(native ProcessIdentity) error { p.Native = native; return f.write(refreshProgressFile, p) })
		stopErr := containDescendants(os.Getpid())
		reapOrphans()
		if stopErr == nil {
			stopErr = lease.ProveStopped()
		}
		if stopErr == nil {
			stopErr = lease.Close()
		}
		p.WritersStopped = stopErr == nil
		if stopErr != nil {
			e = ErrUnavailable
		}
	}
	if e != nil {
		p.Phase = PhaseFailed
		p.ErrorCode = classifyError(e)
	} else {
		p.Phase = PhaseReady
	}
	if writeErr := f.write(refreshProgressFile, p); writeErr != nil {
		return writeErr
	}
	return e
}

type refreshGrant interface {
	Home() string
	Generation() ([32]byte, error)
	RecordNative(processcontain.Identity) error
	PrepareAuxiliary(string) error
	CheckAuxiliary() error
	CleanupAuxiliary() error
	ProveStopped() error
	ValidateRenewal([32]byte, time.Time) error
}

func refreshEnvironment(f *jobFiles, canonical string) ([]string, error) {
	for _, name := range []string{"home", "tmp", "home/config", "home/data", "home/state", "home/cache"} {
		if e := f.root.Mkdir(name, 0700); e != nil && !errors.Is(e, os.ErrExist) {
			return nil, e
		}
		if _, e := safeAbsolute(filepath.Join(f.path, name), true, true); e != nil {
			return nil, e
		}
	}
	env := cleanEnvironment(os.Environ())
	out := env[:0]
	for _, entry := range env {
		if len(entry) >= 5 && entry[:5] == "HOME=" {
			continue
		}
		out = append(out, entry)
	}
	home := filepath.Join(f.path, "home")
	tmp := filepath.Join(f.path, "tmp")
	return append(out, "HOME="+home, "CODEX_HOME="+canonical, "XDG_CONFIG_HOME="+filepath.Join(home, "config"), "XDG_DATA_HOME="+filepath.Join(home, "data"), "XDG_STATE_HOME="+filepath.Join(home, "state"), "XDG_CACHE_HOME="+filepath.Join(home, "cache"), "TMPDIR="+tmp, "TMP="+tmp, "TEMP="+tmp), nil
}
func refreshCodexNative(ctx context.Context, c RefreshConfig, f *jobFiles, grant refreshGrant, inventory func([]string, []string) ([]processcontain.Identity, error), publish func(ProcessIdentity) error) (err error) {
	before, e := grant.Generation()
	if e != nil {
		return ErrInvalid
	}
	if e = grant.PrepareAuxiliary(c.NativeVersion); e != nil {
		return ErrUnsafe
	}
	var child *nativeChild
	defer func() {
		if child != nil {
			if stopErr := child.stop(); stopErr != nil {
				err = nativeFailure("refresh_native_stop_failed")
				return
			}
		}
		if stopErr := drainRefreshWriters(grant.Home(), inventory); stopErr != nil {
			err = nativeFailure("refresh_native_inventory_failed")
			return
		}
		if stopErr := grant.ProveStopped(); stopErr != nil {
			err = nativeFailure("refresh_grant_stop_failed")
			return
		}
		if grant.CheckAuxiliary() != nil || checkRefreshScratch(f, true) != nil {
			err = ErrUnsafe
		}
		if err == nil {
			if e := grant.ValidateRenewal(before, time.Now()); e != nil {
				err = ErrInvalid
			}
		}
		if e := grant.CleanupAuxiliary(); e != nil {
			err = ErrUnsafe
		}
		if e := cleanupRefreshScratch(f); e != nil {
			err = ErrUnsafe
		}
		_ = f.root.Remove("n")
	}()
	if e = prepareRefreshScratch(f, c.LaunchNonce); e != nil {
		return e
	}
	env, e := refreshEnvironment(f, grant.Home())
	if e != nil {
		return e
	}
	// shortcut: 100 ms polling permits temporary overshoot, use a filesystem quota when a hard byte ceiling is required.
	monitorCtx, monitorCancel := context.WithCancel(ctx)
	defer monitorCancel()
	monitorDone := make(chan struct{})
	monitorFailure := make(chan struct{}, 1)
	defer func() {
		monitorCancel()
		<-monitorDone
		select {
		case <-monitorFailure:
			err = ErrUnsafe
		default:
		}
	}()
	go func() {
		defer close(monitorDone)
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-monitorCtx.Done():
				return
			case <-tick.C:
				if grant.CheckAuxiliary() != nil || checkRefreshScratch(f, true) != nil {
					monitorFailure <- struct{}{}
					monitorCancel()
					return
				}
			}
		}
	}()
	if e = refreshVersion(monitorCtx, c, f, env, grant, publish); e != nil {
		return nativeFailure("refresh_version_probe_failed")
	}
	socket := filepath.Join(f.path, "n")
	cmd := exec.Command(c.NativePath, "-c", "cli_auth_credentials_store=\"file\"", "app-server", "--listen", "unix://"+socket)
	cmd.Dir = f.path
	cmd.Env = env
	cmd.SysProcAttr = nativeAttrs()
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if !persist.MatchCLIFingerprint(c.NativePath, c.NativeFingerprint) {
		return ErrStale
	}
	child, e = startChild(cmd)
	if e != nil {
		return nativeFailure("refresh_native_start_failed")
	}
	if e = grant.RecordNative(child.identity); e != nil {
		return nativeFailure("refresh_native_identity_failed")
	}
	if e = publish(child.identity); e != nil {
		return e
	}
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		physical, e := codexSocketPath(socket)
		if e == nil {
			socket = physical
			break
		}
		if !os.IsNotExist(e) {
			return nativeFailure("refresh_socket_failed")
		}
		select {
		case <-monitorCtx.Done():
			return monitorCtx.Err()
		case <-child.done:
			return nativeFailure("refresh_native_exited")
		case <-tick.C:
		}
	}
	client, e := appserver.Dial(monitorCtx, socket, appserver.Options{DialTimeout: 3 * time.Second})
	if e != nil {
		return nativeFailure("refresh_transport_failed")
	}
	defer client.Close()
	var initialized json.RawMessage
	if e = client.Call(monitorCtx, "initialize", map[string]any{"clientInfo": map[string]string{"name": "swarm-refresh", "version": "1"}}, &initialized); e != nil {
		return nativeFailure("refresh_initialize_failed")
	}

	if e = client.Notify(monitorCtx, "initialized", map[string]any{}); e != nil {
		return nativeFailure("refresh_initialized_failed")
	}
	var account struct {
		Account *struct {
			Type string `json:"type"`
		} `json:"account"`
	}
	if e = client.Call(monitorCtx, "account/read", map[string]bool{"refreshToken": true}, &account); e != nil {
		return nativeFailure("refresh_account_read_failed")
	}
	if account.Account == nil || account.Account.Type != "chatgpt" {
		return ErrInvalid
	}
	if grant.CheckAuxiliary() != nil || checkRefreshScratch(f, true) != nil {
		return ErrUnsafe
	}
	return nil
}
func drainRefreshWriters(home string, inventory func([]string, []string) ([]processcontain.Identity, error)) error {
	deadline := time.Now().Add(3 * time.Second)
	for {
		writers, e := inventory([]string{home}, nil)
		if e != nil {
			return ErrUnavailable
		}
		if len(writers) == 0 {
			return nil
		}
		for _, writer := range writers {
			if writer.Alive() {
				if e = processcontain.SignalIdentity(writer, syscall.SIGKILL); e != nil {
					return ErrUnavailable
				}
			}
		}
		if time.Now().After(deadline) {
			return ErrUnavailable
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func refreshVersion(ctx context.Context, c RefreshConfig, f *jobFiles, env []string, grant refreshGrant, publish func(ProcessIdentity) error) error {
	versionCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.Command(c.NativePath, "--version")
	cmd.Dir = f.path
	cmd.Env = env
	cmd.SysProcAttr = nativeAttrs()
	output := &boundedBuffer{limit: 4096}
	cmd.Stdout = output
	cmd.Stderr = io.Discard
	if !persist.MatchCLIFingerprint(c.NativePath, c.NativeFingerprint) {
		return ErrStale
	}
	child, e := startChild(cmd)
	if e != nil {
		raw, overflow := output.Bytes()
		if cmd.ProcessState != nil && cmd.ProcessState.Success() && !overflow && versionMatches("codex", c.NativeVersion, string(raw)) {
			return nil
		}
		return e
	}
	defer child.stop()
	if e = publish(child.identity); e != nil {
		return e
	}
	select {
	case e = <-child.done:
		if e != nil {
			return ErrUnavailable
		}
	case <-versionCtx.Done():
		return versionCtx.Err()
	}
	raw, overflow := output.Bytes()
	if overflow || !versionMatches("codex", c.NativeVersion, string(raw)) {
		return ErrUnsupported
	}
	return nil
}
