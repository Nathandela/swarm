package phonecore

// Failing-first test for PB-BIND-0: the bound package's dependency closure is
// constrained by an executable allowlist of exact import paths held in a checked-in
// file (deps_allowlist.txt), and any package outside it fails.
//
// RED today is blocker 4.2: phonecore -> internal/protocol -> internal/daemon drags
// internal/shim, internal/engine, internal/vt, internal/transcript, internal/persist,
// internal/shimwire, github.com/creack/pty and charmbracelet/x/vt into the closure --
// 52 non-stdlib packages, shipping the PTY and the VT emulator to a handset an
// adversary may hold, against ADR-007 Decision 2.

import (
	"bufio"
	"os"
	"os/exec"
	"sort"
	"strings"
	"testing"
)

// guardedPackage is the package whose closure PB-BIND-0 constrains. It moves to the
// non-internal facade once PB-BIND-1 lands; the facade is then the only bound surface.
const guardedPackage = "github.com/Nathandela/swarm/internal/phonecore"

const allowlistFile = "deps_allowlist.txt"

// TestBoundClosureIsCleanOnMobileTargets runs the same guard for the platforms the AAR is
// actually built for. The host closure and the android closure already differ today
// (golang.org/x/sys/cpu is in darwin/arm64 and absent from android/arm64), so a host-only
// check has a real blind spot: a //go:build android file importing a forbidden package
// would leave CI green on macOS and Linux while the daemon shipped to the handset
// (S1 review R5).
func TestBoundClosureIsCleanOnMobileTargets(t *testing.T) {
	for _, goos := range []string{"android", "ios"} {
		t.Run(goos, func(t *testing.T) {
			allow := readAllowlist(t)
			cmd := exec.Command("go", "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", guardedPackage)
			cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH=arm64", "CGO_ENABLED=0")
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Skipf("go list for %s/arm64 unavailable: %v", goos, err)
			}
			var extra []string
			for _, line := range strings.Split(string(out), "\n") {
				line = strings.TrimSpace(line)
				if line == "" || allow[line] {
					continue
				}
				extra = append(extra, line)
			}
			if len(extra) > 0 {
				t.Errorf("%s/arm64 closure of %s has %d package(s) not in deps_allowlist.txt:\n\t%s",
					goos, guardedPackage, len(extra), strings.Join(extra, "\n\t"))
			}
		})
	}
}

func TestBoundClosureMatchesAllowlist(t *testing.T) {
	allowed := readAllowlist(t)
	closure := listNonStdDeps(t, guardedPackage)

	if len(closure) == 0 {
		t.Fatalf("go list -deps %s returned no non-standard packages; the guard would be vacuous", guardedPackage)
	}

	var offenders []string
	for _, pkg := range closure {
		if !allowed[pkg] {
			offenders = append(offenders, pkg)
		}
	}
	sort.Strings(offenders)

	if len(offenders) > 0 {
		t.Errorf("%d of %d non-standard packages in the closure of %s are not in %s:\n\t%s",
			len(offenders), len(closure), guardedPackage, allowlistFile,
			strings.Join(offenders, "\n\t"))
	}
}

// readAllowlist parses the checked-in file: one exact import path per line, blank
// lines and # comments ignored.
func readAllowlist(t *testing.T) map[string]bool {
	t.Helper()
	f, err := os.Open(allowlistFile)
	if err != nil {
		t.Fatalf("open allowlist: %v", err)
	}
	defer f.Close()

	allowed := make(map[string]bool)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		allowed[line] = true
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("read allowlist: %v", err)
	}
	if len(allowed) == 0 {
		t.Fatalf("%s lists no import paths", allowlistFile)
	}
	return allowed
}

// listNonStdDeps returns the non-standard transitive dependency closure of pkg,
// the same computation as remote-phaseB-requirements.md section 4.2.
func listNonStdDeps(t *testing.T, pkg string) []string {
	t.Helper()
	cmd := exec.Command("go", "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", pkg)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps %s: %v\n%s", pkg, err, out)
	}

	seen := make(map[string]bool)
	var deps []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || seen[line] {
			continue
		}
		seen[line] = true
		deps = append(deps, line)
	}
	sort.Strings(deps)
	return deps
}
