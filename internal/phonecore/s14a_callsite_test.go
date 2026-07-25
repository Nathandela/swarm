// FAILING-FIRST (TDD RED, GG-5) tests for slice S14a / PB-KEY-9(a) at the CALL SITES.
//
// Making crypto.KeyStore failable is worth nothing if the callers throw the error away. The
// three operations B14 makes failable are reached from phonecore's own signing helpers, from
// the gomobile facade and from the phone simulator; every one of those must surface the
// refusal rather than ship an unsigned or half-signed artifact. A call site that writes
// `sig, _ := ks.SignCommand(msg)` re-creates the exact defect one layer up -- the interface
// reports the failure and nothing acts on it -- and it type-checks, so only a test catches it.
//
// These do not compile until the signatures change. That is the RED.

package phonecore

import (
	"crypto/ed25519"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// s14aRefusingKeyStore refuses the operation under test and delegates everything else to a
// real software store, so a propagation failure is the only difference from the working path.
type s14aRefusingKeyStore struct {
	crypto.KeyStore
	signCommandErr   error
	signRelayAuthErr error
	noiseStaticErr   error
	openSealedBoxErr error
}

func (k *s14aRefusingKeyStore) SignCommand(msg []byte) ([]byte, error) {
	if k.signCommandErr != nil {
		return nil, k.signCommandErr
	}
	return k.KeyStore.SignCommand(msg)
}

func (k *s14aRefusingKeyStore) SignRelayAuth(challenge []byte) ([]byte, error) {
	if k.signRelayAuthErr != nil {
		return nil, k.signRelayAuthErr
	}
	return k.KeyStore.SignRelayAuth(challenge)
}

func (k *s14aRefusingKeyStore) NoiseStatic() (*crypto.NoiseStatic, error) {
	if k.noiseStaticErr != nil {
		return nil, k.noiseStaticErr
	}
	return k.KeyStore.NoiseStatic()
}

func (k *s14aRefusingKeyStore) OpenSealedBox(sealed []byte) ([]byte, error) {
	if k.openSealedBoxErr != nil {
		return nil, k.openSealedBoxErr
	}
	return k.KeyStore.OpenSealedBox(sealed)
}

func s14aSoftwareKeyStore(t *testing.T) crypto.KeyStore {
	t.Helper()
	ks, err := crypto.NewFileKeyStore(t.TempDir())
	if err != nil {
		t.Fatalf("seeding a software key store: %v", err)
	}
	return ks
}

func s14aCommandInput() CommandInput {
	return CommandInput{
		Action:      "kill",
		Machine:     "m",
		Session:     "m/s",
		OperationID: "op-s14a",
		ExpiresAt:   time.Unix(1700000000, 0),
	}
}

// TestS14A_SignCommandSurfacesTheCustodyRefusal. phonecore.SignCommand is the authoring path
// for every mutating op the phone sends. If it drops the custody error, it returns a
// DeviceCommandAuth whose Sig is the base64 of nothing -- structurally well-formed, refused by
// the daemon, and indistinguishable at the call site from a network problem.
func TestS14A_SignCommandSurfacesTheCustodyRefusal(t *testing.T) {
	inner := s14aSoftwareKeyStore(t)

	for _, tc := range []struct {
		name string
		err  error
	}{
		{"auth-required", crypto.ErrKeyAuthRequired},
		{"key-invalidated", crypto.ErrKeyInvalidated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ks := &s14aRefusingKeyStore{KeyStore: inner, signCommandErr: tc.err}

			cmd, err := SignCommand(ks, s14aCommandInput())
			if !errors.Is(err, tc.err) {
				t.Errorf("PB-KEY-9: SignCommand returned err %v, want %v. A dropped custody error is "+
					"the defect B14 exists to remove, moved one layer up", err, tc.err)
			}
			if cmd.Sig != "" {
				t.Errorf("PB-KEY-9: SignCommand returned Sig %q after custody refused to sign; a command "+
					"the device never authorised must not be constructible", cmd.Sig)
			}
		})
	}
}

// TestS14A_SignTakeControlSurfacesTheCustodyRefusal. PB-KEY-6 says EVERY signing path, and
// take_control is a distinct one: it binds a one-shot gate token into the signature, so a
// caller that treated a refusal as a transient failure would burn the token.
func TestS14A_SignTakeControlSurfacesTheCustodyRefusal(t *testing.T) {
	ks := &s14aRefusingKeyStore{
		KeyStore:       s14aSoftwareKeyStore(t),
		signCommandErr: crypto.ErrKeyAuthRequired,
	}

	cmd, err := SignTakeControl(ks, TakeControlInput{
		Machine: "m", Session: "m/s", OperationID: "op-s14a-tc",
		ExpiresAt: time.Unix(1700000000, 0), GateToken: "gate-token",
	})
	if !errors.Is(err, crypto.ErrKeyAuthRequired) {
		t.Errorf("PB-KEY-9: SignTakeControl returned err %v, want ErrKeyAuthRequired", err)
	}
	if cmd.Sig != "" {
		t.Errorf("PB-KEY-9: SignTakeControl returned Sig %q after custody refused to sign", cmd.Sig)
	}
}

// TestS14A_AcceptGrantSurfacesTheCustodyRefusalDistinctly. The grant path already had a
// failable operation (OpenSealedBox), which is exactly why it is worth pinning: a custody
// refusal must NOT be collapsed into crypto.ErrSealedOpen. The two demand opposite responses
// -- ErrSealedOpen means the grant is not ours and must be discarded, ErrKeyAuthRequired means
// the grant is fine and the user has not authenticated -- so a phone that conflates them
// discards a valid epoch grant and loses the epoch.
func TestS14A_AcceptGrantSurfacesTheCustodyRefusalDistinctly(t *testing.T) {
	ks := &s14aRefusingKeyStore{
		KeyStore:         s14aSoftwareKeyStore(t),
		openSealedBoxErr: crypto.ErrKeyAuthRequired,
	}

	machinePub, machinePriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("machine grant signer: %v", err)
	}
	keys, err := crypto.NewEpochKeys()
	if err != nil {
		t.Fatalf("epoch keys: %v", err)
	}
	grant, err := crypto.SealEpochGrant(machinePriv, ks.RecipientPublic(), 1, 1, keys)
	if err != nil {
		t.Fatalf("sealing a grant: %v", err)
	}

	if _, _, _, err = AcceptGrant(ks, machinePub, grant); !errors.Is(err, crypto.ErrKeyAuthRequired) {
		t.Errorf("PB-KEY-9: AcceptGrant returned err %v, want ErrKeyAuthRequired. A locked content tier "+
			"is not a malformed grant, and discarding the grant loses the epoch", err)
	}
	if errors.Is(err, crypto.ErrSealedOpen) {
		t.Error("PB-KEY-9: the custody refusal was collapsed into ErrSealedOpen, which means 'this grant " +
			"is not addressed to us' -- the phone would discard a grant it merely could not open yet")
	}
}

// ---------------------------------------------------------------------------
// The call-site inventory, as a fence.
// ---------------------------------------------------------------------------

// s14aFailableOps are the three operations ADR-007 B14 makes failable. Identity.NoiseStatic
// (machine identity, internal/remote/machineid and crypto.Identity) is deliberately NOT in
// scope -- it is not a device KeyStore and stays errorless -- and the fence below only ever
// flags a DISCARDED result, so an errorless Identity call used as a value is never matched.
var s14aFailableOps = map[string]bool{
	"SignCommand":   true,
	"SignRelayAuth": true,
	"NoiseStatic":   true,
}

// TestS14A_NoCallSiteDiscardsACustodyError walks every non-test Go file in the repository and
// fails on a call to one of the three failable operations whose result is thrown away -- an
// `_` in the assignment, or a bare expression statement. This is the machine-checkable form of
// the call-site inventory: it is what stops the failability from being reintroduced as a
// defect one layer up, and it keeps working for call sites added after this slice.
//
// It asserts a FLOOR on the number of call sites it saw. A fence that finds nothing exits 0
// while guarding nothing, which is a defect class this project has already shipped.
func TestS14A_NoCallSiteDiscardsACustodyError(t *testing.T) {
	root := s14aRepoRoot(t)

	var visited int
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "testdata", "build", "node_modules":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil // not our business to police unparseable files
		}
		rel, _ := filepath.Rel(root, path)

		ast.Inspect(f, func(n ast.Node) bool {
			switch stmt := n.(type) {
			case *ast.AssignStmt:
				for _, rhs := range stmt.Rhs {
					if !s14aCallsFailableOp(rhs) {
						continue
					}
					visited++
					for i, lhs := range stmt.Lhs {
						id, ok := lhs.(*ast.Ident)
						if ok && id.Name == "_" && i == len(stmt.Lhs)-1 && len(stmt.Lhs) > 1 {
							t.Errorf("PB-KEY-9: %s:%d discards the custody error with `_`. The whole point "+
								"of B14 is that this operation can be refused; dropping the refusal here "+
								"re-creates the errorless interface one layer up",
								rel, fset.Position(id.Pos()).Line)
						}
					}
				}
			case *ast.ExprStmt:
				if s14aCallsFailableOp(stmt.X) {
					visited++
					t.Errorf("PB-KEY-9: %s:%d calls a failable custody operation as a bare statement and "+
						"drops both results", rel, fset.Position(stmt.Pos()).Line)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walking the repository: %v", err)
	}

	// Every assignment call site in non-test code, counted. Below this floor the fence is not
	// reading the tree it claims to guard.
	//
	// The floor is the TRUE count, not a round number safely under it. Set below the truth it
	// proves only that the walk parses Go -- it was 3 while the tree held 4, so it was already
	// satisfied before the slice that introduced these call sites and measured none of them.
	// The five are: internal/phonecore/command.go (ks.SignCommand), mobile/pairing.go
	// (ks.NoiseStatic), mobile/commands.go and internal/phonesim/phonesim.go x2 (the
	// package-local phonecore.SignCommand). Adding a call site is fine; losing one means the
	// fence is looking at less of the tree than it did, and that is worth a failure.
	//
	// KNOWN EVASION, recorded not closed: only *ast.AssignStmt and *ast.ExprStmt are
	// inspected, so `var sig, _ = ks.SignRelayAuth(...)` inside a function is legal Go that
	// discards the error and passes. Widening the op set to the bare name "Sign" would
	// over-match badly (relay.ClientAuth.Sign, machineid.RelayAuthSign, every ed25519 signer),
	// so the cheap fix is worse than the gap.
	const floor = 5
	if visited < floor {
		t.Fatalf("PB-KEY-9: the call-site fence found only %d assignment call sites of %v in non-test "+
			"code (floor %d). It is not reading the tree, so it guards nothing",
			visited, s14aSortedOps(), floor)
	}
}

// ---------------------------------------------------------------------------
// The named cleartext fallback, bounded.
// ---------------------------------------------------------------------------

// s14aCleartextCallSites are the ONLY files permitted to reach for unsealed key custody.
// mobile/app.go is the shipped defect ADR-007 B18(c) accepted as interim -- gomobile cannot
// set a Go struct field and the facade is golden-pinned, so NewApp has no way to supply a
// real sealer -- and mobile/conformance/harness_test.go must match it byte for byte or it
// seeds a blob NewApp cannot open.
var s14aCleartextCallSites = []string{
	"mobile/app.go",
	"mobile/conformance/harness_test.go",
}

// TestS14A_TheCleartextSealerIsBoundedToItsTwoKnownCallSites converts a grep convention into
// a fence. docs/verification/remote-phaseB-progress.md records that
// InsecureCleartextSealer's own name is the live defect marker for PB-KEY-9's undelivered
// half, and that exactly two files carry it -- but a comment cannot stop a third appearing,
// and nothing announces when the two go away.
//
// It is deliberately failable in BOTH directions:
//
//   - MORE than these two: something else now writes key material with no KEK over it, which
//     is the property B18(c) exists to make impossible to reach by accident. The whole point
//     of the named constructor is that unsealed custody costs a deliberate call, so the
//     inventory of who paid that cost has to stay short and known.
//   - FEWER: S14 has landed the facade verb and deleted the call sites, which means PB-KEY-9
//     is finally delivered -- and the "NOT delivered" section of
//     docs/verification/remote-phaseB-progress.md, plus this fence, are now stale. Failing
//     here is what forces that reckoning instead of leaving a false record standing.
//
// FILES, not call expressions: mobile/app.go calls it twice (one sealer per tier) and the
// harness twice on one line. A third call inside a file already on this list is not new
// exposure; a third FILE is. Test files are walked too -- one of the two is one.
//
// This cannot be done by restricting visibility instead: mobile/app.go is in another module
// path and needs the exported symbol to build, so counting call sites is the mechanism
// available today.
func TestS14A_TheCleartextSealerIsBoundedToItsTwoKnownCallSites(t *testing.T) {
	root := s14aRepoRoot(t)

	seen := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "build", "node_modules":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		ast.Inspect(f, func(n ast.Node) bool {
			// Any CallExpr anywhere, not just the two statement shapes the fence above
			// inspects: there is no legitimate call to bound this to, so a composite
			// literal or a var initializer must count exactly as much as an assignment.
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fn := call.Fun.(type) {
			case *ast.SelectorExpr:
				if fn.Sel.Name == "InsecureCleartextSealer" {
					seen[filepath.ToSlash(rel)] = true
				}
			case *ast.Ident:
				if fn.Name == "InsecureCleartextSealer" {
					seen[filepath.ToSlash(rel)] = true
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walking the repository: %v", err)
	}

	got := make([]string, 0, len(seen))
	for name := range seen {
		got = append(got, name)
	}
	sort.Strings(got)

	want := append([]string(nil), s14aCleartextCallSites...)
	sort.Strings(want)

	if !reflect.DeepEqual(got, want) {
		t.Errorf("ADR-007 B18(c): the InsecureCleartextSealer call sites are %v, want exactly %v.\n"+
			"MORE means a third place now writes key material with no KEK over it -- unsealed custody is "+
			"supposed to cost a deliberate, inventoried call.\n"+
			"FEWER means S14 landed the facade verb, PB-KEY-9 is delivered, and both this fence and the "+
			"'THE SHIPPED APP STILL WRITES THE CONTENT KEY IN THE CLEAR' section of "+
			"docs/verification/remote-phaseB-progress.md are now false and must be retired.",
			got, want)
	}
	// Named separately because it is the one that matters: the shipped app is the defect this
	// marker tracks, and losing it from the list while the count stays at two would mean the
	// cleartext custody moved somewhere new rather than went away.
	if !seen["mobile/app.go"] {
		t.Errorf("ADR-007 B18(c): mobile/app.go no longer calls InsecureCleartextSealer. If S14 landed the "+
			"facade verb, retire this fence and the PB-KEY-9 'not delivered' record with it; if the call "+
			"merely moved, PB-KEY-9's status note now points at the wrong file. Call sites found: %v", got)
	}
}

// s14aCallsFailableOp reports whether e is a direct call to one of the three failable
// operations, matching both `ks.SignCommand(...)` and the package-local `SignCommand(...)`.
func s14aCallsFailableOp(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		return s14aFailableOps[fn.Sel.Name]
	case *ast.Ident:
		return s14aFailableOps[fn.Name]
	}
	return false
}

func s14aSortedOps() []string {
	out := make([]string, 0, len(s14aFailableOps))
	for name := range s14aFailableOps {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// s14aRepoRoot walks up from the package directory to the module root.
func s14aRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the package directory; the call-site fence cannot locate the repo")
		}
		dir = parent
	}
}
