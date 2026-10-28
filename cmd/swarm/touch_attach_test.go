package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/Nathandela/swarm/internal/protocol"
)

// Drive the shipped entry point, real daemon, shim and PTYs: returning to the
// board must preserve the agent's progress, not merely leave a roster row.
func TestTUI_TouchReturnPreservesLiveSession(t *testing.T) {
	if testing.Short() {
		t.Skip("real daemon, shim and terminal processes")
	}
	swarmBin, fakeAgentBin := buildRoleBinaries(t)
	for _, enabled := range []bool{false, true} {
		name := "keyboard"
		if enabled {
			name = "touch"
		}
		t.Run(name, func(t *testing.T) {
			daemonEnv := startSmokeDaemon(t, swarmBin, fakeAgentBin)
			cc := ccFromSmokeEnv(daemonEnv, swarmBin)
			client, err := protocol.Dial(cc.SocketPath, []string{"attach"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close() })
			script := filepath.Join(t.TempDir(), "script")
			if err := os.WriteFile(script, []byte("ask first-touch-prompt\nask second-touch-prompt\nidle 120s\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			id, _, err := client.Launch(protocol.LaunchReq{Agent: "fake", Cwd: t.TempDir(),
				Options: map[string]string{"script": script}, Env: []string{"PATH=" + os.Getenv("PATH")}, Cols: 80, Rows: 24})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Kill(id) })

			clientHome, configDir := t.TempDir(), t.TempDir()
			if runtime.GOOS == "darwin" {
				configDir = filepath.Join(clientHome, "Library", "Application Support")
			}
			if err := os.MkdirAll(filepath.Join(configDir, "swarm"), 0o700); err != nil {
				t.Fatal(err)
			}
			pref, _ := json.Marshal(map[string]any{"schema_version": 1, "touch_back": enabled})
			if err := os.WriteFile(filepath.Join(configDir, "swarm", "board-layout.json"), pref, 0o600); err != nil {
				t.Fatal(err)
			}
			ptmx, tty, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = ptmx.Close(); _ = tty.Close() })
			if err := pty.Setsize(ptmx, &pty.Winsize{Rows: 24, Cols: 80}); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(swarmBin)
			// Isolate the client preference from the real user's configuration.
			cmd.Env = append(os.Environ(), append(daemonEnv, "HOME="+clientHome, "XDG_CONFIG_HOME="+configDir)...)
			cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			_ = tty.Close()
			t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
			out := &lockedBuf{}
			go func() { _, _ = copyUntilClosed(ptmx, out) }()
			waitText := func(after int, text string) {
				t.Helper()
				deadline := time.Now().Add(15 * time.Second)
				for time.Now().Before(deadline) {
					if strings.Contains(out.String()[after:], text) {
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
				t.Fatalf("missing new %q in terminal output: %q", text, out.String()[after:])
			}
			write := func(s string) {
				t.Helper()
				if _, err := ptmx.Write([]byte(s)); err != nil {
					t.Fatal(err)
				}
			}
			waitText(0, "[Open (⏎)]")
			for round, answer := range []string{"before-return", "after-return"} {
				start := len(out.String())
				write("\r")
				hint := "ctrl+q returns to swarm"
				if enabled {
					hint = "[swarm (ctrl+q)]"
				}
				waitText(start, hint)
				if !enabled && strings.Contains(out.String()[start:], "[swarm (ctrl+q)]") {
					t.Fatal("disabled preference exposed a touch button")
				}
				write(answer + "\r")
				waitText(start, "got: "+answer)
				if round == 0 {
					waitText(start, "second-touch-prompt")
				}
				start = len(out.String())
				if enabled {
					write("\x1b[<0;3;24M\x1b[<0;3;24m")
				} else {
					write("\x11")
				}
				waitText(start, "[Open (⏎)]")
				views, err := client.List()
				if err != nil || len(views) != 1 || views[0].ID != id {
					t.Fatalf("session changed after return: views=%+v err=%v", views, err)
				}
			}
		})
	}
}
