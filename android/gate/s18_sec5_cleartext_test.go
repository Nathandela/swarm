package gate

// FAILING-FIRST (TDD RED, GG-5) tests for PB-SEC-5, slice S18:
//
//	"Cleartext traffic is disabled at the platform level FOR THE JAVA/WEBVIEW STACK. v1 wrongly
//	 claimed this backstops PB-NET-2: networkSecurityConfig does not govern Go's crypto/tls
//	 inside a native .so (opus H3), so PB-NET-2 is the sole control for the relay transport."
//	Criterion: "Manifest assertion, WITH THE SCOPE LIMITATION STATED so it is not mistaken for
//	 transport protection."
//
// THE CORRECTION IS PART OF THE REQUIREMENT, so it is part of the test. Android's cleartext
// controls -- android:usesCleartextTraffic and android:networkSecurityConfig -- are enforced by
// the platform networking stack: java.net, OkHttp, WebView, Cronet. The relay connection is
// made by Go's crypto/tls inside the gomobile .so, which does not go through any of them and
// is not affected by either attribute. PB-NET-2 is the only control over that socket.
//
// This file therefore asserts TWO things, and the second is not decoration:
//
//  1. the manifest disables cleartext for the stack it can disable it for; and
//  2. the artifact SAYS SO IN SCOPE-LIMITED TERMS, so the next reader does not conclude the
//     relay transport is covered.
//
// (2) is the requirement's own criterion, verbatim. It is also the specific defect this row
// already shipped once: v1 of the spec made exactly that inference, and it took a review to
// catch. An attribute with no statement beside it invites the same reading again.
//
// RED AT THE TIME OF WRITING: the manifest carries neither android:usesCleartextTraffic nor
// android:networkSecurityConfig, and there is no res/xml network security configuration.
//
// A NOTE FOR THE IMPLEMENTER, because it has broken this build twice: `--` may not appear
// ANYWHERE inside an XML comment. AAPT2 rejects the whole file, which fails
// :app:mergeDebugResources and with it every Gradle task for the module, including the Kotlin
// unit tests. This project's prose style uses `--` heavily. Use colons or parentheses.

import (
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// PB-SEC-5: the platform attribute.
// ---------------------------------------------------------------------------

// TestPBSEC5_TheManifestDisablesCleartextForThePlatformStack accepts EITHER of the two
// mechanisms Android offers, because both are correct and a test that demanded one would fail
// a correct implementation that chose the other:
//
//   - android:usesCleartextTraffic="false" on <application>, the blunt instrument; or
//   - android:networkSecurityConfig pointing at a config whose base-config sets
//     cleartextTrafficPermitted="false", which is the same control with room for exceptions.
//
// What it does not accept is neither, and it does not accept a networkSecurityConfig reference
// that resolves to a file permitting cleartext -- the plausible-but-wrong artifact that looks
// like the control while granting what the control exists to deny.
func TestPBSEC5_TheManifestDisablesCleartextForThePlatformStack(t *testing.T) {
	app := applicationElement(t, "PB-SEC-5")

	usesCleartext, hasAttr := app.attrs["usesCleartextTraffic"]
	netSec, hasNetSec := app.attrs["networkSecurityConfig"]

	if !hasAttr && !hasNetSec {
		t.Fatalf("PB-SEC-5: <application> declares neither android:usesCleartextTraffic nor " +
			"android:networkSecurityConfig. On the app's minSdk-33 floor the platform default " +
			"is already false, so this is not a live hole today; the requirement is that the " +
			"decision be DECLARED, because a default is not a decision and the next targetSdk " +
			"bump is not required to keep it")
	}

	if hasAttr && usesCleartext != "false" {
		t.Errorf("PB-SEC-5: <application android:usesCleartextTraffic=%q>, want \"false\"", usesCleartext)
	}

	if hasNetSec {
		assertNetworkSecurityConfigDeniesCleartext(t, netSec)
	}
}

// assertNetworkSecurityConfigDeniesCleartext resolves the @xml/... reference and reads the
// file. A dangling reference is an AGP lint failure, but a PRESENT file that permits cleartext
// is not -- and that is the case worth a test.
func assertNetworkSecurityConfigDeniesCleartext(t *testing.T, ref string) {
	t.Helper()
	name := strings.TrimPrefix(ref, "@xml/")
	if name == ref {
		t.Errorf("PB-SEC-5: android:networkSecurityConfig=%q is not an @xml/ resource reference", ref)
		return
	}
	path := filepath.Join(appModule(t), "src", "main", "res", "xml", name+".xml")
	cfg := parseXMLFile(t, path, "PB-SEC-5")

	bases := cfg.findAll("base-config")
	if len(bases) == 0 {
		t.Errorf("PB-SEC-5: %s declares no <base-config>, so it states nothing about cleartext "+
			"for the app as a whole", mustRel(t, path))
		return
	}
	for _, b := range bases {
		if got := b.attrs["cleartextTrafficPermitted"]; got != "false" {
			t.Errorf("PB-SEC-5: %s has <base-config cleartextTrafficPermitted=%q>, want "+
				"\"false\". A config that references the control and then permits what it "+
				"forbids is worse than no config: it reads as evidence", mustRel(t, path), got)
		}
	}
	// A domain-config may legitimately relax the base for a named host, but not for everything.
	for _, d := range cfg.findAll("domain-config") {
		if d.attrs["cleartextTrafficPermitted"] == "true" {
			t.Errorf("PB-SEC-5: %s re-permits cleartext in a <domain-config>. If a host really "+
				"needs it, this test is the place the exception gets argued", mustRel(t, path))
		}
	}
}

// ---------------------------------------------------------------------------
// PB-SEC-5: the scope limitation, which IS the criterion.
// ---------------------------------------------------------------------------

// TestPBSEC5_TheScopeLimitationIsStatedBesideTheControl enforces the second half of the
// criterion: the artifact must say that this control does NOT cover the relay transport.
//
// WHY A COMMENT IS THE RIGHT SUBJECT HERE, when this project normally refuses to assert on
// comments. The thing being required is a STATEMENT: "with the scope limitation stated so it
// is not mistaken for transport protection". There is no behaviour to assert, because the
// defect this clause exists to prevent is a READER's inference, and the reader reads the
// manifest. The spec already made this exact inference once and shipped it in v1.
//
// The statement must name PB-NET-2, which is the control that actually protects the relay
// socket -- naming it is what turns "this does not cover everything" into a pointer at the
// thing that does.
func TestPBSEC5_TheScopeLimitationIsStatedBesideTheControl(t *testing.T) {
	raw := readFileOrFail(t, manifestPath(t), "PB-SEC-5")

	// The statement may equally live in the network security config, if that is the mechanism
	// chosen; check both so a correct implementation is not failed for putting it in the
	// better place.
	sources := []struct {
		name string
		text string
	}{{mustRel(t, manifestPath(t)), raw}}

	app := applicationElement(t, "PB-SEC-5")
	if ref, ok := app.attrs["networkSecurityConfig"]; ok {
		name := strings.TrimPrefix(ref, "@xml/")
		p := filepath.Join(appModule(t), "src", "main", "res", "xml", name+".xml")
		if exists(p) {
			sources = append(sources, struct {
				name string
				text string
			}{mustRel(t, p), readFileOrFail(t, p, "PB-SEC-5")})
		}
	}

	for _, s := range sources {
		if strings.Contains(s.text, "PB-NET-2") {
			return
		}
	}

	var names []string
	for _, s := range sources {
		names = append(names, s.name)
	}
	t.Errorf("PB-SEC-5: none of %s states the scope limitation. The criterion is \"manifest "+
		"assertion, WITH THE SCOPE LIMITATION STATED so it is not mistaken for transport "+
		"protection\", and the statement must name PB-NET-2 as the control that does protect "+
		"the relay socket. networkSecurityConfig and usesCleartextTraffic govern the "+
		"Java/WebView stack only; the relay connection is Go's crypto/tls inside the gomobile "+
		".so and is not affected by either. v1 of the spec drew the wrong inference from this "+
		"attribute and shipped it (opus H3); an attribute with nothing beside it invites the "+
		"same reading again. Reminder: `--` is illegal inside an XML comment and AAPT2 rejects "+
		"the whole file", strings.Join(names, " or "))
}
