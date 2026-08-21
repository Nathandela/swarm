package remotegw

// WAVE R8 / ROUND 3 -- AMENDMENT T2-b's SECOND SEAM, WHICH DID NOT EXIST.
//
// ADR-017 amendment T2-b, authored by this wave, names THREE places a capability record is
// validated: where it is authored, WHERE IT IS DECODED OFF THE WIRE IN THE GATEWAY, and
// where it is decoded on the phone. Round 2's evidence stated "validated at all three seams".
// There were two: internal/skeleton/instance.go (author) and internal/phonecore/journal.go
// (phone decode). `grep -rn 'Validate()' internal/ mobile/` returned no gateway call, and the
// gateway did not touch the record at all.
//
// WHY THE MIDDLE SEAM IS NOT REDUNDANT. The daemon's author-side validation protects records
// THIS daemon writes. It says nothing about a capabilities.json left behind by an older or
// rolled-back build, a partially-written file, or a daemon an attacker has replaced -- and
// the gateway is the last machine-side thing between any of those and a phone that will route
// a session on what it receives. Every gate in this wave is written over BOTH booleans so a
// malformed record cannot open a route; this is where a malformed record is DROPPED rather
// than merely refused a route, so the phone never sees it at all.
//
// It strips the RECORD, not the session: an unroutable record leaves the session on T2-a's
// honest status card, which is the same destination the phone's own decode seam would have
// reached. Dropping the whole record is deliberate -- a partially-trusted record is exactly
// the inconsistent state T2-b makes unrepresentable.

import (
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

func r8ValidRecord() *protocol.SessionCapabilities {
	return &protocol.SessionCapabilities{
		Provider: "opencode", ProviderVersion: "0.9.0", AdapterRevision: "rev-test",
		SessionInstance: "inst-1", TerminalFallback: true, TerminalControl: true,
	}
}

// TestR8R3_TheGatewayStripsAnInconsistentCapabilityRecord: the record every gate in this wave
// is written to refuse must not even reach the phone.
func TestR8R3_TheGatewayStripsAnInconsistentCapabilityRecord(t *testing.T) {
	for _, tc := range []struct {
		name string
		rec  *protocol.SessionCapabilities
	}{
		{"structured and fallback both true", &protocol.SessionCapabilities{
			Provider: "claude", ProviderVersion: "1", AdapterRevision: "r",
			SessionInstance: "inst-1", StructuredChat: true, TerminalFallback: true,
		}},
		{"control without fallback", &protocol.SessionCapabilities{
			Provider: "opencode", ProviderVersion: "1", AdapterRevision: "r",
			SessionInstance: "inst-1", TerminalControl: true,
		}},
		{"no session instance", &protocol.SessionCapabilities{
			Provider: "opencode", ProviderVersion: "1", AdapterRevision: "r",
			TerminalFallback: true,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := namespaceRecord("mach", protocol.JournalRecord{
				SessionID: "sess1", Type: "launched", Capabilities: tc.rec,
			})
			if out.Capabilities != nil {
				t.Fatalf("the gateway forwarded an inconsistent capability record (%+v).\n"+
					"ADR-017 T2-b names the gateway decode as one of the three seams a record "+
					"is validated at. A record the phone's own router would refuse must not "+
					"cross the machine boundary at all: what leaves here is what a phone with "+
					"any router, present or future, will act on.", *out.Capabilities)
			}
		})
	}
}

// TestR8R3_TheGatewayForwardsAValidCapabilityRecordUntouched is the vacuity guard: a seam
// that dropped everything would pass the test above and ship a phone with no routing at all.
func TestR8R3_TheGatewayForwardsAValidCapabilityRecordUntouched(t *testing.T) {
	rec := r8ValidRecord()
	out := namespaceRecord("mach", protocol.JournalRecord{
		SessionID: "sess1", Type: "launched", Capabilities: rec,
	})
	if out.Capabilities == nil {
		t.Fatalf("the gateway dropped a VALID capability record; every session would route to " +
			"the status card and the fallback would be unreachable")
	}
	if *out.Capabilities != *rec {
		t.Fatalf("the gateway altered a valid record: got %+v, want %+v", *out.Capabilities, *rec)
	}
	if out.SessionID != protocol.NamespacedID("mach", "sess1") {
		t.Fatalf("namespacing regressed: session id = %q", out.SessionID)
	}
}

// TestR8R3_TheGatewayDoesNotMutateTheCallersRecord: namespaceRoster returns a new slice so
// the caller's snapshot is not mutated, and the record must obey the same rule -- the daemon's
// own registry hands out a pointer, and stripping through it would blank the machine's copy.
func TestR8R3_TheGatewayDoesNotMutateTheCallersRecord(t *testing.T) {
	bad := &protocol.SessionCapabilities{
		Provider: "claude", ProviderVersion: "1", AdapterRevision: "r",
		SessionInstance: "inst-1", StructuredChat: true, TerminalFallback: true,
	}
	roster := []protocol.JournalRecord{{SessionID: "sess1", Type: "launched", Capabilities: bad}}
	out := namespaceRoster("mach", roster)
	if out[0].Capabilities != nil {
		t.Fatalf("namespaceRoster forwarded an inconsistent record")
	}
	if roster[0].Capabilities == nil {
		t.Fatalf("namespaceRoster blanked the CALLER's record: the gateway shares the daemon's " +
			"own pointer, so a strip through it would erase the machine's registry copy")
	}
}

// TestR8R3_TheJournalEgressPathIsTheOneThatValidates is the rule-4 fence: a source check that
// the two egress call sites in RunJournal are the ones carrying the validation, so a future
// refactor that forwards a record around namespaceRecord fails here rather than silently.
func TestR8R3_TheJournalEgressPathIsTheOneThatValidates(t *testing.T) {
	src := readSource(t, "gateway.go")
	body := goFuncBody(t, src, "func (g *Gateway) RunJournal(")
	if !strings.Contains(body, "namespaceRoster(dc.endpointID, res.Roster)") {
		t.Fatalf("RunJournal no longer publishes its roster through namespaceRoster; the " +
			"gateway's record-validation seam has been routed around")
	}
	if strings.Count(body, "namespaceRecord(dc.endpointID, rec)") < 2 {
		t.Fatalf("RunJournal no longer delivers both its snapshot journal and its live events " +
			"through namespaceRecord")
	}
	if !strings.Contains(goFuncBody(t, src, "func namespaceRecord("), "Validate()") {
		t.Fatalf("namespaceRecord does not validate the capability record it forwards.\n" +
			"This is ADR-017 T2-b's gateway seam, and the wave's own evidence claimed it " +
			"existed for a round while it did not.")
	}
}

// TestR8R3_TheStripIsTheSameRuleTheRouterApplies pins the two ends together: whatever the
// gateway keeps, the phone's router must be able to act on. A gateway that kept a record the
// router refuses would be shipping a routing decision the phone then has to unmake.
func TestR8R3_TheStripIsTheSameRuleTheRouterApplies(t *testing.T) {
	kept := namespaceRecord("mach", protocol.JournalRecord{
		SessionID: "s", Capabilities: r8ValidRecord(),
	}).Capabilities
	if kept == nil {
		t.Fatalf("the gateway dropped the record this test is about")
	}
	if err := kept.Validate(); err != nil {
		t.Fatalf("the gateway kept a record that does not validate: %v", err)
	}
	if !kept.AllowsTerminalWatch() {
		t.Fatalf("the kept record does not answer the predicate every gate is written over")
	}
	_ = schema.CurrentCapabilityRecordVersion
}
