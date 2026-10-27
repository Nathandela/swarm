package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/attach"
	"github.com/Nathandela/swarm/internal/loginworker"
	"github.com/Nathandela/swarm/internal/procstart"
	"github.com/Nathandela/swarm/internal/protocol"
	tea "github.com/Nathandela/swarm/internal/thirdparty/bubbletea"
	"github.com/Nathandela/swarm/internal/tui"
	"github.com/Nathandela/swarm/internal/vt"
	"github.com/creack/pty"
)

const rendererBoard = "private-production-handoff-board"
const rendererDamage = "private-production-handoff-damage"

type rendererBoardModel struct{}

func (rendererBoardModel) Init() tea.Cmd                         { return nil }
func (m rendererBoardModel) Update(tea.Msg) (tea.Model, tea.Cmd) { return m, nil }
func (rendererBoardModel) View() tea.View {
	v := tea.NewView(rendererBoard)
	v.AltScreen = true
	return v
}

type rendererHandoffCapture struct {
	mu      sync.Mutex
	data    []byte
	changed chan struct{}
}

func (c *rendererHandoffCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	c.data = append(c.data, p...)
	c.mu.Unlock()
	select {
	case c.changed <- struct{}{}:
	default:
	}
	return len(p), nil
}
func (c *rendererHandoffCapture) bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.data...)
}
func (c *rendererHandoffCapture) wait(offset int, marker string) error {
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		data := c.bytes()
		if len(data) >= offset && bytes.Contains(data[offset:], []byte(marker)) {
			return nil
		}
		select {
		case <-c.changed:
		case <-timer.C:
			return fmt.Errorf("marker %q absent after offset %d", marker, offset)
		}
	}
}

type rendererHandoffSession struct {
	snapshot []byte
	frames   chan []byte
	resize   func() error
	detached bool
}

func (s *rendererHandoffSession) Snapshot() []byte      { return s.snapshot }
func (s *rendererHandoffSession) Frames() <-chan []byte { return s.frames }
func (s *rendererHandoffSession) Input([]byte) error    { return nil }
func (s *rendererHandoffSession) Resize(int, int) error { return s.resize() }
func (s *rendererHandoffSession) Detach() error         { s.detached = true; return nil }
func (*rendererHandoffSession) Generation() uint64      { return 1 }

func rendererLoginSocket(t *testing.T, snapshot []byte, afterPaint func() error) (string, <-chan error) {
	t.Helper()
	root, err := os.MkdirTemp(os.Getenv("TMPDIR"), "rj-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	job := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	dir := filepath.Join(root, "logins", "enrollment", job)
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	start, err := procstart.StartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	worker := loginworker.ProcessIdentity{PID: os.Getpid(), StartTime: start}
	progress := loginworker.Progress{SchemaVersion: loginworker.SchemaVersion, JobID: job, Generation: 1, Phase: loginworker.PhaseAuthenticating, Worker: worker}
	raw, err := json.Marshal(progress)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, loginworker.ProgressFile), raw, 0600); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, loginworker.SocketFile)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(socket, 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	done := make(chan error, 1)
	go func() {
		_ = listener.SetDeadline(time.Now().Add(3 * time.Second))
		conn, err := listener.AcceptUnix()
		if err != nil {
			done <- err
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		dec := json.NewDecoder(conn)
		var request struct {
			Op         string
			Generation uint64
			Worker     loginworker.ProcessIdentity
		}
		if err = dec.Decode(&request); err != nil {
			done <- err
			return
		}
		if request.Op != "attach" || request.Generation != 1 || request.Worker != worker {
			done <- fmt.Errorf("wrong real login attach request: %+v", request)
			return
		}
		if err = json.NewEncoder(conn).Encode(map[string]any{"type": "snapshot", "data": snapshot}); err != nil {
			done <- err
			return
		}
		if err = dec.Decode(&request); err != nil {
			done <- err
			return
		}
		if request.Op != "resize" || request.Generation != 1 {
			done <- fmt.Errorf("real login runner did not resize after snapshot: %+v", request)
			return
		}
		done <- afterPaint()
	}()
	return socket, done
}

func TestProductionRunnerRendererRestore(t *testing.T) {
	for _, kind := range []string{"attach", "login"} {
		t.Run(kind, func(t *testing.T) {
			master, tty, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			if err = pty.Setsize(master, &pty.Winsize{Rows: 40, Cols: 120}); err != nil {
				t.Fatal(err)
			}
			oldIn, oldOut := os.Stdin, os.Stdout
			os.Stdin, os.Stdout = tty, tty
			defer func() { os.Stdin, os.Stdout = oldIn, oldOut }()
			capture := &rendererHandoffCapture{changed: make(chan struct{}, 1)}
			drain := make(chan struct{})
			go func() { defer close(drain); _, _ = io.Copy(capture, master) }()
			defer func() {
				_ = master.Close()
				_ = tty.Close()
				select {
				case <-drain:
				case <-time.After(3 * time.Second):
					t.Error("owned PTY drain did not join")
				}
			}()
			program := tea.NewProgram(rendererBoardModel{}, tea.WithInput(tty), tea.WithOutput(tty), tea.WithWindowSize(120, 40), tea.WithoutSignalHandler(), tea.WithoutSignals())
			stopped := make(chan error, 1)
			go func() { _, err := program.Run(); stopped <- err }()
			defer func() {
				program.Quit()
				select {
				case err := <-stopped:
					if err != nil {
						t.Error(err)
					}
				case <-time.After(3 * time.Second):
					t.Error("program did not stop")
					program.Kill()
				}
			}()
			if err = capture.wait(0, rendererBoard); err != nil {
				t.Fatal(err)
			}
			emu := vt.NewEmulator(120, 39)
			defer func() { _ = emu.Close() }()
			emu.Feed([]byte(rendererDamage))
			snapshot, err := emu.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			releases, restores, restoreOffset := 0, 0, 0
			hand := tui.TerminalHandoff{Release: func() error { releases++; return program.ReleaseTerminal() }, Restore: func() error { restores++; restoreOffset = len(capture.bytes()); return program.RestoreTerminal() }}
			afterPaint := func() error { return capture.wait(0, rendererDamage) }
			var run func() error
			var serverDone <-chan error
			var session *rendererHandoffSession
			cleaned := false
			if kind == "attach" {
				session = &rendererHandoffSession{snapshot: snapshot, frames: make(chan []byte)}
				session.resize = func() error { err := afterPaint(); close(session.frames); return err }
				runner := tui.NewAttachRunner(func(string) (attach.Session, func(), error) { return session, func() { cleaned = true }, nil }, hand)
				run = func() error {
					return runner(protocol.SessionView{ID: "private/session", Name: "private attach"}, false, false)
				}
			} else {
				var socket string
				socket, serverDone = rendererLoginSocket(t, snapshot, afterPaint)
				run = func() error { return loginRunner(hand)(protocol.LoginJobView{LoginSocket: socket}) }
			}
			done := make(chan error, 1)
			go func() { done <- run() }()
			select {
			case err = <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("production runner handoff did not finish")
			}
			if serverDone != nil {
				select {
				case err = <-serverDone:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("owned login IPC server did not join")
				}
			}
			if releases != 1 || restores != 1 {
				t.Fatalf("handoff count release=%d restore=%d", releases, restores)
			}
			if kind == "attach" && (!cleaned || !session.detached) {
				t.Fatal("attach resources not retired")
			}
			if err = capture.wait(restoreOffset, rendererBoard); err != nil {
				t.Fatal(err)
			}
		})
	}
}
