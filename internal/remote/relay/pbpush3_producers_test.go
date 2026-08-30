package relay

// ADR-007 B87 / residual 4.23 -- THE ENUMERATION THE SIZE FENCE CANNOT BE.
//
// PB-PUSH-3's subject is the push provider: "the provider observes only token, timing and
// size". A property quantified over a CHANNEL is quantified over every producer that feeds it.
// The fence that pins it (remotegw's PushWakeEnvelopeSize test) has a COMPONENT as its
// subject, so every other producer is unfenced BY CONSTRUCTION and adding one is invisible --
// which is how the presence sweep's ciphertext-free wake shipped in normal operation while the
// row read `shipped`.
//
// THE FIX SHAPE IS AN ENUMERATION, NOT A BIGGER ASSERTION. pbpush3_channel_test.go measures
// the invariant over the producers that exist today; this file pins that that set IS the set,
// so a FOURTH producer fails here by name instead of slipping past an invariant nobody pointed
// at it.
//
// IT IS STATIC ON PURPOSE, and that is the whole design. A runtime check sees only the
// producers the test constructs -- which is exactly how this defect survived: the existing
// fence's fixture cannot construct the relay's producers at all. Scanning SOURCE catches the
// next producer on the day it lands, whether or not any test drives it. Same move as
// TestS10_NoTestFixtureStampsANonzeroRosterCursor and TestB88_EveryAtRestWriterIsEnumerated:
// enforce the rule where it is NOT already obeyed.
//
// RESOLUTION IS PER-FUNCTION, NOT PER-FILE, and the difference is load-bearing here. B88's
// at-rest ledger is file-granular because a second write inside an already-listed file is not
// new exposure. That reasoning does NOT transfer: BOTH of the relay's producers live in
// server.go, so a file-granular list would let a third one land in that same file unseen. It
// is not expression-granular either -- a count-of-literals fence fires on a reformat and
// teaches people to edit the number.
//
// WHAT THIS DOES NOT COVER, stated so nobody takes it for more than it is:
//   - A SECOND payload construction inside a function already listed is invisible. Adding a
//     branch to SweepPresence that sends a different shape would pass here; the channel test
//     is what would have to catch that, and it can only see what it drives.
//   - IT PINS CALL SITES, NOT SHAPES. An entry's reason records what that producer puts on the
//     channel; the reason is prose, not evidence. The size property is asserted in
//     pbpush3_channel_test.go and nowhere else.
//   - It scans Go source only. A producer written in Kotlin on the handset does not push
//     through this relay seam, so it is out of scope by construction rather than by omission.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// pbpush3PayloadType is the composite-literal type that IS a provider-bound payload.
const pbpush3PayloadType = "PushPayload"

// pbpush3ProducerCalls are the calls that move a payload one hop closer to the provider.
// Deliberately broad -- `Push` alone would match an unrelated stack push, and that is the
// correct trade: a spurious hit costs one enumeration entry with a reason, while a matcher
// narrowed to today's idiom costs the next producer.
var pbpush3ProducerCalls = map[string]bool{
	"Push":             true, // PushSink.Push -- the sink boundary itself
	"PushTrigger":      true, // relay.Client.PushTrigger -- the machine's way onto the channel
	"deliverPush":      true, // the relay's internal funnel
	"pushAndReconcile": true, // the funnel's delivery half
}

// pbpush3Producers is the enumeration: every function that can put a payload in front of the
// push provider, what it puts there, and which channel test judges it.
//
// ADDING AN ENTRY IS A DECISION. A new producer needs its SHAPE settled -- indistinguishable
// in size from the canonical wake, or refused -- and driven by a channel test BEFORE it is
// listed, because nothing else will ask. A `covered-by:` naming a test that does not exercise
// the new producer silences the only thing that would have noticed.
//
// Entries are keyed `<path from module root>:<enclosing function>`.
var pbpush3Producers = map[string]string{
	"internal/remotegw/push.go:(*PushNotifier).maybeWake": "PRODUCER -- the gateway wake, and the " +
		"canonical shape. crypto.SealWake over an EMPTY plaintext with both key ids zero, handed to " +
		"push_trigger: a constant 78 bytes of ciphertext (remotegw.PushWakeEnvelopeSize). " +
		"covered-by: TestPBPUSH3_EveryProducerIsTheSameSizeOnTheProviderChannel",

	"internal/remote/relay/server.go:(*serverConn).handlePushTrigger": "PRODUCER -- the relay's " +
		"trigger handler. It copies the caller's `envelope` field into PushPayload.Ciphertext with NO " +
		"schema applied, so the number of bytes the provider counts is chosen by whoever calls " +
		"push_trigger rather than by the wake format. " +
		"covered-by: TestPBPUSH3_AnUnschemadTriggerEnvelopeIsTheSameSizeOnTheChannel",

	"internal/remote/relay/server.go:(*Server).SweepPresence": "PRODUCER -- the machine-went-silent " +
		"wake. Sends PushPayload{Alert: GenericPushAlert} with NO ciphertext at all, and it ships in " +
		"NORMAL OPERATION whenever a socket drops -- so it needs no adversary and no key to read: a " +
		"short push means the machine went silent. " +
		"covered-by: TestPBPUSH3_ThePresenceSweepIsTheSameSizeOnTheChannelAsAWake",

	"internal/pushgw/fcmsender.go:(*fcmSenderAdapter).Send": "PRODUCER -- the Swarm push gateway's " +
		"own FCM submission (ADR-015; NOT the relay channel: the gateway is a separate HTTPS " +
		"service and this fence quantifies over the PROVIDER, which both feed). Its shape is " +
		"settled at ADMISSION, upstream of Send: readWakeBody refuses any body that is not " +
		"exactly wakeSize = 74 bytes (WakeV1, push-gateway-api.md PG-WAKE-2/PG-TR-3), and the " +
		"handler forwards the admitted envelope byte-identical, so this producer can only ever " +
		"put a constant 74 bytes in front of the provider or nothing. " +
		"covered-by: TestSubmitWake_LengthTable (internal/pushgw: every non-74 length refused " +
		"before the provider is called), with TestSubmitWake_ForwardsByteIdenticalEnvelopeNeverOpeningIt " +
		"pinning the pass-through.",

	"internal/remote/relay/server.go:(*Server).deliverPush": "FUNNEL, not a producer -- the point " +
		"where both relay producers converge before the sink. It constructs nothing and forwards the " +
		"payload it was handed, on a background goroutine. Listed because the matcher sees it and a " +
		"convergence point that is invisible to the ledger is a convergence point nobody rechecks.",

	"internal/remote/relay/server.go:(*Server).pushAndReconcile": "FUNNEL, not a producer -- the " +
		"sink boundary (`s.push.Push`). Everything that reaches the provider passes through this one " +
		"call, which is why the channel test measures RAW PROVIDER BYTES downstream of it rather than " +
		"the PushPayload struct here.",

	"internal/remotegw/pushtransport.go:(*TransportRouter).PushTrigger": "FUNNEL, not a producer -- " +
		"routes one already-built envelope to exactly one transport per the durable push_transport " +
		"selection (PG-MIG-1). Its legacy_relay case constructs nothing and forwards maybeWake's " +
		"env argument (the already-sealed, constant 78-byte wake) unchanged to r.Legacy.PushTrigger, " +
		"the same relay.Client.PushTrigger hop this ledger's second entry already covers from the " +
		"far side. The gateway case drives WakeObligationMachine.Trigger/Drive instead, a wholly " +
		"separate HTTPS channel the fcmSenderAdapter.Send entry above covers; foreground_only sends " +
		"nothing. Listed because the matcher sees the PushTrigger() call here and a routing hop that " +
		"is invisible to the ledger is a hop nobody rechecks.",
}

// pbpush3ModuleRoot locates the module root so the scan covers EVERY package. A rule enforced
// only where it is already obeyed is not a rule -- and the producer that escaped the existing
// fence lives in a different package from it.
func pbpush3ModuleRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}").Output()
	if err != nil {
		t.Fatalf("locate the module root: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// pbpush3Site is one matched construct, attributed to the function that contains it.
type pbpush3Site struct {
	key  string // "<relpath>:<enclosing function>"
	what string // the construct, for the failure message
	line int
}

// pbpush3ScanProducers walks the module and returns every provider-bound payload construct,
// keyed by enclosing function, plus per-matcher hit counts for the anti-vacuity arm.
func pbpush3ScanProducers(t *testing.T, root string) (map[string][]pbpush3Site, map[string]int) {
	t.Helper()
	sites := map[string][]pbpush3Site{}
	hits := map[string]int{}
	fset := token.NewFileSet()

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".claude", ".codex", ".gradle", "build", "dist", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil // an unparseable file is some other guard's problem
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		ast.Inspect(f, func(n ast.Node) bool {
			what, kind, ok := pbpush3Match(n)
			if !ok {
				return true
			}
			hits[kind]++
			pos := fset.Position(n.Pos())
			key := rel + ":" + pbpush3EnclosingFunc(f, n.Pos())
			sites[key] = append(sites[key], pbpush3Site{key: key, what: what, line: pos.Line})
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk the module: %v", err)
	}
	return sites, hits
}

// pbpush3Match reports whether n is a provider-bound payload construct, what it is, and which
// matcher found it (the matcher name feeds the per-matcher anti-vacuity check).
func pbpush3Match(n ast.Node) (what, kind string, ok bool) {
	switch v := n.(type) {
	case *ast.CompositeLit:
		if pbpush3TypeName(v.Type) == pbpush3PayloadType {
			return pbpush3PayloadType + " literal", "literal", true
		}
	case *ast.CallExpr:
		name := pbpush3CalleeName(v.Fun)
		if pbpush3ProducerCalls[name] {
			return "call " + name + "()", "call:" + name, true
		}
	}
	return "", "", false
}

func pbpush3TypeName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return t.Sel.Name
	}
	return ""
}

func pbpush3CalleeName(e ast.Expr) string {
	switch f := e.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}

// pbpush3EnclosingFunc names the function containing pos, receiver included, so two producers
// in one file are two entries. A construct outside any function body is attributed to the
// package block rather than dropped.
func pbpush3EnclosingFunc(f *ast.File, pos token.Pos) string {
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || pos < fd.Pos() || pos > fd.End() {
			continue
		}
		if fd.Recv == nil || len(fd.Recv.List) == 0 {
			return fd.Name.Name
		}
		return "(" + pbpush3RecvName(fd.Recv.List[0].Type) + ")." + fd.Name.Name
	}
	return "<package-level>"
}

func pbpush3RecvName(e ast.Expr) string {
	if star, ok := e.(*ast.StarExpr); ok {
		return "*" + pbpush3TypeName(star.X)
	}
	return pbpush3TypeName(e)
}

// TestPBPUSH3_EveryProducerOnThePushChannelIsEnumerated is the fence. A fourth producer fails
// it BY NAME, in both directions.
func TestPBPUSH3_EveryProducerOnThePushChannelIsEnumerated(t *testing.T) {
	root := pbpush3ModuleRoot(t)
	sites, hits := pbpush3ScanProducers(t, root)

	// ANTI-VACUITY, first and per matcher. A fence that guards nothing while exiting 0 has
	// shipped in this project before, and a matcher that silently stops matching narrows the
	// scan without narrowing the claim.
	if len(sites) == 0 {
		t.Fatal("the scan found NO provider-bound payload construct anywhere in the module; the " +
			"matchers or the walk are wrong and this fence is guarding nothing")
	}
	if hits["literal"] == 0 {
		t.Fatalf("no %s composite literal was found anywhere. Either the payload type was renamed "+
			"-- update pbpush3PayloadType -- or the walk no longer reaches the relay. Until then this "+
			"fence cannot see the producers it exists to enumerate.", pbpush3PayloadType)
	}
	for name := range pbpush3ProducerCalls {
		if hits["call:"+name] == 0 {
			t.Errorf("the matcher for %s() matched nothing anywhere in the module. A matcher that "+
				"stopped matching makes this enumeration quieter without making it smaller: either the "+
				"call was renamed (update pbpush3ProducerCalls) or that hop no longer exists (remove it).",
				name)
		}
	}

	var unlisted []string
	for key, found := range sites {
		if _, ok := pbpush3Producers[key]; ok {
			continue
		}
		var what []string
		for _, s := range found {
			what = append(what, fmt.Sprintf("%s at line %d", s.what, s.line))
		}
		unlisted = append(unlisted, key+" ["+strings.Join(what, ", ")+"]")
	}
	sort.Strings(unlisted)
	if len(unlisted) > 0 {
		t.Errorf("%d function(s) can put a payload in front of the push provider and are NOT "+
			"enumerated:\n  %s\n\n"+
			"PB-PUSH-3's subject is the PROVIDER, so the constant-size property is quantified over "+
			"every producer into that channel -- not over the one component the existing fence "+
			"constructs. That is how the presence sweep's ciphertext-free wake shipped unfenced in "+
			"normal operation (ADR-007 B87, residual 4.23). Settle this producer's SHAPE first "+
			"(indistinguishable in size from the canonical wake, or refused), drive it from "+
			"pbpush3_channel_test.go, then add it to pbpush3Producers WITH the covering test named. "+
			"Adding the key alone silences the only thing that asks.",
			len(unlisted), strings.Join(unlisted, "\n  "))
	}

	// The other direction. A listed producer the tree no longer has is a list that has stopped
	// describing the tree, and an entry nobody can see is an entry nobody rechecks.
	var vanished []string
	for key := range pbpush3Producers {
		if len(sites[key]) == 0 {
			vanished = append(vanished, key)
		}
	}
	sort.Strings(vanished)
	if len(vanished) > 0 {
		t.Errorf("%d enumerated producer(s) no longer exist:\n  %s\n\n"+
			"Remove them. A ledger that keeps names the tree has dropped is a ledger nobody trusts, "+
			"and the next reader cannot tell which entries are still load-bearing.",
			len(vanished), strings.Join(vanished, "\n  "))
	}
}

// TestPBPUSH3_EveryEnumeratedProducerNamesTheTestThatJudgesIt is what makes the enumeration
// mean "the set the invariant covers" rather than "a set somebody wrote down". Without it the
// ledger degrades into bare keys people add to quiet a failure -- the exact rot this project
// has hit before -- and a producer could be listed while nothing measured its shape.
func TestPBPUSH3_EveryEnumeratedProducerNamesTheTestThatJudgesIt(t *testing.T) {
	root := pbpush3ModuleRoot(t)
	known := pbpush3TestFuncNames(t, root)
	if len(known) == 0 {
		t.Fatal("no test function names were collected from the module; the coverage link below " +
			"would accept any string at all")
	}

	for key, reason := range pbpush3Producers {
		if len(strings.TrimSpace(reason)) < 60 {
			t.Errorf("pbpush3Producers[%q] has no usable reason (%q): each entry records WHAT that "+
				"call site puts on the provider channel, because the size property is about shapes and a "+
				"bare key records none.", key, reason)
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(reason), "FUNNEL") {
			continue // a convergence point produces nothing of its own to judge
		}
		name, ok := pbpush3CoveredBy(reason)
		if !ok {
			t.Errorf("pbpush3Producers[%q] names no covering test. A producer with no `covered-by: "+
				"<TestName>` is a producer whose SHAPE nothing measures -- which is the state PB-PUSH-3 "+
				"was already in.", key)
			continue
		}
		if !known[name] {
			t.Errorf("pbpush3Producers[%q] is covered-by %q, which is not a test function in this "+
				"module. A coverage claim pointing at nothing is worse than none: it reads as measured.",
				key, name)
		}
	}
}

// pbpush3CoveredBy extracts the test named by a `covered-by: <TestName>` reason.
func pbpush3CoveredBy(reason string) (string, bool) {
	const marker = "covered-by:"
	i := strings.Index(reason, marker)
	if i < 0 {
		return "", false
	}
	name := strings.TrimSpace(reason[i+len(marker):])
	if cut := strings.IndexAny(name, " \t\n,;"); cut >= 0 {
		name = name[:cut]
	}
	if name == "" {
		return "", false
	}
	return name, true
}

// pbpush3TestFuncNames collects every top-level Test function in the module, so a covered-by
// claim can be checked against tests that actually exist -- including ones in other packages,
// which is where the channel test has to live.
func pbpush3TestFuncNames(t *testing.T, root string) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".claude", ".codex", ".gradle", "build", "dist", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if ok && fd.Recv == nil && strings.HasPrefix(fd.Name.Name, "Test") {
				names[fd.Name.Name] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk the module for test names: %v", err)
	}
	return names
}
