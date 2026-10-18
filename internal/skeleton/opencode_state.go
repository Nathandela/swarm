package skeleton

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/adapter/opencode"
)

// OpenCodeState is a caller-serialized, bounded reducer for one native root.
// Busy state and input completion are separate. History imports never manufacture
// live completion, and a new feed epoch cannot heal a missing-history gap.
type OpenCodeState struct {
	root       string
	generation string
	epoch      uint64

	// desynced freezes live ingestion after a refusal that made the stream
	// position untrustworthy; BeginEpoch is the only way out.
	desynced bool
	// A status snapshot restores busy knowledge, not missing input history.
	statusUnknown string
	streamUnknown string

	busy      bool
	busyKnown bool
	retry     *opencode.SessionStatus

	sessionErr *OpenCodeError

	msgOrder []OpenCodeMessage
	msgIndex map[string]int

	inputs       map[string]*ocInputState
	inputFloorAt int64
	inputFloorID string

	uncertain []OpenCodeUncertain
	pending   []opencode.Part
}

// ocInputState is one requested input's tracked state: its text parts, the
// composed (bounded) text, and the terminal outcome.
type ocInputState struct {
	msgID      string
	at         int64
	synthetic  bool
	classified bool
	compaction bool
	live       bool
	text       string
	textTrunc  bool
	parts      []ocPartState
	partIDs    map[string]int
	agent      string
	providerID string
	modelID    string
	variant    string

	outcome    OpenCodeOutcome
	outcomeSrc string
	outcomeAt  int64
	errorName  string
}

type ocPartState struct {
	id        string
	text      string
	synthetic bool
	truncated bool
}

// Input custody is separate from the bounded informational cache.
const (
	maxOCMessages     = 64       // informational message list (FIFO)
	maxOCInputs       = 128      // requested-input records (authority)
	maxOCUncertain    = 32       // explicit unattributable terminal records
	maxOCInputParts   = 64       // text parts tracked per input
	maxOCPartText     = 16 << 10 // bytes of text kept per part
	maxOCInputText    = 64 << 10 // bytes of text kept per input
	maxOCPendingParts = 32       // parts buffered until their message is known
)

// OpenCodeDisposition says what Apply did with one event.
type OpenCodeDisposition string

const (
	// OCApplied: the event was consumed into state.
	OCApplied OpenCodeDisposition = "applied"
	// OCIgnoredForeign: a session this reducer does not own (children included).
	OCIgnoredForeign OpenCodeDisposition = "ignored-foreign"
	// OCIgnoredUntracked: well-formed but intentionally without tracked effect
	// (unknown event kinds, tool parts, parts of untracked messages).
	OCIgnoredUntracked OpenCodeDisposition = "ignored-untracked"
	// OCIgnoredUnqualified: recognized content that cannot be qualified (unknown
	// status subtype, compaction artifacts). It flags the epoch degraded.
	OCIgnoredUnqualified OpenCodeDisposition = "ignored-unqualified"
	// OCRefusedEpoch: the caller observed an older epoch.
	OCRefusedEpoch OpenCodeDisposition = "refused-epoch"
	// OCRefusedDegraded: a prior refusal desynchronized the epoch; only a new
	// epoch resumes.
	OCRefusedDegraded OpenCodeDisposition = "refused-degraded"
	// OCRefusedParse: the event did not decode; the epoch is desynchronized.
	OCRefusedParse OpenCodeDisposition = "refused-parse"
	// OCRefusedCap: a bounded capacity refused the event.
	OCRefusedCap OpenCodeDisposition = "refused-cap"
)

// OpenCodeApply is Apply's result for one event.
type OpenCodeApply struct {
	Disposition OpenCodeDisposition
	Kind        string
	EventID     string
}

// OpenCodeOutcome is a requested input's terminal execution outcome. It says
// what the native runtime did, not whether the work itself succeeded.
type OpenCodeOutcome string

const (
	OpenCodeOutcomeNone      OpenCodeOutcome = ""
	OpenCodeOutcomeCompleted OpenCodeOutcome = "completed"
	OpenCodeOutcomeFailed    OpenCodeOutcome = "failed"
	OpenCodeOutcomeCancelled OpenCodeOutcome = "cancelled"
	OpenCodeOutcomeDeclined  OpenCodeOutcome = "declined"
)

// Outcome sources.
const (
	OpenCodeOutcomeSourceLive       = "live"
	OpenCodeOutcomeSourceImported   = "imported"
	OpenCodeOutcomeSourceReconciled = "reconciled"
)

// OpenCodeError is a root session error kept for display.
type OpenCodeError struct {
	Name    string
	Message string
}

// OpenCodeInput is one requested user input and its terminal outcome.
type OpenCodeInput struct {
	MessageID     string
	Text          string
	TextTruncated bool
	// Synthetic: the message consisted only of synthetic/ignored text parts --
	// an auto-continue re-prompt, not owner text. It is recorded (and its own
	// terminal outcome is), but it is never the owner's current input and its
	// outcome never completes a requested input.
	Synthetic     bool
	Classified    bool
	At            int64
	Outcome       OpenCodeOutcome
	OutcomeSource string
	OutcomeAt     int64
	ErrorName     string
	Agent         string
	ProviderID    string
	ModelID       string
	Variant       string
}

// OpenCodeMessage is one observed native message (informational).
type OpenCodeMessage struct {
	ParentID  string
	ID        string
	Role      string
	At        int64
	Finish    string
	ErrorName string
	// Summary marks both compaction summary carriers (user artifacts and
	// assistant continuation summaries).
	Summary bool
}

// OpenCodeUncertain is a terminal assistant outcome that cannot be attributed
// to a known requested input. It is explicit: it is recorded, never guessed
// into an input.
type OpenCodeUncertain struct {
	createdAt int64
	Source    string
	MessageID string
	InputID   string
	Outcome   OpenCodeOutcome
	ErrorName string
	At        int64
	Reason    string
}

// OpenCodeView is a deterministic snapshot of the reducer. Slices and pointers
// are copies: mutating a view never mutates the state.
type OpenCodeView struct {
	Root       string
	Generation string
	Epoch      uint64

	// Status and missing-part holds can recover; stream gaps survive new epochs.
	Degraded       bool
	DegradedReason string

	Busy      bool
	BusyKnown bool
	Retry     *opencode.SessionStatus

	Error *OpenCodeError

	// Current is the newest genuine (non-synthetic) input with no outcome yet.
	Current   *OpenCodeInput
	Inputs    []OpenCodeInput
	Messages  []OpenCodeMessage
	Uncertain []OpenCodeUncertain
}

// NewOpenCodeState builds the reducer for one root session. The root id is the
// only identity; every event naming a different session is foreign.
func NewOpenCodeState(root, generation string) (*OpenCodeState, error) {
	if root == "" {
		return nil, fmt.Errorf("skeleton: OpenCode state requires a root session id")
	}
	return &OpenCodeState{
		root:       root,
		generation: generation,
		msgIndex:   map[string]int{},
		inputs:     map[string]*ocInputState{},
	}, nil
}

// BeginEpoch opens a new observation epoch and returns it. Apply and
// ReconcileRootStatus accept only the current epoch, so stale observers cannot
// interleave a resync. A new epoch withdraws status proof and retains prior history gaps.
func (s *OpenCodeState) BeginEpoch() uint64 {
	s.epoch++
	s.desynced = false
	s.setBusy(false, false, nil)
	if s.epoch > 1 {
		s.statusUnknown = "native status must be reconciled after reconnect"
	}
	clear(s.pending)
	s.pending = nil
	return s.epoch
}

// Apply consumes one raw legacy event JSON document from the given epoch.
func (s *OpenCodeState) Apply(raw []byte, epoch uint64) OpenCodeApply {
	if epoch != s.epoch {
		return OpenCodeApply{Disposition: OCRefusedEpoch}
	}
	if s.desynced {
		return OpenCodeApply{Disposition: OCRefusedDegraded}
	}
	ev, err := opencode.DecodeEvent(raw)
	if err != nil {
		// The stream position is no longer trustworthy: the refused event could
		// have been a terminal one, so later deltas must not be correlated
		// against a view with a hole in it.
		s.desynced = true
		s.degradeStream("unparseable native event; live observation is desynchronized until a new epoch")
		return OpenCodeApply{Disposition: OCRefusedParse}
	}
	apply := OpenCodeApply{Kind: ev.Kind, EventID: ev.ID}
	switch ev.Kind {
	case opencode.EventSessionStatus:
		if f := ev.SessionStatus; f == nil || !s.mine(f.SessionID) {
			apply.Disposition = OCIgnoredForeign
		} else {
			apply.Disposition = s.applyStatus(&f.Status)
		}
	case opencode.EventSessionIdle:
		if f := ev.SessionIdle; f == nil || !s.mine(f.SessionID) {
			apply.Disposition = OCIgnoredForeign
		} else {
			s.setBusy(false, true, nil)
			s.statusUnknown = ""
			apply.Disposition = OCApplied
		}
	case opencode.EventSessionError:
		if f := ev.SessionError; f == nil || !s.mine(f.SessionID) {
			apply.Disposition = OCIgnoredForeign
		} else {
			s.sessionErr = &OpenCodeError{Name: f.Error.Name, Message: f.Error.Data.Message}
			apply.Disposition = OCApplied
		}
	case opencode.EventMessageUpdated:
		if f := ev.MessageUpdated; f == nil || !s.mine(f.SessionID) {
			apply.Disposition = OCIgnoredForeign
		} else {
			apply.Disposition = s.applyMessage(f.Info, OpenCodeOutcomeSourceLive)
		}
	case opencode.EventMessagePartUpdated:
		if f := ev.MessagePartUpdated; f == nil || !s.mine(f.SessionID) {
			apply.Disposition = OCIgnoredForeign
		} else {
			apply.Disposition = s.applyPart(f.Part)
		}
	default:
		apply.Disposition = OCIgnoredUntracked
	}
	return apply
}

// ReconcileRootStatus applies a successful, correctly scoped
// GET /session/status snapshot. A missing root key means idle -- only here,
// because only here is a successful read on the record.
func (s *OpenCodeState) ReconcileRootStatus(epoch uint64, snap map[string]opencode.SessionStatus) error {
	if epoch != s.epoch {
		return fmt.Errorf("skeleton: OpenCode status snapshot for stale epoch %d (current %d)", epoch, s.epoch)
	}
	if snap == nil {
		s.MarkRootStatusUnknown("status snapshot was absent")
		return fmt.Errorf("skeleton: OpenCode status snapshot is nil")
	}
	st, ok := snap[s.root]
	if !ok {
		st.Type = "idle"
	}
	s.applyStatus(&st)
	return nil
}

// MarkRootStatusUnknown records that the root's status could not be read. It
// never claims idle: a failed read proves nothing, and a later successful
// ReconcileRootStatus (or a live status event) restores confidence.
func (s *OpenCodeState) MarkRootStatusUnknown(reason string) {
	s.setBusy(false, false, nil)
	s.statusUnknown = reason
}

// Degrade retains a history gap independently of current busy knowledge.
func (s *OpenCodeState) Degrade(reason string) {
	s.degradeStream(reason)
}

// View returns a copy of the current facts.
func (s *OpenCodeState) View() OpenCodeView {
	v := OpenCodeView{
		Root:       s.root,
		Generation: s.generation,
		Epoch:      s.epoch,
		Busy:       s.busy,
		BusyKnown:  s.busyKnown,
		Retry:      cloneStatus(s.retry),
	}
	v.Degraded = s.streamUnknown != "" || s.statusUnknown != ""
	if s.streamUnknown != "" {
		v.DegradedReason = s.streamUnknown
	} else {
		v.DegradedReason = s.statusUnknown
	}
	if s.sessionErr != nil {
		e := *s.sessionErr
		v.Error = &e
	}
	for _, in := range s.inputs {
		if !in.classified && !v.Degraded {
			v.Degraded = true
			v.DegradedReason = "native input parts have not been observed"
		}
		if in.compaction {
			continue
		}
		v.Inputs = append(v.Inputs, in.view())
	}
	for i := range v.Inputs {
		in := v.Inputs[i]
		if in.Classified && !in.Synthetic && in.Outcome == OpenCodeOutcomeNone && (v.Current == nil || ocAfter(in.At, in.MessageID, v.Current.At, v.Current.MessageID)) {
			current := in
			v.Current = &current
		}
	}
	for _, u := range s.uncertain {
		if u.Source != OpenCodeOutcomeSourceImported && !v.Degraded {
			v.Degraded = true
			v.DegradedReason = "native terminal outcome lacks its requested input"
		}
	}
	sort.SliceStable(v.Inputs, func(i, j int) bool {
		return ocAfter(v.Inputs[j].At, v.Inputs[j].MessageID, v.Inputs[i].At, v.Inputs[i].MessageID)
	})
	v.Messages = append(v.Messages, s.msgOrder...)
	v.Uncertain = append(v.Uncertain, s.uncertain...)
	return v
}

//// internal ////

func (s *OpenCodeState) mine(sessionID string) bool { return sessionID == s.root }

func (s *OpenCodeState) setBusy(busy, known bool, retry *opencode.SessionStatus) {
	s.busy = busy
	s.busyKnown = known
	s.retry = retry
}

func (s *OpenCodeState) degradeStream(reason string) {
	if s.streamUnknown == "" {
		s.streamUnknown = reason
	}
}

// applyStatus folds one live root status. A subtype this build does not know
// is explicit unqualified state, not a guess: busy becomes unknown and the
// epoch is degraded until an authoritative observation arrives.
func (s *OpenCodeState) applyStatus(st *opencode.SessionStatus) OpenCodeDisposition {
	switch st.Type {
	case "busy":
		s.setBusy(true, true, nil)
		s.statusUnknown = ""
		return OCApplied
	case "idle":
		s.setBusy(false, true, nil)
		s.statusUnknown = ""
		return OCApplied
	case "retry":
		s.setBusy(true, true, cloneStatus(st))
		s.statusUnknown = ""
		return OCApplied
	default:
		s.setBusy(false, false, nil)
		s.statusUnknown = fmt.Sprintf("unknown root session status subtype %q; busy state is unqualified", st.Type)
		return OCIgnoredUnqualified
	}
}

// applyMessage binds native identities; parts qualify whether a user row is requested input.
func (s *OpenCodeState) applyMessage(m opencode.Message, source string) OpenCodeDisposition {
	if m.SessionID != s.root {
		return OCIgnoredForeign
	}
	if i, ok := s.msgIndex[m.ID]; ok {
		old := s.msgOrder[i]
		if old.Role != m.Role || old.At != m.Time.Created || old.ParentID != m.ParentID {
			s.degradeStream("native message identity changed")
			return OCIgnoredUnqualified
		}
	}
	if m.Role == "user" {
		if in := s.inputs[m.ID]; in != nil {
			if in.at != m.Time.Created {
				s.degradeStream("native input identity changed")
				return OCIgnoredUnqualified
			}
		} else if !ocAfter(m.Time.Created, m.ID, s.inputFloorAt, s.inputFloorID) {
			return OCIgnoredUntracked // retired history cannot reopen an input
		}
		in, ok := s.ensureInput(m.ID, m.Time.Created)
		if !ok {
			return OCRefusedCap
		}
		if m.Agent != "" && m.Model != nil {
			if in.agent != "" && (in.agent != m.Agent || in.providerID != m.Model.ProviderID || in.modelID != m.Model.ModelID || in.variant != m.Model.Variant) {
				s.degradeStream("native input selection changed")
				return OCIgnoredUnqualified
			}
			in.agent, in.providerID, in.modelID, in.variant = m.Agent, m.Model.ProviderID, m.Model.ModelID, m.Model.Variant
		}
		if source == OpenCodeOutcomeSourceLive && in.outcome == OpenCodeOutcomeNone {
			in.live = true
		}
		s.resolveUncertain(in)
	} else if s.inputs[m.ID] != nil {
		s.degradeStream("native input changed role")
		return OCIgnoredUnqualified
	}
	s.recordMessage(m)
	s.drainPending(m)
	if m.Role == "user" {
		return OCApplied
	}
	if m.IsAssistantSummary() {
		if source != OpenCodeOutcomeSourceImported {
			s.degradeStream("native compaction continuation is not a requested-input outcome")
		}
		return OCIgnoredUnqualified
	}
	return s.applyAssistantOutcome(m, source)
}

// ImportHistory adds bounded native rows without turning old completions into live events.
// It does not heal a stream gap: missing rows or outstanding inputs still need reconciliation.
func (s *OpenCodeState) ImportHistory(rows []json.RawMessage, epoch uint64) error {
	if epoch != s.epoch {
		return fmt.Errorf("skeleton: stale OpenCode history epoch")
	}
	if rows == nil || len(rows) > 200 {
		s.degradeStream("invalid native history page")
		return fmt.Errorf("skeleton: invalid native history page")
	}
	type row struct {
		message opencode.Message
		parts   []opencode.Part
	}
	parsed := make([]row, 0, len(rows))
	total := 0
	for _, raw := range rows {
		total += len(raw)
		if total > 8<<20 {
			s.degradeStream("native history byte limit reached")
			return fmt.Errorf("skeleton: native history byte limit")
		}
		m, parts, err := opencode.DecodeMessageRow(raw)
		if err != nil || m.SessionID != s.root {
			s.degradeStream("native history could not be verified")
			return fmt.Errorf("skeleton: invalid native history scope or row")
		}
		parsed = append(parsed, row{m, parts})
	}
	sort.SliceStable(parsed, func(i, j int) bool {
		return ocAfter(parsed[j].message.Time.Created, parsed[j].message.ID, parsed[i].message.Time.Created, parsed[i].message.ID)
	})
	for _, r := range parsed {
		if got := s.applyMessage(r.message, OpenCodeOutcomeSourceImported); got == OCRefusedCap {
			return fmt.Errorf("skeleton: native history exceeds input capacity")
		}
		for _, p := range r.parts {
			s.applyPart(p)
		}
	}
	return nil
}

func ocAfter(at int64, id string, otherAt int64, otherID string) bool {
	return at > otherAt || at == otherAt && id > otherID
}

// applyAssistantOutcome correlates a terminal assistant message with the
// requested input it answers (parentID). Intermediate steps (finish
// tool-calls, no completion metadata, no error) are progress only, and an
// error once recorded is never erased by later completion metadata.
func (s *OpenCodeState) applyAssistantOutcome(m opencode.Message, source string) OpenCodeDisposition {
	outcome, errName, terminal := ocTerminalOutcome(m)
	if !terminal {
		if m.Time.Completed > 0 && m.Finish != "tool-calls" && source != OpenCodeOutcomeSourceImported {
			s.degradeStream("completed native message has no qualified terminal reason")
			return OCIgnoredUnqualified
		}
		return OCApplied
	}
	at := m.Time.Completed
	if at == 0 {
		at = m.Time.Created
	}
	in := s.inputs[m.ParentID]
	if in == nil {
		// The requested input is unknown (never observed, evicted, or refused at
		// capacity). The terminal fact is explicit uncertainty rather than a
		// fabricated outcome.
		s.recordUncertain(OpenCodeUncertain{
			createdAt: m.Time.Created, Source: source,
			MessageID: m.ID,
			InputID:   m.ParentID,
			Outcome:   outcome,
			ErrorName: errName,
			At:        at,
			Reason:    "terminal assistant message has no known requested input",
		})
		return OCApplied
	}
	if in.compaction {
		return OCIgnoredUnqualified
	}
	if m.Time.Created < in.at {
		s.degradeStream("assistant predates its requested input")
		return OCIgnoredUnqualified
	}
	if source == OpenCodeOutcomeSourceImported && in.live {
		source = OpenCodeOutcomeSourceReconciled
	}
	in.recordOutcome(outcome, errName, at, source)
	if in.synthetic && source != OpenCodeOutcomeSourceImported {
		// The assistant answered a synthetic auto-continue prompt. Recording it
		// on that record is exact; finishing the requested input it may continue
		// would be a guess, so the epoch says so instead.
		s.degradeStream("auto-continue outcome is not attributable to a requested input")
	}
	return OCApplied
}

// recordOutcome applies the rank rule: a native error outranks a completion
// (completion metadata never erases an error, in either order); between
// equal-rank terminals the later one wins by its terminal time.
func (in *ocInputState) recordOutcome(outcome OpenCodeOutcome, errName string, at int64, source string) {
	if in.outcome != OpenCodeOutcomeNone {
		newRank, oldRank := ocOutcomeRank(outcome), ocOutcomeRank(in.outcome)
		if newRank < oldRank || (newRank == oldRank && at <= in.outcomeAt) {
			return
		}
	}
	in.outcome = outcome
	in.outcomeSrc = source
	in.outcomeAt = at
	in.errorName = errName
}

func ocOutcomeRank(o OpenCodeOutcome) int {
	switch o {
	case OpenCodeOutcomeNone:
		return -1
	case OpenCodeOutcomeCompleted:
		return 0
	default:
		return 1
	}
}

// ocTerminalOutcome classifies a message's terminal outcome. Terminal means
// native completion metadata or an error; a tool-calls step is never terminal,
// and an error is terminal (and classified) regardless of finish metadata.
func ocTerminalOutcome(m opencode.Message) (OpenCodeOutcome, string, bool) {
	if m.Error == nil && m.Time.Completed <= 0 {
		return OpenCodeOutcomeNone, "", false
	}
	if m.Error != nil {
		switch m.Error.Name {
		case "MessageAbortedError":
			return OpenCodeOutcomeCancelled, m.Error.Name, true
		case "ContentFilterError":
			return OpenCodeOutcomeDeclined, m.Error.Name, true
		default:
			return OpenCodeOutcomeFailed, m.Error.Name, true
		}
	}
	if m.Finish == "tool-calls" {
		return OpenCodeOutcomeNone, "", false
	}
	if m.Finish == "content-filter" {
		return OpenCodeOutcomeDeclined, "", true
	}
	if m.Finish == "stop" || m.Finish == "length" {
		return OpenCodeOutcomeCompleted, "", true
	}
	return OpenCodeOutcomeNone, "", false
}

// applyPart folds one live part. Parts naming a message this reducer has not
// seen are buffered (stream order is not guaranteed); everything else is
// folded through applyPartFact.
func (s *OpenCodeState) applyPart(p opencode.Part) OpenCodeDisposition {
	if p.SessionID != s.root {
		return OCIgnoredForeign
	}
	if p.Type != "text" && p.Type != "compaction" && p.Type != "file" {
		return OCIgnoredUntracked
	}
	if s.inputs[p.MessageID] != nil {
		return s.applyPartFact(p)
	}
	if _, known := s.msgIndex[p.MessageID]; known {
		return OCIgnoredUntracked
	}
	// Pending classification needs no raw tool payload; retain only bounded text and identity.
	p.Raw = nil
	p.State = nil
	p.Tool = ""
	p.CallID = ""
	p.Text = clampBytes(p.Text, maxOCPartText+utf8.UTFMax)
	for i, old := range s.pending {
		if old.MessageID == p.MessageID && old.ID == p.ID {
			if len(old.Text) > len(p.Text) && strings.HasPrefix(old.Text, p.Text) {
				return OCApplied
			}
			s.pending[i] = p
			return OCApplied
		}
	}
	if len(s.pending) >= maxOCPendingParts {
		s.degradeStream("pending part buffer exceeded capacity")
		return OCRefusedCap
	}
	s.pending = append(s.pending, p)
	return OCApplied
}

// applyPartFact classifies user input independently of the informational message cache.
func (s *OpenCodeState) applyPartFact(p opencode.Part) OpenCodeDisposition {
	in := s.inputs[p.MessageID]
	if in == nil || in.compaction {
		return OCIgnoredUntracked
	}
	if p.Type == "compaction" {
		in.compaction = true
		in.classified = true
		in.synthetic = true
		if i, ok := s.msgIndex[p.MessageID]; ok {
			s.msgOrder[i].Summary = true
		}
		if in.live {
			s.degradeStream("native compaction requires continuation reconciliation")
		}
		return OCIgnoredUnqualified
	}
	if p.Type != "text" && p.Type != "file" {
		return OCIgnoredUntracked
	}
	synthetic := p.Synthetic || p.Ignored
	// Retain owner text ahead of injected context; only genuine text can prove a send's body.
	if !synthetic {
		for i := range in.parts {
			if in.parts[i].synthetic {
				in.parts[i].text = ""
				in.parts[i].truncated = false
			}
		}
	} else if in.classified && !in.synthetic {
		p.Text = ""
	}
	budget := maxOCInputText
	for _, old := range in.parts {
		if old.id != p.ID {
			budget -= len(old.text)
		}
	}
	text := clampBytes(p.Text, min(maxOCPartText, budget))
	if idx, ok := in.partIDs[p.ID]; ok {
		old := in.parts[idx]
		if len(old.text) > len(text) && strings.HasPrefix(old.text, text) {
			return OCApplied
		}
		if old.text != text && !strings.HasPrefix(text, old.text) {
			s.degradeStream("native input part changed content")
			return OCIgnoredUnqualified
		}
		in.parts[idx] = ocPartState{id: p.ID, text: text, synthetic: synthetic, truncated: len(p.Text) > len(text)}
	} else {
		if len(in.parts) >= maxOCInputParts {
			s.degradeStream("input text part capacity reached")
			return OCRefusedCap
		}
		in.partIDs[p.ID] = len(in.parts)
		in.parts = append(in.parts, ocPartState{id: p.ID, text: text, synthetic: synthetic, truncated: len(p.Text) > len(text)})
	}
	in.classified = true
	in.recompute()
	if in.synthetic && in.outcome != OpenCodeOutcomeNone && in.live {
		s.degradeStream("auto-continue outcome is not attributable to a requested input")
	}
	return OCApplied
}

// recompute composes the input's text from its parts (streaming updates
// replace a part in place) under the total byte cap, and reclassifies the
// input as synthetic only when it has synthetic/ignored parts and NO genuine
// text: an owner message with injected context parts stays genuine.
func (in *ocInputState) recompute() {
	synthetic, genuine := false, false
	truncated := false
	var b []byte
	for _, p := range in.parts {
		if p.synthetic {
			synthetic = true
		} else {
			genuine = true
		}
	}
	for _, p := range in.parts {
		if genuine && p.synthetic {
			continue
		}
		if p.truncated {
			truncated = true
		}
		if len(b) >= maxOCInputText {
			if len(p.text) > 0 {
				truncated = true
			}
			continue
		}
		t := p.text
		if len(b)+len(t) > maxOCInputText {
			t = clampBytes(t, maxOCInputText-len(b))
			truncated = true
		}
		b = append(b, t...)
	}
	in.text = string(b)
	in.textTrunc = truncated
	in.synthetic = synthetic && !genuine
}

// drainPending folds the buffered parts of a now-known message, in their
// original arrival order, and drops them from the buffer.
func (s *OpenCodeState) drainPending(m opencode.Message) {
	if len(s.pending) == 0 {
		return
	}
	kept := s.pending[:0]
	for _, p := range s.pending {
		if p.MessageID == m.ID {
			s.applyPartFact(p)
			continue
		}
		kept = append(kept, p)
	}
	clear(s.pending[len(kept):])
	s.pending = kept
}

// recordMessage inserts or updates the informational message list (FIFO
// bounded). Losing an entry here never loses authority: inputs and uncertainty
// live outside this list.
func (s *OpenCodeState) recordMessage(m opencode.Message) {
	rec := OpenCodeMessage{
		ID:       m.ID,
		ParentID: m.ParentID,
		Role:     m.Role,
		At:       m.Time.Created,
		Finish:   m.Finish,
		Summary:  m.IsAssistantSummary(),
	}
	if in := s.inputs[m.ID]; in != nil && in.compaction {
		rec.Summary = true
	}
	if m.Error != nil {
		rec.ErrorName = m.Error.Name
	}
	if i, ok := s.msgIndex[m.ID]; ok {
		s.msgOrder[i] = rec
		return
	}
	if len(s.msgOrder) >= maxOCMessages {
		delete(s.msgIndex, s.msgOrder[0].ID)
		copy(s.msgOrder, s.msgOrder[1:])
		s.msgOrder = s.msgOrder[:len(s.msgOrder)-1]
		for i := range s.msgOrder {
			s.msgIndex[s.msgOrder[i].ID] = i
		}
	}
	s.msgIndex[m.ID] = len(s.msgOrder)
	s.msgOrder = append(s.msgOrder, rec)
}

// ensureInput reserves correlation before parts arrive; open inputs are never evicted.
func (s *OpenCodeState) ensureInput(msgID string, at int64) (*ocInputState, bool) {
	if in, ok := s.inputs[msgID]; ok {
		return in, true
	}
	if len(s.inputs) >= maxOCInputs && !s.evictOldestTerminalInput() {
		s.degradeStream("requested input capacity reached with no terminal input to evict; a requested input was not tracked")
		return nil, false
	}
	in := &ocInputState{msgID: msgID, at: at, partIDs: map[string]int{}}
	s.inputs[msgID] = in
	return in, true
}

func (s *OpenCodeState) evictOldestTerminalInput() bool {
	var oldest *ocInputState
	for _, in := range s.inputs {
		if !in.compaction && (!in.classified || in.outcome == OpenCodeOutcomeNone) {
			continue
		}
		if oldest == nil || ocAfter(oldest.at, oldest.msgID, in.at, in.msgID) {
			oldest = in
		}
	}
	if oldest == nil {
		return false
	}
	if ocAfter(oldest.at, oldest.msgID, s.inputFloorAt, s.inputFloorID) {
		s.inputFloorAt = oldest.at
		s.inputFloorID = oldest.msgID
	}
	delete(s.inputs, oldest.msgID)
	return true
}

// recordUncertain appends an explicit uncertainty record, dropping the oldest
// at capacity -- with the drop itself recorded as degradation, so an
// unqualified terminal fact never vanishes silently.
func (s *OpenCodeState) recordUncertain(u OpenCodeUncertain) {
	for i, old := range s.uncertain {
		if old.MessageID == u.MessageID {
			if old.InputID != u.InputID {
				s.degradeStream("native terminal message changed parent")
				return
			}
			if ocOutcomeRank(u.Outcome) > ocOutcomeRank(old.Outcome) || ocOutcomeRank(u.Outcome) == ocOutcomeRank(old.Outcome) && u.At > old.At {
				s.uncertain[i] = u
			}
			return
		}
	}
	if len(s.uncertain) >= maxOCUncertain {
		drop := -1
		for i, old := range s.uncertain {
			if old.Source == OpenCodeOutcomeSourceImported {
				drop = i
				break
			}
		}
		if drop < 0 {
			if u.Source == OpenCodeOutcomeSourceImported {
				return
			}
			drop = 0
			s.degradeStream("uncertain live outcome records exceeded capacity")
		}
		copy(s.uncertain[drop:], s.uncertain[drop+1:])
		s.uncertain = s.uncertain[:len(s.uncertain)-1]
	}
	s.uncertain = append(s.uncertain, u)
}

func (s *OpenCodeState) resolveUncertain(in *ocInputState) {
	kept := s.uncertain[:0]
	for _, u := range s.uncertain {
		if u.InputID != in.msgID {
			kept = append(kept, u)
			continue
		}
		if u.createdAt < in.at {
			s.degradeStream("assistant predates its requested input")
			continue
		}
		source := u.Source
		if source == OpenCodeOutcomeSourceImported && in.live {
			source = OpenCodeOutcomeSourceReconciled
		}
		in.recordOutcome(u.Outcome, u.ErrorName, u.At, source)
	}
	clear(s.uncertain[len(kept):])
	s.uncertain = kept
}

func (in *ocInputState) view() OpenCodeInput {
	v := OpenCodeInput{MessageID: in.msgID, Text: in.text, TextTruncated: in.textTrunc, Synthetic: in.synthetic, Classified: in.classified, At: in.at, Agent: in.agent, ProviderID: in.providerID, ModelID: in.modelID, Variant: in.variant}
	if in.classified && !in.compaction {
		v.Outcome = in.outcome
		v.OutcomeSource = in.outcomeSrc
		v.OutcomeAt = in.outcomeAt
		v.ErrorName = in.errorName
	}
	return v
}

// cloneStatus deep-copies a status so neither the reducer nor a view ever
// aliases caller-visible memory.
func cloneStatus(st *opencode.SessionStatus) *opencode.SessionStatus {
	if st == nil {
		return nil
	}
	c := *st
	if st.Action != nil {
		a := *st.Action
		c.Action = &a
	}
	return &c
}
