// R-CRY.14 — no hand-rolled primitives. Every crypto import in the package's
// production (non-test) source must come from the vetted allowlist: flynn/noise,
// golang.org/x/crypto, or the stdlib crypto packages. No bespoke cipher/KDF/MAC/
// curve. This test parses the package source directly (go/parser, stdlib), so
// it needs no new symbols; at RED the whole package fails to build on the other
// files' undefined symbols, so it does not run — it enforces the invariant once
// the implementation exists.
package crypto

import (
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// cryptoImportAllowlist is the exact set of crypto-relevant import prefixes the
// foundation may use (ADR-007 D2: "No hand-rolled primitives").
var cryptoImportAllowlist = []string{
	"github.com/flynn/noise",
	"golang.org/x/crypto/", // nacl/box, curve25519, chacha20poly1305, hkdf, blake2b
	"crypto/ed25519",
	"crypto/rand",
	"crypto/sha256",
	"crypto/sha512", // ed25519 dependency surface
	"crypto/hmac",
	"crypto/subtle",
	"crypto/cipher", // AEAD interfaces only, backed by the above
}

// cryptoImportHeuristic flags an import as crypto-relevant (and therefore
// allowlist-gated). A bespoke local crypto package or an unexpected primitive
// trips it.
func cryptoImportHeuristic(path string) bool {
	needles := []string{
		"crypto", "cipher", "noise", "chacha", "poly1305", "salsa",
		"curve25519", "nacl", "hkdf", "ed25519", "sha", "hmac", "blake",
		"aes", "des", "rc4", "md5",
	}
	low := strings.ToLower(path)
	for _, n := range needles {
		if strings.Contains(low, n) {
			return true
		}
	}
	return false
}

func allowedCryptoImport(path string) bool {
	for _, ok := range cryptoImportAllowlist {
		if path == strings.TrimSuffix(ok, "/") || strings.HasPrefix(path, ok) {
			return true
		}
	}
	return false
}

// TestDeps_NoHandRolledCrypto pins the import allowlist over the package's
// production source.
func TestDeps_NoHandRolledCrypto(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	fset := token.NewFileSet()
	var scanned int
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, imp := range file.Imports {
			scanned++
			path := strings.Trim(imp.Path.Value, `"`)
			if cryptoImportHeuristic(path) && !allowedCryptoImport(path) {
				t.Errorf("%s: crypto import %q is not in the allowlist (hand-rolled?)", name, path)
			}
		}
	}
	if scanned == 0 {
		t.Log("no production imports scanned yet (implementation pending)")
	}
}
