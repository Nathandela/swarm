//go:build realcli

// PERMISSION-DIALOG CAPTURE HARNESS (Mirror M1.1, bead agents-tracker-dwwv.2.1).
//
// M1.2 will apply a phone approval by injecting the dialog's own keys into the PTY
// the daemon owns, gated on the live VT grid still showing that dialog. Both halves
// of that gate -- the grid signature and the key map -- must be RECORDED against a
// real `claude`, never assumed. This file is the recorder.
//
// It is a sibling of realcli.go and shares its two gates: the `realcli` build tag
// and the SWARM_REALCLI=1 opt-in. It differs from runScenario in one way that the
// job requires: a permission dialog is a TRANSIENT state, so a capture that only
// renders the final grid cannot see it. dialogSession therefore keeps the emulator
// live for the whole run and lets a scenario WAIT on rendered-grid predicates,
// inject keystrokes at those moments, and dump a labelled snapshot at each one.
//
// !!! BILLABLE -- GATED -- NEVER IN CI !!! Running this launches the real CLI.
package smoke

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/vt"
	"github.com/creack/pty"
)

// pollInterval is how often a wait predicate re-renders the grid. The dialog is a
// human-timescale event, so 150ms is far below anything that could miss it while
// staying cheap.
const pollInterval = 150 * time.Millisecond

// dialogSession is one live CLI process under a PTY, with its emulator kept hot so
// a scenario can observe TRANSIENT screens rather than only the final one. The
// harness owns the PTY fd and the dump directory; no adapter ever does.
type dialogSession struct {
	emu     *vt.Emulator
	ptmx    *os.File
	cmd     *exec.Cmd
	dumpDir string

	mu   sync.Mutex
	raw  bytes.Buffer
	obs  []string
	dead bool
}

// startDialogSession execs argv directly (never through a shell) under a fresh PTY
// at cols x rows, answering the CLI's device queries through the vt emulator so it
// does not stall, and begins draining output into both the raw buffer and the
// emulator. The caller must call close().
func startDialogSession(argv []string, cwd string, env []string, cols, rows int, dumpDir string) (*dialogSession, error) {
	if len(argv) == 0 {
		return nil, errors.New("permdialog: empty argv")
	}
	if err := os.MkdirAll(dumpDir, 0o755); err != nil {
		return nil, err
	}
	bin := argv[0]
	if !strings.ContainsRune(bin, os.PathSeparator) {
		resolved, err := exec.LookPath(bin)
		if err != nil {
			return nil, fmt.Errorf("permdialog: locate %s: %w", bin, err)
		}
		bin = resolved
	}
	cmd := &exec.Cmd{Path: bin, Args: argv, Dir: cwd, Env: env}
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
	if err != nil {
		return nil, fmt.Errorf("permdialog: start %s: %w", argv[0], err)
	}
	s := &dialogSession{emu: vt.NewEmulator(cols, rows), ptmx: ptmx, cmd: cmd, dumpDir: dumpDir}
	s.emu.SetReplyWriter(ptmx)
	go s.drain()
	return s, nil
}

// drain reads the PTY to EOF, feeding every byte to the emulator and the raw log.
func (s *dialogSession) drain() {
	chunk := make([]byte, 4096)
	for {
		n, err := s.ptmx.Read(chunk)
		if n > 0 {
			s.mu.Lock()
			s.raw.Write(chunk[:n])
			s.mu.Unlock()
			s.emu.Feed(chunk[:n])
		}
		if err != nil {
			s.mu.Lock()
			s.dead = true
			s.mu.Unlock()
			return
		}
	}
}

// snapshot renders the live screen: the raw snapshot bytes, the decoded grid, and
// its trailing-trimmed text.
func (s *dialogSession) snapshot() ([]byte, *vt.Snap, string, error) {
	b, err := s.emu.Snapshot()
	if err != nil {
		return nil, nil, "", err
	}
	snap, err := vt.DecodeSnapshot(b)
	if err != nil {
		return nil, nil, "", err
	}
	return b, snap, snapText(snap), nil
}

// snapText concatenates a grid's visible rows, each right-trimmed, newline joined.
func snapText(snap *vt.Snap) string {
	var b strings.Builder
	for _, line := range snap.Lines {
		var row strings.Builder
		for _, r := range line.Runs {
			row.WriteString(r.Text)
		}
		b.WriteString(strings.TrimRight(row.String(), " "))
		b.WriteByte('\n')
	}
	return b.String()
}

// waitFor blocks until the rendered grid contains sub, or timeout elapses. A
// timeout is an error carrying the label so a failed scenario still says where.
func (s *dialogSession) waitFor(sub string, timeout time.Duration) error {
	return s.waitUntil(fmt.Sprintf("contains %q", sub), timeout, func(text string) bool {
		return strings.Contains(text, sub)
	})
}

// waitForAny blocks until the grid contains any of subs, returning the one it saw.
func (s *dialogSession) waitForAny(subs []string, timeout time.Duration) (string, error) {
	var hit string
	err := s.waitUntil(fmt.Sprintf("contains any of %q", subs), timeout, func(text string) bool {
		for _, sub := range subs {
			if strings.Contains(text, sub) {
				hit = sub
				return true
			}
		}
		return false
	})
	return hit, err
}

// waitAbsent blocks until the grid no longer contains sub.
func (s *dialogSession) waitAbsent(sub string, timeout time.Duration) error {
	return s.waitUntil(fmt.Sprintf("absent %q", sub), timeout, func(text string) bool {
		return !strings.Contains(text, sub)
	})
}

// waitUntil polls the rendered grid until pred holds or timeout elapses.
func (s *dialogSession) waitUntil(what string, timeout time.Duration, pred func(string) bool) error {
	deadline := time.Now().Add(timeout)
	for {
		_, _, text, err := s.snapshot()
		if err != nil {
			return err
		}
		if pred(text) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("permdialog: timeout after %s waiting for %s", timeout, what)
		}
		time.Sleep(pollInterval)
	}
}

// send writes data to the CLI's stdin through the PTY master, verbatim.
func (s *dialogSession) send(data string) error {
	_, err := s.ptmx.Write([]byte(data))
	return err
}

// record dumps the live grid under label: <label>.snap.json (the exact vt snapshot
// bytes the daemon's tap would carry) and <label>.txt (its rendered text). The
// snapshot json is what the recognizer's fixtures are made of.
func (s *dialogSession) record(label string) (string, error) {
	raw, _, text, err := s.snapshot()
	if err != nil {
		return "", err
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, raw, "", "  "); err != nil {
		return "", err
	}
	pretty.WriteByte('\n')
	if err := os.WriteFile(filepath.Join(s.dumpDir, label+".snap.json"), pretty.Bytes(), 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(s.dumpDir, label+".txt"), []byte(text), 0o644); err != nil {
		return "", err
	}
	return text, nil
}

// note appends one human-readable observation to the run log (dumped by close).
func (s *dialogSession) note(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.obs = append(s.obs, fmt.Sprintf(format, args...))
}

// observations returns the run log so far.
func (s *dialogSession) observations() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.obs))
	copy(out, s.obs)
	return out
}

// close kills the CLI (promptly -- a real run is billable and must leave no orphan),
// closes the PTY, and dumps the raw capture plus the observation log.
func (s *dialogSession) close() {
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	_ = s.ptmx.Close()
	_ = s.cmd.Wait()
	s.emu.Close()

	s.mu.Lock()
	raw := s.raw.Bytes()
	obs := strings.Join(s.obs, "\n") + "\n"
	s.mu.Unlock()
	_ = os.WriteFile(filepath.Join(s.dumpDir, "raw.bin"), raw, 0o644)
	_ = os.WriteFile(filepath.Join(s.dumpDir, "observations.txt"), []byte(obs), 0o644)
}

// claudeVersion asks the installed CLI for its version banner (not billable: no
// session is started).
func claudeVersion() (string, error) {
	out, err := exec.Command("claude", "--version").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// dialogEnv is the environment a capture run launches under: the caller's own
// environment with a guaranteed TERM, minus two families of variable.
//
//   - SWARM_*: a stray daemon socket would give the capture side effects. The CLI
//     under capture must talk to nobody but its own terminal.
//   - CLAUDE*: when the harness is itself run from inside a Claude Code session,
//     the parent's session markers are inherited and the child CHANGES BEHAVIOR --
//     an observed run reported "Transcript saving is off - inherited
//     CLAUDE_CODE_CHILD_SESSION marker" and carried the parent's session title.
//     The daemon spawns from no such session, so stripping them is the faithful
//     environment, not a convenience.
func dialogEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "SWARM_") || strings.HasPrefix(kv, "CLAUDE") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "TERM=xterm-256color")
}
