package gate

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"unicode"
)

// FAILING-FIRST (TDD RED, GG-5) for agents-tracker-7j4b: a facade verb wired onto a click
// listener, where it runs Go network I/O on the Android main thread.
//
// THE DEFECT THIS FENCES. `PhoneSurface.invoke` used to call the verb inside the listener. A
// command verb resolves its destination through `sendContext`, which is `resolveSend(a.awaitConn)`
// -- and awaitConn "polls for up to five seconds" (mobile/commands.go:513-524, mobile/relay.go:149)
// before the relay append. So every command tap froze the UI for a round trip, and a tap issued
// while the link was reconnecting froze it for about five seconds, which is an ANR dialog.
//
// WHY A GATE AND NOT A UNIT TEST, which is the whole reason this file exists. Three independent
// blindnesses stack on this path and no ordinary Robolectric test closes any of them:
//
//   - `NetworkOnMainThreadException` is raised by Android's own StrictMode-backed socket
//     implementation. Go opens its sockets itself, below the JVM, so the platform detector has
//     never seen a single byte this app sends and never will.
//   - Robolectric is single-threaded, so "did this block the looper" is not a question its
//     runtime can answer.
//   - `PhoneRuntime.phone()` answers `Unavailable` on every JVM run, so `invoke`'s `Ready`
//     branch -- the only branch on which a verb runs at all -- is structurally unreachable in
//     the unit suite. 640 unit tests pass over this defect not because their assertions are weak
//     but because the code is not executable on the machine that runs them.
//
// What is left is the SOURCE, and this file reads it.
//
// THE TWO PLANES ARE DERIVED FROM GO, NOT LISTED HERE, and that is the part that makes this a
// fence rather than a snapshot. `mobile/commands.go` has exactly two send-context policies --
// `sendContext` (waits, via awaitConn) and `liveSendContext` (takes the connection as it stands,
// ADR-007 D7) -- so which verbs may block is a fact about the Go side. A verb added there and
// wired here on the wrong plane fails below without anyone having remembered to edit a list.
//
// WHAT THIS FILE DOES NOT COVER, stated rather than left to be assumed away, because this
// package has shipped an overclaiming check before:
//
//   - THE RENDER PATH. `PhoneSurface.watch`/`unwatch` call `App.TerminalWatch`/`TerminalUnwatch`,
//     which reach awaitConn through `unsignedCommandAt` -- and they are called from `renderReady`
//     and from `release`, on the main thread, with no tap involved. That is a real instance of
//     the same defect and it is FILED AS agents-tracker-jx1x rather than fixed here: `release`'s
//     unwatch has a documented ordering requirement (it must precede the socket close, so the
//     machine's render work is withdrawn while there is still a socket to withdraw it over), and
//     `watch`'s `watching` latch is a state machine no test here can execute. The exemption
//     is enumerated in [s25RenderPathExemptions] and asserted NOT TO GROW, so it can be paid off
//     or argued with, but not quietly extended.
//   - `App.Start`, checked and found not to apply: it spawns `a.run(ctx)` on a goroutine and
//     returns (mobile/app.go:266-284), so the call itself does not block.
//   - Other surfaces. `PairingSurface` has click paths of its own. `App.BeginPairing` was read
//     and does not dial -- its own comment records that `join()` is "the only thing that dials"
//     -- but nothing here has audited the rest of that flow.
//   - It reads SOURCE. That PhoneSurface's `Ready` branch really reaches the dispatcher at
//     runtime is not established by any test in this repository; see VerbDispatchTest's own
//     header. The join is owed to the hardware run.
//
// Every scan below carries a negative control that feeds a perturbed source to the SAME function
// the real assertion calls, because a control that rebuilds the comparison inline proves
// something about the copy and nothing about the assertion.

// ---------------------------------------------------------------------------
// The subject.
// ---------------------------------------------------------------------------

func s25SurfacePath(t *testing.T) string {
	return filepath.Join(appModule(t), "src", "main", "kotlin", "dev", "swarm", "phone", "PhoneSurface.kt")
}

// s25SurfaceCode is PhoneSurface.kt with its comments stripped.
//
// The strip is not cosmetic. This file's whole subject is discussed at length in that file's
// KDoc -- "app.kill", "awaitConn", "SendPlane.COMMAND" all appear in prose -- and a fence a
// comment can satisfy is a fence the next thorough comment turns off. `kotlinCodeOnly` is the
// package's existing answer to exactly that failure, which it has already had once.
func s25SurfaceCode(t *testing.T) string {
	t.Helper()
	return kotlinCodeOnly(readFileOrFail(t, s25SurfacePath(t),
		"agents-tracker-7j4b: the surface that wires the action controls"))
}

// s25RenderPathExemptions are the functions in PhoneSurface.kt that still reach a waiting verb
// from the main thread, each one a known open defect rather than a permitted pattern.
//
// IT MUST SHRINK, NOT GROW. The assertion over it is an equality, so paying one off is a green
// edit and adding a third is a red one that has to be argued for in review.
var s25RenderPathExemptions = map[string]string{
	"watch":   "agents-tracker-jx1x: TerminalWatch from renderReady reaches awaitConn",
	"unwatch": "agents-tracker-jx1x: TerminalUnwatch from release, which must precede the socket close",
}

// ---------------------------------------------------------------------------
// Which verbs may wait, derived from the facade rather than listed.
// ---------------------------------------------------------------------------

// s25SendPolicy names the two destination policies in mobile/commands.go. A verb that reaches
// the first can be parked in awaitConn for five seconds; a verb that reaches only the second
// cannot, and ADR-007 D7 requires that it never be made to.
const (
	s25WaitingPolicy = "sendContext"
	s25LivePolicy    = "liveSendContext"
)

// s25VerbFloor is the "cannot pass by measuring nothing" floor on the DERIVATION. The facade
// routes six exported verbs through the waiting policy today; a run that derived materially
// fewer has stopped walking the call graph rather than discovered a smaller facade, and would
// then report a clean surface over an empty question.
const s25VerbFloor = 4

// s25LiveFloor is the same idea for the live plane, which carries SendInput, Paste, Resize and
// Interrupt.
const s25LiveFloor = 3

// s25AppCallGraph is the one-hop call graph over `(*App)` methods in the bound facade.
//
// IT FOLLOWS METHOD VALUES AND NOT ONLY CALLS, which is load-bearing and is the trap
// mobile/s11r3_livereach_test.go documents having fallen into: `sendContext` does not write
// `a.awaitConn()`, it writes `a.resolveSend(a.awaitConn)` and hands the policy over as a
// FUNCTION VALUE. A walker that recorded only call expressions would see no edge at all and
// would report that nothing waits. So every selector on the receiver counts as an edge,
// whether it is called or merely named.
func s25AppCallGraph(t *testing.T) map[string]map[string]bool {
	t.Helper()
	dir := facadeDir(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("agents-tracker-7j4b: cannot read the bound facade at %s: %v", mustRel(t, dir), err)
	}
	fset := token.NewFileSet()
	graph := map[string]map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatalf("agents-tracker-7j4b: parse %s: %v", name, err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 || fn.Body == nil {
				continue
			}
			if receiverTypeName(fn.Recv.List[0].Type) != "App" {
				continue
			}
			recv := ""
			if names := fn.Recv.List[0].Names; len(names) == 1 {
				recv = names[0].Name
			}
			if recv == "" || recv == "_" {
				continue
			}
			if graph[fn.Name.Name] == nil {
				graph[fn.Name.Name] = map[string]bool{}
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == recv {
					graph[fn.Name.Name][sel.Sel.Name] = true
				}
				return true
			})
		}
	}
	return graph
}

// s25Reaches is the set of `(*App)` methods that can reach target through the graph.
func s25Reaches(graph map[string]map[string]bool, target string) map[string]bool {
	reaching := map[string]bool{}
	// Reverse the edges once, then flood from the target.
	callers := map[string][]string{}
	for from, tos := range graph {
		for to := range tos {
			callers[to] = append(callers[to], from)
		}
	}
	queue := []string{target}
	for len(queue) > 0 {
		at := queue[0]
		queue = queue[1:]
		for _, from := range callers[at] {
			if reaching[from] {
				continue
			}
			reaching[from] = true
			queue = append(queue, from)
		}
	}
	return reaching
}

// s25KotlinName is the name gobind gives a bound method on the Java side: the Go name with its
// first rune lowercased.
func s25KotlinName(goName string) string {
	if goName == "" {
		return ""
	}
	r := []rune(goName)
	r[0] = unicode.ToLower(r[0])
	return string(r)
}

// s25Planes derives the two verb sets. A verb that reaches BOTH policies counts as WAITING:
// `ReleaseControl` flushes buffered keystrokes on the live path and then seals a signed
// command, so it can be parked in awaitConn like any other command.
func s25Planes(t *testing.T) (waiting, live map[string]bool) {
	t.Helper()
	graph := s25AppCallGraph(t)
	reachWaiting := s25Reaches(graph, s25WaitingPolicy)
	reachLive := s25Reaches(graph, s25LivePolicy)
	waiting, live = map[string]bool{}, map[string]bool{}
	for name := range graph {
		if !ast.IsExported(name) {
			continue
		}
		switch {
		case reachWaiting[name]:
			waiting[s25KotlinName(name)] = true
		case reachLive[name]:
			live[s25KotlinName(name)] = true
		}
	}
	return waiting, live
}

// ---------------------------------------------------------------------------
// Reading Kotlin structure as text.
// ---------------------------------------------------------------------------

// s25Span is a half-open region of the scanned source.
type s25Span struct{ start, end int }

// s25Bodies returns the body of every construct opened by `opener`, which must end with the
// bracket that opens the body. Brackets inside string literals are skipped: `kotlinCodeOnly`
// leaves literals intact by design, and this file's subject includes a control whose verb
// appends "\r".
func s25Bodies(code, opener string, open, closing byte) []s25Span {
	var out []s25Span
	for at := 0; ; {
		i := strings.Index(code[at:], opener)
		if i < 0 {
			return out
		}
		i += at
		if !s25WholeToken(code, i, opener) {
			at = i + 1
			continue
		}
		start := i + len(opener) // just past the opening bracket
		depth := 1
		j := start
		for ; j < len(code) && depth > 0; j++ {
			switch code[j] {
			case '"':
				j++
				for ; j < len(code); j++ {
					if code[j] == '\\' {
						j++
						continue
					}
					if code[j] == '"' {
						break
					}
				}
			case open:
				depth++
			case closing:
				depth--
			}
		}
		if depth != 0 {
			return out // unbalanced; the floors below turn this into a loud failure
		}
		out = append(out, s25Span{start: start, end: j - 1})
		at = j
	}
}

// s25WholeToken rejects a match that is the tail of a longer identifier, or a declaration
// rather than a use.
//
// BOTH HALVES ARE THINGS THIS FILE GOT WRONG FIRST TIME. `Press(` is a substring of
// `confirmThenPress(` and of `dispatchPress(`, so without the first check the plane scan
// reported four planeless presses that do not exist; and `private class Press(` is the type's
// own declaration, whose parameter list of course names no plane.
func s25WholeToken(code string, at int, opener string) bool {
	if at > 0 {
		prev := rune(code[at-1])
		if unicode.IsLetter(prev) || unicode.IsDigit(prev) || prev == '_' {
			return false
		}
	}
	if !strings.HasSuffix(opener, "(") {
		return true
	}
	before := strings.TrimRight(code[:at], " \t\n")
	return !strings.HasSuffix(before, "class")
}

// s25CallsIn reports which of `verbs` are called on some receiver inside the span.
func s25CallsIn(code string, span s25Span, verbs map[string]bool) []string {
	body := code[span.start:span.end]
	var found []string
	for verb := range verbs {
		if s25CallsVerb(body, verb) {
			found = append(found, verb)
		}
	}
	sort.Strings(found)
	return found
}

// s25CallsVerb matches `.verb(` with optional space, which is how a bound verb is invoked on a
// receiver. It deliberately matches by NAME and not by receiver type, the same permissive
// direction boundverbledger_test.go records: this over-reports a call, which for a fence that
// demands the call be on a lane is the safe way to be wrong.
func s25CallsVerb(body, verb string) bool {
	for at := 0; ; {
		i := strings.Index(body[at:], "."+verb)
		if i < 0 {
			return false
		}
		i += at
		rest := strings.TrimLeft(body[i+len(verb)+1:], " \t\n")
		// `.kill(` is a call; `.killConfirmation` is a different symbol that starts the same way.
		if strings.HasPrefix(rest, "(") {
			return true
		}
		at = i + 1
	}
}

// s25EnclosingFunction names the function a given offset sits in, by the nearest `fun NAME`
// before it.
//
// IT IS A BACKWARDS SCAN AND NOT A BODY MAP, deliberately. Kotlin functions in this file come
// in both forms -- `fun watch(...) { ... }` and the expression-bodied `fun takeControlOf(...) =
// Press(...)` -- and computing a body span for the second means deciding where an expression
// ends, which is a parser. Naming the nearest enclosing declaration needs neither, and the only
// thing the name is used for is the ledger lookup and the message.
func s25EnclosingFunction(code string, at int) string {
	i := strings.LastIndex(code[:at], "fun ")
	if i < 0 {
		return ""
	}
	rest := code[i+len("fun "):]
	end := strings.IndexAny(rest, "(<")
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:end])
}

// s25CallSite is one call to a bound verb in the scanned source.
type s25CallSite struct {
	verb string
	at   int
	fn   string
}

// s25CallSitesOf finds every call to any of `verbs`, with the function each one sits in.
func s25CallSitesOf(code string, verbs map[string]bool) []s25CallSite {
	var out []s25CallSite
	for verb := range verbs {
		for at := 0; ; {
			i := strings.Index(code[at:], "."+verb)
			if i < 0 {
				break
			}
			i += at
			at = i + 1
			rest := strings.TrimLeft(code[i+len(verb)+1:], " \t\n")
			// `.kill(` is a call; `.killConfirmation` is a different symbol that starts the same.
			if !strings.HasPrefix(rest, "(") {
				continue
			}
			out = append(out, s25CallSite{verb: verb, at: i, fn: s25EnclosingFunction(code, i)})
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].at < out[b].at })
	return out
}

// ---------------------------------------------------------------------------
// 1. No facade verb runs inside a click listener.
// ---------------------------------------------------------------------------

// TestS25_NoFacadeVerbRunsInsideAClickListener is the literal acceptance criterion: something
// that prevents a future verb being wired back onto the click listener.
func TestS25_NoFacadeVerbRunsInsideAClickListener(t *testing.T) {
	code := s25SurfaceCode(t)
	waiting, live := s25Planes(t)
	all := map[string]bool{}
	for v := range waiting {
		all[v] = true
	}
	for v := range live {
		all[v] = true
	}
	if len(waiting) < s25VerbFloor {
		t.Fatalf("agents-tracker-7j4b: derived only %d waiting verbs (%v) from %s, below the floor "+
			"of %d. The call-graph walk has stopped following the facade, so every assertion in "+
			"this file is being made over an empty question",
			len(waiting), sortedKeys(waiting), mustRel(t, facadeDir(t)), s25VerbFloor)
	}
	if len(live) < s25LiveFloor {
		t.Fatalf("agents-tracker-7j4b: derived only %d live verbs (%v), below the floor of %d",
			len(live), sortedKeys(live), s25LiveFloor)
	}

	if bad := s25VerbsInListeners(code, all); len(bad) > 0 {
		t.Errorf("agents-tracker-7j4b: %s\n\n"+
			"A facade verb called inside a click listener runs on the Android main thread. Every "+
			"verb on this surface crosses JNI into a Go network call, and a command verb resolves "+
			"through sendContext -> awaitConn, which polls for up to FIVE SECONDS before it even "+
			"appends to the relay (mobile/commands.go:513-524). That is an ANR dialog on the tap.\n"+
			"Nothing else in this repository can catch it: NetworkOnMainThreadException never "+
			"fires for a socket Go opened, Robolectric is single-threaded, and PhoneRuntime.phone() "+
			"answers Unavailable on every JVM run so the branch that would execute the verb is "+
			"unreachable in the unit suite.\n"+
			"The listener may plan the press -- read the fields, apply the model's refusals -- and "+
			"must hand the verb itself to VerbDispatch.",
			strings.Join(bad, "\n"))
	}
}

// s25VerbsInListeners is the assertion, factored out so the negative control can feed it a
// perturbed source rather than rebuild the comparison.
func s25VerbsInListeners(code string, verbs map[string]bool) []string {
	var bad []string
	for _, span := range s25Bodies(code, "setOnClickListener {", '{', '}') {
		for _, verb := range s25CallsIn(code, span, verbs) {
			bad = append(bad, fmt.Sprintf("  a click listener calls the facade verb %q directly", verb))
		}
	}
	return bad
}

func TestS25_ListenerScanDiscriminates(t *testing.T) {
	verbs := map[string]bool{"kill": true, "launch": true}
	clean := `setOnClickListener { control -> confirmThenPress(control, ask(), plan) }`
	if bad := s25VerbsInListeners(clean, verbs); len(bad) != 0 {
		t.Errorf("the listener scan reported a clean listener: %v", bad)
	}
	wired := `setOnClickListener { app.kill(session) }`
	if bad := s25VerbsInListeners(wired, verbs); len(bad) == 0 {
		t.Error("the listener scan passed a verb wired straight onto the click listener, which " +
			"is the whole defect it exists to catch")
	}
	nested := `setOnClickListener { view -> if (ready) { app.launch(spec) } }`
	if bad := s25VerbsInListeners(nested, verbs); len(bad) == 0 {
		t.Error("the listener scan does not see through a nested block, so a verb one `if` deep " +
			"defeats it")
	}
	notACall := `setOnClickListener { show(detail.killConfirmation) }`
	if bad := s25VerbsInListeners(notACall, verbs); len(bad) != 0 {
		t.Errorf("the listener scan matched a symbol that merely starts with a verb name: %v", bad)
	}
}

// ---------------------------------------------------------------------------
// 2. Every press declares a plane, and the plane matches the verb's own.
// ---------------------------------------------------------------------------

// TestS25_EveryPressDeclaresThePlaneItsVerbResolvesThrough fences BOTH directions, and both are
// real failures rather than one rule and its mirror.
//
// A WAITING VERB ON THE LIVE LANE would put awaitConn's five-second poll in front of the
// keystrokes -- the queue on live input ADR-007 D7 makes structurally impossible one layer down,
// reintroduced from above.
//
// A LIVE VERB ON THE COMMAND LANE is the same defect wearing the other hat: the keystroke now
// waits behind whatever command is in awaitConn, which is exactly what two lanes exist to stop.
func TestS25_EveryPressDeclaresThePlaneItsVerbResolvesThrough(t *testing.T) {
	code := s25SurfaceCode(t)
	waiting, live := s25Planes(t)

	presses := s25Bodies(code, "Press(", '(', ')')
	if len(presses) < s25PressFloor {
		t.Fatalf("agents-tracker-7j4b: found only %d Press( declarations in %s, below the floor of "+
			"%d. The surface carries six action controls; a scan that finds fewer has stopped "+
			"parsing and is asserting over nothing",
			len(presses), mustRel(t, s25SurfacePath(t)), s25PressFloor)
	}
	if bad := s25MisplanedPresses(code, presses, waiting, live); len(bad) > 0 {
		t.Errorf("agents-tracker-7j4b: %s\n\n"+
			"mobile/commands.go has exactly two destination policies and the difference IS the "+
			"requirement. sendContext waits on awaitConn for up to five seconds, which is right "+
			"for a command -- idempotent, queued by design -- and exactly wrong for a keystroke. "+
			"liveSendContext takes the connection as it stands so a keystroke fails immediately "+
			"and is recorded undelivered instead (ADR-007 D7). The two planes take separate lanes "+
			"so neither can ever wait on the other; a press declared on the wrong one puts them "+
			"back on the same queue.",
			strings.Join(bad, "\n"))
	}
}

// s25PressFloor is the scan floor for the surface's declared presses.
const s25PressFloor = 5

func s25MisplanedPresses(code string, presses []s25Span, waiting, live map[string]bool) []string {
	var bad []string
	for _, span := range presses {
		body := code[span.start:span.end]
		declaresCommand := strings.Contains(body, "SendPlane.COMMAND")
		declaresLive := strings.Contains(body, "SendPlane.LIVE")
		switch {
		case !declaresCommand && !declaresLive:
			bad = append(bad, "  a Press declares no SendPlane at all, so nothing says which lane "+
				"its verb may wait on")
			continue
		case declaresCommand && declaresLive:
			bad = append(bad, "  a Press declares both planes, so which lane it takes is ambiguous")
			continue
		}
		for _, verb := range s25CallsIn(code, span, waiting) {
			if !declaresCommand {
				bad = append(bad, fmt.Sprintf(
					"  %q resolves through sendContext, which polls awaitConn for up to five "+
						"seconds, but its Press is declared SendPlane.LIVE", verb))
			}
		}
		for _, verb := range s25CallsIn(code, span, live) {
			if !declaresLive {
				bad = append(bad, fmt.Sprintf(
					"  %q is a LIVE-ONLY verb (ADR-007 D7) but its Press is declared "+
						"SendPlane.COMMAND, where a five-second awaitConn poll can sit in front "+
						"of it", verb))
			}
		}
	}
	return bad
}

func TestS25_PlaneScanDiscriminates(t *testing.T) {
	waiting := map[string]bool{"kill": true}
	live := map[string]bool{"sendInput": true}

	good := `Press(SendPlane.COMMAND, verb = { app -> app.kill(target) })` +
		`Press(SendPlane.LIVE, verb = { app -> app.sendInput(target, bytes) })`
	if bad := s25MisplanedPresses(good, s25Bodies(good, "Press(", '(', ')'), waiting, live); len(bad) != 0 {
		t.Errorf("the plane scan reported correctly planed presses: %v", bad)
	}

	waitingOnLive := `Press(SendPlane.LIVE, verb = { app -> app.kill(target) })`
	if bad := s25MisplanedPresses(waitingOnLive, s25Bodies(waitingOnLive, "Press(", '(', ')'), waiting, live); len(bad) == 0 {
		t.Error("the plane scan passed a waiting verb declared on the live lane, which puts " +
			"awaitConn's five-second poll in front of the keystrokes")
	}

	liveOnCommand := `Press(SendPlane.COMMAND, verb = { app -> app.sendInput(target, bytes) })`
	if bad := s25MisplanedPresses(liveOnCommand, s25Bodies(liveOnCommand, "Press(", '(', ')'), waiting, live); len(bad) == 0 {
		t.Error("the plane scan passed a live-only verb declared on the command lane, where a " +
			"command in awaitConn can delay it")
	}

	noPlane := `Press(verb = { app -> app.kill(target) })`
	if bad := s25MisplanedPresses(noPlane, s25Bodies(noPlane, "Press(", '(', ')'), waiting, live); len(bad) == 0 {
		t.Error("the plane scan passed a Press that declares no plane at all")
	}
}

// ---------------------------------------------------------------------------
// 3. Every waiting verb on this surface is either dispatched or ledgered.
// ---------------------------------------------------------------------------

// TestS25_EveryWaitingVerbIsDispatchedOrLedgered closes the hole the first two scans leave: a
// waiting verb called from neither a listener nor a Press -- from `render`, say, where the
// original defect also lives and where nothing above would look.
func TestS25_EveryWaitingVerbIsDispatchedOrLedgered(t *testing.T) {
	code := s25SurfaceCode(t)
	waiting, _ := s25Planes(t)
	stray := s25StrayWaitingCalls(code, waiting)

	var unledgered []string
	for fn, verbs := range stray {
		if _, ok := s25RenderPathExemptions[fn]; ok {
			continue
		}
		sort.Strings(verbs)
		unledgered = append(unledgered, fmt.Sprintf("  %q calls %v outside any Press", fn, verbs))
	}
	sort.Strings(unledgered)
	if len(unledgered) > 0 {
		t.Errorf("agents-tracker-7j4b: %s\n\n"+
			"These verbs reach awaitConn, which polls for up to five seconds, and they are called "+
			"from a function that runs on the main thread with nothing dispatching them. Hand the "+
			"call to VerbDispatch, or -- if it genuinely cannot move yet -- add it to "+
			"s25RenderPathExemptions with the issue that will pay it off.",
			strings.Join(unledgered, "\n"))
	}

	// The ledger must SHRINK. An exemption that can be extended silently is not a ledger, it is
	// an allowlist, and PB-DS-11's own wording -- existing violations are fixed, not allowlisted
	// -- is what a row here is borrowing against.
	for fn, reason := range s25RenderPathExemptions {
		if len(stray[fn]) == 0 {
			t.Errorf("agents-tracker-7j4b: %q is ledgered as still reaching a waiting verb from "+
				"the main thread (%s) and no longer does. Delete the row: a ledger nobody prunes "+
				"stops meaning anything, and this one is the record of a debt", fn, reason)
		}
	}
}

// s25StrayWaitingCalls finds waiting verbs called outside any Press, keyed by the function they
// sit in, so the ledger can name the ones that are known and filed.
func s25StrayWaitingCalls(code string, waiting map[string]bool) map[string][]string {
	dispatched := s25Bodies(code, "Press(", '(', ')')
	stray := map[string][]string{}
	for _, call := range s25CallSitesOf(code, waiting) {
		covered := false
		for _, d := range dispatched {
			if call.at >= d.start && call.at < d.end {
				covered = true
				break
			}
		}
		if !covered {
			stray[call.fn] = append(stray[call.fn], call.verb)
		}
	}
	return stray
}

func TestS25_StrayCallScanDiscriminates(t *testing.T) {
	waiting := map[string]bool{"kill": true}

	dispatched := `private fun killing(target: String) { Press(SendPlane.COMMAND, verb = { app -> app.kill(target) }) }`
	if bad := s25StrayWaitingCalls(dispatched, waiting); len(bad) != 0 {
		t.Errorf("the stray scan reported a dispatched call: %v", bad)
	}

	onTheMainThread := `private fun render() { app.kill(target) }`
	stray := s25StrayWaitingCalls(onTheMainThread, waiting)
	if len(stray) == 0 {
		t.Error("the stray scan passed a waiting verb called straight from a main-thread " +
			"function, which is the render-path half of this defect")
	}
	if got := stray["render"]; len(got) != 1 || got[0] != "kill" {
		t.Errorf("the stray scan attributed the call to %v, not to the function it sits in; the "+
			"ledger is keyed by that name, so a misattribution exempts the wrong function", stray)
	}
}

// ---------------------------------------------------------------------------
// The derivation itself, asserted so a silently-empty walk cannot pass anything.
// ---------------------------------------------------------------------------

// TestS25_ThePlaneDerivationFindsTheVerbsTheFacadeActuallyHas pins the walk against the facts
// mobile/commands.go states in prose, so a walker that quietly stopped following the call graph
// fails HERE, with a message about the walker, rather than by reporting every surface clean.
func TestS25_ThePlaneDerivationFindsTheVerbsTheFacadeActuallyHas(t *testing.T) {
	waiting, live := s25Planes(t)

	// Named in mobile/commands.go's own doc comments as command-path verbs.
	for _, verb := range []string{"takeControl", "kill", "launch", "revokeThisDevice"} {
		if !waiting[verb] {
			t.Errorf("agents-tracker-7j4b: %q was not derived as reaching sendContext, though it "+
				"seals a signed command. The call-graph walk is not following the facade -- most "+
				"likely it has stopped following METHOD VALUES, which is how sendContext hands "+
				"awaitConn to resolveSend (mobile/commands.go:513). derived: %v",
				verb, sortedKeys(waiting))
		}
	}

	// ADR-007 D7's live-only plane. sendInput must NOT be derived as waiting: if it is, the walk
	// has collapsed the two policies and every plane assertion above is meaningless.
	for _, verb := range []string{"sendInput", "paste", "resize"} {
		if !live[verb] {
			t.Errorf("agents-tracker-7j4b: %q was not derived as a LIVE-ONLY verb. derived live: %v",
				verb, sortedKeys(live))
		}
		if waiting[verb] {
			t.Errorf("agents-tracker-7j4b: %q was derived as reaching sendContext. It resolves "+
				"through liveSendContext, which exists precisely so it does NOT wait (ADR-007 D7) "+
				"-- so the walk has collapsed the two policies together and every plane assertion "+
				"in this file is now vacuous", verb)
		}
	}
}
