package main

// WAVE R8 / ROUND 2 -- THE SHIPPED PROFILE, ASSERTED THROUGH THE REAL ASSEMBLER.
//
// ROUND-2 BLOCKER 3, stated as what a user experienced. `resolveGatewayParams` is the ONLY
// production construction of `RemoteProfileV1` in the tree, and it set the three ADR-016
// relay fields and left `Version`, `TerminalViewVersion`, `CapabilityRecordVersion` and all
// three bounds at ZERO. `phonecore.RouteSession` fails closed on `TrustsCapabilityRecord()`
// FIRST, so in the shipped binary:
//
//   - EVERY session routed to the honest status card. R8's exit criterion -- "OpenCode and
//     AGY can be launched and safely monitored from the fallback" -- was FALSE end to end.
//   - `ComposerAvailable` is the same predicate, so R6's and R7's chat composer DISAPPEARED
//     FOR EVERY SESSION. A wiring defect in R8 took away two preceding waves' feature.
//
// The plan named this exactly ("R8 populates the production profile -- a wiring defect") and
// round 1 did not do it while its evidence claimed the opposite. So the fence is driven
// through `resolveGatewayParams` over a provisioned state dir, and then through
// `phonecore.RouteSession` with THAT profile value, because the version numbers are not the
// claim -- the DESTINATIONS are.

import (
	"testing"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/relaycfg"
)

// r8ProvisionedProfile runs the real assembler over a provisioned state dir and returns the
// profile the shipped sidecar would publish.
func r8ProvisionedProfile(t *testing.T) schema.RemoteProfileV1 {
	t.Helper()
	stateDir := t.TempDir()
	writeMachineIdentity(t, stateDir)
	if err := relaycfg.Save(stateDir, relaycfg.Config{
		RelayURL:          "wss://swarm-relay.example.com:8443",
		OperatorNamespace: "owner",
		TLSPolicy:         relaycfg.PolicyWebPKI,
	}); err != nil {
		t.Fatalf("relaycfg.Save: %v", err)
	}
	addPairedDevice(t, stateDir)
	got, err := resolveGatewayParams(stateDir, "/tmp/does-not-need-to-exist/remote.sock")
	if err != nil {
		t.Fatalf("resolveGatewayParams: %v", err)
	}
	return got.Profile
}

// TestR8Profile_TheShippedProfileDeclaresTheVersionsTheFallbackDependsOn is the direct half.
func TestR8Profile_TheShippedProfileDeclaresTheVersionsTheFallbackDependsOn(t *testing.T) {
	p := r8ProvisionedProfile(t)
	for _, f := range []struct {
		name string
		got  int
		want int
	}{
		{"version", p.Version, schema.CurrentProfileVersion},
		{"terminal_view_version", p.TerminalViewVersion, schema.CurrentTerminalViewVersion},
		{"capability_record_version", p.CapabilityRecordVersion, schema.CurrentCapabilityRecordVersion},
	} {
		if f.got != f.want {
			t.Errorf("the SHIPPED profile declares %s = %d, want %d. ADR-017 T5-a: the phone reads "+
				"zero here as \"no fallback exists\" / \"record untrusted\" and routes EVERY session "+
				"to the status card -- which is R8's exit criterion being false in the binary while "+
				"every unit test is green.", f.name, f.got, f.want)
		}
	}
	if p.InteractionSchemaVersion <= 0 {
		t.Errorf("the shipped profile declares interaction_schema_version = %d; the machine has been "+
			"stamping daemon.InteractionSchemaVersion on every item since ADR-009", p.InteractionSchemaVersion)
	}
	for _, b := range []struct {
		name string
		got  int
	}{
		{"terminal_view_max_line_bytes", p.TerminalViewMaxLineBytes},
		{"terminal_view_max_rows", p.TerminalViewMaxRows},
		{"terminal_view_max_rate_hz", p.TerminalViewMaxRateHz},
	} {
		if b.got <= 0 {
			t.Errorf("the shipped profile declares no %s. Zero clamps to the phone's built-in and is "+
				"therefore SAFE, but it is also indistinguishable from \"this machine has no opinion\", "+
				"and a declared ceiling is checkable against what the machine actually sends.", b.name)
		}
	}
}

// TestR8Profile_TheShippedProfileRoutesBothDestinationsForReal is the half that matters, and
// it is written over DESTINATIONS rather than over version numbers: the numbers are a means,
// and asserting them alone is what let round 1 ship a profile that routed everything to the
// status card while `CurrentTerminalViewVersion` was 1 the whole time.
func TestR8Profile_TheShippedProfileRoutesBothDestinationsForReal(t *testing.T) {
	p := r8ProvisionedProfile(t)

	healthyClaude := &schema.SessionCapabilities{
		Provider: "claude", ProviderVersion: "1.0.0", SessionInstance: "inst-1",
		StructuredChat: true,
	}
	if got := phonecore.RouteSession(healthyClaude, p); got != phonecore.DestinationChat {
		t.Errorf("under the SHIPPED profile a healthy Claude session routes to %s, want chat. R6 and "+
			"R7 made this session a chat client; a profile that routes it to the status card takes "+
			"the composer away from every session in the app.", got)
	}
	if !phonecore.ComposerAvailable(healthyClaude, p) {
		t.Errorf("under the SHIPPED profile a healthy Claude session has NO COMPOSER. " +
			"ComposerAvailable is RouteSession's own predicate, so a profile defect here is a " +
			"silent regression of the two preceding waves rather than a missing R8 feature.")
	}

	opencode := &schema.SessionCapabilities{
		Provider: "opencode", ProviderVersion: "0.9.0", SessionInstance: "inst-2",
		TerminalFallback: true,
	}
	if got := phonecore.RouteSession(opencode, p); got != phonecore.DestinationTerminalFallback {
		t.Errorf("under the SHIPPED profile an OpenCode session routes to %s, want terminal_fallback. "+
			"This IS wave R8's exit criterion: \"OpenCode and AGY can be launched and safely "+
			"monitored from the fallback\".", got)
	}

	// AND THE FAIL-CLOSED DIRECTION IS UNTOUCHED BY THE POPULATION. Publishing versions must
	// not make an absent or inconsistent record renderable.
	if got := phonecore.RouteSession(nil, p); got != phonecore.DestinationStatusCard {
		t.Errorf("under the SHIPPED profile an ABSENT record routes to %s, want status_card (T2-a)", got)
	}
	inconsistent := &schema.SessionCapabilities{
		Provider: "claude", SessionInstance: "inst-3", StructuredChat: true, TerminalFallback: true,
	}
	if got := phonecore.RouteSession(inconsistent, p); got != phonecore.DestinationStatusCard {
		t.Errorf("under the SHIPPED profile an INCONSISTENT record routes to %s, want status_card (T2-b)", got)
	}
}
