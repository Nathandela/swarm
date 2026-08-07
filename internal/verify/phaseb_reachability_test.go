package verify_test

// ADR-007 B94 / residual 4.25 -- THE INSTRUMENT FOR "CORRECT CODE NOTHING POINTS AT".
//
// Five rounds found fourteen fences that could not fail by testing CODE against REQUIREMENTS.
// B94 found the mirror image: three requirements fenced against internal/remote/transport, a
// package whose Session type has ZERO production constructions. PB-NET-4's backoff numbers and
// PB-NET-7's "every call times out" are both true of that object and false of the shipped
// phone. Every one of those discoveries was a human tracing a symbol to zero callers by hand.
//
// NOTHING MECHANICAL COULD SEE IT. `go build` is happy -- the package compiles and is even
// IMPORTED, because the gateway uses two helpers out of it. golangci-lint's `unused` does not
// flag exported identifiers at all, so the whole class is invisible to the linter BY
// CONSTRUCTION. That is the gap this closes.
//
// WHY REACHABILITY AND NOT REFERENCE COUNTING. The first formulation tried here -- "every
// package named in an evidence file is reachable from a main" -- is WRONG, and it is worth
// recording why, because it is the obvious formulation and it PASSES on the known-dead package:
// internal/remote/transport IS reachable, via the two helpers internal/remotegw imports. A
// second formulation -- "every exported symbol is referenced from another package" -- was
// measured at 395 of 852 symbols, a 46% false-positive rate, because a type that only ever
// flows through `:=` inference is never named qualified. Neither is a fence; both are noise.
//
// WHAT THIS DOES: a TYPED call graph (x/tools SSA + RTA) from the real production entry points,
// and every exported function or method that the graph cannot reach must be listed here WITH A
// REASON. A symbol that goes dead fails on the day it does, naming the requirements whose
// evidence cites its package.
//
// THE ROOT SET IS THE WHOLE DESIGN, and getting it wrong fails in both directions -- too narrow
// and the phone reads dead, too broad and nothing ever fails:
//
//   - every cmd/... main() and its init(), which is where the machine side starts;
//   - every exported function AND exported method on an exported type of `mobile`, because the
//     gomobile facade is called from Java. Its methods are not called from any Go main and a
//     root set of mains alone reports the entire phone core as dead. This is verified rather
//     than assumed: the soundness control below pins that phonecore.SealInputData -- reachable
//     ONLY through App.SendInput -> sendInputFrame -- is live.
//
// WHAT IT DELIBERATELY DOES NOT DECIDE, stated so nobody takes it for more than it is:
//
//   - IT IS PER-SYMBOL, NOT PER-SUBGRAPH. phonecore.NewOpQueue is CALLED from Core.New, so it
//     is reachable and this test is silent about it -- even though everything the queue feeds
//     is dead (B90). A live one-hop reference into a dead subgraph is exactly the shape it
//     cannot see, and B90's finding would NOT have been caught here. B92's RetryFor and B94's
//     transport.Session would.
//   - RTA OVER-APPROXIMATES interface dispatch. That is the safe direction for a fence -- it
//     reports FEWER dead symbols than exist, never more -- but it means a clean run is not a
//     proof of liveness.
//   - REFLECTION IS INVISIBLE to any static call graph, which is why the fmt/json method names
//     below are excluded by RULE rather than by allowlist: `String()` on a redacting type is
//     called by fmt and no graph can see it. Excluding them by name is a soundness hole with a
//     stated shape, which is better than 30 allowlist rows that teach the reader to add more.

import (
	"fmt"
	"go/types"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/callgraph/rta"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

const b94Module = "github.com/Nathandela/swarm"

// b94FacadePkg is the gomobile boundary: its exported surface is called from Java, so it is a
// ROOT rather than a subject.
const b94FacadePkg = b94Module + "/mobile"

// b94ReflectionMethods are dispatched by the standard library through reflection, so no static
// call graph can see their call sites. Excluded by RULE, not by allowlist -- see the header.
var b94ReflectionMethods = map[string]bool{
	"String": true, "GoString": true, "Error": true, "Format": true,
	"MarshalJSON": true, "UnmarshalJSON": true, "MarshalText": true, "UnmarshalText": true,
}

// b94HarnessPkgs exist to be driven by tests. Their exported surface having no production
// caller is the POINT of the package, not a finding, so they are exempt wholesale rather than
// symbol by symbol.
var b94HarnessPkgs = map[string]string{
	b94Module + "/internal/phonesim":          "the phone simulator: a test double for the handset, driven only by tests and the E2E rigs.",
	b94Module + "/internal/fakeagent":         "the scripted fake agent: a test fixture by construction.",
	b94Module + "/internal/adapter/fixtureio": "adapter characterization fixtures, loaded only by the conformance harness.",
}

// b94Allowed is the ledger: an exported symbol with no production caller, and WHY that is
// acceptable. It is bidirectional -- an entry that becomes reachable fails too, so a symbol
// that gets wired up cannot leave a stale exemption behind.
var b94Allowed = map[string]string{
	// ---- 2026-08-02 merge of main (v0.6 perf wave) into this line ------------------------
	"github.com/Nathandela/swarm/internal/transcript.Writer.Write": "the io.Writer face of the transcript writer; the shim's hot path moved to WriteOwned (v0.6 perf: caller-owned buffer, no copy) and Write remains the copying general-purpose entry, exercised by the transcript package's own tests.",
	"github.com/Nathandela/swarm/internal/testbin.Binaries.Build":  "test-support package from main: builds per-test binaries for e2e suites; production never builds test binaries, by definition.",
	// ---- internal/remote/crypto: the frozen Phase-1 primitive's own API ------------------
	// The machine's shipped identity path is internal/remote/machineid; the phone's keystore
	// is the Android Keystore behind mobile/keycustody.go. These are the primitive's file-backed
	// equivalents, kept because the package is frozen and heavily exercised (GenerateIdentity
	// and NewFileKeyStore appear in 34 and 36 test files respectively).
	"github.com/Nathandela/swarm/internal/remote/crypto.GenerateIdentity":            "frozen Phase-1 identity API; the shipped machine path is internal/remote/machineid. 34 test files.",
	"github.com/Nathandela/swarm/internal/remote/crypto.LoadIdentity":                "as GenerateIdentity: the file-backed identity loader, superseded on the machine side by machineid.Load.",
	"github.com/Nathandela/swarm/internal/remote/crypto.Identity.Save":               "as GenerateIdentity: the file-backed identity writer.",
	"github.com/Nathandela/swarm/internal/remote/crypto.NewFileKeyStore":             "frozen file keystore; the phone uses the Android Keystore, the machine uses machineid. 36 test files.",
	"github.com/Nathandela/swarm/internal/remote/crypto.NewFileKeyStoreFromMaterial": "as NewFileKeyStore, from in-memory material.",
	"github.com/Nathandela/swarm/internal/remote/crypto.OpenFileKeyStore":            "as NewFileKeyStore.",
	"github.com/Nathandela/swarm/internal/remote/crypto.LivePrologue":                "Noise prologue helper for the LIVE handshake tier; no shipped caller since the live tier rides the mailbox.",
	"github.com/Nathandela/swarm/internal/remote/crypto.NoiseSession.Rekey":          "Noise rekey, reachable only on a session the shipped mailbox tier does not open.",
	"github.com/Nathandela/swarm/internal/remote/crypto.NoiseSession.Suite":          "as NoiseSession.Rekey.",

	// ---- internal/remotegw: openers and test seams ---------------------------------------
	"github.com/Nathandela/swarm/internal/remotegw.OpenCommandEnvelope":      "inverse of the seal, used by the harnesses that assert what the gateway emitted.",
	"github.com/Nathandela/swarm/internal/remotegw.OpenInputFrame":           "as OpenCommandEnvelope.",
	"github.com/Nathandela/swarm/internal/remotegw.OpenRemoteCommand":        "as OpenCommandEnvelope; the guarded form below is what the bridge calls.",
	"github.com/Nathandela/swarm/internal/remotegw.OpenRemoteCommandGuarded": "as OpenCommandEnvelope.",
	"github.com/Nathandela/swarm/internal/remotegw.CommandBridge.PollOnce":   "single-step seam so a test can drive one poll instead of the Run loop.",
	// CommandBridge.Err's row is GONE because the symbol is now REACHABLE: Service.Err joins
	// it with RelaySink's and PushNotifier's, and cmd/swarm-remote's watchDegraded prints the
	// result to the unit's log. Its old reason ("last-error accessor for tests; Run surfaces
	// the same error to its caller") was the defect ADR-007 B114 recorded -- Run does NOT
	// surface it, which is why a gateway dropping every keystroke was invisible.
	"github.com/Nathandela/swarm/internal/remotegw.Service.Gateway":       "accessor exposing an assembled sub-component to tests.",
	"github.com/Nathandela/swarm/internal/remotegw.Service.CommandBridge": "as Service.Gateway.",
	"github.com/Nathandela/swarm/internal/remotegw.Service.PushNotifier":  "as Service.Gateway.",

	// ---- internal/remote/relay: deliberately test-only, already fenced --------------------
	// client.go says of Dial: "NO production caller may reach it, which internal/remote/
	// transport's productiondial_test.go enforces at the call site." This agrees with that
	// fence from the other direction, which is the corroboration worth having.
	"github.com/Nathandela/swarm/internal/remote/relay.Dial":                "policy-free dial, documented at client.go:429 as having no production caller; productiondial_test.go fences the call site.",
	"github.com/Nathandela/swarm/internal/remote/relay.DialRaw":             "as Dial.",
	"github.com/Nathandela/swarm/internal/remote/relay.Server.URL":          "in-process test relay's address; 83 test files. cmd/swarm-relay listens on a configured address instead.",
	"github.com/Nathandela/swarm/internal/remote/relay.Server.MailboxDepth": "test assertion helper over server state.",
	"github.com/Nathandela/swarm/internal/remote/relay.WithClock":           "injection option; the shipped binary takes the real clock.",
	"github.com/Nathandela/swarm/internal/remote/relay.WithLogWriter":       "injection option; the shipped binary logs to its own sink.",
	"github.com/Nathandela/swarm/internal/remote/relay.WithSourceKeyFunc":   "injection option for rate-key attribution in tests.",
	"github.com/Nathandela/swarm/internal/remote/relay.WriteConfigFile":     "config writer with no shipped caller: cmd/swarm-relay READS a config an operator authored. RECORDED AS A GAP -- the runbook tells operators to write it by hand.",

	// ---- internal/phonecore ----------------------------------------------------------------
	"github.com/Nathandela/swarm/internal/phonecore.AcceptGrant":                   "grant acceptance reachable in production through MailboxRouter.AcceptCommit, not through this entry point.",
	"github.com/Nathandela/swarm/internal/phonecore.InsecureCleartextSealer":       "named for what it is: a cleartext sealer for tests. No production caller is the REQUIREMENT here, not an accident.",
	"github.com/Nathandela/swarm/internal/phonecore.NewJournalReceiver":            "the standalone receiver; production drives journal frames through MailboxRouter.",
	"github.com/Nathandela/swarm/internal/phonecore.JournalReceiver.Accept":        "as NewJournalReceiver.",
	"github.com/Nathandela/swarm/internal/phonecore.JournalReceiver.SeedHighWater": "as NewJournalReceiver.",
	"github.com/Nathandela/swarm/internal/phonecore.NewMailboxRouter":              "production constructs the router inside Core.New rather than through this exported form.",
	"github.com/Nathandela/swarm/internal/phonecore.OpenControlReply":              "opener used by harnesses to read what the gateway sealed.",
	"github.com/Nathandela/swarm/internal/phonecore.OpenJournalEnvelope":           "as OpenControlReply.",

	// ---- superseded by a later, shipped equivalent -----------------------------------------
	"github.com/Nathandela/swarm/internal/protocol.Serve":              "superseded: skeleton/serve.go calls ServeRemoteWithID; nothing calls the id-less forms.",
	"github.com/Nathandela/swarm/internal/protocol.ServeRemote":        "superseded by ServeRemoteWithID (skeleton/serve.go:242).",
	"github.com/Nathandela/swarm/internal/protocol.FromDaemon":         "superseded: skeleton/api.go:40 documents itself as \"a leak-free, self-contained equivalent of protocol.FromDaemon\".",
	"github.com/Nathandela/swarm/internal/remote/grant.ParseBootstrap": "superseded by internal/remote/grantwire.ParseBootstrap, which phonecore/snapshot.go:743 calls. This one is reached only by phonesim.",

	// ---- accessors and helpers with no shipped caller ---------------------------------------
	"github.com/Nathandela/swarm/internal/skeleton.Daemon.Core":             "accessor for the assembled core, used by the E2E rigs.",
	"github.com/Nathandela/swarm/internal/skeleton.Daemon.SocketPath":       "as Daemon.Core.",
	"github.com/Nathandela/swarm/internal/adapter.Conformance":              "the adapter conformance harness: a test contract by construction.",
	"github.com/Nathandela/swarm/internal/adapter.CheckConformance":         "as Conformance.",
	"github.com/Nathandela/swarm/internal/adapter.CheckInteractionFixture":  "as Conformance: ADR-010's obligation-3 corpus half and obligation 4, replaying a recorded fixture's payloads through Interactions. Its two siblings above -- AsInteractionSource and Interaction.Validate -- are NOT listed, because internal/skeleton's interaction producer calls both in production (interaction.go).",
	"github.com/Nathandela/swarm/internal/idempotency.Open":                 "production opens the store via OpenWithOptions; this is the default-options form.",
	"github.com/Nathandela/swarm/internal/transcript.Writer.Dropped":        "drop counter read by the transcript tests; the shipped writer reports drops through its metrics.",
	"github.com/Nathandela/swarm/internal/remote/pairing.Machine.Listening": "readiness probe for the rendezvous, used by tests to avoid a sleep.",
	"github.com/Nathandela/swarm/internal/remote/qrterm.Symbol.ECC":         "error-correction level accessor, asserted by the QR tests.",
	"github.com/Nathandela/swarm/internal/remote/supervise.ShouldRestart":   "restart predicate; the shipped supervisors (launchd, systemd) apply their own policy from the rendered unit.",

	// ---- internal/design: the PB-TOK-7 derivation pipeline --------------------------------
	// None of these run in the shipped app -- the Android module never executes Go at
	// runtime. They run in android/gate/s22_derived_test.go, the PB-TOK-7 gate that resolves
	// every derived colour from the staged tokens and asserts no Kotlin/XML literal in
	// src/main equals one, which is a real production-adjacent consumer (a go test binary
	// walking the shipped app's sources) that this test's root set -- cmd/... mains and the
	// mobile facade -- does not and structurally cannot model.
	"github.com/Nathandela/swarm/internal/design.Derivations":        "enumerates the derivation table; called directly by android/gate/s22_derived_test.go (derivedValues, TestPBTOK7_TheDerivationsAreReachableFromTheOrigin) to build the set of forbidden literals for the PB-TOK-7 scan.",
	"github.com/Nathandela/swarm/internal/design.Derivation.Resolve": "computes one derivation's value; called directly by the same gate (derivedValues:127, TestPBTOK7_TheDerivationsAreReachableFromTheOrigin:274) over the staged tokens.json.",
	"github.com/Nathandela/swarm/internal/design.RGBA.Hex":           "renders Resolve's output for comparison; called directly by android/gate/s22_derived_test.go:131 to turn the resolved RGBA into the hex spelling matched against scanned literals.",
	"github.com/Nathandela/swarm/internal/design.ParseColor":         "called from within Derivation.Resolve (derive.go:192,202, allowed above) to parse each token's value -- deleting it breaks Resolve and therefore the PB-TOK-7 gate. Its exact grammar (hex, rgba(), transparent, strict rejection of the 3-digit shorthand) is separately pinned by TestPBTOK7_TheColourCodecRoundTrips.",
	"github.com/Nathandela/swarm/internal/design.Mix":                "called from within Derivation.Resolve (derive.go:206, allowed above) to blend the parsed colours -- deleting it breaks Resolve for the same reason. Its premultiplied-alpha blend semantics are separately pinned by TestPBTOK7_MixingWithAColourBlendsRGBAndMixingWithTransparentScalesAlpha and TestPBTOK7_TheBlendCanActuallyFail.",

	// ---- NOT ALLOWLISTED, DELIBERATELY -------------------------------------------------------
	// internal/remote/transport's 16 symbols are ADR-007 B94's open defect. Listing them here
	// would be the move this whole instrument exists to prevent: an exemption written to make a
	// check green over code that three requirements were fenced against. They stay failing until
	// the package is deleted or wired.
}

func TestB94_EveryExportedSymbolIsReachableFromProduction(t *testing.T) {
	root := repoRoot(t)
	pkgs, err := packages.Load(&packages.Config{
		Mode: packages.LoadAllSyntax, Dir: root, Tests: false,
	}, "./...")
	if err != nil {
		t.Fatalf("B94: cannot load packages: %v", err)
	}
	var loadErrs int
	packages.Visit(pkgs, nil, func(p *packages.Package) { loadErrs += len(p.Errors) })
	if loadErrs > 0 {
		t.Fatalf("B94: %d package load errors; a partial graph would report live code as dead", loadErrs)
	}

	prog, _ := ssautil.AllPackages(pkgs, ssa.InstantiateGenerics)
	prog.Build()

	roots, nMain, nFacade := b94Roots(prog)

	// ---- ANTI-VACUITY, before any verdict -------------------------------------------
	// A reachability computation that returns everything passes "no violations" perfectly.
	if nMain < 5 {
		t.Fatalf("B94 VACUOUS: only %d main() roots found; the cmd/ tree has more, so the walk is broken", nMain)
	}
	if nFacade < 20 {
		t.Fatalf("B94 VACUOUS: only %d gomobile facade roots found; App alone exports more than that, "+
			"and a missing facade root reports the whole phone core as dead", nFacade)
	}

	res := rta.Analyze(roots, true)
	reach := map[*ssa.Function]bool{}
	for fn := range res.Reachable {
		reach[fn] = true
	}
	if len(reach) < 5000 {
		t.Fatalf("B94 VACUOUS: RTA reached only %d functions; the graph did not build", len(reach))
	}

	// SOUNDNESS CONTROL: symbols reachable ONLY through a gomobile facade METHOD. If these
	// read dead, the root set is wrong and every verdict below is noise.
	for _, q := range []string{
		b94Module + "/internal/phonecore.SealInputData",
		b94Module + "/internal/phonecore.SealCommandEnvelope",
		b94Module + "/internal/remote/relay.DialSecure",
	} {
		if !b94Reachable(reach, q) {
			t.Fatalf("B94 CONTROL FAILED: %s is reachable only through the gomobile facade and reads "+
				"DEAD, so the facade roots are not wired: every finding below is an artifact", q)
		}
	}

	// ---- the verdict ----------------------------------------------------------------
	type finding struct{ pkg, sym string }
	var dead []finding
	checked := 0
	// Keyed on the QUALIFIED NAME, not the *ssa.Function: prog.MethodValue over the POINTER
	// method set synthesizes a wrapper for a value-receiver method, so one source-level method
	// yields two distinct functions and RTA marks only the one actually called. Deduping on the
	// pointer reported every value-receiver method as dead. The bidirectional arm below is what
	// caught it -- crypto.Command.Canonical came back both dead and reachable in one run.
	anyLive := map[string]bool{}
	order := []finding{}
	seenName := map[string]bool{}

	for _, sp := range prog.AllPackages() {
		if sp == nil || sp.Pkg == nil {
			continue
		}
		p := sp.Pkg.Path()
		if !strings.HasPrefix(p, b94Module) || sp.Pkg.Name() == "main" || p == b94FacadePkg {
			continue
		}
		if _, ok := b94HarnessPkgs[p]; ok {
			continue
		}
		for _, fn := range b94ExportedFuncs(prog, sp) {
			if fn.Signature.Recv() != nil && b94ReflectionMethods[fn.Name()] {
				continue
			}
			name := b94SymbolName(fn)
			q := p + "." + name
			if reach[fn] {
				anyLive[q] = true
			}
			if !seenName[q] {
				seenName[q] = true
				checked++
				order = append(order, finding{p, name})
			}
		}
	}
	for _, f := range order {
		if !anyLive[f.pkg+"."+f.sym] {
			dead = append(dead, f)
		}
	}
	if checked < 300 {
		t.Fatalf("B94 VACUOUS: only %d exported symbols examined; the module has far more", checked)
	}

	// Bidirectional: an allowlist entry that is now reachable must be deleted.
	var stale []string
	for k := range b94Allowed {
		if anyLive[k] {
			stale = append(stale, k)
		}
	}

	byPkg := map[string][]string{}
	var flat [][2]string
	for _, d := range dead {
		if _, ok := b94Allowed[d.pkg+"."+d.sym]; ok {
			continue
		}
		byPkg[d.pkg] = append(byPkg[d.pkg], d.sym)
		flat = append(flat, [2]string{d.pkg, d.sym})
	}
	cited := b94EvidenceJoin(t, root, flat)

	if len(byPkg) == 0 && len(stale) == 0 {
		t.Logf("B94: %d exported symbols examined, %d unreachable and all accounted for", checked, len(dead))
		return
	}

	var b strings.Builder
	if len(byPkg) > 0 {
		fmt.Fprintf(&b, "\n%d package(s) export symbols NO PRODUCTION ENTRY POINT CAN REACH.\n\n",
			len(byPkg))
		var keys []string
		for k := range byPkg {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return len(byPkg[keys[i]]) > len(byPkg[keys[j]]) })
		for _, k := range keys {
			sort.Strings(byPkg[k])
			short := strings.TrimPrefix(k, b94Module+"/")
			fmt.Fprintf(&b, "  %s -- %d unreachable exported symbol(s):\n", short, len(byPkg[k]))
			for _, s := range byPkg[k] {
				fmt.Fprintf(&b, "      %s.%s\n", short, s)
				if reqs := cited[k+"."+s]; len(reqs) > 0 {
					fmt.Fprintf(&b, "          !! evidence for %s names this symbol as code\n",
						strings.Join(reqs, ", "))
				}
			}
			fmt.Fprintf(&b, "\n")
		}
		fmt.Fprintf(&b, "Each symbol is either (a) dead and to be DELETED, or (b) legitimately\n"+
			"unreferenced, in which case add it to b94Allowed WITH A STATED REASON.\n"+
			"Do not widen the root set to make this pass: a root set that reaches everything\n"+
			"is how PB-NET-4 read met for five rounds.\n")
	}
	for _, s := range stale {
		fmt.Fprintf(&b, "\nSTALE EXEMPTION: %s is in b94Allowed but is now REACHABLE. Delete the row.\n", s)
	}
	t.Fatal(b.String())
}

func b94SymbolName(fn *ssa.Function) string {
	if recv := fn.Signature.Recv(); recv != nil {
		t := recv.Type()
		if p, ok := t.(*types.Pointer); ok {
			t = p.Elem()
		}
		if n, ok := t.(*types.Named); ok {
			return n.Obj().Name() + "." + fn.Name()
		}
	}
	return fn.Name()
}

func b94Reachable(reach map[*ssa.Function]bool, q string) bool {
	for fn := range reach {
		if fn.Pkg != nil && fn.Pkg.Pkg != nil && fn.Pkg.Pkg.Path()+"."+fn.Name() == q {
			return true
		}
	}
	return false
}

func b94ExportedFuncs(prog *ssa.Program, sp *ssa.Package) []*ssa.Function {
	var out []*ssa.Function
	for _, mem := range sp.Members {
		switch m := mem.(type) {
		case *ssa.Function:
			if m.Object() != nil && m.Object().Exported() {
				out = append(out, m)
			}
		case *ssa.Type:
			if !m.Object().Exported() {
				continue
			}
			named, ok := m.Type().(*types.Named)
			if !ok {
				continue
			}
			for _, T := range []types.Type{named, types.NewPointer(named)} {
				ms := prog.MethodSets.MethodSet(T)
				for i := 0; i < ms.Len(); i++ {
					sel := ms.At(i)
					if !sel.Obj().Exported() {
						continue
					}
					if fn := prog.MethodValue(sel); fn != nil && fn.Object() != nil {
						out = append(out, fn)
					}
				}
			}
		}
	}
	return out
}

var b94TraceRow = regexp.MustCompile(`^\|\s*(PB-[A-Z0-9]+-\d+)\s*\|[^|]*\|[^|]*\|\s*` + "`([^`]+)`")

// b94EvidenceJoin maps "pkgPath.Symbol" to the requirements whose cited evidence file names
// BOTH the package and that symbol. That conjunction is the tightening that makes the message
// worth reading: a join on the PACKAGE alone returned 80-requirement lists, because an
// architecture or closure document mentions every package in the tree. A reader handed 80 rows
// deletes the test; a reader handed "PB-NET-2, PB-NET-3, PB-NET-6 cite evidence naming
// SendLive, and SendLive is unreachable" has something to act on.
func b94EvidenceJoin(t *testing.T, root string, dead [][2]string) map[string][]string {
	body, err := os.ReadFile(filepath.Join(root, "docs", "verification", "remote-phaseB-traceability.md"))
	if err != nil {
		t.Fatalf("B94: cannot read the traceability table: %v", err)
	}
	evidenceFor := map[string][]string{} // evidence path -> requirement ids
	for _, line := range strings.Split(string(body), "\n") {
		if m := b94TraceRow.FindStringSubmatch(line); m != nil {
			evidenceFor[m[2]] = append(evidenceFor[m[2]], m[1])
		}
	}
	text := map[string]string{}
	for path := range evidenceFor {
		if blob, err := os.ReadFile(filepath.Join(root, path)); err == nil {
			text[path] = string(blob)
		}
	}
	out := map[string][]string{}
	for _, d := range dead {
		pkg, sym := d[0], d[1]
		q := pkg + "." + sym
		if i := strings.LastIndex(sym, "."); i >= 0 {
			sym = sym[i+1:] // Type.Method -> Method; evidence prose names the method
		}
		// A short identifier ("New", "Dial", "State") matches prose everywhere; only
		// distinctive names carry signal, and a missed join is silence rather than a lie.
		if len(sym) < 6 {
			continue
		}
		shortPkg := strings.TrimPrefix(pkg, b94Module+"/")
		// Backticked only: the symbol must be named AS CODE, not as English. "Gateway" and
		// "Session" are ordinary words in this prose, and a bare-word match makes the list
		// long enough that a reader stops reading it.
		word := regexp.MustCompile("`[^`\n]*\\b" + regexp.QuoteMeta(sym) + "\\b[^`\n]*`")
		for path, reqs := range evidenceFor {
			blob := text[path]
			if strings.Contains(blob, shortPkg) && word.MatchString(blob) {
				out[q] = append(out[q], reqs...)
			}
		}
		sort.Strings(out[q])
		out[q] = b94Uniq(out[q])
	}
	return out
}

func b94Uniq(in []string) []string {
	var out []string
	for i, s := range in {
		if i == 0 || s != in[i-1] {
			out = append(out, s)
		}
	}
	return out
}

func b94Roots(prog *ssa.Program) (roots []*ssa.Function, nMain, nFacade int) {
	for _, sp := range prog.AllPackages() {
		if sp == nil || sp.Pkg == nil {
			continue
		}
		if sp.Pkg.Name() == "main" {
			if fn := sp.Func("main"); fn != nil {
				roots = append(roots, fn)
				nMain++
			}
			if fn := sp.Func("init"); fn != nil {
				roots = append(roots, fn)
			}
			continue
		}
		if sp.Pkg.Path() != b94FacadePkg {
			continue
		}
		for _, fn := range b94ExportedFuncs(prog, sp) {
			roots = append(roots, fn)
			nFacade++
		}
	}
	return roots, nMain, nFacade
}
