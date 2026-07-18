// Remediation RED (fix wave) — behavioral tests that exercise EXISTING symbols
// and fail against the un-fixed implementation. Companion new-API tests that
// fail to compile (undefined symbols) live in hardening_api_test.go.
//
// Findings pinned here: F1 (empty-pin fail-open), F2 (NoiseStatic private
// leak), F6 (exported caller-nonce footgun), F8 (0600 not tightened on an
// existing file), F11 (prologue field-splicing), F12 (empty-plaintext
// rejected), F14 (missing sender_key_id-in-AAD tamper pin).
package crypto

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// F1 — a LIVE session with an empty/short pin must be a hard error at
// construction. An empty pin previously reached transport against any peer
// (fail-open at noise.go:130 behind the `len(s.pinned) > 0` guard).
func TestLive_EmptyPinIsHardError(t *testing.T) {
	a, _ := GenerateIdentity()

	if _, err := NewNoise(NoiseConfig{
		Initiator: true, Static: a.NoiseStatic(), PeerStatic: nil,
		Prologue: livePrologue(),
	}); err == nil {
		t.Fatal("NewNoise accepted an empty (nil) pin for a live session")
	}
	if _, err := NewNoise(NoiseConfig{
		Initiator: true, Static: a.NoiseStatic(), PeerStatic: []byte{0x01, 0x02},
		Prologue: livePrologue(),
	}); err == nil {
		t.Fatal("NewNoise accepted a wrong-length pin for a live session")
	}
}

// F2 — *NoiseStatic must redact its private scalar under every fmt verb, so a
// stray log.Printf("%v", ks.NoiseStatic()) cannot spill the DH private.
func TestNoiseStatic_NoPrivateInFormat(t *testing.T) {
	priv := fill(0xD7)
	id := NewIdentityFromMaterial(priv, fill(0xE8))
	ns := id.NoiseStatic()

	// A struct embedding *NoiseStatic exercises the promoted Formatter across
	// the %x / %q struct-field rendering paths as well.
	type wrap struct{ *NoiseStatic }
	w := wrap{ns}

	// Variable verbs (not string literals) keep `go vet` from statically
	// objecting to %x/%q on a struct before the Formatter exists.
	verbs := []string{"%v", "%+v", "%#v", "%s", "%x", "%q"}
	expected := fmt.Sprintf("crypto.NoiseStatic{pub:%s}", fingerprint(id.NoiseStaticPublic()))
	for _, verb := range verbs {
		if got := fmt.Sprintf(verb, ns); got != expected {
			t.Errorf("format %s = %q, want redacted %q", verb, got, expected)
		}
	}

	needles := [][]byte{
		priv[:],
		bytes.Repeat([]byte{0xD7}, 16), // %s raw run
		[]byte(strings.TrimRight(strings.Repeat("215 ", 8), " ")),  // %v decimal run
		[]byte(hex.EncodeToString(bytes.Repeat([]byte{0xD7}, 16))), // %x / %#v hex run
	}
	var reps []string
	reps = append(reps, fmt.Sprint(ns))
	for _, verb := range verbs {
		reps = append(reps, fmt.Sprintf(verb, ns), fmt.Sprintf(verb, w))
	}
	for _, rep := range reps {
		for _, n := range needles {
			if bytes.Contains([]byte(rep), n) {
				t.Errorf("formatted NoiseStatic leaks private material: %q", rep)
			}
		}
	}
}

// F2 — NoiseSession and NoiseConfig transitively hold private material (flynn's
// HandshakeState/CipherStates, the static keypair, the PSK). No fmt verb may
// print their contents; the PSK is the newly-added surface.
func TestNoiseSessionAndConfig_NoPrivateInFormat(t *testing.T) {
	a, _ := GenerateIdentity()
	b, _ := GenerateIdentity()
	psk := fill(0x77)
	cfg := NoiseConfig{
		Initiator: true, Static: a.NoiseStatic(), AllowUnpinnedPeer: true,
		PSK: psk[:], Prologue: PairPrologue([]byte("rv")),
	}
	sess, _ := completedPair(t) // an established *NoiseSession with live keys
	_ = b

	pskNeedle := bytes.Repeat([]byte{0x77}, 16)
	verbs := []string{"%v", "%+v", "%#v", "%s"}
	for _, verb := range verbs {
		for label, rep := range map[string]string{
			"NoiseConfig":  fmt.Sprintf(verb, cfg),
			"NoiseConfigP": fmt.Sprintf(verb, &cfg),
			"NoiseSession": fmt.Sprintf(verb, sess),
		} {
			if strings.Contains(rep, "redacted") == false {
				t.Errorf("%s %s not redacted: %q", label, verb, rep)
			}
			if bytes.Contains([]byte(rep), pskNeedle) {
				t.Errorf("%s %s leaks PSK material: %q", label, verb, rep)
			}
		}
	}
}

// F6 — no EXPORTED function may accept a caller-supplied 24-byte nonce; the
// deterministic seal is a package-private KAT/fan-out helper only.
func TestSeal_NoExportedCallerNonce(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !fn.Name.IsExported() || fn.Type.Params == nil {
				continue
			}
			for _, p := range fn.Type.Params.List {
				arr, ok := p.Type.(*ast.ArrayType)
				if !ok {
					continue
				}
				if lit, ok := arr.Len.(*ast.BasicLit); ok && lit.Value == "24" {
					t.Errorf("exported %s accepts a 24-byte caller nonce (nonce-reuse footgun)", fn.Name.Name)
				}
			}
		}
	}
}

// F8 — Save must yield a 0600 key file even when one already exists 0644;
// os.WriteFile does not tighten an existing file's mode.
func TestIdentity_SaveTightensExistingPerms(t *testing.T) {
	dir := t.TempDir()
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, identityFile)
	if err := os.WriteFile(path, []byte("stale-world-readable"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := id.Save(dir); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("Save left key file at %o over a pre-existing 0644 file, want 0600", perm)
	}
}

func TestKeyStore_SaveTightensExistingPerms(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, deviceKeyFile)
	if err := os.WriteFile(path, bytes.Repeat([]byte{0}, 128), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileKeyStoreFromMaterial(dir, stdMaterial()); err != nil {
		t.Fatalf("NewFileKeyStoreFromMaterial: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("device key file left at %o over a pre-existing 0644 file, want 0600", perm)
	}
}

// F11 — LivePrologue must length-prefix its routing ids; ("a","bc") must not
// collide with ("ab","c").
func TestLivePrologue_NoFieldSplicing(t *testing.T) {
	if bytes.Equal(LivePrologue([]byte("a"), []byte("bc")), LivePrologue([]byte("ab"), []byte("c"))) {
		t.Error("LivePrologue splices variable-length ids: (a,bc) collides with (ab,c)")
	}
}

// F12 — an empty-plaintext envelope (16-byte tag, no content) is valid and must
// round-trip; ParseEnvelope's `<=` boundary rejected it.
func TestEnvelope_EmptyPlaintextRoundTrips(t *testing.T) {
	key := fill(0x9c)
	env, err := seal(key, testHeader(), []byte{})
	if err != nil {
		t.Fatalf("seal(empty): %v", err)
	}
	raw := env.Marshal()
	parsed, err := ParseEnvelope(raw)
	if err != nil {
		t.Fatalf("ParseEnvelope(empty-plaintext): %v", err)
	}
	pt, err := parsed.open(key)
	if err != nil {
		t.Fatalf("open(empty-plaintext): %v", err)
	}
	if len(pt) != 0 {
		t.Errorf("empty-plaintext round-trip returned %d bytes, want 0", len(pt))
	}
}

// F14 — regression pin: sender_key_id (offset 22) IS AAD-covered, so tampering
// it fails Open. (This behavior is already correct; the test was missing.)
func TestEnvelope_SenderKeyIDInAAD(t *testing.T) {
	key := fill(0x9c)
	env, err := seal(key, testHeader(), []byte("payload"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	raw := env.Marshal()
	raw[22] ^= 0xff // first byte of sender_key_id
	bad, err := ParseEnvelope(raw)
	if err != nil {
		return // a parse rejection is also acceptable
	}
	if _, err := bad.open(key); err == nil {
		t.Error("tampering sender_key_id (AAD-covered) was accepted by Open")
	}
}
