package daemon

// FAILING-FIRST (TDD RED, GG-5) for review finding R5 against 79a070d.
//
// THE FINDING. `InteractionItem.validate` carries five refusals, but only three of them --
// IS-ENV-3's `v`, `item_id` and `kind` -- were re-implemented inline in the entry the shipped
// producer actually calls (`RecordInteractionRaw`, reached from skeleton's
// ItemAdmission.Append). The other two -- interaction-schema.md §2's REQUIRED `ts`, and its
// rule that `full_bytes` is carried only alongside `truncated` -- lived on the typed
// `RecordInteraction`, which no production caller reaches. They could not fire on a shipped
// item, and the typed path could not fire the `ts` one either, because it stamps `ts` before
// validating it.
//
// WHAT THIS FILE PINS. Both refusals on the entry the producer releases into, so a producer
// bug that would put a `ts`-less item on the wire is refused instead. §2's `ts` is required
// for a reason a consumer cannot work around: the enclosing wire journal record carries NO
// timestamp, so an item without one leaves the phone substituting arrival time, which is
// exactly the PB-APP-11 clock mistake.

import (
	"encoding/json"
	"testing"
)

// rawItem is one serialized item, written as bytes rather than marshalled from an
// InteractionItem on purpose: the shipped path hands this entry bytes that may have been
// merged field-wise by the append floor (IS-DELTA-3), so the fixture must be able to express
// an item the typed struct would have auto-corrected.
func rawItem(body string) json.RawMessage { return json.RawMessage(body) }

// TestDaemon_RecordInteractionRawRefusesAnItemWithNoTS is §2's `ts` on the SHIPPED entry.
// Emitting the item without it is IS-ENV-3's partial item: a consumer's only recourse is to
// substitute arrival time, and the record has already burned a cursor.
func TestDaemon_RecordInteractionRawRefusesAnItemWithNoTS(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))

	err := d.RecordInteractionRaw("m/s1", rawItem(
		`{"v":1,"item_id":"`+testItemID+`","kind":"agent_message","text":"hi"}`))
	if err == nil {
		t.Error("RecordInteractionRaw accepted an item with no `ts`; §2 makes it required and the " +
			"enclosing wire record carries none to substitute (PB-APP-11)")
	}
	if n := len(interactionRecords(t, d)); n != 0 {
		t.Fatalf("%d interaction record(s) appended for a `ts`-less item; want none", n)
	}
}

// TestDaemon_RecordInteractionRawRefusesFullBytesWithoutTruncated is §2's other pairing rule:
// `full_bytes` reports the size of what was CLIPPED, so it is meaningless -- and actively
// misleading on a card that renders "N bytes elided" -- on an item that was not truncated.
//
// The positive half is the control: the same item WITH `truncated` is appended, so the
// refusal is the pair and not the field.
func TestDaemon_RecordInteractionRawRefusesFullBytesWithoutTruncated(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))
	const ts = `"ts":"2026-08-07T10:00:00Z"`

	err := d.RecordInteractionRaw("m/s1", rawItem(
		`{"v":1,"item_id":"`+testItemID+`",`+ts+`,"kind":"tool_run","full_bytes":4096}`))
	if err == nil {
		t.Error("RecordInteractionRaw accepted `full_bytes` on an item that is not `truncated`; §2 " +
			"carries the two together, and alone it reports a clip that never happened")
	}
	if n := len(interactionRecords(t, d)); n != 0 {
		t.Fatalf("%d interaction record(s) appended for an unpaired `full_bytes`; want none", n)
	}

	if err := d.RecordInteractionRaw("m/s1", rawItem(
		`{"v":1,"item_id":"`+testItemID+`",`+ts+`,"kind":"tool_run","truncated":true,"full_bytes":4096}`)); err != nil {
		t.Fatalf("RecordInteractionRaw refused a correctly paired truncated/full_bytes item: %v", err)
	}
	if n := len(interactionRecords(t, d)); n != 1 {
		t.Fatalf("%d interaction record(s) after the paired item; want exactly 1", n)
	}
}
