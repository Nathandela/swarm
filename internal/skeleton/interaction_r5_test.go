package skeleton

// FAILING-FIRST (TDD RED, GG-5) for review finding R5, END TO END on the shipped release path.
//
// The daemon-side half of this finding is pinned in internal/daemon/interaction_r5_test.go.
// This is the half the finding is actually about: the refusal must fire where the PRODUCER
// releases, which is ItemAdmission.Append -> daemon.RecordInteractionRaw, not on the typed
// entry no production caller reaches.
//
// The item is offered to the floor directly rather than shaped through captureInteractions
// because shapeItem always stamps `ts` -- which is precisely why the refusal behind it was
// never exercised. Offering the malformed bytes at the floor is the seam a real producer bug
// would arrive at: the floor merges bytes field-wise (IS-DELTA-3) and asserts nothing about
// them beyond `item_id` and `kind`.

import (
	"encoding/json"
	"testing"
)

// TestInteractionR5_TheShippedReleasePathRefusesAnItemWithNoTS: nothing reaches the journal,
// and the refusal is not swallowed by the floor on its way back to the caller.
func TestInteractionR5_TheShippedReleasePathRefusesAnItemWithNoTS(t *testing.T) {
	sk := assemble(t)
	sk.initInteractions()

	item := json.RawMessage(`{"v":1,"item_id":"01JBQ4Z0X9M6T7NPKV2RQF8SJD","kind":"agent_message","text":"hi"}`)
	if err := sk.items.Offer("s-r5", item); err == nil {
		t.Error("the append floor released a `ts`-less item into the journal without complaint; §2 " +
			"makes `ts` required and the wire journal record carries none to substitute (PB-APP-11)")
	}
	if got := interactionItems(t, sk, "s-r5"); len(got) != 0 {
		t.Fatalf("the journal holds %d interaction record(s) for a `ts`-less item: %v; want none "+
			"(IS-ENV-3: emit nothing rather than a partial item)", len(got), got)
	}
}
