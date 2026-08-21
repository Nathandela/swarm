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
)

// FAILING-FIRST (TDD RED, GG-5) for the committee round-3 onPause finding: a facade verb that
// BLOCKS ON SESSION TEARDOWN, called on the Android main thread from a lifecycle callback.
//
// THE DEFECT THIS FENCES. `PhoneActivity.onPause()` calls `PhoneSurface.release()`
// synchronously, and `release()` called `live.stop()` INLINE on the main looper. `App.Stop`
// is not a command in awaitConn's sense -- it never touches `sendContext` -- which is exactly
// why every assertion in s25_mainthread_test.go stayed green over it: that file DERIVES its
// waiting set from the two send-context policies, and Stop blocks a different way. It cancels
// the relay session and then waits on `<-s.done` (mobile/app.go:480) for the drain goroutine,
// whose teardown performs a documented five-second graceful close
// (internal/remote/relay/client.go:411). Five seconds inside onPause is an ANR-class freeze,
// and a silent one -- `NetworkOnMainThreadException` never fires for a socket Go opened, and
// Robolectric cannot see a blocked looper (s25's own recorded blindnesses, all three).
//
// THE DERIVATION IS FROM THE FACADE, NOT A LIST, the same discipline as s25Planes: a verb
// that grows a teardown join tomorrow is derived the day it is written. A teardown-waiting
// verb is an exported `(*App)` method that -- itself or through the s25 call graph -- performs
// a BARE channel receive: a `<-x` outside any `select` and outside any function literal.
//
// WHAT THE DERIVATION DELIBERATELY EXCLUDES, so its floor test can discriminate:
//
//   - A receive inside a `select` is a poll (`rearmAfterPairing`, `IsRunning`) or a
//     multiplexed wait on a goroutine that is not the caller's (`drainPoll`,
//     `pollPresence`); the canonical teardown join this fence is about is the bare
//     `<-s.done`. STATED LIMIT: a teardown join rewritten as a select would evade the
//     derivation and would need a row here the day it is written.
//   - A receive inside a function literal blocks that literal's caller, not necessarily this
//     method: `Start` spawns `a.run(ctx)` on a goroutine and returns, and a derivation that
//     followed FuncLits would report Start as blocking -- the exact false positive the floor
//     test below pins against.
//
// THE SCAN IS OVER A FIXED FILE SET, NOT ALL PRODUCTION KOTLIN, and the difference from
// assertion 4's module-wide wrapper walk is deliberate and measured: `stop` and `close` are
// promiscuous bare names -- `QrScanner.stop()` releases a camera on the main thread correctly,
// `image.close()` closes an ImageProxy -- so a module-wide by-name walk would demand a lane
// around verbs that never leave the JVM. The judged set is every file that can hold a live
// `App` on the lifecycle path (the two lifecycle owners, the s25 subject surfaces, and the
// lifecycle lane pair), which is where this defect class lives. A teardown call moved to a
// file outside this set is the same one-hop bound jx1x's wrapper derivation records.
//
// Every scan below carries a negative control that feeds a perturbed source to the SAME
// function the real assertion calls.

// ---------------------------------------------------------------------------
// The derivation: which verbs block on session teardown.
// ---------------------------------------------------------------------------

// s25r3BlockingReceivers is the set of `(*App)` methods whose OWN body performs a bare
// channel receive -- outside any select, outside any function literal.
func s25r3BlockingReceivers(t *testing.T) map[string]bool {
	t.Helper()
	dir := facadeDir(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("committee-r3-onpause: cannot read the bound facade at %s: %v", mustRel(t, dir), err)
	}
	fset := token.NewFileSet()
	out := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatalf("committee-r3-onpause: parse %s: %v", name, err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 || fn.Body == nil {
				continue
			}
			if receiverTypeName(fn.Recv.List[0].Type) != "App" {
				continue
			}
			if s25r3HasBareReceive(fn.Body) {
				out[fn.Name.Name] = true
			}
		}
	}
	return out
}

// s25r3HasBareReceive reports whether the node contains a `<-x` receive that is not under a
// select statement and not inside a function literal. Both exclusions are the header's.
func s25r3HasBareReceive(root ast.Node) bool {
	found := false
	ast.Inspect(root, func(n ast.Node) bool {
		if found {
			return false
		}
		switch v := n.(type) {
		case *ast.FuncLit:
			return false // a goroutine's or deferred closure's wait is not this caller's
		case *ast.SelectStmt:
			return false // a select is a poll or a multiplexed wait, not the teardown join
		case *ast.UnaryExpr:
			if v.Op == token.ARROW {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

// s25r3TeardownVerbs derives the exported teardown-waiting verbs, gobind-named: every
// exported `(*App)` method that blocks itself or reaches a blocker through the s25 call
// graph (which follows method VALUES, s25AppCallGraph's own recorded reason).
func s25r3TeardownVerbs(t *testing.T) map[string]bool {
	t.Helper()
	blockers := s25r3BlockingReceivers(t)
	graph := s25AppCallGraph(t)
	reaching := map[string]bool{}
	for target := range blockers {
		for name := range s25Reaches(graph, target) {
			reaching[name] = true
		}
	}
	verbs := map[string]bool{}
	for name := range graph {
		if !ast.IsExported(name) {
			continue
		}
		if blockers[name] || reaching[name] {
			verbs[s25KotlinName(name)] = true
		}
	}
	return verbs
}

// TestS25R3_TheTeardownDerivationFindsStopAndClose pins the derivation against the facts
// mobile/app.go states in prose, so a walker that quietly stopped deriving fails HERE with a
// message about the walker rather than by reporting every surface clean.
func TestS25R3_TheTeardownDerivationFindsStopAndClose(t *testing.T) {
	verbs := s25r3TeardownVerbs(t)

	// Stop waits `<-s.done` (mobile/app.go:480); Close waits the same receive plus the
	// pairing WaitGroup (mobile/app.go:699). Both are named in their own doc comments as the
	// teardown joins.
	for _, verb := range []string{"stop", "close"} {
		if !verbs[verb] {
			t.Errorf("committee-r3-onpause: %q was not derived as blocking on session "+
				"teardown, though its body receives from the drain goroutine's done channel. "+
				"derived: %v", verb, sortedKeys(verbs))
		}
	}

	// Start spawns `a.run(ctx)` on a goroutine and returns; a derivation that reports it has
	// started following function literals and every assertion below over-reaches.
	if verbs["start"] {
		t.Errorf("committee-r3-onpause: %q was derived as teardown-waiting. Start spawns the "+
			"drain on a goroutine and returns (mobile/app.go:355-373); the derivation has "+
			"stopped excluding function literals. derived: %v", "start", sortedKeys(verbs))
	}

	// IsRunning and rearmAfterPairing poll `s.done` inside a select with a default; a
	// derivation that reports the poll has stopped excluding selects.
	if verbs["isRunning"] {
		t.Errorf("committee-r3-onpause: %q polls s.done non-blockingly (select with default, "+
			"mobile/app.go:637-642) and was derived as blocking; the derivation has stopped "+
			"excluding selects", "isRunning")
	}
}

// ---------------------------------------------------------------------------
// Reading the dispatched shapes, including Kotlin's trailing lambda.
// ---------------------------------------------------------------------------

// s25r3Openers are the dispatched shapes on the judged files: the three s25 press shapes,
// plus `machineVerb(` -- PhoneSurface's own forwarder onto `VerbDispatch.enqueue`'s command
// lane (its body hands `work = { verb(startup.app) }` to the lane, so a verb inside its call
// is a verb on the lane).
var s25r3Openers = []string{"Press(", "press(", "enqueue(", "machineVerb("}

// s25r3DispatchedBodies is every dispatched call on the source, each span extended through
// the trailing-lambda block Kotlin allows after the argument list -- because that block IS
// the call's last argument: `machineVerb(key = K) { app -> app.stop() }` runs the lambda on
// the lane, and a reader that stopped at the `)` would report it as a stray.
//
// THE EXTENSION IS BOUNDED AND ITS BOUND IS INHERITED: the s25 paren spans already cover the
// `settle = { ... }` argument, which runs on the MAIN looper -- a verb called inside a settle
// would read as covered there too. This file inherits that bound rather than tightening it,
// and states it, because a fence that silently claims more than it checks is this package's
// oldest defect class.
func s25r3DispatchedBodies(code string) []s25Span {
	var out []s25Span
	for _, opener := range s25r3Openers {
		for _, span := range s25Bodies(code, opener, '(', ')') {
			out = append(out, s25r3WithTrailingLambda(code, span))
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].start < out[b].start })
	return out
}

// s25r3WithTrailingLambda extends a paren span through one immediately-following `{ ... }`
// block, if any. String literals are skipped the way s25Bodies skips them.
func s25r3WithTrailingLambda(code string, span s25Span) s25Span {
	at := span.end // the closing ')' of the call's argument list
	if at >= len(code) || code[at] != ')' {
		return span
	}
	j := at + 1
	for j < len(code) && (code[j] == ' ' || code[j] == '\t' || code[j] == '\n') {
		j++
	}
	if j >= len(code) || code[j] != '{' {
		return span
	}
	depth := 1
	j++
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
		case '{':
			depth++
		case '}':
			depth--
		}
	}
	if depth != 0 {
		return span
	}
	return s25Span{start: span.start, end: j - 1}
}

// s25r3StrayCalls finds calls to `verbs` outside any dispatched body, keyed by the function
// they sit in -- s25StrayWaitingCalls with this file's trailing-lambda-aware reader.
func s25r3StrayCalls(code string, verbs map[string]bool) map[string][]string {
	dispatched := s25r3DispatchedBodies(code)
	stray := map[string][]string{}
	for _, call := range s25CallSitesOf(code, verbs) {
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

// ---------------------------------------------------------------------------
// The assertion: no teardown verb blocks the main looper.
// ---------------------------------------------------------------------------

// s25r3JudgedFiles is the fixed set the header argues for: the lifecycle owners and the
// surfaces that can hold a live App on the lifecycle path.
func s25r3JudgedFiles(t *testing.T) map[string]string {
	t.Helper()
	dir := filepath.Join(appModule(t), "src", "main", "kotlin", "dev", "swarm", "phone")
	return map[string]string{
		// The lifecycle lane pair the fix installed: the dispatcher, whose enqueue bodies are
		// where the teardown verbs now live, and the binding, whose thin wrappers are the one
		// legal stray shape (the declaration rule below). Their absence fails loudly here --
		// a deleted lane is the defect re-inlined somewhere this fence must then find.
		"AppLifecycle.kt":      filepath.Join(dir, "AppLifecycle.kt"),
		"LifecycleLane.kt":     filepath.Join(dir, "LifecycleLane.kt"),
		"PhoneActivity.kt":     filepath.Join(dir, "PhoneActivity.kt"),
		"PhoneRuntime.kt":      filepath.Join(dir, "PhoneRuntime.kt"),
		"PhoneSurface.kt":      filepath.Join(dir, "PhoneSurface.kt"),
		"SettingsSurface.kt":   filepath.Join(dir, "SettingsSurface.kt"),
		"TerminalWatchLane.kt": filepath.Join(dir, "TerminalWatchLane.kt"),
	}
}

// s25r3BindingFiles are the files whose THIN WRAPPERS are the wrapper's own declaration
// (jx1x's rule, restated for the lifecycle seam): a stray teardown call is legal there if
// and only if its enclosing function is named exactly the verb it wraps, so the by-name
// scans above keep seeing every call site of the wrapper -- renaming the verb is the
// laundering this package has already shipped once and may not ship again.
var s25r3BindingFiles = map[string]bool{"AppLifecycle.kt": true}

// s25r3Exemptions is the ledger of known main-thread teardown calls, per file per function,
// each one an open debt rather than a permitted pattern. IT MUST SHRINK, NOT GROW: the
// pruning assertion below fails on a row whose call is gone, and a new row has to be argued
// for in review.
var s25r3Exemptions = map[string]map[string]string{
	"PhoneRuntime.kt": {
		// `rebuildAfterPairing` closes the pre-pairing App on the caller's thread, which is
		// the main thread on the pairing completion path. App.Close joins the drain goroutine
		// (`<-s.done`, mobile/app.go:699) and the pairing WaitGroup; in the common case the
		// pre-pairing App was never started (fresh install, empty relay URL) so the join is
		// immediate, but a re-pair over a live session parks the looper for the close.
		// Committee round-3 residual, reported for its own fix rather than folded into the
		// onPause change.
		"rebuildAfterPairing": "re-pair over a live session joins the old App's drain on the main thread",
	},
}

// TestS25R3_NoTeardownVerbBlocksTheMainLooper is the fence the onPause finding was missing:
// a verb that joins the session drain -- five seconds of graceful close -- called from a
// main-thread function with nothing dispatching it.
func TestS25R3_NoTeardownVerbBlocksTheMainLooper(t *testing.T) {
	verbs := s25r3TeardownVerbs(t)
	if len(verbs) < 2 {
		t.Fatalf("committee-r3-onpause: derived only %d teardown verbs (%v); the floor test "+
			"has its own message, this one exists so the scan below never runs over an empty "+
			"question", len(verbs), sortedKeys(verbs))
	}

	var bad []string
	for name, path := range s25r3JudgedFiles(t) {
		code := kotlinCodeOnly(readFileOrFail(t, path,
			"committee-r3-onpause: a lifecycle-path holder of a live App"))
		stray := s25r3StrayCalls(code, verbs)
		exempt := s25r3Exemptions[name]
		for _, fn := range sortedKeys(stray) {
			calls := stray[fn]
			if s25r3IsWrapperDeclaration(s25r3BindingFiles[name], fn, calls) {
				continue
			}
			if _, ok := exempt[fn]; ok {
				continue
			}
			sort.Strings(calls)
			bad = append(bad, fmt.Sprintf("  %s: %q calls %v outside any dispatched press",
				name, fn, calls))
		}

		// The ledger must SHRINK (s25's own rule): a row whose call is gone is a row that
		// reads as live debt over nothing.
		for fn, reason := range exempt {
			if len(stray[fn]) == 0 {
				t.Errorf("committee-r3-onpause: %s's %q is ledgered as still joining a "+
					"teardown on the main thread (%s) and no longer does. Delete the row: a "+
					"ledger nobody prunes stops meaning anything", name, fn, reason)
			}
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Errorf("committee-r3-onpause: %s\n\n"+
			"These verbs join the relay session's drain goroutine: App.Stop cancels the "+
			"session and then waits on <-s.done (mobile/app.go:480), and the drain's teardown "+
			"performs a five-second graceful close (internal/remote/relay/client.go:411). "+
			"Called on the main looper -- and PhoneActivity.onPause -> PhoneSurface.release "+
			"runs there -- that is an ANR-class freeze nothing on the platform will ever "+
			"report. Hand the call to VerbDispatch on SendPlane.COMMAND, the way "+
			"TerminalWatchLane does for the watch verbs and LifecycleLane does for these.",
			strings.Join(bad, "\n"))
	}
}

// s25r3IsWrapperDeclaration reports whether a stray is the binding file's own thin-wrapper
// declaration: every verb the function strays must be the function's own name.
func s25r3IsWrapperDeclaration(bindingFile bool, fn string, calls []string) bool {
	if !bindingFile || fn == "" {
		return false
	}
	for _, verb := range calls {
		if verb != fn {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Negative controls: perturbed sources through the SAME functions.
// ---------------------------------------------------------------------------

func TestS25R3_TheReleasePathScanDiscriminates(t *testing.T) {
	verbs := map[string]bool{"stop": true}

	// The finding's exact shape: the teardown verb inline in the release path.
	inline := `fun release() { live.stop() }`
	stray := s25r3StrayCalls(inline, verbs)
	if got := stray["release"]; len(got) != 1 || got[0] != "stop" {
		t.Errorf("the scan passed the teardown verb called inline from release(), which is "+
			"the onPause finding exactly: %v", stray)
	}

	// The fix's shape: the verb inside an enqueue body on the command lane.
	laneShaped := `fun background() {
        dispatch.enqueue(plane = SendPlane.COMMAND, work = { handle.stop() }, settle = {})
    }`
	if got := s25r3StrayCalls(laneShaped, verbs); len(got) != 0 {
		t.Errorf("the scan reported a teardown verb already on the command lane: %v", got)
	}

	// The forwarder's trailing lambda: addComputer hands its verb to machineVerb, which
	// enqueues it -- the lambda sits AFTER the argument list's closing paren and is still
	// the call's last argument.
	trailing := `fun addComputer() {
        machineVerb(key = ADD_MACHINE_KEY, settle = {}) { app ->
            app.stop()
            try { app.addMachine(id, name) } finally { app.start() }
        }
    }`
	if got := s25r3StrayCalls(trailing, verbs); len(got) != 0 {
		t.Errorf("the scan reported a verb inside a dispatched call's trailing lambda, so "+
			"the correctly-laned Add computer flow would be forced off the lane to keep the "+
			"gate quiet: %v", got)
	}

	// And the extension does not swallow what follows the lambda: a stray AFTER the
	// dispatched call is still a stray.
	after := `fun addComputer() {
        machineVerb(key = K) { app -> app.addMachine(id, name) }
        app.stop()
    }`
	if got := s25r3StrayCalls(after, verbs); len(got["addComputer"]) != 1 {
		t.Errorf("the trailing-lambda extension covered source beyond the lambda itself: %v", got)
	}

	// The wrapper-declaration rule: a thin wrapper named for its verb is the declaration
	// (legal in a binding file only); a wrapper under any other name is the laundering jx1x
	// already shipped once.
	if !s25r3IsWrapperDeclaration(true, "stop", []string{"stop"}) {
		t.Error("the declaration rule rejects the thin wrapper `override fun stop() { app.stop() }`, " +
			"which is the one shape a binding file exists to hold")
	}
	if s25r3IsWrapperDeclaration(true, "halt", []string{"stop"}) {
		t.Error("the declaration rule accepted a teardown verb laundered under a new name, " +
			"which is agents-tracker-jx1x's defect class exactly")
	}
	if s25r3IsWrapperDeclaration(false, "stop", []string{"stop"}) {
		t.Error("the declaration rule accepted a wrapper outside the binding files, so any " +
			"judged file could grow one")
	}
}
