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
	"errors"
	"fmt"
	"io"
	"os"
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

// InstalledExec is the program the unit installed for stateDir names, read from the FILE.
// It reports ErrNotInstalled when there is no unit to read, which is the same answer
// Ensure and Stop give for the same state dir.
//
// It exists because a unit is a CLAIM ABOUT A PATH and a path can stop being true
// (agents-tracker-nx44.4). Nothing re-checked it because nothing could read it back: this
// package could write a unit and never look at one. See StampedExec for why the check is a
// parse rather than a re-render.
func InstalledExec(stateDir string) (string, error) {
	p, err := HostPlatform()
	if err != nil {
		return "", err
	}
	path, err := UnitPath(p, stateDir)
	if err != nil {
		return "", err
	}
	unit, err := os.ReadFile(path)
	if err != nil {
		// Same shape as requireUnit: an unreadable unit is treated as none, because every
		// caller's next act is to install one.
		return "", fmt.Errorf("%w at %s", ErrNotInstalled, path)
	}
	return StampedExec(p, unit)
}

// StampedExec is the program a rendered unit names.
//
// IT PARSES THE FILE AND DOES NOT RE-RENDER A SPEC. The file is what the supervisor is
// actually holding, and the two can differ for one reason that matters: an operator who
// hand-edits the installed unit to get a broken machine back. Answering with what this
// process WOULD have written would report the path the operator just removed.
//
// A unit naming no program is an ERROR and never the empty string. An empty answer would
// reach a caller as a path that merely is not executable, so the day this parse stops
// matching the templates it would silently re-stamp and reload every unit on every pair,
// which is the opposite of what a check exists for.
func StampedExec(p Platform, unit []byte) (string, error) {
	switch p {
	case PlatformLaunchd:
		return launchdExec(unit)
	case PlatformSystemd:
		return systemdExec(unit)
	default:
		return "", fmt.Errorf("supervise: unknown platform %q", p)
	}
}

// launchdExec returns the first string of the plist's ProgramArguments array -- the program
// launchd execs. Nothing is trimmed: a path padded with whitespace is one launchd cannot
// run, and reporting it verbatim is what lets a caller notice.
func launchdExec(unit []byte) (string, error) {
	dec := xml.NewDecoder(bytes.NewReader(unit))
	key, inArgs := "", false
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("supervise: read launchd unit: %w", err)
		}
		el, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch el.Name.Local {
		case "key":
			var s string
			if err := dec.DecodeElement(&s, &el); err != nil {
				return "", fmt.Errorf("supervise: read launchd unit: %w", err)
			}
			key = strings.TrimSpace(s)
		case "array":
			inArgs = key == "ProgramArguments"
		case "string":
			if !inArgs {
				continue
			}
			var s string
			if err := dec.DecodeElement(&s, &el); err != nil {
				return "", fmt.Errorf("supervise: read launchd unit: %w", err)
			}
			return s, nil
		}
	}
	return "", errors.New("supervise: this launchd unit names no ProgramArguments program")
}

// systemdExec returns the unit's ExecStart value. systemd ignores whitespace around the
// separator, so this does too -- and the VALUE is then taken verbatim, for launchdExec's
// reason.
func systemdExec(unit []byte) (string, error) {
	for _, line := range strings.Split(string(unit), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(name) != "ExecStart" {
			continue
		}
		if value = strings.TrimSpace(value); value != "" {
			return value, nil
		}
	}
	return "", errors.New("supervise: this systemd unit names no ExecStart program")
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
//
// CONFINEMENT (ADR-007 B41, restated by B62(3)). D4/R1 claims sidecar isolation limits the
// blast radius on daemon/PTY state. What follows is what a per-user service manager can
// actually deliver on that, and -- just as load-bearing -- what it cannot.
//
// The gateway dials the relay over TLS, CONNECTS to (never creates) the daemon's
// remote.sock, reads and writes SWARM_DAEMON_STATE, and may append to LogPath. Each
// directive below is justified against exactly that, and every one of them fails SOFT:
// the gateway gets an errno, it is not killed.
//
//	NoNewPrivileges=yes         no setuid/setgid/fscaps escalation through execve. It is
//	                            also the kernel's precondition for an unprivileged process
//	                            to load a seccomp filter at all (seccomp(2): CAP_SYS_ADMIN
//	                            or no_new_privs), so the three seccomp settings below rest
//	                            on it. Stated explicitly rather than left to systemd's
//	                            inference, because the dependency is not obvious.
//	RestrictAddressFamilies=    the four families the gateway uses, and nothing else --
//	                            AF_UNIX for remote.sock, AF_INET/AF_INET6 for the relay,
//	                            AF_NETLINK because glibc's getaddrinfo opens one in
//	                            check_pf(). Release builds are CGO_ENABLED=0 and use Go's
//	                            own resolver, which needs no netlink; a source-built
//	                            gateway (resolveGatewayBinary supports one) falls back to
//	                            the cgo resolver wherever nsswitch.conf demands it, and
//	                            name resolution that fails on only some hosts is precisely
//	                            the production outage this block must not introduce. A
//	                            denied socket() returns EAFNOSUPPORT. AF_PACKET and the
//	                            rest of the exotica are gone.
//	RestrictNamespaces=yes      the gateway creates no namespace, and unprivileged
//	                            user-namespace creation is the standard escalation
//	                            primitive left to a compromised unprivileged process.
//	                            unshare/clone/setns with a CLONE_NEW* flag return EPERM;
//	                            Go's clone() for thread creation sets no such flag.
//	SystemCallArchitectures=    systemd.exec(5) recommends it for exactly the reason it is
//	                            here: seccomp filters are per-ABI, so a secondary ABI would
//	                            otherwise walk around RestrictAddressFamilies=.
//	UMask=0077                  PB-LIFE-4. A user unit inherits the manager's 0022; the
//	                            state dir is a 0700 tree and must stay one.
//
// NOT APPLIED, and not by oversight:
//
// ProtectSystem=, ProtectHome=, PrivateTmp= (and ReadWritePaths=, the only thing that
// could have punched the state dir back out of them) do not work here. systemd.exec(5):
// "some sandboxing functionality is generally not available in user services (i.e.
// services run by the per-user service manager). Specifically, the various settings
// requiring file system namespacing support (such as ProtectSystem=) are not available, as
// the underlying kernel functionality is only accessible to privileged processes." The
// failure is not a silent no-op -- the service exits EXIT_NAMESPACE (226) before it runs,
// Restart=on-failure retries it, and StartLimitBurst parks the unit in StateFailed. The
// owner's gateway simply never starts. The escape hatch systemd offers, PrivateUsers=true,
// needs unprivileged user namespaces to be available (which Debian historically and Ubuntu
// 24.04's AppArmor policy both restrict) and would remap the very UID the daemon's 0600
// remote.sock is checked against. Neither is worth a gateway that will not start.
//
// SystemCallFilter= is a judgement rather than a limitation: it WOULD work in a user unit.
// Its default action on a denied call is to kill the process with SIGSYS, and
// SystemCallErrorNumber= only swaps that for an errno the Go runtime did not expect on a
// call it makes internally. Either way the process dies on a syscall it never made before,
// the trigger is a Go toolchain upgrade rather than any change in this repo, and because
// the death is a failure exit the supervisor escalates it to StateFailed -- a permanent,
// silent remote-control outage. Bought against a process that already runs with the
// owner's own authority and has no User= to escalate away from, that is a bad trade.
// Revisit it in an ADR, not by editing the template.
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

# Confinement (ADR-007 B41). One prctl and three seccomp filters -- all a per-user service
# manager can enforce. Each fails by handing the gateway an errno, never by killing it.
#
# systemd's file-system sandbox is ABSENT ON PURPOSE. systemd.exec(5) states those settings
# "are not available" to a per-user service manager: the kernel lets only a privileged
# process build the mount namespace they need. Adding one here would not confine this
# gateway, it would stop it from starting, and Restart= above would turn that into a
# permanent restart loop. A syscall filter is left out on a separate judgement. Both
# omissions are argued in full in internal/remote/supervise/unit.go -- read it before
# "completing" this block.
NoNewPrivileges=yes
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK
RestrictNamespaces=yes
SystemCallArchitectures=native
UMask=0077

[Install]
WantedBy=default.target
`))
