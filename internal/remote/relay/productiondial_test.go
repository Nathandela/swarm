// Transport-policy invariants shared by the relay-v2 clients: one parser owns the
// provisioned policy, and MachineSecurity admits cleartext only to a loopback IP literal,
// including in a release build. The live dial-path coverage is in relayv2 and the CLI/gateway
// packages; this package owns policy resolution only.
package relay_test

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/relay"
)

// TestPBOPS5_OneParserOwnsRelayJSON is the structural half of "the pin must not be
// applied to two of three dial paths".
//
// Three copies of this file's shape used to exist -- cmd/swarm's readRelayURL,
// cmd/swarm-remote's loadRelayURL and internal/skeleton's loadRelayURL -- each with its
// own anonymous struct and its own copy of the JSON key, and two of them carried comments
// saying the writer and the reader "must agree on this filename + shape". Adding a field
// to two of three produces a machine that reads as pinned and is not, which is worse than
// no pin: nothing at runtime distinguishes it from a pinned one.
//
// The fence looks for the JSON key in a STRING LITERAL, because that is what a fourth
// reader would have to write in order to exist at all -- a struct tag or an explicit
// key. It walks the AST rather than the bytes for the reason ADR-007 B42 records as a
// recurring shape: a text search over source matches the COMMENTS that describe the file
// (both former readers carried one), so it would fail forever against files that parse
// nothing.
func TestPBOPS5_OneParserOwnsRelayJSON(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	var offenders []string

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".claude", ".codex", ".gradle", "build", "dist", "vendor", "testdata", "node_modules", "relaycfg":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			val, uerr := strconv.Unquote(lit.Value)
			if uerr != nil {
				return true
			}
			// The two forms a reader or writer of this file must use: a struct tag, or
			// the bare key. Prose that merely MENTIONS the key -- an error message
			// naming the field it could not find -- is not a parser and does not count.
			if val != "relay_url" && !strings.Contains(val, `json:"relay_url"`) {
				return true
			}
			rel, _ := filepath.Rel(root, path)
			offenders = append(offenders, rel+":"+strconv.Itoa(fset.Position(lit.Pos()).Line))
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walking the tree: %v", err)
	}
	if len(offenders) > 0 {
		t.Fatalf("relay.json is parsed or written outside internal/remote/relaycfg, in %v.\n"+
			"One parser owns the file so a field cannot be added to some readers and not "+
			"others: a machine pinned on two of its three dial paths reads as covered and is "+
			"not (ADR-007 B34).", offenders)
	}
}

// TestPBOPS5_OnlyRelayCfgDecodesThePin is the invariant one parser was for, applied one level
// down -- and it exists because the file-level fence above did NOT catch the thing it was
// written to prevent.
//
// TestPBOPS5_OneParserOwnsRelayJSON stops a second READER of relay.json. It says nothing about
// a second DECODER of a field that reader hands out: internal/skeleton acquired its own
// base64.StdEncoding.DecodeString of Config.SPKIPin, with no 32-byte length check, while the
// file-level fence stayed green. Two decoders that disagree about what "malformed" means is
// how a pin ends up carried and never consulted, which is the whole of ADR-007 B34.
//
// So the pin's base64 form is relaycfg's alone. Callers take the DECODED value through
// Config.Pin or the whole policy through Config.Security, and reaching for the string field is
// the fence's subject.
//
// A composite-literal key is deliberately NOT a violation: `relaycfg.Config{SPKIPin: v}` is how
// `swarm remote init` WRITES the provisioning, and a write cannot disagree about decoding. Only
// a selector expression -- someone reading the base64 back out to do something with it -- is
// matched, which is what an ast.SelectorExpr gives for free.
func TestPBOPS5_OnlyRelayCfgDecodesThePin(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	var readers []string

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".claude", ".codex", ".gradle", "build", "dist", "vendor", "testdata", "node_modules", "relaycfg":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "SPKIPin" {
				return true
			}
			rel, _ := filepath.Rel(root, path)
			readers = append(readers, filepath.ToSlash(rel)+":"+
				strconv.Itoa(fset.Position(sel.Pos()).Line))
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walking the tree: %v", err)
	}
	if len(readers) > 0 {
		t.Fatalf("%d production site(s) read the pin's BASE64 form out of relaycfg.Config: %v.\n"+
			"Take the decoded bytes from Config.Pin, or the whole policy from Config.Security. A "+
			"second decoder is a second opinion about what a malformed pin is, and the one that "+
			"grew here had no length check at all (ADR-007 B34).", len(readers), readers)
	}
}

// TestPBNET2_MachineSecurityRefusesCleartextToAnythingButLoopback asserts the machine
// policy is narrow. The three targets are a name, a private address and a documentation
// address: none is a loopback IP literal, so none may be dialed in cleartext however the
// operator provisioned the relay URL.
func TestPBNET2_MachineSecurityRefusesCleartextToAnythingButLoopback(t *testing.T) {
	for _, target := range []string{
		"ws://relay.example.com:8080/",
		"ws://10.0.0.7:8080/",
		"ws://198.51.100.4:8080/",
		"ws://localhost:8080/", // a NAME, never resolved: the carve-out is IP literals only
	} {
		_, err := relay.MachineSecurity().Resolve(target)
		if err == nil {
			t.Fatalf("%s: MachineSecurity admitted a non-loopback cleartext relay", target)
		}
		if !errors.Is(err, relay.ErrCleartextRefused) {
			t.Fatalf("%s: got %v, want ErrCleartextRefused", target, err)
		}
	}
}

// TestPBNET2_MachineSecurityAdmitsALoopbackRelay asserts the carve-out actually carves:
// relay-v2's dialer may proceed to a ws://127.0.0.1 relay under this policy.
func TestPBNET2_MachineSecurityAdmitsALoopbackRelay(t *testing.T) {
	if cfg, err := relay.MachineSecurity().Resolve("ws://127.0.0.1:8790/v2/ws"); err != nil || cfg != nil {
		t.Fatalf("MachineSecurity refused the loopback relay: %v", err)
	}
}

// TestPBNET2_DefaultPolicyRefusesLoopbackCleartext keeps the development carve-out explicit:
// only MachineSecurity (or the test-only opt-in) admits it; Security's zero value does not.
func TestPBNET2_DefaultPolicyRefusesLoopbackCleartext(t *testing.T) {
	if _, err := (relay.Security{}).Resolve("ws://127.0.0.1:8790/v2/ws"); !errors.Is(err, relay.ErrCleartextRefused) {
		t.Fatalf("default policy admitted cleartext: %v", err)
	}
}

// machineReleaseProgram is a NON-test main package that exercises the machine policy the
// gateway sidecar and the CLI dial under. Resolve makes the policy decision without a
// network attempt.
const machineReleaseProgram = `package main

import (
	"errors"
	"fmt"

	"github.com/Nathandela/swarm/internal/remote/relay"
)

func main() {
	sec := relay.MachineSecurity()

	if _, err := sec.Resolve("ws://198.51.100.4:1/"); !errors.Is(err, relay.ErrCleartextRefused) {
		fmt.Printf("ROUTABLE-ADMITTED err=%v", err)
		return
	}
	if _, err := sec.Resolve("ws://127.0.0.1:1/"); errors.Is(err, relay.ErrCleartextRefused) {
		fmt.Print("LOOPBACK-REFUSED")
		return
	}
	fmt.Print("OK")
}
`

// TestPBNET2_MachinePolicyIsLoopbackOnlyInAReleaseBuild covers MachineSecurity's deliberate
// exception: the gateway sidecar is a release binary and local Workerd is on 127.0.0.1, so
// the carve-out has to be live in a normally-built binary.
//
// What must therefore be proved instead is that it is live for LOOPBACK IP LITERALS AND
// NOTHING ELSE. Security.AllowLoopbackCleartext keeps its stronger, test-binary-only
// property untouched -- the field this policy sets is unexported, so no code outside the
// relay package can turn the exception on for a URL of its choosing.
func TestPBNET2_MachinePolicyIsLoopbackOnlyInAReleaseBuild(t *testing.T) {
	got := runReleaseProbe(t, buildReleaseProbe(t, machineReleaseProgram))
	switch {
	case got == "OK":
	case strings.HasPrefix(got, "ROUTABLE-ADMITTED"):
		t.Fatalf("a release build dialed a ROUTABLE ws:// relay under MachineSecurity: %s", got)
	default:
		t.Fatalf("machine policy in a release build: %s", got)
	}
}
