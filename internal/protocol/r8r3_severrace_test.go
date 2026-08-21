package protocol

// WAVE R8 / ROUND 3 -- MINOR 7: the terminal sever has no race fence, and its own sibling
// documents why one is needed.
//
// Server.severControl bumps s.severGen BEFORE snapshotting s.conns, and its comment states
// the reason: "a take_control that publishes its lease/cc.control after this snapshot
// re-checks the generation under ctlMu and, seeing it advanced, fails closed rather than
// escaping the sever." Server.severTerminalControl snapshotted with no bump and no re-check,
// so a terminal_control_begin landing between the snapshot and the sweep escaped it outright
// -- and a surviving generation is live again on the kill switch's OFF->ON, which is verbatim
// the resume defect round 2's blocker 2 was written to close.

import (
	"testing"
	"time"
)

// TestR8R3_ABeginRacingASeverFailsClosed is MINOR 7: Server.severControl bumps a generation
// counter BEFORE snapshotting so a lease published after the snapshot re-checks and fails
// closed, and its own comment says why. severTerminalControl had neither the bump nor the
// re-check, so a terminal_control_begin landing between the snapshot and the sweep escaped
// the sever outright -- and a survivor is live again the moment the kill switch goes back on,
// which is verbatim the resume defect round 2's blocker 2 exists to close.
// It is driven in the ORDER THE RACE HAPPENS -- sample, sever, publish -- because the earlier
// version of this test sampled `Load()-1` and passed with the counter removed entirely
// (`0-1` still differs from `0`). A fence that survives its own mutation is this wave's own
// named defect class, and it is recorded here rather than quietly repaired.
func TestR8R3_ABeginRacingASeverFailsClosed(t *testing.T) {
	rig := newControlRig(t)

	// 1. A begin arrives and samples the sever generation, exactly as handleTerminalControlBegin
	//    does before it does any of its authority work.
	severAtStart := rig.srv.termSeverGen.Load()

	// 2. The kill switch goes off WHILE that begin is still working. It sweeps a registry the
	//    raced generation has not reached yet, so the sweep cannot see it.
	rig.srv.severTerminalControl(func(string) bool { return true })

	// 3. The begin now tries to publish. It must fail closed.
	published := rig.srv.publishTerminalGenerationIfCurrent(severAtStart, &terminalGeneration{
		id: "race", session: "sess1", instance: "inst-1", deviceID: "devA",
		horizon: rig.base.Add(time.Hour), keepalive: rig.base.Add(time.Hour),
	})
	if published {
		t.Fatalf("a generation whose authority was decided BEFORE a sever was published AFTER " +
			"it. The sever's own sweep cannot see it, so the kill switch leaves a live keyboard " +
			"behind -- and a survivor is live again the moment the switch goes back on, which is " +
			"verbatim the resume defect round-2's blocker 2 was written to close.")
	}
	if rig.srv.anyLiveTerminalGeneration() {
		t.Fatalf("the raced generation reached the registry anyway")
	}

	// 4. AND THE COUNTER DOES NOT WEDGE THE PLANE. A fence that made every subsequent begin
	//    fail would be fail-closed in the way that means "the feature is off".
	fresh := rig.srv.termSeverGen.Load()
	if !rig.srv.publishTerminalGenerationIfCurrent(fresh, &terminalGeneration{
		id: "after", session: "sess1", instance: "inst-1", deviceID: "devA",
		horizon: rig.base.Add(time.Hour), keepalive: rig.base.Add(time.Hour),
	}) {
		t.Fatalf("a begin that started AFTER the sever could not publish either; the race fence " +
			"has become a permanent refusal")
	}
}
