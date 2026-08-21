package verify_test

// WAVE R8 CLOSING ROUND -- THE PARKED CONTROL HALF, AS A LEDGER ROW A READER CAN CHECK
// (closing review, findings 3 and 4; ADR-017 amendments C0 and C1).
//
// WHAT R8 SHIPS. The READ half: a capability-routed, machine-sanitized terminal view, watched
// read-only from the phone. The CONTROL half -- `terminal_input`, the generation/keepalive
// plane, any take-control affordance -- is PARKED as its own slice.
//
// THE FACT THIS FILE PINS, and it is the one three rounds of evidence got wrong.
// `protocol.TerminalInputSink` has NO PRODUCTION IMPLEMENTATION. `internal/skeleton.coreAPI`
// is what is passed as `srv.d`, and it has no `TerminalInput` method, so `handleTerminalInput`
// takes the `op_not_implemented` arm for EVERY frame the product can produce. Round 3's
// "bytes at the PTY" were bytes at `r8Backend.written`, an in-process fake; the accurate claim
// is "bytes counted at the TerminalInputSink seam over the real gateway composition".
//
// THE POSITIVE COROLLARY IS THE REASON THIS IS SAFE TO SHIP: **the raw-input attack surface in
// the shipped product is currently ZERO.** Nothing can type into a terminal from a phone,
// because nothing on the machine would accept it.
//
// WHY IT IS A TEST AND NOT A PARAGRAPH. B94's own header is about "correct code nothing points
// at", and its ledger exists because an exemption written to make a check green over
// unreachable code is the failure mode. The symmetric failure is an EVIDENCE FILE claiming a
// seam is wired when it is not, which is what happened here. So the claim is mechanical: this
// test fails the day a production implementation appears, and its failure message carries the
// precondition that must be closed BEFORE one may.
//
// It is deliberately NOT in b94Allowed. That map is keyed by symbol and answers "no production
// CALLER"; this is a different question -- "no production IMPLEMENTOR of an interface" -- and
// folding it into the map would make the map mean two things.

import (
	"go/types"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// TestR8R4Parked_TheControlSinkHasNoProductionImplementation is the ledger row.
//
// When it fails, it is because someone wired the parked half. That is allowed -- but not
// before ADR-017 amendment C1 is closed, and the message says so rather than leaving the next
// agent to rediscover it.
func TestR8R4Parked_TheControlSinkHasNoProductionImplementation(t *testing.T) {
	root := repoRoot(t)
	pkgs, err := packages.Load(&packages.Config{
		Mode: packages.LoadAllSyntax, Dir: root, Tests: false,
	}, "./...")
	if err != nil {
		t.Fatalf("cannot load packages: %v", err)
	}

	var sink *types.Interface
	byPath := map[string]*packages.Package{}
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		byPath[p.PkgPath] = p
		if p.PkgPath != "github.com/Nathandela/swarm/internal/protocol" || p.Types == nil {
			return
		}
		obj := p.Types.Scope().Lookup("TerminalInputSink")
		if obj == nil {
			return
		}
		iface, _ := obj.Type().Underlying().(*types.Interface)
		sink = iface
	})
	if sink == nil {
		t.Fatalf("protocol.TerminalInputSink no longer exists. If the control half was reshaped, this " +
			"ledger row has to be reshaped with it -- do not delete it: it is the record that the raw " +
			"input surface is zero.")
	}
	// ANTI-VACUITY: an interface with no methods is satisfied by everything and an
	// unresolvable one by nothing. Either would make the scan below meaningless.
	if sink.NumMethods() != 1 {
		t.Fatalf("TerminalInputSink has %d methods, want 1; the scan below assumes the single "+
			"TerminalInput(string, []byte) error seam", sink.NumMethods())
	}

	var implementors []string
	var scanned int
	for path, p := range byPath {
		if p.Types == nil || !strings.HasPrefix(path, "github.com/Nathandela/swarm/") {
			continue
		}
		scope := p.Types.Scope()
		for _, name := range scope.Names() {
			obj, ok := scope.Lookup(name).(*types.TypeName)
			if !ok || obj.IsAlias() {
				continue
			}
			named, ok := obj.Type().(*types.Named)
			if !ok {
				continue
			}
			if _, isIface := named.Underlying().(*types.Interface); isIface {
				continue // an interface that embeds the seam is a declaration, not an implementation
			}
			scanned++
			if types.Implements(named, sink) || types.Implements(types.NewPointer(named), sink) {
				implementors = append(implementors, path+"."+name)
			}
		}
	}
	if scanned < 100 {
		t.Fatalf("VACUOUS: only %d named types scanned across the module; the walk is broken and "+
			"this fence would pass over anything", scanned)
	}

	if len(implementors) != 0 {
		t.Fatalf(`the CONTROL half of Wave R8 has been wired: %v now implements protocol.TerminalInputSink.

Wave R8 shipped the READ half only, and this row is the record of it (ADR-017 amendment C0).
If you are landing the parked control slice, that is what this fence is for -- but read C1
FIRST, because it is a PRECONDITION and not a recommendation:

  RAW INPUT IS BEARER-AUTHORISED TODAY. `+"`SealTerminalInputEnvelope`"+` (internal/phonecore/
  command.go) builds a DeviceCommandAuth with NO DeviceID, so forwardControl sets
  DeviceID: "", and liveTerminalGeneration never compares the SENDER of a frame to
  gen.deviceID. The epoch ContentKey is per-machine and granted to every paired device, and
  the begin reply is sealed to one shared ReplyTarget -- so a paired READ-ONLY device can read
  a control-tier device's generation id and type under it. That is moot only while no sink
  exists. It stops being moot at the exact moment this test fails.

Before deleting or updating this row: bind the generation to the SENDING DEVICE's identity,
check that binding per frame, and fence it. Then update docs/verification/r8-green/
README-closing.md's CAN/CANNOT ledger, which currently says a user CANNOT enter control of
anything, and ADR-017 amendment C0's statement that the raw-input attack surface is zero.`,
			implementors)
	}
}
