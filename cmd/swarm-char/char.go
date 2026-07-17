package main

// The swarm-char characterization engine (E9.3 / T-6). characterize drives a
// real CLI in a PTY and records the raw output as a versioned adapter.Fixture.
// It ACTUALLY characterizes, not merely captures idle output:
//
//   - it answers the CLI's terminal device queries through internal/vt so a real
//     CLI does not stall waiting for replies;
//   - it feeds a scripted, timed stdin sequence (charSpec.Input) so the CLI is
//     driven through interactive states, not just observed at rest;
//   - it exposes a hook-collection sink (a unix socket named by
//     $SWARM_CHAR_HOOK_SINK) that the CLI's hooks/events post JSON payloads to
//     during the run, recorded into Fixture.HookPayloads;
//   - the derived capability entry (deriveCapability) comes from the ACTUAL
//     adapter under test and feeds it the REAL grid rendered from the capture,
//     never a nil grid.
//
// It reuses internal/shim's PTY-spawn pattern (creack/pty StartWithSize) and the
// vt emulator as LIBRARIES; it adds no shim API and, as the tool that owns the
// session, it legitimately owns the PTY fds and the sink socket (adapters never
// do).

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/vt"
	"github.com/creack/pty"
)

// defaultTimeout bounds a characterization run when the spec supplies none.
const defaultTimeout = 30 * time.Second

// hookSinkEnv names the unix socket a characterized CLI's hooks post their JSON
// payloads to. A real CLI's hook command is configured to dial it and write
// newline-delimited JSON objects; the harness records each into
// Fixture.HookPayloads with the event name and arrival time.
const hookSinkEnv = "SWARM_CHAR_HOOK_SINK"

// ScriptedInput is one timed keystroke burst fed to the CLI's stdin, so the
// harness can drive interactive states (answering a prompt, issuing a command)
// rather than only capturing the CLI's idle output.
type ScriptedInput struct {
	Delay time.Duration // wait this long after spawn before sending Data
	Data  string        // bytes written to the CLI's stdin (via the PTY master)
}

// charSpec is the input to characterize: the CLI's identity, the argv to exec
// directly (never via a shell), the working directory, the PTY geometry, a
// wall-clock bound, an optional scripted stdin sequence, and an optional hook
// sink socket path.
type charSpec struct {
	CLI, Version, Scenario string
	Argv                   []string
	Cwd                    string
	Cols, Rows             int
	Timeout                time.Duration
	Input                  []ScriptedInput // timed stdin keystrokes (optional)
	HookSink               string          // unix socket path for hook payloads (optional)
}

// characterize execs spec.Argv under a fresh PTY, feeds its output through the
// vt emulator (routing device-query replies back to the CLI), drives any
// scripted stdin sequence, records the raw PTY bytes plus any hook payloads
// posted to the sink, and returns a schema-valid fixture. It errors on an empty
// argv, a spawn failure, a hook-sink setup failure, or a recorded capture that
// fails Fixture.Validate.
func characterize(spec charSpec) (adapter.Fixture, error) {
	if len(spec.Argv) == 0 {
		return adapter.Fixture{}, errors.New("swarm-char: empty Argv (no program to characterize)")
	}
	cols, rows := spec.Cols, spec.Rows
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	emu := vt.NewEmulator(cols, rows)
	defer emu.Close()

	// Bring the hook-collection sink up BEFORE the child so its socket is live
	// the moment the CLI's first hook fires. Torn down after the child exits.
	sink, err := startHookSink(spec.HookSink)
	if err != nil {
		return adapter.Fixture{}, err
	}
	defer sink.stop()

	cmd := &exec.Cmd{
		Path: spec.Argv[0],
		Args: spec.Argv,
		Dir:  spec.Cwd,
		Env:  charEnv(spec.HookSink),
	}
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
	if err != nil {
		return adapter.Fixture{}, fmt.Errorf("swarm-char: start %s: %w", spec.Argv[0], err)
	}
	var closeOnce sync.Once
	closePTY := func() { closeOnce.Do(func() { _ = ptmx.Close() }) }
	defer closePTY()

	// Answer the CLI's device queries (DA/DSR/...) so a real CLI does not stall.
	// The emulator's reply drain writes them straight back onto the PTY master.
	emu.SetReplyWriter(ptmx)

	// Drive scripted stdin keystrokes at their timed offsets so the CLI advances
	// through interactive states. The feeder stops when the run ends.
	inputDone := make(chan struct{})
	go feedInput(ptmx, spec.Input, inputDone)
	defer close(inputDone)

	// Read the PTY to EOF in a goroutine, recording raw bytes and feeding the
	// emulator. The child's exit closes the slave, so the master read returns an
	// error and the goroutine delivers the accumulated capture.
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
		// Hung CLI: reap the group and close the master to unblock the reader,
		// then take whatever drained.
		_ = cmd.Process.Kill()
		closePTY()
		capture = <-capCh
	}
	_ = cmd.Wait()

	// The child has exited: drain and collect every hook payload it posted.
	hooks := sink.collect()

	fx := adapter.Fixture{
		SchemaVersion: adapter.FixtureSchemaVersion,
		CLI:           spec.CLI,
		Version:       spec.Version,
		Scenario:      spec.Scenario,
		PTYCapture:    capture,
		HookPayloads:  hooks,
	}
	if err := fx.Validate(); err != nil {
		return adapter.Fixture{}, fmt.Errorf("swarm-char: recorded an invalid fixture: %w", err)
	}
	return fx, nil
}

// feedInput writes each scripted keystroke burst to the CLI's stdin (the PTY
// master) after its delay, stopping early if the run ends (done closed) or a
// write fails (the PTY is gone).
func feedInput(w io.Writer, inputs []ScriptedInput, done <-chan struct{}) {
	for _, in := range inputs {
		if in.Delay > 0 {
			select {
			case <-time.After(in.Delay):
			case <-done:
				return
			}
		}
		if _, err := io.WriteString(w, in.Data); err != nil {
			return
		}
	}
}

// hookSink is the harness-owned unix-socket sink the CLI's hooks post JSON to
// during a run. Each accepted connection carries newline-delimited JSON objects;
// each object becomes one recorded HookPayload.
type hookSink struct {
	ln       net.Listener
	acceptWG sync.WaitGroup
	connWG   sync.WaitGroup
	mu       sync.Mutex
	payloads []adapter.HookPayload
}

// startHookSink listens on the unix socket at path and begins accepting hook
// posts. An empty path disables the sink (collect returns nil).
func startHookSink(path string) (*hookSink, error) {
	s := &hookSink{}
	if path == "" {
		return s, nil
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("swarm-char: hook sink listen %s: %w", path, err)
	}
	s.ln = ln
	s.acceptWG.Add(1)
	go s.acceptLoop()
	return s, nil
}

// acceptLoop accepts hook connections until the listener is closed, spawning a
// reader per connection.
func (s *hookSink) acceptLoop() {
	defer s.acceptWG.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return // listener closed
		}
		s.connWG.Add(1)
		go s.readConn(conn)
	}
}

// readConn records every newline-delimited JSON object on conn as a HookPayload.
// Non-JSON and blank lines are skipped; a payload's event name is read from the
// object (falling back to "unknown"), its raw body is the full line, and its
// arrival time is stamped on receipt.
func (s *hookSink) readConn(conn net.Conn) {
	defer s.connWG.Done()
	defer conn.Close()
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 || !json.Valid(line) {
			continue
		}
		raw := make([]byte, len(line))
		copy(raw, line)
		s.mu.Lock()
		s.payloads = append(s.payloads, adapter.HookPayload{
			Event:        hookEvent(raw),
			Raw:          json.RawMessage(raw),
			ReceivedAtMs: time.Now().UnixMilli(),
		})
		s.mu.Unlock()
	}
}

// collect closes the sink, waits for every posted payload to be recorded, and
// returns them in arrival order (nil when the sink was disabled).
func (s *hookSink) collect() []adapter.HookPayload {
	if s.ln != nil {
		_ = s.ln.Close()
		s.acceptWG.Wait()
		s.connWG.Wait()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.payloads
}

// stop closes the listener if collect did not already; safe to call after
// collect.
func (s *hookSink) stop() {
	if s.ln != nil {
		_ = s.ln.Close()
	}
}

// hookEvent reads the event name out of a posted hook payload, tolerating either
// the generic "event" key or a CLI's "hook_event_name" key, and never returning
// empty (so a recorded payload always passes Fixture.Validate).
func hookEvent(raw []byte) string {
	var probe struct {
		Event         string `json:"event"`
		HookEventName string `json:"hook_event_name"`
	}
	_ = json.Unmarshal(raw, &probe)
	switch {
	case probe.Event != "":
		return probe.Event
	case probe.HookEventName != "":
		return probe.HookEventName
	default:
		return "unknown"
	}
}

// buildGrid renders capture through the vt emulator and returns the final grid
// snapshot — the same projection the engine hands an adapter at runtime. The
// standard 80x24 geometry is used (the fixture records raw bytes, not size).
func buildGrid(capture []byte) (*vt.Snap, error) {
	emu := vt.NewEmulator(80, 24)
	defer emu.Close()
	emu.Feed(capture)
	b, err := emu.Snapshot()
	if err != nil {
		return nil, err
	}
	return vt.DecodeSnapshot(b)
}

// deriveCapability derives the E9.6 capability entry from the ACTUAL adapter a
// (passed in, not a hardcoded reference), rendering the REAL grid from the
// recorded capture and feeding it to extraction — never a nil grid.
func deriveCapability(a adapter.Adapter, fx adapter.Fixture) (adapter.CapabilityEntry, error) {
	grid, err := buildGrid(fx.PTYCapture)
	if err != nil {
		return adapter.CapabilityEntry{}, fmt.Errorf("swarm-char: render capability grid: %w", err)
	}
	return adapter.Capability(a, fx, grid), nil
}

// charEnv returns the current environment with a TERM guaranteed (so a CLI that
// consults terminfo has a known terminal type, same convention as the shim) and,
// when a hook sink is configured, the sink socket path exported to the child.
func charEnv(hookSink string) []string {
	env := os.Environ()
	hasTerm := false
	for _, kv := range env {
		if strings.HasPrefix(kv, "TERM=") {
			hasTerm = true
			break
		}
	}
	if !hasTerm {
		env = append(env, "TERM=xterm-256color")
	}
	if hookSink != "" {
		env = append(env, hookSinkEnv+"="+hookSink)
	}
	return env
}
