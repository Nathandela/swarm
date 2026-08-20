package claude

// FAILING-FIRST (TDD RED, GG-5) for Wave R6's Mirror M2.2 adapter half: the `Grep`,
// `Glob` and `WebFetch` arms of the §7 action classifier, grounded in RECORDED fixtures
// -- the exact condition PROVENANCE.md sets for adding an arm ("Each is one recorded
// payload away from an arm here", actionFor's own comment). Bead: agents-tracker-hggx.7.
//
// This file introduces no new Go symbols. It fails at RUNTIME, for the RIGHT reason: the
// three fixtures do not exist yet. Recording them (real `claude`, real PTY, real hook
// sink -- the S-B rig, version-stamped) is part of the M2.2 deliverable, and the corpus
// provenance fence (provenance_test.go) will hold each new file to a PROVENANCE.md row
// exactly as it holds the S-B three. Until the payloads are recorded, IS-TOOL-2 keeps
// the classifier honest: `other`, never a guessed argument key -- which is why these
// tests DEMAND the fixture rather than feeding a hand-authored body.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
)

// r6SearchFetchCase names one to-be-recorded fixture and the classification its
// PreToolUse tool_run must carry once the arm exists.
type r6SearchFetchCase struct {
	fixture  string
	tool     string
	wantType string
}

func r6SearchFetchCases() []r6SearchFetchCase {
	return []r6SearchFetchCase{
		// Grep: §7 `search`, with `query` carrying the pattern.
		{fixture: "claude-grep-pretooluse.json", tool: "Grep", wantType: "search"},
		// Glob: also `search` -- a filename pattern is a search, and inventing a
		// separate type would grow the sealed §7 vocabulary for no rendering gain.
		{fixture: "claude-glob-pretooluse.json", tool: "Glob", wantType: "search"},
		// WebFetch: §7 `fetch`.
		{fixture: "claude-webfetch-pretooluse.json", tool: "WebFetch", wantType: "fetch"},
	}
}

// TestR6SearchFetch_RecordedFixturesClassifyTheirOwnTools drives the shipped producer
// over each recorded corpus and asserts the tool_run it shapes: the right §7 type, and a
// non-empty target argument surfaced on the action (the pattern for search; the URL for
// fetch) -- read from the RECORDED body, so the argument key is a fact, not a guess.
func TestR6SearchFetch_RecordedFixturesClassifyTheirOwnTools(t *testing.T) {
	for _, tc := range r6SearchFetchCases() {
		t.Run(tc.fixture, func(t *testing.T) {
			src, ok := adapter.AsInteractionSource(New())
			if !ok {
				t.Fatal("the claude adapter implements no InteractionSource")
			}
			var runs []adapter.Interaction
			for _, hp := range loadCorpus(t, tc.fixture).HookPayloads {
				for _, in := range src.Interactions(hp) {
					if in.Kind == adapter.KindToolRun && in.Tool == tc.tool {
						runs = append(runs, in)
					}
				}
			}
			if len(runs) == 0 {
				t.Fatalf("the recorded corpus %s yielded no %s tool_run", tc.fixture, tc.tool)
			}
			for _, in := range runs {
				if in.Action.Type != tc.wantType {
					t.Errorf("%s classified %q, want %q: the arm exists exactly because the "+
						"payload is now recorded (IS-TOOL-2 forbade it before)", tc.tool, in.Action.Type, tc.wantType)
				}
				if in.Action.Path == "" && in.Action.Query == "" && in.Action.Command == "" {
					t.Errorf("%s action carries no target argument at all; a card reading just "+
						"%q tells the owner nothing (§7's whole point)", tc.tool, tc.wantType)
				}
			}
		})
	}
}

// TestR6SearchFetch_TheProvenanceLedgerCarriesEachNewFixture: every new corpus file
// takes its PROVENANCE.md row (the nq0q fence's own input), so the recording's origin --
// binary version, rig, copied-vs-reconstructed -- is declared rather than inferred. A
// fixture in the directory with no row is exactly the drift the ledger exists to stop.
func TestR6SearchFetch_TheProvenanceLedgerCarriesEachNewFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "interaction", "PROVENANCE.md"))
	if err != nil {
		t.Fatalf("read PROVENANCE.md: %v", err)
	}
	for _, tc := range r6SearchFetchCases() {
		if !strings.Contains(string(raw), tc.fixture) {
			t.Errorf("PROVENANCE.md has no row for %s; a recorded fixture lands with its "+
				"provenance in the same slice", tc.fixture)
		}
	}
}
