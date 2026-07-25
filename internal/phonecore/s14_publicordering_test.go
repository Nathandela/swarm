package phonecore

// Slice S14 -- fencing the F5 residual S14a recorded and could not close.
//
// THE PROPERTY, precisely. sealedKeyStore's three CONTENT-tier public accessors --
// NoiseStaticPublic, RecipientPublic, CommandSigningPublic -- are errorless by design, and
// that is forced rather than chosen: a phone whose content tier is locked must still be able
// to state its own routing id, or it cannot receive the push that asks the user to unlock. So
// they answer from the container's CLEARTEXT half, which PB-SEC-1's adversary (root, or a
// restored image) can write.
//
// checkPublic re-derives each of them from the sealed material and refuses a mismatch, but it
// can only run at the UNSEAL -- the material to check against is exactly what a locked tier
// withholds. With the content tier locked the check does not run at all, so a forged
// recipient_pub or command_pub in device.key is, at that instant, unverified.
//
// WHY THAT WAS NOT EXPLOITABLE, AND WHY THE ARGUMENT WAS NOT SAFE. mobile/pairing.go hoists
// ks.NoiseStatic() and returns on its error BEFORE it builds the payload that reads
// RecipientPublic and CommandSigningPublic; phonecore/command.go calls ks.SignCommand() before
// device.DeviceIDFor(ks.CommandSigningPublic()). Both unseal the content tier first, so a
// locked tier stops them and an unlocked one has already run checkPublic. That is an ORDERING
// DEPENDENCY -- true of the code as written, guaranteed by nothing, invisible to a reviewer
// moving a line, and it was the residual S14a recorded as unfenced.
//
// This is the fence. Every function that reads a content-tier public must either FORCE THE
// UNSEAL FIRST -- call one of the content-tier operations and return on its error -- or be
// named in the inventory below with the reason its read is acceptable unverified. A new reader
// gets neither by accident.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// s14ContentPublics are the three accessors that answer from the unauthenticated cleartext
// half while the content tier is locked. RelayAuthPublic is deliberately absent: it is WAKE
// tier, openSealedDeviceKeys unseals that tier unconditionally at load and fails closed, so a
// forged relay_auth_pub never reaches a caller at all.
var s14ContentPublics = map[string]bool{
	"NoiseStaticPublic":    true,
	"RecipientPublic":      true,
	"CommandSigningPublic": true,
}

// s14ContentUnseals are the operations that force the content tier open -- and therefore run
// checkPublic -- and that report a refusal the caller must handle. Calling one of these and
// returning its error is what makes a later public read verified.
var s14ContentUnseals = map[string]bool{
	"NoiseStatic":   true,
	"OpenSealedBox": true,
	"SignCommand":   true,
}

// s14PublicMechanism are the functions that DERIVE the publics rather than consume them, and
// they are exempt for a structural reason rather than an accepted-risk one.
//
// sealDeviceKeys reads them off a crypto.KeyStore built from material it just generated, and
// contentStore/wakeStore read them off the inner store built from material they just unsealed
// -- both are the authoritative side of checkPublic's comparison, not the container's
// unauthenticated cleartext claim. Requiring an "unseal first" of the unseal itself is
// circular. They are named rather than matched by file, so a genuine consumer added to
// keycustody.go still trips the fence.
var s14PublicMechanism = map[string]bool{
	"sealDeviceKeys": true,
	"contentStore":   true,
	"wakeStore":      true,
}

// s14UnverifiedPublicReaders is the INVENTORY: functions that read a content-tier public with
// no preceding unseal. Each entry is a decision, and the reason is the point of the entry.
//
// It is not empty and must not be pretended to be. S14a's own re-review found that "every
// content operation refuses in that state anyway" was FALSE as stated -- the errorless
// accessors are consumed by callers that are neither pairing nor content operations.
var s14UnverifiedPublicReaders = map[string]string{
	"mobile/app.go:deviceID": "" +
		"Reads CommandSigningPublic to derive this phone's own registry id (R-DEV.1) with no " +
		"content unseal. Its ONLY consumer is App.RevokeThisDevice, which seals nothing: " +
		"device_revoke has no gateway action->op mapping, so the value lands in a DURABLE LOCAL " +
		"REFUSAL RECORD and never on the wire. A forged command_pub therefore mislabels one " +
		"local refusal on a device whose data directory the attacker already writes -- it " +
		"authorises nothing and reaches no other party. Forcing an unseal here would be worse " +
		"than the exposure: it would put a biometric prompt in front of the panic button, on a " +
		"path whose whole purpose is to work when something has gone wrong. " +
		"IF A SECOND CONSUMER APPEARS, especially one that puts the id on the wire or into a " +
		"signed tuple, this entry stops being true and must be re-decided rather than carried.",
}

// TestS14_EveryContentTierPublicReadIsOrderedBehindAnUnsealOrInventoried.
//
// The failure mode it removes is small and quiet: someone adds a helper that reads
// RecipientPublic to display a key fingerprint, or reorders pairing.go so the payload literal
// is built before the NoiseStatic hoist, and the phone enrols an attacker-chosen recipient key
// -- which seals every epoch grant to a key the attacker holds. Nothing else in the tree would
// notice; that is exactly how the F5 defect got in the first time.
func TestS14_EveryContentTierPublicReadIsOrderedBehindAnUnsealOrInventoried(t *testing.T) {
	root := s14aRepoRoot(t)

	var unguarded []string
	inventoryUsed := map[string]bool{}

	for _, pkg := range []string{"mobile", filepath.Join("internal", "phonecore")} {
		dir := filepath.Join(root, pkg)
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if path != dir {
					// One package at a time: mobile/conformance is a test-only package and
					// internal/phonecore has no subpackages.
					return fs.SkipDir
				}
				return nil
			}
			// PRODUCTION code only. A test that reads a public with the tier locked is
			// exercising exactly that state on purpose, which is the opposite of the defect.
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			fset := token.NewFileSet()
			f, perr := parser.ParseFile(fset, path, nil, 0)
			if perr != nil {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)

			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				// The accessors' own declarations, and checkPublic's own call sites inside
				// the unseal, are the mechanism rather than consumers of it.
				if s14ContentPublics[fn.Name.Name] || fn.Name.Name == "checkPublic" ||
					s14PublicMechanism[fn.Name.Name] {
					continue
				}
				readPos, readName := s14FirstCall(fn.Body, s14ContentPublics)
				if readName == "" {
					continue
				}
				unsealPos, _ := s14FirstCall(fn.Body, s14ContentUnseals)
				// STRICTLY BEFORE. An unseal after the read verifies nothing about the value
				// already taken, and "somewhere in the same function" is the weaker claim that
				// makes this fence look like it holds while a reorder walks straight through.
				if unsealPos != token.NoPos && unsealPos < readPos {
					continue
				}
				key := rel + ":" + fn.Name.Name
				if _, inventoried := s14UnverifiedPublicReaders[key]; inventoried {
					inventoryUsed[key] = true
					continue
				}
				unguarded = append(unguarded, key+" reads "+readName+"()")
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", pkg, err)
		}
	}

	sort.Strings(unguarded)
	if len(unguarded) > 0 {
		t.Errorf("PB-SEC-1 (S14a residual F5): %d function(s) read a CONTENT-tier public key with "+
			"no preceding content unseal and no inventory entry:\n\t%s\n"+
			"Those accessors answer from device.key's UNAUTHENTICATED cleartext half, and "+
			"checkPublic can only re-derive them at an unseal -- so with the tier locked the value "+
			"is whatever was written to the app's private data directory. A forged recipient_pub "+
			"seals every epoch grant to a key the attacker holds.\n"+
			"Fix by calling one of %v and returning its error BEFORE the read, or -- if the read is "+
			"genuinely harmless -- add it to s14UnverifiedPublicReaders with the reason, which is a "+
			"decision someone has to write down.",
			len(unguarded), strings.Join(unguarded, "\n\t"), s14SortedKeys(s14ContentUnseals))
	}

	// A stale inventory entry is the other direction, and it matters as much: an exemption for
	// a function that no longer exists (or that now unseals properly) is a standing licence
	// nobody re-examined, and the next reader takes it as precedent.
	for key := range s14UnverifiedPublicReaders {
		if !inventoryUsed[key] {
			t.Errorf("PB-SEC-1: s14UnverifiedPublicReaders exempts %q, which no longer reads a "+
				"content-tier public without an unseal. Delete the entry rather than leaving an "+
				"exemption standing over nothing", key)
		}
	}
}

// TestS14_TheOrderingFenceCanActuallyFail is the negative control, and it is not optional.
//
// The fence above is an AST walk over real files, so the ordinary way it breaks is by matching
// nothing at all -- a renamed accessor, a changed walk root, an over-eager skip -- and a fence
// that matches nothing passes forever. This asserts the two halves it depends on are really
// present in the tree: that some function reads a content public, and that some function orders
// one behind an unseal (which is the branch the main test takes silently).
func TestS14_TheOrderingFenceCanActuallyFail(t *testing.T) {
	root := s14aRepoRoot(t)

	readers, ordered := 0, 0
	for _, rel := range []string{"mobile/pairing.go", "internal/phonecore/command.go", "mobile/app.go"} {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, filepath.Join(root, rel), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", rel, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || s14ContentPublics[fn.Name.Name] || s14PublicMechanism[fn.Name.Name] {
				continue
			}
			readPos, readName := s14FirstCall(fn.Body, s14ContentPublics)
			if readName == "" {
				continue
			}
			readers++
			if unsealPos, _ := s14FirstCall(fn.Body, s14ContentUnseals); unsealPos != token.NoPos && unsealPos < readPos {
				ordered++
			}
		}
	}
	if readers == 0 {
		t.Fatal("the ordering fence found NO content-public reader anywhere in mobile/pairing.go, " +
			"internal/phonecore/command.go or mobile/app.go. It is matching nothing, so it would " +
			"pass over any defect: the accessors were probably renamed")
	}
	if ordered == 0 {
		t.Error("the ordering fence found no reader ORDERED BEHIND AN UNSEAL, so its central " +
			"branch is never taken and the fence has never been shown to accept anything. " +
			"mobile/pairing.go's hoisted NoiseStatic() is supposed to be exactly that case")
	}
}

// s14FirstCall returns the position and name of the first call in body to a method whose
// selector is in want, in source order.
func s14FirstCall(body *ast.BlockStmt, want map[string]bool) (token.Pos, string) {
	pos, name := token.NoPos, ""
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !want[sel.Sel.Name] {
			return true
		}
		if pos == token.NoPos || call.Pos() < pos {
			pos, name = call.Pos(), sel.Sel.Name
		}
		return true
	})
	return pos, name
}

func s14SortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
