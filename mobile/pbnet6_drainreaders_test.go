package swarmmobile

// PB-NET-6's CONCURRENT-DRAIN clause, over the shipped phone (ADR-007 B98 step 3, fence 2 of 3).
//
// WHAT THE DEAD FENCE PROVED, AND WHY IT DOES NOT TRANSFER.
// internal/remote/transport/restart_test.go's TestConcurrentDrainsDeliverEachItemOnce drove two
// concurrent transport.Session.Drain calls and asserted each mailbox item arrived once. It found
// a real defect in review -- two drains delivered 10 items for a 5-item mailbox -- and the S6
// evidence names its shape exactly: "a foreground drain plus a push-wake drain on a handset".
// transport.Session has zero production constructions (B94), so that fence proved a property of
// an object that never ships.
//
// THE SHIPPED PHONE SATISFIES THE PROPERTY STRUCTURALLY, NOT DEFENSIVELY. There is exactly one
// mailbox reader -- App.drain, one goroutine per Start..Stop generation (mobile/relay.go) -- and
// HandlePushWake reads no mailbox at all. So there is no interleaving to make safe, and a
// runtime test that raced two drains could not even be written against the facade: nothing
// exposes a second one.
//
// WHICH IS EXACTLY WHY THE FENCE MUST BE STATIC. The property is "there is one reader", and the
// way it breaks is that someone ADDS one -- a push-wake that drains, a foreground refresh, a
// pull-to-refresh verb -- which is the defect the S6 review already found once and named. A
// behavioural test cannot see a reader nobody wired yet; scanning SOURCE fails on the day it
// lands. Same move as TestB88_EveryAtRestWriterIsEnumerated and PB-PUSH-3's producer
// enumeration: enforce the rule where it is NOT already obeyed.
//
// WHAT IT DOES NOT COVER, stated so nobody takes it for more than it is:
//   - It pins CALL SITES, not concurrency. A second read added INSIDE App.drain would pass
//     here; the drain loop's own budget is PB-SYNC-6's (mobile/conformance/drain_test.go).
//   - It scans the `mobile` package only. The gateway is a different hop with its own reader
//     (remotegw's CommandBridge), deliberately out of scope rather than forgotten.
//   - A reader reached through an interface whose method is not named below is invisible to it.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pbnet6ReadCalls are the relay client methods that CONSUME the phone's inbound mailbox. A
// second caller of any of them is a second drain.
var pbnet6ReadCalls = map[string]bool{
	"MailboxRead":     true,
	"MailboxReadPage": true,
	"MailboxWait":     true,
}

// pbnet6PermittedReaders is the enumeration: the functions in `mobile` allowed to consume the
// inbound mailbox, and why each is the only one.
var pbnet6PermittedReaders = map[string]string{
	"(*App).drainWait": "THE reader's live-tail mode (Wave R9): the bounded MailboxWait loop " +
		"App.drain selects against every relay whose hello advertised the \"wait\" capability. " +
		"Same goroutine, same single call site in App.run, same durable State.RelayCursor advanced " +
		"only inside phonecore's receive transaction. PB-NET-6's concurrent-drain clause is " +
		"satisfied by drain running the modes SEQUENTIALLY on one goroutine, never concurrently.",
	"(*App).drainPoll": "THE reader's compatibility-fallback mode: the pre-wait 500 ms poll, " +
		"selected by App.drain when a connection's hello did not advertise \"wait\" (an old " +
		"relay), or -- defense in depth -- after drainWait demoted a relay that advertised the " +
		"op and never answered one. Never concurrent with drainWait: drain dispatches on one " +
		"goroutine, waiting mode first, so there is still exactly one consumer of the durable " +
		"cursor.",
}

// TestPBNET6_ThePhoneHasExactlyOneMailboxReader fails when a second consumer arrives.
func TestPBNET6_ThePhoneHasExactlyOneMailboxReader(t *testing.T) {
	root := b88RepoRoot(t)
	dir := filepath.Join(root, "mobile")

	found := map[string][]string{} // enclosing func -> calls it makes
	scanned := 0

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("PB-NET-6: cannot read %s (%v); the fence would silently cover nothing", dir, err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("PB-NET-6: cannot parse %s: %v", name, err)
		}
		scanned++
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			who := pbnet6FuncName(fn)
			ast.Inspect(fn, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !pbnet6ReadCalls[sel.Sel.Name] {
					return true
				}
				found[who] = append(found[who], sel.Sel.Name)
				return true
			})
		}
	}

	// Anti-vacuity: a scan that parsed nothing, or that found no reader at all, would satisfy
	// "no unenumerated readers" perfectly. The phone HAS a reader; not seeing it means the
	// matcher is broken, not that the code changed.
	if scanned < 5 {
		t.Fatalf("PB-NET-6: only %d non-test files scanned under mobile/; the walk is broken", scanned)
	}
	if len(found) == 0 {
		t.Fatalf("PB-NET-6: the scan found NO mailbox reader in %d files. App.drain calls MailboxRead, "+
			"so this is a broken matcher rather than a clean tree -- and a broken matcher passes every "+
			"assertion below", scanned)
	}

	for who, calls := range found {
		if _, ok := pbnet6PermittedReaders[who]; !ok {
			t.Errorf("PB-NET-6: %s consumes the inbound mailbox (%s) and is NOT enumerated.\n"+
				"A second reader is a second drain: two consumers advancing one durable cursor deliver "+
				"an item twice or skip it, which is the defect the S6 review found by racing two "+
				"transport.Session.Drain calls -- 10 items delivered for a 5-item mailbox -- and whose "+
				"shape it named as 'a foreground drain plus a push-wake drain on a handset'.\n"+
				"If this reader is correct, serialise it with App.drain and add it to "+
				"pbnet6PermittedReaders WITH A REASON. Adding the name alone silences the only thing "+
				"that asks.", who, strings.Join(calls, ", "))
		}
	}
	for who := range pbnet6PermittedReaders {
		if _, ok := found[who]; !ok {
			t.Errorf("PB-NET-6: pbnet6PermittedReaders names %s, but no such reader was found. Either "+
				"it was renamed -- in which case the entry no longer protects anything -- or the drain "+
				"moved and this fence is now pointed at nothing.", who)
		}
	}
}

// TestPBNET6_EveryEnumeratedReaderStatesItsReason keeps the table from decaying into a list of
// names, which is how an enumeration stops meaning anything.
func TestPBNET6_EveryEnumeratedReaderStatesItsReason(t *testing.T) {
	for who, reason := range pbnet6PermittedReaders {
		if len(strings.TrimSpace(reason)) < 40 {
			t.Errorf("pbnet6PermittedReaders[%q] has no usable reason (%q): each entry records why that "+
				"reader is the only one, and without it the next reader adds a name to quiet a failure",
				who, reason)
		}
	}
}

// pbnet6FuncName renders a declaration as "(*App).drain" or "helper".
func pbnet6FuncName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	switch rt := fn.Recv.List[0].Type.(type) {
	case *ast.StarExpr:
		if id, ok := rt.X.(*ast.Ident); ok {
			return "(*" + id.Name + ")." + fn.Name.Name
		}
	case *ast.Ident:
		return "(" + rt.Name + ")." + fn.Name.Name
	}
	return fn.Name.Name
}
