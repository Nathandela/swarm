package scheduletimer

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRenderUnits(t *testing.T) {
	exe := `/opt/swarm path/100%/${HOME}/a"b\c&`
	plist := renderLaunchd(exe)
	if !strings.Contains(plist, "<string>/opt/swarm path/100%/${HOME}/a&#34;b\\c&amp;</string>") || !strings.Contains(plist, "<string>schedule</string><string>tick</string>") {
		t.Fatalf("launchd arguments not safely rendered: %s", plist)
	}
	if strings.Count(plist, "<key>Minute</key>") != 60 {
		t.Fatal("launchd must schedule each calendar minute")
	}
	if !strings.Contains(plist, "<key>AbandonProcessGroup</key><true/>") {
		t.Fatal("launchd must preserve descendants when the tick exits")
	}
	service := renderSystemdService(exe)
	if !strings.Contains(service, "\nType=oneshot\nKillMode=process\n") {
		t.Fatal("systemd must complete each tick without killing the descendants in its cgroup")
	}
	if !strings.Contains(service, `ExecStart=:/bin/sh -c 'exec "$@"' swarm-schedule "/opt/swarm path/100%%/${HOME}/a\"b\\c&" schedule tick`) {
		t.Fatalf("unexpected systemd ExecStart: %s", service)
	}
	if !strings.Contains(renderSystemdTimer(), "OnCalendar=*-*-* *:*:00\nPersistent=true\nAccuracySec=1s") {
		t.Fatal("systemd timer must be minutely and persistent")
	}
}

func TestUnitPathsAndValidation(t *testing.T) {
	if _, err := unitPaths("linux", "relative"); err == nil {
		t.Fatal("accepted relative home")
	}
	if _, err := unitPaths("plan9", "/home/test"); err == nil {
		t.Fatal("accepted unsupported platform")
	}
	if safePath("relative") || safePath("/bin/swarm\n") {
		t.Fatal("accepted unsafe executable path")
	}
	got, _ := unitPaths("darwin", "/home/test")
	if !strings.HasSuffix(got[0], "Library/LaunchAgents/"+label+".plist") {
		t.Fatal(got)
	}
}

type fixture struct {
	manager
	active, enabled bool
	loaded          string
	fail            string
	after           bool
}

func newFixture(t *testing.T, platform string) *fixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	paths, err := unitPaths(platform, home)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{manager: manager{platform: platform, paths: paths}}
	f.run = func(name string, args ...string) (string, error) {
		if platform == "linux" {
			if name != "systemctl" || args[0] != "--user" {
				t.Fatalf("unexpected invocation: %s %v", name, args)
			}
			args = args[1:]
		} else if name != "launchctl" {
			t.Fatal(name)
		}
		op := args[0]
		if op == "show" {
			if strings.Join(args[1:], " ") != "--property=LoadState,ActiveState,UnitFileState,FragmentPath "+timerUnit {
				t.Fatal(args)
			}
			if _, err := os.Stat(paths[1]); os.IsNotExist(err) && !f.active {
				return "LoadState=not-found\nActiveState=inactive\n", nil
			}
			enabled, active := "disabled", "inactive"
			if f.enabled {
				enabled = "enabled"
			}
			if f.active {
				active = "active"
			}
			return fmt.Sprintf("LoadState=loaded\nActiveState=%s\nUnitFileState=%s\nFragmentPath=%s\n", active, enabled, paths[1]), nil
		}
		if op == "print" {
			if args[1] != domain()+"/"+label {
				t.Fatal(args)
			}
			if !f.active {
				return "Could not find service \"" + label + "\" in domain for user", fmt.Errorf("absent")
			}
			return "path = " + paths[0] + "\nstate = not running", nil
		}
		failing := f.fail == op
		if failing {
			f.fail = ""
			if !f.after {
				return "", fmt.Errorf("injected %s", op)
			}
		}
		switch op {
		case "start", "stop", "enable", "disable":
			if args[1] != timerUnit {
				t.Fatal(args)
			}
			if op == "start" || op == "stop" {
				f.active = op == "start"
			} else {
				f.enabled = op == "enable"
			}
		case "bootstrap":
			if args[1] != domain() || args[2] != paths[0] || f.active {
				t.Fatal(args)
			}
			b, err := os.ReadFile(paths[0])
			if err != nil {
				t.Fatal(err)
			}
			f.loaded = string(b)
			f.active = true
		case "bootout":
			if args[1] != domain()+"/"+label || !f.active {
				t.Fatal(args)
			}
			f.loaded = ""
			f.active = false
		case "daemon-reload":
		default:
			t.Fatal(args)
		}
		if failing {
			return "", fmt.Errorf("injected %s", op)
		}
		return "", nil
	}
	return f
}

func TestLifecycle(t *testing.T) {
	for _, platform := range []string{"linux", "darwin"} {
		t.Run(platform, func(t *testing.T) {
			f := newFixture(t, platform)
			dir := filepath.Dir(f.paths[0])
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(dir, 0755); err != nil {
				t.Fatal(err)
			}
			status := func(want string) {
				t.Helper()
				s, err := f.status()
				if s != want || err != nil {
					t.Fatalf("status = %q, %v; want %s", s, err, want)
				}
			}
			status("not installed, inactive")
			for range 2 {
				if err := f.change("/bin/swarm"); err != nil {
					t.Fatal(err)
				}
			}
			status("installed, active")
			f.active = false
			status("installed, inactive")
			for _, p := range f.paths {
				st, err := os.Stat(p)
				if err != nil || st.Mode().Perm() != 0600 {
					t.Fatalf("unit %s: %v, %v", p, st, err)
				}
			}
			if platform == "linux" {
				if err := os.Remove(f.paths[0]); err != nil {
					t.Fatal(err)
				}
				status("partially installed, inactive")
			}
			for range 2 {
				if err := f.change(""); err != nil {
					t.Fatal(err)
				}
			}
			status("not installed, inactive")
			st, err := os.Stat(dir)
			if err != nil || st.Mode().Perm() != 0755 {
				t.Fatalf("shared directory: %v, %v", st, err)
			}
		})
	}
}

func TestRollback(t *testing.T) {
	for _, tc := range []struct {
		platform, exe, fail    string
		prior, active, enabled bool
	}{
		{"linux", "/new", "start", false, false, false},
		{"linux", "/new", "enable", true, true, false},
		{"linux", "/new", "start", true, false, true},
		{"linux", "/new", "daemon-reload", true, true, true},
		{"linux", "", "disable", true, true, true},
		{"linux", "", "stop", true, true, false},
		{"linux", "", "daemon-reload", true, true, true},
		{"linux", "", "daemon-reload", true, false, true},
		{"darwin", "/new", "bootstrap", false, false, false},
		{"darwin", "/new", "bootstrap", true, true, false},
		{"darwin", "/new", "bootstrap", true, false, false},
		{"darwin", "/new", "bootout", true, true, false},
		{"darwin", "", "bootout", true, true, false},
	} {
		for _, after := range []bool{false, true} {
			t.Run(fmt.Sprint(tc, after), func(t *testing.T) {
				f := newFixture(t, tc.platform)
				if tc.prior {
					if err := os.MkdirAll(filepath.Dir(f.paths[0]), 0755); err != nil {
						t.Fatal(err)
					}
					for _, p := range f.paths {
						if err := os.WriteFile(p, []byte("old unit"), 0640); err != nil {
							t.Fatal(err)
						}
					}
					if tc.platform == "darwin" && tc.active {
						f.loaded = "old unit"
					}
				}
				loaded := f.loaded
				f.active, f.enabled, f.fail, f.after = tc.active, tc.enabled, tc.fail, after
				if err := f.change(tc.exe); err == nil || !strings.Contains(err.Error(), "injected "+tc.fail) {
					t.Fatal(err)
				}
				if f.active != tc.active || f.enabled != tc.enabled || loaded != f.loaded {
					t.Fatalf("rollback state active=%t enabled=%t loaded=%q", f.active, f.enabled, f.loaded)
				}
				for _, p := range f.paths {
					d, err := snapshot(p)
					if err != nil || d.exists != tc.prior || tc.prior && (string(d.data) != "old unit" || d.mode != 0640) {
						t.Fatalf("rollback %s: %+v, %v", p, d, err)
					}
				}
			})
		}
	}
}

func TestStableExecutableAcrossCaskUpgrade(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PATH", root)
	version := func(v string) string {
		p := filepath.Join(root, "Caskroom/swarm", v, "swarm")
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(v), 0700); err != nil {
			t.Fatal(err)
		}
		return p
	}
	old := version("1")
	if _, err := stableExecutable(old); err == nil {
		t.Fatal("accepted versioned binary without stable link")
	}
	link := filepath.Join(root, "bin/swarm")
	if err := os.MkdirAll(filepath.Dir(link), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(old, link); err != nil {
		t.Fatal(err)
	}
	got, err := stableExecutable(old)
	if err != nil || got != link {
		t.Fatalf("%q, %v", got, err)
	}
	if err = os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(version("2"), link); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(got); err != nil || string(b) != "2" {
		t.Fatalf("after upgrade: %q, %v", b, err)
	}
	if _, err := stableExecutable(old); err == nil {
		t.Fatal("accepted link to different executable")
	}
}

func TestManagerUnavailablePreservesDefinitions(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		t.Run(platform, func(t *testing.T) {
			f := newFixture(t, platform)
			if err := f.change("/original"); err != nil {
				t.Fatal(err)
			}
			f.run = func(_ string, args ...string) (string, error) {
				if args[0] != "print" && (args[0] != "--user" || args[1] != "show") {
					t.Fatalf("unexpected mutation %v", args)
				}
				return "", fmt.Errorf("manager unavailable")
			}
			if _, err := f.status(); err == nil || !strings.Contains(err.Error(), "installed; runtime status unknown") {
				t.Fatalf("status: %v", err)
			}
			for _, exe := range []string{"", "/replacement"} {
				if err := f.change(exe); err == nil {
					t.Fatal("changed timer with unknown state")
				}
			}
			b, err := os.ReadFile(f.paths[0])
			if err != nil || !strings.Contains(string(b), "/original") {
				t.Fatalf("lost definition: %q, %v", b, err)
			}
		})
	}
}

func TestRollbackReportsRecoveryFailure(t *testing.T) {
	f := newFixture(t, "darwin")
	if err := f.change("/old"); err != nil {
		t.Fatal(err)
	}
	run := f.run
	f.run = func(name string, args ...string) (string, error) {
		if args[0] == "bootstrap" {
			return "", fmt.Errorf("bootstrap unavailable")
		}
		return run(name, args...)
	}
	err := f.change("/new")
	if err == nil || !strings.Contains(err.Error(), "timer rollback: bootstrap unavailable") || f.active {
		t.Fatalf("err=%v active=%t", err, f.active)
	}
	b, err := os.ReadFile(f.paths[0])
	if err != nil || !strings.Contains(string(b), "/old") {
		t.Fatalf("%q, %v", b, err)
	}
}

func TestLaunchdPlistSyntax(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Apple plist parser available on macOS")
	}
	f := newFixture(t, "darwin")
	if err := f.change(`/opt/a"b'&c/swarm`); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("/usr/bin/plutil", "-lint", "--", f.paths[0]).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}

func TestSystemdShellForwardsLiteralArguments(t *testing.T) {
	exe := filepath.Join(t.TempDir(), `a=b '${HOME} "\100% swarm`)
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	unit := renderSystemdService(exe)
	const prefix = "ExecStart=:/bin/sh -c '"
	_, args, ok := strings.Cut(unit, prefix)
	script, _, end := strings.Cut(args, "' swarm-schedule ")
	if !ok || !end {
		t.Fatalf("unable to find fixed shell program in %s", unit)
	}
	b, err := exec.Command("/bin/sh", "-c", script, "swarm-schedule", exe, "schedule", "tick").CombinedOutput()
	if err != nil || string(b) != "schedule\ntick\n" {
		t.Fatalf("%v, %q", err, b)
	}
}

func TestPinnedTimerTarget(t *testing.T) {
	p := "/tmp/schedule state & 25%/a=b"
	env, err := (Target{p, p + "/socket", p + "/lock", p + "/log"}).entries()
	if err != nil {
		t.Fatal(err)
	}
	if b := renderLaunchd("/bin/swarm", env...); !strings.Contains(b, "<key>SWARM_DAEMON_STATE</key><string>/tmp/schedule state &amp; 25%/a=b</string>") {
		t.Fatal(b)
	}
	if b := renderSystemdService("/bin/swarm", env...); !strings.Contains(b, "Environment=\"SWARM_DAEMON_STATE=/tmp/schedule state & 25%%/a=b\"") {
		t.Fatal(b)
	}
	if _, err := (Target{"relative", p, p, p}).entries(); err == nil {
		t.Fatal("relative timer state accepted")
	}
}
