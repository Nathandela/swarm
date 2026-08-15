package swarmmobile_test

// Slice S14 -- source-level guards for PB-KEY-9's facade seam (ADR-007 B8, B17).
//
// The runtime halves are in ./conformance (the dial-refusal behaviour) and in ../android/gate
// (the byte-level proof that the shipped path actually seals). These are the shape guards, and
// they exist because the shape is where the mistake would be made: the natural way to write a
// KEK seam is a reverse-bound Seal/Open pair, and that hands Java the PLAINTEXT device scalars.

import (
	"go/ast"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestS14_TheCustodySeamIsInboundOnly pins the direction of the reverse-bound surface.
//
// THE RULE IS MIRRORED, and that is the whole subtlety. On App's own methods Go is the callee,
// so a PARAMETER is inbound and a RESULT is outbound -- which is why
// TestPBBIND4_TheOnlySecretCrossingIsNamedAndInbound forbids a method returning []byte. On a
// reverse-bound INTERFACE Go is the caller and Kotlin the callee, so the directions swap: a
// RESULT travels Java -> Go (inbound, and that is B8's single permitted crossing) while a
// PARAMETER travels Go -> Java (outbound, and key material must never take it).
//
// So: no exported interface method on this facade may TAKE []byte. The shape that violates it
// is not hypothetical -- `Seal(plaintext []byte) ([]byte, error)` is the obvious way to express
// a KEK seam, it is exactly what phonecore.Sealer looks like on the Go side, and reverse-binding
// it would hand the Java layer the three content-tier private scalars in the clear on every
// first launch. B8 permits Go to hand back "sealed blobs, public keys and signatures" and
// nothing else.
//
// PB-BIND-4's own guard cannot cover this: entryPoints() is funcs and methods only, so an
// ifacemethod is invisible to it. That gap is the reason this test exists.
//
// RelayTrust IS A NAMED, DELIBERATE EXEMPTION (ADR-016 W2), not a loosening of B8. B8 bans
// KEY MATERIAL crossing outbound; RelayTrust.VerifyRelayChain's []byte parameter is a
// PEM-encoded server certificate chain, which "is public by construction" the instant the
// TLS handshake presents it -- a network observer already saw every byte in the clear.
// "The direction rule is different from KeyCustody's and the difference is the point"
// (ADR-016 W2). The exemption is scoped to this ONE interface by name, so any future
// reverse-bound interface still trips this fence until it is reviewed and named here too.
func TestS14_TheCustodySeamIsInboundOnly(t *testing.T) {
	// relayTrustOutboundChainException is ADR-016 W2's one named carve-out from B8's
	// inbound-only rule -- see the doc comment above.
	const relayTrustOutboundChainException = "RelayTrust"
	src := loadFacade(t)

	found := map[string]string{}
	for _, f := range src.Files {
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || !ts.Name.IsExported() {
					continue
				}
				it, ok := ts.Type.(*ast.InterfaceType)
				if !ok {
					continue
				}
				for _, m := range it.Methods.List {
					ft, ok := m.Type.(*ast.FuncType)
					if !ok {
						continue
					}
					for _, mn := range m.Names {
						if !mn.IsExported() {
							continue
						}
						name := ts.Name.Name + "." + mn.Name
						found[name] = ts.Name.Name
						if ft.Params == nil || ts.Name.Name == relayTrustOutboundChainException {
							continue
						}
						for _, p := range ft.Params.List {
							if isByteSlice(p.Type) {
								t.Errorf("ADR-007 B8: the reverse-bound %s TAKES []byte. On a "+
									"reverse-bound interface a parameter travels Go -> Java, so this "+
									"is an OUTBOUND key crossing -- the direction B8 forbids and B17 "+
									"declined to widen. The KEK comes IN; key material never goes OUT",
									name)
							}
						}
					}
				}
			}
		}
	}

	// The seam must EXIST and be exactly two verbs wide, or the assertion above is a guard over
	// nothing -- and a facade with no custody verb is the state PB-KEY-9 was undelivered in.
	for _, want := range []string{"KeyCustody.WakeKEK", "KeyCustody.ContentKEK"} {
		if _, ok := found[want]; !ok {
			t.Errorf("PB-KEY-9: the facade declares no %s. Without it the Android app cannot supply "+
				"a KEK at all, and the only reachable custody is the cleartext one", want)
		}
	}
	var custody []string
	for name, owner := range found {
		if owner == "KeyCustody" {
			custody = append(custody, name)
		}
	}
	if len(custody) != 2 {
		t.Errorf("ADR-007 B8: KeyCustody carries %d methods (%v), want exactly two -- one per "+
			"PB-KEY-2 tier. B8 lets the matrix NARROW the crossing and never widen it, so a third "+
			"verb here is a second crossing", len(custody), custody)
	}
}

// TestS14_TheTwoCustodyVerdictTokensAgreeAcrossTheLanguageBoundary.
//
// gomobile flattens a Go error into a Java exception carrying only its MESSAGE, so the two
// crypto sentinels cross as tokens. Go is authoritative and Kotlin is checked against it here,
// in this direction, because the Kotlin unit-test JVM does not load the AAR and therefore cannot
// read the bound constants at test time -- it holds literals instead.
//
// A drifted copy would fail SILENTLY and in the worst possible direction: an unrecognised token
// degrades a permanent invalidation into "authenticate again", which is a prompt the user can
// satisfy and that changes nothing, forever. That is precisely the failure PB-KEY-6 exists to
// prevent, so the two copies are pinned to each other rather than trusted to stay in step.
func TestS14_TheTwoCustodyVerdictTokensAgreeAcrossTheLanguageBoundary(t *testing.T) {
	src := loadFacade(t)
	goSide := facadeSourceText(t, src)

	kotlin := filepath.Join(repoRoot(t), "android", "app", "src", "main", "kotlin",
		"dev", "swarm", "phone", "keys", "Custody.kt")
	raw, err := os.ReadFile(kotlin)
	if err != nil {
		t.Fatalf("PB-KEY-6: the Kotlin custody layer is not where this fence looks (%s): %v", kotlin, err)
	}
	kotlinSide := string(raw)

	for _, token := range []string{"swarm-custody/auth-required", "swarm-custody/key-invalidated"} {
		if !strings.Contains(goSide, token) {
			t.Errorf("PB-KEY-6: the facade no longer declares the verdict token %q. Kotlin still "+
				"matches on it, so every custody refusal now classifies as Unexpected -- which the "+
				"UI reports as a bug rather than as a prompt", token)
		}
		if !strings.Contains(kotlinSide, token) {
			t.Errorf("PB-KEY-6: %s does not contain the verdict token %q that the facade stamps onto "+
				"custody refusals. The Android side cannot then tell a recoverable refusal from a "+
				"permanent one, and PB-KEY-6's whole point is that the UI acts differently on each",
				kotlin, token)
		}
	}
}
