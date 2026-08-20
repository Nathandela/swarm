package skeleton

// FAILING-FIRST (TDD RED, GG-5) for Wave R6's Mirror M2.2 tool-card vocabulary: the §7
// action type surfaces as an ADDITIVE top-level `tool_kind` field on every journalled
// tool_run item, so the phone picks a glyph from one flat field and parses nothing
// (IS-TOOL-1: "a phone SHALL NOT parse `tool` or raw arguments to infer an action") --
// plus the M2.2-booked schema row that keeps the field from being silent drift (GG-7).
// Bead: agents-tracker-hggx.7.
//
// This file introduces NO new Go symbols: it fails at RUNTIME (the journalled item
// carries no `tool_kind` key; the spec table carries no row) once the package's sibling
// R6 RED files stop holding compilation -- the staged-RED convention fixpack-red.txt
// records for this package.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
)

// r6ToolRun captures one tool_run with the given action and returns its journalled item.
func r6ToolRun(t *testing.T, act adapter.ToolAction, tool, session string) map[string]any {
	t.Helper()
	sk := assemble(t)
	sk.captureInteractions(session, newCaptureAdapter(adapter.Interaction{
		Kind: adapter.KindToolRun, Status: adapter.StatusCompleted,
		Tool: tool, Action: act, OutputExcerpt: "ok",
	}), adapter.HookPayload{Event: "PostToolUse"})
	items := awaitItems(t, sk, session, 1)
	for _, it := range items {
		if it["kind"] == adapter.KindToolRun {
			return it
		}
	}
	t.Fatalf("no tool_run reached the journal for %s: %v", session, items)
	return nil
}

// TestR6ToolKind_AToolRunItemCarriesTheFlatToolKindField: `tool_kind` mirrors the §7
// action type at the item's top level, beside the envelope, for every classified kind.
func TestR6ToolKind_AToolRunItemCarriesTheFlatToolKindField(t *testing.T) {
	cases := []struct {
		name string
		act  adapter.ToolAction
		tool string
	}{
		{"search", adapter.ToolAction{Type: "search", Query: "TODO"}, "Grep"},
		{"execute", adapter.ToolAction{Type: "execute", Command: "go test ./..."}, "Bash"},
		{"fetch", adapter.ToolAction{Type: "fetch"}, "WebFetch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			it := r6ToolRun(t, tc.act, tc.tool, "s-toolkind-"+tc.name)
			got, _ := it["tool_kind"].(string)
			if got != tc.act.Type {
				t.Errorf("journalled tool_run tool_kind = %q, want %q: the glyph vocabulary "+
					"rides one flat field so the phone renders from the record and parses "+
					"nothing (M2.2, IS-TOOL-1)", got, tc.act.Type)
			}
		})
	}
}

// TestR6ToolKind_AnUnclassifiedCallCarriesOtherNeverAGuess is IS-TOOL-2 restated on the
// new field: `other`, verbatim, so the card falls back to the tool name.
func TestR6ToolKind_AnUnclassifiedCallCarriesOtherNeverAGuess(t *testing.T) {
	it := r6ToolRun(t, adapter.ToolAction{Type: "other"}, "SomeNewTool", "s-toolkind-other")
	if got, _ := it["tool_kind"].(string); got != "other" {
		t.Errorf("unclassified tool_run tool_kind = %q, want \"other\"", got)
	}
}

// TestR6ToolKind_TheSchemaBooksTheFieldRow is M2.2's "+ GG-7 row": interaction-schema.md
// §3.3 documents `tool_kind` in the same slice that emits it, so the field is never
// silent drift (implementation-goals.md GG-7; the interaction-item table is procedural,
// not fenced -- §1 says so -- which is exactly why this test pins it).
func TestR6ToolKind_TheSchemaBooksTheFieldRow(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "specifications", "interaction-schema.md"))
	if err != nil {
		t.Fatalf("read interaction-schema.md: %v", err)
	}
	if !strings.Contains(string(raw), "`tool_kind`") {
		t.Error("interaction-schema.md documents no `tool_kind` field; the additive M2.2 field " +
			"must land with its schema row in the same slice (GG-7)")
	}
}
