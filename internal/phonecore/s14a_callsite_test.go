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
	const floor = 3
	if visited < floor {
		t.Fatalf("PB-KEY-9: the call-site fence found only %d assignment call sites of %v in non-test "+
			"code (floor %d). It is not reading the tree, so it guards nothing",
			visited, s14aSortedOps(), floor)
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
