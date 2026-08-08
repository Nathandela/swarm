package claude

// The Claude Code PRODUCER's failing-first suite (TDD, GG-5): ADR-010 §5's four capture rows,
// shaped against the REAL recorded S-B corpus in testdata/interaction (provenance: PROVENANCE.md
// there — verbatim copies of docs/verification/fixtures/spike-sb/*.json, real `claude` 2.1.214
// driven through the real `swarm-char` binary on 2026-07-18).
//
// NOTHING HERE RUNS A CLI. Every assertion is against those recorded bytes, which is exactly what
// ADR-010's "reusing HookPayload makes the fixture corpus the conformance corpus" buys.
//
// The RED reason before the producer lands is that claudeAdapter implements no
// adapter.InteractionSource and declares no capture=raw row, so AsInteractionSource reports false
// and the golden corpus shapes nothing.

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/fixtureio"
)

// shapedRows are ADR-010 §5's four Claude Code rows: the events whose bodies are PRESERVED
// rather than flattened, because this producer shapes an item out of each.
var shapedRows = []string{"UserPromptSubmit", "PreToolUse", "PostToolUse", "PermissionRequest"}

// loadCorpus reads one recorded fixture out of the golden corpus.
func loadCorpus(t *testing.T, name string) adapter.Fixture {
	t.Helper()
	fx, err := fixtureio.LoadFixture(filepath.Join("testdata", "interaction", name))
	if err != nil {
		t.Fatalf("LoadFixture(%s): %v", name, err)
	}
	return fx
}

// TestInteractionSource_ClaudeImplementsTheOptionalExtension — ADR-010 §5: Claude Code is a
// CLI-NATIVE adapter, so it is discovered by the production type assertion, not by a new method
// on the frozen Adapter interface.
func TestInteractionSource_ClaudeImplementsTheOptionalExtension(t *testing.T) {
	if _, ok := adapter.AsInteractionSource(New()); !ok {
		t.Fatal("AsInteractionSource(claude) reports false; ADR-010 §5 makes Claude Code a native capture source, and without it the daemon falls back to deriving items from the sanitized grid (spike S-A: PARTIAL, and it never recovers tool_input at all)")
	}
}

// TestSignalSources_DeclareCaptureRawOnExactlyTheShapedRows — ADR-010 §1 + conformance
// obligation 3: an event whose body this producer shapes MUST declare capture=raw, or ingest
// flattens the body to top-level strings before the shaper ever sees it (§6). The converse
// matters just as much: a row that declares capture but shapes nothing preserves a body for a
// shaper that does not exist.
func TestSignalSources_DeclareCaptureRawOnExactlyTheShapedRows(t *testing.T) {
	want := map[string]bool{}
	for _, e := range shapedRows {
		want[e] = true
	}
	got := map[string]bool{}
	for _, s := range New().SignalSources() {
		if s.Descriptor[adapter.DescriptorCapture] == adapter.CaptureRaw {
			got[s.Descriptor["event"]] = true
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("capture=raw rows = %v, want %v (ADR-010 §5 names UserPromptSubmit, PreToolUse, PostToolUse and PermissionRequest and no others)", got, want)
	}
}

// goldenCorpus is obligation 4's GOLDEN half: for each recorded fixture, the exact items its
// payloads shape, in payload order. CheckInteractionFixture proves the items are WELL-FORMED;
// only this table proves they are RIGHT.
var goldenCorpus = []struct {
	fixture string
	want    []adapter.Interaction
}{
	{
		// A Bash call that never escalated to a permission dialog. Two prompts, one tool run.
		fixture: "claude-bash-pretooluse-no-escalation.json",
		want: []adapter.Interaction{
			{Kind: adapter.KindUserMessage, Source: adapter.SourceOwner,
				Text: "Using the Bash tool, run this exact shell command: echo hello-interactive-spike-approve"},
			{Kind: adapter.KindToolRun, Status: adapter.StatusInProgress,
				Ref: "toolu_01Q3Vd8s9HhtsCpKJjRHB2Qj", Tool: "Bash",
				Action: adapter.ToolAction{Type: "execute", Command: "echo hello-interactive-spike-approve"}},
			{Kind: adapter.KindToolRun, Status: adapter.StatusCompleted,
				Ref: "toolu_01Q3Vd8s9HhtsCpKJjRHB2Qj", Tool: "Bash",
				Action:        adapter.ToolAction{Type: "execute", Command: "echo hello-interactive-spike-approve"},
				OutputExcerpt: "hello-interactive-spike-approve"},
			{Kind: adapter.KindUserMessage, Source: adapter.SourceOwner, Text: "1"},
		},
	},
	{
		// The one genuine Bash PermissionRequest S-B captured — and `touch approval-test.txt`
		// names a file path, so it is spike S-C's carve-out: prompt_card, with the keystroke map.
		fixture: "claude-bash-permissionrequest-run1.json",
		want: []adapter.Interaction{
			{Kind: adapter.KindUserMessage, Source: adapter.SourceOwner,
				Text: "Using the Bash tool, run this exact shell command: touch approval-test.txt"},
			{Kind: adapter.KindToolRun, Status: adapter.StatusInProgress,
				Ref: "toolu_01WwtgUTi7urC7fP8YDPZAQz", Tool: "Bash",
				Action: adapter.ToolAction{Type: "execute", Command: "touch approval-test.txt"}},
			{Kind: adapter.KindApprovalRequest, Status: adapter.StatusInProgress,
				Ref:     "permission-request:prompt-card:Bash:1784379016529",
				Summary: "Bash touch approval-test.txt",
				Action:  adapter.ToolAction{Type: "execute", Command: "touch approval-test.txt"},
				Decisions: []adapter.DecisionChoice{
					{ID: "allow", Label: "Yes", Verdict: adapter.VerdictAllow},
					{ID: "deny", Label: "No", Verdict: adapter.VerdictDeny},
				},
				Mode:       adapter.ModePromptCard,
				Keystrokes: map[string]string{"allow": "1", "deny": "\x1b"}},
			{Kind: adapter.KindToolRun, Status: adapter.StatusCompleted,
				Ref: "toolu_01WwtgUTi7urC7fP8YDPZAQz", Tool: "Bash",
				Action: adapter.ToolAction{Type: "execute", Command: "touch approval-test.txt"}},
		},
	},
	{
		// A Read run, then an Edit that escalated. Edit is not Bash, so the hook resolves it
		// natively (S-C measured that hook holding to 300 s): mode card, no keystroke map. Its
		// PostToolUse carries the structuredPatch that becomes the file_change.
		fixture: "claude-edit-permissionrequest-run1.json",
		want: []adapter.Interaction{
			{Kind: adapter.KindUserMessage, Source: adapter.SourceOwner,
				Text: "Using the Edit tool, change the text 'line two' to 'line TWO EDITED' in edit-target3.txt"},
			{Kind: adapter.KindToolRun, Status: adapter.StatusInProgress,
				Ref: "toolu_01RKGYmCbHp7NkRPNoaZ6zrb", Tool: "Read",
				Action: adapter.ToolAction{Type: "read", Path: "/Users/Nathan/spike-sb-work/edit-target3.txt"}},
			{Kind: adapter.KindToolRun, Status: adapter.StatusCompleted,
				Ref: "toolu_01RKGYmCbHp7NkRPNoaZ6zrb", Tool: "Read",
				Action:        adapter.ToolAction{Type: "read", Path: "/Users/Nathan/spike-sb-work/edit-target3.txt"},
				OutputExcerpt: "line one\nline two\nline three\n"},
			{Kind: adapter.KindToolRun, Status: adapter.StatusInProgress,
				Ref: "toolu_01M3bB2YSRc8Pb1oVXTAZDSb", Tool: "Edit",
				Action: adapter.ToolAction{Type: "edit", Path: "/Users/Nathan/spike-sb-work/edit-target3.txt"}},
			{Kind: adapter.KindApprovalRequest, Status: adapter.StatusInProgress,
				Ref:     "permission-request:card:Edit:1784379271002",
				Summary: "Edit /Users/Nathan/spike-sb-work/edit-target3.txt",
				Action:  adapter.ToolAction{Type: "edit", Path: "/Users/Nathan/spike-sb-work/edit-target3.txt"},
				Decisions: []adapter.DecisionChoice{
					{ID: "allow", Label: "Yes", Verdict: adapter.VerdictAllow},
					{ID: "deny", Label: "No", Verdict: adapter.VerdictDeny},
				},
				Mode: adapter.ModeCard},
			{Kind: adapter.KindToolRun, Status: adapter.StatusCompleted,
				Ref: "toolu_01M3bB2YSRc8Pb1oVXTAZDSb", Tool: "Edit",
				Action: adapter.ToolAction{Type: "edit", Path: "/Users/Nathan/spike-sb-work/edit-target3.txt"}},
			{Kind: adapter.KindFileChange, Path: "/Users/Nathan/spike-sb-work/edit-target3.txt",
				Change:      "modify",
				DiffExcerpt: "@@ -1,3 +1,3 @@\n line one\n-line two\n+line TWO EDITED\n line three",
				Added:       1, Removed: 1},
		},
	},
}

// TestGoldenCorpus_TheRecordedPayloadsShapeExactlyTheseItems — obligation 4's golden half,
// against real recorded bytes.
func TestGoldenCorpus_TheRecordedPayloadsShapeExactlyTheseItems(t *testing.T) {
	src, ok := adapter.AsInteractionSource(New())
	if !ok {
		t.Fatal("claude is not an InteractionSource")
	}
	for _, tc := range goldenCorpus {
		t.Run(tc.fixture, func(t *testing.T) {
			fx := loadCorpus(t, tc.fixture)
			var got []adapter.Interaction
			for _, hp := range fx.HookPayloads {
				got = append(got, src.Interactions(hp)...)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("shaped %d item(s), want %d:\n got %+v\nwant %+v", len(got), len(tc.want), got, tc.want)
			}
			for i := range got {
				if !reflect.DeepEqual(got[i], tc.want[i]) {
					t.Errorf("item %d mismatch:\n got %+v\nwant %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestGoldenCorpus_PassesCheckInteractionFixture — obligation 3's corpus half and obligation 4's
// structural half, run over the SAME recorded corpus. This is the call whose absence kept
// adapter.CheckInteractionFixture on B94's unreachable-symbol ledger.
func TestGoldenCorpus_PassesCheckInteractionFixture(t *testing.T) {
	a := New()
	// NOT vacuous: CheckInteractionFixture returns nil for an adapter that is not an
	// InteractionSource, so without this guard the whole check passes by having nothing to do.
	if _, ok := adapter.AsInteractionSource(a); !ok {
		t.Fatal("claude is not an InteractionSource, so CheckInteractionFixture has nothing to replay and would pass vacuously")
	}
	for _, tc := range goldenCorpus {
		t.Run(tc.fixture, func(t *testing.T) {
			if errs := adapter.CheckInteractionFixture(a, loadCorpus(t, tc.fixture)); len(errs) != 0 {
				for _, err := range errs {
					t.Error(err)
				}
			}
		})
	}
}

// TestDecision_ReturnsThePermissionRequestHookReplyBody — ADR-010 §5. The envelope is spike
// S-C's, verbatim: a decision object at the TOP level was silently ignored by the CLI and the
// prompt fell through to a keystroke; wrapped in hookSpecificOutput the transcript reads
// "Allowed by PermissionRequest hook" and the command runs with zero keystrokes.
func TestDecision_ReturnsThePermissionRequestHookReplyBody(t *testing.T) {
	src, ok := adapter.AsInteractionSource(New())
	if !ok {
		t.Fatal("claude is not an InteractionSource")
	}
	const nativeRef = "permission-request:card:Edit:1784379271002"

	for _, tc := range []struct {
		verdict, behavior string
	}{{"allow", "allow"}, {"deny", "deny"}} {
		act, native := src.Decision(nativeRef, tc.verdict)
		if !native {
			t.Fatalf("Decision(%q, %q) reports no native mechanism; S-C measured this hook holding a decision to 300 s with no timeout, which is what makes one-tap the primary path", nativeRef, tc.verdict)
		}
		var body struct {
			HookSpecificOutput struct {
				HookEventName string `json:"hookEventName"`
				Decision      struct {
					Behavior string `json:"behavior"`
				} `json:"decision"`
			} `json:"hookSpecificOutput"`
		}
		if err := json.Unmarshal(act.Reply, &body); err != nil {
			t.Fatalf("Decision(%q) reply is not JSON: %v (%s)", tc.verdict, err, act.Reply)
		}
		if body.HookSpecificOutput.HookEventName != "PermissionRequest" {
			t.Errorf("reply hookEventName = %q, want PermissionRequest; S-C proved a bare top-level decision is silently ignored", body.HookSpecificOutput.HookEventName)
		}
		if got := body.HookSpecificOutput.Decision.Behavior; got != tc.behavior {
			t.Errorf("Decision(%q) behavior = %q, want %q", tc.verdict, got, tc.behavior)
		}
	}

	// A verdict outside the CLI's own behavior vocabulary has no reply body to write, and
	// inventing one would put bytes on a live hook that no capture ever showed.
	if _, native := src.Decision(nativeRef, "acceptWithExecpolicyAmendment"); native {
		t.Error("Decision reported a native mechanism for a verdict outside Claude Code's allow|deny behavior vocabulary")
	}
}

// TestCarveOut_BashWithAFilePathIsPromptCardAndDecisionRefusesIt — ADR-010 conformance
// obligation 5, tested where it is produced. S-C measured that a Bash command naming a file path
// trips a SECOND confirmation the PermissionRequest hook's allow does NOT resolve, so those
// requests must declare the prompt card AT CAPTURE and Decision must report ok=false for them —
// otherwise the daemon renders a one-tap card that silently leaves the session blocked.
func TestCarveOut_BashWithAFilePathIsPromptCardAndDecisionRefusesIt(t *testing.T) {
	src, ok := adapter.AsInteractionSource(New())
	if !ok {
		t.Fatal("claude is not an InteractionSource")
	}
	fx := loadCorpus(t, "claude-bash-permissionrequest-run1.json")

	var approval adapter.Interaction
	for _, hp := range fx.HookPayloads {
		for _, in := range src.Interactions(hp) {
			if in.Kind == adapter.KindApprovalRequest {
				approval = in
			}
		}
	}
	if approval.Kind == "" {
		t.Fatal("the recorded PermissionRequest shaped no approval_request")
	}
	if approval.Mode != adapter.ModePromptCard {
		t.Fatalf("mode = %q, want %s: `touch approval-test.txt` names a file path, which is exactly S-C's carve-out", approval.Mode, adapter.ModePromptCard)
	}
	for _, d := range approval.Decisions {
		if _, held := approval.Keystrokes[d.ID]; !held {
			t.Errorf("decision %q has no keystroke; Decision is never called on this path, so the map must be produced at capture (ADR-010 §2/§4)", d.ID)
		}
		if _, native := src.Decision(approval.Ref, d.ID); native {
			t.Errorf("Decision(%q, %q) reports a native mechanism on the carve-out path", approval.Ref, d.ID)
		}
	}
	// IS-APR-3: the map is machine-side, and the keys are read off the REAL dialog in this
	// fixture's own pty_capture — "❯ 1. Yes" and "Esc to cancel".
	if approval.Keystrokes["allow"] != "1" || approval.Keystrokes["deny"] != "\x1b" {
		t.Errorf("keystrokes = %q, want allow=1 and deny=ESC (the recorded dialog's own affordances)", approval.Keystrokes)
	}
}
