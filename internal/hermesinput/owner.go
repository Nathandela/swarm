package hermesinput

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Nathandela/swarm/internal/procstart"
)

// OwnerConfig requires a private, already configured native Hermes home. Provisioning
// and authenticating that home remains Hermes's normal CLI flow, outside this package.
type OwnerConfig struct {
	Argv      []string
	Home      string
	Cwd       string
	Env       []string
	Log       io.Writer
	Grace     time.Duration
	RequestID string
	Launch    map[string]any
	ResumeID  string
}

type Owner struct {
	Binding Binding
	cmd     *exec.Cmd
	dead    chan struct{}
	grace   time.Duration
	stop    sync.Once
}

func GatewayArgv(program string) []string {
	return []string{program, "gateway", "run", "--no-supervise", "--force", "--quiet"}
}

// Environment keeps the private owner and viewer on the same home and prevents
// inherited remote-gateway routing or another home's rendezvous from changing it.
func Environment(base []string, home string) []string {
	out := make([]string, 0, len(base)+3)
	for _, item := range base {
		key, _, _ := strings.Cut(item, "=")
		switch key {
		case "HERMES_HOME", "HERMES_GATEWAY_LOCK_DIR", "HERMES_TUI_GATEWAY_URL", "HERMES_GATEWAY_NO_SUPERVISE", "_HERMES_GATEWAY":
			continue
		}
		out = append(out, item)
	}
	return append(out, "HERMES_HOME="+home, "HERMES_GATEWAY_LOCK_DIR="+filepath.Join(home, "gateway-locks"), "HERMES_GATEWAY_NO_SUPERVISE=1")
}

// Start owns only the supplied foreground process group. It refuses a gateway
// that multiplexes other profiles and never adopts, replaces, or kills one.
func Start(ctx context.Context, cfg OwnerConfig) (*Owner, error) {
	if len(cfg.Argv) == 0 || cfg.Argv[0] == "" || !filepath.IsAbs(cfg.Home) || !filepath.IsAbs(cfg.Cwd) {
		return nil, errors.New("hermes owner requires argv and absolute home/cwd")
	}
	if err := privateNode(cfg.Home, os.ModeDir); err != nil {
		return nil, err
	}
	canonical, err := filepath.EvalSymlinks(cfg.Home)
	if err != nil {
		return nil, err
	}
	cfg.Home = canonical
	if filepath.Base(filepath.Dir(cfg.Home)) == "profiles" {
		return nil, errors.New("hermes owned gateway requires an independent home, not a multiplexed profile")
	}
	profiles, err := os.ReadDir(filepath.Join(cfg.Home, "profiles"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if len(profiles) > 0 {
		return nil, errors.New("hermes owned gateway home must contain no other profiles")
	}
	if _, err := os.Stat(filepath.Join(cfg.Home, "config.yaml")); err != nil {
		return nil, fmt.Errorf("hermes native home is not configured; use the native Hermes setup flow: %w", err)
	}
	cmd := exec.Command(cfg.Argv[0], cfg.Argv[1:]...)
	cmd.Dir = cfg.Cwd
	cmd.Env = Environment(cfg.Env, cfg.Home)
	cmd.Stdout = cfg.Log
	cmd.Stderr = cfg.Log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	o := &Owner{Binding: Binding{Home: cfg.Home, OwnerPID: cmd.Process.Pid}, cmd: cmd, dead: make(chan struct{}), grace: cfg.Grace}
	if o.grace <= 0 {
		o.grace = 3 * time.Second
	}
	go func() { _ = cmd.Wait(); close(o.dead) }()
	start, err := procstart.StartTime(cmd.Process.Pid)
	if err != nil || start <= 0 {
		o.Stop()
		return nil, errors.New("hermes native owner start identity unavailable")
	}
	o.Binding.OwnerStartTime = start
	ready, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	ticker := time.NewTicker(40 * time.Millisecond)
	defer ticker.Stop()
	for {
		e, err := identify(ready, o.Binding)
		if err == nil {
			o.Binding.InstanceID = e.InstanceID
			break
		}
		select {
		case <-o.dead:
			o.Stop()
			return nil, errors.New("hermes native owner exited before readiness")
		case <-ready.Done():
			o.Stop()
			return nil, fmt.Errorf("hermes native owner not ready: %w (%v)", ready.Err(), err)
		case <-ticker.C:
		}
	}
	c, err := connect(ready, o.Binding)
	if err == nil {
		defer func() { _ = c.CloseNow() }()
		var status struct {
			Configured bool `json:"provider_configured"`
		}
		err = rpc(ready, c, 1, "setup.status", map[string]any{}, &status)
		if err == nil && !status.Configured {
			err = errors.New("hermes native provider is not configured; use the native Hermes setup flow")
		}
		if err == nil {
			params := map[string]any{}
			for key, value := range cfg.Launch {
				params[key] = value
			}
			params["request_id"], params["source"], params["cwd"] = cfg.RequestID, "cli", cfg.Cwd
			method := "session.create"
			if cfg.ResumeID != "" {
				method = "session.resume"
				params = map[string]any{"session_id": cfg.ResumeID}
			}
			var session struct {
				ID string `json:"session_id"`
			}
			err = rpc(ready, c, 2, method, params, &session)
			if err == nil && session.ID == "" {
				err = errors.New("hermes native owner returned no session")
			}
			if err == nil {
				o.Binding.SessionID = session.ID
			}
		}
	}
	if err != nil {
		o.Stop()
		return nil, err
	}
	return o, nil
}

// Stop waits for this owner and always kills its own group after the grace, even
// if its leader exited first. Unrelated native gateways are never signalled.
func (o *Owner) Stop() {
	o.stop.Do(func() {
		_ = syscall.Kill(-o.cmd.Process.Pid, syscall.SIGTERM)
		timer := time.NewTimer(o.grace)
		select {
		case <-o.dead:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
		}
		_ = syscall.Kill(-o.cmd.Process.Pid, syscall.SIGKILL)
		<-o.dead
	})
}

func (o *Owner) Done() <-chan struct{} { return o.dead }
