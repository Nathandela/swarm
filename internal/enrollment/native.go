package enrollment

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/appserver"
	"github.com/Nathandela/swarm/internal/procstart"
	"github.com/Nathandela/swarm/internal/vt"
	"github.com/creack/pty"
)

type boundedBuffer struct {
	mu       sync.Mutex
	data     []byte
	limit    int
	exceeded bool
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(data)
	remaining := b.limit - len(b.data)
	if n > remaining {
		b.exceeded = true
		data = data[:remaining]
	}
	b.data = append(b.data, data...)
	return n, nil
}
func (b *boundedBuffer) Bytes() ([]byte, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.data...), b.exceeded
}

type nativeChild struct {
	cmd      *exec.Cmd
	identity ProcessIdentity
	done     chan error
}

func (w *loginWorker) command(args ...string) *exec.Cmd {
	cmd := exec.Command(w.cfg.NativePath, args...)
	cmd.Dir = w.files.path
	cmd.Env = NativeEnvironment(w.cfg, os.Environ())
	cmd.SysProcAttr = nativeAttrs()
	return cmd
}

func startChild(cmd *exec.Cmd) (*nativeChild, error) {
	if err := cmd.Start(); err != nil {
		return nil, ErrUnavailable
	}
	start, err := procstart.StartTime(cmd.Process.Pid)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, ErrUnavailable
	}
	child := &nativeChild{cmd: cmd, identity: ProcessIdentity{PID: cmd.Process.Pid, StartTime: start}, done: make(chan error, 1)}
	go func() { child.done <- cmd.Wait(); close(child.done) }()
	return child, nil
}

func (child *nativeChild) stop() error { return stopNative(child.cmd, child.identity, child.done) }

func (w *loginWorker) capture(ctx context.Context, limit int, args ...string) ([]byte, error) {
	cmd := w.command(args...)
	output := &boundedBuffer{limit: limit}
	cmd.Stdout, cmd.Stderr = output, io.Discard
	child, err := startChild(cmd)
	if err != nil {
		return nil, err
	}
	defer func() { _ = child.stop() }()
	if err := w.publish(PhaseVerifying, []ProcessIdentity{child.identity}, nil); err != nil {
		return nil, err
	}
	select {
	case err := <-child.done:
		if err != nil {
			return nil, ErrUnavailable
		}
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	raw, exceeded := output.Bytes()
	if exceeded {
		return nil, ErrInvalid
	}
	return raw, nil
}

func runLogin(ctx context.Context, cfg Config, files *jobFiles, worker ProcessIdentity) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	runnerStart, err := procstart.StartTime(os.Getpid())
	if err != nil {
		return ErrUnavailable
	}
	w := &loginWorker{cfg: cfg, files: files, cancel: cancel, status: LiveStatus{Progress: Progress{SchemaVersion: SchemaVersion, JobID: cfg.JobID, Generation: cfg.Generation, Phase: PhaseStarting, Worker: worker, Runner: ProcessIdentity{PID: os.Getpid(), StartTime: runnerStart}}}}
	if err := w.publish(PhaseStarting, nil, nil); err != nil {
		return err
	}
	if err := w.serve(); err != nil {
		_ = w.publish(PhaseFailed, nil, nativeFailure("worker_control_socket_failed"))
		return err
	}
	defer func() { _ = w.ln.Close(); w.endTerminal() }()
	defer func() {
		cleanupErr := containDescendants(os.Getpid())
		reapOrphans()
		w.mu.Lock()
		defer w.mu.Unlock()
		w.status.Progress.NativeWritersStopped = cleanupErr == nil
		w.status.Progress.UpdatedAt = time.Now().UTC()
		if cleanupErr != nil {
			w.status.Progress.Phase = PhaseFailed
			w.status.Progress.ErrorCode = "writers_still_live"
		}
		if files.canceled(cfg.Generation) {
			w.status.Progress.Phase = PhaseCanceled
			w.status.Progress.ErrorCode = ""
		}
		_ = files.write(ProgressFile, w.status.Progress)
	}()
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-watchDone:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if files.canceled(cfg.Generation) || !worker.Alive() {
					cancel()
					return
				}
			}
		}
	}()
	if files.canceled(cfg.Generation) {
		_ = w.publish(PhaseCanceled, nil, nil)
		return ErrCanceled
	}
	// Fresh native-login candidates must contain no copied configuration or
	// authentication source. Import is a distinct daemon-owned operation.
	profilePath := filepath.Join(cfg.StateRoot, "accounts", "profiles", cfg.CandidateProfileGeneration)
	entries, err := os.ReadDir(profilePath)
	if err == nil {
		for _, entry := range entries {
			if entry.Name() != ".swarm-candidate.json" {
				err = ErrUnsafe
				break
			}
		}
	}
	if err == nil {
		versionCtx, stop := context.WithTimeout(ctx, 5*time.Second)
		var version []byte
		version, err = w.capture(versionCtx, 4096, "--version")
		stop()
		if err == nil && !versionMatches(cfg.Provider, cfg.NativeVersion, string(version)) {
			err = ErrUnsupported
		}
		if err != nil {
			_ = w.publish(PhaseFailed, nil, nativeFailure("native_version_probe_failed"))
			return err
		}
	}
	if err == nil {
		if cfg.Provider == "codex" {
			err = w.codex(ctx)
		} else {
			err = w.claude(ctx)
		}
	}
	if err == nil && (files.canceled(cfg.Generation) || ctx.Err() != nil) {
		err = ErrCanceled
	}
	if files.canceled(cfg.Generation) {
		_ = w.publish(PhaseCanceled, nil, nil)
		return ErrCanceled
	}
	if err != nil {
		_ = w.publish(PhaseFailed, nil, err)
		return err
	}
	return w.publish(PhaseReady, nil, nil)
}

func versionMatches(provider, version, banner string) bool {
	fields := strings.Fields(strings.TrimSpace(banner))
	if provider == "codex" {
		return len(fields) == 2 && fields[0] == "codex-cli" && fields[1] == version
	}
	return len(fields) >= 1 && fields[0] == version && strings.TrimSpace(banner) == version+" (Claude Code)"
}

func plain(value string, max int) bool {
	if value == "" || len(value) > max {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

type deviceStart struct {
	Type            string `json:"type"`
	LoginID         string `json:"loginId"`
	VerificationURL string `json:"verificationUrl"`
	UserCode        string `json:"userCode"`
}

type completion struct {
	LoginID string `json:"loginId"`
	Success bool   `json:"success"`
}

func validateCompletion(start deviceStart, completed completion) error {
	if completed.LoginID != "" && completed.LoginID != start.LoginID {
		return ErrStale
	}
	if !completed.Success {
		return ErrUnavailable
	}
	return nil
}

func codexSocketPath(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != uint32(os.Getuid()) {
			return "", ErrUnsafe
		}
		// Codex 0.160 publishes an alias to this deterministic, owner-private
		// socket after binding it (app-server-transport/src/transport/unix_socket.rs).
		tmp, err := filepath.EvalSymlinks("/tmp")
		if err != nil {
			return "", ErrUnsafe
		}
		dir := filepath.Join(tmp, fmt.Sprintf("codex-daemon-%d", os.Getuid()))
		expected := filepath.Join(dir, fmt.Sprintf("%x", sha256.Sum256([]byte(path))))
		target, err := os.Readlink(path)
		if err != nil || target != expected {
			return "", ErrUnsafe
		}
		if _, err := safeAbsolute(dir, true, true); err != nil {
			return "", err
		}
		path = target
		info, err = os.Lstat(path)
		if err != nil {
			return "", ErrUnsafe
		}
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o600 {
		return "", ErrUnsafe
	}
	return path, nil
}

func (w *loginWorker) codex(ctx context.Context) error {
	socketPath := filepath.Join(w.files.path, "native.sock")
	cmd := w.command("-c", "cli_auth_credentials_store=\"file\"", "app-server", "--listen", "unix://"+socketPath)
	child, err := startChild(cmd)
	if err != nil {
		return nativeFailure("codex_child_start_failed")
	}
	defer func() { _ = child.stop() }()
	if err := w.publish(PhaseStarting, []ProcessIdentity{child.identity}, nil); err != nil {
		return err
	}
	ready := time.NewTimer(10 * time.Second)
	defer ready.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		physical, err := codexSocketPath(socketPath)
		if err == nil {
			socketPath = physical
			break
		}
		if !os.IsNotExist(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-child.done:
			return nativeFailure("codex_child_exited_before_socket")
		case <-ready.C:
			return ErrUnavailable
		case <-ticker.C:
		}
	}
	completed := make(chan completion, 4)
	client, err := appserver.Dial(ctx, socketPath, appserver.Options{DialTimeout: 3 * time.Second, OnNotify: func(method string, raw json.RawMessage) {
		if method != "account/login/completed" || len(raw) > 16<<10 {
			return
		}
		var value completion
		if json.Unmarshal(raw, &value) == nil {
			select {
			case completed <- value:
			default:
				cancel := w.cancel
				cancel()
			}
		}
	}})
	if err != nil {
		return nativeFailure("codex_transport_failed")
	}
	defer func() { _ = client.Close() }()
	rpcCtx, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	var initialized json.RawMessage
	if err := client.Call(rpcCtx, "initialize", map[string]any{"clientInfo": map[string]string{"name": "swarm-account-enrollment", "version": "1"}}, &initialized); err != nil {
		return nativeFailure("codex_initialize_failed")
	}
	if err := client.Notify(rpcCtx, "initialized", map[string]any{}); err != nil {
		return ErrUnavailable
	}
	var before struct {
		Account json.RawMessage `json:"account"`
	}
	if err := client.Call(rpcCtx, "account/read", map[string]bool{"refreshToken": false}, &before); err != nil || string(before.Account) != "null" {
		return ErrUnsafe
	}
	if w.files.canceled(w.cfg.Generation) {
		return ErrCanceled
	}
	// Exactly one login/start per server/worker generation. Its notifications may
	// arrive before this reply and are retained in the generation-local channel.
	var start deviceStart
	if err := client.Call(rpcCtx, "account/login/start", map[string]string{"type": "chatgptDeviceCode"}, &start); err != nil {
		return nativeFailure("codex_login_start_failed")
	}
	verification, err := url.Parse(start.VerificationURL)
	if err != nil || verification.Scheme != "https" || verification.Host == "" || verification.User != nil || !plain(start.VerificationURL, 4096) || !plain(start.UserCode, 128) || !plain(start.LoginID, 256) || start.Type != "chatgptDeviceCode" {
		return ErrInvalid
	}
	w.mu.Lock()
	w.status.Device = &Device{VerificationURL: start.VerificationURL, UserCode: start.UserCode}
	w.mu.Unlock()
	if err := w.publish(PhaseAuthenticating, []ProcessIdentity{child.identity}, nil); err != nil {
		return err
	}
	select {
	case value := <-completed:
		if err := validateCompletion(start, value); err != nil {
			return err
		}
	case <-ctx.Done():
		cancelCtx, stop := context.WithTimeout(context.Background(), time.Second)
		var ignored json.RawMessage
		_ = client.Call(cancelCtx, "account/login/cancel", map[string]string{"loginId": start.LoginID}, &ignored)
		stop()
		return ctx.Err()
	case <-child.done:
		return nativeFailure("codex_child_exited_during_login")
	case <-client.Done():
		return ErrUnavailable
	}
	if w.files.canceled(w.cfg.Generation) || ctx.Err() != nil {
		return ErrCanceled
	}
	var account struct {
		Account *struct {
			Type  string `json:"type"`
			Email string `json:"email"`
			Plan  string `json:"planType"`
		} `json:"account"`
	}
	readCtx, stopRead := context.WithTimeout(ctx, 5*time.Second)
	err = client.Call(readCtx, "account/read", map[string]bool{"refreshToken": false}, &account)
	stopRead()
	if err != nil || account.Account == nil || account.Account.Type != "chatgpt" {
		return ErrInvalid
	}
	w.mu.Lock()
	if plain(account.Account.Email, 1024) {
		w.status.Email = account.Account.Email
	}
	if plain(account.Account.Plan, 128) {
		w.status.Plan = account.Account.Plan
	}
	w.status.Device = nil
	w.mu.Unlock()
	if err := w.publish(PhaseVerifying, []ProcessIdentity{child.identity}, nil); err != nil {
		return err
	}
	_ = client.Close()
	return child.stop()
}

func (w *loginWorker) claude(ctx context.Context) error {
	cmd := w.command("auth", "login", "--claudeai")
	cmd.SysProcAttr = nativePTYAttrs()
	ptmx, err := pty.StartWithAttrs(cmd, &pty.Winsize{Cols: 80, Rows: 24}, cmd.SysProcAttr)
	if err != nil {
		return nativeFailure("claude_pty_start_failed")
	}
	start, err := procstart.StartTime(cmd.Process.Pid)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = ptmx.Close()
		return ErrUnavailable
	}
	child := &nativeChild{cmd: cmd, identity: ProcessIdentity{PID: cmd.Process.Pid, StartTime: start}, done: make(chan error, 1)}
	go func() { child.done <- cmd.Wait(); close(child.done) }()
	defer func() { _ = child.stop(); w.endTerminal() }()
	w.termMu.Lock()
	w.ptmx = ptmx
	w.emu = vt.NewEmulator(80, 24)
	w.termMu.Unlock()
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				w.feed(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	if err := w.publish(PhaseAuthenticating, []ProcessIdentity{child.identity}, nil); err != nil {
		return err
	}
	select {
	case err := <-child.done:
		if err != nil {
			return nativeFailure("claude_login_child_failed")
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := child.stop(); err != nil {
		return err
	}
	w.endTerminal()
	if w.files.canceled(w.cfg.Generation) || ctx.Err() != nil {
		return ErrCanceled
	}
	statusCtx, stop := context.WithTimeout(ctx, 5*time.Second)
	raw, err := w.capture(statusCtx, 64<<10, "auth", "status", "--json")
	stop()
	if err != nil {
		return err
	}
	var status struct {
		LoggedIn         bool   `json:"loggedIn"`
		AuthMethod       string `json:"authMethod"`
		SubscriptionType string `json:"subscriptionType"`
		Email            string `json:"email"`
	}
	if json.Unmarshal(raw, &status) != nil || !status.LoggedIn || status.AuthMethod != "claude.ai" || (status.SubscriptionType != "pro" && status.SubscriptionType != "max") {
		return ErrInvalid
	}
	w.mu.Lock()
	if plain(status.Email, 1024) {
		w.status.Email = status.Email
	}
	w.status.Plan = status.SubscriptionType
	w.mu.Unlock()
	return nil
}

func verifyNativeFiles(cfg Config) error {
	path := filepath.Join(cfg.StateRoot, "accounts", "profiles", cfg.CandidateProfileGeneration)
	if _, err := safeAbsolute(path, true, true); err != nil {
		return err
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return ErrUnsafe
	}
	defer func() { _ = root.Close() }()
	if cfg.Provider == "codex" {
		raw, err := readPrivate(root, "auth.json", 1<<20)
		if err != nil {
			return ErrUnsafe
		}
		if _, err := accounts.CodexIdentity(raw); err != nil {
			return ErrInvalid
		}
		var auth struct {
			APIKey string `json:"OPENAI_API_KEY"`
			Tokens struct {
				Access  string `json:"access_token"`
				Refresh string `json:"refresh_token"`
			} `json:"tokens"`
		}
		if json.Unmarshal(raw, &auth) != nil || auth.APIKey != "" || auth.Tokens.Access == "" || auth.Tokens.Refresh == "" {
			return ErrInvalid
		}
		return nil
	}
	raw, err := readPrivate(root, ".claude.json", 1<<20)
	if err != nil {
		return ErrUnsafe
	}
	if _, err := accounts.ClaudeIdentity(raw); err != nil {
		return ErrInvalid
	}
	credentials, err := readPrivate(root, ".credentials.json", 1<<20)
	if err != nil {
		return ErrUnsafe
	}
	var auth struct {
		OAuth struct {
			Access       string   `json:"accessToken"`
			Refresh      string   `json:"refreshToken"`
			Subscription string   `json:"subscriptionType"`
			Scopes       []string `json:"scopes"`
		} `json:"claudeAiOauth"`
	}
	if json.Unmarshal(credentials, &auth) != nil || auth.OAuth.Access == "" || auth.OAuth.Refresh == "" || (auth.OAuth.Subscription != "pro" && auth.OAuth.Subscription != "max") {
		return ErrInvalid
	}
	for _, scope := range auth.OAuth.Scopes {
		if scope == "user:inference" {
			return nil
		}
	}
	return ErrInvalid
}
