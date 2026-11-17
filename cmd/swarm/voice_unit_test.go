package main

import (
	"bytes"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/voice"
)

type voiceUnitExit int

func (e voiceUnitExit) Error() string { return "fixture exit " + strconv.Itoa(int(e)) }
func (e voiceUnitExit) ExitCode() int { return int(e) }

func TestVoiceUnitLifecycle(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			home, state := t.TempDir(), t.TempDir()
			binary := filepath.Join(home, "swarm")
			configureVoiceUnitFixture(t, state)
			otherState := t.TempDir()
			configureVoiceUnitFixture(t, otherState)
			var calls []string
			macLoaded := false
			run := func(name string, args ...string) error {
				calls = append(calls, strings.Join(append([]string{name}, args...), " "))
				if name == "launchctl" {
					switch args[0] {
					case "print":
						if !macLoaded {
							return voiceUnitExit(113)
						}
					case "bootstrap":
						macLoaded = true
					case "bootout":
						macLoaded = false
					}
				}
				return nil
			}
			for range 2 {
				if err := manageVoiceUnit("start", goos, home, binary, state, run, nil); err != nil {
					t.Fatal(err)
				}
			}
			unit, err := voice.UnitPath(goos, home)
			if err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(unit)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(body), state) || strings.Contains(string(body), "serve --unattended --state-dir \""+filepath.Join(state, "voice")+"\"") {
				t.Fatalf("unit did not use the base state directory: %s", body)
			}
			log, err := os.Stat(filepath.Join(state, "voice", "service.log"))
			if err != nil || log.Mode().Perm() != 0600 {
				t.Fatalf("service log is not private: %v, %v", log, err)
			}
			beforeRefusal := len(calls)
			for _, command := range []string{"start", "stop"} {
				if err := manageVoiceUnit(command, goos, home, binary, otherState, run, nil); err == nil {
					t.Fatalf("%s accepted a different state directory", command)
				}
			}
			if len(calls) != beforeRefusal {
				t.Fatal("state mismatch reached the service manager")
			}
			for range 2 {
				if err := manageVoiceUnit("stop", goos, home, binary, state, run, nil); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := os.Stat(unit); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("stopped voice unit remains: %v", err)
			}
			if _, err := voice.LoadConfig(filepath.Join(state, "voice")); err != nil {
				t.Fatalf("stop changed the manager configuration: %v", err)
			}
			domain := "gui/" + strconv.Itoa(os.Getuid())
			want := []string{
				"launchctl print " + domain + "/" + voiceMacLabel,
				"launchctl bootstrap " + domain + " " + unit,
				"launchctl print " + domain + "/" + voiceMacLabel,
				"launchctl print " + domain + "/" + voiceMacLabel,
				"launchctl bootout " + domain + "/" + voiceMacLabel,
			}
			stopMode := "<key>AbandonProcessGroup</key><true/>"
			if goos == "linux" {
				stopMode = "KillMode=process"
				want = []string{
					"systemctl --user daemon-reload",
					"systemctl --user enable --now " + voiceLinuxUnit,
					"systemctl --user daemon-reload",
					"systemctl --user enable --now " + voiceLinuxUnit,
					"systemctl --user disable --now " + voiceLinuxUnit,
					"systemctl --user daemon-reload",
				}
			}
			if !slices.Equal(calls, want) || !bytes.Contains(body, []byte(stopMode)) {
				t.Fatalf("service steps = %v, want %v; unit must include %s", calls, want, stopMode)
			}
		})
	}
}

func TestVoiceUnitRejectsUnsafePathAndMissingConfig(t *testing.T) {
	home, state := t.TempDir(), t.TempDir()
	binary := filepath.Join(home, "swarm")
	run := func(string, ...string) error {
		t.Error("unsafe or incomplete installation reached the system service manager")
		return nil
	}
	if err := manageVoiceUnit("start", "linux", home, binary, state, run, nil); err == nil || !strings.Contains(err.Error(), "swarm voice setup") {
		t.Fatalf("missing configuration error = %v", err)
	}
	configureVoiceUnitFixture(t, state)
	unit, err := voice.UnitPath("linux", home)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(unit), 0700); err != nil {
		t.Fatal(err)
	}
	protected := filepath.Join(state, "untouched")
	if err := os.WriteFile(protected, []byte("retain"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(protected, unit); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"start", "stop"} {
		if err := manageVoiceUnit(command, "linux", home, binary, state, run, nil); err == nil {
			t.Fatalf("%s accepted a symlink unit", command)
		}
	}
	data, err := os.ReadFile(protected)
	if err != nil || string(data) != "retain" {
		t.Fatalf("foreign file changed: %q, %v", data, err)
	}
}

func TestVoiceUnitKeepsRegistrationAfterFailedStop(t *testing.T) {
	home, state := t.TempDir(), t.TempDir()
	binary := filepath.Join(home, "swarm")
	configureVoiceUnitFixture(t, state)
	available := func(string, ...string) error { return nil }
	if err := manageVoiceUnit("start", "linux", home, binary, state, available, nil); err != nil {
		t.Fatal(err)
	}
	domainFailure := errors.New("service manager unavailable")
	failed := func(string, ...string) error { return domainFailure }
	if err := manageVoiceUnit("stop", "linux", home, binary, state, failed, nil); !errors.Is(err, domainFailure) {
		t.Fatalf("stop error = %v", err)
	}
	unit, _ := voice.UnitPath("linux", home)
	if _, err := os.Stat(unit); err != nil {
		t.Fatalf("failed stop removed installation state: %v", err)
	}
}

func configureVoiceUnitFixture(t *testing.T, state string) {
	t.Helper()
	err := voice.SaveConfig(filepath.Join(state, "voice"), voice.Config{
		Version: 2, PlaceID: "fixture-place", ConversationID: "fixture-conversation", Name: "Fixture",
		Provider: "codex", WorkDir: state, DaemonSocket: filepath.Join(state, "daemon.sock"),
		Language: "any", VoiceID: "marin", Mode: "conversation",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestVoiceUnitPreparesInteractiveDaemonOnlyAfterValidation(t *testing.T) {
	home, state := t.TempDir(), t.TempDir()
	binary := filepath.Join(home, "swarm")
	var order []string
	run := func(_ string, args ...string) error {
		order = append(order, args[1])
		return nil
	}
	prepare := func(cfg voice.Config) error {
		if cfg.DaemonSocket != filepath.Join(state, "daemon.sock") {
			t.Fatalf("wrong daemon socket: %s", cfg.DaemonSocket)
		}
		order = append(order, "prepare")
		return nil
	}
	if err := manageVoiceUnit("start", "linux", home, binary, state, run, prepare); err == nil || len(order) != 0 {
		t.Fatalf("missing config reached preparations: %v %v", err, order)
	}
	configureVoiceUnitFixture(t, state)
	path, err := voice.UnitPath("linux", home)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("unmanaged unit"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := manageVoiceUnit("start", "linux", home, binary, state, run, prepare); err == nil || len(order) != 0 {
		t.Fatalf("conflicting unit reached preparations: %v %v", err, order)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := manageVoiceUnit("start", "linux", home, binary, state, run, prepare); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(order, []string{"prepare", "daemon-reload", "enable"}) {
		t.Fatalf("unsafe startup order: %v", order)
	}
}

func TestVoiceDaemonEnvironmentUsesSavedOwnerStateOnlyUnattended(t *testing.T) {
	state := t.TempDir()
	cc := daemon.ClientConfig{StateDir: state}
	cfg := voice.Config{DaemonSocket: filepath.Join(state, "configured.sock")}
	dials, starts := 0, 0
	dial := func(socket string, _ int) (net.Conn, error) {
		dials++
		if socket != cfg.DaemonSocket {
			t.Fatal("dial ignored configured manager socket")
		}
		return nil, os.ErrNotExist
	}
	var received daemon.ClientConfig
	start := func(got daemon.ClientConfig) (net.Conn, error) {
		starts++
		received = got
		conn, peer := net.Pipe()
		_ = peer.Close()
		return conn, nil
	}
	if err := ensureVoiceDaemon(cc, cfg, false, dial, start); err != nil || dials != 0 || starts != 1 || received.Env != nil {
		t.Fatalf("interactive start did not inherit the owner shell: %v, dials=%d starts=%d", err, dials, starts)
	}
	if err := ensureVoiceDaemon(cc, cfg, true, dial, start); err == nil || starts != 1 {
		t.Fatalf("unattended cold spawn did not refuse missing saved environment: %v", err)
	}
	saved := []string{"PATH=/fixture/native-cli:/usr/bin", "LANG=C"}
	if err := os.WriteFile(daemon.SavedEnvPath(state), []byte(strings.Join(saved, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ensureVoiceDaemon(cc, cfg, true, dial, start); err != nil || starts != 2 || !slices.Equal(received.Env, saved) {
		t.Fatalf("unattended spawn did not use the exact saved environment: %v, starts=%d", err, starts)
	}
	if received.SocketPath != cfg.DaemonSocket || received.LockPath != filepath.Join(state, "daemon.lock") ||
		received.LogPath != filepath.Join(state, "daemon.log") {
		t.Fatalf("unattended spawn changed daemon custody: %+v", received)
	}
	alreadyRunning := func(string, int) (net.Conn, error) {
		conn, peer := net.Pipe()
		_ = peer.Close()
		return conn, nil
	}
	if err := ensureVoiceDaemon(cc, cfg, true, alreadyRunning, start); err != nil || starts != 2 {
		t.Fatalf("running daemon was restarted or treated as cold: %v, starts=%d", err, starts)
	}
}

func TestVoiceSwitchCommandRequiresExactSelection(t *testing.T) {
	var out, stderr bytes.Buffer
	if code := dispatchVoice([]string{"switch", "--help"}, &out, &stderr); code != 0 ||
		!strings.Contains(stderr.String(), "-from") || !strings.Contains(stderr.String(), "-manager") {
		t.Fatalf("switch help = %d, %q", code, stderr.String())
	}
	stderr.Reset()
	if code := dispatchVoice([]string{"switch", "--manager", "claude"}, &out, &stderr); code != misuseExit ||
		!strings.Contains(stderr.String(), "--from") {
		t.Fatalf("missing exact identity = %d, %q", code, stderr.String())
	}
}
