package adapter

// ADR-010 — test stubs for the OPTIONAL structured-capture extension.
//
// captureAdapter is a fully conformant capturing strategy object: it embeds the
// conformant baseAdapter (stubs_test.go), declares capture=raw on the event rows
// it shapes, and implements InteractionSource purely and totally. Every violator
// below embeds captureAdapter and breaks EXACTLY ONE ADR-010 conformance
// obligation, so a failure pinpoints the rule under test — the same discipline
// the frozen-contract violators in stubs_test.go use.
//
// The stubs live in their own file so stubs_test.go (the frozen-contract
// battery) stays untouched by this extension.

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
)

// promptCardRefPrefix is the stub's stand-in for spike-SC's Bash-with-a-file-path
// carve-out: a request whose ref carries this prefix has no native apply
// mechanism, so Decision reports ok==false and the shaped item declares
// mode: prompt_card (ADR-010 §5, conformance obligation 5).
const promptCardRefPrefix = "bash-"

// captureAdapter is the conformant capturing reference stub.
type captureAdapter struct{ baseAdapter }

// SignalSources declares capture=raw on exactly the rows Interactions shapes,
// leaves Stop undeclared (a status-only row), and keeps the grid heuristic.
func (captureAdapter) SignalSources() []SignalSource {
	return []SignalSource{
		{Kind: "hook", Descriptor: map[string]string{"event": "UserPromptSubmit", DescriptorCapture: CaptureRaw}},
		{Kind: "hook", Descriptor: map[string]string{"event": "PreToolUse", DescriptorCapture: CaptureRaw}},
		{Kind: "hook", Descriptor: map[string]string{"event": "PermissionRequest", DescriptorCapture: CaptureRaw}},
		{Kind: "hook", Descriptor: map[string]string{"event": "Stop"}},
		{Kind: "heuristic", Descriptor: map[string]string{"grid": "stub"}},
	}
}

// stubBody is the shape captureAdapter reads out of a captured body. Decoding
// into a typed struct is what makes Interactions total: a nil, truncated or
// garbage body fails the unmarshal and yields no items.
type stubBody struct {
	Prompt string `json:"prompt"`
	Tool   string `json:"tool"`
	Path   string `json:"path"`
	Ref    string `json:"ref"`
}

func (captureAdapter) Interactions(p HookPayload) []Interaction {
	var body stubBody
	if len(p.Raw) == 0 || json.Unmarshal(p.Raw, &body) != nil {
		return nil
	}
	switch p.Event {
	case "UserPromptSubmit":
		if body.Prompt == "" {
			return nil
		}
		return []Interaction{{Kind: KindUserMessage, Status: StatusCompleted, Text: body.Prompt, Source: SourceOwner}}
	case "PreToolUse":
		if body.Tool == "" {
			return nil
		}
		return []Interaction{{
			Kind:   KindToolRun,
			Status: StatusInProgress,
			Ref:    body.Ref,
			Tool:   body.Tool,
			Action: ToolAction{Type: "read", Path: body.Path},
		}}
	case "PermissionRequest":
		if body.Ref == "" {
			return nil
		}
		in := Interaction{
			Kind:    KindApprovalRequest,
			Status:  StatusInProgress,
			Ref:     body.Ref,
			Summary: "run " + body.Tool,
			Action:  ToolAction{Type: "execute", Command: body.Tool},
			Decisions: []DecisionChoice{
				{ID: "allow", Label: "Allow", Verdict: VerdictAllow},
				{ID: "deny", Label: "Deny", Verdict: VerdictDeny},
			},
			Mode: ModeCard,
		}
		if strings.HasPrefix(body.Ref, promptCardRefPrefix) {
			in.Mode = ModePromptCard
			in.PromptLines = []string{"Allow " + body.Tool + "?"}
			in.Keystrokes = map[string]string{"allow": "1\r", "deny": "2\r"}
		}
		return []Interaction{in}
	}
	return nil
}

func (captureAdapter) Decision(ref, verdict string) (DecisionAction, bool) {
	if ref == "" || verdict == "" || strings.HasPrefix(ref, promptCardRefPrefix) {
		return DecisionAction{}, false
	}
	return DecisionAction{Reply: json.RawMessage(`{"decision":"` + verdict + `"}`)}, true
}

// ---------------------------------------------------------------------------
// Targeted violators — each breaks exactly one ADR-010 obligation.
// ---------------------------------------------------------------------------

// captureWithoutSource violates: a capture=raw declaration must be backed by an
// InteractionSource, or the body is preserved for a shaper that does not exist.
type captureWithoutSource struct{ baseAdapter }

func (captureWithoutSource) SignalSources() []SignalSource {
	return []SignalSource{{Kind: "hook", Descriptor: map[string]string{"event": "Stop", DescriptorCapture: CaptureRaw}}}
}

// sourceWithoutCapture violates: an InteractionSource must declare capture=raw
// on at least one event row, or shaping never receives a body.
type sourceWithoutCapture struct{ captureAdapter }

func (sourceWithoutCapture) SignalSources() []SignalSource {
	return []SignalSource{{Kind: "hook", Descriptor: map[string]string{"event": "Stop"}}}
}

// captureOnNonEventRow violates: every declared capture key must name a real
// event row (ADR-010 conformance obligation 3).
type captureOnNonEventRow struct{ captureAdapter }

func (captureOnNonEventRow) SignalSources() []SignalSource {
	return []SignalSource{{Kind: "heuristic", Descriptor: map[string]string{"grid": "stub", DescriptorCapture: CaptureRaw}}}
}

// captureUnknownMode violates: "raw" is the only capture value ADR-010 §1
// defines; an unrecognized one would silently disable capture.
type captureUnknownMode struct{ captureAdapter }

func (captureUnknownMode) SignalSources() []SignalSource {
	return []SignalSource{{Kind: "hook", Descriptor: map[string]string{"event": "Stop", DescriptorCapture: "cooked"}}}
}

// panicInteractions violates: Interactions must be TOTAL (never panics on a nil,
// truncated, garbage or unbounded body).
type panicInteractions struct{ captureAdapter }

func (panicInteractions) Interactions(p HookPayload) []Interaction {
	_ = p.Raw[0] // panics on an empty/nil body
	return nil
}

// nondeterministicInteractions violates: Interactions is PURE (same body -> same
// items).
type nondeterministicInteractions struct {
	captureAdapter
	n atomic.Int64
}

func (i *nondeterministicInteractions) Interactions(HookPayload) []Interaction {
	return []Interaction{{Kind: KindAgentMessage, Status: StatusInProgress, Text: fmt.Sprintf("%d", i.n.Add(1))}}
}

// badKindInteractions violates: a shaped item's Kind must be one of the eight
// (interaction-schema.md §3).
type badKindInteractions struct{ captureAdapter }

func (badKindInteractions) Interactions(HookPayload) []Interaction {
	return []Interaction{{Kind: "telepathy", Status: StatusCompleted}}
}

// promptCardWithoutKeys violates ADR-010 conformance obligation 5: a
// prompt_card request must carry the machine-side decision->keystroke map the
// adapter produces AT CAPTURE, because Decision is never called on that path.
type promptCardWithoutKeys struct{ captureAdapter }

func (promptCardWithoutKeys) Interactions(HookPayload) []Interaction {
	return []Interaction{{
		Kind:        KindApprovalRequest,
		Status:      StatusInProgress,
		Ref:         promptCardRefPrefix + "1",
		Summary:     "run Bash",
		Decisions:   []DecisionChoice{{ID: "allow", Label: "Allow", Verdict: VerdictAllow}},
		Mode:        ModePromptCard,
		PromptLines: []string{"Allow Bash?"},
	}}
}

// cardWithNoNativeMechanism violates ADR-010 conformance obligation 5 from the
// other side: an item declaring mode: card claims Decision applies it natively,
// but Decision reports ok==false for that ref.
type cardWithNoNativeMechanism struct{ captureAdapter }

func (cardWithNoNativeMechanism) Interactions(HookPayload) []Interaction {
	return []Interaction{{
		Kind:      KindApprovalRequest,
		Status:    StatusInProgress,
		Ref:       promptCardRefPrefix + "1", // Decision returns ok==false for this ref
		Summary:   "run Bash",
		Decisions: []DecisionChoice{{ID: "allow", Label: "Allow", Verdict: VerdictAllow}},
		Mode:      ModeCard,
	}}
}

// modelessApproval violates ADR-010 §4: an approval_request declares its apply
// mechanism AT CAPTURE, so the daemon knows before the phone renders whether the
// request resolves natively or degrades to a prompt card.
type modelessApproval struct{ captureAdapter }

func (modelessApproval) Interactions(HookPayload) []Interaction {
	return []Interaction{{
		Kind:      KindApprovalRequest,
		Status:    StatusInProgress,
		Ref:       "req-1",
		Summary:   "run Read",
		Decisions: []DecisionChoice{{ID: "allow", Label: "Allow", Verdict: VerdictAllow}},
	}}
}

// decisionWithoutVerdict violates the 2026-08-07 owner ruling: every decision on
// an approval_request carries a verdict (allow | deny | other), set by the adapter
// at capture from its own CLI vocabulary. Without it the daemon cannot classify
// §3.6's allowed/denied and resolves the card as "not a denial" whatever the owner
// tapped — a wrong transcript line, given silently.
type decisionWithoutVerdict struct{ captureAdapter }

func (decisionWithoutVerdict) Interactions(HookPayload) []Interaction {
	return []Interaction{{
		Kind:      KindApprovalRequest,
		Status:    StatusInProgress,
		Ref:       "req-1",
		Summary:   "run Read",
		Decisions: []DecisionChoice{{ID: "allow", Label: "Allow"}},
		Mode:      ModeCard,
	}}
}

// shapesUndeclaredEvent violates ADR-010 conformance obligation 3's corpus half:
// it shapes items out of PreToolUse while declaring capture=raw only on the other
// two rows, so at runtime that body would already have been flattened away.
type shapesUndeclaredEvent struct{ captureAdapter }

func (shapesUndeclaredEvent) SignalSources() []SignalSource {
	return []SignalSource{
		{Kind: "hook", Descriptor: map[string]string{"event": "UserPromptSubmit", DescriptorCapture: CaptureRaw}},
		{Kind: "hook", Descriptor: map[string]string{"event": "PermissionRequest", DescriptorCapture: CaptureRaw}},
		{Kind: "hook", Descriptor: map[string]string{"event": "PreToolUse"}},
	}
}
