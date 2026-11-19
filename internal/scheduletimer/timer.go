// Package scheduletimer installs the per-user timer that drives `swarm schedule tick`.
package scheduletimer

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"
	"unicode/utf8"
)

const label = "com.swarm.schedule"

// Target pins the timer to the same supervisor and catalog as the installer.
type Target struct {
	State, Socket, Lock, Log string
}

func (t Target) entries() ([]string, error) {
	var env []string
	for _, e := range []struct{ key, value string }{
		{"SWARM_DAEMON_STATE", t.State}, {"SWARM_DAEMON_SOCK", t.Socket},
		{"SWARM_DAEMON_LOCK", t.Lock}, {"SWARM_DAEMON_LOG", t.Log},
	} {
		if !safePath(e.value) {
			return nil, fmt.Errorf("invalid timer path for %s", e.key)
		}
		env = append(env, e.key+"="+e.value)
	}
	return env, nil
}

// Install writes and activates a per-user timer. executable must be absolute.
func Install(executable string, target Target) error {
	if !safePath(executable) {
		return errors.New("scheduletimer: executable must be an absolute, valid path")
	}
	exe, err := stableExecutable(executable)
	if err != nil {
		return err
	}
	m, err := localManager()
	if err != nil {
		return err
	}
	m.environment, err = target.entries()
	if err != nil {
		return err
	}
	return m.change(exe)
}

// Uninstall deactivates the per-user timer and removes its definitions.
func Uninstall() error {
	m, err := localManager()
	if err != nil {
		return err
	}
	return m.change("")
}

// Status reports definition presence and whether the manager has armed the timer.
func Status() (string, error) {
	m, err := localManager()
	if err != nil {
		return "", err
	}
	return m.status()
}

func localManager() (manager, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return manager{}, err
	}
	paths, err := unitPaths(runtime.GOOS, home)
	return manager{platform: runtime.GOOS, paths: paths, run: command}, err
}

// Prefer an upgrade-stable name only if it identifies the running executable.
func stableExecutable(exe string) (string, error) {
	info, err := os.Stat(exe)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return "", fmt.Errorf("%q is not executable", exe)
	}
	candidates := []string{}
	versioned := func(path string) (string, bool) {
		for _, marker := range []string{"/Caskroom/", "/Cellar/"} {
			if prefix, _, ok := strings.Cut(path, marker); ok {
				return filepath.Join(prefix, "bin", "swarm"), true
			}
		}
		return "", false
	}
	if link, ok := versioned(exe); ok {
		candidates = append(candidates, link)
	}
	if link, e := exec.LookPath("swarm"); e == nil {
		candidates = append(candidates, link)
	}
	if filepath.IsAbs(os.Args[0]) {
		candidates = append(candidates, os.Args[0])
	}
	candidates = append(candidates, exe)
	for _, p := range candidates {
		if _, ok := versioned(p); ok || !safePath(p) {
			continue
		}
		if st, e := os.Stat(p); e == nil && os.SameFile(info, st) {
			return p, nil
		}
	}
	return "", fmt.Errorf("cannot locate a stable executable link for %q", exe)
}

func unitPaths(goos, home string) ([]string, error) {
	if !safePath(home) {
		return nil, errors.New("scheduletimer: invalid home path")
	}
	switch goos {
	case "darwin":
		return []string{filepath.Join(home, "Library/LaunchAgents", label+".plist")}, nil
	case "linux":
		config := os.Getenv("XDG_CONFIG_HOME")
		if !filepath.IsAbs(config) {
			config = filepath.Join(home, ".config")
		}
		d := filepath.Join(config, "systemd/user")
		return []string{filepath.Join(d, "swarm-schedule.service"), filepath.Join(d, "swarm-schedule.timer")}, nil
	default:
		return nil, fmt.Errorf("scheduletimer: unsupported platform %q", goos)
	}
}

func safePath(s string) bool {
	if !filepath.IsAbs(s) || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || r == 0xfffe || r == 0xffff {
			return false
		}
	}
	return true
}
func plist(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// The tick hands any long-lived processes off to the Swarm supervisor lifecycle.
func renderLaunchd(exe string, environment ...string) string {
	var env strings.Builder
	if len(environment) > 0 {
		env.WriteString("<key>EnvironmentVariables</key><dict>")
		for _, e := range environment {
			key, value, _ := strings.Cut(e, "=")
			fmt.Fprintf(&env, "<key>%s</key><string>%s</string>", plist(key), plist(value))
		}
		env.WriteString("</dict>\n")
	}
	var minutes strings.Builder
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&minutes, "<dict><key>Minute</key><integer>%d</integer></dict>", i)
	}
	return "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n<plist version=\"1.0\"><dict>\n" +
		"<key>Label</key><string>" + label + "</string>\n<key>ProgramArguments</key><array><string>" + plist(exe) + "</string><string>schedule</string><string>tick</string></array>\n" +
		env.String() + "<key>AbandonProcessGroup</key><true/>\n" +
		"<key>StartCalendarInterval</key><array>" + minutes.String() + "</array>\n</dict></plist>\n"
}

// The tick may cold-start a supervisor and shims that outlive it. setsid
// leaves them in the systemd service cgroup, so limit cleanup to the tick.
// Pass the binary as an argument. systemd restricts characters in the program
// name even when quoted. The colon disables systemd environment expansion.
// The fixed shell program forwards argv verbatim, including paths containing '='.
func renderSystemdService(exe string, environment ...string) string {
	var env strings.Builder
	for _, e := range environment {
		fmt.Fprintf(&env, "Environment=\"%s\"\n", systemdEscape(e))
	}
	return "[Unit]\nDescription=Swarm schedule tick\n[Service]\nType=oneshot\nKillMode=process\n" + env.String() + "ExecStart=:/bin/sh -c 'exec \"$@\"' swarm-schedule \"" + systemdEscape(exe) + "\" schedule tick\n"
}
func renderSystemdTimer() string {
	return "[Unit]\nDescription=Run the Swarm schedule tick every minute\n[Timer]\nOnCalendar=*-*-* *:*:00\nPersistent=true\nAccuracySec=1s\nUnit=swarm-schedule.service\n[Install]\nWantedBy=timers.target\n"
}
func systemdEscape(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	s = strings.ReplaceAll(s, "%", "%%")
	return s
}
