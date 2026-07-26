// ADR-007 B37 (FAILING FIRST): the transport-security policy must be applied on the
// dial path PRODUCTION TAKES, not only on the helper a test reaches for.
//
// B34 recorded that relay.Security was applied only by relay.DialSecure and that no
// non-test file in the repository constructed one; tls_test.go in this directory is
// green and guards that unreached helper. B37 then showed what the gap costs when it
// composes with B27's first-use authority rule: a passive on-path observer of a ws://
// connection reads the victim's relay-auth public key out of auth_init, registers a
// throwaway identity, and device_revokes an identity that has never paired. Refusing
// cleartext is the half of that chain which needs no pin channel, so it is the half
// with no excuse for being deferred.
//
// The fences here are therefore about CALL SITES and about the machine-side policy that
// makes local development possible without reopening the hole:
//
//   - no non-test file may reach relay.Dial/relay.DialRaw, the two entry points that
//     apply no policy at all;
//   - relay.MachineSecurity admits cleartext to a LOOPBACK IP LITERAL and to nothing
//     else, in a release build as well as a test binary, because a connection that never
//     leaves the host has no on-path position for an observer to occupy.
package transport_test

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/relay"
)

// relayImportPath is the package whose unpoliced dial entry points production must not
// reach.
const relayImportPath = "github.com/Nathandela/swarm/internal/remote/relay"

// unpolicedDials are the relay entry points that apply NO transport-security policy.
// Both remain exported: the relay's own tests, and the test rigs that stand up an
// in-process relay, dial without a policy on purpose.
var unpolicedDials = map[string]string{
	"Dial":    "relay.Dial applies no policy: the URL is dialed as given, so a ws:// relay runs the auth_init handshake -- which carries the FULL relay-auth public key -- in cleartext (ADR-007 B37 steps 1-3). Use relay.DialSecure with relay.MachineSecurity() on the machine side.",
	"DialRaw": "relay.DialRaw applies no policy. Use relay.DialRawSecure.",
}

// TestPBNET2_NoProductionCodeDialsTheRelayWithoutATransportPolicy is B34's defect class
// pinned at its source. It is an AST walk rather than a text search because
// mobile/pairing.go carries `relay.DialRaw(ctx, payload.RelayURL)` inside a comment
// describing the defect it already fixed, and a grep-based fence would report that
// comment forever while missing a real call written across two lines.
func TestPBNET2_NoProductionCodeDialsTheRelayWithoutATransportPolicy(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	var offenders []string

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "testdata", "node_modules":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil // not our business: the build gate reports unparseable Go
		}
		local, ok := relayLocalName(f)
		if !ok {
			return nil
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok || ident.Name != local {
				return true
			}
			why, forbidden := unpolicedDials[sel.Sel.Name]
			if !forbidden {
				return true
			}
			rel, _ := filepath.Rel(root, path)
			offenders = append(offenders, rel+":"+strconv.Itoa(fset.Position(call.Pos()).Line)+
				" calls "+local+"."+sel.Sel.Name+"\n    "+why)
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walking the tree: %v", err)
	}
	if len(offenders) > 0 {
		t.Fatalf("%d production dial site(s) apply no transport-security policy (ADR-007 B34/B37):\n  %s",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}

// relayLocalName returns the name the relay package is imported under in f, and whether
// it is imported at all. The alias is resolved rather than assumed, so a file importing
// it as `rly` is still checked.
func relayLocalName(f *ast.File) (string, bool) {
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil || p != relayImportPath {
			continue
		}
		if imp.Name != nil {
			if imp.Name.Name == "_" || imp.Name.Name == "." {
				return "", false
			}
			return imp.Name.Name, true
		}
		return "relay", true
	}
	return "", false
}

// TestPBNET2_MachineSecurityRefusesCleartextToAnythingButLoopback asserts the machine
// policy is narrow. The three targets are a name, a private address and a documentation
// address: none is a loopback IP literal, so none may be dialed in cleartext however the
// operator provisioned the relay URL.
//
// Every refusal is decided from the URL alone, so it costs no connection attempt -- the
// assertion below that each returns immediately is what pins "before any key material is
// sent": auth_init cannot precede a socket.
func TestPBNET2_MachineSecurityRefusesCleartextToAnythingButLoopback(t *testing.T) {
	pub, priv := newRelayAuthKey(t)
	for _, target := range []string{
		"ws://relay.example.com:8080/",
		"ws://10.0.0.7:8080/",
		"ws://198.51.100.4:8080/",
		"ws://localhost:8080/", // a NAME, never resolved: the carve-out is IP literals only
	} {
		start := time.Now()
		c, err := relay.DialSecure(testCtx(t), target, authFor(pub, priv), relay.MachineSecurity())
		if err == nil {
			_ = c.Close()
			t.Fatalf("%s: MachineSecurity admitted a non-loopback cleartext relay", target)
		}
		if !errors.Is(err, relay.ErrCleartextRefused) {
			t.Fatalf("%s: got %v, want ErrCleartextRefused", target, err)
		}
		if took := time.Since(start); took > 2*time.Second {
			t.Fatalf("%s: the refusal took %s, so it was decided after a network attempt "+
				"rather than from the URL", target, took)
		}
	}
}

// TestPBNET2_MachineSecurityAdmitsALoopbackRelay asserts the carve-out actually carves:
// the ws://127.0.0.1 relay a developer runs, and the one the S19 exit demonstration
// spawns the real gateway binary against, still completes the relay-auth handshake.
func TestPBNET2_MachineSecurityAdmitsALoopbackRelay(t *testing.T) {
	_, ws := startRelay(t, nil)

	pub, priv := newRelayAuthKey(t)
	c, err := relay.DialSecure(testCtx(t), ws, authFor(pub, priv), relay.MachineSecurity())
	if err != nil {
		t.Fatalf("MachineSecurity refused the loopback relay: %v", err)
	}
	defer func() { _ = c.Close() }()
	if c.RoutingID() != relay.RoutingID(pub) {
		t.Fatalf("routing id mismatch after a loopback cleartext dial")
	}
}

// TestPBNET2_RawDialsCarryTheSamePolicy covers the pairing rendezvous, which is the other
// production dial shape: unauthenticated and unpumped. It discloses no relay-auth key,
// but it is the first packet a handset sends to a URL a scanned QR chose, so the same
// refusal applies.
func TestPBNET2_RawDialsCarryTheSamePolicy(t *testing.T) {
	_, ws := startRelay(t, nil)

	c, err := relay.DialRawSecure(testCtx(t), ws, relay.MachineSecurity())
	if err != nil {
		t.Fatalf("DialRawSecure refused the loopback relay under MachineSecurity: %v", err)
	}
	_ = c.Close()

	if _, err := relay.DialRawSecure(testCtx(t), "ws://relay.example.com:8080/", relay.MachineSecurity()); !errors.Is(err, relay.ErrCleartextRefused) {
		t.Fatalf("DialRawSecure(non-loopback ws://): got %v, want ErrCleartextRefused", err)
	}
	if _, err := relay.DialRawSecure(testCtx(t), ws, relay.Security{}); !errors.Is(err, relay.ErrCleartextRefused) {
		t.Fatalf("DialRawSecure under the default policy admitted cleartext: %v", err)
	}
}

// machineReleaseProgram is a NON-test main package that exercises the machine policy the
// gateway sidecar and the CLI dial under. Both dial targets are dead ports, so each
// answer can only be the policy decision -- which also pins that the decision precedes
// any network attempt.
const machineReleaseProgram = `package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Nathandela/swarm/internal/remote/relay"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sec := relay.MachineSecurity()

	if _, err := relay.DialSecure(ctx, "ws://198.51.100.4:1/", relay.ClientAuth{}, sec); !errors.Is(err, relay.ErrCleartextRefused) {
		fmt.Printf("ROUTABLE-ADMITTED err=%v", err)
		return
	}
	if _, err := relay.DialSecure(ctx, "ws://127.0.0.1:1/", relay.ClientAuth{}, sec); errors.Is(err, relay.ErrCleartextRefused) {
		fmt.Print("LOOPBACK-REFUSED")
		return
	}
	fmt.Print("OK")
}
`

// TestPBNET2_MachinePolicyIsLoopbackOnlyInAReleaseBuild is the counterpart to
// tls_test.go's TestCleartext_CarveOutCannotBeEnabledInAReleaseBuild, and it is here
// because MachineSecurity deliberately does NOT share that property: the gateway sidecar
// is a release binary and a developer's relay is on 127.0.0.1, so the carve-out has to be
// live in a normally-built binary.
//
// What must therefore be proved instead is that it is live for LOOPBACK IP LITERALS AND
// NOTHING ELSE. Security.AllowLoopbackCleartext keeps its stronger, test-binary-only
// property untouched -- the field this policy sets is unexported, so no code outside the
// relay package can turn the exception on for a URL of its choosing.
func TestPBNET2_MachinePolicyIsLoopbackOnlyInAReleaseBuild(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go toolchain unavailable: %v", err)
	}

	root := repoRoot(t)
	pkgDir := filepath.Join(root, "internal", "remote", "transport", "machinereleasecheck")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("mkdir machinereleasecheck: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(pkgDir) })
	if err := os.WriteFile(filepath.Join(pkgDir, "main.go"), []byte(machineReleaseProgram), 0o644); err != nil {
		t.Fatalf("write machinereleasecheck main: %v", err)
	}

	bin := filepath.Join(t.TempDir(), "machinereleasecheck")
	build := exec.Command("go", "build", "-o", bin, "./internal/remote/transport/machinereleasecheck")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the release binary failed: %v\n%s", err, out)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin).CombinedOutput()
	if err != nil {
		t.Fatalf("running the release binary failed: %v\n%s", err, out)
	}
	got := strings.TrimSpace(string(out))
	switch {
	case got == "OK":
	case strings.HasPrefix(got, "ROUTABLE-ADMITTED"):
		t.Fatalf("a release build dialed a ROUTABLE ws:// relay under MachineSecurity: %s", got)
	default:
		t.Fatalf("machine policy in a release build: %s", got)
	}
}
