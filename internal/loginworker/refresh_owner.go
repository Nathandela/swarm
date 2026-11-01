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
	"github.com/Nathandela/swarm/internal/codexnative"
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
	Version           int                    `json:"version"`
	Grant             logins.CodexRefreshRef `json:"grant"`
	LaunchNonce       string                 `json:"launch_nonce"`
	StateRoot         string                 `json:"state_root"`
	LoginID           string                 `json:"login_id"`
	Generation        uint64                 `json:"generation"`
	NativePath        string                 `json:"native_path"`
	NativeVersion     string                 `json:"native_version"`
	NativeFingerprint string                 `json:"native_fingerprint"`
	Deadline          time.Time              `json:"deadline"`
}
type RefreshRef struct {
	Config RefreshConfig
	Worker ProcessIdentity
}
type RefreshProgress struct {
	Version     int                     `json:"version"`
	Grant       logins.CodexRefreshRef  `json:"grant"`
	Output      *logins.CodexRefreshRef `json:"output,omitempty"`
	ExpiresAt   time.Time               `json:"expires_at,omitempty"`
	Creator     ProcessIdentity         `json:"creator"`
	LaunchNonce string                  `json:"launch_nonce"`
	Generation  uint64                  `json:"generation"`
	Worker      ProcessIdentity         `json:"worker_ref"`
	Native      ProcessIdentity         `json:"native,omitempty"`
	Phase       string                  `json:"phase"`
	// WritersStopped covers this attempt; admission separately rechecks grant custody.
	WritersStopped bool `json:"writers_stopped"`
	// NoNativeStarted is an exact bound worker refusal before any native child.
	NoNativeStarted bool   `json:"no_native_started,omitempty"`
	ErrorCode       string `json:"error_code,omitempty"`
	// Failures saturates at the 15-minute delay; a zero RetryAfter retains a held budget.
	Failures   uint8     `json:"failures,omitempty"`
	RetryAfter time.Time `json:"retry_after,omitzero"`
}

func validateRefresh(c RefreshConfig) error {
	if !validID(c.LaunchNonce) || !validID(c.LoginID) || c.Generation == 0 || !logins.SupportsCodexGrantRefresh(c.NativeVersion) {
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
	installation, resolveErr := codexnative.Resolve(c.NativePath)
	if resolveErr != nil || installation.Path != c.NativePath || installation.Version != "" && installation.Version != c.NativeVersion {
		return ErrUnsupported
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
	defer func() { _ = root.Close() }()
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
	defer func() { _ = parent.Close() }()
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
	defer func() { _ = lock.Close() }()
	if _, e := readRefreshProgress(f); e != nil && !errors.Is(e, os.ErrNotExist) {
		return RefreshRef{}, e
	}
	s, e := logins.OpenExisting(c.StateRoot)
	if e != nil {
		return RefreshRef{}, e
	}
	defer func() { _ = s.Close() }()
	lease, e := s.AcquireCodexGrant(c.LoginID)
	if e != nil {
		return RefreshRef{}, e
	}
	defer func() { _ = lease.Close() }()
	c.Version = refreshRecordVersion
	c.Grant, e = lease.RefreshRef()
	if e != nil {
		return RefreshRef{}, e
	}
	if e = lease.ProveStopped(); e != nil {
		return RefreshRef{}, e
	}
	creatorStart, e := procstart.StartTime(os.Getpid())
	if e != nil {
		return RefreshRef{}, e
	}
	p, e := prepareCodexRefresh(f, c, ProcessIdentity{PID: os.Getpid(), StartTime: creatorStart})
	if e != nil {
		return RefreshRef{}, e
	}
	if e = lease.Close(); e != nil {
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
	p.Worker, p.Phase = ref.Worker, PhaseStarting
	if e = f.write(refreshProgressFile, p); e != nil {
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
	if e = decodeRefreshRecord(raw, &p); e != nil {
		return p, e
	}
	if p.Generation == 0 || !p.Grant.Valid() || !validID(p.LaunchNonce) || p.Creator.PID <= 0 || p.Creator.StartTime <= 0 || p.Worker.PID < 0 || p.Worker.StartTime < 0 || (p.Worker.PID == 0) != (p.Worker.StartTime == 0) || p.Native.PID < 0 || p.Native.StartTime < 0 || (p.Native.PID == 0) != (p.Native.StartTime == 0) {
		return p, ErrInvalid
	}
	if p.Failures > 5 || (p.Failures == 0 && !p.RetryAfter.IsZero()) || (p.Phase == PhaseReady && p.Failures != 0) {
		return p, ErrInvalid
	}
	if p.NoNativeStarted && (p.Phase != PhaseFailed || p.Native != (ProcessIdentity{})) {
		return p, ErrInvalid
	}
	switch p.Phase {
	case refreshPrepared:
		if p.Worker != (ProcessIdentity{}) || p.Native != (ProcessIdentity{}) || p.WritersStopped || p.Output != nil {
			return p, ErrInvalid
		}
	case PhaseStarting:
		if p.Worker.PID <= 0 || p.Native != (ProcessIdentity{}) || p.WritersStopped || p.NoNativeStarted || p.Output != nil {
			return p, ErrInvalid
		}
	case refreshClaimed, PhaseFailed:
		if p.Worker.PID <= 0 || p.Output != nil {
			return p, ErrInvalid
		}
	case PhaseReady:
		if p.Worker.PID <= 0 || p.Native.PID <= 0 || !p.WritersStopped || p.Output == nil || !p.Output.Valid() || p.Output.LoginID != p.Grant.LoginID || p.Output.Identity != p.Grant.Identity || p.Output.Owner != p.Grant.Owner || p.Output.CredentialRevision != p.Grant.CredentialRevision || p.Output.Generation == p.Grant.Generation || p.ExpiresAt.IsZero() {
			return p, ErrInvalid
		}
	default:
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
	if p.Generation != ref.Config.Generation || p.Worker != ref.Worker || p.LaunchNonce != ref.Config.LaunchNonce || p.Grant != ref.Config.Grant {
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
	defer func() { _ = lock.Close() }()
	raw, e := readPrivate(f.root, refreshConfigFile, 16<<10)
	if e != nil {
		return e
	}
	var c RefreshConfig
	if e = decodeRefreshRecord(raw, &c); e != nil {
		return e
	}
	if c.StateRoot != stateRoot || c.LoginID != id || !c.Grant.Valid() || c.Grant.LoginID != id {
		return ErrInvalid
	}
	if c.LaunchNonce == "" || os.Getenv(refreshLaunchEnv) != c.LaunchNonce {
		return ErrStale
	}
	start, e := procstart.StartTime(os.Getpid())
	if e != nil {
		return ErrUnavailable
	}
	p := RefreshProgress{Version: refreshRecordVersion, Grant: c.Grant, LaunchNonce: c.LaunchNonce, Generation: c.Generation, Worker: ProcessIdentity{PID: os.Getpid(), StartTime: start}, Phase: PhaseStarting}
	published, e := readRefreshProgress(f)
	if e != nil || published.Worker != p.Worker || published.Generation != p.Generation || published.LaunchNonce != p.LaunchNonce || published.Phase != PhaseStarting || published.Grant != c.Grant {
		return ErrStale
	}
	p.Creator = published.Creator
	p.Failures, p.RetryAfter = published.Failures, published.RetryAfter
	p.Phase = refreshClaimed
	if e = f.write(refreshProgressFile, p); e != nil {
		return e
	}
	refuseBeforeNative := func(cause error) error {
		p.Phase, p.NoNativeStarted, p.ErrorCode = PhaseFailed, true, classifyError(cause)
		p.WritersStopped = true // This worker's native scope is positively empty.
		p.RetryAfter = time.Time{}
		// Empty native scope alone does not establish the current grant's custody.
		if retryableRefreshFailure(p.ErrorCode) {
			if store, err := logins.OpenExisting(c.StateRoot); err == nil {
				if grant, err := acquireRefreshGrant(store, c); err == nil {
					stopped := grant.ProveStopped()
					closed := grant.Close()
					if stopped == nil && closed == nil {
						recordRefreshFailure(&p, c.Grant)
					}
				}
				_ = store.Close()
			}
		}
		if err := f.write(refreshProgressFile, p); err != nil {
			return err
		}
		return cause
	}
	if !c.Deadline.IsZero() && !time.Now().Before(c.Deadline) {
		return refuseBeforeNative(context.DeadlineExceeded)
	}
	if e = validateRefresh(c); e != nil {
		return refuseBeforeNative(e)
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	ctx, cancel := context.WithDeadline(ctx, c.Deadline)
	defer cancel()
	s, e := logins.OpenExisting(c.StateRoot)
	if e != nil {
		return refuseBeforeNative(e)
	}
	defer func() { _ = s.Close() }()
	lease, e := acquireRefreshGrant(s, c)
	if e != nil {
		return refuseBeforeNative(e)
	}
	if e = enableSubreaper(); e != nil {
		_ = lease.Close()
		return refuseBeforeNative(e)
	}
	if e = ctx.Err(); e != nil {
		_ = lease.Close()
		return refuseBeforeNative(e)
	}
	syscall.Umask(0077)
	var failedRef logins.CodexRefreshRef
	{
		e = refreshCodexNative(ctx, c, f, lease, processcontain.EnvironmentWriters, func(native ProcessIdentity) error { p.Native = native; return f.write(refreshProgressFile, p) })
		stopErr := containDescendants(os.Getpid())
		reapOrphans()
		if stopErr == nil {
			stopErr = lease.ProveStopped()
		}
		if stopErr == nil {
			if e != nil {
				failedRef, _ = lease.RefreshRef()
			}
			if e == nil {
				output, refErr := lease.RefreshRef()
				access, accessErr := lease.Access(time.Now())
				if refErr != nil {
					e = refErr
				} else if accessErr != nil {
					e = accessErr
				} else {
					p.Output, p.ExpiresAt = &output, access.ExpiresAt
				}
			}
			stopErr = lease.Close()
		}
		p.WritersStopped = stopErr == nil
		if stopErr != nil {
			e = ErrUnavailable
		}
	}
	if e != nil {
		p.Phase = PhaseFailed
		p.Output, p.ExpiresAt = nil, time.Time{}
		p.ErrorCode = classifyError(e)
		recordRefreshFailure(&p, failedRef)
	} else {
		p.Failures, p.RetryAfter = 0, time.Time{}
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
	var socketOwner *refreshSocket
	defer func() {
		if socketOwner != nil {
			defer socketOwner.close()
		}
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
		if socketOwner != nil {
			if e := socketOwner.removeAfterStop(f); e != nil {
				err = nativeFailure("refresh_socket_cleanup_failed")
			}
		}
	}()
	if e = prepareRefreshScratch(f, c.LaunchNonce); e != nil {
		return e
	}
	env, e := refreshEnvironment(f, grant.Home())
	if e != nil {
		return e
	}
	installation, resolveErr := codexnative.Resolve(c.NativePath)
	if resolveErr != nil || installation.Path != c.NativePath || installation.Version != "" && installation.Version != c.NativeVersion {
		return ErrUnsupported
	}
	env = installation.ApplyEnvironment(env)
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
	if e = refreshVersion(monitorCtx, c, f, env, (*nativeChild).stop, publish); e != nil {
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
		captured, e := captureRefreshSocket(f)
		if e == nil {
			socketOwner = captured
			socket = captured.path
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
	defer func() { _ = client.Close() }()
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

func refreshVersion(ctx context.Context, c RefreshConfig, f *jobFiles, env []string, stop func(*nativeChild) error, publish func(ProcessIdentity) error) (err error) {
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
	defer func() {
		if stop(child) != nil {
			err = nativeFailure("refresh_version_stop_failed")
		}
	}()
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
