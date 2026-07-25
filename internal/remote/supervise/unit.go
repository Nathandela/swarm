// Package supervise generates the swarm-remote gateway's supervision unit and installs
// it through the owner-invoked CLI.
//
// ADR-007 D5 requires the gateway to run "under an external supervisor (macOS launchd
// LaunchAgent, Linux systemd user unit), never spawned by the daemon": the daemon owns
// PTYs and spawns agents, and the gateway is the one component parsing
// attacker-influenced relay bytes, so the daemon must not be its parent. This package is
// therefore the ONE source both unit types are generated from (PB-LIFE-1), plus the seam
// (Supervisor) through which `swarm remote init` installs a unit and `swarm remote pair`
// activates it.
//
// Units are installed under <stateDir>/remote/units, NOT ~/Library/LaunchAgents or
// ~/.config/systemd/user. Both supervisors load a unit from an absolute path, the state
// dir is already the 0700 tree that guards the machine identity -- so PB-LIFE-4's
// owner-only permissions come for free -- and nothing this package WRITES ever lands in
// the real system's supervision directories.
//
// That is a statement about this package's own writes only. Ensure and Stop ask launchd or
// systemd to act on the owner's live session, and what the init system then does is its
// own business -- `systemctl --user enable` links the unit into ~/.config/systemd/user,
// and launchd registers the label in the gui/<uid> domain. Which is why runUnit refuses to
// run at all inside a test binary.
package supervise

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"
	"time"
)

// Platform is a host supervision system. There are exactly two, and an unknown one is
// always refused rather than defaulted to either.
type Platform string

const (
	PlatformLaunchd Platform = "launchd"
	PlatformSystemd Platform = "systemd"
)

// HostPlatform returns the Platform for the running GOOS.
func HostPlatform() (Platform, error) {
	switch runtime.GOOS {
	case "darwin":
		return PlatformLaunchd, nil
	case "linux":
		return PlatformSystemd, nil
	default:
		return "", fmt.Errorf("supervise: no gateway supervision unit for GOOS %q", runtime.GOOS)
	}
}

const (
	// LaunchdLabel is the plist Label, which is also the launchd service name the
	// gui/<uid> domain addresses the job by.
	LaunchdLabel = "com.swarm.remote"
	// SystemdUnitName is the systemd user unit's file name.
	SystemdUnitName = "swarm-remote.service"
	// DefaultBackoff is the restart throttle a zero Spec.Backoff resolves to. PB-LIFE-5's
	// floor: a caller that forgets to set one must not get an unthrottled unit.
	DefaultBackoff = 10 * time.Second
)

// PB-LIFE-5's rate limiter, in the terms systemd expresses it. launchd has no equivalent
// burst window (ThrottleInterval is its whole restart policy), so this is the systemd
// half only: after startLimitBurst failures inside startLimitInterval the unit stops
// being restarted and lands in StateFailed, where an operator has to look at it.
const (
	startLimitInterval = 5 * time.Minute
	startLimitBurst    = 5
)

// Spec is the ONE source both renderers consume. It carries PATHS ONLY -- never key
// material, never a token (PB-LIFE-4): everything the gateway authenticates with lives
// under StateDir, in a 0700 tree.
type Spec struct {
	Exec         string        // absolute path to the swarm-remote binary
	Owner        string        // username the gateway runs as; never root, never empty
	StateDir     string        // SWARM_DAEMON_STATE
	RemoteSocket string        // SWARM_DAEMON_REMOTE_SOCK (optional; empty omits it)
	Backoff      time.Duration // restart throttle; 0 means DefaultBackoff, negative errors
	LogPath      string        // stdout+stderr destination (optional)
}

// unitView is a validated Spec in the terms the templates render: whole seconds instead
// of durations, and nothing left to decide.
type unitView struct {
	Label                     string
	Exec                      string
	Owner                     string
	StateDir                  string
	RemoteSocket              string
	LogPath                   string
	BackoffSeconds            int
	StartLimitIntervalSeconds int
	StartLimitBurst           int
}

// resolve validates spec and applies its defaults. A unit that would run as root, or
// that names a binary or a state dir by a relative path -- which a supervisor resolves
// against a working directory neither swarm nor the operator controls -- is refused at
// generation time rather than installed and debugged later.
func (s Spec) resolve() (unitView, error) {
	switch {
	case s.Exec == "":
		return unitView{}, fmt.Errorf("supervise: Spec.Exec is empty; the unit would name no program to run")
	case !filepath.IsAbs(s.Exec):
		return unitView{}, fmt.Errorf("supervise: Spec.Exec %q is relative; a supervisor resolves it against a working directory nobody controls", s.Exec)
	case s.Owner == "":
		return unitView{}, fmt.Errorf("supervise: Spec.Owner is empty; the unit must name the user it is meant to run as")
	case s.Owner == "root":
		return unitView{}, fmt.Errorf("supervise: Spec.Owner is root; the gateway runs as the owner (ADR-007 D4)")
	case s.StateDir == "":
		return unitView{}, fmt.Errorf("supervise: Spec.StateDir is empty; the gateway would find no identity at all")
	case !filepath.IsAbs(s.StateDir):
		return unitView{}, fmt.Errorf("supervise: Spec.StateDir %q is relative; a supervised process has no meaningful working directory", s.StateDir)
	case s.Backoff < 0:
		return unitView{}, fmt.Errorf("supervise: Spec.Backoff %v is negative; a restart throttle cannot run backwards", s.Backoff)
	}
	// A unit file is line-oriented on the systemd side and has no quoting that survives a
	// newline, so a path carrying one would inject a directive rather than be escaped.
	for name, v := range map[string]string{"Exec": s.Exec, "Owner": s.Owner, "StateDir": s.StateDir, "RemoteSocket": s.RemoteSocket, "LogPath": s.LogPath} {
		if strings.ContainsAny(v, "\r\n") {
			return unitView{}, fmt.Errorf("supervise: Spec.%s contains a line break; a unit file cannot carry one", name)
		}
	}

	backoff := s.Backoff
	if backoff == 0 {
		backoff = DefaultBackoff
	}
	return unitView{
		Label:        LaunchdLabel,
		Exec:         s.Exec,
		Owner:        s.Owner,
		StateDir:     s.StateDir,
		RemoteSocket: s.RemoteSocket,
		LogPath:      s.LogPath,
		// Rounded UP: both unit types take whole seconds, and rounding a sub-second
		// backoff down to zero would leave the gateway unthrottled (PB-LIFE-5).
		BackoffSeconds:            int((backoff + time.Second - 1) / time.Second),
		StartLimitIntervalSeconds: int(startLimitInterval / time.Second),
		StartLimitBurst:           startLimitBurst,
	}, nil
}

// Render generates p's unit file from spec. A refused spec renders nothing.
func Render(p Platform, spec Spec) ([]byte, error) {
	var tmpl *template.Template
	switch p {
	case PlatformLaunchd:
		tmpl = launchdTemplate
	case PlatformSystemd:
		tmpl = systemdTemplate
	default:
		return nil, fmt.Errorf("supervise: unknown platform %q", p)
	}
	view, err := spec.resolve()
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, view); err != nil {
		return nil, fmt.Errorf("supervise: render %s unit: %w", p, err)
	}
	return buf.Bytes(), nil
}

// UnitFileName is p's unit file name.
func UnitFileName(p Platform) (string, error) {
	switch p {
	case PlatformLaunchd:
		return LaunchdLabel + ".plist", nil
	case PlatformSystemd:
		return SystemdUnitName, nil
	default:
		return "", fmt.Errorf("supervise: unknown platform %q", p)
	}
}

// UnitDir is the directory units are installed into: inside the state dir, whose 0700
// tree already provides PB-LIFE-4's owner-only permissions.
func UnitDir(stateDir string) string { return filepath.Join(stateDir, "remote", "units") }

// UnitPath is p's installed unit file for stateDir.
func UnitPath(p Platform, stateDir string) (string, error) {
	name, err := UnitFileName(p)
	if err != nil {
		return "", err
	}
	return filepath.Join(UnitDir(stateDir), name), nil
}

// xmlEscape renders a path as plist text. Nothing in a Spec is trusted to be
// XML-clean just because it is a path.
func xmlEscape(s string) string {
	var buf bytes.Buffer
	if err := xml.EscapeText(&buf, []byte(s)); err != nil {
		return ""
	}
	return buf.String()
}

// launchdTemplate is the LaunchAgent plist.
//
// KeepAlive is the DICT form {SuccessfulExit: false}, never <true/>: bare KeepAlive=true
// restarts the job on EVERY exit including a clean one, which is precisely the permanent
// crash loop PB-LIFE-3 forbids once the paired device is revoked and the gateway has
// nothing left to serve. RunAtLoad is safe under that policy -- with no paired device the
// gateway exits ExitQuiescent and launchd leaves it alone -- and it is what brings the
// gateway back after a reboot.
var launchdTemplate = template.Must(template.New("launchd").Funcs(template.FuncMap{"x": xmlEscape}).Parse(
	`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<!--
  swarm remote gateway LaunchAgent. Generated by ` + "`swarm remote init`" + `; edits are lost
  on the next run.

  owner: {{x .Owner}}

  Load it as that user: launchctl bootstrap gui/$UID on this file. A LaunchAgent runs as
  whoever loads it, so this file sets no UserName or GroupName; launchd honors those for
  LaunchDaemons only, and stating an authority the file cannot confer is how an operator
  ends up debugging a job that never ran as the user it names.

  The file carries PATHS only. Everything the gateway authenticates with lives under
  SWARM_DAEMON_STATE, in a 0700 tree.
-->
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>{{x .Label}}</string>

	<key>ProgramArguments</key>
	<array>
		<string>{{x .Exec}}</string>
	</array>

	<key>EnvironmentVariables</key>
	<dict>
		<key>SWARM_DAEMON_STATE</key>
		<string>{{x .StateDir}}</string>
{{- if .RemoteSocket}}
		<key>SWARM_DAEMON_REMOTE_SOCK</key>
		<string>{{x .RemoteSocket}}</string>
{{- end}}
	</dict>

	<key>RunAtLoad</key>
	<true/>

	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>

	<key>ThrottleInterval</key>
	<integer>{{.BackoffSeconds}}</integer>
{{- if .LogPath}}

	<key>StandardOutPath</key>
	<string>{{x .LogPath}}</string>
	<key>StandardErrorPath</key>
	<string>{{x .LogPath}}</string>
{{- end}}
</dict>
</plist>
`))

// systemdTemplate is the systemd USER unit. Restart=on-failure is the systemd spelling of
// the launchd policy above: a clean exit is quiescence, never a restart. The start limit
// sits in [Unit] because that is the section systemd reads it from -- the directives moved
// there in v229, and in [Service] the throttle silently does nothing.
var systemdTemplate = template.Must(template.New("systemd").Parse(
	`# swarm remote gateway. Generated by ` + "`swarm remote init`" + `; edits are lost on the next run.
#
# owner: {{.Owner}}
#
# A systemd USER unit (systemctl --user), so it already runs as that user and sets no
# User or Group directive. It carries PATHS only: everything the gateway authenticates
# with lives under SWARM_DAEMON_STATE, in a 0700 tree.

[Unit]
Description=swarm remote gateway
StartLimitIntervalSec={{.StartLimitIntervalSeconds}}
StartLimitBurst={{.StartLimitBurst}}

[Service]
ExecStart={{.Exec}}
Restart=on-failure
RestartSec={{.BackoffSeconds}}
Environment=SWARM_DAEMON_STATE={{.StateDir}}
{{- if .RemoteSocket}}
Environment=SWARM_DAEMON_REMOTE_SOCK={{.RemoteSocket}}
{{- end}}
{{- if .LogPath}}
StandardOutput=append:{{.LogPath}}
StandardError=append:{{.LogPath}}
{{- end}}

[Install]
WantedBy=default.target
`))
