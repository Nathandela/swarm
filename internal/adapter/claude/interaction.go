package claude

// The Claude Code STRUCTURED-CAPTURE PRODUCER: ADR-010's optional InteractionSource, shaping the
// five capture=raw hook bodies of ADR-010 §5 (as amended 2026-08-07) into
// interaction-schema.md §3 items.
//
// It stays inside the frozen boundary exactly as the rest of this package does. It is PURE and
// TOTAL — one hook body in, zero or more items out, no fd, no state, no clock — and Decision
// returns a DESCRIPTOR the core writes back on the pending hook, never a write of its own (E9.2,
// ADR-001). The daemon owns everything this file does not: item ids, ordering, timestamps, §5's
// caps, redaction, the content hash, expires_at, and the D7 binding tuple (ADR-010 §3).
//
// EVERY FIELD READ HERE WAS OBSERVED IN A REAL CAPTURE. The corpus is
// testdata/interaction/*.json — verbatim spike S-B recordings of `claude` 2.1.214, see
// PROVENANCE.md beside them. Where the corpus is silent this file classifies "other" rather than
// guessing (IS-TOOL-2), and PROVENANCE.md lists exactly what is missing and why.

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/Nathandela/swarm/internal/adapter"
)

// The decision ids are Claude Code's OWN vocabulary, not a normalized one (§3.5): they are the
// two `behavior` values its PermissionRequest hook reply accepts (ADR-010 §5, envelope confirmed
// live by spike S-C). That they read the same as the §3.5 verdicts is a property of THIS CLI --
// Codex's ids are accept | acceptWithExecpolicyAmendment | cancel -- so the two are still set
// independently below.
const (
	decisionAllow = "allow"
	decisionDeny  = "deny"
)

// The PermissionRequest hook reply bodies. The hookSpecificOutput WRAPPER is load-bearing and
// was paid for in spike S-C: an identical decision object emitted at the TOP level was silently
// ignored by the CLI and the prompt fell through to the interactive keystroke every time, at
// every staged delay. Wrapped, the transcript reads "Allowed by PermissionRequest hook" and the
// command runs with zero keystrokes.
const (
	replyAllow = `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}`
	replyDeny  = `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny"}}}`
)

// Claude Code's PermissionRequest body carries NO per-request id of its own -- S-B's captures
// have session_id, prompt_id and tool_name but nothing naming this request, and the tool_use_id
// that PreToolUse carries is absent here. So the ref is SYNTHESIZED, and it encodes the apply
// mode because Decision is handed nothing but the ref and this adapter is stateless: it cannot
// otherwise know that the request it is being asked to answer was the carve-out. The capture
// instant is in the ref so two requests for the same tool in one turn stay distinct items --
// the daemon folds successive records under one item_id BY REF (skeleton's itemIDLocked), and a
// ref that repeated would fold a second approval into a resolved one, violating IS-ST-1.
const (
	approvalRefPrefix     = "permission-request:"
	approvalRefCard       = approvalRefPrefix + "card:"
	approvalRefPromptCard = approvalRefPrefix + "prompt-card:"
)

// pathish are the bytes whose presence in a Bash command means it may name a file path, which is
// spike S-C's measured carve-out: such a command trips a SECOND "sensitive file" confirmation
// that the PermissionRequest hook's own `allow` does NOT resolve, so the one-tap path would leave
// the session blocked behind a dialog nobody is at the machine to answer. The test is
// deliberately CRUDE AND OVER-INCLUSIVE -- `.` alone catches `echo hello. world` -- because the
// two directions are not symmetric: a false prompt_card costs one extra tap on a path that
// always works, while a false `card` strands the session. S-C's own probe command (`kill -0
// 99999`, chosen to have no file-path argument) still classifies native, which is the case that
// keeps this from collapsing to "always prompt_card".
//
// ponytail: IT IS ALSO UNDER-INCLUSIVE, ON THE UNSAFE SIDE, and that is stated here rather than
// left to be discovered. A bare relative filename carries none of these bytes, so `touch
// Makefile`, `rm README` and `cat LICENSE` classify `card` while being exactly the class S-C
// describes ("Bash commands that manipulate a file path directly"). No character test can close
// it -- any bare word is a possible path -- so the only complete answers are to send EVERY Bash
// PermissionRequest to the prompt card, which ADR-010 §5's "Bash-with-a-file-path is the measured
// carve-out" does not say, or to widen the ADR. Both are rulings, not producer choices. It is
// latent today: nothing in production calls Decision or applies a prompt card at all
// (a1-integration.md §8.7), so `mode` reaches the phone as a rendering hint with no apply path
// behind either value. Recorded in a1b-claude-producer.md §5(c).
const pathish = "/.<>"

// hookBody is the subset of a Claude Code hook body this producer reads. The two nested members
// are RAW and decoded separately: tool responses genuinely differ in shape per tool (S-B
// recorded three -- Bash's {stdout,stderr,...}, Read's {type,file{...}}, Edit's
// {filePath,structuredPatch,...}), and a shape this struct does not model must cost that one
// member, never the whole item.
type hookBody struct {
	Prompt               string          `json:"prompt"`
	LastAssistantMessage string          `json:"last_assistant_message"` // Stop only: the agent's reply
	ToolName             string          `json:"tool_name"`
	ToolUseID            string          `json:"tool_use_id"`
	ToolInput            json.RawMessage `json:"tool_input"`
	ToolResponse         json.RawMessage `json:"tool_response"`
}

// toolInput is the per-tool argument object. Claude Code's Edit body is
// old_string/new_string/replace_all, NOT a unified diff (spike S-B's headline finding); the
// diff-shaped data lives downstream in PostToolUse's structuredPatch, which is where §3.4's
// diff_excerpt is rendered from.
type toolInput struct {
	Command  string `json:"command"`
	FilePath string `json:"file_path"`
}

// toolResponse is the union of the recorded response shapes, keyed by the members this producer
// reads from each.
type toolResponse struct {
	Stdout   string `json:"stdout"`   // Bash
	FilePath string `json:"filePath"` // Edit
	File     struct {
		Content string `json:"content"`
	} `json:"file"` // Read
	StructuredPatch []patchHunk `json:"structuredPatch"` // Edit
}

// patchHunk is one hunk of PostToolUse's structuredPatch: a unified diff in structured form.
type patchHunk struct {
	OldStart int      `json:"oldStart"`
	OldLines int      `json:"oldLines"`
	NewStart int      `json:"newStart"`
	NewLines int      `json:"newLines"`
	Lines    []string `json:"lines"`
}

// Interactions shapes ONE captured hook body into zero or more items (ADR-010 §2). It is pure,
// total and deterministic: an unparseable, truncated, garbage or unbounded body yields no item
// rather than a panic, and no branch returns content it did not read out of p.Raw.
//
// An event with no tool_name (or, for a prompt, no prompt) shapes NOTHING. That is not laziness:
// a card built from a body that names no tool would render an approval with an empty headline,
// and IS-ENV-3's posture is that an item which cannot be shaped is not emitted at all.
func (claudeAdapter) Interactions(p adapter.HookPayload) []adapter.Interaction {
	var b hookBody
	if err := json.Unmarshal(p.Raw, &b); err != nil {
		return nil
	}
	var in toolInput
	_ = json.Unmarshal(b.ToolInput, &in)

	switch p.Event {
	case "UserPromptSubmit":
		if b.Prompt == "" {
			return nil
		}
		// SourceOwner, not SourcePhone: the phone authors no prompt (D7/B43 freezes remote input
		// to live keystrokes), so everything this hook reports was typed at the machine.
		return []adapter.Interaction{{Kind: adapter.KindUserMessage, Text: b.Prompt, Source: adapter.SourceOwner}}

	case "PreToolUse":
		if b.ToolName == "" {
			return nil
		}
		return []adapter.Interaction{{
			Kind: adapter.KindToolRun, Status: adapter.StatusInProgress,
			Ref: b.ToolUseID, Tool: b.ToolName, Action: actionFor(b.ToolName, in),
		}}

	case "PostToolUse":
		if b.ToolName == "" {
			return nil
		}
		var resp toolResponse
		_ = json.Unmarshal(b.ToolResponse, &resp)
		// The CLOSE carries the same Ref as the open, which is what collapses the pair into one
		// item (IS-DELTA-3). The file_change deliberately carries NO ref: it is a distinct item,
		// and sharing the tool_use_id would fold a change and a run under one item_id.
		items := []adapter.Interaction{{
			Kind: adapter.KindToolRun, Status: adapter.StatusCompleted,
			Ref: b.ToolUseID, Tool: b.ToolName, Action: actionFor(b.ToolName, in),
			OutputExcerpt: outputExcerpt(resp),
		}}
		if fc, ok := fileChangeFrom(in, resp); ok {
			items = append(items, fc)
		}
		return items

	case "PermissionRequest":
		if b.ToolName == "" {
			return nil
		}
		return []adapter.Interaction{approvalFrom(b.ToolName, in, p.ReceivedAtMs)}

	case "Stop":
		if b.LastAssistantMessage == "" {
			return nil
		}
		// ONE record, whole and terminal: Stop fires once the reply is finished, so the CLI hands
		// over the finished text rather than increments. Hence no Ref -- this is a self-contained
		// item and the daemon mints it a fresh item_id; a shared ref would fold two consecutive
		// replies into one item and put two terminal statuses on it (IS-ST-1). IS-DELTA-1's
		// increment semantics are not violated by a single record that is the whole text.
		//
		// StopReason is left EMPTY, and that is the honest reading: no field of the recorded body
		// names a stop reason. IS-ENV-1 closes the turn on ANY terminal status, so the turn still
		// closes; writing end_turn here would be reading the CLI's docs, not the capture.
		//
		// The text is returned WHOLE. §5's MaxTextBytes is the daemon's (ADR-010 §3) -- an adapter
		// that clipped would be excerpting untrusted tool output on the wrong side of the
		// boundary, and the daemon caps and redacts before anything is journaled (§6).
		//
		// ponytail: THE REPLY ARRIVES ALL AT ONCE, AT THE END OF THE TURN, and that is the ceiling
		// of this path rather than an implementation detail to fix here. A hook fires per event,
		// not per token, and Stop is the only recorded event carrying prose at all -- so nothing in
		// this corpus can stream. IS-DELTA-1's increments stay open for a source that can (a
		// stream-json print mode, an SDK transport, a hook that emits increments); such a producer
		// carries a Ref and needs no schema change. Ruled 2026-08-07, ADR-010's amendment.
		return []adapter.Interaction{{
			Kind: adapter.KindAgentMessage, Status: adapter.StatusCompleted, Text: b.LastAssistantMessage,
		}}
	}
	return nil
}

// Decision describes how to apply one offered choice to a pending approval (ADR-010 §2). The
// argument is a DECISION ID -- this CLI's own vocabulary, the two `behavior` values above -- and
// not the normalized DecisionChoice.Verdict, which is the daemon's to classify §3.6 from.
//
// ok == false is the honest answer on two paths, and both matter: the carve-out ref, where the
// hook's allow does not resolve the second dialog and the daemon must fall back to the prompt
// card; and an id outside the CLI's own allow|deny vocabulary, where writing SOMETHING would mean
// inventing a reply body no capture ever showed on a hook that is holding a live tool call.
func (claudeAdapter) Decision(ref, decisionID string) (adapter.DecisionAction, bool) {
	if !strings.HasPrefix(ref, approvalRefCard) {
		return adapter.DecisionAction{}, false
	}
	switch decisionID {
	case decisionAllow:
		return adapter.DecisionAction{Reply: json.RawMessage(replyAllow)}, true
	case decisionDeny:
		return adapter.DecisionAction{Reply: json.RawMessage(replyDeny)}, true
	}
	return adapter.DecisionAction{}, false
}

// actionFor is interaction-schema.md §7's structured summary, produced machine-side per IS-TOOL-1
// so a card reads "Read src/main.rs" without the phone parsing an argument.
//
// Only the four tools S-B actually recorded are classified. `search` and `fetch` have no arm
// because no Grep/Glob/WebFetch body was ever captured, and their argument key would be a guess
// -- IS-TOOL-2 says an unclassifiable call is "other", never guessed at, and PROVENANCE.md
// records the gap. Each is one recorded payload away from an arm here.
func actionFor(tool string, in toolInput) adapter.ToolAction {
	switch tool {
	case "Bash":
		return adapter.ToolAction{Type: "execute", Command: in.Command}
	case "Read":
		return adapter.ToolAction{Type: "read", Path: in.FilePath}
	case "Edit":
		return adapter.ToolAction{Type: "edit", Path: in.FilePath}
	case "Write":
		return adapter.ToolAction{Type: "write", Path: in.FilePath}
	}
	return adapter.ToolAction{Type: "other"}
}

// outputExcerpt is the tool's own output: Bash's stdout, or a Read's file content. stderr is
// deliberately not folded in -- interleaving two streams into one excerpt is a rendering
// decision, and every recorded stderr was empty, so there is nothing to base one on.
func outputExcerpt(resp toolResponse) string {
	if resp.Stdout != "" {
		return resp.Stdout
	}
	return resp.File.Content
}

// fileChangeFrom renders §3.4's file_change out of PostToolUse's structuredPatch. IS-FC-1 is why
// it hangs off PostToolUse and not PreToolUse: only an APPLIED change is a file_change, and the
// proposed edit is the approval_request.
//
// `change` is read off the hunks' own unified-diff arithmetic rather than off a tool name: a
// patch in which nothing existed before (every hunk oldLines == 0) creates the file, anything
// else modifies it. `delete` and `rename` have no arm because Claude Code has no tool that emits
// a structuredPatch for either.
func fileChangeFrom(in toolInput, resp toolResponse) (adapter.Interaction, bool) {
	if len(resp.StructuredPatch) == 0 {
		return adapter.Interaction{}, false
	}
	path := resp.FilePath
	if path == "" {
		path = in.FilePath
	}
	var diff strings.Builder
	added, removed, existed := 0, 0, false
	for i, h := range resp.StructuredPatch {
		if i > 0 {
			diff.WriteByte('\n')
		}
		diff.WriteString("@@ -" + strconv.Itoa(h.OldStart) + "," + strconv.Itoa(h.OldLines) +
			" +" + strconv.Itoa(h.NewStart) + "," + strconv.Itoa(h.NewLines) + " @@")
		if h.OldLines > 0 {
			existed = true
		}
		for _, l := range h.Lines {
			diff.WriteByte('\n')
			diff.WriteString(l)
			switch {
			case strings.HasPrefix(l, "+"):
				added++
			case strings.HasPrefix(l, "-"):
				removed++
			}
		}
	}
	change := "modify"
	if !existed {
		change = "create"
	}
	return adapter.Interaction{
		Kind: adapter.KindFileChange, Path: path, Change: change,
		DiffExcerpt: diff.String(), Added: added, Removed: removed,
	}, true
}

// approvalFrom shapes §3.5's approval_request, declaring its apply mode AT CAPTURE -- the only
// place the carve-out is decidable, because this is where the tool and its input are both
// visible (ADR-010 §4).
//
// The decisions are the CLI's own two `behavior` values and NOT its permission_suggestions, and
// that is a deliberate reading of the evidence rather than an omission. Neither half of a
// suggestion is answerable today: no capture ever showed a hook reply body that APPLIES one
// (S-C proved only {"behavior":"allow"}), and the recorded Bash dialog rules out reaching it by
// keystroke -- that body offered TWO suggestions (addDirectories, setMode) while its dialog
// showed ONE middle entry, so suggestion index is not menu position and any keystroke derived
// from it would press the wrong button. Carrying them is additive the day a capture shows how to
// answer one; manufacturing an answer now would put invented bytes on a live hook.
func approvalFrom(tool string, in toolInput, receivedAtMs int64) adapter.Interaction {
	action := actionFor(tool, in)
	item := adapter.Interaction{
		Kind: adapter.KindApprovalRequest, Status: adapter.StatusInProgress,
		Summary: summaryFor(tool, action), Action: action,
		Decisions: []adapter.DecisionChoice{
			{ID: decisionAllow, Label: "Yes", Verdict: adapter.VerdictAllow},
			{ID: decisionDeny, Label: "No", Verdict: adapter.VerdictDeny},
		},
		Mode: adapter.ModeCard,
	}
	refKind := approvalRefCard
	if tool == "Bash" && strings.ContainsAny(in.Command, pathish) {
		item.Mode = adapter.ModePromptCard
		refKind = approvalRefPromptCard
		// Produced HERE because Decision is never called on this path (ADR-010 §2/§4), and read
		// off the real dialog in claude-bash-permissionrequest-run1.json's own pty_capture:
		// "❯ 1. Yes" and, on its own line, "Esc to cancel". Both are position-INDEPENDENT, which
		// the numbered "No" is not: its number is the menu length, and the body does not
		// determine the menu length (the same capture shows 2 suggestions against 3 entries).
		//
		// ponytail: MACHINE-SIDE. IS-APR-3 forbids this map on the item and IS-LIFE-6 forbids
		// the phone authoring a keystroke; it rides Interaction only because Interaction is the
		// one adapter->core carrier, and the daemon drops it before anything is journaled.
		item.Keystrokes = map[string]string{decisionAllow: "1", decisionDeny: "\x1b"}
	}
	item.Ref = refKind + tool + ":" + strconv.FormatInt(receivedAtMs, 10)
	return item
}

// summaryFor is §3.5's one-line card headline. It is built from the LITERAL action -- the command
// that will run, the path that will change -- and deliberately not from Bash's `description`
// member, even though the CLI supplies one and it reads better: that text is the agent's own
// prose about what it intends, and an approval card is the one surface where the human is being
// asked to authorize the thing itself. The card shows what runs.
func summaryFor(tool string, a adapter.ToolAction) string {
	arg := a.Command
	if arg == "" {
		arg = a.Path
	}
	if arg == "" {
		return tool
	}
	return tool + " " + arg
}
