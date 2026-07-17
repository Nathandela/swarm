//go:build realcli

// This file is the ENGINE of the real-CLI smoke / characterization harness. It is
// compiled ONLY under `-tags realcli` and is BILLABLE when run (it drives the real
// `claude` and `codex` CLIs). It owns the PTY fds, the stand-in daemon hook
// socket, and the fixture-rewrite path — none of which any adapter is allowed to
// own; the harness legitimately does, because it is the tool that owns the session
// (the same stance as cmd/swarm-char).
//
// It reuses production transport unchanged: Claude's real hooks travel the exact
// `swarm hook <event>` -> hookclient.Post -> daemon-socket path, so the harness
// stands in as that socket and decodes the real engine.Callback the daemon would
// see (hookclient.Decode). Codex reports through typed app-server JSON-RPC events
// whose live producer is deferred (D1), so its stream is captured raw for the
// human to characterize.
//
// Nothing here runs at `go test ./...` time or in CI; the untagged doc.go keeps
// the package buildable when the tag is absent.
package smoke

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/detect"
	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/hookclient"
	"github.com/Nathandela/swarm/internal/vt"
	"github.com/creack/pty"
)

// swarmModulePkg is the import path of the swarm binary the harness builds into a
// temp dir so a launched CLI's injected `swarm hook <event>` resolves on PATH.
const swarmModulePkg = "github.com/Nathandela/swarm/cmd/swarm"

// scriptedInput is one timed keystroke burst written to the CLI's stdin (via the
// PTY master), so the harness can drive a real CLI through interactive states
// rather than only observing it at rest. Mirrors cmd/swarm-char's ScriptedInput.
type scriptedInput struct {
	delay time.Duration
	data  string
}

// scenario is one live characterization run: the argv the adapter composed (from
// Command or Resume), the working directory, a scripted stdin sequence, PTY
// geometry, a wall-clock bound, and whether to stand up the stand-in daemon hook
// socket (Claude only — Codex uses events, not hooks).
type scenario struct {
	argv        []string
	cwd         string
	input       []scriptedInput
	cols, rows  int
	timeout     time.Duration
	captureHook bool
}

// captureResult is everything one live run observed: the raw PTY bytes, the
// authenticated hook callbacks the CLI's `swarm hook` posted (Claude), and the
// rendered final grid (the projection the engine hands an adapter at runtime).
type captureResult struct {
	pty       []byte
	callbacks []engine.Callback
	grid      *vt.Snap
}

// detectCLI probes the host for a's real CLI through the production exec-based
// prober. It returns the detection so a caller can skip when the binary is
// absent (a human runs this with the CLIs installed + authenticated).
func detectCLI(a adapter.Adapter) adapter.Detection {
	return adapter.Detect(a, detect.Host{})
}

// buildSwarmBinary compiles the swarm binary into dir so a launched CLI's
// injected `swarm hook <event>` command resolves (dir is prepended to PATH). It
// uses the module import path, so it works from any cwd inside the module.
func buildSwarmBinary(dir string) (string, error) {
	bin := filepath.Join(dir, "swarm")
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, swarmModulePkg)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build swarm binary: %w\n%s", err, out)
	}
	return bin, nil
}

// runScenario drives one real CLI end to end: (optionally) stands up the stand-in
// daemon hook socket + per-session env so Claude's real hooks are captured,
// launches sc.argv under a fresh PTY, drives the scripted stdin, records the raw
// PTY bytes and any hook callbacks, and renders the final grid. It never runs at
// normal test time (the whole file is realcli-gated) and is bounded by sc.timeout.
func runScenario(sc scenario) (captureResult, error) {
	cols, rows := geometry(sc.cols, sc.rows)
	timeout := sc.timeout
	if timeout <= 0 {
		timeout = 45 * time.Second
	}

	env := baseEnv()

	// Claude reports through settings-configured hooks that shell out to
	// `swarm hook <event>`; stand up the socket that command posts to and inject
	// the per-session auth the daemon would inject at spawn, so the harness
	// captures the REAL callback stream over the production transport.
	var sink *callbackSink
	if sc.captureHook {
		tmp, err := os.MkdirTemp("", "realcli-hooks-")
		if err != nil {
			return captureResult{}, fmt.Errorf("hook tmp dir: %w", err)
		}
		defer os.RemoveAll(tmp)

		binDir := filepath.Join(tmp, "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			return captureResult{}, err
		}
		if _, err := buildSwarmBinary(binDir); err != nil {
			return captureResult{}, err
		}

		sockPath := filepath.Join(tmp, "daemon.sock")
		sink, err = startCallbackSink(sockPath)
		if err != nil {
			return captureResult{}, err
		}
		defer sink.stop()

		env = prependPath(env, binDir)
		env = injectHookEnv(env, "realcli-session", "realcli-token", sockPath, filepath.Join(tmp, "hook.seq"))
	}

	capture, err := runInPTY(sc.argv, sc.cwd, env, sc.input, timeout, cols, rows)
	if err != nil {
		return captureResult{}, err
	}

	grid, err := buildGrid(capture, cols, rows)
	if err != nil {
		return captureResult{}, fmt.Errorf("render grid: %w", err)
	}

	res := captureResult{pty: capture, grid: grid}
	if sink != nil {
		res.callbacks = sink.collect()
	}
	return res, nil
}

// runInPTY execs argv directly (never through a shell) under a fresh PTY, answers
// the CLI's device-status queries through the vt emulator so it does not stall,
// drives the scripted stdin at its timed offsets, and reads the PTY to EOF —
// returning the raw capture. A hung CLI is reaped at the timeout and whatever
// drained is returned. Mirrors cmd/swarm-char's characterize loop.
func runInPTY(argv []string, cwd string, env []string, inputs []scriptedInput, timeout time.Duration, cols, rows int) ([]byte, error) {
	if len(argv) == 0 {
		return nil, errors.New("realcli: empty argv (nothing to launch)")
	}

	emu := vt.NewEmulator(cols, rows)
	defer emu.Close()

	cmd := &exec.Cmd{Path: argv[0], Args: argv, Dir: cwd, Env: env}
	// argv[0] from an adapter is a bare binary name; resolve it on PATH.
	if !strings.ContainsRune(argv[0], os.PathSeparator) {
		resolved, err := exec.LookPath(argv[0])
		if err != nil {
			return nil, fmt.Errorf("realcli: locate %s: %w", argv[0], err)
		}
		cmd.Path = resolved
	}

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
	if err != nil {
		return nil, fmt.Errorf("realcli: start %s: %w", argv[0], err)
	}
	var closeOnce sync.Once
	closePTY := func() { closeOnce.Do(func() { _ = ptmx.Close() }) }
	defer closePTY()

	emu.SetReplyWriter(ptmx)

	inputDone := make(chan struct{})
	go feedInput(ptmx, inputs, inputDone)
	defer close(inputDone)

	capCh := make(chan []byte, 1)
	go func() {
		var buf bytes.Buffer
		chunk := make([]byte, 4096)
		for {
			n, rerr := ptmx.Read(chunk)
			if n > 0 {
				buf.Write(chunk[:n])
				emu.Feed(chunk[:n])
			}
			if rerr != nil {
				break
			}
		}
		capCh <- buf.Bytes()
	}()

	var capture []byte
	select {
	case capture = <-capCh:
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		closePTY()
		capture = <-capCh
	}
	_ = cmd.Wait()
	return capture, nil
}

// feedInput writes each scripted keystroke burst after its delay, stopping early
// if the run ends (done closed) or a write fails (the PTY is gone).
func feedInput(w io.Writer, inputs []scriptedInput, done <-chan struct{}) {
	for _, in := range inputs {
		if in.delay > 0 {
			select {
			case <-time.After(in.delay):
			case <-done:
				return
			}
		}
		if _, err := io.WriteString(w, in.data); err != nil {
			return
		}
	}
}

// captureAppServer runs argv (a stdio JSON-RPC server, e.g. `codex app-server`)
// as a plain piped subprocess, writes stdinPayload to its stdin, and reads its
// stdout for up to timeout — returning the raw stream. It is the D1 characterization
// primitive: the exact app-server invocation + handshake are themselves VERIFY
// items, so the caller supplies argv and payload (from env) rather than the harness
// guessing an unverified protocol. Not a PTY: an app-server speaks line-delimited
// JSON-RPC over pipes.
func captureAppServer(argv []string, stdinPayload []byte, timeout time.Duration) ([]byte, error) {
	if len(argv) == 0 {
		return nil, errors.New("realcli: empty app-server argv")
	}
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = baseEnv()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("realcli: start app-server %v: %w", argv, err)
	}
	if len(stdinPayload) > 0 {
		_, _ = stdin.Write(stdinPayload)
	}
	_ = stdin.Close()
	_ = cmd.Wait() // ctx timeout or clean exit; we take whatever drained either way
	return out.Bytes(), nil
}

// jsonRPCMethods returns the distinct, sorted set of top-level JSON-RPC "method"
// values on the line-delimited messages in stream. It is total: non-JSON lines
// are skipped, and it never panics. This is what the human compares against the
// codex adapter's declared eventSources method names.
func jsonRPCMethods(stream []byte) []string {
	seen := map[string]bool{}
	for _, line := range bytes.Split(stream, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || !json.Valid(line) {
			continue
		}
		var probe struct {
			Method string `json:"method"`
		}
		if json.Unmarshal(line, &probe) == nil && probe.Method != "" {
			seen[probe.Method] = true
		}
	}
	out := make([]string, 0, len(seen))
	for m := range seen {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// --- stand-in daemon hook socket -------------------------------------------

// callbackSink is the harness-owned unix socket that a launched CLI's real
// `swarm hook` posts authenticated engine.Callbacks to. Each `swarm hook`
// invocation dials, writes exactly one JSON callback, and closes; the sink
// decodes one callback per connection with the production hookclient.Decode.
type callbackSink struct {
	ln        net.Listener
	acceptWG  sync.WaitGroup
	connWG    sync.WaitGroup
	mu        sync.Mutex
	callbacks []engine.Callback
	conns     []net.Conn
}

// startCallbackSink listens on the unix socket at path and begins accepting posts.
func startCallbackSink(path string) (*callbackSink, error) {
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("realcli: hook sink listen %s: %w", path, err)
	}
	s := &callbackSink{ln: ln}
	s.acceptWG.Add(1)
	go s.acceptLoop()
	return s, nil
}

func (s *callbackSink) acceptLoop() {
	defer s.acceptWG.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.conns = append(s.conns, conn)
		s.mu.Unlock()
		s.connWG.Add(1)
		go s.readConn(conn)
	}
}

// readConn decodes one production engine.Callback from conn and records it.
func (s *callbackSink) readConn(conn net.Conn) {
	defer s.connWG.Done()
	defer conn.Close()
	cb, err := hookclient.Decode(conn)
	if err != nil {
		return
	}
	s.mu.Lock()
	s.callbacks = append(s.callbacks, cb)
	s.mu.Unlock()
}

// collect closes the sink and returns every callback in arrival order. It is
// bounded: after the listener is closed it gives accepted connections a short
// read deadline so a client that never sends cannot wedge the harness.
func (s *callbackSink) collect() []engine.Callback {
	const grace = 2 * time.Second
	_ = s.ln.Close()
	s.acceptWG.Wait()

	deadline := time.Now().Add(grace)
	s.mu.Lock()
	for _, c := range s.conns {
		_ = c.SetReadDeadline(deadline)
	}
	s.mu.Unlock()

	done := make(chan struct{})
	go func() { s.connWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(grace + time.Second):
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return s.callbacks
}

// stop closes the listener if collect did not already; safe after collect.
func (s *callbackSink) stop() {
	if s.ln != nil {
		_ = s.ln.Close()
	}
}

// --- fixture rewrite --------------------------------------------------------

// recordFixture writes fx as indented JSON to path (the adapter's testdata file),
// so a drifted fixture tracks the real CLI format again (T-6). It validates
// before writing, so the harness never persists a fixture the loader would reject.
func recordFixture(path string, fx adapter.Fixture) error {
	if err := fx.Validate(); err != nil {
		return fmt.Errorf("realcli: refusing to record an invalid fixture: %w", err)
	}
	b, err := json.MarshalIndent(fx, "", "  ")
	if err != nil {
		return fmt.Errorf("realcli: marshal fixture: %w", err)
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

// callbacksToHookPayloads reconstructs Fixture.HookPayloads from captured
// callbacks: the event name is the callback's event, and the raw body is its
// flattened string payload re-marshaled to JSON (the top-level fields `swarm hook`
// extracted from the CLI's real stdin JSON). A callback with an empty payload
// records an empty JSON object so Validate's "valid JSON raw" rule holds.
func callbacksToHookPayloads(cbs []engine.Callback) []adapter.HookPayload {
	if len(cbs) == 0 {
		return nil
	}
	out := make([]adapter.HookPayload, 0, len(cbs))
	base := time.Now().UnixMilli()
	for i, cb := range cbs {
		raw, err := json.Marshal(cb.Payload)
		if err != nil || len(cb.Payload) == 0 {
			raw = []byte("{}")
		}
		out = append(out, adapter.HookPayload{
			Event:        cb.Event,
			Raw:          json.RawMessage(raw),
			ReceivedAtMs: base + int64(i),
		})
	}
	return out
}

// jsonRPCHookPayloads reconstructs Fixture.HookPayloads from the line-delimited
// JSON-RPC messages in stream (a codex app-server capture): each message with a
// non-empty "method" becomes one payload (event = method, raw = the whole line).
// It is total: non-JSON lines are skipped. Codex records its typed events this
// way (its testdata/codex.json pty_capture is exactly such a stream).
func jsonRPCHookPayloads(stream []byte) []adapter.HookPayload {
	var out []adapter.HookPayload
	base := time.Now().UnixMilli()
	for i, line := range bytes.Split(stream, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || !json.Valid(line) {
			continue
		}
		var probe struct {
			Method string `json:"method"`
		}
		if json.Unmarshal(line, &probe) != nil || probe.Method == "" {
			continue
		}
		raw := make([]byte, len(line))
		copy(raw, line)
		out = append(out, adapter.HookPayload{
			Event:        probe.Method,
			Raw:          json.RawMessage(raw),
			ReceivedAtMs: base + int64(i),
		})
	}
	return out
}

// --- env + grid helpers -----------------------------------------------------

// baseEnv returns the process environment with a TERM guaranteed (same convention
// as the shim and swarm-char), so a CLI that consults terminfo has a known type.
func baseEnv() []string {
	env := os.Environ()
	for _, kv := range env {
		if strings.HasPrefix(kv, "TERM=") {
			return env
		}
	}
	return append(env, "TERM=xterm-256color")
}

// prependPath prepends dir to the PATH entry of env (in place-returning a copy),
// so a launched CLI resolves the harness-built `swarm` first.
func prependPath(env []string, dir string) []string {
	out := make([]string, 0, len(env))
	replaced := false
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			out = append(out, "PATH="+dir+string(os.PathListSeparator)+kv[len("PATH="):])
			replaced = true
			continue
		}
		out = append(out, kv)
	}
	if !replaced {
		out = append(out, "PATH="+dir)
	}
	return out
}

// injectHookEnv appends the four per-session hook variables the daemon injects at
// spawn (session id, token, socket, monotonic counter file), so a launched CLI's
// `swarm hook` reaches and authenticates to the stand-in socket. Mirrors
// daemon.injectHookEnv exactly (via the shared hookclient env keys).
func injectHookEnv(env []string, id, token, sock, seqFile string) []string {
	return append(env,
		hookclient.EnvSessionID+"="+id,
		hookclient.EnvToken+"="+token,
		hookclient.EnvSocket+"="+sock,
		hookclient.EnvSequenceFile+"="+seqFile,
	)
}

// geometry normalizes non-positive PTY dimensions to the 80x24 default, matching
// swarm-char's normalization so a rendered grid matches what the CLI drew.
func geometry(cols, rows int) (int, int) {
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	return cols, rows
}

// buildGrid renders capture through the vt emulator at cols x rows and returns the
// final grid snapshot — the same projection the engine hands an adapter at runtime.
func buildGrid(capture []byte, cols, rows int) (*vt.Snap, error) {
	emu := vt.NewEmulator(cols, rows)
	defer emu.Close()
	emu.Feed(capture)
	b, err := emu.Snapshot()
	if err != nil {
		return nil, err
	}
	return vt.DecodeSnapshot(b)
}

// --- observation helpers (used by the test entrypoint) ----------------------

// declaredHookEvents returns the set of hook event names a declares in its
// SignalSources (Kind == "hook"). Claude declares these; the harness asserts the
// events the real CLI actually fired are a subset (any extra is drift).
func declaredHookEvents(a adapter.Adapter) map[string]bool {
	set := map[string]bool{}
	for _, s := range a.SignalSources() {
		if s.Kind == "hook" {
			if ev := s.Descriptor["event"]; ev != "" {
				set[ev] = true
			}
		}
	}
	return set
}

// declaredEventMethods returns the set of typed-event method names a declares in
// its SignalSources (Kind == "event"). Codex declares these; the harness compares
// them against the JSON-RPC methods observed on the real app-server stream.
func declaredEventMethods(a adapter.Adapter) map[string]bool {
	set := map[string]bool{}
	for _, s := range a.SignalSources() {
		if s.Kind == "event" {
			if ev := s.Descriptor["event"]; ev != "" {
				set[ev] = true
			}
		}
	}
	return set
}

// notificationSubtypeField returns the payload field name the adapter reads a
// Notification's subtype from (its declared subtype_field, e.g. "notification_type"),
// or "" if none is declared. The harness asserts a real Notification payload
// actually carries that field (D2).
func notificationSubtypeField(a adapter.Adapter) string {
	for _, s := range a.SignalSources() {
		if s.Kind == "hook" && s.Descriptor["event"] == "Notification" {
			return s.Descriptor["subtype_field"]
		}
	}
	return ""
}

// observedEvents returns the distinct, sorted event names across the captured
// callbacks (the hook events the real CLI actually fired).
func observedEvents(cbs []engine.Callback) []string {
	seen := map[string]bool{}
	for _, cb := range cbs {
		if cb.Event != "" {
			seen[cb.Event] = true
		}
	}
	out := make([]string, 0, len(seen))
	for e := range seen {
		out = append(out, e)
	}
	sort.Strings(out)
	return out
}

// firstCallbackFor returns the first captured callback whose event is name.
func firstCallbackFor(cbs []engine.Callback, name string) (engine.Callback, bool) {
	for _, cb := range cbs {
		if cb.Event == name {
			return cb, true
		}
	}
	return engine.Callback{}, false
}
