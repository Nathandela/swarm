package adapter

// ADR-010 — the OPTIONAL, ADDITIVE structured-capture extension of the frozen
// adapter contract, and the conformance additions it obliges (its E9.2/E9.4
// clauses, "Conformance obligations" 1-5).
//
// These tests pin three things:
//
//  1. The SHAPE of the extension — Interaction (pure data, the normalized fields
//     interaction-schema.md §3 lets an adapter source), InteractionSource, and
//     DecisionAction with its ONE Reply field. Compile-anchored, exactly as
//     adapter_test.go pins the frozen contract.
//  2. ABSENCE is detectable — an adapter implementing no InteractionSource is
//     complete and fully supported, and the daemon can see that it must fall back
//     to the generic S-A derivation (ADR-010 §5 "Generic fallback").
//  3. The conformance obligations have TEETH — a conformant capturing adapter
//     passes, and each single-defect violator fails on its own rule.

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestInteractionExtension_FrozenTypeShape constructs every extension data type
// using EVERY field by name; a removed or renamed field breaks compilation.
func TestInteractionExtension_FrozenTypeShape(t *testing.T) {
	_ = Interaction{
		Kind:             KindToolRun,
		Status:           StatusInProgress,
		Ref:              "call-1",
		Text:             "hello",
		Source:           SourceOwner,
		StopReason:       "end_turn",
		Tool:             "Read",
		Action:           ToolAction{Type: "read", Path: "a.go", Query: "needle", Command: "ls -l"},
		OutputExcerpt:    "out",
		TruncationMarker: "... [output truncated]",
		ExitCode:         0,
		Path:             "a.go",
		Change:           "modify",
		OldPath:          "b.go",
		DiffExcerpt:      "@@ -1 +1 @@",
		Added:            1,
		Removed:          2,
		Summary:          "run ls -l",
		Decisions:        []DecisionChoice{{ID: "allow", Label: "Allow"}},
		Mode:             ModeCard,
		PromptLines:      []string{"Allow?"},
		Keystrokes:       map[string]string{"allow": "1\r"},
		Revision:         3,
		Steps:            []PlanStep{{Text: "step", State: "pending"}},
	}
	_ = DecisionAction{Reply: json.RawMessage(`{"behavior":"allow"}`)}

	// The extension is discovered by type assertion, never by an Adapter method:
	// the frozen method set is unchanged (ADR-010 Non-goals).
	var _ InteractionSource = captureAdapter{}
	var _ func(Adapter) (InteractionSource, bool) = AsInteractionSource
	var _ func(Adapter, Fixture) []error = CheckInteractionFixture
}

// TestInteractionKindsAndStatuses_MatchTheSchema pins the wire vocabulary to
// interaction-schema.md §3/§4/§3.5 verbatim. A typo here would journal a kind no
// consumer knows, and IS-COMPAT-1 would silently skip every such item.
func TestInteractionKindsAndStatuses_MatchTheSchema(t *testing.T) {
	for _, tc := range []struct{ got, want string }{
		{KindUserMessage, "user_message"},
		{KindAgentMessage, "agent_message"},
		{KindToolRun, "tool_run"},
		{KindFileChange, "file_change"},
		{KindApprovalRequest, "approval_request"},
		{KindApprovalResolved, "approval_resolved"},
		{KindPlanUpdate, "plan_update"},
		{KindSessionStatus, "session_status"},
		{StatusInProgress, "in_progress"},
		{StatusCompleted, "completed"},
		{StatusFailed, "failed"},
		{StatusDeclined, "declined"},
		{ModeCard, "card"},
		{ModePromptCard, "prompt_card"},
		{SourcePhone, "phone"},
		{SourceOwner, "owner"},
		{SourceDerived, "derived"},
		{DescriptorCapture, "capture"},
		{CaptureRaw, "raw"},
	} {
		if tc.got != tc.want {
			t.Errorf("constant = %q, want %q (interaction-schema.md §3/§4, ADR-010 §1)", tc.got, tc.want)
		}
	}
}

// TestDecisionAction_CarriesOnlyTheReplyBody — ADR-010 §2 gives DecisionAction
// exactly one field. The prompt-card decision->keystroke map is produced AT
// CAPTURE and held machine-side (IS-APR-3, IS-LIFE-6); a Keys field on the apply
// descriptor would invite the very implementation those rules forbid.
func TestDecisionAction_CarriesOnlyTheReplyBody(t *testing.T) {
	ty := reflect.TypeOf(DecisionAction{})
	var names []string
	for i := 0; i < ty.NumField(); i++ {
		names = append(names, ty.Field(i).Name)
	}
	if len(names) != 1 || names[0] != "Reply" {
		t.Fatalf("DecisionAction fields = %v, want exactly [Reply]; the decision->keystroke map is machine-side capture data, never an apply-descriptor field (ADR-010 §2, IS-APR-3)", names)
	}
}

// TestAsInteractionSource_AbsenceIsDetectable — an adapter that implements no
// InteractionSource is complete and supported; the daemon detects the absence and
// falls back to the generic S-A derivation (ADR-010 §5).
func TestAsInteractionSource_AbsenceIsDetectable(t *testing.T) {
	if _, ok := AsInteractionSource(baseAdapter{}); ok {
		t.Error("a non-capturing adapter reported an InteractionSource; the generic-fallback signal must be detectable")
	}
	src, ok := AsInteractionSource(captureAdapter{})
	if !ok || src == nil {
		t.Fatalf("AsInteractionSource(captureAdapter{}) = (%v, %v), want a non-nil source", src, ok)
	}
	if items := src.Interactions(HookPayload{Event: "UserPromptSubmit", Raw: json.RawMessage(`{"prompt":"hi"}`)}); len(items) != 1 {
		t.Fatalf("shaped %d items through the discovered source, want 1", len(items))
	}
}

// TestInteractionValidate — the structural well-formedness of one shaped item:
// every enum is the schema's, and the prompt-card/keystroke pairing holds. Size
// caps are NOT checked here: §5's caps, ids, timestamps and hashes are all
// daemon-side (ADR-010 §3).
func TestInteractionValidate(t *testing.T) {
	valid := Interaction{Kind: KindToolRun, Status: StatusInProgress, Tool: "Read", Action: ToolAction{Type: "read", Path: "a.go"}}
	if err := valid.Validate(); err != nil {
		t.Fatalf("baseline item invalid: %v", err)
	}
	if err := (Interaction{Kind: KindAgentMessage}).Validate(); err != nil {
		t.Fatalf("an item with no status (a kind that carries none) was rejected: %v", err)
	}

	// allowOnly keeps the approval cases SINGLE-DEFECT: an approval_request needs
	// at least one decision to be renderable, so a case probing another rule must
	// still carry one.
	allowOnly := []DecisionChoice{{ID: "allow", Label: "Allow"}}

	cases := []struct {
		name    string
		in      Interaction
		keyword string
	}{
		{"empty-kind", Interaction{}, "kind"},
		{"unknown-kind", Interaction{Kind: "telepathy"}, "kind"},
		{"unknown-status", Interaction{Kind: KindToolRun, Status: "pending"}, "status"},
		{"unknown-source", Interaction{Kind: KindUserMessage, Source: "telepathy"}, "source"},
		{"unknown-stop-reason", Interaction{Kind: KindAgentMessage, StopReason: "bored"}, "stop_reason"},
		{"unknown-change", Interaction{Kind: KindFileChange, Path: "a.go", Change: "mangle"}, "change"},
		{"old-path-without-rename", Interaction{Kind: KindFileChange, Path: "a.go", Change: "modify", OldPath: "b.go"}, "old_path"},
		{"unknown-action-type", Interaction{Kind: KindToolRun, Action: ToolAction{Type: "divine"}}, "action"},
		{"unknown-mode", Interaction{Kind: KindApprovalRequest, Mode: "banner"}, "mode"},
		{"decision-without-id", Interaction{Kind: KindApprovalRequest, Mode: ModeCard, Decisions: []DecisionChoice{{Label: "Allow"}}}, "decisions"},
		{"approval-with-no-decisions", Interaction{Kind: KindApprovalRequest, Mode: ModeCard, Summary: "run ls"}, "decisions"},
		{"keystrokes-on-a-card", Interaction{Kind: KindApprovalRequest, Mode: ModeCard, Decisions: allowOnly, Keystrokes: map[string]string{"allow": "1\r"}}, "keystroke"},
		{"prompt-lines-on-a-card", Interaction{Kind: KindApprovalRequest, Mode: ModeCard, Decisions: allowOnly, PromptLines: []string{"?"}}, "prompt_lines"},
		{"unknown-step-state", Interaction{Kind: KindPlanUpdate, Steps: []PlanStep{{Text: "s", State: "maybe"}}}, "state"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.in.Validate()
			if err == nil {
				t.Fatalf("Validate accepted a malformed item (expected a violation about %q)", tc.keyword)
			}
			if !strings.Contains(strings.ToLower(err.Error()), tc.keyword) {
				t.Errorf("violation %q does not mention %q", err, tc.keyword)
			}
		})
	}
}

// TestConformance_AcceptsCapturingAdapter — the extension checks must find ZERO
// violations in a conformant capturing adapter, and must stay silent for an
// adapter that opts out entirely (every existing adapter, ADR-010 Consequences).
func TestConformance_AcceptsCapturingAdapter(t *testing.T) {
	if errs := CheckConformance(captureAdapter{}); len(errs) != 0 {
		t.Fatalf("conformant capturing adapter reported %d violation(s): %v", len(errs), errs)
	}
	for _, kw := range []string{"capture", "interactionsource", "interactions", "decision"} {
		if errsContain(CheckConformance(baseAdapter{}), kw) {
			t.Errorf("a non-capturing adapter was flagged for %q; the extension is OPTIONAL", kw)
		}
	}
}

// TestConformance_RejectsInteractionViolations — the teeth test for ADR-010's
// conformance obligations 1, 3 and 5. Each violator must produce at least one
// violation naming its broken rule.
func TestConformance_RejectsInteractionViolations(t *testing.T) {
	cases := []struct {
		name    string
		adapter Adapter
		keyword string
	}{
		{"capture-without-source", captureWithoutSource{}, "interactionsource"},
		{"source-without-capture", sourceWithoutCapture{}, "capture"},
		{"capture-on-non-event-row", captureOnNonEventRow{}, "event"},
		{"capture-unknown-mode", captureUnknownMode{}, "capture"},
		{"interactions-panics", panicInteractions{}, "interactions"},
		{"interactions-nondeterministic", &nondeterministicInteractions{}, "determin"},
		{"interactions-bad-kind", badKindInteractions{}, "kind"},
		{"prompt-card-without-keystrokes", promptCardWithoutKeys{}, "keystroke"},
		{"card-with-no-native-mechanism", cardWithNoNativeMechanism{}, "decision"},
		{"approval-without-mode", modelessApproval{}, "mode"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := CheckConformance(tc.adapter)
			if len(errs) == 0 {
				t.Fatalf("%s: harness reported NO violation (expected one about %q)", tc.name, tc.keyword)
			}
			if !errsContain(errs, tc.keyword) {
				t.Errorf("%s: violations %v do not mention %q", tc.name, errs, tc.keyword)
			}
		})
	}
}

// TestCheckConformance_InteractionsTotalityIsProbed — obligation 1: the probe
// battery must actually feed nil, empty, truncated, deeply nested, garbage and
// oversized bodies. A harness that skipped it would green-light panicInteractions,
// which dereferences byte zero of the body.
func TestCheckConformance_InteractionsTotalityIsProbed(t *testing.T) {
	if errsContain(CheckConformance(captureAdapter{}), "interactions") {
		t.Error("a total Interactions was flagged")
	}
	if !errsContain(CheckConformance(panicInteractions{}), "interactions") {
		t.Error("a panicking Interactions was NOT flagged — the totality probe is missing")
	}
	var sawNil, sawTruncated, sawOversized bool
	for _, p := range interactionProbes {
		switch {
		case len(p.Raw) == 0:
			sawNil = true
		case !json.Valid(p.Raw):
			sawTruncated = true
		case len(p.Raw) > 16<<10: // larger than interaction-schema.md §5's MaxItemBytes
			sawOversized = true
		}
	}
	if !sawNil || !sawTruncated || !sawOversized {
		t.Errorf("probe battery is incomplete: nil=%v truncated/garbage=%v oversized=%v (ADR-010 conformance obligation 1)", sawNil, sawTruncated, sawOversized)
	}
}

// TestCheckInteractionFixture_ReplaysTheCorpus — obligation 4 (fixture replay)
// and obligation 3's corpus half: a recorded payload that shapes items must come
// from an event row declaring capture=raw, because at runtime an undeclared
// event's body is flattened to strings before the shaper ever sees it.
func TestCheckInteractionFixture_ReplaysTheCorpus(t *testing.T) {
	fx := captureFixture()

	if errs := CheckInteractionFixture(captureAdapter{}, fx); len(errs) != 0 {
		t.Fatalf("conformant capturing adapter reported %d violation(s) on its own corpus: %v", len(errs), errs)
	}
	if errs := CheckInteractionFixture(baseAdapter{}, fx); len(errs) != 0 {
		t.Errorf("a non-capturing adapter was flagged on a corpus replay: %v", errs)
	}
	errs := CheckInteractionFixture(shapesUndeclaredEvent{}, fx)
	if !errsContain(errs, "capture") {
		t.Errorf("shaping items out of an event that declares no capture=raw was NOT flagged: %v", errs)
	}
	if !errsContain(CheckInteractionFixture(badKindInteractions{}, fx), "kind") {
		t.Error("a malformed shaped item was NOT flagged on corpus replay")
	}
}

// TestCheckInteractionFixture_ProvesThePromptCardCarveOut — obligation 5, tested
// where it is PRODUCED: the carve-out payload shapes an item declaring
// mode: prompt_card, the adapter reports ok==false from Decision for that ref,
// and the machine-side decision->keystroke map exists on the capture result.
func TestCheckInteractionFixture_ProvesThePromptCardCarveOut(t *testing.T) {
	src, ok := AsInteractionSource(captureAdapter{})
	if !ok {
		t.Fatal("captureAdapter is not an InteractionSource")
	}
	var carveOut *Interaction
	for _, hp := range captureFixture().HookPayloads {
		for _, in := range src.Interactions(hp) {
			if in.Kind == KindApprovalRequest && in.Mode == ModePromptCard {
				item := in
				carveOut = &item
			}
		}
	}
	if carveOut == nil {
		t.Fatal("the corpus shaped no prompt_card approval; the carve-out is unexercised")
	}
	if len(carveOut.Keystrokes) == 0 {
		t.Error("prompt_card item carries no machine-side decision->keystroke map (ADR-010 §4: produced at capture, because Decision is never called on this path)")
	}
	for _, d := range carveOut.Decisions {
		if _, ok := src.Decision(carveOut.Ref, d.ID); ok {
			t.Errorf("Decision(%q, %q) reported a native mechanism for a prompt_card request", carveOut.Ref, d.ID)
		}
		if _, held := carveOut.Keystrokes[d.ID]; !held {
			t.Errorf("no keystroke held for decision %q", d.ID)
		}
	}
	// The native path is the exercised counterpart: ok==true with a reply body.
	act, ok := src.Decision("req-1", "allow")
	if !ok || !json.Valid(act.Reply) || len(act.Reply) == 0 {
		t.Errorf("Decision on the native path = (%q, %v), want a valid non-empty reply body", act.Reply, ok)
	}
}

// captureFixture is a recorded-shaped corpus for the capturing stub: one payload
// per shaped event plus the prompt-card carve-out, on the E9.4 fixture schema.
func captureFixture() Fixture {
	return Fixture{
		SchemaVersion: FixtureSchemaVersion,
		CLI:           "stub-cli",
		Version:       "1.0.0",
		Scenario:      "prompt-tool-permission",
		PTYCapture:    []byte("Welcome\r\nconv-id=abc123\r\n> \r\n"),
		HookPayloads: []HookPayload{
			{Event: "UserPromptSubmit", Raw: json.RawMessage(`{"prompt":"read a.go"}`), ReceivedAtMs: 1710000000000},
			{Event: "PreToolUse", Raw: json.RawMessage(`{"tool":"Read","path":"a.go","ref":"t-1"}`), ReceivedAtMs: 1710000000100},
			{Event: "PermissionRequest", Raw: json.RawMessage(`{"tool":"Read","ref":"req-1"}`), ReceivedAtMs: 1710000000200},
			{Event: "PermissionRequest", Raw: json.RawMessage(`{"tool":"Bash","ref":"bash-1"}`), ReceivedAtMs: 1710000000300},
			{Event: "Stop", Raw: json.RawMessage(`{"reason":"done"}`), ReceivedAtMs: 1710000000400},
		},
	}
}
