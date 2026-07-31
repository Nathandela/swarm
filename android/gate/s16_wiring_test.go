package gate

// FAILING-FIRST (TDD RED, GG-5) for the S14 carry-over S16 inherits: THE ANDROID KEY CUSTODY
// HAS NO PRODUCTION WIRING AT ALL.
//
// Verified in the tree before these assertions were written, and each of the three is
// independently sufficient to make the app non-functional on a real handset:
//
//  1. `swarmmobile.NewApp` appears in android/app/src/main/ ONLY INSIDE COMMENTS. Nothing
//     constructs a phone. Every Go-side conformance test in this repository drives an App the
//     Android app has no code to create.
//  2. There is NO KekProvider implementation outside the tests. The single one is
//     FakeKeystoreKek in src/test/.../CustodyFixtures.kt, which is a software AES key held in
//     the test JVM.
//  3. There is NO FILE I/O ANYWHERE under src/main/ -- no openFileOutput, no filesDir, no
//     SharedPreferences, no File. SealedStore is `LinkedHashMap<String, Entry>`.
//
// THE CONSEQUENCE IS A BRICK, AND IT IS STANDING DEFECT CLASS (ii) -- a plausible-but-wrong
// value hiding it. The sealing key lives only in the process, so on the next start both
// device.key and phone-state.json are sealed under a key that no longer exists: permanently
// unopenable, on a platform where process death is routine. SealedStore.rawBytes's own doc
// says "The persisted bytes, exactly as they sit on disk" -- a comment asserting a property
// the type does not have, which is what a reader checks instead of the code. This is the same
// failure shape as the fresh-install defect found earlier in this phase, which lost the entire
// durable state on the first restart.
//
// WHAT IS NOT CLAIMED HERE. None of this establishes that the KEK is in a TEE or that
// StrongBox behaves as advertised -- that is PB-E2E-5 and it stays deferred. These are existence checks on
// the PRODUCTION PATH: whether the app has any code at all to create a phone and to keep a key
// across a restart. They are deliberately weak checks of a strong property, and they fire on
// the one case that matters, which is zero.

import (
	"path/filepath"
	"strings"
	"testing"
)

// androidPersistenceAPIs are the ways an Android app can put bytes somewhere that survives the
// process. The list is generous on purpose: the assertion is that the app uses ONE of them,
// and a narrow list would fail a correct implementation that chose a different one.
var androidPersistenceAPIs = []string{
	"openFileOutput", "openFileInput", "filesDir", "noBackupFilesDir", "getDataDir",
	"SharedPreferences", "EncryptedFile", "EncryptedSharedPreferences",
	"java.io.File", "FileOutputStream", "FileInputStream", "okio", "DataStore",
}

// TestS16_TheSealedStoreSurvivesAProcessDeath.
//
// The phone core's key material at rest is sealed under a KEK the Android side holds
// (PB-KEY-9), and phonecore.Resume fails closed without one. If that KEK is created fresh in
// each process, the seal succeeds, the app looks correct, and the SECOND launch cannot open
// its own state -- and cannot recover, because the material needed to open it is gone.
func TestS16_TheSealedStoreSurvivesAProcessDeath(t *testing.T) {
	root := kotlinMainRoot(t)
	var hits []string
	for _, f := range kotlinFiles(t, root) {
		src := readFileOrFail(t, f, "PB-KEY-9")
		for _, api := range androidPersistenceAPIs {
			if strings.Contains(src, api) {
				hits = append(hits, mustRel(t, f)+": "+api)
			}
		}
	}
	if len(hits) == 0 {
		t.Errorf("PB-KEY-9/PB-STATE-1: there is NO durable storage anywhere under %s -- none of "+
			"%s appears in any production Kotlin file.\n"+
			"SealedStore is a LinkedHashMap, so the sealing key and every sealed blob live only "+
			"in the process. The seal succeeds, the app looks correct, and the SECOND launch "+
			"finds device.key and phone-state.json sealed under a key that no longer exists: "+
			"permanently unopenable, on a platform where process death is routine.\n"+
			"SealedStore.rawBytes's own doc calls its return \"the persisted bytes, exactly as "+
			"they sit on disk\" -- a comment claiming a property the type does not have, which is "+
			"the thing a reader checks instead of the code. Same failure shape as the "+
			"fresh-install defect that lost the whole durable state on the first restart.",
			mustRel(t, root), strings.Join(androidPersistenceAPIs, ", "))
	}
}

// TestS16_AProductionKekProviderExists.
//
// KekProvider is the seam between the sealed store and the Android Keystore, and its only
// implementation in the repository is a test fixture. A seam with nothing on the far side is
// exactly the shape S14a's residual warned about from the other direction -- a Go-side gate
// green over a path the Android app cannot take.
func TestS16_AProductionKekProviderExists(t *testing.T) {
	var impls []string
	for _, f := range kotlinFiles(t, kotlinMainRoot(t)) {
		src := readFileOrFail(t, f, "PB-KEY-9")
		for _, decl := range kotlinImplementsSupertype(src, "KekProvider") {
			impls = append(impls, mustRel(t, f)+": "+decl)
		}
	}
	if len(impls) == 0 {
		t.Errorf("PB-KEY-9: no KekProvider implementation exists under %s. The only one in the "+
			"repository is FakeKeystoreKek in src/test/.../CustodyFixtures.kt -- a software AES "+
			"key held in the test JVM.\n"+
			"So the seam the whole custody design rests on has nothing on the far side in "+
			"production: the app cannot wrap or unwrap anything, and the Go-side fences that "+
			"prove the facade seals correctly are proving it over a path the handset cannot take.",
			mustRel(t, kotlinMainRoot(t)))
	}
}

// kotlinImplementsSupertype reports whether src declares any class or object implementing
// name, scanning the WHOLE FILE rather than one line at a time.
//
// Both halves of that are corrections to a first draft that got this wrong in both directions,
// and both mistakes were silent:
//
//   - Per LINE, a genuine multi-line declaration is missed. Kotlin's own convention puts the
//     supertype on the line with the closing paren:
//     class FakeKeystoreKek(
//     ...
//     ) : KekProvider {
//     A line-based check sees `) : KekProvider {`, finds no `class` keyword, and reports no
//     implementation -- pushing the implementer to write it on one line to satisfy a fence.
//   - Without stripping the CONSTRUCTOR, `class SealedStore(private val kek: KekProvider)` is
//     counted as an implementation. It TAKES one and implements nothing. This fence passed
//     green over a tree with no production provider at all until that was fixed.
//
// So: find each declaration keyword, walk to the `{` that opens its body while tracking paren
// depth, and look for name after a depth-zero `:` in that header.
func kotlinImplementsSupertype(src, name string) []string {
	// COMMENTS ARE STRIPPED FIRST, and this is not tidiness. Custody.kt's doc comments discuss
	// KekProvider at length, and the word "class" appears in prose all over this codebase -- so
	// a scanner that walks forward from a keyword inside a comment can reach a `:` and the name
	// in ordinary English and report an implementation that does not exist. That is the same
	// vacuous pass this fence already had once, arriving by a different route.
	src = stripKotlinComments(src)
	var out []string
	for _, kw := range []string{"class ", "object "} {
		for i := 0; ; {
			j := strings.Index(src[i:], kw)
			if j < 0 {
				break
			}
			at := i + j
			i = at + len(kw)
			// A keyword must start a token, or "dataclass"/"myobject " would match.
			if at > 0 && !isKotlinBreak(src[at-1]) {
				continue
			}
			header, ok := kotlinDeclHeader(src[at:])
			if !ok {
				continue
			}
			if kotlinSupertypeList(header, name) {
				out = append(out, strings.Join(strings.Fields(header), " "))
			}
		}
	}
	return out
}

// stripKotlinComments blanks // and /* */ spans, preserving newlines so the scanner's
// line-sensitive branch still sees the same structure.
func stripKotlinComments(src string) string {
	out := []byte(src)
	for i := 0; i < len(out); i++ {
		switch {
		case out[i] == '/' && i+1 < len(out) && out[i+1] == '/':
			for i < len(out) && out[i] != '\n' {
				out[i] = ' '
				i++
			}
		case out[i] == '/' && i+1 < len(out) && out[i+1] == '*':
			for i < len(out) {
				if out[i] == '*' && i+1 < len(out) && out[i+1] == '/' {
					out[i], out[i+1] = ' ', ' '
					i++
					break
				}
				if out[i] != '\n' {
					out[i] = ' '
				}
				i++
			}
		}
	}
	return string(out)
}

func isKotlinBreak(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

// kotlinDeclHeader is everything from a declaration keyword to the `{` that opens its body.
func kotlinDeclHeader(src string) (string, bool) {
	depth := 0
	for i := 0; i < len(src); i++ {
		switch src[i] {
		case '(', '<':
			depth++
		case ')', '>':
			depth--
		case '{':
			if depth <= 0 {
				return src[:i], true
			}
		case '\n':
			// A declaration with no body ends at its line, e.g. `class Foo : Bar`.
			if depth <= 0 && strings.Contains(src[:i], ":") {
				return src[:i], true
			}
		}
	}
	return "", false
}

// kotlinSupertypeList reports whether name appears after a depth-zero ":" in a declaration
// header -- i.e. in the supertype list rather than in a constructor parameter's type.
func kotlinSupertypeList(header, name string) bool {
	depth := 0
	for i := 0; i < len(header); i++ {
		switch header[i] {
		case '(', '<':
			depth++
		case ')', '>':
			depth--
		case ':':
			if depth == 0 {
				return strings.Contains(header[i:], name)
			}
		}
	}
	return false
}

// TestS16_TheAppConstructsThePhone.
//
// swarmmobile.NewApp is the ONLY way to obtain a phone -- there is deliberately no constructor
// that omits the KeyCustody -- so an app that never calls it has no phone core, no relay
// connection, no durable state and no screens with anything to render. The symbol appears in
// src/main/ today only inside doc comments explaining why the seam matters.
func TestS16_TheAppConstructsThePhone(t *testing.T) {
	for _, f := range kotlinFiles(t, kotlinMainRoot(t)) {
		src := readFileOrFail(t, f, "PB-BIND-3")
		for _, line := range strings.Split(src, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, "//") ||
				strings.HasPrefix(trimmed, "/*") {
				continue // a doc comment ABOUT NewApp is not a call TO it
			}
			if strings.Contains(trimmed, "newApp(") || strings.Contains(trimmed, "NewApp(") {
				return
			}
		}
	}
	t.Errorf("PB-BIND-3/PB-KEY-9: nothing under %s constructs a phone. `swarmmobile.NewApp` "+
		"appears only inside doc comments.\n"+
		"It is the only constructor there is -- there is deliberately no variant that omits the "+
		"KeyCustody -- so the shipped app has no phone core, no relay connection, no durable "+
		"state and no screen with anything to render. Every conformance test in this repository "+
		"drives an App the Android app has no code to create, which is standing defect class (v) "+
		"at the largest possible scale: the whole Go suite fences a path production does not take.",
		mustRel(t, kotlinMainRoot(t)))
}

// TestS16_TheKeystoreProviderLivesBesideItsPolicy is a placement check, and it is here because
// the alternative placement is the one that silently reintroduces the defect.
//
// dev.swarm.phone.keys already holds the POLICY (Provisioning, Custody) and
// its tests. A provider written somewhere else -- an Activity, an Application subclass -- is
// one the policy tests do not see, and the first thing it will do is hold the KEK in a field.
func TestS16_TheKeystoreProviderLivesBesideItsPolicy(t *testing.T) {
	keysDir := filepath.Join(kotlinMainRoot(t), filepath.FromSlash("dev/swarm/phone/keys"))
	if len(kotlinFiles(t, keysDir)) == 0 {
		t.Fatalf("PB-KEY-9: %s does not exist", mustRel(t, keysDir))
	}
	for _, f := range kotlinFiles(t, keysDir) {
		src := readFileOrFail(t, f, "PB-KEY-9")
		for _, api := range androidPersistenceAPIs {
			if strings.Contains(src, api) {
				return
			}
		}
	}
	t.Errorf("PB-KEY-9: the key custody package %s contains no durable storage. Whatever keeps "+
		"the sealed blobs must sit beside the policy that governs them -- a provider written "+
		"into an Activity or the Application subclass is one none of the PB-KEY-2/PB-KEY-7/"+
		"PB-KEY-8 tests can see, and the first thing an unwatched provider does is cache the "+
		"KEK in a field, which quietly replaces Keystore sealing at rest with a copy in "+
		"process memory",
		mustRel(t, keysDir))
}
