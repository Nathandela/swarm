package shim

// Structured stdout capture serves both the dev fixture and the explicit managed chat.
import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/procstart"
	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/wire"
	"golang.org/x/sys/unix"
)

const (
	claudeChatFrames      = 64
	claudeChatBytes       = 1 << 20
	claudeChatCaptureFile = "claude-capture.json"
)

var errClaudeChatCapture = errors.New("shim: Claude primary capture incomplete")

// ValidateClaudeChatConfig checks carriage; owned native birth separately validates reservation and image custody.
func ValidateClaudeChatConfig(cfg Config) error {
	if (cfg.RequiredRuntime == "" || cfg.RequiredRuntime == persist.RequiredRuntimeClaudeNativeControl) && cfg.ClaudeChatFixture == "" {
		return nil
	}
	if cfg.RequiredRuntime == persist.RequiredRuntimeClaudeChat && cfg.ClaudeChatFixture == "" {
		return validateClaudeNativeChatConfig(cfg)
	}
	if cfg.RequiredRuntime != persist.RequiredRuntimeClaudeChat || !adapter.IsCanonicalConversationID(cfg.ConversationID) || !filepath.IsAbs(cfg.ClaudeChatFixture) || filepath.Clean(cfg.ClaudeChatFixture) != cfg.ClaudeChatFixture || len(cfg.Argv) != 3 || cfg.Argv[0] != cfg.ClaudeChatFixture || cfg.Argv[1] != "--claude-primary-fixture" || cfg.Argv[2] != "--session-id="+cfg.ConversationID || cfg.LoginRuntime != nil || cfg.CLIIdentity != nil || cfg.NativeProvider != "" || cfg.LoginSwitchID != "" || cfg.LoginSwitchSource != "" || cfg.Backend != nil || cfg.OpenCode != nil || cfg.HermesInput != nil || cfg.AgyInput != nil || cfg.ClaudeInput || cfg.InputEmbargo != "" || cfg.InputEmbargoToken != "" || !persist.ValidID(cfg.SessionID) || cfg.HookSpoolMaxBytes > hookSpoolDefaultMaxBytes {
		return errors.New("shim: Claude primary fixture configuration refused")
	}
	return nil
}

// The existing strict storage validator classifies the fixture row. This
// read adds only exact config/birth binding; it does not create native custody.
func validateClaudeChatReservation(cfg Config, shimStart int64) error {
	if !filepath.IsAbs(cfg.LoginStateRoot) || cfg.SessionDir != filepath.Join(cfg.LoginStateRoot, cfg.SessionID) {
		return errClaudeChatCapture
	}
	state, err := os.OpenRoot(cfg.LoginStateRoot)
	if err != nil {
		return errClaudeChatCapture
	}
	defer func() { _ = state.Close() }()
	dir, err := state.Lstat(cfg.SessionID)
	owner, ok := infoSys(dir)
	if err != nil || !ok || !dir.IsDir() || dir.Mode().Perm()&0077 != 0 || owner.Uid != uint32(os.Getuid()) {
		return errClaudeChatCapture
	}
	root, err := os.OpenRoot(cfg.SessionDir)
	if err != nil {
		return errClaudeChatCapture
	}
	defer func() { _ = root.Close() }()
	opened, err := root.Lstat(".")
	if err != nil || !os.SameFile(dir, opened) {
		return errClaudeChatCapture
	}
	f, err := root.OpenFile("meta.json", os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return errClaudeChatCapture
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	owner, ok = infoSys(info)
	if err != nil || !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || owner.Uid != uint32(os.Getuid()) || owner.Nlink != 1 || info.Size() > 1<<20 {
		return errClaudeChatCapture
	}
	raw, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 || logins.ValidateClaudeChatFixtureMetadata(raw, cfg.SessionID) != nil {
		return errClaudeChatCapture
	}
	var m persist.Meta
	if json.Unmarshal(raw, &m) != nil || m.ID != cfg.SessionID || m.RequiredRuntime != cfg.RequiredRuntime || m.ConversationID != cfg.ConversationID || m.ProviderCwd() != cfg.Cwd || m.Status.Process != status.ProcessRunning || m.ShimPID == 0 && m.ShimStartTime != 0 || m.ShimPID != 0 && (m.ShimPID != os.Getpid() || m.ShimStartTime != shimStart) {
		return errClaudeChatCapture
	}
	return nil
}

// One stdout frame has one shim-authored envelope. Raw preserves all bytes
// except exactly one terminal LF. The identity is captured before readers start.
type claudeChatPrimaryIdentity = shimwire.ClaudePrimaryIdentity

type claudeChatEnvelope struct {
	Version         int                       `json:"v"`
	Origin          string                    `json:"origin"`
	NativeSessionID string                    `json:"native_session_id"`
	Primary         claudeChatPrimaryIdentity `json:"primary"`
	ReadMonotonicNS int64                     `json:"read_monotonic_ns"`
	StreamSequence  uint64                    `json:"stream_sequence"`
	Raw             []byte                    `json:"raw"`
}

type claudeChatFrame struct {
	body     []byte
	sequence uint64
}

type claudeChatCapture struct {
	admission                     sync.Mutex
	partial                       []byte
	oversized                     bool
	readerRevision, ownerRevision uint64
	stdin, stdout                 *os.File
	durableSequence               uint64     // capture.mu; successful contiguous Append prefix
	signalMu                      sync.Mutex // held THROUGH onSignal; retirement joins entered calls
	signalsRetired                bool
	mu                            sync.Mutex
	frames, bytes                 int // queued PLUS the frame currently inside Append
	lost                          bool
	eof                           [2]bool
	sequence                      uint64
	identity                      claudeChatPrimaryIdentity
	queue                         chan claudeChatFrame
	done                          chan struct{}
	spool                         *HookSpool
	server                        *server
	native                        bool
	initializeRequestID           string // capture.mu
	initializeSequence            uint64
	initializeWritten             bool
	initializeState               string
	nativeInputBlocked            bool
	lifetimeLoss                  *atomic.Bool
	stderrBytes                   uint64
	stderrTruncated               bool
	readinessMu                   sync.Mutex
	readinessRead, readinessWrite *os.File
	readinessCancelled            bool
}

func newClaudeChatCapture(spool *HookSpool, srv *server, identity claudeChatPrimaryIdentity) *claudeChatCapture {
	c := &claudeChatCapture{queue: make(chan claudeChatFrame, claudeChatFrames), done: make(chan struct{}), spool: spool, server: srv, identity: identity}
	go func() {
		defer close(c.done)
		for frame := range c.queue {
			_, appendErr := c.spool.Append(frame.body)
			if appendErr != nil {
				c.fail()
			}
			c.mu.Lock()
			if appendErr == nil && frame.sequence == c.durableSequence+1 {
				c.durableSequence = frame.sequence
			}
			c.frames--
			c.bytes -= len(frame.body)
			c.mu.Unlock()
		}
	}()
	return c
}

func (c *claudeChatCapture) captureLost() bool {
	return c.lost || c.lifetimeLoss != nil && c.lifetimeLoss.Load()
}

func (c *claudeChatCapture) fail() {
	c.mu.Lock()
	first := !c.lost
	c.lost = true
	if c.lifetimeLoss != nil {
		c.lifetimeLoss.Store(true)
	}
	c.mu.Unlock()
	if first {
		c.signalMu.Lock()
		if !c.signalsRetired {
			c.server.onSignal(shimwire.SigKill)
		}
		c.signalMu.Unlock()
	}
}

// Retire before final containment. Loss still latches afterward, but returning
// proves that neither a copied callback nor an entered onSignal can run later.
func (c *claudeChatCapture) retireSignals() {
	c.signalMu.Lock()
	c.signalsRetired = true
	c.signalMu.Unlock()
}

func encodeClaudeChatEnvelope(identity claudeChatPrimaryIdentity, raw []byte, sequence uint64, readNS int64) ([]byte, error) {
	body, err := json.Marshal(claudeChatEnvelope{Version: 1, Origin: "claude_primary", NativeSessionID: identity.NativeSessionID, Primary: identity, ReadMonotonicNS: readNS, StreamSequence: sequence, Raw: raw})
	if err != nil || len(raw) > claudeChatBytes || len(body) > wire.MaxFrame-1 {
		return nil, errClaudeChatCapture
	}
	return body, nil
}

func (c *claudeChatCapture) admit(raw []byte) bool {
	if len(raw) > claudeChatBytes {
		c.fail()
		return false
	}
	var clock unix.Timespec
	if unix.ClockGettime(unix.CLOCK_MONOTONIC, &clock) != nil || clock.Nano() <= 0 {
		c.fail()
		return false
	}
	c.mu.Lock()
	body, err := encodeClaudeChatEnvelope(c.identity, raw, c.sequence+1, clock.Nano())
	if err != nil || c.sequence == ^uint64(0) || c.captureLost() || c.frames == claudeChatFrames || len(body) > claudeChatBytes-c.bytes {
		c.mu.Unlock()
		c.fail()
		return false
	}
	c.sequence++
	invalidNative := c.native && !c.observeNativeInitialize(raw, c.sequence)
	c.frames++
	c.bytes += len(body)
	c.queue <- claudeChatFrame{body: body, sequence: c.sequence} // capacity reserved under the same lock; never waits on fsync
	c.mu.Unlock()
	if invalidNative {
		c.fail()
	}
	return !invalidNative
}

func (c *claudeChatCapture) readStdout(r io.Reader) {
	defer c.closeReadiness()
	var buf [32 << 10]byte
	for {
		_, err := c.consumeStdout(r, buf[:])
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EINTR) {
			if err := c.waitReadiness(); err != nil {
				if !errors.Is(err, context.Canceled) {
					c.fail()
				}
				return
			}
			continue
		}
		if err != nil {
			return
		}
	}
}

// Drain continuously; retain only the diagnostic tail, never primary frames.
func (c *claudeChatCapture) readStderr(r io.Reader, diagnostic io.Writer) {
	const retained = 64 << 10
	var buf [32 << 10]byte
	tail := make([]byte, 0, retained)
	for {
		n, err := r.Read(buf[:])
		if n > 0 {
			c.mu.Lock()
			if uint64(n) > ^uint64(0)-c.stderrBytes {
				c.mu.Unlock()
				c.fail()
				return
			}
			c.stderrBytes += uint64(n)
			c.stderrTruncated = c.stderrBytes > retained
			c.mu.Unlock()
			if len(tail)+n > retained {
				tail = tail[len(tail)+n-retained:]
			}
			tail = append(tail, buf[:n]...)
		}
		if err != nil {
			if len(tail) > 0 {
				written, writeErr := diagnostic.Write(tail)
				if writeErr != nil || written != len(tail) {
					c.fail()
				}
			}
			if errors.Is(err, io.EOF) {
				c.mu.Lock()
				c.eof[1] = true
				c.mu.Unlock()
			} else {
				c.fail()
			}
			return
		}
	}
}

func (c *claudeChatCapture) result() ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	body, err := json.Marshal(struct {
		StdoutEOF       bool   `json:"stdout_eof"`
		StderrEOF       bool   `json:"stderr_eof"`
		Loss            bool   `json:"loss"`
		StderrBytes     uint64 `json:"stderr_bytes"`
		StderrTruncated bool   `json:"stderr_truncated"`
	}{c.eof[0], c.eof[1], c.captureLost(), c.stderrBytes, c.stderrTruncated})
	if c.captureLost() || !c.eof[0] || !c.eof[1] {
		err = errors.Join(err, errClaudeChatCapture)
	}
	return body, err
}

// No relay may consume this channel before the final synchronous gate. A
// successful Start transfers exactly one Wait to Run; cancellation starts none.
func startClaudeChatPrimary(cmd *exec.Cmd, signals <-chan os.Signal) error {
	select {
	case <-signals:
		return context.Canceled
	default:
	}
	return cmd.Start()
}

func runClaudeChatPrimary(cfg Config, sigCh <-chan os.Signal) (agentExit int, runErr error) {
	shimStart, err := procstart.StartTime(os.Getpid())
	if err != nil || shimStart <= 0 {
		return 0, errClaudeChatCapture
	}
	if err := validateClaudeChatReservation(cfg, shimStart); err != nil {
		return 0, err
	}
	fixtureEnv := make([]string, 0, len(cfg.Env)+1)
	for _, entry := range cfg.Env {
		if !strings.HasPrefix(entry, "SWARM_CLAUDE_PRIMARY_FIXTURE_DIR=") {
			fixtureEnv = append(fixtureEnv, entry)
		}
	}
	cfg.Env = append(fixtureEnv, "SWARM_CLAUDE_PRIMARY_FIXTURE_DIR="+cfg.SessionDir)
	return runClaudeChatLifetime(cfg, sigCh, nil, nil, nil)
}

func runClaudeChatLifetime(cfg Config, sigCh <-chan os.Signal, birth *claudeBirth, scope *managedNativeScope, retained *server) (agentExit int, runErr error) {
	if (birth == nil) != (scope == nil) {
		return 0, errLoginResolution
	}
	shimStart, err := procstart.StartTime(os.Getpid())
	if err != nil || shimStart <= 0 {
		return 0, errClaudeChatCapture
	}
	spool, err := OpenHookSpoolProducer(filepath.Join(cfg.SessionDir, HookSpoolFile), cfg.HookSpoolMaxBytes)
	if err != nil {
		return 0, err
	}
	defer func() { runErr = errors.Join(runErr, spool.Close()) }()
	srv := retained
	if srv == nil {
		srv = newServer(nil, cfg.SocketPath, nil, nil, nil, 0, cfg.GraceTimeout, cfg.Metrics)
	}
	var controlDone chan struct{}
	if birth == nil {
		listener, err := listen(cfg.SocketPath)
		if err != nil {
			return 0, err
		}
		srv.listener, srv.claudeChat = listener, true
		controlDone = make(chan struct{})
		go func() { defer close(controlDone); srv.acceptLoop() }()
	} else if retained == nil || srv.claudeStopBinding == nil || srv.claudeStopDone == nil {
		return 0, errLoginResolution
	}
	var controlStopOnce sync.Once
	stopControl := func() {
		controlStopOnce.Do(func() {
			if retained != nil {
				srv.stopClaudeChatControl()
				return
			}
			if controlDone != nil {
				srv.shutdown(shimwire.Control{})
				<-controlDone
			}
		})
	}
	var hooks *hookServer
	var hookDone chan struct{}
	var lifetimeLoss atomic.Bool
	defer func() {
		if lifetimeLoss.Load() {
			runErr = errors.Join(runErr, errClaudeChatCapture)
		}
	}()
	defer func() {
		if hooks != nil {
			hooks.stopAndJoin()
			<-hookDone
		}
	}()
	var capture *claudeChatCapture
	var containmentOnce sync.Once
	finishContainment := func() {
		containmentOnce.Do(func() {
			stopControl()
			if capture != nil {
				capture.retireSignals()
			}
			if hooks != nil {
				hooks.claudeOrigin.retire()
			}
			if retained != nil {
				srv.finishClaudeChatControl()
			} else {
				srv.finishEscalation()
			}
		})
	}
	defer finishContainment()
	// Explicit pipes make Wait independent of persistence/readers. os/exec owns
	// no copying goroutine and only this function calls Wait, exactly once.
	var ends []*os.File
	defer func() {
		for _, f := range ends {
			_ = f.Close()
		}
	}()
	pipe := func() (*os.File, *os.File, error) {
		r, w, e := os.Pipe()
		if e == nil {
			ends = append(ends, r, w)
		}
		return r, w, e
	}
	inR, inW, err := pipe()
	if err != nil {
		return 0, err
	}
	outR, outW, err := pipe()
	if err != nil {
		return 0, err
	}
	errR, errW, err := pipe()
	if err != nil {
		return 0, err
	}
	if err := claudePipeNonblock(inW); err != nil {
		return 0, err
	}
	if err := claudePipeNonblock(outR); err != nil {
		return 0, err
	}
	cmd := &exec.Cmd{Path: cfg.Argv[0], Args: cfg.Argv, Env: buildEnv(cfg.Env), Dir: cfg.Cwd, Stdin: inR, Stdout: outW, Stderr: errW, SysProcAttr: &syscall.SysProcAttr{Setpgid: true}}
	sigDone, sigJoined := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(sigJoined)
		select {
		case <-sigCh:
			srv.onSignal(shimwire.SigTerm)
		case <-sigDone:
		}
	}()
	var signalStopOnce sync.Once
	stopSignals := func() { signalStopOnce.Do(func() { close(sigDone); <-sigJoined }) }
	defer stopSignals()
	if birth != nil {
		if cfg.HookSocketPath == "" {
			return 0, errClaudeChatCapture
		}
		l, err := listen(cfg.HookSocketPath)
		if err != nil {
			return 0, err
		}
		hooks = newHookServer(l, spool, cfg.HookToken)
		identity := claudeChatPrimaryIdentity{SessionID: cfg.SessionID, RequiredRuntime: cfg.RequiredRuntime, NativeSessionID: cfg.ConversationID, ShimPID: os.Getpid(), ShimStartTime: shimStart}
		if err := hooks.bindClaudeChatOrigin(identity, scope.preclaim, &lifetimeLoss, srv); err != nil {
			_ = l.Close()
			hooks = nil
			return 0, err
		}
		hookDone = make(chan struct{})
		go func() { defer close(hookDone); hooks.acceptLoop() }()
	}
	if testHookAfterSignalArm != nil {
		testHookAfterSignalArm()
	}
	var startErr error
	if birth != nil {
		startErr = birth.startStdio(scope, cfg, cmd, srv, sigCh)
	} else {
		startErr = srv.startClaudeChatPrimary(cmd, sigCh)
	}
	if startErr != nil && cmd.Process == nil {
		return 0, startErr
	}
	_ = inR.Close()
	_ = outW.Close()
	_ = errW.Close()
	if startErr != nil {
		stopSignals()
		finishContainment()
		_ = cmd.Wait()
		return 0, startErr
	}
	if scope != nil {
		scope.reapWhileRunning(cmd.Process.Pid)
	}
	primaryStart, identityErr := procstart.StartTime(cmd.Process.Pid)
	if identityErr != nil || primaryStart <= 0 {
		stopSignals()
		finishContainment()
		_ = cmd.Wait()
		return 0, errClaudeChatCapture
	}
	identity := claudeChatPrimaryIdentity{SessionID: cfg.SessionID, RequiredRuntime: cfg.RequiredRuntime, NativeSessionID: cfg.ConversationID, ShimPID: os.Getpid(), ShimStartTime: shimStart, PrimaryPID: cmd.Process.Pid, PrimaryStartTime: primaryStart}
	capture = newClaudeChatCapture(spool, srv, identity)
	capture.stdin, capture.stdout = inW, outR
	if birth != nil {
		capture.native, capture.lifetimeLoss = true, &lifetimeLoss
	}
	srv.claudePrimary.Store(capture)
	// Reserve/write initialize before the reader can contend for admission; the
	// owned pipes retain early stdout until these readers start immediately below.
	var initializeErr error
	if capture.native {
		initializeErr = capture.initializeNative()
		if initializeErr != nil {
			capture.fail()
		}
	}
	var readers sync.WaitGroup
	readers.Add(2)
	go func() { defer readers.Done(); capture.readStdout(claudePipeReader{file: outR}) }()
	go func() { defer readers.Done(); capture.readStderr(errR, os.Stderr) }()
	waitErr := cmd.Wait()
	stopSignals()
	// Stop has no separate wire ACK: closing its socket after direct Wait
	// preserves the existing delivery/EOF contract and joins entered handlers.
	// A leader may exit while its descendants hold stdout/stderr. Contain that
	// same group BEFORE joining readers; capture fsync never owns this path.
	finishContainment()
	_ = inW.Close()
	readDone := make(chan struct{})
	go func() { readers.Wait(); close(readDone) }()
	grace := cfg.GraceTimeout
	if grace <= 0 {
		grace = time.Second
	}
	select {
	case <-readDone:
	case <-time.After(grace):
		capture.fail()
		capture.cancelReadiness()
		_ = outR.Close()
		_ = errR.Close()
		<-readDone
	}
	if hooks != nil {
		hooks.stopAndJoin()
		<-hookDone
		hooks = nil
	}
	close(capture.queue)
	<-capture.done // persistence must join; Stop already contained the child
	body, captureErr := capture.result()
	writeErr := writeFileAtomic(cfg.SessionDir, claudeChatCaptureFile, body)
	code, sig := interpretExit(waitErr)
	exitErr := persistSideFiles(cfg.SessionDir, code, sig, nil)
	return code, errors.Join(initializeErr, captureErr, writeErr, exitErr)
}

func validateClaudeNativeChatConfig(cfg Config) error {
	cli := cfg.CLIIdentity
	if cfg.RequiredRuntime != persist.RequiredRuntimeClaudeChat || !adapter.IsCanonicalConversationID(cfg.ConversationID) || !persist.ValidID(cfg.SessionID) || !filepath.IsAbs(cfg.LoginStateRoot) || filepath.Clean(cfg.LoginStateRoot) != cfg.LoginStateRoot || cfg.SessionDir != filepath.Join(cfg.LoginStateRoot, cfg.SessionID) || cfg.NativeProvider != logins.ProviderClaude || cfg.LoginRuntime == nil || !logins.IsClaudeProfileRuntime(cfg.LoginStateRoot, *cfg.LoginRuntime) || cli == nil || cli.Version != "2.1.310" || !logins.IsClaudeCLIFingerprint(cli.Fingerprint) || !filepath.IsAbs(cli.Path) || filepath.Clean(cli.Path) != cli.Path || len(cfg.Argv) == 0 || cfg.Argv[0] != cli.Path || !filepath.IsAbs(cfg.Cwd) || filepath.Clean(cfg.Cwd) != cfg.Cwd || cfg.ClaudeChatFixture != "" || cfg.Backend != nil || cfg.OpenCode != nil || cfg.HermesInput != nil || cfg.AgyInput != nil || cfg.ClaudeInput || cfg.LoginSwitchID != "" || cfg.LoginSwitchSource != "" || cfg.InputEmbargo != "" || cfg.InputEmbargoToken != "" || cfg.HookSpoolMaxBytes > hookSpoolDefaultMaxBytes {
		return errors.New("shim: invalid managed Claude chat configuration")
	}
	seen := map[string]bool{}
	for i := 1; i < len(cfg.Argv); i++ {
		arg := cfg.Argv[i]
		if strings.HasPrefix(arg, "--session-id=") {
			if seen["--session-id"] || arg != "--session-id="+cfg.ConversationID {
				return errClaudeChatCapture
			}
			seen["--session-id"] = true
			continue
		}
		if seen[arg] && arg != "--plugin-dir" {
			return errClaudeChatCapture
		}
		seen[arg] = true
		switch arg {
		case "-p", "--verbose", "--replay-user-messages", "--dangerously-skip-permissions":
		case "--input-format", "--output-format", "--permission-prompt-tool", "--permission-prompts", "--settings", "--model", "--name", "--plugin-dir":
			i++
			if i >= len(cfg.Argv) {
				return errClaudeChatCapture
			}
			value := cfg.Argv[i]
			switch arg {
			case "--input-format", "--output-format":
				if value != "stream-json" {
					return errClaudeChatCapture
				}
			case "--permission-prompt-tool":
				if value != "stdio" {
					return errClaudeChatCapture
				}
			case "--permission-prompts":
				if value != "host" {
					return errClaudeChatCapture
				}
			case "--settings":
				settings, ok := nativeInitializeObject([]byte(value), "autoContinueAtUsageLimit")
				if !ok || strings.TrimSpace(string(settings["autoContinueAtUsageLimit"])) != "false" {
					return errClaudeChatCapture
				}
			default:
				if value == "" {
					return errClaudeChatCapture
				}
			}
		default:
			return errClaudeChatCapture
		}
	}
	for _, flag := range []string{"-p", "--input-format", "--output-format", "--verbose", "--replay-user-messages", "--permission-prompt-tool", "--permission-prompts", "--settings", "--session-id"} {
		if !seen[flag] {
			return errClaudeChatCapture
		}
	}
	return nil
}
