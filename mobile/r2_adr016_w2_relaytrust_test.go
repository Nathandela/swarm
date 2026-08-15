package swarmmobile_test

// ADR-016 W2 (FAILING FIRST, RED phase, bd agents-tracker-hggx.3.5): the reverse-bound
// RelayTrust seam, in EXACTLY the shape mobile/keycustody.go established for KeyCustody
// (mobile/s14_custody_test.go is this file's direct template).
//
//	RelayTrust interface {
//	    VerifyRelayChain(host string, pemChain []byte) error
//	}
//
// THE DIRECTION IS DELIBERATELY THE OPPOSITE OF KeyCustody'S, and W2 states why: "a server
// certificate chain is public by construction", so unlike KeyCustody (no []byte PARAMETER
// permitted -- key material must never cross outbound) RelayTrust's whole point is a
// PARAMETER carrying the chain outbound to Kotlin. This file's direction test is therefore
// the deliberate MIRROR of TestS14_TheCustodySeamIsInboundOnly, not a copy of it: it
// requires the []byte parameter mobile/s14_custody_test.go forbids elsewhere.
//
// mobile/bind_test.go's own guard (TestPBBIND7_ExportedSurfaceMatchesTheGolden) is what
// makes this addition to the bound surface a DELIBERATE, reviewed act once implemented --
// see that file's own doc comment ("Run with -update-surface to regenerate after a
// REVIEWED contract change"). This RED-phase test does not touch the golden file: the
// interface does not exist yet, so TestPBBIND2/TestPBBIND7 already fail to build against
// this file's own expectations, which is the point of a RED phase.

import (
	"go/ast"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestADR016W2_RelayTrustInterfaceExistsWithTheOutboundChainShape parses the facade's
// source (the same technique TestS14_TheCustodySeamIsInboundOnly uses) and requires an
// exported RelayTrust interface with exactly one exported method, VerifyRelayChain, taking
// (host string, pemChain []byte) and returning error.
func TestADR016W2_RelayTrustInterfaceExistsWithTheOutboundChainShape(t *testing.T) {
	src := loadFacade(t)

	var found *ast.InterfaceType
	for _, f := range src.Files {
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || ts.Name.Name != "RelayTrust" {
					continue
				}
				it, ok := ts.Type.(*ast.InterfaceType)
				if !ok {
					t.Fatalf("RelayTrust is declared but is not an interface type")
				}
				found = it
			}
		}
	}
	if found == nil {
		t.Fatalf("ADR-016 W2: the facade declares no exported RelayTrust interface. Go calls " +
			"Kotlin's X509TrustManagerExtensions through this reverse-bound seam; without it " +
			"WithPlatformVerifier has nothing to receive from Android")
	}
	if len(found.Methods.List) != 1 {
		t.Fatalf("RelayTrust carries %d methods, want exactly 1 (VerifyRelayChain) -- W2 names one "+
			"verb, and a second would be a second reverse crossing nothing in the ADR authorizes",
			len(found.Methods.List))
	}
	m := found.Methods.List[0]
	if len(m.Names) != 1 || m.Names[0].Name != "VerifyRelayChain" {
		t.Fatalf("RelayTrust's one method is not named VerifyRelayChain")
	}
	ft, ok := m.Type.(*ast.FuncType)
	if !ok {
		t.Fatalf("RelayTrust.VerifyRelayChain is not a func type")
	}
	if ft.Params == nil || len(ft.Params.List) != 2 {
		t.Fatalf("VerifyRelayChain must take exactly two parameters (host string, pemChain []byte)")
	}
	if !isIdent(ft.Params.List[0].Type, "string") {
		t.Errorf("VerifyRelayChain's first parameter is not string (host)")
	}
	if !isByteSlice(ft.Params.List[1].Type) {
		t.Errorf("VerifyRelayChain's second parameter is not []byte (pemChain) -- W2: the chain " +
			"travels as PEM because gomobile cannot bind [][]byte")
	}
	if ft.Results == nil || len(ft.Results.List) != 1 || !isIdent(ft.Results.List[0].Type, "error") {
		t.Errorf("VerifyRelayChain must return exactly (error)")
	}
}

// TestADR016W2_RelayTrustParameterCarriesTheChainOutbound is the deliberate mirror of
// TestS14_TheCustodySeamIsInboundOnly: on KeyCustody a []byte PARAMETER is forbidden
// (outbound key material); on RelayTrust a []byte parameter is REQUIRED, because the chain
// is public and the whole point is handing it to Kotlin. A regression that "fixed" this by
// copying KeyCustody's rule verbatim would delete the seam's only parameter.
func TestADR016W2_RelayTrustParameterCarriesTheChainOutbound(t *testing.T) {
	src := loadFacade(t)
	sawByteSliceParam := false
	for _, f := range src.Files {
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || ts.Name.Name != "RelayTrust" {
					continue
				}
				it, ok := ts.Type.(*ast.InterfaceType)
				if !ok {
					continue
				}
				for _, m := range it.Methods.List {
					ft, ok := m.Type.(*ast.FuncType)
					if !ok || ft.Params == nil {
						continue
					}
					for _, p := range ft.Params.List {
						if isByteSlice(p.Type) {
							sawByteSliceParam = true
						}
					}
				}
			}
		}
	}
	if !sawByteSliceParam {
		t.Fatalf("RelayTrust has no []byte parameter; W2's outbound chain crossing is missing")
	}
}

// TestADR016W2_VerdictTokensAgreeAcrossTheLanguageBoundary mirrors
// TestS14_TheTwoCustodyVerdictTokensAgreeAcrossTheLanguageBoundary exactly: the two verdict
// tokens W2 names -- swarm-relaytrust/untrusted (a real security verdict) and
// swarm-relaytrust/unavailable (no platform verifier answered) -- must be stamped by the Go
// facade AND matched by the Kotlin implementation, so a refusal reaches the user as the
// right one of W8's distinct states rather than as an opaque bug report.
func TestADR016W2_VerdictTokensAgreeAcrossTheLanguageBoundary(t *testing.T) {
	src := loadFacade(t)
	goSide := facadeSourceText(t, src)

	kotlin := filepath.Join(repoRoot(t), "android", "app", "src", "main", "kotlin",
		"dev", "swarm", "phone", "relay", "RelayTrust.kt")
	raw, err := os.ReadFile(kotlin)
	if err != nil {
		t.Fatalf("ADR-016 W2: the Kotlin RelayTrust implementation is not at %s: %v", kotlin, err)
	}
	kotlinSide := string(raw)

	for _, token := range []string{"swarm-relaytrust/untrusted", "swarm-relaytrust/unavailable"} {
		if !strings.Contains(goSide, token) {
			t.Errorf("ADR-016 W2: the facade does not declare the verdict token %q", token)
		}
		if !strings.Contains(kotlinSide, token) {
			t.Errorf("ADR-016 W2: %s does not stamp the verdict token %q it must throw for the "+
				"Go side to distinguish a security verdict from a platform-capability fault "+
				"(W8's relay_untrusted vs relay_trust_unavailable)", kotlin, token)
		}
	}
}

func isIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}
