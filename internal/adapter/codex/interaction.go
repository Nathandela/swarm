package codex

// The Codex STRUCTURED-CAPTURE PRODUCER (Wave R7, Mirror M4.2/M4.3): ADR-010's optional
// InteractionSource, shaping one `codex app-server` JSON-RPC frame into
// interaction-schema.md §3 items. It is what makes Codex a structured chat provider EQUAL to
// Claude rather than a grid-heuristic session.
//
// THE CARRIER. p.Event is the JSON-RPC METHOD and p.Raw is the WHOLE FRAME, verbatim
// (ADR-013 §R7.3). That is deliberate reuse of the HookPayload seam: it makes
// docs/verification/r1-codex-fixtures/frame-samples.json literally the golden vector set for
// this file, because the bytes the tests drive are the bytes the gate recorded off a live
// 0.147.0 connection.
//
// IT IS PURE, TOTAL AND STATELESS, on ExtractConversationID's terms: a nil, truncated,
// garbage or unbounded body yields zero items rather than a panic, the same frame always
// yields the same items, and no branch returns content it did not read out of p.Raw. In
// particular the DELTA FOLD IS NOT HERE: two deltas of one message carry the same Ref and
// their OWN text, and the daemon folds them (skeleton's itemIDLocked plus the producer-edge
// batcher). An adapter that accumulated would double every token once the daemon folded too.
//
// AN UNRECOGNIZED METHOD SHAPES NOTHING (ADR-013 §R7.6): a Codex upgrade that adds a frame
// shape this revision does not know degrades to the grid heuristic instead of inventing
// content.

import (
	"encoding/json"

	"github.com/Nathandela/swarm/internal/adapter"
)

// The JSON-RPC methods this revision shapes. Every one is in the RECORDED notification
// inventory (protocol-methods.txt) or the generated ServerRequest union
// (r7-schema-methods.txt); a row naming a method the server does not have can never fire.
const (
	methodItemStarted        = "item/started"
	methodItemCompleted      = "item/completed"
	methodAgentMessageDelta  = "item/agentMessage/delta"
	methodTurnCompleted      = "turn/completed"
	methodFileChangeApproval = "item/fileChange/requestApproval"
	methodCommandApproval    = "item/commandExecution/requestApproval"
)

// The FileChange/CommandExecution approval decision ids are the CLI's OWN vocabulary
// (§3.5), read off the generated FileChangeRequestApprovalResponse /
// CommandExecutionRequestApprovalResponse schemas. They are NOT normalized: the verdict
// beside each is the one normalized thing about a decision, and it is what §3.6's
// allowed/denied split is classified from.
//
// The CommandExecution union additionally has two OBJECT variants
// (acceptWithExecpolicyAmendment, applyNetworkPolicyAmendment) carrying required parameters
// a user would have to choose. DecisionChoice.ID is a string and Decision(ref, decisionID)
// is handed nothing else, so they are NOT OFFERED at all -- IS-TOOL-2's posture, a decision
// the adapter cannot place is declared rather than guessed at.
var approvalDecisions = []adapter.DecisionChoice{
	{ID: "accept", Label: "Yes, proceed", Verdict: adapter.VerdictAllow},
	{ID: "acceptForSession", Label: "Yes, and don't ask again", Verdict: adapter.VerdictAllow},
	{ID: "decline", Label: "No, keep going", Verdict: adapter.VerdictDeny},
	{ID: "cancel", Label: "No, and stop", Verdict: adapter.VerdictDeny},
}

// frame is the JSON-RPC envelope. `params` stays RAW and is decoded per method: the
// app-server's params genuinely differ in shape per method, and a shape this file does not
// model must cost that one method, never the whole frame.
type frame struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// itemParams is `item/started` / `item/completed`.
type itemParams struct {
	// TurnID is the CLI's OWN turn id, carried on EVERY content-bearing notification
	// (RECORDED: frame-samples.json -- item/started, item/completed,
	// item/agentMessage/delta and both */requestApproval frames all carry `turnId`).
	// It is Interaction.TurnRef's only source, and review BLOCKING 1 is the record of what
	// happens without it: the daemon steered with an id it minted itself.
	TurnID string `json:"turnId"`
	Item   struct {
		Type     string `json:"type"`
		ID       string `json:"id"`
		ClientID string `json:"clientId"` // userMessage: the client's own echo key
		Content  []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"` // userMessage: an ARRAY of UserInput
		Command          string `json:"command"`          // commandExecution
		Status           string `json:"status"`           // commandExecution
		AggregatedOutput string `json:"aggregatedOutput"` // commandExecution, at completion
		ExitCode         *int   `json:"exitCode"`         // commandExecution, at completion
	} `json:"item"`
}

// deltaParams is `item/agentMessage/delta`.
type deltaParams struct {
	ItemID string `json:"itemId"`
	TurnID string `json:"turnId"`
	Delta  string `json:"delta"`
}

// turnParams is `turn/completed` (and `turn/started`, which shapes nothing).
type turnParams struct {
	Turn struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Items  []struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		} `json:"items"`
	} `json:"turn"`
}

// approvalParams is the server-initiated `*/requestApproval`. Only {itemId, startedAtMs,
// threadId, turnId} are required; `command`, `cwd` and `reason` are all nullable.
type approvalParams struct {
	ItemID  string `json:"itemId"`
	TurnID  string `json:"turnId"`
	Command string `json:"command"`
	Reason  string `json:"reason"`
}

// Interactions shapes ONE app-server frame into zero or more items.
func (codexAdapter) Interactions(p adapter.HookPayload) []adapter.Interaction {
	var fr frame
	if err := json.Unmarshal(p.Raw, &fr); err != nil {
		return nil
	}
	switch p.Event {
	case methodItemStarted:
		return itemStarted(fr.Params)
	case methodItemCompleted:
		return itemCompleted(fr.Params)
	case methodAgentMessageDelta:
		return agentMessageDelta(fr.Params)
	case methodTurnCompleted:
		return turnCompleted(fr.Params)
	case methodFileChangeApproval:
		return approval(fr.Params, "edit", fileChangeSummary)
	case methodCommandApproval:
		return approval(fr.Params, "execute", commandSummary)
	}
	// Everything else -- turn/started, item/commandExecution/outputDelta,
	// serverRequest/resolved, thread/*, and every method a later CLI adds -- shapes NO item.
	//
	// outputDelta in particular is DROPPED ON PURPOSE: item/completed carries
	// `aggregatedOutput` in full, so shaping the deltas too would duplicate every byte of
	// tool output into the transcript AND burn one append-floor slot per chunk.
	// turn/started and serverRequest/resolved are LIFECYCLE facts the daemon reads for
	// status (M4.5), not content; an item shaped from turn/started would be an empty bubble
	// at the head of every turn.
	return nil
}

// itemStarted opens the item a `item/started` frame announces.
func itemStarted(params json.RawMessage) []adapter.Interaction {
	var ip itemParams
	if json.Unmarshal(params, &ip) != nil {
		return nil
	}
	switch ip.Item.Type {
	case "userMessage":
		text := userText(ip)
		if text == "" && ip.Item.ID == "" {
			return nil
		}
		return []adapter.Interaction{{
			Kind: adapter.KindUserMessage,
			Text: text,
			Ref:  ip.Item.ID,
			// SourceOwner because the ADAPTER CANNOT KNOW: it sees the same frame whoever
			// typed it. The daemon re-attributes the one IT injected, by ClientRef.
			Source:    adapter.SourceOwner,
			ClientRef: ip.Item.ClientID,
			TurnRef:   ip.TurnID,
		}}
	case "commandExecution":
		if ip.Item.ID == "" {
			return nil
		}
		return []adapter.Interaction{{
			Kind: adapter.KindToolRun, Status: adapter.StatusInProgress,
			Ref: ip.Item.ID, Tool: "commandExecution",
			Action:  adapter.ToolAction{Type: "execute", Command: ip.Item.Command},
			TurnRef: ip.TurnID,
		}}
	}
	return nil
}

// itemCompleted closes an item. A userMessage's completion carries exactly what its start
// did, so it shapes nothing: a second record would be a second journal append for one
// unchanged fact.
func itemCompleted(params json.RawMessage) []adapter.Interaction {
	var ip itemParams
	if json.Unmarshal(params, &ip) != nil {
		return nil
	}
	if ip.Item.Type != "commandExecution" || ip.Item.ID == "" {
		return nil
	}
	exit := 0
	if ip.Item.ExitCode != nil {
		exit = *ip.Item.ExitCode
	}
	return []adapter.Interaction{{
		Kind: adapter.KindToolRun, Status: commandStatus(ip.Item.Status),
		// The SAME Ref as the open, which is what collapses the pair into one item
		// (IS-DELTA-3).
		Ref: ip.Item.ID, Tool: "commandExecution",
		Action:        adapter.ToolAction{Type: "execute", Command: ip.Item.Command},
		OutputExcerpt: ip.Item.AggregatedOutput,
		ExitCode:      exit,
		TurnRef:       ip.TurnID,
	}}
}

// commandStatus maps the RECORDED CommandExecutionStatus union onto §4's item statuses.
func commandStatus(s string) string {
	switch s {
	case "failed":
		return adapter.StatusFailed
	case "declined":
		return adapter.StatusDeclined
	case "inProgress":
		return adapter.StatusInProgress
	}
	return adapter.StatusCompleted
}

// agentMessageDelta shapes ONE increment. IS-DELTA-1: Text is what THIS record appends,
// never the accumulated body -- the adapter is stateless and the daemon folds by Ref.
func agentMessageDelta(params json.RawMessage) []adapter.Interaction {
	var dp deltaParams
	if json.Unmarshal(params, &dp) != nil || dp.ItemID == "" {
		return nil
	}
	return []adapter.Interaction{{
		Kind: adapter.KindAgentMessage, Status: adapter.StatusInProgress,
		Ref: dp.ItemID, Text: dp.Delta, TurnRef: dp.TurnID,
	}}
}

// turnCompleted synthesizes the TERMINAL agent_message that closes the turn.
//
// IT MUST FIRE EVEN WHEN THE TURN CARRIES NO AGENT MESSAGE, and that is ADR-013 §R7.8 rule 1
// rather than a nicety. Both the daemon (skeleton's turnIDLocked) and the phone close a turn
// ONLY on a terminal agent_message, and an INTERRUPTED Codex turn was RECORDED with
// `items: []` and `itemsView: "notLoaded"` (turn-completed-interrupted.json). A turn that
// never closes means expected_turn never empties and every subsequent phone send is refused
// stale_turn for the life of the session -- the exact R6 round-2 blocker that broke idle
// replies 100% of the time.
//
// The record folds onto the turn's OWN agent message when `items` names one, and stands
// alone on the turn id when it does not. It carries NO TEXT either way: the deltas already
// delivered the body (IS-DELTA-1 makes text an increment, so repeating it doubles the
// message), and an interrupted turn with items:[] has no text to carry -- inventing one
// would put words in the agent's mouth.
func turnCompleted(params json.RawMessage) []adapter.Interaction {
	var tp turnParams
	if json.Unmarshal(params, &tp) != nil {
		return nil
	}
	ref := tp.Turn.ID
	for _, it := range tp.Turn.Items {
		if it.Type == "agentMessage" && it.ID != "" {
			ref = it.ID
			break
		}
	}
	status, stop := adapter.StatusCompleted, ""
	switch tp.Turn.Status {
	case "completed":
		stop = "end_turn"
	case "interrupted":
		stop = "interrupted"
	case "failed":
		status, stop = adapter.StatusFailed, "error"
	}
	return []adapter.Interaction{{
		Kind: adapter.KindAgentMessage, Status: status, Ref: ref, StopReason: stop,
		// The turn's OWN id, not the folded item's: `turn/completed` names the turn in
		// `params.turn.id` rather than in a `turnId` member.
		TurnRef: tp.Turn.ID,
	}}
}

// approval shapes one server-initiated approval request into a card.
//
// Mode is ALWAYS `card` and the keystroke map is ALWAYS absent. Codex answers natively by
// JSON-RPC, and prompt_card is the path that TYPES A KEYSTROKE (IS-LIFE-6) -- which playbook
// §8.2 forbids on this provider in as many words.
func approval(params json.RawMessage, actionType string, summarize func(approvalParams) string) []adapter.Interaction {
	var ap approvalParams
	if json.Unmarshal(params, &ap) != nil || ap.ItemID == "" {
		return nil
	}
	return []adapter.Interaction{{
		Kind: adapter.KindApprovalRequest, Status: adapter.StatusInProgress,
		// params.itemId: Decision(ref, id) is called with it, and it is what ties the
		// pending JSON-RPC server-request to the card.
		Ref:       ap.ItemID,
		Mode:      adapter.ModeCard,
		Summary:   summarize(ap),
		Action:    adapter.ToolAction{Type: actionType, Command: ap.Command},
		Decisions: append([]adapter.DecisionChoice(nil), approvalDecisions...),
		TurnRef:   ap.TurnID,
	}}
}

// commandSummary puts the COMMAND in the headline: a card whose headline omits the command
// asks the owner to approve something they cannot see.
func commandSummary(ap approvalParams) string {
	if ap.Command != "" {
		return "Run " + ap.Command
	}
	if ap.Reason != "" {
		return ap.Reason
	}
	return "Run a command"
}

// fileChangeSummary uses the server's own `reason` when it sent one. The RECORDED frame
// carries reason: null and names no path at all, so the fallback is a classification of the
// METHOD (which is observed) and never a fabricated path.
func fileChangeSummary(ap approvalParams) string {
	if ap.Reason != "" {
		return ap.Reason
	}
	return "Apply the proposed file change"
}

// userText concatenates the `text` arms of a userMessage's content array, which is where the
// prompt lives (RECORDED: frame-samples.json's item/started).
func userText(ip itemParams) string {
	out := ""
	for _, c := range ip.Item.Content {
		if c.Type == "text" {
			out += c.Text
		}
	}
	return out
}

// Decision describes how to apply one offered choice, as the JSON-RPC RESPONSE BODY the CORE
// writes back on the pending server-request (ADR-010 §4, E9.2: the adapter performs no I/O).
// The envelope is RECORDED at r1-codex-gate.md:128 -- {"decision": <id>} -- and the id is the
// CLI's own.
//
// ok == false for an id outside the union: a reply the server rejects leaves the approval
// pending while the phone believes it answered, which is strictly worse than a refusal the
// phone can see.
func (codexAdapter) Decision(_, decisionID string) (adapter.DecisionAction, bool) {
	for _, d := range approvalDecisions {
		if d.ID != decisionID {
			continue
		}
		body, err := json.Marshal(map[string]string{"decision": decisionID})
		if err != nil {
			return adapter.DecisionAction{}, false
		}
		return adapter.DecisionAction{Reply: body}, true
	}
	return adapter.DecisionAction{}, false
}
