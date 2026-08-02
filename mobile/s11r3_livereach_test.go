package swarmmobile_test

// Slice S11 REVIEW ROUND 3 -- FAILING-FIRST (TDD RED, GG-5) replacement fence for ADR-007
// D7's live-only rule.
//
// WHY THE ROUND-1 FENCE WAS NOT ONE. TestS11R_InputNeverWaitsForAConnection asserts that
// three function bodies do not contain the literal "a.sendContext(" and do contain
// "liveSendContext". It never looks at what liveSendContext RESOLVES TO, so both of the
// review's mutations pass it:
//
//  1. `func (a *App) liveSendContext() (sendCtx, error) { return a.resolveSend(a.awaitConn) }`
//     -- the five-second reconnect wait is back on the keystroke path, one level down, and
//     every body still says "liveSendContext".
//  2. `Paste` switched to `a.sendContext()` -- Paste is a live-input path and appears in
//     none of the three round-1 wiring guards.
//
// B2 shipped in the first place because a fence guarded a path production did not take.
// Replacing it with a fence a one-line move defeats is not a remediation, so this one
// follows the CALL GRAPH: from each live-input entry point, awaitConn must be unreachable
// through any number of hops, and conn must be reachable. Renaming or re-spelling the
// intermediate resolves nothing, because the assertion is about which of the two connection
// policies the path can reach -- and those two are the mechanism.
//
// This file contains NO implementation.

import (
	"go/ast"
	"sort"
	"strings"
	"testing"
)

// s11r3Func names one top-level func or method. owner is "" for a plain function.
type s11r3Func struct{ owner, name string }

func (f s11r3Func) String() string { return s11FuncLabel(f.owner, f.name) }

// s11r3CallGraph is the facade's intra-package call graph.
//
// EDGES ARE REFERENCES, NOT CALLS, and that is deliberate: resolveSend takes the connection
// policy as a FUNCTION VALUE (`a.resolveSend(a.awaitConn)`), so a fence that only followed
// call expressions would miss the exact shape mutation 1 uses.
type s11r3CallGraph map[s11r3Func]map[s11r3Func]bool

// s11r3BuildCallGraph walks every declaration in the facade and records, for each, the
// package-level funcs and receiver methods its body mentions.
//
// WHAT IT DOES NOT SEE, recorded rather than chased (round 4). It resolves `a.foo` against
// the receiver's own identifier, so two shapes evade edge recording: aliasing the receiver
// (`self := a; self.awaitConn()`) and method expressions ((*App).awaitConn(a)). Neither is
// worth a type-checked walker here, because an ACCIDENTAL variant of either also loses the
// connection reference and trips the "cannot reach conn" non-vacuity assertion below -- only
// a deliberate decoy evades both, and a fence's job is to stop regressions, not authors.
func s11r3BuildCallGraph(t *testing.T, src *facadeSource) s11r3CallGraph {
	t.Helper()

	decls := map[s11r3Func]*ast.FuncDecl{}
	for _, f := range src.Files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			owner := ""
			if fd.Recv != nil {
				owner = receiverTypeName(fd.Recv)
			}
			decls[s11r3Func{owner, fd.Name.Name}] = fd
		}
	}
	if len(decls) == 0 {
		t.Fatal("the facade declares no functions at all; there is no call graph to walk")
	}

	graph := s11r3CallGraph{}
	for key, fd := range decls {
		// The receiver's identifier, so `a.foo` is resolved against THIS type's methods and
		// a same-named field on some other value is not mistaken for one.
		recv := ""
		if fd.Recv != nil && len(fd.Recv.List) > 0 && len(fd.Recv.List[0].Names) > 0 {
			recv = fd.Recv.List[0].Names[0].Name
		}
		out := map[s11r3Func]bool{}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			switch e := n.(type) {
			case *ast.SelectorExpr:
				id, ok := e.X.(*ast.Ident)
				if !ok || recv == "" || id.Name != recv {
					return true
				}
				if callee := (s11r3Func{key.owner, e.Sel.Name}); decls[callee] != nil {
					out[callee] = true
				}
			case *ast.Ident:
				if callee := (s11r3Func{"", e.Name}); decls[callee] != nil {
					out[callee] = true
				}
			}
			return true
		})
		graph[key] = out
	}
	return graph
}

// reaches reports whether want is reachable from root, and the shortest path it found.
func (g s11r3CallGraph) reaches(root, want s11r3Func) (bool, []s11r3Func) {
	type node struct {
		fn   s11r3Func
		path []s11r3Func
	}
	seen := map[s11r3Func]bool{root: true}
	queue := []node{{root, []s11r3Func{root}}}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.fn == want {
			return true, cur.path
		}
		next := make([]s11r3Func, 0, len(g[cur.fn]))
		for callee := range g[cur.fn] {
			next = append(next, callee)
		}
		sort.Slice(next, func(i, j int) bool { return next[i].name < next[j].name })
		for _, callee := range next {
			if seen[callee] {
				continue
			}
			seen[callee] = true
			queue = append(queue, node{callee, append(append([]s11r3Func{}, cur.path...), callee)})
		}
	}
	return false, nil
}

func s11r3Path(path []s11r3Func) string {
	parts := make([]string, 0, len(path))
	for _, f := range path {
		parts = append(parts, f.name)
	}
	return strings.Join(parts, " -> ")
}

// s11r3LiveInputPaths is EVERY surface that puts bytes on the live control lease. The list
// is the point: Paste and ReleaseControl are live-input paths and were in none of the
// round-1 wiring guards, so the mutation that moved Paste onto the waiting resolver was
// invisible to all three.
//
// THE LIST IS CLOSED AND NOTHING ENROLS ITSELF. A live-input surface added tomorrow is
// covered by this fence only when it is written here, exactly as Paste and ReleaseControl had
// to be after round 1 found them missing. There is no property that distinguishes a
// live-input entry point from any other facade method by shape, so the enumeration is the
// only handle there is -- and saying so is cheaper than discovering it a third time.
var s11r3LiveInputPaths = []s11r3Func{
	{"App", "SendInput"},
	{"App", "Paste"},
	{"App", "Resize"},
	{"App", "ReleaseControl"},
	{"App", "drainHeldInput"},
}

// s11r3ResolverScopedOnly EXEMPTS a live-input path from the WHOLE-ROOT assertion below,
// with the reason it is exempt. Everything in s11r3LiveInputPaths is whole-root by default,
// so a surface added to that list is fully fenced without anyone remembering a second list --
// which is the drift that produced round 4's hole in the first place.
//
// ROUND 4, on why the whole-root half exists at all: the round-3 fence queried reachability
// only from each root's directResolvers, so it never asked `reaches(root, awaitConn)`. An
// `awaitConn()` fallback written DIRECTLY inside SendInput -- "add reconnect resilience", the
// most natural regression shape there is -- therefore passed the reachability fence (never
// queried from the root), the round-1 string fence (which bans only the literal
// "a.sendContext("), and both behavioural disconnect tests (which refuse on the LEASE gate:
// suspendInput severs before the resolver is ever consulted). The walker already recorded the
// SendInput -> awaitConn edge; the test simply never asked for it.
var s11r3ResolverScopedOnly = map[s11r3Func]string{
	{"App", "ReleaseControl"}: "it also seals a signed take_control_end, and that command path " +
		"legitimately waits for a connection. What must be live-only is the coalescer flush it " +
		"performs first, which is exactly the resolver it names in its own body -- so it stays " +
		"resolver-scoped rather than being dropped from the fence",
}

// s11r3Resolvers is every destination resolver in the facade, identified by its RESULT TYPE
// -- a func that yields a sendCtx is a resolver, whatever it is called. Naming them by type
// rather than by spelling is what keeps this fence from being another string search: a
// renamed or newly-added resolver is picked up automatically, and the assertions below are
// about which connection policy each one embodies.
func s11r3Resolvers(t *testing.T, src *facadeSource) map[s11r3Func]bool {
	t.Helper()
	out := map[s11r3Func]bool{}
	for _, f := range src.Files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Type.Results == nil {
				continue
			}
			for _, res := range fd.Type.Results.List {
				id, ok := res.Type.(*ast.Ident)
				if !ok || id.Name != "sendCtx" {
					continue
				}
				owner := ""
				if fd.Recv != nil {
					owner = receiverTypeName(fd.Recv)
				}
				out[s11r3Func{owner, fd.Name.Name}] = true
			}
		}
	}
	if len(out) < 2 {
		t.Fatalf("the facade has %d functions returning a sendCtx; this guard compares the LIVE "+
			"resolver against the WAITING one, and with fewer than two there is nothing to compare",
			len(out))
	}
	return out
}

// s11r3DirectResolvers is the set of resolvers fn names in its OWN body -- the policy that
// path actually chose, before any indirection.
func (g s11r3CallGraph) directResolvers(fn s11r3Func, resolvers map[s11r3Func]bool) []s11r3Func {
	var out []s11r3Func
	for callee := range g[fn] {
		if resolvers[callee] {
			out = append(out, callee)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// TestS11R3_NoLiveInputPathCanReachTheWaitingResolver is ADR-007 D7 as a reachability
// property rather than a spelling.
//
// awaitConn POLLS FOR UP TO FIVE SECONDS for a connection to come up. On a keystroke path
// that means the byte is appended to the RECONNECTED link and lands at the machine seconds
// later, against a terminal state the user has since changed and long after they gave up on
// it -- a keystroke surviving a disconnect, which the rule is structural about. The wait is
// correct for a COMMAND, which is idempotent and queued by design, so the two policies must
// stay on opposite sides of this line.
//
// It is asserted in three composed halves, and it takes all three to catch all three
// mutations:
//
//  1. WHICH RESOLVER a live-input path chooses -- read off its own body by result type, so
//     Paste switching to the waiting one is caught however it is spelled.
//  2. WHAT THAT RESOLVER CAN REACH -- transitively, through function VALUES, so restoring
//     the wait one level down inside it is caught too.
//  3. WHAT THE ROOT ITSELF CAN REACH -- whole-closure, because 1 and 2 together still only
//     ever ask about the RESOLVER, and a wait written directly in the entry point is not on
//     any resolver's closure at all. That is round 4's hole, and it is the shape a regression
//     takes when someone "adds reconnect resilience" to SendInput.
//
// ReleaseControl is deliberately in the list AND deliberately not asserted whole: it also
// seals a signed take_control_end, and that command path legitimately reaches awaitConn.
// What must be live-only is the coalescer flush it performs first, which is exactly the
// resolver it names in its own body.
func TestS11R3_NoLiveInputPathCanReachTheWaitingResolver(t *testing.T) {
	src := loadFacade(t)
	graph := s11r3BuildCallGraph(t, src)
	resolvers := s11r3Resolvers(t, src)

	waiting := s11r3Func{"App", "awaitConn"}
	immediate := s11r3Func{"App", "conn"}
	// NON-VACUITY, first: both policies must exist, or every assertion below passes by
	// finding nothing.
	for _, fn := range []s11r3Func{waiting, immediate} {
		if _, ok := graph[fn]; !ok {
			t.Fatalf("the facade declares no %s; this guard is about which of the two connection "+
				"policies a live-input path can reach, and it cannot assert that if one is gone", fn)
		}
	}

	for _, root := range s11r3LiveInputPaths {
		if _, ok := graph[root]; !ok {
			t.Errorf("the facade declares no %s -- a live-input surface this guard covers has been "+
				"renamed or removed, so the rule is no longer enforced on it", root)
			continue
		}
		chosen := graph.directResolvers(root, resolvers)
		if len(chosen) == 0 {
			t.Errorf("%s names no destination resolver of its own, so which connection policy its "+
				"bytes ride cannot be established here and this guard is vacuous for it", root)
			continue
		}
		for _, r := range chosen {
			if reached, path := graph.reaches(r, waiting); reached {
				t.Errorf("%s resolves its destination through %s, which can reach awaitConn: %s.\n"+
					"ADR-007 D7: input is LIVE-ONLY. awaitConn polls for up to five seconds for a "+
					"connection, so a keystroke typed offline is delivered on the RECONNECTED link "+
					"-- a keystroke surviving a disconnect. The live path must resolve through the "+
					"policy that fails immediately, however many hops away the wait is written.",
					root, r.name, s11r3Path(path))
			}
			if reached, _ := graph.reaches(r, immediate); !reached {
				t.Errorf("%s resolves through %s, which cannot reach conn (the immediate, "+
					"non-waiting policy). Either it resolves no connection at all, or it acquired a "+
					"third policy nobody has argued for.", root, r.name)
			}
		}
	}

	// WHOLE-ROOT, the half round 3 never asked for. Every hop from the entry point, not just
	// the ones under the resolver it names.
	for _, root := range s11r3LiveInputPaths {
		if _, ok := graph[root]; !ok {
			continue // already reported above
		}
		if _, exempt := s11r3ResolverScopedOnly[root]; exempt {
			continue
		}
		if reached, path := graph.reaches(root, waiting); reached {
			t.Errorf("%s can reach awaitConn: %s.\n"+
				"ADR-007 D7: input is LIVE-ONLY. It does not matter that the destination RESOLVER is "+
				"the immediate one -- a wait reached from anywhere in this closure, including one "+
				"written directly in the entry point as a reconnect fallback, blocks the keystroke "+
				"for up to five seconds and then delivers it on the RECONNECTED link.",
				root, s11r3Path(path))
		}
		// NON-VACUITY for this half: a root that reached NOTHING would satisfy the assertion
		// above for free, so each one must still get to the immediate policy.
		if reached, _ := graph.reaches(root, immediate); !reached {
			t.Errorf("%s cannot reach conn (the immediate, non-waiting policy) from anywhere in its "+
				"closure, so the assertion that it cannot reach awaitConn is satisfied by it "+
				"resolving no connection at all", root)
		}
	}

	// ... and the COMMAND path must KEEP the wait, or fixing input breaks the race
	// awaitConn exists to close: a command issued right after Start must not be refused by
	// a race the caller cannot see.
	cmd := s11r3Func{"App", "sealSignedCommand"}
	cmdResolvers := graph.directResolvers(cmd, resolvers)
	if len(cmdResolvers) == 0 {
		t.Fatalf("%s names no destination resolver; the command half of this rule has nowhere to "+
			"live", cmd)
	}
	for _, r := range cmdResolvers {
		if reached, _ := graph.reaches(r, waiting); !reached {
			t.Errorf("%s resolves through %s, which cannot reach awaitConn. A command is idempotent "+
				"and queued by design, so the brief wait for a connection Start is bringing up is "+
				"correct for it -- only INPUT must fail fast. Deleting the wait outright is not the "+
				"fix.", cmd, r.name)
		}
	}
}

// TestS11R3_TheCallGraphResolvesThroughIndirection is a guard on the guard. The whole point
// of the fence above is that it follows what a string search does not, so the machinery that
// does the following is itself asserted.
func TestS11R3_TheCallGraphResolvesThroughIndirection(t *testing.T) {
	src := loadFacade(t)
	graph := s11r3BuildCallGraph(t, src)

	// A FUNCTION VALUE, never called: sendContext passes a.awaitConn to resolveSend and
	// never writes `a.awaitConn()`. A walker that only recorded call expressions would miss
	// it, and mutation 1 is written in exactly that shape.
	if !graph[s11r3Func{"App", "sendContext"}][s11r3Func{"App", "awaitConn"}] {
		t.Errorf("the call graph has no sendContext -> awaitConn edge, though sendContext passes "+
			"a.awaitConn as a value. The walker is not following function values, so every "+
			"'cannot reach' assertion in this file is vacuous. graph[sendContext] = %v",
			graph[s11r3Func{"App", "sendContext"}])
	}

	// MULTI-HOP: the fence has to survive an indirection being introduced, so transitive
	// closure is asserted on a chain that is three nodes long in the shipped source.
	reached, path := graph.reaches(s11r3Func{"App", "SendInput"}, s11r3Func{"App", "sendInputFrame"})
	if !reached || len(path) < 3 {
		t.Errorf("SendInput -> sendCoalesced -> sendInputFrame is not reachable transitively "+
			"(reached=%v, path=%q); the walker is not closing over intermediates", reached, s11r3Path(path))
	}

	// ... and it must not report reachability that is not there.
	if reached, path := graph.reaches(s11r3Func{"App", "Roster"}, s11r3Func{"App", "sendInputFrame"}); reached {
		t.Errorf("the call graph claims Roster reaches sendInputFrame (%s); a walker that reports "+
			"edges that do not exist makes every assertion here noise", s11r3Path(path))
	}
}
