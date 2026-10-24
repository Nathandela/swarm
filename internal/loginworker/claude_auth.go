package loginworker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/processcontain"
	"github.com/Nathandela/swarm/internal/procstart"
)

// ClaudeAuthConfig is immutable launch input. The Store intent is the only job
// progress/attempt authority. Callers must supply their qualified native pin.
type ClaudeAuthConfig = logins.ClaudeAuthWorkerConfig

func validateClaudeAuthConfig(c ClaudeAuthConfig) error {
	if c.NativeVersion != "2.1.310" || !validID(c.Ref.LoginID) || !validID(c.Ref.Nonce) || c.Ref.CredentialRevision == 0 {
		return ErrUnsupported
	}
	if _, err := safeAbsolute(c.StateRoot, true, true); err != nil {
		return err
	}
	if err := ValidateNativePath(c.NativePath); err != nil {
		return err
	}
	if c.NativeFingerprint == "" || !persist.MatchCLIFingerprint(c.NativePath, c.NativeFingerprint) {
		return ErrStale
	}
	return nil
}

// StartClaudeAuth launches one helper-specific unit. No discussion sharing the
// saved authority is stopped. It does not activate a login or publish adoption.
func StartClaudeAuth(ctx context.Context, executable string, c ClaudeAuthConfig) (logins.ClaudeAuthResult, error) {
	if err := ctx.Err(); err != nil {
		return logins.ClaudeAuthResult{}, err
	}
	if err := validateClaudeAuthConfig(c); err != nil {
		return logins.ClaudeAuthResult{}, err
	}
	image, err := openClaudeAuthImage(c)
	if err != nil {
		return logins.ClaudeAuthResult{}, err
	}
	_ = image.Close()
	if err := ValidateNativePath(executable); err != nil {
		return logins.ClaudeAuthResult{}, err
	}
	store, err := logins.OpenExisting(c.StateRoot)
	if err != nil {
		return logins.ClaudeAuthResult{}, err
	}
	defer func() { _ = store.Close() }()
	path, err := store.PrepareClaudeAuthWorker(c.Ref, c.NativePath, c.NativeVersion, c.NativeFingerprint)
	if err != nil {
		return logins.ClaudeAuthResult{}, err
	}
	result, err := store.ReadClaudeAuth(c.Ref)
	if err != nil {
		return result, err
	}
	scope, err := startClaudeAuthScope(ctx, executable, path, c.Ref, result.Deadline)
	if err != nil {
		return result, err
	}
	if err = store.BindClaudeAuthScope(c.Ref, scope); err != nil {
		return result, err
	} // retain unit if durable binding uncertain
	return store.ReadClaudeAuth(c.Ref)
}

// ClaudeAuthRecovery reports cleanup diagnostics, never authentication/adoption.
// Settled means the exact terminal budget remains retained; it is not a new claim.
type ClaudeAuthRecovery struct {
	Settled                bool
	MetadataCleanupUnknown bool
}

// RecoverClaudeAuth settles and collects exact custody while retaining ready or
// failed intent. Manager/boot changes remain explicit refusals. Only an explicit
// Store.RetireClaudeAuth removes the terminal budget.
func RecoverClaudeAuth(ctx context.Context, c ClaudeAuthConfig) (ClaudeAuthRecovery, error) {
	if err := ctx.Err(); err != nil {
		return ClaudeAuthRecovery{}, err
	}
	store, err := logins.OpenExisting(c.StateRoot)
	if err != nil {
		return ClaudeAuthRecovery{}, err
	}
	defer func() { _ = store.Close() }()
	result, err := store.ReadClaudeAuth(c.Ref)
	if err != nil {
		return ClaudeAuthRecovery{}, err
	}
	if result.Phase == "running" {
		if err := verifyClaudeAuthScopeRetired(ctx, result.Scope); err != nil {
			return ClaudeAuthRecovery{}, err
		}
		if err := store.RecordClaudeAuthScopeRetired(c.Ref, result.Attempt, result.Scope); err != nil {
			return ClaudeAuthRecovery{}, err
		}
	} else if err := store.PrepareClaudeAuthRecovery(c.Ref); err != nil {
		return ClaudeAuthRecovery{}, err
	}
	result, err = store.ReadClaudeAuth(c.Ref)
	if err != nil {
		return ClaudeAuthRecovery{}, err
	}
	if result.Scope == (logins.ClaudeAuthScope{}) {
		// shortcut: an unobserved late unit birth can leave harmless metadata after
		// exact unadmitted revocation; qualify a birth journal only if cleanup needs it.
		if _, err := store.SettleClaudeAuth(c.Ref); err != nil {
			return ClaudeAuthRecovery{}, err
		}
		return ClaudeAuthRecovery{Settled: true, MetadataCleanupUnknown: true}, nil
	}
	if !result.UnitRetired {
		if result.Phase == "retiring" && result.Scope.Worker.Alive() {
			if err := stopClaudeAuthRevokedWorker(ctx, result.Scope); err != nil {
				return ClaudeAuthRecovery{}, err
			}
		}
		if err := verifyClaudeAuthScopeRetired(ctx, result.Scope); err != nil {
			return ClaudeAuthRecovery{}, err
		}
		if err := store.RecordClaudeAuthUnitRetired(c.Ref, result.Scope); err != nil {
			return ClaudeAuthRecovery{}, err
		}
	}
	// The durable receipt precedes collection. Failed is published only after
	// collection and payload cleanup, so replacement cannot outrun either step.
	if err := collectClaudeAuthScope(ctx, result.Scope); err != nil {
		return ClaudeAuthRecovery{}, err
	}
	if _, err := store.SettleClaudeAuth(c.Ref); err != nil {
		return ClaudeAuthRecovery{}, err
	}
	return ClaudeAuthRecovery{Settled: true}, nil
}

func RunClaudeAuthConfig(ctx context.Context, path string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if _, err := safeAbsolute(path, false, true); err != nil {
		return err
	}
	anchor, err := safeAbsolute(filepath.Dir(path), true, true)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return ErrUnsafe
	}
	defer func() { _ = root.Close() }()
	files := &jobFiles{root: root, path: filepath.Dir(path), anchor: anchor}
	if err := files.safe(); err != nil {
		return err
	}
	raw, err := readPrivate(root, filepath.Base(path), 8192)
	if err != nil || len(raw) > 8192 {
		return ErrInvalid
	}
	var c ClaudeAuthConfig
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&c) != nil {
		return ErrInvalid
	}
	if decoder.Decode(new(any)) != io.EOF {
		return ErrInvalid
	}
	canonical, marshalErr := json.Marshal(c)
	if marshalErr != nil || !bytes.Equal(canonical, raw) {
		return ErrInvalid
	}
	if err := validateClaudeAuthConfig(c); err != nil {
		return err
	}
	expected := filepath.Join(c.StateRoot, "logins", "claude-auth", c.Ref.LoginID, c.Ref.Nonce, "worker.json")
	if path != expected {
		return ErrUnsafe
	}
	image, err := openClaudeAuthImage(c)
	if err != nil {
		return err
	}
	defer func() { _ = image.Close() }()
	store, err := logins.OpenExisting(c.StateRoot)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	started, err := procstart.StartTime(os.Getpid())
	if err != nil {
		return ErrUnavailable
	}
	worker := processcontain.Identity{PID: os.Getpid(), StartTime: started}
	// Strict preclaim: immutable reads + inert checks only. No provider process,
	// progress write, scratch write or saved-authority mutation before Claim.
	var result logins.ClaudeAuthResult
	for {
		result, err = store.ReadClaudeAuth(c.Ref)
		if err != nil {
			return err
		}
		if result.Phase != "prepared" || !time.Now().Before(result.Deadline) {
			return ErrStale
		}
		if result.Scope != (logins.ClaudeAuthScope{}) {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	if result.Scope.Worker != worker {
		return ErrStale
	}
	if err := checkClaudeAuthConfinement(result.Scope); err != nil {
		return err
	}
	if err := enableSubreaper(); err != nil {
		return err
	}
	ctx, cancel := context.WithDeadline(ctx, result.Deadline)
	defer cancel()
	for n := 0; n < 2; n++ {
		attempt, launch, err := store.ClaimClaudeAuthAttempt(c.Ref, worker)
		if err != nil {
			return err
		}
		err = runClaudeAuthAttempt(ctx, c, store, attempt, launch, result.Scope, image)
		latest, readErr := store.ReadClaudeAuth(c.Ref)
		if readErr != nil {
			return readErr
		}
		if latest.Phase == "retry" {
			continue
		}
		return err
	}
	return ErrUnavailable
}

func claudeAuthArguments() []string {
	return []string{"--safe-mode", "--await-claim", "--setting-sources", "", "-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--tools", ""}
}
func claudeAuthEnvelope(raw []byte) (map[string]json.RawMessage, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, false
	}
	fields := map[string]json.RawMessage{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, false
		}
		key, ok := token.(string)
		if !ok {
			return nil, false
		}
		if _, exists := fields[key]; exists {
			return nil, false
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, false
		}
		for _, routing := range []string{"type", "response", "subtype", "request_id"} {
			if strings.EqualFold(key, routing) && key != routing {
				return nil, false
			}
		}
		fields[key] = value
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, false
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, false
	}
	return fields, true
}
func claudeAuthInitialized(raw []byte) bool {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 4096), 200000)
	for scanner.Scan() {
		fields, ok := claudeAuthEnvelope(scanner.Bytes())
		if !ok {
			continue
		}
		var kind string
		if json.Unmarshal(fields["type"], &kind) != nil || kind != "control_response" {
			continue
		}
		response, ok := claudeAuthEnvelope(fields["response"])
		if !ok {
			continue
		}
		var subtype, id string
		if json.Unmarshal(response["subtype"], &subtype) == nil && json.Unmarshal(response["request_id"], &id) == nil && subtype == "success" && id == "swarm-auth-init" {
			return true
		}
	}
	return false
}

func claudeAuthImageCommand(image *os.File, argv0 string, args ...string) *exec.Cmd {
	cmd := exec.Command("/proc/self/fd/3", args...)
	cmd.Args[0] = argv0
	cmd.ExtraFiles = []*os.File{image}
	return cmd
}

// The default factory is fixed production policy. Only compiled test code
// replaces it to transport synthetic loopback TLS; no runtime override exists.
var claudeAuthNativeEnvironment = func(a logins.ClaudeAuthAttempt, home, tmp string) []string {
	return []string{"PATH=/usr/bin:/bin", "HOME=" + home, "TMPDIR=" + tmp, "CLAUDE_CONFIG_DIR=" + a.ConfigHome, "CLAUDE_SECURESTORAGE_CONFIG_DIR=" + a.AuthorityHome, "XDG_CONFIG_HOME=" + filepath.Join(home, ".config"), "XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"), "XDG_CACHE_HOME=" + filepath.Join(home, ".cache"), "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "DISABLE_AUTOUPDATER=1", "CLAUDE_CODE_SKIP_PROMPT_HISTORY=1"}
}

// observeClaudeAuthChild is called only after successful cmd.Start. It always
// captures birth metadata before allowing Wait to reap/recycle the PID, then
// unconditionally starts async Wait. Lookup failure never waits synchronously.
func observeClaudeAuthChild(cmd *exec.Cmd, inspect func(int) (int64, error)) (*nativeChild, error) {
	child := &nativeChild{cmd: cmd, done: make(chan error, 1)}
	started, err := inspect(cmd.Process.Pid)
	go func() { child.done <- cmd.Wait(); close(child.done) }()
	if err == nil {
		child.identity = ProcessIdentity{PID: cmd.Process.Pid, StartTime: started}
	}
	return child, err
}

// waitClaudeAuthOutput waits for os/exec pipe copies, not just native death.
// A contained descendant may have held stdout after the main process exited.
func waitClaudeAuthOutput(child *nativeChild) error {
	if child == nil {
		return nil
	}
	select {
	case <-child.done:
		return nil
	case <-time.After(500 * time.Millisecond):
		return ErrUnavailable
	}
}

func runClaudeAuthAttempt(ctx context.Context, c ClaudeAuthConfig, store *logins.Store, a logins.ClaudeAuthAttempt, launch *logins.ClaudeAuthLaunch, scope logins.ClaudeAuthScope, image *os.File) (err error) {
	if !persist.MatchCLIFingerprint(c.NativePath, c.NativeFingerprint) {
		_ = store.AbortClaudeAuthLaunch(launch, nil)
		return ErrStale
	}
	syscall.Umask(0o077)
	home := filepath.Join(a.ConfigHome, "home")
	tmp := filepath.Join(home, "tmp")
	for _, path := range []string{home, tmp} {
		if err := os.Mkdir(path, 0700); err != nil {
			_ = store.AbortClaudeAuthLaunch(launch, nil)
			return ErrUnsafe
		}
	}
	env := claudeAuthNativeEnvironment(a, home, tmp)
	cmd := claudeAuthImageCommand(image, c.NativePath, claudeAuthArguments()...)
	cmd.Dir = home
	cmd.Env = env
	cmd.SysProcAttr = nativeAttrs()
	output := &boundedBuffer{limit: 2 << 20}
	stderr := &boundedBuffer{limit: 64 << 10}
	cmd.Stdout = output
	cmd.Stderr = stderr
	input, err := cmd.StdinPipe()
	if err != nil {
		_ = store.AbortClaudeAuthLaunch(launch, nil)
		return ErrUnavailable
	}
	defer func() { _ = input.Close() }()
	if err := ctx.Err(); err != nil {
		_ = store.AbortClaudeAuthLaunch(launch, nil)
		return err
	}
	if err := launch.Handoff(); err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		if abortErr := store.AbortClaudeAuthLaunch(launch, logins.ClaudeAuthLaunchNotStarted(c.Ref, a.Number, err)); abortErr != nil {
			return abortErr
		}
		return ErrUnavailable
	}
	// A successful Start, including a fast unpublished exit, is never no-start.
	candidate := false
	child, startErr := observeClaudeAuthChild(cmd, procstart.StartTime)
	if startErr == nil {
		_ = store.RecordClaudeAuthNative(c.Ref, a.Number, child.identity)
	}

	defer func() {
		// Kill/reap through the exact kernel scope BEFORE waiting for cmd.Wait:
		// an unpublished fast main may leave descendants holding its pipes.
		if stopErr := containClaudeAuthScope(scope, cmd.Process.Pid); stopErr != nil {
			err = ErrUnavailable
			return
		}
		if drainErr := waitClaudeAuthOutput(child); drainErr != nil {
			candidate = false
			err = drainErr
		} else if reapErr := reapClaudeAuthScope(); reapErr != nil {
			candidate = false
			err = reapErr
		}
		_, lateOutputOverflow := output.Bytes()
		_, lateErrorOverflow := stderr.Bytes()
		if lateOutputOverflow || lateErrorOverflow || store.CheckClaudeAuthScratch(c.Ref, a.Number) != nil {
			candidate = false
			err = ErrInvalid
		}
		if stopErr := store.RecordClaudeAuthScopeContained(c.Ref, a.Number, scope); stopErr != nil {
			err = stopErr
			return
		}
		if !candidate {
			if failureErr := store.FailClaudeAuthAttempt(c.Ref, a.Number); failureErr != nil {
				err = failureErr
			}
			return
		}
		_, finishErr := store.FinishClaudeAuthAttempt(c.Ref, a.Number)
		if finishErr != nil {
			err = finishErr
		}
	}()
	if startErr != nil {
		return ErrUnavailable
	}
	if _, err := io.WriteString(input, "{\"type\":\"control_request\",\"request_id\":\"swarm-auth-init\",\"request\":{\"subtype\":\"initialize\"}}\n"); err != nil {
		return ErrUnavailable
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		raw, exceeded := output.Bytes()
		_, stderrExceeded := stderr.Bytes()
		if exceeded || stderrExceeded || store.CheckClaudeAuthScratch(c.Ref, a.Number) != nil {
			return ErrInvalid
		}
		if claudeAuthInitialized(raw) && store.ObserveClaudeAuthProfile(c.Ref, a.Number) == nil {
			candidate = true
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-child.done:
			return ErrUnavailable
		case <-ticker.C:
		}
	}
}
