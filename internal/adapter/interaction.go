package adapter

// ADR-010 — structured interaction capture: an OPTIONAL, ADDITIVE extension of
// the frozen contract. The Adapter method set (adapter.go) is UNCHANGED and every
// existing adapter compiles and behaves exactly as before; an adapter that
// implements nothing here is complete and fully supported, and the daemon detects
// that absence through AsInteractionSource and falls back to deriving items from
// the sanitized snapshot (ADR-010 §5 "Generic fallback").
//
// The extension keeps the boundary's original guarantee: it adds DESCRIPTORS and
// PURE FUNCTIONS, the same trick Detect(a, HostProber) already plays. No adapter
// gains an fd — Decision returns a descriptor the CORE executes, exactly as
// Command/Resume return an argv core runs (E9.2 / ADR-001).
//
// The adapter returns NORMALIZED FIELDS AND NOTHING ELSE. Item ids, ordering and
// journal cursor, timestamps, size caps and excerpting, redaction, the byte-exact
// canonicalization and its SHA-256, expires_at, the D7 binding tuple, and
// transport are ALL daemon-side (ADR-010 §3). The normative field list is
// docs/specifications/interaction-schema.md §3; this file names only the subset an
// adapter can source.

import (
	"encoding/json"
	"fmt"

	"github.com/Nathandela/swarm/internal/vt"
)

// Item kinds — interaction-schema.md §3, the AEAD-plaintext-bound discriminator.
// It lives only inside the item payload: nothing here is ever written to
// SenderKeyID or EpochID (IS-LAYER-2, PB-SYNC-1).
const (
	KindUserMessage      = "user_message"
	KindAgentMessage     = "agent_message"
	KindToolRun          = "tool_run"
	KindFileChange       = "file_change"
	KindApprovalRequest  = "approval_request"
	KindApprovalResolved = "approval_resolved"
	KindPlanUpdate       = "plan_update"
	KindSessionStatus    = "session_status"
)

// Item statuses — interaction-schema.md §4. in_progress is the only
// non-terminal one; IS-ST-1 allows at most one terminal status per item.
const (
	StatusInProgress = "in_progress"
	StatusCompleted  = "completed"
	StatusFailed     = "failed"
	StatusDeclined   = "declined"
)

// Approval apply modes — interaction-schema.md §3.5. card means Decision applies
// the verdict natively; prompt_card is IS-LIFE-6's fallback, where the machine
// injects a mapped keystroke instead.
const (
	ModeCard       = "card"
	ModePromptCard = "prompt_card"
)

// user_message sources — interaction-schema.md §3.1, plus one the wire never carries.
//
// SourceSynthetic is the CLI's OWN envelope posted through its prompt hook (Claude Code's
// system-reminder, teammate-message, task-notification, a slash command's stdout; phone refit
// W2.4, round-1 review ruling). It is a user_message because a user_message is the only
// turn-opening signal that CLI gives, and the daemon opens the turn on it -- then neither
// persists nor publishes it, so §3.1's phone|owner|derived stays the whole of the wire's
// vocabulary. Validate admits it so the daemon can see it at all.
const (
	SourcePhone     = "phone"
	SourceOwner     = "owner"
	SourceDerived   = "derived"
	SourceSynthetic = "synthetic"
)

// Decision verdicts — the grant/refuse polarity of one offered decision, set by
// the adapter AT CAPTURE from its own CLI vocabulary (owner ruling 2026-08-07,
// ADR-009's amendment of that date). interaction-schema.md §3.5 keeps the decision
// IDS the CLI's own — Codex offers accept | acceptWithExecpolicyAmendment | cancel
// — so this is the ONLY normalized thing about a decision, and it is what §3.6's
// allowed/denied split is classified from.
//
// VerdictOther is IS-TOOL-2's posture applied here: a decision the adapter cannot
// place is declared unclassified rather than guessed at.
const (
	VerdictAllow = "allow"
	VerdictDeny  = "deny"
	VerdictOther = "other"
)

// DescriptorCapture is the SignalSource.Descriptor key an adapter sets to declare
// that an event's body must be PRESERVED rather than flattened to top-level
// strings (ADR-010 §1). Capture is declared in the existing descriptor map, so
// SignalSource itself is untouched and an adapter that declares nothing behaves
// exactly as today.
const DescriptorCapture = "capture"

// CaptureRaw is the ONLY capture value ADR-010 §1 defines.
//
// ponytail: an unrecognized value is a conformance VIOLATION, not an ignored key.
// A typo would otherwise silently flatten the CLI's bodies away with no signal at
// all — the failure would surface as an empty transcript, three layers downstream.
const CaptureRaw = "raw"

// descriptorEvent is the descriptor key naming the event a row describes. It is
// the engine's lookup key (descriptorForEvent); spelled literally here so the
// contract package keeps depending on nothing but itself and internal/vt (T-5).
const descriptorEvent = "event"

// CaptureEvents returns the events a declares capture=raw on, in declaration
// order. An adapter that declares none returns nil, which is the ordinary case:
// the extension is optional (ADR-010 §5).
//
// It is the READ side of DescriptorCapture, and it exists because the declaration
// has to travel. A `swarm hook` process knows its event name but not the adapter
// that launched it, so the core resolves the rows once at spawn and injects them
// (hookclient.EnvCapture) — the same trick Detect(a, HostProber) plays, with the
// adapter supplying pure data and the core doing everything else.
func CaptureEvents(a Adapter) []string {
	var out []string
	for _, s := range a.SignalSources() {
		if s.Descriptor[DescriptorCapture] == CaptureRaw && s.Descriptor[descriptorEvent] != "" {
			out = append(out, s.Descriptor[descriptorEvent])
		}
	}
	return out
}

// Interaction is the pure, normalized content ONE adapter shaped out of ONE
// captured event body. It is the adapter's whole output: the daemon is the sole
// producer of what goes on the wire (ADR-010 §3).
//
// Fields are grouped by the kind that uses them, mirroring interaction-schema.md
// §3.1-§3.7. A field belonging to another kind is left zero.
//
// ponytail: there are no fields for approval_resolved (§3.6) or session_status
// (§3.8) because no adapter sources them — IS-LIFE-2's resolver covers five paths
// of which four are daemon-observed, IS-ST-2's sweep fires on instance death, and
// IS-SS-1 makes session_status the status.* projection the roster already derives.
// The KIND constants cover all eight (they are the wire vocabulary the daemon
// reuses) and Validate accepts all eight, so a later agent-sourced cancel is an
// additive field, not a contract change.
type Interaction struct {
	// Kind is one of the eight kinds above. Required.
	Kind string
	// Status is one of the four statuses above, or empty when the kind carries
	// none (interaction-schema.md §2: "absent means not applicable to the kind").
	Status string

	// Ref is the CLI's OWN id for this interaction, in the CLI's own vocabulary.
	// It is NOT the item_id and never reaches the phone: the daemon mints the
	// ULID item_id (IS-APR-1 leaves exactly one id on the wire) and maps Ref to
	// it. It serves two machine-side jobs, which is why there is one field and
	// not two:
	//
	//   - it is the ref Decision(ref, decisionID) is later called with, for an
	//     approval_request (ADR-010 §4);
	//   - it is the correlation key that lets the daemon fold successive records
	//     of ONE item under one item_id — the agent_message increments of
	//     IS-DELTA-1 and the tool_run open+close IS-DELTA-3 collapses. The
	//     adapter is the only party that sees the CLI's own id, so nobody else
	//     can supply it.
	//
	// Empty for a self-contained one-record item.
	Ref string

	// ClientRef is the id THE CLIENT supplied when it originated this interaction, echoed
	// back by the CLI verbatim -- Codex's `userMessage.clientId`, which the daemon set as
	// `clientUserMessageId` on turn/start or turn/steer.
	//
	// It exists because it is the ONLY exact composer-echo correlation any provider offers.
	// The alternative -- matching the echoed prompt by TEXT -- has a probed mis-attribution
	// on record (internal/skeleton/chat.go's pendingSendTTL doc: an OWNER-typed "yes" was
	// stamped source=phone because a phone send of "yes" was still pending). An adapter
	// whose CLI carries no such id leaves this empty, and the daemon falls back to what it
	// already does.
	//
	// Like Ref, it is MACHINE-SIDE: the daemon consumes it and it never reaches the wire.
	ClientRef string

	// TurnRef is the CLI's OWN id for the TURN this interaction belongs to -- Codex's
	// `params.turnId`, a UUIDv7 the app-server mints.
	//
	// THE TURN RULE ITSELF IS STILL THE DAEMON'S. IS-ENV-1 says a turn opens on a
	// user_message and closes on a terminal agent_message, `turn_id` on the wire is the
	// daemon's own ULID, and none of that changes: this field sources NO boundary and
	// decides NO grouping. It is the third machine-side correlation key beside Ref and
	// ClientRef, and it exists for one reason -- a provider whose turn operations take the
	// CLI's own turn id as a PRECONDITION cannot be driven with an id the CLI never minted.
	//
	// PROBED (Wave R7 review BLOCKING 1, and confirmed against the real 0.147.0 server):
	// `turn/steer` documents expectedTurnId as "Required active turn id precondition. The
	// request fails when it does not match the currently active turn", and R7 round 1 sent
	// the daemon's 26-char ULID against the server's UUIDv7 -- so EVERY mid-turn phone send
	// was rejected, and `turn/interrupt` was worse, because the server's honest
	// `no active turn to interrupt` for an id it had never seen was swallowed as benign and
	// a Stop that stopped nothing reported success.
	//
	// Like Ref and ClientRef it is MACHINE-SIDE and never reaches the wire. An adapter whose
	// CLI has no turn identity leaves it empty, and the daemon's turn state is unaffected.
	TurnRef string

	// user_message (§3.1) / agent_message (§3.2). Text on an agent_message is the
	// INCREMENT this record appends, never the accumulated body (IS-DELTA-1).
	Text       string
	Source     string // user_message only: phone | owner | derived
	StopReason string // agent_message terminal record only: end_turn | interrupted | error

	// tool_run (§3.3). ExitCode is meaningful only for an execute action; the
	// daemon omits it otherwise, so a zero here is not "exited 0" for a read.
	Tool             string
	Action           ToolAction // §7; also the approval_request summary action
	OutputExcerpt    string
	TruncationMarker string // the CLI's own truncation text, VERBATIM (IS-TOOL-3)
	ExitCode         int

	// file_change (§3.4). IS-FC-1: only an APPLIED change is a file_change; a
	// proposed edit is an approval_request.
	Path        string
	Change      string // create | modify | delete | rename
	OldPath     string // rename only
	DiffExcerpt string // unified diff text; the producer normalizes (spike-SB: Claude's Edit body is old_string/new_string)
	Added       int
	Removed     int

	// approval_request (§3.5). content_hash, expires_at and the agent_instance
	// binding tuple are daemon-authoritative and deliberately absent here.
	Summary   string
	Decisions []DecisionChoice // the CLI's OWN decision vocabulary, not a normalized one
	// Mode declares the apply mechanism AT CAPTURE, because that is where the
	// spike-SC carve-out is decidable — the adapter can see the tool and its
	// input then, so the daemon knows before the phone renders whether the
	// request resolves natively (ADR-010 §4).
	Mode        string
	PromptLines []string // prompt_card only: the sanitized prompt region, as text

	// Keystrokes is the decision->keystroke map, keyed by DecisionChoice.ID.
	// Present IFF Mode == prompt_card, because Decision is never called on that
	// path, so the map must be produced at capture (ADR-010 §2/§4).
	//
	// ponytail: this is MACHINE-SIDE data with a deliberate ceiling — the daemon
	// holds it and SHALL NOT copy it onto the item. IS-APR-3 forbids the item
	// carrying it and IS-LIFE-6 forbids the phone authoring the keystroke, so a
	// wire field for it would only invite the implementation those rules exist to
	// prevent. It rides Interaction because Interaction is the only adapter->core
	// carrier there is; DecisionAction deliberately has no Keys field.
	Keystrokes map[string]string

	// plan_update (§3.7). IS-PLAN-1: latest-state, not incremental.
	Revision int
	Steps    []PlanStep
}

// ToolAction is interaction-schema.md §7's structured summary — what makes a card
// read "Read src/main.rs". IS-TOOL-1 puts its production machine-side, in the
// per-CLI adapter; IS-TOOL-2 says an unclassifiable call is "other", never
// guessed at.
type ToolAction struct {
	Type    string // read | edit | write | search | execute | fetch | other
	Path    string // read/edit/write
	Query   string // search
	Command string // execute
}

// DecisionChoice is one decision the CLI offers for a pending approval (§3.5).
// The ids are the CLI's own vocabulary (spike-SB captured Codex's accept /
// acceptWithExecpolicyAmendment / cancel), never a normalized set.
type DecisionChoice struct {
	ID    string
	Label string
	// Verdict is this decision's grant/refuse polarity: allow | deny | other.
	// REQUIRED on an approval_request (conformance obligation, owner ruling
	// 2026-08-07); Validate additionally rejects a value outside the three.
	//
	// It is the daemon's only source for §3.6's allowed/denied split. Nothing
	// downstream can derive it: the ids are the CLI's own by §3.5's design, and a
	// daemon classifying `cancel` as a refusal would be guessing at a vocabulary
	// it does not own — the posture IS-TOOL-2 forbids for the same reason. The
	// adapter knows it at capture, which is where Mode is decided too.
	//
	// ponytail: MACHINE-SIDE, like Keystrokes — it is never copied onto the item
	// and never reaches the phone. The card labels its buttons from
	// decisions[].label (IS-APR-3) and no phone surface switches on polarity, so a
	// wire field would be a second place for the two to disagree. A phone that
	// later needs it (styling a destructive button) is an additive §3.5 field and
	// a schema change, not an unused field shipped ahead of its consumer.
	Verdict string
}

// PlanStep is one step of a plan_update (§3.7).
type PlanStep struct {
	Text  string
	State string // pending | in_progress | completed | cancelled
}

// InteractionSource is the OPTIONAL extension a CLI-native adapter implements.
// It is discovered by TYPE ASSERTION (AsInteractionSource), never by a method on
// Adapter: the frozen method set gains nothing (ADR-010 Non-goals).
type InteractionSource interface {
	// Interactions maps ONE captured event body to zero or more items. PURE and
	// TOTAL, on the same terms as ExtractConversationID: never panics on a nil,
	// truncated, garbage, or unbounded body; deterministic; and it never returns
	// content it did not observe in p.Raw.
	Interactions(p HookPayload) []Interaction

	// Decision describes HOW to apply one offered choice to the pending approval
	// named by ref, as a descriptor the CORE executes — the adapter performs no I/O
	// (E9.2), exactly as Command/Resume return an argv core runs. ok == false means
	// this CLI has no native mechanism here and the daemon must use the prompt card.
	//
	// decisionID is a DecisionChoice.ID — the CLI's OWN vocabulary (§3.5), which is
	// what the adapter itself offered. It is NOT DecisionChoice.Verdict: the
	// normalized allow|deny|other bit is what the DAEMON classifies §3.6's
	// allowed/denied from, and an adapter switching on it here would be answering a
	// choice it never offered.
	Decision(ref, decisionID string) (DecisionAction, bool)
}

// DecisionAction is the core-executed effect: the body core writes back on the
// pending hook or JSON-RPC channel. The prompt-card path carries NO DecisionAction
// — spike S-C's carve-out is exactly the path on which Decision is never called, so
// its decision-to-keystroke map is produced at capture and held MACHINE-SIDE. It is
// never a field on the item and never reaches the phone (interaction-schema.md
// IS-APR-3 and IS-LIFE-6; ADR-009 (4)).
type DecisionAction struct {
	Reply json.RawMessage
}

// ApprovalApplier is the OPTIONAL interface an adapter implements when a pending
// approval can be answered by TYPING INTO THE CLI'S OWN DIALOG — the apply path
// mirror-program.md section 3 makes M1's primary one for Claude Code, after
// rejecting the held-hook alternative on co-presence grounds (a hook held
// undecided hides the terminal's own prompt).
//
// It is a PURE function of the rendered grid, which is what keeps it inside the
// T-5 boundary while being callable from the daemon layer, where the session
// tap's snapshots arrive.
//
// ABSENCE IS A SIGNAL, exactly as it is for InteractionSource (ADR-010 §5): an
// adapter that does not implement this has no keystroke answer for its approvals,
// which is the normal case rather than a defect — mirror-program.md's own table
// answers Codex by native RPC and opencode over HTTP, and neither should ever be
// typed at. The daemon refuses to apply rather than inventing a key.
type ApprovalApplier interface {
	// ApprovalKeys returns the keystrokes that answer the dialog CURRENTLY on
	// snap with the given verdict (VerdictAllow | VerdictDeny), to be written to
	// the session's PTY VERBATIM.
	//
	// action is the PENDING REQUEST'S own ToolAction.Type, as the adapter
	// classified it at capture, and the adapter must refuse a dialog that is not
	// THAT request's. Proving only that some answerable dialog is on screen is
	// not enough: a hook is fire-and-forget, so a dialog reaches the glass before
	// its own card exists, and an answer for the request the owner just closed at
	// the terminal would otherwise be typed into the one that replaced it. Which
	// screens belong to which action is per-CLI knowledge and therefore the
	// adapter's, exactly like the key map.
	//
	// ok is false for any grid the adapter cannot positively identify as a
	// dialog it has a RECORDED key map for, for a dialog raised by a different
	// action than the request's, and for a verdict that dialog has no key for.
	// All three are refusals and never guesses: a key returned for the wrong
	// screen is typed into whatever has focus, while a refusal only declines the
	// phone's tap and leaves the terminal's own dialog untouched.
	ApprovalKeys(snap *vt.Snap, verdict, action string) (keys string, ok bool)
}

// AsApprovalApplier reports whether a can answer its approvals by keystroke.
func AsApprovalApplier(a Adapter) (ApprovalApplier, bool) {
	ap, ok := a.(ApprovalApplier)
	return ap, ok
}

// TurnInterrupter is the OPTIONAL interface an adapter implements when its CLI's current
// turn can be interrupted by a RECORDED key sequence (Wave R6, Mirror M2.4's semantic Stop;
// ADR-017 T2/T6). It is pure data out -- the CORE does the writing, exactly as Command/
// Resume return an argv core runs -- and it is discovered by type assertion like every
// other extension here.
//
// ABSENCE IS A SIGNAL (ADR-010 §5): an adapter that implements nothing here is complete
// and fully supported; the daemon refuses turn_interrupt with interrupt_unsupported rather
// than guessing a keystroke into a CLI whose cancel key nobody recorded (IS-TOOL-2's
// never-guess posture one layer down), and the ADR-017 capability record derives its
// `interrupt` field from this same seam, so the phone's Stop affordance and the op it
// rides agree by construction.
type TurnInterrupter interface {
	// InterruptKeys is the CLI's OWN cancel sequence, written to the session's PTY
	// verbatim. It must be non-empty: a declared interrupt that types nothing is a Stop
	// button that does nothing.
	InterruptKeys() []byte
}

// AsTurnInterrupter reports whether a proves a semantic interrupt seam.
func AsTurnInterrupter(a Adapter) (TurnInterrupter, bool) {
	ti, ok := a.(TurnInterrupter)
	return ti, ok
}

// AsInteractionSource reports whether a implements the optional capture
// extension. ok == false is the GENERIC-FALLBACK SIGNAL (ADR-010 §5): the adapter
// is complete and fully supported, and the daemon derives items from the
// sanitized snapshot instead. Native capture is an upgrade, never a precondition.
func AsInteractionSource(a Adapter) (InteractionSource, bool) {
	src, ok := a.(InteractionSource)
	return src, ok
}

// Validate reports the first structural violation in one shaped item: an unknown
// kind, status or enum value, or a broken card/prompt-card pairing. It is PURE
// and checks SHAPE ONLY — interaction-schema.md §5's size caps, §2's ids and
// timestamps, and §3.5's hash/expiry are daemon-side and deliberately unchecked
// here (ADR-010 §3).
func (in Interaction) Validate() error {
	if err := oneOf("kind", in.Kind, false, KindUserMessage, KindAgentMessage, KindToolRun,
		KindFileChange, KindApprovalRequest, KindApprovalResolved, KindPlanUpdate, KindSessionStatus); err != nil {
		return err
	}
	if err := oneOf("status", in.Status, true, StatusInProgress, StatusCompleted, StatusFailed, StatusDeclined); err != nil {
		return err
	}
	if err := oneOf("source", in.Source, true, SourcePhone, SourceOwner, SourceDerived, SourceSynthetic); err != nil {
		return err
	}
	if err := oneOf("stop_reason", in.StopReason, true, "end_turn", "interrupted", "error"); err != nil {
		return err
	}
	if err := oneOf("change", in.Change, true, "create", "modify", "delete", "rename"); err != nil {
		return err
	}
	if in.OldPath != "" && in.Change != "rename" {
		return fmt.Errorf("old_path is set with change %q; interaction-schema.md §3.4 carries it on a rename only", in.Change)
	}
	if err := oneOf("action.type", in.Action.Type, true, "read", "edit", "write", "search", "execute", "fetch", "other"); err != nil {
		return err
	}
	if err := oneOf("mode", in.Mode, true, ModeCard, ModePromptCard); err != nil {
		return err
	}
	if in.Kind == KindApprovalRequest && len(in.Decisions) == 0 {
		return fmt.Errorf("approval_request carries no decisions; the card labels its buttons from decisions[].label (IS-APR-3), so a request with none renders an unactionable card — an adapter that cannot enumerate the CLI's decisions emits no approval at all")
	}
	for i, d := range in.Decisions {
		if d.ID == "" {
			return fmt.Errorf("decisions[%d] has an empty id; the card resolves a decision by id (interaction-schema.md §3.5)", i)
		}
		// SHAPE only: an absent verdict is not malformed, it is incomplete, and
		// completeness is conformance's (checkShapedItems). A value outside the
		// three IS malformed — the daemon switches on it to classify §3.6's
		// allowed/denied, so a fourth value would read as "not a denial".
		if err := oneOf(fmt.Sprintf("decisions[%d].verdict", i), d.Verdict, true,
			VerdictAllow, VerdictDeny, VerdictOther); err != nil {
			return err
		}
	}
	if len(in.Keystrokes) > 0 && in.Mode != ModePromptCard {
		return fmt.Errorf("keystrokes are held with mode %q; the decision->keystroke map exists only for %s (IS-APR-3, IS-LIFE-6)", in.Mode, ModePromptCard)
	}
	if len(in.PromptLines) > 0 && in.Mode != ModePromptCard {
		return fmt.Errorf("prompt_lines are set with mode %q; interaction-schema.md §3.5 carries them on %s only", in.Mode, ModePromptCard)
	}
	for i, s := range in.Steps {
		// A plan step's state is its OWN vocabulary (§3.7), not the item status
		// enum of §4 — the two overlap on two values and must not be conflated.
		if err := oneOf(fmt.Sprintf("steps[%d].state", i), s.State, true, "pending", "in_progress", "completed", "cancelled"); err != nil {
			return err
		}
	}
	return nil
}

// oneOf reports a violation when v is not one of want. An empty v is a violation
// unless optional.
func oneOf(field, v string, optional bool, want ...string) error {
	if v == "" {
		if optional {
			return nil
		}
		return fmt.Errorf("%s is empty; it is required (interaction-schema.md §2)", field)
	}
	for _, w := range want {
		if v == w {
			return nil
		}
	}
	return fmt.Errorf("%s %q is not one of %v (interaction-schema.md §3/§4)", field, v, want)
}
