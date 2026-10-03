// Package enrollment owns detached, owner-local native login workers. It never
// changes the account registry: ready means the daemon may verify a candidate.
package enrollment

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Nathandela/swarm/internal/procstart"
)

const (
	SchemaVersion       = 1
	MethodDeviceCode    = "device-code"
	MethodNativeLogin   = "native-login"
	PhaseStarting       = "starting"
	PhaseAuthenticating = "authenticating"
	PhaseVerifying      = "verifying"
	PhaseReady          = "ready"
	PhaseCanceled       = "canceled"
	PhaseFailed         = "failed"
	ConfigFile          = "worker-config.json"
	ProgressFile        = "worker-progress.json"
	SocketFile          = "worker.sock"
	CancelFile          = "cancel-fence.json"
	maxDeadline         = 15 * time.Minute
	runnerEnv           = "SWARM_ACCOUNT_ENROLL_RUNNER"
)

var (
	ErrUnsafe      = errors.New("unsafe enrollment path or permissions")
	ErrInvalid     = errors.New("invalid enrollment configuration")
	ErrUnavailable = errors.New("enrollment worker unavailable")
	ErrStale       = errors.New("enrollment worker generation changed")
	ErrCanceled    = errors.New("enrollment canceled")
	ErrUnsupported = errors.New("native enrollment unsupported")
)

// Config contains no credentials. NativePath is an absolute executable path;
// profile and job selectors are resolved under the private state anchor.
type Config struct {
	SchemaVersion              int       `json:"schema_version"`
	StateRoot                  string    `json:"state_root"`
	JobID                      string    `json:"job_id"`
	CandidateID                string    `json:"candidate_id"`
	CandidateProfileGeneration string    `json:"candidate_profile_generation"`
	Generation                 uint64    `json:"generation"`
	Provider                   string    `json:"provider"`
	Method                     string    `json:"method"`
	NativePath                 string    `json:"native_path"`
	NativeVersion              string    `json:"native_version"`
	Deadline                   time.Time `json:"deadline"`
}

type ProcessIdentity struct {
	PID       int   `json:"pid"`
	StartTime int64 `json:"start_time"`
}

func (p ProcessIdentity) Alive() bool {
	if p.PID <= 0 || p.StartTime <= 0 {
		return false
	}
	start, err := procstart.StartTime(p.PID)
	return err == nil && start == p.StartTime && processRunning(p.PID)
}

type Ref struct {
	StateRoot  string
	JobID      string
	Generation uint64
	Worker     ProcessIdentity
}

func (r Ref) SocketPath() string {
	return filepath.Join(r.StateRoot, "accounts", "jobs", r.JobID, SocketFile)
}

// Progress is bounded and nonsecret. WritersStopped is published only by the
// supervising worker after its runner and every owned descendant have died.
type Progress struct {
	SchemaVersion        int               `json:"schema_version"`
	JobID                string            `json:"job_id"`
	Generation           uint64            `json:"generation"`
	Phase                string            `json:"phase"`
	Worker               ProcessIdentity   `json:"worker"`
	Runner               ProcessIdentity   `json:"runner"`
	Children             []ProcessIdentity `json:"children,omitempty"`
	WritersStopped       bool              `json:"writers_stopped"`
	NativeWritersStopped bool              `json:"native_writers_stopped"`
	ErrorCode            string            `json:"error_code,omitempty"`
	Email                string            `json:"email,omitempty"`
	Plan                 string            `json:"plan,omitempty"`
	UpdatedAt            time.Time         `json:"updated_at"`
}

// Device presentation is RAM/live IPC only, never part of Progress.
type Device struct {
	VerificationURL string `json:"verification_url"`
	UserCode        string `json:"user_code"`
}

type LiveStatus struct {
	Progress    Progress `json:"progress"`
	Device      *Device  `json:"device,omitempty"`
	Email       string   `json:"email,omitempty"`
	Plan        string   `json:"plan,omitempty"`
	LoginSocket string   `json:"login_socket,omitempty"`
}

// Start writes a private config and starts the same executable detached. The
// returned PID is the supervisor; re-exec runner identity is recorded separately.
func Start(ctx context.Context, executable string, cfg Config) (Ref, error) {
	if err := ctx.Err(); err != nil {
		return Ref{}, err
	}
	if cfg.SchemaVersion == 0 {
		cfg.SchemaVersion = SchemaVersion
	}
	if cfg.CandidateProfileGeneration == "" {
		cfg.CandidateProfileGeneration = cfg.CandidateID
	}
	if cfg.Deadline.IsZero() {
		cfg.Deadline = time.Now().Add(maxDeadline).UTC()
	}
	if err := validateConfig(cfg); err != nil {
		return Ref{}, err
	}
	files, err := openJob(cfg.StateRoot, cfg.JobID, true)
	if err != nil {
		return Ref{}, err
	}
	defer files.Close()
	if _, err := files.root.Lstat(ConfigFile); err == nil {
		return Ref{}, ErrStale
	} else if !errors.Is(err, os.ErrNotExist) {
		return Ref{}, ErrUnsafe
	}
	if err := files.write(ConfigFile, cfg); err != nil {
		return Ref{}, err
	}
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return Ref{}, err
	}
	defer func() { _ = devnull.Close() }()
	cmd := exec.Command(executable, "internal", "account-enroll", filepath.Join(files.path, ConfigFile))
	cmd.Env = cleanEnvironment(os.Environ())
	cmd.Dir, cmd.Stdin, cmd.Stdout, cmd.Stderr = files.path, devnull, devnull, devnull
	cmd.SysProcAttr = detachedAttrs()
	if err := cmd.Start(); err != nil {
		return Ref{}, ErrUnavailable
	}
	start, err := procstart.StartTime(cmd.Process.Pid)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return Ref{}, ErrUnavailable
	}
	ref := Ref{StateRoot: cfg.StateRoot, JobID: cfg.JobID, Generation: cfg.Generation, Worker: ProcessIdentity{PID: cmd.Process.Pid, StartTime: start}}
	// Reap our direct child if this daemon survives; lifetime is independent of ctx.
	go func() { _ = cmd.Wait() }()
	return ref, nil
}

func cleanEnvironment(env []string) []string {
	var clean []string
	for _, item := range env {
		key, _, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		switch key {
		case "HOME", "USER", "LOGNAME", "PATH", "LANG", "LC_ALL", "LC_CTYPE", "TERM", "COLORTERM", "TZ":
			clean = append(clean, item)
		}
	}
	return clean
}

func NativeEnvironment(cfg Config, inherited []string) []string {
	env := cleanEnvironment(inherited)
	profile := filepath.Join(cfg.StateRoot, "accounts", "profiles", cfg.CandidateProfileGeneration)
	if cfg.Provider == "codex" {
		env = append(env, "CODEX_HOME="+profile)
	} else {
		env = append(env, "CLAUDE_CONFIG_DIR="+profile)
	}
	return env
}
