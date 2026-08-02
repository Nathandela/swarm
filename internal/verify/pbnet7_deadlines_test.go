package verify_test

// PB-NET-7, THE QUANTIFIER: "timeouts EVERYWHERE" -- quantified over CALL PATHS, and until now
// nothing enumerated them.
//
// Every clause the row names is fenced and mutation-proven, and the one unbounded call site
// anyone found (the gateway's command-IN loop parking in MailboxWait, ADR-007 B112) is bounded
// at c4cc8b8. That closed an INSTANCE. It did not close the QUANTIFIER: a call site added
// tomorrow that hands relay an undeadlined context would have been caught by nothing, which is
// residual 4.23's shape and the reason B109 and B115 both declined to close this row.
//
// WHAT BOUNDS WHAT, AS A RULE RATHER THAN A LIST. relay has exactly one bounding mechanism and
// it is structural:
//
//	Conn.roundtrip takes the deadline BEFORE the exchange lock, from Conn.bounded, which is
//	context.WithTimeout(ctx, c.callTimeout) -- and a NO-OP when callTimeout is zero.
//	dialConn sets callTimeout ONLY on the PUMPED dial, and a *Client exists only on a pumped
//	connection (authenticate is the sole constructor and Dial/DialSecure are its sole callers,
//	both pumped).
//
// So the partition is DERIVED, not transcribed:
//
//	CLIENT-BOUNDED  -- a method on *Client that reaches roundtrip. It carries §6.0's 10 s
//	                   whatever context the caller passes, which is why every phone call site
//	                   may legitimately pass context.Background().
//	CALLER-BOUNDED  -- everything else, for one of two reasons, both of them rules:
//	                     (a) it does not reach roundtrip at all. That is the LONG POLL.
//	                         MailboxWait is deliberately unbounded at the client --
//	                         relay.TestCallDeadline_TheLongPollIsNotBoundedByIt pins that
//	                         contract on purpose, because a poll cut by the generic call
//	                         timeout would turn PB-NET-5's inbound seam into a timeout loop.
//	                         The corollary is that some caller must declare a deadline.
//	                     (b) it runs on a *Conn, which may be RAW -- and a raw connection has
//	                         no callTimeout, so bounded() returns the caller's context
//	                         untouched. That is the whole rendezvous transport, and the DIAL
//	                         itself, which happens before any connection exists to bound it.
//
// TestPBNET7_TheClientsOwnBoundIsStructural asserts the four facts that rule rests on, so the
// exemption is PROVEN rather than argued -- the exact failure ADR-007 B112 recorded, where a
// residual was discharged by an argument that delegated the bound to the declared adversary.
//
// TestPBNET7_EveryCallerBoundedCallSiteIsUnderADeclaredDeadline is the enumeration. It is a
// TYPED backward dataflow over SSA, not a name scan: for every production call into a
// caller-bounded relay operation it traces the context argument to its ORIGIN, through
// parameters (via the RTA call graph, so interface dispatch is resolved), through phis, and
// through helpers that return a context. Every origin must be context.WithTimeout or
// context.WithDeadline. A path that reaches context.Background(), a cancel-only context, or a
// value the walk cannot decide FAILS BY NAME, printing the chain.
//
// THERE IS NO ALLOWLIST, DELIBERATELY. A ledger of exempt call sites is a second copy of the
// call graph that rots (ADR-007 B111), and this walk needs none: the rule decides every site.
// The only judgement it encodes is the partition above, and that is derived from production
// and separately fenced.
//
// WHAT IT DOES NOT DECIDE, stated so nobody takes it for more than it is:
//
//   - It bounds the CONTEXT, not the wall clock. A caller that declares an hour passes.
//     Whether a declared budget is the RIGHT one is §6.0's table and other fences' subject.
//   - It walks only call sites RTA can reach from a production root. A call site in code no
//     entry point reaches cannot park a production process; dead code is B94's fence.
//   - RTA over-approximates interface dispatch, which for THIS walk is the safe direction: an
//     extra caller edge can only add an obligation, never remove one.
//   - Boundedness is decided per call site, so a caller that bounds one relay call and not the
//     next is reported for the second. That is the intent: B112's defect was exactly a function
//     that bounded some of its work.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/callgraph/rta"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

const pbnet7RelayPkg = b94Module + "/internal/remote/relay"

// ---- layer 1: the partition, derived from production ---------------------------------------

// pbnet7API is one entry in relay's context-taking public surface.
type pbnet7API struct {
	recv string // "*Client", "*Conn", or "" for a package-level function
	name string
	pos  token.Position
	// bounded reports whether the operation carries relay's own deadline. True only for a
	// *Client method that reaches roundtrip -- see the header for why the receiver matters.
	bounded bool
}

func (a pbnet7API) key() string {
	if a.recv == "" {
		return a.name
	}
	return "(" + a.recv + ")." + a.name
}

// pbnet7Partition parses relay's non-test sources and splits its context-taking public surface
// into the client-bounded and caller-bounded halves. It returns the whole surface; callers read
// the bounded field.
func pbnet7Partition(t *testing.T, root string) []pbnet7API {
	t.Helper()
	dir := filepath.Join(root, "internal", "remote", "relay")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("PB-NET-7: cannot read %s (%v); the partition would be derived from nothing", dir, err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, n), nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("PB-NET-7: cannot parse %s: %v", n, err)
		}
		files = append(files, f)
	}
	if len(files) < 5 {
		t.Fatalf("PB-NET-7 VACUOUS: only %d non-test files found under %s; the walk is broken",
			len(files), dir)
	}

	// callees[key] is every function name the body of key mentions in call position. Intra-
	// package and name-based, which is sound HERE because it is one package with unique
	// function names -- and the only question asked of it is "does this reach roundtrip".
	callees := map[string]map[string]bool{}
	var surface []pbnet7API
	for _, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			recv := pbnet7RecvName(fn)
			a := pbnet7API{recv: recv, name: fn.Name.Name, pos: fset.Position(fn.Pos())}
			set := map[string]bool{}
			ast.Inspect(fn, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch fun := call.Fun.(type) {
				case *ast.Ident:
					set[fun.Name] = true
				case *ast.SelectorExpr:
					set[fun.Sel.Name] = true
				}
				return true
			})
			callees[a.key()] = set
			if ast.IsExported(fn.Name.Name) && pbnet7TakesContext(fn) && pbnet7IsOutbound(fn, recv) {
				surface = append(surface, a)
			}
		}
	}

	// THE RULE, in one line: an operation is client-bounded iff RELAY applies a deadline on its
	// own path to the socket. There are exactly two ways that happens.
	//
	//	(1) it reaches roundtrip AND its receiver is *Client, so the connection is guaranteed
	//	    pumped and Conn.bounded is a real deadline rather than a pass-through;
	//	(2) it applies one itself, anywhere on its own call chain.
	//
	// Clause (2) is what keeps this file from encoding an implementer's CHOICE OF FIX LOCATION.
	// The dial is caller-bounded today because nothing on the path from Dial* to websocket.Dial
	// declares a deadline -- but bounding it inside dialConn instead of at each caller is a
	// perfectly good design, and under it the dial becomes client-bounded and its callers stop
	// owing anything. Deriving that, rather than asserting "package functions are caller-
	// bounded", means this fence keeps telling the truth either way instead of demanding the
	// world match its author.
	//
	// Conn.bounded is BLOCKED from clause (2) and that exclusion is the load-bearing one: the
	// deadline it applies is CONDITIONAL on c.callTimeout, which only the pumped dial sets
	// (FACT 3). Crediting it would mark every rendezvous op on a raw connection as bounded --
	// the precise over-approximation that made the whole quantifier unfenceable.
	blocked := map[string]bool{"(*Conn).bounded": true}
	for i := range surface {
		key := surface[i].key()
		viaRoundtrip := surface[i].recv == "*Client" &&
			pbnet7Reaches(callees, key, "roundtrip", nil, map[string]bool{})
		ownDeadline := pbnet7Reaches(callees, key, "WithTimeout", blocked, map[string]bool{}) ||
			pbnet7Reaches(callees, key, "WithDeadline", blocked, map[string]bool{})
		surface[i].bounded = viaRoundtrip || ownDeadline
	}
	sort.Slice(surface, func(i, j int) bool { return surface[i].key() < surface[j].key() })
	return surface
}

// pbnet7Reaches reports whether from's body transitively mentions want in call position,
// without descending into any function named in blocked.
func pbnet7Reaches(callees map[string]map[string]bool, from, want string, blocked, seen map[string]bool) bool {
	if seen[from] || blocked[from] {
		return false
	}
	seen[from] = true
	for callee := range callees[from] {
		if callee == want {
			return true
		}
		// A callee is named without its receiver, so try it as a package function and as a
		// method on either relay type. Over-matching here can only make an operation read
		// BOUNDED that is not, which the soundness controls below are there to catch.
		for _, k := range []string{callee, "(*Conn)." + callee, "(*Client)." + callee} {
			if _, ok := callees[k]; ok && pbnet7Reaches(callees, k, want, blocked, seen) {
				return true
			}
		}
	}
	return false
}

func pbnet7RecvName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	switch tp := fn.Recv.List[0].Type.(type) {
	case *ast.StarExpr:
		if id, ok := tp.X.(*ast.Ident); ok {
			return "*" + id.Name
		}
	case *ast.Ident:
		return tp.Name
	}
	return ""
}

// pbnet7IsOutbound scopes the subject to the surface that CALLS a relay: the two connection
// types and the dials that produce them.
//
// THE RELAY SERVER IS NOT IN SCOPE, AND THAT IS A RULE RATHER THAN AN EXCLUSION LIST. relay's
// package holds both halves of the protocol. Server.Start, SweepPresence and the delivery paths
// take a context because they run for the PROCESS's lifetime -- a deadline on Server.Start is a
// relay that shuts itself down mid-session. PB-NET-7 is the client's hygiene against a relay
// that answers nothing, and the relay is this design's DECLARED ADVERSARY: it is the thing
// called, not a caller. So the subject is what acts on an outbound connection.
func pbnet7IsOutbound(fn *ast.FuncDecl, recv string) bool {
	if recv != "" {
		return recv == "*Client" || recv == "*Conn"
	}
	if fn.Type.Results == nil {
		return false
	}
	for _, r := range fn.Type.Results.List {
		if st, ok := r.Type.(*ast.StarExpr); ok {
			if id, ok := st.X.(*ast.Ident); ok && (id.Name == "Client" || id.Name == "Conn") {
				return true
			}
		}
	}
	return false
}

func pbnet7TakesContext(fn *ast.FuncDecl) bool {
	for _, p := range fn.Type.Params.List {
		sel, ok := p.Type.(*ast.SelectorExpr)
		if ok && sel.Sel.Name == "Context" {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "context" {
				return true
			}
		}
	}
	return false
}

// TestPBNET7_TheClientsOwnBoundIsStructural proves the four facts the partition rests on. Each
// is a separate verdict, because each fails differently and a reader needs to know which moved.
func TestPBNET7_TheClientsOwnBoundIsStructural(t *testing.T) {
	root := repoRoot(t)
	path := filepath.Join(root, "internal", "remote", "relay", "client.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("PB-NET-7: cannot parse %s: %v", path, err)
	}
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("PB-NET-7: cannot read %s: %v", path, err)
	}
	body := func(name string) string {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != name || fn.Body == nil {
				continue
			}
			return string(src[fn.Body.Pos()-f.FileStart : fn.Body.End()-f.FileStart])
		}
		return ""
	}

	// FACT 1 -- bounded IS the deadline, and it is a NO-OP without a call timeout. If the
	// guard goes, raw connections silently gain a bound and clause (b) of the partition
	// becomes wrong in the permissive direction.
	b := body("bounded")
	switch {
	case b == "":
		t.Errorf("PB-NET-7 FACT 1: Conn.bounded no longer exists in client.go. The entire " +
			"client-bounded half of the partition is the claim that roundtrip applies a deadline " +
			"through it; with it gone, nothing here knows what does.")
	case !strings.Contains(b, "context.WithTimeout(ctx, c.callTimeout)"):
		t.Errorf("PB-NET-7 FACT 1: Conn.bounded no longer applies context.WithTimeout(ctx, "+
			"c.callTimeout). Body:\n%s", b)
	case !strings.Contains(b, "c.callTimeout <= 0"):
		t.Errorf("PB-NET-7 FACT 1: Conn.bounded no longer guards on c.callTimeout <= 0, so this "+
			"file can no longer tell a bounded connection from a raw one. Body:\n%s", b)
	}

	// FACT 2 -- roundtrip is the choke point every non-wait exchange takes, and it takes the
	// deadline BEFORE the lock.
	if rt := body("roundtrip"); !strings.Contains(rt, "c.bounded(ctx)") {
		t.Errorf("PB-NET-7 FACT 2: Conn.roundtrip no longer calls c.bounded(ctx). The partition "+
			"calls an operation client-bounded because it reaches roundtrip; that is now false.\n%s", rt)
	}

	// FACT 3 -- only the PUMPED dial carries a call timeout. Asserted at the assignment, so a
	// second assignment anywhere in the package is a finding rather than a silent widening.
	assigns := 0
	pumped := false
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			ifs, ok := n.(*ast.IfStmt)
			inPumped := ok && pbnet7IsIdent(ifs.Cond, "pumped")
			if inPumped {
				ast.Inspect(ifs.Body, func(m ast.Node) bool {
					if pbnet7AssignsCallTimeout(m) {
						pumped = true
					}
					return true
				})
			}
			if pbnet7AssignsCallTimeout(n) {
				assigns++
			}
			return true
		})
	}
	if assigns != 1 || !pumped {
		t.Errorf("PB-NET-7 FACT 3: c.callTimeout is assigned %d time(s) in client.go and the "+
			"assignment inside dialConn's `if pumped` branch is %v. The partition's clause (b) -- "+
			"a *Conn may be RAW and therefore unbounded -- is exactly the claim that a call timeout "+
			"is given ONLY to the pumped dial. Another assignment, or none, and the enumeration is "+
			"measuring the wrong surface.", assigns, pumped)
	}

	// FACT 4 -- a *Client is ALWAYS pumped. This is what lets the partition exempt every
	// *Client method that reaches roundtrip, and it is the fact that would go stale silently
	// if someone added a Client constructor over a raw connection.
	lits, ctors := 0, map[string]bool{}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			cl, ok := n.(*ast.CompositeLit)
			if ok && pbnet7IsIdent(cl.Type, "Client") {
				lits++
				ctors[fn.Name.Name] = true
			}
			return true
		})
	}
	if lits != 1 || !ctors["authenticate"] {
		t.Errorf("PB-NET-7 FACT 4: client.go constructs a Client %d time(s), in %v. The partition "+
			"exempts *Client methods because authenticate is the only constructor and both of its "+
			"callers dial PUMPED; a second construction can put a Client on a raw connection, where "+
			"bounded() is a no-op and every one of those exemptions is false.", lits, pbnet7Keys(ctors))
	}
	for _, dialer := range []string{"Dial", "DialSecure"} {
		d := body(dialer)
		if !strings.Contains(d, "authenticate(ctx, conn, auth)") || !strings.Contains(d, "true)") {
			t.Errorf("PB-NET-7 FACT 4: %s no longer reaches authenticate over a pumped dialConn. "+
				"Body:\n%s", dialer, d)
		}
	}

	// The partition itself, with its soundness controls. These are the known members: if the
	// derivation puts them on the wrong side, every verdict in the enumeration is noise.
	surface := pbnet7Partition(t, root)
	got := map[string]bool{}
	nBounded, nCaller := 0, 0
	for _, a := range surface {
		got[a.key()] = a.bounded
		if a.bounded {
			nBounded++
		} else {
			nCaller++
		}
	}
	if nBounded < 5 || nCaller < 5 {
		t.Fatalf("PB-NET-7 VACUOUS: the partition found %d client-bounded and %d caller-bounded "+
			"operations. relay has more of each; the derivation is broken and an empty half would "+
			"satisfy the enumeration perfectly.", nBounded, nCaller)
	}
	// The controls are STRUCTURAL cases only -- ones whose side is decided by the type system
	// and the contract rather than by where someone chose to put a fix. The dial is
	// deliberately NOT among them: it is caller-bounded today, and bounding it inside dialConn
	// instead would be a legitimate design that moves it. A control on it would be a fence
	// demanding one implementation of a free choice.
	for key, wantBounded := range map[string]bool{
		"(*Client).MailboxAppend":   true,  // a plain exchange: bounded by roundtrip on a pumped conn
		"(*Client).MailboxReadPage": true,  //   "
		"(*Client).MailboxWait":     false, // THE long poll: unbounded at the client by contract
		"(*Conn).RendezvousRecv":    false, // the rendezvous transport: raw connection, no timeout
	} {
		gotB, ok := got[key]
		if !ok {
			t.Errorf("PB-NET-7 CONTROL: %s is no longer part of relay's context-taking public "+
				"surface, so the partition can say nothing about it. If it was renamed, this "+
				"control must follow it; if it was deleted, delete the control.", key)
			continue
		}
		if gotB != wantBounded {
			t.Errorf("PB-NET-7 CONTROL FAILED: the derivation calls %s bounded=%v, want %v. "+
				"A control on the wrong side means the partition is measuring something other than "+
				"what this file claims, and every enumeration verdict below is an artifact.",
				key, gotB, wantBounded)
		}
	}
	var cb, kb []string
	for _, a := range surface {
		if a.bounded {
			cb = append(cb, a.key())
		} else {
			kb = append(kb, a.key())
		}
	}
	t.Logf("PB-NET-7 partition: %d client-bounded %v", nBounded, cb)
	t.Logf("PB-NET-7 partition: %d caller-bounded %v", nCaller, kb)
}

func pbnet7AssignsCallTimeout(n ast.Node) bool {
	as, ok := n.(*ast.AssignStmt)
	if !ok {
		return false
	}
	for _, lhs := range as.Lhs {
		if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "callTimeout" {
			return true
		}
	}
	return false
}

func pbnet7IsIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

func pbnet7Keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---- layer 2: the enumeration ----------------------------------------------------------------

var (
	pbnet7Once sync.Once
	pbnet7Prog *ssa.Program
	pbnet7Err  error
)

func pbnet7Load(t *testing.T, root string) *ssa.Program {
	t.Helper()
	pbnet7Once.Do(func() {
		pkgs, err := packages.Load(&packages.Config{
			Mode: packages.LoadAllSyntax, Dir: root, Tests: false,
		}, "./...")
		if err != nil {
			pbnet7Err = err
			return
		}
		var loadErrs int
		packages.Visit(pkgs, nil, func(p *packages.Package) { loadErrs += len(p.Errors) })
		if loadErrs > 0 {
			pbnet7Err = fmt.Errorf("%d package load errors", loadErrs)
			return
		}
		prog, _ := ssautil.AllPackages(pkgs, ssa.InstantiateGenerics)
		prog.Build()
		pbnet7Prog = prog
	})
	if pbnet7Err != nil {
		t.Fatalf("PB-NET-7: cannot build the program (%v); a partial graph would report unbounded "+
			"call sites as absent", pbnet7Err)
	}
	return pbnet7Prog
}

// pbnet7Origin is what a context traced backwards turned out to be.
type pbnet7Origin int

const (
	pbnet7Bounded   pbnet7Origin = iota // context.WithTimeout / WithDeadline
	pbnet7Unbounded                     // Background, TODO, a cancel-only context, a nil literal
	pbnet7Unknown                       // the walk could not decide, which is a finding, not a pass
)

type pbnet7Result struct {
	origin pbnet7Origin
	why    string
	path   []string
	hops   int // parameter propagations crossed; the interprocedural depth actually exercised
}

type pbnet7Walker struct {
	cg    *callgraph.Graph
	seen  map[string]bool
	depth int
}

// resolve traces v -- a context value handed to a relay operation -- back to its origin.
func (w *pbnet7Walker) resolve(fn *ssa.Function, v ssa.Value, trail []string) pbnet7Result {
	if w.depth > 80 {
		return pbnet7Result{pbnet7Unknown, "the walk exceeded 80 hops without reaching an origin", trail, 0}
	}
	w.depth++
	defer func() { w.depth-- }()

	switch t := v.(type) {
	case *ssa.Extract:
		return w.fromCall(fn, t.Tuple, t.Index, trail)
	case *ssa.Call:
		return w.fromCall(fn, t, 0, trail)
	case *ssa.Parameter:
		return w.fromParam(fn, t, trail)
	case *ssa.Phi:
		// EVERY incoming edge must be bounded. One unbounded branch is one unbounded call.
		best := pbnet7Result{pbnet7Bounded, "every branch is bounded", trail, 0}
		for _, e := range t.Edges {
			r := w.resolve(fn, e, trail)
			if r.origin != pbnet7Bounded {
				return r
			}
			if r.hops > best.hops {
				best.hops = r.hops
			}
		}
		return best
	case *ssa.MakeInterface:
		return w.resolve(fn, t.X, trail)
	case *ssa.ChangeInterface:
		return w.resolve(fn, t.X, trail)
	case *ssa.TypeAssert:
		return w.resolve(fn, t.X, trail)
	case *ssa.UnOp:
		// A LOAD. Every context this codebase hands the relay across a goroutine boundary
		// arrives this way: `go func() { a.run(ctx) }()` heap-allocates ctx, so the call sees
		// a load from an Alloc, and the closure sees a load from a FreeVar. Refusing to trace
		// those would report every goroutine-launched call site as undecidable, which is a
		// fence that cannot distinguish a bounded caller from an unbounded one.
		if t.Op == token.MUL {
			return w.fromAddr(fn, t.X, trail)
		}
	case *ssa.Const:
		return pbnet7Result{pbnet7Unbounded, "a nil context literal", trail, 0}
	}
	return pbnet7Result{pbnet7Unknown,
		fmt.Sprintf("the context comes from %T (%s), which this walk cannot trace. "+
			"Bound the call explicitly, or extend the walk -- do not leave it undecided",
			v, v.Name()), trail, 0}
}

// fromAddr resolves a context read out of a memory cell: a local whose address was taken (the
// shape `go func(){ ...ctx... }()` produces) or a variable a closure captured. Every value that
// can be in the cell must be bounded -- flow-insensitively, so a cell written once with a
// deadline and once without is reported.
func (w *pbnet7Walker) fromAddr(fn *ssa.Function, addr ssa.Value, trail []string) pbnet7Result {
	switch a := addr.(type) {
	case *ssa.Alloc:
		best := pbnet7Result{pbnet7Bounded, "every store is bounded", trail, 0}
		stores := 0
		for _, blk := range fn.Blocks {
			for _, instr := range blk.Instrs {
				st, ok := instr.(*ssa.Store)
				if !ok || st.Addr != a {
					continue
				}
				stores++
				r := w.resolve(fn, st.Val, trail)
				if r.origin != pbnet7Bounded {
					return r
				}
				if r.hops > best.hops {
					best.hops = r.hops
				}
			}
		}
		if stores == 0 {
			return pbnet7Result{pbnet7Unknown,
				"a context read from a local nothing in " + pbnet7Short(fn) + " writes", trail, 0}
		}
		return best

	case *ssa.FreeVar:
		// A captured variable: the obligation is the ENCLOSING function's, at the binding it
		// supplied when it made the closure.
		parent := fn.Parent()
		if parent == nil {
			return pbnet7Result{pbnet7Unknown, "a captured context with no enclosing function", trail, 0}
		}
		idx := -1
		for i, f := range fn.FreeVars {
			if f == a {
				idx = i
				break
			}
		}
		if idx < 0 {
			return pbnet7Result{pbnet7Unknown, "a captured context of no closure", trail, 0}
		}
		best := pbnet7Result{pbnet7Bounded, "every closure binding is bounded", trail, 0}
		made := 0
		for _, blk := range parent.Blocks {
			for _, instr := range blk.Instrs {
				mc, ok := instr.(*ssa.MakeClosure)
				if !ok || mc.Fn != fn || idx >= len(mc.Bindings) {
					continue
				}
				made++
				r := w.fromValueOrAddr(parent, mc.Bindings[idx],
					append(trail, pbnet7Short(fn)+" captured by "+pbnet7Short(parent)))
				if r.origin != pbnet7Bounded {
					return r
				}
				if r.hops > best.hops {
					best.hops = r.hops
				}
			}
		}
		if made == 0 {
			return pbnet7Result{pbnet7Unknown,
				"a closure over " + pbnet7Short(parent) + " this walk cannot find the construction of",
				trail, 0}
		}
		return best

	case *ssa.FieldAddr:
		return pbnet7Result{pbnet7Unknown,
			"the context is stashed in the struct field " + a.X.Type().String() + " and read back. " +
				"A context outliving the call that made it cannot be bounded by this walk; declare " +
				"the deadline at the relay call site instead", trail, 0}
	case *ssa.Global:
		return pbnet7Result{pbnet7Unbounded,
			"a context held in the package-level variable " + a.Name(), trail, 0}
	}
	return pbnet7Result{pbnet7Unknown,
		fmt.Sprintf("the context is loaded from %T, which this walk cannot trace", addr), trail, 0}
}

// fromValueOrAddr resolves a closure binding, which is the ADDRESS of the captured cell when
// the variable is assigned after capture and the value itself when it is not.
func (w *pbnet7Walker) fromValueOrAddr(fn *ssa.Function, v ssa.Value, trail []string) pbnet7Result {
	switch v.(type) {
	case *ssa.Alloc, *ssa.FreeVar, *ssa.FieldAddr, *ssa.Global:
		return w.fromAddr(fn, v, trail)
	}
	return w.resolve(fn, v, trail)
}

// fromCall resolves a context produced by a call: a context package constructor, or a helper
// that returns one (in which case every return operand at that index must be bounded).
func (w *pbnet7Walker) fromCall(fn *ssa.Function, call ssa.Value, idx int, trail []string) pbnet7Result {
	c, ok := call.(*ssa.Call)
	if !ok {
		return pbnet7Result{pbnet7Unknown,
			fmt.Sprintf("the context is extracted from %T, which this walk cannot trace", call), trail, 0}
	}
	callee := c.Common().StaticCallee()
	if callee == nil {
		return pbnet7Result{pbnet7Unknown,
			"the context is produced by a dynamic call this walk cannot resolve", trail, 0}
	}
	q := pbnet7FQN(callee)
	switch q {
	case "context.WithTimeout", "context.WithDeadline", "context.WithDeadlineCause", "context.WithTimeoutCause":
		return pbnet7Result{pbnet7Bounded, q, append(trail, q), 0}
	case "context.WithCancel", "context.WithCancelCause", "context.WithValue", "context.WithoutCancel":
		// These carry whatever deadline the parent had, and add none.
		return w.resolve(fn, c.Common().Args[0], append(trail, q))
	case "context.Background", "context.TODO":
		return pbnet7Result{pbnet7Unbounded, q + " -- no deadline of any kind", append(trail, q), 0}
	}
	if callee.Pkg == nil || callee.Pkg.Pkg == nil ||
		!strings.HasPrefix(callee.Pkg.Pkg.Path(), b94Module) {
		return pbnet7Result{pbnet7Unbounded,
			q + " -- a context from outside this module that declares no deadline", append(trail, q), 0}
	}
	// A helper in this module that returns a context: every return operand must be bounded.
	best := pbnet7Result{pbnet7Bounded, "every return is bounded", trail, 0}
	rets := 0
	for _, blk := range callee.Blocks {
		for _, instr := range blk.Instrs {
			ret, ok := instr.(*ssa.Return)
			if !ok || idx >= len(ret.Results) {
				continue
			}
			rets++
			r := w.resolve(callee, ret.Results[idx], append(trail, q+" returns"))
			if r.origin != pbnet7Bounded {
				return r
			}
			if r.hops > best.hops {
				best.hops = r.hops
			}
		}
	}
	if rets == 0 {
		return pbnet7Result{pbnet7Unknown, q + " has no traceable return", trail, 0}
	}
	return best
}

// fromParam moves the obligation to the callers: the context was handed in, so whoever hands
// it in must be the one that declared the deadline. Callers come from the RTA call graph, so
// an interface method's implementations are found rather than guessed at by name.
func (w *pbnet7Walker) fromParam(fn *ssa.Function, p *ssa.Parameter, trail []string) pbnet7Result {
	idx := -1
	for i, q := range fn.Params {
		if q == p {
			idx = i
			break
		}
	}
	if idx < 0 {
		return pbnet7Result{pbnet7Unknown, "a parameter of no function", trail, 0}
	}
	key := fmt.Sprintf("%s#%d", fn.String(), idx)
	if w.seen[key] {
		// A cycle (a recursive or mutually recursive relay driver). Every non-cyclic path into
		// it is checked on its own; the cycle itself introduces no new origin.
		return pbnet7Result{pbnet7Bounded, "already on this path", trail, 0}
	}
	w.seen[key] = true
	defer delete(w.seen, key)

	node := w.cg.Nodes[fn]
	label := pbnet7Short(fn)
	if node == nil || len(node.In) == 0 {
		return pbnet7Result{pbnet7Unbounded,
			"nothing calls " + label + ", so the context it hands the relay is supplied by an " +
				"entry point and no caller can declare a deadline for it",
			append(trail, label+" (no caller)"), 0}
	}
	best := pbnet7Result{pbnet7Bounded, "every caller declares a deadline", trail, 0}
	for _, e := range node.In {
		if e.Site == nil {
			continue
		}
		actual := pbnet7Actual(e.Site, idx)
		if actual == nil {
			return pbnet7Result{pbnet7Unknown,
				"the call to " + label + " from " + pbnet7Short(e.Caller.Func) +
					" passes an argument this walk cannot line up with the parameter",
				append(trail, label), 0}
		}
		r := w.resolve(e.Caller.Func, actual, append(trail, label+" <- "+pbnet7Short(e.Caller.Func)))
		if r.origin != pbnet7Bounded {
			r.hops++
			return r
		}
		if r.hops+1 > best.hops {
			best.hops = r.hops + 1
		}
	}
	return best
}

// pbnet7Actual lines a callee parameter index up with the argument at a call site. An invoke's
// receiver is the interface value rather than Args[0], which is the one place the two
// numberings differ.
func pbnet7Actual(site ssa.CallInstruction, idx int) ssa.Value {
	c := site.Common()
	if c.IsInvoke() {
		if idx == 0 {
			return c.Value
		}
		if idx-1 < len(c.Args) {
			return c.Args[idx-1]
		}
		return nil
	}
	if idx < len(c.Args) {
		return c.Args[idx]
	}
	return nil
}

func pbnet7FQN(fn *ssa.Function) string {
	if fn == nil {
		return ""
	}
	if fn.Pkg != nil && fn.Pkg.Pkg != nil {
		return fn.Pkg.Pkg.Path() + "." + fn.Name()
	}
	if obj := fn.Object(); obj != nil && obj.Pkg() != nil {
		return obj.Pkg().Path() + "." + fn.Name()
	}
	return fn.String()
}

func pbnet7Short(fn *ssa.Function) string {
	return strings.TrimPrefix(fn.String(), b94Module+"/")
}

// pbnet7RecvOf names an ssa method's receiver type the way layer 1's partition keys it.
func pbnet7RecvOf(fn *ssa.Function) string {
	recv := fn.Signature.Recv()
	if recv == nil {
		return ""
	}
	s := recv.Type().String()
	if i := strings.LastIndex(s, "."); i >= 0 {
		star := ""
		if strings.HasPrefix(s, "*") {
			star = "*"
		}
		return star + s[i+1:]
	}
	return s
}

// TestPBNET7_EveryCallerBoundedCallSiteIsUnderADeclaredDeadline is the row's missing quantifier.
func TestPBNET7_EveryCallerBoundedCallSiteIsUnderADeclaredDeadline(t *testing.T) {
	root := repoRoot(t)
	prog := pbnet7Load(t, root)

	callerBounded := map[string]bool{}
	for _, a := range pbnet7Partition(t, root) {
		if !a.bounded {
			callerBounded[a.key()] = true
		}
	}
	if len(callerBounded) == 0 {
		t.Fatal("PB-NET-7 VACUOUS: the partition produced no caller-bounded operations, so this " +
			"enumeration has nothing to enumerate and would pass over any tree")
	}

	// Resolve the caller-bounded surface to SSA functions.
	targets := map[*ssa.Function]string{}
	for fn := range ssautil.AllFunctions(prog) {
		if fn.Pkg == nil || fn.Pkg.Pkg == nil || fn.Pkg.Pkg.Path() != pbnet7RelayPkg {
			continue
		}
		key := fn.Name()
		if r := pbnet7RecvOf(fn); r != "" {
			key = "(" + r + ")." + fn.Name()
		}
		if callerBounded[key] {
			targets[fn] = key
		}
	}
	if len(targets) < len(callerBounded) {
		var missing []string
		found := map[string]bool{}
		for _, k := range targets {
			found[k] = true
		}
		for k := range callerBounded {
			if !found[k] {
				missing = append(missing, k)
			}
		}
		sort.Strings(missing)
		t.Fatalf("PB-NET-7 VACUOUS: %d caller-bounded operation(s) have no SSA function, so no "+
			"call site of theirs can be found: %s. The partition and the program disagree.",
			len(missing), strings.Join(missing, ", "))
	}

	roots, nMain, nFacade := b94Roots(prog)
	if nMain < 5 || nFacade < 20 {
		t.Fatalf("PB-NET-7 VACUOUS: %d main roots and %d facade roots; the root set is broken and "+
			"an unreachable call site is silently exempt", nMain, nFacade)
	}
	res := rta.Analyze(roots, true)
	if len(res.Reachable) < 5000 {
		t.Fatalf("PB-NET-7 VACUOUS: RTA reached only %d functions; the graph did not build",
			len(res.Reachable))
	}
	cg := res.CallGraph

	type site struct {
		op     string
		caller string
		pos    token.Position
		res    pbnet7Result
	}
	var checked, multiHop, viaInterface int
	var bad []site
	seenSite := map[string]bool{}

	// CALL SITES COME FROM THE CALL GRAPH, NOT FROM THE INSTRUCTION'S STATIC CALLEE, and that
	// distinction is the whole fence. The first version of this walk matched StaticCallee only.
	// It passed 15 call sites, and then PASSED the mutation that restores ADR-007 B112's
	// CRITICAL verbatim -- the gateway handing MailboxWait the bridge's lifetime context --
	// because internal/remotegw reaches the relay through its own Mailbox INTERFACE, so the
	// instruction is an invoke with no static callee and the matcher saw nothing at all. The
	// fence could not catch the defect it was written for. RTA resolves the interface, so
	// walking edges sees both shapes.
	for fn := range res.Reachable {
		if fn.Pkg == nil || fn.Pkg.Pkg == nil ||
			!strings.HasPrefix(fn.Pkg.Pkg.Path(), b94Module) ||
			fn.Pkg.Pkg.Path() == pbnet7RelayPkg {
			// relay's own in-package calls are the IMPLEMENTATION of the partition, not
			// consumers of it: bounded() is applied inside them, so asking whether their
			// caller declared a deadline is asking the wrong question of the wrong hop.
			continue
		}
		node := cg.Nodes[fn]
		if node == nil {
			continue
		}
		for _, e := range node.Out {
			op, ok := targets[e.Callee.Func]
			if !ok || e.Site == nil {
				continue
			}
			ctxIdx := -1
			for i, p := range e.Callee.Func.Params {
				if p.Type().String() == "context.Context" {
					ctxIdx = i
					break
				}
			}
			if ctxIdx < 0 {
				continue
			}
			actual := pbnet7Actual(e.Site, ctxIdx)
			if actual == nil {
				continue
			}
			pos := prog.Fset.Position(e.Site.Pos())
			// One source line can produce several edges when an interface has more than one
			// implementation. The obligation is the site's, so it is judged once.
			if key := pos.String() + "|" + op; seenSite[key] {
				continue
			} else {
				seenSite[key] = true
			}
			checked++
			if e.Site.Common().IsInvoke() {
				viaInterface++
			}
			w := &pbnet7Walker{cg: cg, seen: map[string]bool{}}
			r := w.resolve(fn, actual, nil)
			if r.hops > 0 {
				multiHop++
			}
			if r.origin != pbnet7Bounded {
				bad = append(bad, site{op, pbnet7Short(fn), pos, r})
			}
		}
	}

	// ---- anti-vacuity, before any verdict ------------------------------------------------
	// A walk that found no call sites satisfies "every call site is bounded" perfectly, and a
	// walk that never crossed a function boundary is a grep with extra steps.
	if checked < 8 {
		t.Fatalf("PB-NET-7 VACUOUS: only %d caller-bounded call site(s) found. The gateway's "+
			"MailboxWait, both rendezvous adapters and the production dials are more than that, "+
			"so the matcher is broken -- and a broken matcher passes every assertion below.", checked)
	}
	if multiHop == 0 {
		t.Fatalf("PB-NET-7 VACUOUS: %d call sites checked and NOT ONE resolved through a caller. "+
			"The rendezvous adapters forward a context they were handed, so at least one site must "+
			"cross a function boundary; zero means the parameter walk never ran.", checked)
	}
	if viaInterface == 0 {
		t.Fatalf("PB-NET-7 VACUOUS: %d call sites checked and NOT ONE goes through an interface. "+
			"internal/remotegw reaches the relay ONLY through its own Mailbox/Appender/Pusher "+
			"seams, so a walk that sees no invoke has stopped resolving interface dispatch -- "+
			"which is exactly the state in which this fence passed ADR-007 B112's own CRITICAL.",
			checked)
	}

	if len(bad) == 0 {
		t.Logf("PB-NET-7: %d caller-bounded relay call sites, %d resolved through a caller, "+
			"%d reached through an interface, all under a declared deadline",
			checked, multiHop, viaInterface)
		return
	}

	sort.Slice(bad, func(i, j int) bool { return bad[i].pos.String() < bad[j].pos.String() })
	var b strings.Builder
	fmt.Fprintf(&b, "\n%d of %d call sites hand a relay operation a context WITH NO DEADLINE.\n\n",
		len(bad), checked)
	for _, s := range bad {
		rel := strings.TrimPrefix(s.pos.Filename, root+string(filepath.Separator))
		fmt.Fprintf(&b, "  %s:%d\n", rel, s.pos.Line)
		fmt.Fprintf(&b, "      %s  calls  relay.%s\n", s.caller, s.op)
		fmt.Fprintf(&b, "      origin: %s\n", s.res.why)
		if len(s.res.path) > 0 {
			fmt.Fprintf(&b, "      via:    %s\n", strings.Join(s.res.path, "\n              "))
		}
		fmt.Fprintf(&b, "\n")
	}
	fmt.Fprintf(&b,
		"relay bounds an exchange ONLY inside roundtrip, and ONLY on a connection that carries a\n"+
			"call timeout -- which the pumped dial gives a *Client and nothing gives a raw *Conn.\n"+
			"Every operation outside that set ends when ITS CALLER says so, or it does not end:\n"+
			"the relay is the declared adversary and it can complete a handshake and answer nothing\n"+
			"(ADR-007 B112 measured 70 s against a 25 s server ceiling).\n\n"+
			"Fix at the CALL SITE with context.WithTimeout/WithDeadline. Do NOT make the operation\n"+
			"bound itself: for the long poll that breaks the contract\n"+
			"relay.TestCallDeadline_TheLongPollIsNotBoundedByIt exists to state (ADR-007 B115), and\n"+
			"for the rendezvous it would cut the pairing handshake's own window.\n")
	t.Error(b.String())
}
