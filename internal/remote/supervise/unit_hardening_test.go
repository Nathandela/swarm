package supervise

// FAILING-FIRST tests for ADR-007 B41 (restated by B62(3)): the generated systemd user
// unit's [Service] section carries no process-confinement directive at all, so D4/R1's
// "sidecar isolation limits blast radius on daemon/PTY state" has nothing behind it on
// Linux.
//
// WHAT THE GATEWAY ACTUALLY DOES, which is what any directive here has to be justified
// against: it dials the relay over TLS (outbound TCP + name resolution), it CONNECTS to
// the daemon's remote.sock (it never creates it), it reads and writes SWARM_DAEMON_STATE,
// it optionally appends to LogPath, and systemd restarts it on failure.
//
// WHY THE OBVIOUS SIX ARE NOT THE ANSWER. This is a systemd USER unit (systemctl --user).
// systemd.exec(5) is explicit that the file-system sandbox does not exist there:
//
//	"Also note that some sandboxing functionality is generally not available in user
//	 services (i.e. services run by the per-user service manager). Specifically, the
//	 various settings requiring file system namespacing support (such as ProtectSystem=)
//	 are not available, as the underlying kernel functionality is only accessible to
//	 privileged processes."
//
// So ProtectSystem=, ProtectHome=, PrivateTmp= -- and ReadWritePaths=, the only thing that
// could have punched the state dir back out of them -- are not a hardening/bricking
// trade-off here. They do not work at all, and the way they do not work is that the
// service fails to start (EXIT_NAMESPACE, 226), which Restart=on-failure then turns into
// the crash loop up to StateFailed that PB-LIFE-3 exists to forbid. A gateway that will
// not start is a worse outcome than an unconfined one.
//
// SystemCallFilter= is refused for a different reason, pinned separately below.
//
// WHAT IS LEFT works without a mount namespace -- one prctl and three seccomp filters --
// and every one of them fails SOFT (an errno the caller sees) rather than by killing the
// process. That is the whole of what a per-user manager can enforce, so that is what these
// tests demand.

import (
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// hardeningSpec is deliberately NOT testSpec: its StateDir, RemoteSocket and LogPath are
// three unrelated absolute paths, none under another. A unit that only works when they all
// sit inside one tree -- the shape a hardcoded exception list silently assumes -- cannot
// pass the derived-path test below with this Spec.
func hardeningSpec() Spec {
	return Spec{
		Exec:         "/opt/swarm/bin/swarm-remote",
		Owner:        "swarmowner",
		StateDir:     "/srv/swarm/state",
		RemoteSocket: "/run/user/1000/swarm/remote.sock",
		LogPath:      "/var/log/swarm/gateway.log",
		Backoff:      30 * time.Second,
	}
}

// TestRenderSystemd_ConfinesTheGatewayProcess is ADR-007 B41's positive half: the
// directives a per-user service manager CAN enforce are present, with the exact values
// their justification depends on.
func TestRenderSystemd_ConfinesTheGatewayProcess(t *testing.T) {
	out, err := Render(PlatformSystemd, testSpec(t))
	if err != nil {
		t.Fatalf("Render(systemd) error = %v, want nil", err)
	}
	u := parseUnit(t, out)

	for _, tc := range []struct{ key, want, why string }{
		{
			"NoNewPrivileges", "yes",
			"an exploited gateway must not be able to regain privilege through a setuid " +
				"binary; it is also the precondition the kernel requires before an " +
				"unprivileged process may load any seccomp filter at all, so the three below " +
				"depend on it",
		},
		{
			"RestrictNamespaces", "yes",
			"the gateway creates no namespace, and unprivileged user-namespace creation is " +
				"the standard escalation primitive available to a compromised unprivileged " +
				"process; unshare/clone/setns then fail with EPERM, and Go's clone() for " +
				"thread creation carries no CLONE_NEW* flag so it is unaffected",
		},
		{
			"SystemCallArchitectures", "native",
			"systemd.exec(5) recommends this precisely so a secondary ABI cannot be used to " +
				"circumvent RestrictAddressFamilies=, which is filtered per-ABI",
		},
		{
			"UMask", "0077",
			"PB-LIFE-4: whatever the gateway creates under the state dir stays owner-only; " +
				"a user unit otherwise inherits the manager's 0022",
		},
	} {
		if got := u.one(t, "Service", tc.key); got != tc.want {
			t.Errorf("[Service] %s = %q, want %q -- %s", tc.key, got, tc.want, tc.why)
		}
	}

	// RestrictAddressFamilies is asserted as an EXACT set, not a containment check: the
	// failure mode this guards is a later "fix" that widens the list back toward useless.
	//   AF_UNIX   the daemon's remote.sock
	//   AF_INET   relay TLS
	//   AF_INET6  relay TLS
	//   AF_NETLINK glibc's getaddrinfo opens one in check_pf(). Release builds are
	//             CGO_ENABLED=0 and use Go's own resolver, which does not -- but a
	//             source-built gateway (resolveGatewayBinary supports one) falls back to
	//             the cgo resolver on any host whose nsswitch.conf needs it, and DNS that
	//             fails only on some hosts is the exact production outage this unit must
	//             not introduce.
	// Everything else -- AF_PACKET above all -- is denied, and a denied socket() returns
	// EAFNOSUPPORT rather than killing the process.
	wantFamilies := []string{"AF_INET", "AF_INET6", "AF_NETLINK", "AF_UNIX"}
	raf := u.one(t, "Service", "RestrictAddressFamilies")
	if strings.HasPrefix(raf, "~") {
		t.Errorf("[Service] RestrictAddressFamilies = %q is a DENY list; it must be an allow list, "+
			"or every family nobody thought of stays reachable", raf)
	}
	if strings.TrimSpace(raf) == "" {
		t.Errorf("[Service] RestrictAddressFamilies is empty; systemd reads an empty value as " +
			"UNDOING all address-family restrictions, which is the one setting that looks like " +
			"hardening and permits everything")
	}
	got := strings.Fields(raf)
	slices.Sort(got)
	if !slices.Equal(got, wantFamilies) {
		t.Errorf("[Service] RestrictAddressFamilies = %v, want exactly %v", got, wantFamilies)
	}
}

// namespacingDirectives are the [Service] settings that require file-system (or other)
// namespacing. systemd.exec(5) states these are not available to a per-user service
// manager; setting one fails the unit at startup, which Restart=on-failure escalates into
// PB-LIFE-3's crash loop. They are omitted deliberately, and this list is what keeps a
// later reader from "completing" the hardening block with one.
var namespacingDirectives = []string{
	"ProtectSystem", "ProtectHome", "PrivateTmp", "PrivateDevices", "PrivateUsers",
	"PrivateNetwork", "PrivateIPC", "ProtectProc", "ProcSubset", "ProtectHostname",
	"ProtectKernelTunables", "ProtectKernelModules", "ProtectControlGroups",
	"ReadWritePaths", "ReadOnlyPaths", "InaccessiblePaths", "ExecPaths", "NoExecPaths",
	"TemporaryFileSystem", "BindPaths", "BindReadOnlyPaths", "RootDirectory", "RootImage",
	"MountAPIVFS",
}

// TestRenderSystemd_OmitsDirectivesAUserManagerCannotApply is the anti-bricking half. It
// fails the moment someone adds a namespacing directive to a unit that cannot support one.
func TestRenderSystemd_OmitsDirectivesAUserManagerCannotApply(t *testing.T) {
	out, err := Render(PlatformSystemd, hardeningSpec())
	if err != nil {
		t.Fatalf("Render(systemd) error = %v, want nil", err)
	}
	u := parseUnit(t, out)

	for _, k := range namespacingDirectives {
		if vs := u["Service"][k]; len(vs) != 0 {
			t.Errorf("[Service] %s = %v; file-system namespacing is NOT AVAILABLE in a systemd "+
				"user unit (systemd.exec(5)), so this does not confine the gateway -- it stops it "+
				"from starting, and Restart=on-failure turns that into PB-LIFE-3's crash loop", k, vs)
		}
	}
}

// TestRenderSystemd_OmitsSystemCallFilter pins a judgement, not a limitation:
// SystemCallFilter= WOULD work in a user unit, and it is still refused.
//
// Its default action on a denied call is to terminate the process with SIGSYS
// (systemd.exec(5)); SystemCallErrorNumber= replaces that with an errno, which for a
// runtime-internal call the Go runtime does not expect is a throw rather than a recovery.
// Either way the gateway dies on a call it never made before, the trigger is a Go
// toolchain upgrade rather than any change to this repo, and the death is a FAILURE exit
// -- so Restart=on-failure retries it into StartLimitBurst and StateFailed. That is a
// silent, permanent remote-control outage bought against an already-unprivileged process
// that runs with the owner's own authority and has no User= to escalate away from.
func TestRenderSystemd_OmitsSystemCallFilter(t *testing.T) {
	out, err := Render(PlatformSystemd, hardeningSpec())
	if err != nil {
		t.Fatalf("Render(systemd) error = %v, want nil", err)
	}
	u := parseUnit(t, out)

	for _, k := range []string{"SystemCallFilter", "SystemCallErrorNumber"} {
		if vs := u["Service"][k]; len(vs) != 0 {
			t.Errorf("[Service] %s = %v; a syscall filter kills the gateway on a call the Go "+
				"runtime makes after some future upgrade, and the supervisor escalates that into "+
				"a permanent outage (PB-LIFE-3). If this is being reconsidered, do it in an ADR, "+
				"not by editing the template", k, vs)
		}
	}
}

// absPath matches an absolute path anywhere inside a directive value, including one that
// follows a prefix systemd understands -- "append:/var/log/x", "SWARM_DAEMON_STATE=/srv/x".
var absPath = regexp.MustCompile(`/\S+`)

// TestRenderSystemd_EveryPathIsDerivedFromTheSpec is the assertion that catches the
// bricking case. Rendered with a Spec whose state dir, socket and log file are three
// unrelated paths, the unit may name those paths and the exec path -- and nothing else.
//
// A hardcoded "/home/...", a "%h" that disagrees with a non-default StateDir, or a "/tmp"
// assumed to hold the socket all fail here, at generation time, instead of on the machine
// of the one operator who moved SWARM_DAEMON_STATE.
func TestRenderSystemd_EveryPathIsDerivedFromTheSpec(t *testing.T) {
	spec := hardeningSpec()
	out, err := Render(PlatformSystemd, spec)
	if err != nil {
		t.Fatalf("Render(systemd) error = %v, want nil", err)
	}
	u := parseUnit(t, out)

	derived := []string{spec.Exec, spec.StateDir, spec.RemoteSocket, spec.LogPath}
	for section, keys := range u {
		for key, values := range keys {
			for _, v := range values {
				if strings.Contains(v, "%") {
					t.Errorf("[%s] %s = %q carries a systemd specifier; a specifier resolves against "+
						"the manager's idea of the user, NOT against Spec, so it disagrees with any "+
						"non-default StateDir=%s", section, key, v, spec.StateDir)
				}
				for _, p := range absPath.FindAllString(v, -1) {
					if !slices.Contains(derived, p) {
						t.Errorf("[%s] %s names path %q, which is not one of the Spec's paths %v; "+
							"a unit that hardcodes a path breaks every install that moved one",
							section, key, p, derived)
					}
				}
			}
		}
	}

	// The other direction: all four must actually reach the unit, so the test above cannot
	// be satisfied by a template that stopped emitting paths altogether.
	body := string(out)
	for _, p := range derived {
		if !strings.Contains(body, p) {
			t.Errorf("systemd unit does not carry Spec path %q:\n%s", p, body)
		}
	}
}
