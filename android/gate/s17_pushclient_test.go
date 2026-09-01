package gate

// FAILING-FIRST (TDD RED, GG-5) for slice S17's ANDROID-side obligations: PB-PUSH-9's client
// token lifecycle and PB-PUSH-4's notification, checked as SOURCE FACTS about production
// Kotlin.
//
// WHY THIS FILE IS THE POINT OF THE SLICE. PB-PUSH-9 carries its own warning, verbatim: "A
// façade method can exist while no Android code ever calls it." That is this project's
// standing fifth defect class -- a fence guarding a path production does not take -- written
// into the requirement text, and it has already happened twice in this phase: a fully tested
// FCM sender with ZERO production callers, and a phone that could not obtain an epoch key at
// all because the only code opening the delivering frame was the test simulator. Both were
// green everywhere.
//
// A Go conformance test can prove App.RegisterPushToken works. It cannot prove Kotlin calls
// it, and no test inside mobile/ ever can. So this file fences the CALL:
//
//	the OS entry point EXISTS   -> a FirebaseMessagingService subclass in production Kotlin
//	the OS can REACH it         -> declared in AndroidManifest.xml with the MESSAGING_EVENT filter
//	its callbacks REACH the facade -> onNewToken -> App.RegisterPushToken,
//	                                  onMessageReceived -> App.HandlePushWake
//	and reach NOTHING ELSE      -> no content verb is reachable from onMessageReceived
//
// The last one is PB-PUSH-4's real defect: not a leaky payload (the wake is a constant 78
// bytes over an empty plaintext, ADR-007 B20) but an app that goes and FETCHES content to fill
// the notification in.
//
// THE PHYSICAL-HANDSET GATE STAYS DEFERRED. Nothing here claims real FCM delivery, real Doze,
// a real handset or a real biometric. These are assertions about SOURCE. They cannot prove
// that FCM ever calls onNewToken on a real device -- see
// TestS17_TheFirebaseInitialisationGapIsRecordedRatherThanClaimed at the end of this file,
// which is where the limit of what this gate can know is written down instead of being left
// for a reader to assume away.

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// s17PushPackage is where S17's Kotlin lives. It is named once so a moved package is one edit
// and not a silently-passing gate.
const s17PushPackage = "dev/swarm/phone/push"

// ---------------------------------------------------------------------------
// A tiny Kotlin reader: function bodies and a bounded call graph over them.
//
// It is deliberately crude -- brace matching, not parsing. What it must never do is pass
// because it understood nothing, so every helper below fails loudly on an empty result and
// each assertion states what it could not see.
// ---------------------------------------------------------------------------

var s17FunDecl = regexp.MustCompile(`fun\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(`)

// s17KotlinMain is every production .kt file's contents, concatenated with markers.
func s17KotlinMain(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for _, f := range kotlinFiles(t, kotlinMainRoot(t)) {
		b.WriteString(readFileOrFail(t, f, "PB-PUSH-9"))
		b.WriteString("\n")
	}
	return b.String()
}

// s17Bodies maps every production Kotlin function name to its body text.
//
// A name declared twice (an override in two classes) has its bodies concatenated. That is the
// conservative direction for a REACHABILITY question and the wrong one for an EXCLUSION
// question, so the exclusion assertion below narrows its scope to the push package rather than
// relying on this.
func s17Bodies(t *testing.T) map[string]string {
	t.Helper()
	return s17BodiesIn(s17KotlinMain(t))
}

// s17BodiesIn is the same over an explicit source string, so the walk itself can be driven
// against synthetic Kotlin. A reader that silently understands nothing is the failure mode
// these helpers are most exposed to, and it cannot be measured against the production tree --
// there, a walk that finds nothing and a codebase that does nothing wrong look identical.
func s17BodiesIn(src string) map[string]string {
	out := map[string]string{}
	for _, m := range s17FunDecl.FindAllStringSubmatchIndex(src, -1) {
		name := src[m[2]:m[3]]
		body, ok := s17BodyAt(src, m[1])
		if !ok {
			continue
		}
		out[name] += "\n" + body
	}
	return out
}

// s17BodyAt returns the brace-balanced body that follows the parameter list starting at from.
// s17NamesVerb reports whether src calls the bound facade verb `goName`, in EITHER spelling.
//
// gobind LOWERCASES the first letter when it emits the Java binding -- the generated
// swarmmobile/App.java declares registerPushToken, handlePushWake, deletePushToken, and
// Swarmmobile.java declares newApp -- so NO correct Kotlin call site can contain the Go-cased
// name. Matching only the Go casing made five assertions in this file unsatisfiable by any
// correct implementation: TestS17_TheAppTheServiceUsesIsTheProductionOne failed against S16's
// shipped and correct `Swarmmobile.newApp(...)`. S16's own gate already accepts both
// (s16_wiring_test.go:245); this is that precedent, factored.
func s17NamesVerb(src, goName string) bool {
	lower := strings.ToLower(goName[:1]) + goName[1:]
	return strings.Contains(src, goName+"(") || strings.Contains(src, lower+"(")
}

func s17BodyAt(src string, from int) (string, bool) {
	// depth starts at ONE, not zero: s17FunDecl ends with `\(`, so `from` is already INSIDE
	// the parameter list. Starting at zero sent the outer `)` to -1 and the break condition
	// `depth == 0 && src[i] == ')'` could never hold -- so this returned no body at all for
	// most functions, and a WRONG body (short by the parameter list) for the rest. Six
	// assertions in this file were unable to fire.
	depth, i := 1, from
	// Skip the parameter list.
	for ; i < len(src); i++ {
		switch src[i] {
		case '(':
			depth++
		case ')':
			depth--
		}
		if depth == 0 && src[i] == ')' {
			i++
			break
		}
	}
	// Find the body. Kotlin declares one in TWO shapes and both have to be read: a braced
	// block, and an expression body (`fun f(x) = expr`). The expression form used to return
	// no body at all, so an expression-bodied helper never entered the body map and the
	// reachability walk stopped dead at the call site -- silently satisfying every EXCLUSION
	// assertion in this file, which is the one carrying PB-PUSH-4's security property. It is
	// idiomatic Kotlin, so this was not an exotic bypass; it was the shape a reviewer reads
	// past. Fenced by TestS17_TheReachabilityWalkFollowsExpressionBodiedHelpers.
	for ; i < len(src); i++ {
		if src[i] == '{' {
			break
		}
		// `=` here opens an expression body -- but not `==`/`!=`/`<=`/`>=`, which can appear in
		// a default-free return position (`fun f(): Boolean = a == b`) and whose FIRST `=` is
		// the one that opens the body anyway.
		if src[i] == '=' && (i+1 >= len(src) || src[i+1] != '=') &&
			!strings.ContainsRune("=!<>", rune(src[i-1])) {
			return s17ExprBodyAt(src, i+1)
		}
		if src[i] == '\n' && i+1 < len(src) && src[i+1] != ' ' && src[i+1] != '\t' {
			return "", false
		}
	}
	if i >= len(src) || src[i] != '{' {
		return "", false
	}
	start, depth := i, 0
	for ; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[start : i+1], true
			}
		}
	}
	return "", false
}

// s17ExprBodyAt returns the expression that forms the body of `fun f(...) = <here>`.
//
// It runs to the end of the DECLARATION, not to the end of the line, because both idiomatic
// forms have to read: the one-liner (`= app.roster()`) and the far more common shape in this
// tree, where the `=` ends the line and the expression is a `when`/builder/lambda on the lines
// below (FacadeBridge and LifecycleConvergence are almost entirely this). Stopping at the
// first newline would have read the first form and silently returned NOTHING for the second,
// which is the same class of hole one layer along. So it stops at a newline outside every
// bracket whose next non-blank text begins a new declaration.
//
// Crude in the same way the rest of this reader is crude, and in the same direction: it never
// runs past the declaration it was pointed at.
func s17ExprBodyAt(src string, from int) (string, bool) {
	depth := 0
	for i := from; i < len(src); i++ {
		switch src[i] {
		case '(', '{', '[':
			depth++
		case ')', '}', ']':
			depth--
		case '\n':
			if depth <= 0 && s17StartsDeclaration(src[i+1:]) {
				return src[from:i], strings.TrimSpace(src[from:i]) != ""
			}
		}
	}
	return src[from:], strings.TrimSpace(src[from:]) != ""
}

// s17StartsDeclaration reports whether the next non-blank text opens a new declaration rather
// than continuing the expression above it. A continuation (`.map { }`, `?: x`, `else ->`)
// never starts with one of these; a declaration or the closing brace of the enclosing class
// always does.
func s17StartsDeclaration(rest string) bool {
	rest = strings.TrimLeft(rest, " \t\r\n")
	if rest == "" || strings.HasPrefix(rest, "}") || strings.HasPrefix(rest, "@") {
		return true
	}
	for _, kw := range []string{"fun", "val", "var", "private", "internal", "public",
		"protected", "override", "companion", "object", "class", "init", "constructor"} {
		if strings.HasPrefix(rest, kw+" ") {
			return true
		}
	}
	return false
}

// s17Reachable is the text reachable from entry, following calls up to depth hops.
//
// The depth bound is what keeps this honest in both directions. Unbounded, one helper that
// calls a logger would drag the whole app into the reachable set and the EXCLUSION assertions
// would fail on code that has nothing to do with push. Three hops is enough for
// callback -> helper -> facade, which is the shape a reviewer would accept, and any deeper
// indirection between an OS callback and a facade call is itself worth failing on.
func s17Reachable(t *testing.T, entry string, depth int) (string, bool) {
	t.Helper()
	return s17ReachableIn(s17Bodies(t), entry, depth)
}

func s17ReachableIn(bodies map[string]string, entry string, depth int) (string, bool) {
	root, ok := bodies[entry]
	if !ok {
		return "", false
	}
	seen := map[string]bool{entry: true}
	frontier := []string{entry}
	var b strings.Builder
	b.WriteString(root)
	for hop := 0; hop < depth; hop++ {
		var next []string
		for _, name := range frontier {
			for _, call := range s17CallsIn(bodies[name]) {
				if seen[call] {
					continue
				}
				if body, ok := bodies[call]; ok {
					seen[call] = true
					next = append(next, call)
					b.WriteString("\n")
					b.WriteString(body)
				}
			}
		}
		if len(next) == 0 {
			break
		}
		frontier = next
	}
	return b.String(), true
}

// s17Indent renders a reachable body in a failure message, bounded, so the message names the
// code it walked instead of asking the reader to take the walk on trust.
func s17Indent(body string) string {
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if len(lines) > 40 {
		lines = append(lines[:40], "\t... (truncated)")
	}
	return "\t" + strings.Join(lines, "\n\t")
}

var s17CallSite = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\s*\(`)

func s17CallsIn(body string) []string {
	var out []string
	for _, m := range s17CallSite.FindAllStringSubmatch(s17StripComments(body), -1) {
		out = append(out, m[1])
	}
	return out
}

// s17StripComments removes // and /* */ comments, so a fence cannot be satisfied by a comment
// that mentions the method it is supposed to require a CALL to. This is not hypothetical: the
// method names below appear in doc comments in this very file.
func s17StripComments(src string) string {
	var b strings.Builder
	for i := 0; i < len(src); {
		switch {
		case strings.HasPrefix(src[i:], "//"):
			j := strings.IndexByte(src[i:], '\n')
			if j < 0 {
				return b.String()
			}
			i += j
		case strings.HasPrefix(src[i:], "/*"):
			j := strings.Index(src[i:], "*/")
			if j < 0 {
				return b.String()
			}
			i += j + 2
		default:
			b.WriteByte(src[i])
			i++
		}
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// The reader is fenced against itself, because a reader that understands nothing passes
// everything.
// ---------------------------------------------------------------------------

// TestS17_TheReachabilityWalkFollowsExpressionBodiedHelpers.
//
// DEMONSTRATED, not hypothetical: a content fetch routed through an expression-bodied Kotlin
// helper -- `private fun sessionsFor(app: App) = app.roster()` -- called from the wake
// renderer passed TestS17_TheWakeCallbackReachesNoContentVerb. s17BodyAt looked only for an
// opening brace, so an `= expr` declaration yielded no body at all, the name never entered the
// body map, and the walk stopped at the call site. The forbidden-verb guard then went quiet on
// the one thing it exists to catch. Expression bodies are idiomatic Kotlin and the shape a
// reviewer would read straight past, and this is the guard carrying PB-PUSH-4's actual
// security property rather than a wiring convention.
//
// It is driven against SYNTHETIC Kotlin on purpose. Against the production tree a walk that
// understands nothing and a codebase that does nothing wrong are indistinguishable -- which is
// exactly how this survived. The braced control below is what makes the expression-bodied case
// a measurement of the reader rather than of the fixture.
func TestS17_TheReachabilityWalkFollowsExpressionBodiedHelpers(t *testing.T) {
	const braced = `
class SwarmMessagingService : FirebaseMessagingService() {
    override fun onMessageReceived(message: RemoteMessage) {
        renderWake(phoneOf(this))
    }

    private fun sessionsFor(app: App): String {
        return app.roster()
    }

    private fun renderWake(app: App) {
        notifyUser(sessionsFor(app))
    }
}
`
	// The SAME class with one helper written in the other idiomatic form. Nothing else differs.
	const expressionBodied = `
class SwarmMessagingService : FirebaseMessagingService() {
    override fun onMessageReceived(message: RemoteMessage) {
        renderWake(phoneOf(this))
    }

    private fun sessionsFor(app: App) = app.roster()

    private fun renderWake(app: App) {
        notifyUser(sessionsFor(app))
    }
}
`
	// The form that DOMINATES this tree -- `=` ends the line and the expression is a
	// when/builder/lambda below it (FacadeBridge, ConnectionUi, LifecycleConvergence are almost
	// entirely this). A reader that stopped at the first newline would read the one-liner above
	// and silently return nothing here, which is the same hole one shape along.
	const expressionBodiedOverTwoLines = `
class SwarmMessagingService : FirebaseMessagingService() {
    override fun onMessageReceived(message: RemoteMessage) {
        renderWake(phoneOf(this))
    }

    private fun sessionsFor(app: App): String =
        when (app.isReady()) {
            true -> app.roster()
            else -> ""
        }

    private fun renderWake(app: App) {
        notifyUser(sessionsFor(app))
    }
}
`
	for _, tc := range []struct{ name, src string }{
		{"braced control", braced},
		{"expression body", expressionBodied},
		{"expression body over two lines", expressionBodiedOverTwoLines},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, ok := s17ReachableIn(s17BodiesIn(tc.src), "onMessageReceived", 3)
			if !ok {
				t.Fatalf("the walk found no onMessageReceived body in:\n%s", tc.src)
			}
			if !s17NamesVerb(s17StripComments(body), "Roster") {
				t.Errorf("PB-PUSH-4: the reachability walk does not see App.roster() two hops from "+
					"onMessageReceived when the helper carrying it is written as `%s`.\n"+
					"Every EXCLUSION assertion in this file -- the forbidden content verbs, which are "+
					"the security property PB-PUSH-4 is actually about -- is silently satisfied by any "+
					"fetch routed through a function shape s17BodyAt cannot read. A guard that cannot "+
					"fail is worth less than no guard, because it is reported as coverage.\n"+
					"reachable from onMessageReceived:\n%s", tc.name, s17Indent(body))
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The OS entry point: it exists, and the OS can reach it.
// ---------------------------------------------------------------------------

// TestS17_AFirebaseMessagingServiceExistsInProductionKotlin. Without this class there is no
// onNewToken and no onMessageReceived anywhere, so every facade push verb is a method with no
// caller -- the exact shape PB-PUSH-9's own warning names.
func TestS17_AFirebaseMessagingServiceExistsInProductionKotlin(t *testing.T) {
	src := s17StripComments(s17KotlinMain(t))

	// The service lives in the package the Robolectric tests are written against
	// (android/app/src/test/kotlin/dev/swarm/phone/push). Two packages named push, one per
	// source set, is how a JVM test ends up asserting against a class the app does not ship.
	pkgRoot := filepath.Join(kotlinMainRoot(t), filepath.FromSlash(s17PushPackage))
	if len(kotlinFiles(t, pkgRoot)) == 0 {
		t.Errorf("PB-PUSH-9: there is no production Kotlin under %s. S17's Robolectric tests are "+
			"written against dev.swarm.phone.push; a service in a different package leaves them "+
			"asserting about a class the app does not ship", mustRel(t, pkgRoot))
	}

	if !strings.Contains(src, "FirebaseMessagingService") {
		t.Fatalf("PB-PUSH-9: no class under %s extends FirebaseMessagingService.\n"+
			"That class is the ONLY way an FCM message reaches this app: the relay's sender emits "+
			"a DATA-ONLY message (internal/remote/push/fcm.go), which Android does not render "+
			"itself and hands to the app's service. Without it the phone receives every wake and "+
			"acts on none, and App.RegisterPushToken / App.HandlePushWake are methods nothing "+
			"calls -- which is what this requirement warns about in its own text.",
			mustRel(t, kotlinMainRoot(t)))
	}
	for _, override := range []string{"onNewToken", "onMessageReceived"} {
		if !strings.Contains(src, "fun "+override+"(") {
			t.Errorf("PB-PUSH-9/PB-PUSH-4: production Kotlin declares no %s. %s", override,
				map[string]string{
					"onNewToken": "FCM rotates a token without asking -- on reinstall, on data " +
						"restore, on TTL expiry -- and a phone that does not notice is silently " +
						"unreachable by push forever.",
					"onMessageReceived": "This is where a data-only wake arrives. Without it the " +
						"message is delivered and dropped.",
				}[override])
		}
	}
}

// TestS17_TheMessagingServiceIsDeclaredInTheManifest is the class-(v) fence in its purest
// form. A service Android does not know about is never instantiated: the class compiles, the
// unit tests pass, and the OS never calls a line of it.
func TestS17_TheMessagingServiceIsDeclaredInTheManifest(t *testing.T) {
	manifest := readFileOrFail(t, manifestPath(t), "PB-PUSH-9")

	if !strings.Contains(manifest, "com.google.firebase.MESSAGING_EVENT") {
		t.Errorf("PB-PUSH-9: %s declares no <service> with the "+
			"com.google.firebase.MESSAGING_EVENT intent filter.\n"+
			"Android instantiates a FirebaseMessagingService ONLY through that filter. A subclass "+
			"that is not declared here is dead code with a full unit-test suite -- the fifth "+
			"defect class, and the one this requirement names.", mustRel(t, manifestPath(t)))
	}
	if !strings.Contains(manifest, "POST_NOTIFICATIONS") {
		t.Errorf("PB-RUN-2/PB-PUSH-4: the manifest does not request POST_NOTIFICATIONS. Without " +
			"it every notification on API 33+ is silently dropped, which is the failure mode that " +
			"looks exactly like push not working")
	}
}

// TestS17_TheMessagingServiceIsNotExported. An exported service can be started by any app on
// the device. There is nothing to gain by exporting this one and a wake handler reachable from
// outside is a way to drive the phone's push path from another process.
func TestS17_TheMessagingServiceIsNotExported(t *testing.T) {
	manifest := readFileOrFail(t, manifestPath(t), "PB-PUSH-9")
	idx := strings.Index(manifest, "com.google.firebase.MESSAGING_EVENT")
	if idx < 0 {
		// FAILS rather than skips. A skipped test reads as green in a run summary, and this
		// slice's whole subject is a fence that must not be able to disappear along with the
		// thing it guards (defect class (iii)).
		t.Fatalf("PB-PUSH-9: there is no FCM service to check the export flag on. " +
			"TestS17_TheMessagingServiceIsDeclaredInTheManifest owns the missing declaration; " +
			"this assertion refuses to report success for a property it could not evaluate")
	}
	// The <service ...> element that contains the filter: search backwards for its opening tag.
	open := strings.LastIndex(manifest[:idx], "<service")
	if open < 0 {
		t.Fatalf("PB-PUSH-9: the MESSAGING_EVENT filter is not inside a <service> element")
	}
	element := manifest[open:idx]
	if !strings.Contains(element, `android:exported="false"`) {
		t.Errorf("PB-PUSH-9: the FCM service is declared without android:exported=\"false\":\n%s\n"+
			"Any app on the device can then start it. Android 12+ requires the attribute to be "+
			"explicit, so omitting it is a build failure on some AGP versions and an exported "+
			"service on others", strings.TrimSpace(element))
	}
}

// ---------------------------------------------------------------------------
// The callbacks reach the facade. THIS is the fence PB-PUSH-9 asks for.
// ---------------------------------------------------------------------------

// TestS17_OnNewTokenReachesTheFacadeRegistration.
//
// A method that exists and is never called is worth exactly nothing, and that is the whole of
// PB-PUSH-9's warning. This walks the call graph from the OS callback and requires the facade
// verb to be reachable within three hops -- callback, helper, facade -- with comments stripped
// so a doc comment naming the method cannot satisfy it.
func TestS17_OnNewTokenReachesTheFacadeRegistration(t *testing.T) {
	body, ok := s17Reachable(t, "onNewToken", 3)
	if !ok {
		t.Fatalf("PB-PUSH-9: production Kotlin has no onNewToken body to walk. " +
			"TestS17_AFirebaseMessagingServiceExistsInProductionKotlin owns the missing class; " +
			"this test owns the missing CALL")
	}
	if !s17NamesVerb(s17StripComments(body), "RegisterPushToken") {
		t.Errorf("PB-PUSH-9: nothing reachable from onNewToken calls App.RegisterPushToken.\n"+
			"The rotated token is then delivered to the app and dropped. State.PushToken still "+
			"holds the OLD one, the reconnect path re-registers the dead token, FCM answers "+
			"UNREGISTERED, the relay prunes it -- and the handset is unreachable by push with "+
			"nothing anywhere reporting it.\nreachable from onNewToken:\n%s", s17Indent(body))
	}
}

// TestS17_OnMessageReceivedReachesTheFacadeWakeHandler. Same fence on the other callback: the
// payload has to reach the Go core, because only the core holds the wake key that says whether
// the wake is genuine and the replay coordinate that says whether it is new.
func TestS17_OnMessageReceivedReachesTheFacadeWakeHandler(t *testing.T) {
	body, ok := s17Reachable(t, "onMessageReceived", 3)
	if !ok {
		t.Fatalf("PB-PUSH-9/PB-PUSH-4: production Kotlin has no onMessageReceived body to walk")
	}
	stripped := s17StripComments(body)
	if !s17NamesVerb(stripped, "HandlePushWake") {
		t.Errorf("PB-PUSH-4/PB-PUSH-3: nothing reachable from onMessageReceived calls "+
			"App.HandlePushWake.\nA notification raised without it is raised on the say-so of the "+
			"push provider and the relay: neither holds the wake key, so neither can be "+
			"distinguished from an attacker, and a replayed envelope puts a notification on the "+
			"owner's lock screen whenever the relay likes.\nreachable from onMessageReceived:\n%s",
			s17Indent(body))
	}
	// The payload key the sender actually emits. A rename on either side is a phone that
	// receives every wake and opens none, and neither side's tests can see it.
	if !strings.Contains(stripped, `"e"`) {
		t.Errorf("PB-PUSH-3: nothing reachable from onMessageReceived reads the data key \"e\", " +
			"which is the ONE key internal/remote/push/fcm.go marshalMessage puts in the data " +
			"block")
	}
}

// TestS17_TheWakeCallbackReachesNoContentVerb is PB-PUSH-4's real requirement, as a source
// fact.
//
// "Renders a content-free notification unless the user has authenticated" is satisfied by an
// app that reads the roster, is refused because the content tier is locked, and renders the
// generic string anyway -- and that app decrypts session content the moment it runs on a
// handset the user unlocked five minutes ago. The defect is the FETCH, not the string, so the
// fence is on which verbs are reachable from the wake callback at all.
func TestS17_TheWakeCallbackReachesNoContentVerb(t *testing.T) {
	body, ok := s17Reachable(t, "onMessageReceived", 3)
	if !ok {
		// FAILS rather than skips: this is PB-PUSH-4's headline assertion, and a headline that
		// evaporates when its subject does not exist is defect class (iii) exactly.
		t.Fatalf("PB-PUSH-4: there is no wake callback to walk, so the 'no content fetch while " +
			"locked' property is UNVERIFIED. It is reported as a failure rather than a skip " +
			"because a skipped headline reads as a green run")
	}
	stripped := s17StripComments(body)

	// Every facade verb whose answer is derived from the CONTENT tier.
	// Bare Go names -- s17NamesVerb checks BOTH spellings. Matching only the Go casing made
	// this check unable to fire at all, since gobind lowercases the first letter for Java: it
	// was a guard that could not fail, and it is the guard carrying this requirement's actual
	// security property rather than a wiring convention.
	forbidden := map[string]string{
		"Roster":            "the session roster is the decrypted journal model",
		"Session":           "one session's decrypted model",
		"Peek":              "a server-rendered terminal grid",
		"ReadJournal":       "decrypted journal entries",
		"Outcome":           "a decrypted command reply",
		"TerminalWatch":     "starts a content stream from a locked device",
		"SubscribeJournal":  "starts a content stream from a locked device",
		"SendInput":         "sends keystrokes from a push callback",
		"InstallContentKey": "installs the content key from the wake path, which is the tier split collapsing",
	}
	var found []string
	for verb, why := range forbidden {
		if s17NamesVerb(stripped, verb) {
			found = append(found, verb+" -- "+why)
		}
	}
	sort.Strings(found)
	if len(found) > 0 {
		t.Errorf("PB-PUSH-4/PB-KEY-2: the wake callback reaches %d content verb(s):\n\t%s\n"+
			"The wake arrives with NO USER PRESENT. Fetching content to fill a notification in is "+
			"the reachable defect this requirement exists to stop -- there is nothing in the "+
			"payload to leak (it is a constant 78 bytes over an empty plaintext, ADR-007 B20), so "+
			"the only way content reaches the lock screen is if the app goes and gets it.",
			len(found), strings.Join(found, "\n\t"))
	}
}

// ---------------------------------------------------------------------------
// Initial getToken, and deletion. The two ends of the lifecycle that are not callbacks.
// ---------------------------------------------------------------------------

// TestS17_ProductionKotlinAsksFirebaseForATokenAtLeastOnce.
//
// onNewToken fires on ROTATION. A fresh install that never rotates never gets one, so a client
// that only implements onNewToken registers nothing, ever, on the devices where push matters
// most -- the ones that just installed the app.
//
// WHAT THIS CANNOT SEE, said rather than implied: `FirebaseMessaging.getInstance().token`
// completes in a listener lambda, and the crude reader above does not follow lambdas. So this
// asserts the two facts are in the SAME FILE rather than that one calls the other. The
// callback fences above are the strong ones; this is the weaker guard on the one shape they
// cannot cover.
func TestS17_ProductionKotlinAsksFirebaseForATokenAtLeastOnce(t *testing.T) {
	var withGetToken []string
	for _, f := range kotlinFiles(t, kotlinMainRoot(t)) {
		src := s17StripComments(readFileOrFail(t, f, "PB-PUSH-9"))
		if !s17AsksFirebaseMessagingToken(src) {
			continue
		}
		withGetToken = append(withGetToken, mustRel(t, f))
		if !s17NamesVerb(src, "RegisterPushToken") {
			t.Errorf("PB-PUSH-9: %s asks Firebase for a token and never calls "+
				"App.RegisterPushToken. A token obtained and not registered is the same as no "+
				"token at all", mustRel(t, f))
		}
	}
	if len(withGetToken) == 0 {
		t.Errorf("PB-PUSH-9: no production Kotlin asks Firebase for a token.\n" +
			"The requirement lists 'initial getToken' FIRST and separately from 'onNewToken " +
			"rotation' for a reason: onNewToken fires only on rotation, so an app that implements " +
			"only the callback never registers on a fresh install -- and a fresh install is a " +
			"phone that has just been paired and is about to be backgrounded.")
	}
}

var s17FirebaseMessagingToken = regexp.MustCompile(
	`FirebaseMessaging\s*\.\s*getInstance\s*\(\s*\)\s*\.\s*token\b`,
)

func s17AsksFirebaseMessagingToken(src string) bool {
	return s17FirebaseMessagingToken.MatchString(src)
}

func TestS17_InitialTokenFenceDistinguishesPlayIntegrityTokens(t *testing.T) {
	if !s17AsksFirebaseMessagingToken(`FirebaseMessaging.getInstance().token.addOnSuccessListener {}`) {
		t.Fatal("initial-token fence missed the FirebaseMessaging token request it owns")
	}
	if s17AsksFirebaseMessagingToken(`provider.request(request).token()`) {
		t.Fatal("Play Integrity Standard token acquisition was misclassified as an FCM token request")
	}
}

// s17PushPackagePrefix is the file-path fragment identifying the push package, used to ask
// whether a deletion is reached from OUTSIDE the file that defines it.
const s17PushPackagePrefix = "dev/swarm/phone/push/"

// s17DeclarationFiles maps every production Kotlin function name to the repo-relative files
// declaring it. s17Bodies deliberately loses this -- it concatenates same-named bodies -- and
// the reachability question below is partly about WHERE a caller lives.
func s17DeclarationFiles(t *testing.T) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, f := range kotlinFiles(t, kotlinMainRoot(t)) {
		rel := mustRel(t, f)
		src := readFileOrFail(t, f, "PB-PUSH-9")
		for _, m := range s17FunDecl.FindAllStringSubmatch(src, -1) {
			out[m[1]] = append(out[m[1]], rel)
		}
	}
	return out
}

// s17CallersOf inverts the call graph: for each function name, who calls it.
func s17CallersOf(bodies map[string]string) map[string][]string {
	callers := map[string][]string{}
	for name, body := range bodies {
		for _, call := range s17CallsIn(body) {
			if call == name {
				continue // a self-call is not a caller; recursion is not reachability
			}
			if _, declared := bodies[call]; !declared {
				continue
			}
			callers[call] = append(callers[call], name)
		}
	}
	return callers
}

// s17DeletionSinks are the production functions whose own bodies call App.DeletePushToken.
func s17DeletionSinks(bodies map[string]string) []string {
	var out []string
	for name, body := range bodies {
		if s17NamesVerb(s17StripComments(body), "DeletePushToken") {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// s17ReachedFromOutsideThePushPackage answers the question the fence is actually about: is the
// deletion CALLED, transitively, by code that is not the push package talking to itself.
//
// It returns the chain it found, so a passing run names the path rather than asserting one.
func s17ReachedFromOutsideThePushPackage(
	bodies map[string]string, files map[string][]string, sink string, depth int,
) []string {
	seen := map[string]bool{sink: true}
	frontier := []string{sink}
	callers := s17CallersOf(bodies)
	chain := []string{sink}
	for hop := 0; hop < depth; hop++ {
		var next []string
		for _, name := range frontier {
			for _, caller := range callers[name] {
				if seen[caller] {
					continue
				}
				seen[caller] = true
				next = append(next, caller)
				outside := false
				for _, f := range files[caller] {
					if !strings.Contains(filepath.ToSlash(f), s17PushPackagePrefix) {
						outside = true
					}
				}
				if outside {
					return append(chain, caller)
				}
			}
		}
		if len(next) == 0 {
			return nil
		}
		sort.Strings(next)
		chain = append(chain, next[0])
		frontier = next
	}
	return nil
}

// TestS17_ProductionKotlinDeletesTheTokenOnRevokeOrDisable. Deletion is listed in the
// requirement beside registration. A revoked device that keeps a registered token leaves a
// provider-visible identifier for it in the relay's store and leaves the machine able to wake a
// handset its owner disowned.
//
// THIS FENCE USED TO BE VACUOUS, and it was vacuous in the exact way PB-PUSH-9's own text warns
// about. It required the string `deletePushToken(` to appear ANYWHERE in production Kotlin --
// so it passed on `PushTokens.disable`, a function that carried the call and that NOTHING CALLED
// for the whole of S17 and S18. Its own source comment said so: "[disable] has no caller yet".
// The requirement's warning is "a facade method can exist while no Android code ever calls it";
// the fence written to catch that reproduced it one layer up, on the wrapper instead of on the
// facade method.
//
// SO IT ASKS FOR A CALLER, and for that caller to be outside the push package -- because
// `PushTokens` calling itself would satisfy a bare caller check while the deletion was still
// unreachable from any screen. The walk is the same bounded one the callback fences above use,
// inverted; nothing new was written to answer it.
//
// WHAT IT STILL CANNOT SEE, stated rather than left for a reader to assume closed. It does NOT
// prove a user's tap reaches the deletion. The last two hops of the real chain are a listener
// installed in a PROPERTY INITIALIZER (`private val needsInput = gatedSwitch(...)`) and a panel
// constructed in another property initializer, and neither is a `fun` body -- so a walker built
// on `fun NAME(` cannot cross them, and following them would mean indexing class bodies and
// constructors, which is a different tool from the brace matcher this file is. What is proved is
// strictly stronger than presence and strictly weaker than end-to-end reachability: the deletion
// is called, and it is called by a screen rather than by the push package alone.
func TestS17_ProductionKotlinDeletesTheTokenOnRevokeOrDisable(t *testing.T) {
	bodies := s17Bodies(t)
	sinks := s17DeletionSinks(bodies)
	if len(sinks) == 0 {
		t.Fatalf("PB-PUSH-9: no production Kotlin calls App.DeletePushToken. The verb exists on " +
			"the facade and no screen and no callback reaches it, so 'deletion on revoke/disable' " +
			"is a method with no caller")
	}

	files := s17DeclarationFiles(t)
	var orphaned []string
	for _, sink := range sinks {
		if chain := s17ReachedFromOutsideThePushPackage(bodies, files, sink, 4); chain != nil {
			t.Logf("PB-PUSH-9: DeletePushToken is reached via %s", strings.Join(chain, " <- "))
			return
		}
		orphaned = append(orphaned, sink+" (declared in "+strings.Join(files[sink], ", ")+")")
	}
	t.Errorf("PB-PUSH-9: App.DeletePushToken is CALLED but nothing outside the push package "+
		"reaches the call. The requirement's word is \"deletion on revoke/DISABLE\", and disable "+
		"is a user turning notifications off -- which happens on a screen. A wrapper nothing "+
		"presses is the defect PB-PUSH-9 warns about in its own text, moved one layer up.\n"+
		"function(s) holding the call, with no caller outside %s:\n\t%s",
		s17PushPackagePrefix, strings.Join(orphaned, "\n\t"))
}

// TestS17_TheDeletionFenceFailsOnAnUncalledDisable is the fence on the fence, and it exists
// because the version above replaced one that could not fail.
//
// It is driven against SYNTHETIC Kotlin for the reason
// TestS17_TheReachabilityWalkFollowsExpressionBodiedHelpers already establishes: against the
// production tree a check that understands nothing and a codebase that does nothing wrong are
// indistinguishable, which is exactly how the presence check survived two slices.
//
// `uncalled` is the state of this repository at the commit that introduced this test's
// predecessor -- PushTokens.disable verbatim, with no caller anywhere. The old fence passed on
// it. `called` differs only by a settings panel that presses it.
func TestS17_TheDeletionFenceFailsOnAnUncalledDisable(t *testing.T) {
	const uncalled = `
object PushTokens {
    fun requestInitialToken(context: Context) {
        register(context, "t")
    }

    fun register(context: Context, token: String) {
        phoneOf(context)?.registerPushToken(token)
    }

    fun disable(context: Context) {
        val app = phoneOf(context) ?: return
        app.deletePushToken()
    }
}
`
	const called = uncalled + `
class SettingsSurface {
    private fun onToggled(value: Boolean) {
        reconcileTheToken(value)
    }

    private fun reconcileTheToken(wanted: Boolean) {
        if (!wanted) PushTokens.disable(activity) else PushTokens.requestInitialToken(activity)
    }
}
`
	pushFile := "android/app/src/main/kotlin/" + s17PushPackagePrefix + "PushTokens.kt"
	screenFile := "android/app/src/main/kotlin/dev/swarm/phone/SettingsSurface.kt"

	for _, tc := range []struct {
		name    string
		src     string
		files   map[string][]string
		reached bool
	}{
		{
			name: "uncalled disable, which the presence check passed",
			src:  uncalled,
			files: map[string][]string{
				"requestInitialToken": {pushFile}, "register": {pushFile}, "disable": {pushFile},
			},
			reached: false,
		},
		{
			name: "disable pressed from a settings panel",
			src:  called,
			files: map[string][]string{
				"requestInitialToken": {pushFile}, "register": {pushFile}, "disable": {pushFile},
				"onToggled": {screenFile}, "reconcileTheToken": {screenFile},
			},
			reached: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bodies := s17BodiesIn(tc.src)
			sinks := s17DeletionSinks(bodies)
			if len(sinks) != 1 || sinks[0] != "disable" {
				t.Fatalf("the sink finder did not locate disable; it found %v", sinks)
			}
			chain := s17ReachedFromOutsideThePushPackage(bodies, tc.files, "disable", 4)
			if tc.reached && chain == nil {
				t.Errorf("PB-PUSH-9: the fence reports a disable that a settings panel calls as "+
					"unreached, so it would fail on a correct implementation -- which is how a "+
					"fence gets deleted rather than fixed.\nsource:\n%s", tc.src)
			}
			if !tc.reached && chain != nil {
				t.Errorf("PB-PUSH-9: the fence reports an UNCALLED PushTokens.disable as reached "+
					"via %v. That is the state this repository was in for two slices, and the "+
					"presence check it replaced passed on it: a facade method with no caller, "+
					"reported as coverage of \"deletion on revoke/disable\".\nsource:\n%s",
					chain, tc.src)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// PB-PUSH-4's platform half: lock-screen redaction and channel privacy.
// ---------------------------------------------------------------------------

// TestS17_TheNotificationAndItsChannelAreLockScreenSecret.
//
// The requirement names both, and they are two different settings that fail differently. The
// CHANNEL's lockscreenVisibility is what the user's system settings show and what applies when
// the app has not set a per-notification value; the NOTIFICATION's visibility is what applies
// to that notification. VISIBILITY_SECRET means the notification does not appear on the lock
// screen at all; VISIBILITY_PRIVATE shows a redacted line, which is still a line that says an
// agent wants something.
//
// It is checked as source text because it is platform CONFIGURATION -- there is no behaviour to
// drive without a handset, and PB-E2E-5 is deferred.
func TestS17_TheNotificationAndItsChannelAreLockScreenSecret(t *testing.T) {
	src := s17StripComments(s17KotlinMain(t))

	if !strings.Contains(src, "createNotificationChannel") && !strings.Contains(src, "NotificationChannel(") {
		t.Errorf("PB-PUSH-4: production Kotlin creates no notification channel. On API 26+ a " +
			"notification posted to a channel that does not exist is DROPPED, so this is not a " +
			"policy omission -- it is a notification the user never sees")
	}
	if !strings.Contains(src, "setLockscreenVisibility") {
		t.Errorf("PB-PUSH-4: no notification channel sets lockscreenVisibility. " +
			"'Notification-channel privacy is set' is the requirement's own wording")
	}
	if !strings.Contains(src, "VISIBILITY_SECRET") {
		t.Errorf("PB-PUSH-4: VISIBILITY_SECRET appears nowhere in production Kotlin. " +
			"VISIBILITY_PRIVATE redacts the text and still puts a Swarm notification on the lock " +
			"screen of a device the owner may not be holding; SECRET is the one that does not")
	}
	if strings.Contains(src, "VISIBILITY_PUBLIC") {
		t.Errorf("PB-PUSH-4: production Kotlin sets VISIBILITY_PUBLIC somewhere. That renders the " +
			"notification's full text on the lock screen with no authentication")
	}
}

// TestS17_TheNotificationTextIsNotBuiltByInterpolation is the leak fence one level below the
// visibility flags, because a SECRET notification still appears in the shade the moment the
// user unlocks, and the text is what a screenshot carries.
//
// A Kotlin string template is the shape this defect takes: setContentText("Session $id needs
// input") is one edit away from setContentText("Swarm has an update for you") and reads almost
// the same in review.
//
// WHAT IT CANNOT SEE, RECORDED RATHER THAN LEFT TO BE REDISCOVERED: it inspects the DIRECT
// argument text of the four setters, so text built in a helper and passed in by name
// (`setContentText(wakeLine(session))`) is invisible to it. Closing that needs the argument
// resolved back to its definition, which is dataflow rather than the brace matching this file
// does, and it is deliberately NOT attempted here -- a half-done version would report as
// coverage. It is bounded by what sits above it rather than left open: whatever a helper
// interpolates has to come from somewhere, and the two somewheres on this path are the payload
// (content-free by construction, ADR-007 B20) and a content read, which
// TestS17_TheWakeCallbackReachesNoContentVerb forbids by REACHABILITY -- through helpers,
// including expression-bodied ones, since
// TestS17_TheReachabilityWalkFollowsExpressionBodiedHelpers.
func TestS17_TheNotificationTextIsNotBuiltByInterpolation(t *testing.T) {
	var offenders []string
	setters := 0
	for _, f := range kotlinFiles(t, kotlinMainRoot(t)) {
		src := s17StripComments(readFileOrFail(t, f, "PB-PUSH-4"))
		for _, setter := range []string{"setContentTitle(", "setContentText(", "setSubText(", "setTicker("} {
			for _, arg := range s17ArgsOf(src, setter) {
				setters++
				if strings.Contains(arg, "$") {
					offenders = append(offenders, mustRel(t, f)+": "+setter+arg+")")
				}
			}
		}
	}
	// NON-VACUITY, first, because "no interpolated text" is satisfied perfectly by an app that
	// builds no notification at all -- which is the state this file is red in today and would
	// be the state after any regression that removed the notification.
	if setters == 0 {
		t.Fatalf("PB-PUSH-4: production Kotlin sets no notification text anywhere, so this fence "+
			"measures nothing. The app receives a push and renders NOTHING, which fails the "+
			"requirement in the other direction.\nsearched: %s", mustRel(t, kotlinMainRoot(t)))
	}
	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Errorf("PB-PUSH-4: %d notification text(s) are built by string interpolation:\n\t%s\n"+
			"Whatever is interpolated came from somewhere, and the only somewheres on this path "+
			"are the payload (content-free by construction) or a content read (forbidden while "+
			"locked). The text is a CONSTANT: swarmmobile.WakeNotificationText, or a string "+
			"resource with no arguments.", len(offenders), strings.Join(offenders, "\n\t"))
	}
}

// s17ArgsOf returns the parenthesised argument text of every call to name in src.
func s17ArgsOf(src, name string) []string {
	var out []string
	for i := 0; ; {
		j := strings.Index(src[i:], name)
		if j < 0 {
			return out
		}
		start := i + j + len(name)
		depth, k := 1, start
		for ; k < len(src) && depth > 0; k++ {
			switch src[k] {
			case '(':
				depth++
			case ')':
				depth--
			}
		}
		if depth != 0 {
			return out
		}
		out = append(out, src[start:k-1])
		i = k
	}
}

// ---------------------------------------------------------------------------
// The two things this gate CANNOT know, written down instead of assumed away.
// ---------------------------------------------------------------------------

// TestS17_TheAppTheServiceUsesIsTheProductionOne.
//
// Every fence above is satisfied by a service that calls App.RegisterPushToken on an App it
// constructed for itself, or on one a test injected. The facade is only real if
// swarmmobile.NewApp is called by production Kotlin -- it is the single constructor, it takes
// the KeyCustody, and ADR-007 B18(c) makes it fail closed without one.
//
// THIS IS EXPECTED TO FAIL FOR A REASON THAT IS NOT S17's. An independent review found, and it
// is confirmed, that there is NO production wiring on the Android side at all: no file I/O
// anywhere under android/app/src/main/, no KeyCustody implementation outside tests, and nothing
// that constructs swarmmobile.NewApp. That is recorded as an S16 blocker. It is fenced here
// anyway, and named, because S17's whole subject is a call that production must make -- and a
// slice that shipped a FirebaseMessagingService talking to an App nobody builds would satisfy
// every other test in this file while push did not exist on a handset.
func TestS17_TheAppTheServiceUsesIsTheProductionOne(t *testing.T) {
	src := s17StripComments(s17KotlinMain(t))
	if !s17NamesVerb(src, "NewApp") {
		t.Errorf("PB-PUSH-9/PB-KEY-9: no production Kotlin calls Swarmmobile.newApp.\n"+
			"BLOCKED ON S16, NOT ON THIS SLICE: android/app/src/main/ has no key-provider "+
			"implementation and constructs no App, so there is no phone for a push callback to "+
			"call into. Recorded so that a green S17 is never read as 'push works on a handset' "+
			"while the facade has no production instance at all.\nproduction Kotlin searched: %s",
			mustRel(t, kotlinMainRoot(t)))
	}
}

// TestS17_TheFirebaseInitialisationGapIsRecordedRatherThanClaimed.
//
// This is the honest boundary of the whole slice and it is asserted so that it cannot be
// forgotten. FirebaseMessagingService is only ever invoked if FirebaseApp initialises, and
// FirebaseApp initialises from google-services.json processed by the com.google.gms
// .google-services plugin. THERE IS NO GOOGLE ACCOUNT IN THIS PROJECT, so that file cannot
// exist here and must not be faked -- a fabricated one would produce an app that initialises
// against a project id nobody owns.
//
// So: the Firebase dependency must be declared (or the service cannot compile), and the
// absence of a real project must be RECORDED in the module rather than discovered by whoever
// first installs the APK and wonders why no push ever arrives. That is PB-E2E-5, which is
// deferred under section 13 and which this slice does not close and does not claim.
func TestS17_TheFirebaseInitialisationGapIsRecordedRatherThanClaimed(t *testing.T) {
	build := readFileOrFail(t, moduleBuildFile(t), "PB-PUSH-9")

	if !strings.Contains(build, "firebase-messaging") {
		t.Errorf("PB-PUSH-9: %s declares no firebase-messaging dependency, so a "+
			"FirebaseMessagingService subclass cannot compile", mustRel(t, moduleBuildFile(t)))
	}
	if strings.Contains(build, "google-services") &&
		!strings.Contains(build, "PB-E2E-5") {
		t.Errorf("PB-PUSH-9/PB-E2E-5: %s applies the google-services plugin without a note "+
			"naming PB-E2E-5. That plugin requires a google-services.json from a real Firebase "+
			"project; there is no Google account in this project, so a build that appears to be "+
			"wired for delivery and is not must SAY SO where the build is read",
			mustRel(t, moduleBuildFile(t)))
	}
}
