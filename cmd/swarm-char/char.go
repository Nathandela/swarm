package main

// The swarm-char characterization engine (E9.3 / T-6). characterize drives a
// real CLI in a PTY, records the raw output as a versioned adapter.Fixture, and
// answers the CLI's terminal device queries through internal/vt so a real CLI
// does not stall waiting for replies. It reuses internal/shim's PTY-spawn
// pattern (creack/pty StartWithSize) and the vt emulator as LIBRARIES; it adds
// no shim API and, as the tool that owns the session, it legitimately owns the
// PTY fds (adapters never do).

import (
	"bytes"
	"errors"
	"fmt"
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

// charSpec is the input to characterize: the CLI's identity, the argv to exec
// directly (never via a shell), the working directory, the PTY geometry, and a
// wall-clock bound.
type charSpec struct {
	CLI, Version, Scenario string
	Argv                   []string
	Cwd                    string
	Cols, Rows             int
	Timeout                time.Duration
}

// characterize execs spec.Argv under a fresh PTY, feeds its output through the
// vt emulator (routing device-query replies back to the CLI), records the raw
// PTY bytes, and returns a schema-valid fixture. It errors on an empty argv, a
// spawn failure, or a recorded capture that fails Fixture.Validate.
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

	cmd := &exec.Cmd{
		Path: spec.Argv[0],
		Args: spec.Argv,
		Dir:  spec.Cwd,
		Env:  charEnv(),
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

	fx := adapter.Fixture{
		SchemaVersion: adapter.FixtureSchemaVersion,
		CLI:           spec.CLI,
		Version:       spec.Version,
		Scenario:      spec.Scenario,
		PTYCapture:    capture,
	}
	if err := fx.Validate(); err != nil {
		return adapter.Fixture{}, fmt.Errorf("swarm-char: recorded an invalid fixture: %w", err)
	}
	return fx, nil
}

// charEnv returns the current environment with a TERM guaranteed, so a CLI that
// consults terminfo has a known terminal type (same convention as the shim).
func charEnv() []string {
	env := os.Environ()
	for _, kv := range env {
		if strings.HasPrefix(kv, "TERM=") {
			return env
		}
	}
	return append(env, "TERM=xterm-256color")
}
