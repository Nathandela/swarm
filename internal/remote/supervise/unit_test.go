package supervise

// FAILING-FIRST tests for slice S4 — PB-LIFE-1, PB-LIFE-4, PB-LIFE-5: the gateway's
// supervision unit, rendered for launchd AND systemd from ONE Spec.
//
// WHY THIS PACKAGE EXISTS. ADR-007 D5 requires cmd/swarm-remote to run "under an
// external supervisor (macOS launchd LaunchAgent, Linux systemd user unit), never
// spawned by the daemon". Today nothing generates such a unit and nothing starts the
// gateway — an owner has to run the binary by hand. This package is the ONE source both
// unit types are generated from, plus the seam (Supervisor) through which the
// owner-invoked CLI activates them.
//
// INTENDED PRODUCTION SURFACE (RED — none of it exists yet; GREEN implements it):
//
//	type Platform string
//	const (
//		PlatformLaunchd Platform = "launchd"
//		PlatformSystemd Platform = "systemd"
//	)
//
//	// HostPlatform returns the Platform for the running GOOS; any other GOOS errors.
//	func HostPlatform() (Platform, error)
//
//	// Spec is the ONE source both renderers consume. It carries PATHS ONLY -- never key
//	// material, never a token (PB-LIFE-4).
//	type Spec struct {
//		Exec         string        // absolute path to the swarm-remote binary
//		Owner        string        // username the gateway runs as; never root, never empty
//		StateDir     string        // SWARM_DAEMON_STATE
//		RemoteSocket string        // SWARM_DAEMON_REMOTE_SOCK (optional; empty omits it)
//		Backoff      time.Duration // restart throttle; 0 means DefaultBackoff, negative errors
//		LogPath      string        // stdout+stderr destination (optional)
//	}
//
//	const (
//		LaunchdLabel    = "com.swarm.remote"     // the plist Label / launchd service name
//		SystemdUnitName = "swarm-remote.service" // the systemd user unit name
//		DefaultBackoff  = 10 * time.Second       // PB-LIFE-5 floor when Spec.Backoff is 0
//	)
//
//	func Render(p Platform, spec Spec) ([]byte, error)
//	func UnitFileName(p Platform) (string, error)
//	func UnitDir(stateDir string) string                     // <stateDir>/remote/units
//	func UnitPath(p Platform, stateDir string) (string, error)
//
// THE INSTALL LOCATION IS DELIBERATE. Units are installed under <stateDir>/remote/units,
// NOT ~/Library/LaunchAgents or ~/.config/systemd/user. Both supervisors can load a unit
// from an arbitrary absolute path (`launchctl bootstrap gui/$UID <plist>`,
// `systemctl --user link <unit>`), the state dir is already 0700 owner-only, and it means
// NO TEST -- this one or any other in the tree -- can ever write into the real system's
// supervision directories. Owner-only permissions (PB-LIFE-4) then come from the same
// 0700 tree that already guards machine.key.

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/machineid"
)

// testSpec is a fully-populated Spec pointing at a temp state dir. Every path is
// synthetic, so nothing a test renders or installs can reach the real system.
func testSpec(t *testing.T) Spec {
	t.Helper()
	dir := t.TempDir()
	return Spec{
		Exec:         "/usr/local/bin/swarm-remote",
		Owner:        "swarmowner",
		StateDir:     dir,
		RemoteSocket: filepath.Join(dir, "remote.sock"),
		Backoff:      30 * time.Second,
		LogPath:      filepath.Join(dir, "remote", "gateway.log"),
	}
}

// --- launchd -----------------------------------------------------------------------

// plistBody is the minimum decode needed to prove the rendered plist is well-formed XML
// and to read back its top-level key list. Values stay raw because a plist dict is
// heterogeneous; they are asserted with plistValue below.
type plistBody struct {
	XMLName xml.Name `xml:"plist"`
	Version string   `xml:"version,attr"`
	Dict    struct {
		Keys []string `xml:"key"`
		Raw  string   `xml:",innerxml"`
	} `xml:"dict"`
}

// plistValue returns the XML element immediately following <key>name</key>, e.g.
// "<false/>" or "<integer>30</integer>". Empty means the key is absent.
//
// It BALANCES tags rather than stopping at the first closing one, because the two values
// this file cares most about -- KeepAlive and EnvironmentVariables -- are themselves
// dicts, and a scan that stopped early would return "<dict><key>SuccessfulExit</key>"
// and silently assert nothing about the policy underneath it.
func plistValue(body, name string) string {
	key := "<key>" + name + "</key>"
	i := strings.Index(body, key)
	if i < 0 {
		return ""
	}
	return firstElement(strings.TrimLeft(body[i+len(key):], " \t\r\n"))
}

// firstElement returns the element s opens with, children included; "" when s does not
// open with one.
func firstElement(s string) string {
	dec := xml.NewDecoder(strings.NewReader(s))
	depth := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			return ""
		}
		switch tok.(type) {
		case xml.StartElement:
			depth++
		case xml.EndElement:
			if depth--; depth == 0 {
				return strings.TrimSpace(s[:dec.InputOffset()])
			}
		}
	}
}

// TestRenderLaunchd_RestartPolicyOwnerAndNoSecrets pins the LaunchAgent plist
// (PB-LIFE-1): it is a well-formed property list (plutil -lint where available, an XML
// decode everywhere else), it runs the gateway binary from Spec.Exec, it restarts on a
// FAILURE exit only, it starts at load, and it names the owner it is meant to run as.
//
// The restart policy is the load-bearing assertion. KeepAlive must be the DICT form
// {SuccessfulExit: false}, not <true/>: bare `KeepAlive=true` restarts the job on EVERY
// exit including a clean one, which is precisely the permanent crash loop PB-LIFE-3
// forbids once the paired device is revoked and the gateway has nothing to serve.
func TestRenderLaunchd_RestartPolicyOwnerAndNoSecrets(t *testing.T) {
	spec := testSpec(t)
	out, err := Render(PlatformLaunchd, spec)
	if err != nil {
		t.Fatalf("Render(launchd) error = %v, want nil", err)
	}
	body := string(out)

	var pl plistBody
	if err := xml.Unmarshal(out, &pl); err != nil {
		t.Fatalf("rendered plist is not well-formed XML: %v\n%s", err, body)
	}
	if pl.Version != "1.0" {
		t.Errorf("plist version = %q, want %q", pl.Version, "1.0")
	}
	for _, k := range []string{"Label", "ProgramArguments", "EnvironmentVariables", "KeepAlive", "ThrottleInterval", "RunAtLoad"} {
		if !slices.Contains(pl.Dict.Keys, k) {
			t.Errorf("plist missing top-level key %q; keys = %v", k, pl.Dict.Keys)
		}
	}

	if got := plistValue(pl.Dict.Raw, "Label"); !strings.Contains(got, LaunchdLabel) {
		t.Errorf("plist Label = %q, want it to carry %q", got, LaunchdLabel)
	}
	// The job runs the gateway binary itself: ProgramArguments[0] is Spec.Exec, and no
	// shell wrapper stands between the supervisor and the process whose exit status the
	// restart policy is written against.
	if args := plistValue(pl.Dict.Raw, "ProgramArguments"); !strings.Contains(args, "<string>"+spec.Exec+"</string>") {
		t.Errorf("plist ProgramArguments = %q, want it to run %q directly", args, spec.Exec)
	}
	if strings.Contains(body, "/bin/sh") || strings.Contains(body, "/bin/bash") {
		t.Errorf("plist runs the gateway through a shell; the supervisor must see the gateway's own exit status:\n%s", body)
	}

	// Restart on FAILURE only (PB-LIFE-1 restart-on-exit, PB-LIFE-3 no crash loop).
	keepAlive := plistValue(pl.Dict.Raw, "KeepAlive")
	if !strings.Contains(keepAlive, "SuccessfulExit") {
		t.Fatalf("plist KeepAlive = %q, want the dict form with a SuccessfulExit key", keepAlive)
	}
	if got := plistValue(keepAlive, "SuccessfulExit"); got != "<false/>" {
		t.Errorf("plist KeepAlive/SuccessfulExit = %q, want <false/> (restart on failure exits only)", got)
	}
	// RunAtLoad is what brings the gateway back after a reboot/login. It is safe with the
	// policy above: with no paired device the gateway exits ExitQuiescent and launchd
	// leaves it alone.
	if got := plistValue(pl.Dict.Raw, "RunAtLoad"); got != "<true/>" {
		t.Errorf("plist RunAtLoad = %q, want <true/> (the gateway must come back after a reboot)", got)
	}

	// Environment carries the state dir and the daemon's remote socket by PATH.
	env := plistValue(pl.Dict.Raw, "EnvironmentVariables")
	if !strings.Contains(env, "SWARM_DAEMON_STATE") || !strings.Contains(env, spec.StateDir) {
		t.Errorf("plist EnvironmentVariables = %q, want SWARM_DAEMON_STATE=%s", env, spec.StateDir)
	}
	if !strings.Contains(env, "SWARM_DAEMON_REMOTE_SOCK") || !strings.Contains(env, spec.RemoteSocket) {
		t.Errorf("plist EnvironmentVariables = %q, want SWARM_DAEMON_REMOTE_SOCK=%s", env, spec.RemoteSocket)
	}

	// The owner is recorded (PB-LIFE-1 "running as the owner"). A LaunchAgent runs as the
	// user that loads it, so the plist must NOT carry UserName/GroupName -- those keys are
	// honored only for LaunchDaemons, and writing them here would state an authority the
	// file cannot actually confer.
	if !strings.Contains(body, spec.Owner) {
		t.Errorf("plist does not name its intended owner %q:\n%s", spec.Owner, body)
	}
	for _, k := range []string{"UserName", "GroupName"} {
		if plistValue(pl.Dict.Raw, k) != "" {
			t.Errorf("plist carries a %s key; a LaunchAgent runs as the loading user and must not claim otherwise", k)
		}
	}
	if strings.Contains(body, "<string>root</string>") {
		t.Errorf("plist references root; the gateway runs as the owner (ADR-007 D4):\n%s", body)
	}

	// plutil is the authoritative linter where it exists (PB-LIFE-1's stated criterion).
	if plutil, err := exec.LookPath("plutil"); err == nil {
		path := filepath.Join(t.TempDir(), "com.swarm.remote.plist")
		if err := os.WriteFile(path, out, 0o600); err != nil {
			t.Fatalf("write plist for plutil: %v", err)
		}
		if lint, err := exec.Command(plutil, "-lint", path).CombinedOutput(); err != nil {
			t.Errorf("plutil -lint failed: %v\n%s\n%s", err, lint, body)
		}
	}
}

// --- systemd -----------------------------------------------------------------------

// unitFile is a rendered systemd unit parsed into section -> key -> values. Directives
// repeat legally (Environment=), so values are a slice.
type unitFile map[string]map[string][]string

func parseUnit(t *testing.T, b []byte) unitFile {
	t.Helper()
	u := unitFile{}
	section := ""
	for _, raw := range strings.Split(string(b), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.Trim(line, "[]")
			if u[section] == nil {
				u[section] = map[string][]string{}
			}
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			t.Errorf("systemd unit line %q is neither a section nor a key=value directive", line)
			continue
		}
		if u[section] == nil {
			u[section] = map[string][]string{}
		}
		u[section][strings.TrimSpace(k)] = append(u[section][strings.TrimSpace(k)], strings.TrimSpace(v))
	}
	return u
}

func (u unitFile) one(t *testing.T, section, key string) string {
	t.Helper()
	vs := u[section][key]
	if len(vs) != 1 {
		t.Errorf("systemd unit [%s] %s = %v, want exactly one value", section, key, vs)
		return ""
	}
	return vs[0]
}

// TestRenderSystemd_UserUnitRestartsOnFailureOnly pins the systemd side of PB-LIFE-1 and
// PB-LIFE-5. It must be a USER unit (WantedBy=default.target, no User=/Group= directive):
// a user unit already runs as the owner, which is exactly the authority ADR-007 D4 wants,
// while a system unit would run outside the owner's session and outside the owner's key
// custody. Restart=on-failure is the systemd spelling of the launchd policy above, and
// StartLimitIntervalSec/StartLimitBurst must sit in [Unit] -- in [Service] systemd
// ignores them (they moved sections in v229) and the throttle silently does nothing.
func TestRenderSystemd_UserUnitRestartsOnFailureOnly(t *testing.T) {
	spec := testSpec(t)
	out, err := Render(PlatformSystemd, spec)
	if err != nil {
		t.Fatalf("Render(systemd) error = %v, want nil", err)
	}
	u := parseUnit(t, out)

	if got := u.one(t, "Service", "ExecStart"); got != spec.Exec {
		t.Errorf("[Service] ExecStart = %q, want %q", got, spec.Exec)
	}
	if got := u.one(t, "Service", "Restart"); got != "on-failure" {
		t.Errorf("[Service] Restart = %q, want %q (a clean exit is quiescence, never a restart)", got, "on-failure")
	}
	if got := u.one(t, "Service", "RestartSec"); got != "30" {
		t.Errorf("[Service] RestartSec = %q, want %q (Spec.Backoff)", got, "30")
	}
	if got := u.one(t, "Install", "WantedBy"); got != "default.target" {
		t.Errorf("[Install] WantedBy = %q, want %q (a user unit, not a system unit)", got, "default.target")
	}
	for _, k := range []string{"User", "Group", "DynamicUser"} {
		if vs := u["Service"][k]; len(vs) != 0 {
			t.Errorf("[Service] %s = %v; a systemd USER unit already runs as the owner and must not set it", k, vs)
		}
	}

	// PB-LIFE-5: the rate limiter, in the section systemd actually reads it from.
	for _, k := range []string{"StartLimitIntervalSec", "StartLimitBurst"} {
		if len(u["Unit"][k]) != 1 {
			t.Errorf("[Unit] %s missing; without it a failing gateway restarts unthrottled (PB-LIFE-5). [Service] had %v", k, u["Service"][k])
		}
	}

	env := strings.Join(u["Service"]["Environment"], " ")
	if !strings.Contains(env, "SWARM_DAEMON_STATE="+spec.StateDir) {
		t.Errorf("[Service] Environment = %q, want SWARM_DAEMON_STATE=%s", env, spec.StateDir)
	}
	if !strings.Contains(env, "SWARM_DAEMON_REMOTE_SOCK="+spec.RemoteSocket) {
		t.Errorf("[Service] Environment = %q, want SWARM_DAEMON_REMOTE_SOCK=%s", env, spec.RemoteSocket)
	}
	if !strings.Contains(string(out), spec.Owner) {
		t.Errorf("systemd unit does not name its intended owner %q:\n%s", spec.Owner, out)
	}
}

// --- one source ---------------------------------------------------------------------

// TestRender_OneSourceDrivesBothUnits is PB-LIFE-1's "generated from ONE source" clause.
// A single Spec must reach both unit types: change a field and BOTH outputs change. The
// failure this guards against is the ordinary one -- a launchd plist that gets fixed and a
// systemd unit that quietly keeps the old exec path or the old backoff.
func TestRender_OneSourceDrivesBothUnits(t *testing.T) {
	base := testSpec(t)
	changed := base
	changed.Exec = "/opt/swarm/bin/swarm-remote"
	changed.Backoff = 45 * time.Second

	for _, p := range []Platform{PlatformLaunchd, PlatformSystemd} {
		before, err := Render(p, base)
		if err != nil {
			t.Fatalf("Render(%s, base) error = %v", p, err)
		}
		after, err := Render(p, changed)
		if err != nil {
			t.Fatalf("Render(%s, changed) error = %v", p, err)
		}
		if !strings.Contains(string(before), base.Exec) {
			t.Errorf("%s unit does not carry base Exec %q", p, base.Exec)
		}
		if !strings.Contains(string(after), changed.Exec) {
			t.Errorf("%s unit does not carry changed Exec %q; the two unit types are not generated from one Spec", p, changed.Exec)
		}
		if strings.Contains(string(after), base.Exec) {
			t.Errorf("%s unit still carries the OLD Exec %q after the Spec changed", p, base.Exec)
		}
		if !strings.Contains(string(after), "45") {
			t.Errorf("%s unit does not carry changed Backoff 45s", p)
		}
	}
}

// TestRender_RefusesUnitsThatCannotRunAsTheOwner pins the validation half of PB-LIFE-1: a
// unit that would run as root, or that names a binary by a relative path (which a
// supervisor resolves against a working directory neither swarm nor the operator
// controls), is refused at generation time rather than installed and debugged later.
func TestRender_RefusesUnitsThatCannotRunAsTheOwner(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Spec)
	}{
		{"empty owner", func(s *Spec) { s.Owner = "" }},
		{"root owner", func(s *Spec) { s.Owner = "root" }},
		{"relative exec", func(s *Spec) { s.Exec = "swarm-remote" }},
		{"empty exec", func(s *Spec) { s.Exec = "" }},
		{"empty state dir", func(s *Spec) { s.StateDir = "" }},
		{"negative backoff", func(s *Spec) { s.Backoff = -time.Second }},
	}
	for _, tc := range cases {
		for _, p := range []Platform{PlatformLaunchd, PlatformSystemd} {
			t.Run(tc.name+"/"+string(p), func(t *testing.T) {
				spec := testSpec(t)
				tc.mutate(&spec)
				out, err := Render(p, spec)
				if err == nil {
					t.Fatalf("Render(%s, %s) error = nil, want a refusal; got unit:\n%s", p, tc.name, out)
				}
				if out != nil {
					t.Errorf("Render(%s, %s) returned %d bytes alongside its error; a refused spec must render nothing", p, tc.name, len(out))
				}
			})
		}
	}
}

// TestRender_ZeroBackoffStillThrottles is PB-LIFE-5's floor: a caller that forgets to set
// Backoff must not get an unthrottled unit. Zero means DefaultBackoff in BOTH unit types.
func TestRender_ZeroBackoffStillThrottles(t *testing.T) {
	spec := testSpec(t)
	spec.Backoff = 0
	want := int(DefaultBackoff.Seconds())
	if want <= 0 {
		t.Fatalf("DefaultBackoff = %v, want a positive throttle", DefaultBackoff)
	}

	plist, err := Render(PlatformLaunchd, spec)
	if err != nil {
		t.Fatalf("Render(launchd) error = %v", err)
	}
	var pl plistBody
	if err := xml.Unmarshal(plist, &pl); err != nil {
		t.Fatalf("rendered plist is not well-formed XML: %v", err)
	}
	if got := plistValue(pl.Dict.Raw, "ThrottleInterval"); !strings.Contains(got, strconv.Itoa(want)) {
		t.Errorf("plist ThrottleInterval = %q, want DefaultBackoff (%d seconds)", got, want)
	}

	unit, err := Render(PlatformSystemd, spec)
	if err != nil {
		t.Fatalf("Render(systemd) error = %v", err)
	}
	u := parseUnit(t, unit)
	if got := u.one(t, "Service", "RestartSec"); got != strconv.Itoa(want) {
		t.Errorf("[Service] RestartSec = %q, want DefaultBackoff (%d)", got, want)
	}
}

// --- PB-LIFE-4: no credentials, owner-only files -------------------------------------

// TestUnits_CarryNoCredentials is PB-LIFE-4's content half. The gateway's secrets live in
// <stateDir>/remote/machine.key, reached through SWARM_DAEMON_STATE; a unit file is
// world-readable-by-accident-prone config that must reference that state by PATH and
// never inline any of it. This provisions a REAL machine identity and asserts none of its
// bytes -- raw, hex, or base64 -- appear in either rendered unit, and that no environment
// entry is even NAMED like a credential.
func TestUnits_CarryNoCredentials(t *testing.T) {
	spec := testSpec(t)
	// Directory names are not credential names (macOS itself uses /private/tmp).
	spec.StateDir = filepath.Join(spec.StateDir, "private-workspace")
	spec.RemoteSocket = filepath.Join(spec.StateDir, "remote.sock")
	spec.LogPath = filepath.Join(spec.StateDir, "remote", "gateway.log")
	keyPath := filepath.Join(spec.StateDir, "remote", "machine.key")
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		t.Fatalf("mkdir remote dir: %v", err)
	}
	id, err := machineid.Generate("supervise-test-host")
	if err != nil {
		t.Fatalf("generate machine identity: %v", err)
	}
	if err := id.Save(keyPath); err != nil {
		t.Fatalf("save machine identity: %v", err)
	}
	secret, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read machine.key: %v", err)
	}

	credName := regexp.MustCompile(`(?i)(secret|passwd|password|token|psk|private|_key\b|key=)`)
	for _, p := range []Platform{PlatformLaunchd, PlatformSystemd} {
		out, err := Render(p, spec)
		if err != nil {
			t.Fatalf("Render(%s) error = %v", p, err)
		}
		for encoding, blob := range encodings(secret) {
			if strings.Contains(string(out), blob) {
				t.Errorf("%s unit embeds machine.key material (%s encoding); units carry paths, never credentials", p, encoding)
			}
		}
		var envNames []string
		if p == PlatformLaunchd {
			var pl plistBody
			if err := xml.Unmarshal(out, &pl); err != nil {
				t.Fatal(err)
			}
			var env struct {
				Keys []string `xml:"key"`
			}
			if err := xml.Unmarshal([]byte(plistValue(pl.Dict.Raw, "EnvironmentVariables")), &env); err != nil {
				t.Fatal(err)
			}
			envNames = env.Keys
		} else {
			for _, entry := range parseUnit(t, out)["Service"]["Environment"] {
				name, _, _ := strings.Cut(strings.Trim(entry, `"`), "=")
				envNames = append(envNames, name)
			}
		}
		if len(envNames) == 0 {
			t.Fatalf("%s parsed no environment names; credential-name check would be vacuous", p)
		}
		for _, name := range envNames {
			if credName.MatchString(name) {
				t.Errorf("%s environment entry %q is named like a credential; PB-LIFE-4 forbids credentials in units", p, name)
			}
		}
		// The state dir must still be reachable -- by path.
		if !strings.Contains(string(out), spec.StateDir) {
			t.Errorf("%s unit does not reference the state dir %q; the gateway would find no identity at all", p, spec.StateDir)
		}
	}
}

// TestInstall_WritesOwnerOnlyUnderTheStateDir is PB-LIFE-4's permission half, and the
// reason the install location is <stateDir>/remote/units: it proves the whole install
// path is confined to a directory the test owns. The unit file is 0600 inside a 0700
// directory, and its bytes are exactly Render's output for the host platform -- install
// must not be a second, drifting copy of the generator.
func TestInstall_WritesOwnerOnlyUnderTheStateDir(t *testing.T) {
	spec := testSpec(t)
	sup, err := Host(spec.StateDir)
	if err != nil {
		t.Fatalf("Host(%q) error = %v", spec.StateDir, err)
	}
	if err := sup.Install(spec); err != nil {
		t.Fatalf("Install error = %v", err)
	}

	p, err := HostPlatform()
	if err != nil {
		t.Fatalf("HostPlatform error = %v", err)
	}
	path, err := UnitPath(p, spec.StateDir)
	if err != nil {
		t.Fatalf("UnitPath error = %v", err)
	}
	if want := filepath.Join(UnitDir(spec.StateDir), mustUnitFileName(t, p)); path != want {
		t.Errorf("UnitPath = %q, want %q", path, want)
	}
	if !strings.HasPrefix(path, spec.StateDir+string(filepath.Separator)) {
		t.Fatalf("unit installed at %q, OUTSIDE the state dir %q; no test may write into the real supervision dirs", path, spec.StateDir)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat installed unit: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("installed unit mode = %04o, want 0600 (owner-only)", got)
	}
	di, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat unit dir: %v", err)
	}
	if got := di.Mode().Perm(); got != 0o700 {
		t.Errorf("unit dir mode = %04o, want 0700 (owner-only)", got)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read installed unit: %v", err)
	}
	want, err := Render(p, spec)
	if err != nil {
		t.Fatalf("Render(%s) error = %v", p, err)
	}
	if string(got) != string(want) {
		t.Errorf("installed unit differs from Render(%s) output; install must not re-implement the generator", p)
	}

	// Idempotent: `swarm remote init` is idempotent, so re-installing must be too.
	if err := sup.Install(spec); err != nil {
		t.Fatalf("second Install error = %v, want nil (install is idempotent)", err)
	}
}

// TestUnitFileName_BothPlatformsAndNothingElse pins the two filenames and refuses an
// unknown platform rather than defaulting to one of them.
func TestUnitFileName_BothPlatformsAndNothingElse(t *testing.T) {
	if got := mustUnitFileName(t, PlatformLaunchd); got != LaunchdLabel+".plist" {
		t.Errorf("UnitFileName(launchd) = %q, want %q", got, LaunchdLabel+".plist")
	}
	if got := mustUnitFileName(t, PlatformSystemd); got != SystemdUnitName {
		t.Errorf("UnitFileName(systemd) = %q, want %q", got, SystemdUnitName)
	}
	if _, err := UnitFileName(Platform("upstart")); err == nil {
		t.Error("UnitFileName(upstart) error = nil, want a refusal for an unknown platform")
	}
	if _, err := Render(Platform("upstart"), testSpec(t)); err == nil {
		t.Error("Render(upstart) error = nil, want a refusal for an unknown platform")
	}
}

// --- helpers -------------------------------------------------------------------------

func mustUnitFileName(t *testing.T, p Platform) string {
	t.Helper()
	n, err := UnitFileName(p)
	if err != nil {
		t.Fatalf("UnitFileName(%s) error = %v", p, err)
	}
	return n
}

// encodings renders secret material the three ways a leak would plausibly appear in a
// generated config file, so the PB-LIFE-4 scan is not defeated by a base64 wrapper.
func encodings(b []byte) map[string]string {
	return map[string]string{
		"raw":    string(b),
		"hex":    hex.EncodeToString(b),
		"base64": base64.StdEncoding.EncodeToString(b),
	}
}
