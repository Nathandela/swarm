package skeleton

// WAVE R8 / CLOSING ROUND -- DEGRADE-ON-READ MUST NOT LAUNDER AN INVALID RECORD
// (closing review, finding 9).
//
// THE FINDING. `lookupCapabilitiesLocked` applies `SetStructuredChat(false)` to whatever
// `rawCapabilitiesLocked` returns WITHOUT validating it, and that setter FORCES
// `TerminalFallback = true`. So an INVALID record on disk --
//
//	{structured_chat:true, terminal_fallback:false, terminal_control:true}
//
// which every T2-b seam refuses, becomes the VALID
//
//	{structured_chat:false, terminal_fallback:true, terminal_control:true}
//
// which grants `AllowsTerminalControl()`. The transform runs from LESS VALID to MORE
// AUTHORITY, which is the one direction a degrade may never take: T2 rule 2 permits a
// degrade to remove structured chat, and nothing in it permits a read to manufacture a
// grant the authoring daemon never made.
//
// REACHABILITY, stated honestly because it is part of the finding rather than a defence of
// it: producing that record requires WRITE ACCESS to the 0700 session directory, and no
// remote path produces one. It is nonetheless the one seam where T2-b's validation is absent
// -- every other decode seam has it -- and the fence exists because "unreachable today" is a
// property of the current call graph and not of the record.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
)

// TestR8R4Capability_ADegradeOnReadNeverLaundersAnInvalidRecordIntoAGrant is the fence.
func TestR8R4Capability_ADegradeOnReadNeverLaundersAnInvalidRecordIntoAGrant(t *testing.T) {
	dir := t.TempDir()
	d := &Daemon{}
	d.capStore.dir = dir

	// EVERY invalid boolean shape reachable by the degrade transform, not one of them
	// (closing round 2, the blocking finding). Validate has TWO boolean clauses; the
	// original fence drove only the shape the control-without-fallback clause rejects,
	// so a guard covering that one clause stayed green while the mutual-exclusion
	// shapes laundered through: {true,true,true} read back granting
	// AllowsTerminalControl() and {true,true,false} read back granting a watch.
	shapes := []struct {
		name             string
		terminalFallback bool
		terminalControl  bool
	}{
		{"control-without-fallback", false, true},
		{"mutual-exclusion-with-control", true, true},
		{"mutual-exclusion-watch-only", true, false},
	}
	for i, shape := range shapes {
		sess := fmt.Sprintf("s-launder-%d", i)
		sdir := filepath.Join(dir, sess)
		if err := os.MkdirAll(sdir, 0o700); err != nil {
			t.Fatalf("%s: session dir: %v", shape.name, err)
		}
		bad := protocol.SessionCapabilities{
			Provider: "claude", ProviderVersion: "1.2.3", AdapterRevision: "rev",
			SessionInstance:  "inst-1",
			StructuredChat:   true,
			TerminalFallback: shape.terminalFallback,
			TerminalControl:  shape.terminalControl,
		}
		if bad.Validate() == nil {
			t.Fatalf("%s: the fixture is supposed to be an INVALID record and Validate accepts "+
				"it; the fence would then measure nothing.\n%+v", shape.name, bad)
		}
		blob, err := json.Marshal(bad)
		if err != nil {
			t.Fatalf("%s: marshal: %v", shape.name, err)
		}
		if err := os.WriteFile(filepath.Join(sdir, sessionCapabilityFile), blob, 0o600); err != nil {
			t.Fatalf("%s: write record: %v", shape.name, err)
		}
		// The durable degraded marker: the proof of a structured gap, which is what makes
		// the read apply SetStructuredChat(false).
		if err := os.WriteFile(filepath.Join(sdir, sessionDegradedFile), []byte("proven"), 0o600); err != nil {
			t.Fatalf("%s: write marker: %v", shape.name, err)
		}

		got, ok := d.sessionCapabilities(sess)
		if ok && got.AllowsTerminalControl() {
			t.Fatalf("%s: ADR-017 T2-b: an INVALID record on disk was read back as a record granting "+
				"AllowsTerminalControl(). The degrade-on-read applied SetStructuredChat(false) without "+
				"validating what it was applied to, and that setter forces terminal_fallback=true -- so "+
				"the transform ran from LESS VALID to MORE AUTHORITY. A read may remove structured chat; "+
				"it may never manufacture a grant the authoring daemon did not make.\ngot: %+v", shape.name, got)
		}
		if ok && got.AllowsTerminalWatch() {
			t.Fatalf("%s: ADR-017 T2-b: the same laundering, one authority level down: an invalid "+
				"record was read back as one permitting a terminal watch.\ngot: %+v", shape.name, got)
		}
	}
}

// TestR8R4Capability_AValidDegradedRecordStillReadsBack is the vacuity guard. A fence that
// made every disk record unreadable would pass the test above and break the feature.
func TestR8R4Capability_AValidDegradedRecordStillReadsBack(t *testing.T) {
	dir := t.TempDir()
	d := &Daemon{}
	d.capStore.dir = dir

	const sess = "s-honest"
	sdir := filepath.Join(dir, sess)
	if err := os.MkdirAll(sdir, 0o700); err != nil {
		t.Fatalf("session dir: %v", err)
	}
	good := protocol.SessionCapabilities{
		Provider: "claude", ProviderVersion: "1.2.3", AdapterRevision: "rev",
		SessionInstance: "inst-1",
		StructuredChat:  true,
	}
	blob, _ := json.Marshal(good)
	if err := os.WriteFile(filepath.Join(sdir, sessionCapabilityFile), blob, 0o600); err != nil {
		t.Fatalf("write record: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sdir, sessionDegradedFile), []byte("proven"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	got, ok := d.sessionCapabilities(sess)
	if !ok {
		t.Fatalf("a VALID record with a proven degrade marker did not read back at all")
	}
	if got.StructuredChat {
		t.Errorf("ADR-017 T2 rule 2: the proven degrade marker did not remove structured chat")
	}
	if !got.AllowsTerminalWatch() {
		t.Errorf("a session degraded by a PROVEN structured gap must gain the sanitized fallback "+
			"surface it lost chat for.\ngot: %+v", got)
	}
	if got.AllowsTerminalControl() {
		t.Errorf("ADR-017 T6-b: a session degraded INTO the fallback may watch and may NOT control")
	}
}
