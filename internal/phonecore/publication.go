package phonecore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

const (
	// MaxPendingPublications bounds unresolved publication work. Settled/unknown results are
	// retained in a separate equally-bounded projection so they can never permanently consume
	// the send queue while still surviving one Android recreation.
	MaxPendingPublications = 64
	maxPublicationResults  = 64

	// PublicationOutcomeTimeout ends indefinite "sending" without ever authorizing a resend.
	// A late authenticated outcome/echo still matches and may refine/remove the retained result.
	PublicationOutcomeTimeout = 15 * time.Minute
)

var (
	ErrPublicationQueueFull = errors.New("phonecore: pending publication queue full")
	ErrPublicationConflict  = errors.New("phonecore: conflicting pending publication")
	ErrPublicationNotFound  = errors.New("phonecore: pending publication not found")
	ErrPublicationState     = errors.New("phonecore: invalid pending publication state")
)

type PublicationKind string

const (
	PublicationComposer PublicationKind = "composer_send"
	PublicationHistory  PublicationKind = "interaction_history"
	PublicationDetail   PublicationKind = "interaction_detail"
)

type PublicationPhase string

const (
	// PublicationPrepared is durable user intent which has not yet been assigned a mailbox
	// sequence. Keeping this phase is what closes the crash between a press and NextCommand.
	PublicationPrepared PublicationPhase = "prepared"
	// PublicationSealed owns the exact envelope and sequence which may have reached the relay.
	// It may only be retried byte-for-byte; resealing the same sequence is forbidden.
	PublicationSealed PublicationPhase = "sealed"
	// PublicationAdmitted means MailboxAppend returned success. The command may now await its
	// authenticated outcome without holding later publications behind it.
	PublicationAdmitted PublicationPhase = "admitted"
	// PublicationTerminal is retained as an honest local outcome but is never published.
	// Authority replacement uses it instead of silently re-targeting or deleting text.
	PublicationTerminal PublicationPhase = "terminal"
)

const (
	PublicationAuthorityChanged = "authority_changed"
	PublicationAccepted         = "accepted_waiting_echo"
	PublicationExpired          = "expired"
	PublicationOutcomeUnknown   = "outcome_unknown"
)

const (
	controlOpOK    = "ok"
	controlOpError = "error"
)

// PendingPublication is one crash-safe phone->machine publication. It is deliberately not a
// QueuedOp: that legacy scaffold is a signed-once offline command with a destructive Drain and
// no exact envelope, while this record is the state machine around one exact relay append.
//
// The record is content-tier material. Composer Text is user conversation content, while the
// exact envelope and signed command are authority-bearing bytes. State custody seals them and
// revoke destroys them with every other registration-bound content record.
type PendingPublication struct {
	LogicalID       string                        `json:"logical_id"`
	OperationID     string                        `json:"operation_id"`
	Kind            PublicationKind               `json:"kind"`
	SessionID       string                        `json:"session_id"`
	SessionInstance string                        `json:"session_instance,omitempty"`
	ExpectedTurn    string                        `json:"expected_turn,omitempty"`
	Text            string                        `json:"text,omitempty"`
	Machine         string                        `json:"machine"`
	EpochID         uint32                        `json:"epoch_id"`
	Target          string                        `json:"target"`
	AuthorityPub    []byte                        `json:"authority_pub"`
	Command         schema.DeviceCommandAuth      `json:"command"`
	Composer        *schema.ComposerSendReq       `json:"composer,omitempty"`
	History         *schema.InteractionHistoryReq `json:"history,omitempty"`
	Detail          *schema.InteractionDetailReq  `json:"detail,omitempty"`
	Phase           PublicationPhase              `json:"phase"`
	Sequence        uint64                        `json:"sequence,omitempty"`
	Envelope        []byte                        `json:"envelope,omitempty"`
	TerminalCode    string                        `json:"terminal_code,omitempty"`
	// ResultOrder is a durable monotonic recency ordinal for terminal results. It is
	// independent of the slice's logical FIFO position: a long-lived admitted head may settle
	// after newer sends and must not be evicted as though its old insertion position were age.
	// Zero is the migrated legacy value and sorts older than every authored ordinal.
	ResultOrder uint64    `json:"result_order,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

func (p PendingPublication) clone() PendingPublication {
	p.Envelope = slices.Clone(p.Envelope)
	p.AuthorityPub = slices.Clone(p.AuthorityPub)
	p.Command.ContentHash = slices.Clone(p.Command.ContentHash)
	if p.Composer != nil {
		body := *p.Composer
		p.Composer = &body
	}
	if p.History != nil {
		body := *p.History
		p.History = &body
	}
	if p.Detail != nil {
		body := *p.Detail
		p.Detail = &body
	}
	return p
}

func clonePendingPublications(in []PendingPublication) []PendingPublication {
	out := make([]PendingPublication, len(in))
	for i := range in {
		out[i] = in[i].clone()
	}
	return out
}

func validatePendingPublication(p PendingPublication) error {
	if p.LogicalID == "" || p.OperationID == "" || p.SessionID == "" || p.Machine == "" ||
		p.EpochID == 0 || p.Target == "" || len(p.AuthorityPub) == 0 || p.Command.OperationID != p.OperationID ||
		p.Command.Machine != p.Machine || p.Command.Session != p.SessionID || p.CreatedAt.IsZero() {
		return ErrPublicationState
	}
	bodies := 0
	if p.Composer != nil {
		bodies++
	}
	if p.History != nil {
		bodies++
	}
	if p.Detail != nil {
		bodies++
	}
	if bodies != 1 {
		return ErrPublicationState
	}
	switch p.Kind {
	case PublicationComposer:
		if p.Composer == nil || p.Command.Action != schema.ActionComposerSend || p.Text == "" ||
			p.SessionInstance == "" || p.Composer.Session != p.SessionID ||
			p.Composer.SessionInstance != p.SessionInstance || p.Composer.ExpectedTurn != p.ExpectedTurn ||
			p.Composer.Text != p.Text || p.Command.ExpiresAt.IsZero() {
			return ErrPublicationState
		}
	case PublicationHistory:
		if p.History == nil || p.Command.Action != schema.ActionInteractionHistory ||
			p.History.Session != p.SessionID {
			return ErrPublicationState
		}
	case PublicationDetail:
		if p.Detail == nil || p.Command.Action != schema.ActionInteractionDetail ||
			p.Detail.Session != p.SessionID {
			return ErrPublicationState
		}
	default:
		return ErrPublicationState
	}
	switch p.Phase {
	case PublicationPrepared:
		if p.Sequence != 0 || len(p.Envelope) != 0 || p.TerminalCode != "" || p.ResultOrder != 0 {
			return ErrPublicationState
		}
	case PublicationSealed, PublicationAdmitted:
		if p.Sequence == 0 || len(p.Envelope) == 0 || p.TerminalCode != "" || p.ResultOrder != 0 {
			return ErrPublicationState
		}
	case PublicationTerminal:
		if p.TerminalCode == "" {
			return ErrPublicationState
		}
	default:
		return ErrPublicationState
	}
	return nil
}

func validatePendingPublications(publications []PendingPublication) error {
	unresolved, results := 0, 0
	seenOperations := make(map[string]struct{}, len(publications))
	seenLogical := make(map[string]struct{}, len(publications))
	type sequenceCoordinate struct {
		epoch    uint32
		sequence uint64
	}
	seenSequences := make(map[sequenceCoordinate]string, len(publications))
	seenResultOrders := make(map[uint64]string, len(publications))
	for _, p := range publications {
		if err := validatePendingPublication(p); err != nil {
			return fmt.Errorf("%w: operation %q: %v", ErrPublicationState, p.OperationID, err)
		}
		if _, ok := seenOperations[p.OperationID]; ok {
			return fmt.Errorf("%w: duplicate operation %q", ErrPublicationConflict, p.OperationID)
		}
		if _, ok := seenLogical[p.LogicalID]; ok {
			return fmt.Errorf("%w: duplicate logical id %q", ErrPublicationConflict, p.LogicalID)
		}
		seenOperations[p.OperationID] = struct{}{}
		seenLogical[p.LogicalID] = struct{}{}
		if p.Sequence != 0 {
			coordinate := sequenceCoordinate{epoch: p.EpochID, sequence: p.Sequence}
			if operation, ok := seenSequences[coordinate]; ok {
				return fmt.Errorf("%w: operations %q and %q share epoch %d sequence %d",
					ErrPublicationConflict, operation, p.OperationID, p.EpochID, p.Sequence)
			}
			seenSequences[coordinate] = p.OperationID
		}
		if p.Phase == PublicationTerminal {
			results++
			if p.ResultOrder != 0 {
				if operation, ok := seenResultOrders[p.ResultOrder]; ok {
					return fmt.Errorf("%w: operations %q and %q share result order %d",
						ErrPublicationConflict, operation, p.OperationID, p.ResultOrder)
				}
				seenResultOrders[p.ResultOrder] = p.OperationID
			}
		} else {
			unresolved++
		}
	}
	if unresolved > MaxPendingPublications || results > maxPublicationResults {
		return ErrPublicationQueueFull
	}
	return nil
}

func validatePendingPublicationAuthority(publications []PendingPublication, authorityPub []byte) error {
	if err := validatePendingPublications(publications); err != nil {
		return err
	}
	for _, p := range publications {
		if p.Phase != PublicationTerminal && !bytes.Equal(p.AuthorityPub, authorityPub) {
			return fmt.Errorf("%w: operation %q belongs to another routing authority", ErrPublicationState, p.OperationID)
		}
	}
	return nil
}

func migratePendingPublicationAuthority(publications []PendingPublication, authorityPub []byte) []PendingPublication {
	out := clonePendingPublications(publications)
	for i := range out {
		if len(out[i].AuthorityPub) == 0 {
			out[i].AuthorityPub = slices.Clone(authorityPub)
		}
	}
	return out
}

func publicationsForIdentity(st *State, machine string, epoch uint32, authorityPub []byte) {
	for i := range st.PendingPublications {
		p := &st.PendingPublications[i]
		if p.Machine == machine && p.EpochID == epoch && bytes.Equal(p.AuthorityPub, authorityPub) {
			continue
		}
		var code string
		switch p.Phase {
		case PublicationPrepared:
			// No sequence or exact envelope existed, so the old authority received nothing.
			code = PublicationAuthorityChanged
		case PublicationSealed, PublicationAdmitted:
			// Exact bytes may already be at the relay or machine. Replacement ends redrive but
			// cannot truthfully invite a resend.
			code = PublicationOutcomeUnknown
		case PublicationTerminal:
			// A terminal machine/local verdict remains true after registration replacement.
			// In particular, accepted and outcome_unknown may not become “not sent”.
			continue
		default:
			continue
		}
		terminalizePublication(st, p, code)
		if st.OpOutcomes == nil {
			st.OpOutcomes = map[string]schema.Control{}
		}
		st.OpOutcomes[p.OperationID] = localPublicationOutcome(*p, code)
	}
	boundPublicationResults(st)
}

func publicationForReply(publications []PendingPublication, ctrl schema.Control) (PendingPublication, int, bool) {
	for i := range publications {
		if publications[i].OperationID == ctrl.OperationID {
			return publications[i], i, true
		}
	}
	return PendingPublication{}, -1, false
}

func replyMatchesPublication(p PendingPublication, ctrl schema.Control) bool {
	if ctrl.OperationID != p.OperationID {
		return false
	}
	if ctrl.Op == controlOpError || ctrl.ErrorCode != "" {
		// Error replies carry no session by protocol. The globally unique operation id is the
		// exact command binding; the reply must not carry a contradictory non-empty session.
		return len(ctrl.Journal) == 0 && (ctrl.SessionID == "" || ctrl.SessionID == p.SessionID)
	}
	var expected string
	switch p.Kind {
	case PublicationComposer:
		expected = controlOpOK
	case PublicationHistory:
		expected = schema.ActionInteractionHistory
	case PublicationDetail:
		expected = schema.ActionInteractionDetail
	default:
		return false
	}
	if ctrl.Op != expected || ctrl.SessionID != p.SessionID {
		return false
	}
	if p.Kind == PublicationComposer {
		// Composer admission carries only a verdict. Accepting an attached journal payload
		// would make an operation id a second transcript-authority channel.
		return len(ctrl.Journal) == 0
	}
	for _, rec := range ctrl.Journal {
		if rec.SessionID != p.SessionID {
			return false
		}
	}
	if p.Kind == PublicationDetail {
		if len(ctrl.Journal) != 1 || p.Detail == nil || ctrl.Journal[0].Type != RecordTypeInteraction {
			return false
		}
		var detail wireItem
		if json.Unmarshal(ctrl.Journal[0].Item, &detail) != nil || detail.ItemID != p.Detail.ItemID {
			return false
		}
	}
	return true
}

func removePublicationAt(publications []PendingPublication, index int) []PendingPublication {
	copy(publications[index:], publications[index+1:])
	publications[len(publications)-1] = PendingPublication{}
	return publications[:len(publications)-1]
}

func removePublicationResultAt(st *State, index int) {
	operationID := st.PendingPublications[index].OperationID
	st.PendingPublications = removePublicationAt(st.PendingPublications, index)
	delete(st.OpOutcomes, operationID)
}

func resultOrderOlder(left, right uint64) bool {
	if left == 0 {
		return right != 0
	}
	return right != 0 && left < right
}

func oldestPublicationResultIndex(publications []PendingPublication) int {
	oldest := -1
	for i := range publications {
		if publications[i].Phase != PublicationTerminal {
			continue
		}
		if oldest < 0 || resultOrderOlder(publications[i].ResultOrder, publications[oldest].ResultOrder) {
			oldest = i
		}
	}
	return oldest
}

func nextPublicationResultOrder(st *State) uint64 {
	var maximum uint64
	for _, p := range st.PendingPublications {
		if p.Phase == PublicationTerminal && p.ResultOrder > maximum {
			maximum = p.ResultOrder
		}
	}
	if maximum != ^uint64(0) {
		return maximum + 1
	}
	// The counter is ordering metadata, not a wire/replay coordinate. At exhaustion,
	// renumber the bounded terminal set in its existing recency order, preserving zero-valued
	// migrated rows as oldest and slice order only as the stable tie-breaker.
	indices := make([]int, 0, maxPublicationResults)
	for i := range st.PendingPublications {
		if st.PendingPublications[i].Phase == PublicationTerminal {
			indices = append(indices, i)
		}
	}
	sort.SliceStable(indices, func(i, j int) bool {
		return resultOrderOlder(st.PendingPublications[indices[i]].ResultOrder,
			st.PendingPublications[indices[j]].ResultOrder)
	})
	for i, index := range indices {
		st.PendingPublications[index].ResultOrder = uint64(i + 1)
	}
	return uint64(len(indices) + 1)
}

func terminalizePublication(st *State, p *PendingPublication, code string) {
	order := nextPublicationResultOrder(st)
	p.Phase, p.TerminalCode, p.ResultOrder = PublicationTerminal, code, order
}

func boundPublicationResults(st *State) {
	results := 0
	for _, p := range st.PendingPublications {
		if p.Phase == PublicationTerminal {
			results++
		}
	}
	for results > maxPublicationResults {
		if oldest := oldestPublicationResultIndex(st.PendingPublications); oldest >= 0 {
			removePublicationResultAt(st, oldest)
			results--
		}
	}
}

func localPublicationOutcome(p PendingPublication, code string) schema.Control {
	message := "Delivery could not be confirmed. Check the conversation before retrying."
	switch code {
	case PublicationExpired:
		message = "The request expired before it could be delivered."
	case PublicationAuthorityChanged:
		message = "The connected computer changed before this message could be delivered. Send it again if it is still relevant."
	}
	return schema.Control{
		Op: controlOpError, OperationID: p.OperationID, SessionID: p.SessionID,
		ErrorCode: schema.ErrorCode(code), Error: message,
	}
}

func recordLocalPublicationOutcome(st *State, p PendingPublication, code string) {
	if st.OpOutcomes == nil {
		st.OpOutcomes = map[string]schema.Control{}
	}
	if _, exists := st.OpOutcomes[p.OperationID]; exists {
		return
	}
	st.OpOutcomes[p.OperationID] = localPublicationOutcome(p, code)
}

func settleComposerItems(st *State) {
	for _, echoed := range st.Items {
		if echoed.Kind != KindUserMessage || echoed.Source != "phone" || echoed.OperationID == "" {
			continue
		}
		for i := range st.PendingPublications {
			p := st.PendingPublications[i]
			if p.Kind == PublicationComposer && p.OperationID == echoed.OperationID &&
				p.SessionID == echoed.SessionID && p.Text == echoed.Text {
				st.PendingPublications = removePublicationAt(st.PendingPublications, i)
				delete(st.OpOutcomes, p.OperationID)
				break
			}
		}
	}
}

// PreparePublication durably appends one unsequenced publication. Exact repetition is
// idempotent; the same operation id carrying different content is refused.
func (c *Core) PreparePublication(p PendingPublication) error {
	// Prepared is the only caller-authored state. Sequences, exact envelopes, admission and
	// terminal verdicts belong to the transition methods below; accepting them here would let a
	// caller bypass both uniqueness checks and the durable crash boundaries those methods own.
	if (p.Phase != "" && p.Phase != PublicationPrepared) || p.Sequence != 0 ||
		len(p.Envelope) != 0 || p.TerminalCode != "" {
		return ErrPublicationState
	}
	p.Phase = PublicationPrepared
	if err := validatePendingPublication(p); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if p.Machine != c.st.Machine || p.EpochID != c.st.EpochID ||
		!bytes.Equal(p.AuthorityPub, c.st.MachineRelayAuthPub) || c.st.Disowned {
		return ErrPublicationState
	}
	for _, current := range c.st.PendingPublications {
		if current.LogicalID == p.LogicalID && current.OperationID != p.OperationID {
			return ErrPublicationConflict
		}
		if current.OperationID != p.OperationID {
			continue
		}
		if reflect.DeepEqual(current, p) {
			return nil
		}
		return ErrPublicationConflict
	}
	unresolved := 0
	for _, current := range c.st.PendingPublications {
		if current.Phase != PublicationTerminal {
			unresolved++
		}
	}
	if unresolved >= MaxPendingPublications {
		return ErrPublicationQueueFull
	}
	st := c.st.clone()
	// A bounded terminal projection is history, not unresolved work. Drop its oldest row only
	// when accepting a newer intent; no prepared/sealed/admitted record is ever evicted.
	if len(st.PendingPublications) > 0 {
		results := 0
		for _, current := range st.PendingPublications {
			if current.Phase == PublicationTerminal {
				results++
			}
		}
		if results >= maxPublicationResults {
			if oldest := oldestPublicationResultIndex(st.PendingPublications); oldest >= 0 {
				removePublicationResultAt(&st, oldest)
			}
		}
	}
	st.PendingPublications = append(st.PendingPublications, p.clone())
	return c.persistLocked(st)
}

// PreparePublicationRetry replaces one authenticated input_busy result with a fresh exact
// attempt while retaining the logical send's FIFO position. input_busy is the only machine
// verdict which proves that the prior composer operation wrote no provider bytes; accepted,
// expired and delivery-unknown outcomes must never authorize this transition.
func (c *Core) PreparePublicationRetry(previousOperationID string, p PendingPublication) error {
	if previousOperationID == "" || previousOperationID == p.OperationID ||
		(p.Phase != "" && p.Phase != PublicationPrepared) || p.Sequence != 0 ||
		len(p.Envelope) != 0 || p.TerminalCode != "" {
		return ErrPublicationState
	}
	p.Phase = PublicationPrepared
	if err := validatePendingPublication(p); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if p.Machine != c.st.Machine || p.EpochID != c.st.EpochID ||
		!bytes.Equal(p.AuthorityPub, c.st.MachineRelayAuthPub) || c.st.Disowned {
		return ErrPublicationState
	}
	// The durable transition may complete before a facade callback is replayed. Recognize only
	// the exact replacement, not another operation which happens to reuse the same text.
	for _, current := range c.st.PendingPublications {
		if current.OperationID == p.OperationID {
			if reflect.DeepEqual(current, p) {
				return nil
			}
			return ErrPublicationConflict
		}
	}
	priorIndex := -1
	var prior PendingPublication
	for i, current := range c.st.PendingPublications {
		if current.OperationID == previousOperationID {
			prior, priorIndex = current, i
			break
		}
		if current.LogicalID == p.LogicalID {
			return ErrPublicationConflict
		}
	}
	if priorIndex < 0 || prior.Kind != PublicationComposer || prior.Phase != PublicationTerminal ||
		prior.TerminalCode != string(schema.CodeInputBusy) || !sameComposerRetry(prior, p) {
		return ErrPublicationState
	}
	proof, ok := c.st.OpOutcomes[previousOperationID]
	if !ok || proof.Op != controlOpError || proof.OperationID != previousOperationID ||
		proof.ErrorCode != schema.CodeInputBusy || len(proof.Journal) != 0 ||
		(proof.SessionID != "" && proof.SessionID != prior.SessionID) {
		return ErrPublicationState
	}
	unresolved := 0
	for _, current := range c.st.PendingPublications {
		if current.Phase != PublicationTerminal {
			unresolved++
		}
	}
	if unresolved >= MaxPendingPublications {
		return ErrPublicationQueueFull
	}
	st := c.st.clone()
	st.PendingPublications[priorIndex] = p.clone()
	delete(st.OpOutcomes, previousOperationID)
	return c.persistLocked(st)
}

func sameComposerRetry(prior, retry PendingPublication) bool {
	return prior.LogicalID == retry.LogicalID && prior.SessionID == retry.SessionID &&
		prior.SessionInstance == retry.SessionInstance && prior.ExpectedTurn == retry.ExpectedTurn &&
		prior.Text == retry.Text && prior.Machine == retry.Machine && prior.EpochID == retry.EpochID &&
		prior.Target == retry.Target && bytes.Equal(prior.AuthorityPub, retry.AuthorityPub) &&
		prior.Command.DeviceID == retry.Command.DeviceID &&
		prior.Command.Action == retry.Command.Action && prior.Command.Machine == retry.Command.Machine &&
		prior.Command.Session == retry.Command.Session &&
		bytes.Equal(prior.Command.ContentHash, retry.Command.ContentHash) &&
		reflect.DeepEqual(prior.Composer, retry.Composer)
}

// ExpirePublications terminalizes work whose safe retry/outcome window closed. It never deletes
// or reseals it: expiration is an honest result visible to the projection, and a late exact
// authenticated reply/echo may still settle it.
func (c *Core) ExpirePublications(now time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.st.clone()
	changed := false
	for i := range st.PendingPublications {
		p := &st.PendingPublications[i]
		switch p.Phase {
		case PublicationPrepared:
			if !p.Command.ExpiresAt.IsZero() && !now.Before(p.Command.ExpiresAt) {
				terminalizePublication(&st, p, PublicationExpired)
				recordLocalPublicationOutcome(&st, *p, PublicationExpired)
				changed = true
			}
		case PublicationSealed:
			if !p.Command.ExpiresAt.IsZero() && !now.Before(p.Command.ExpiresAt) {
				// Once exact bytes have been sealed, a crash or failed append response leaves
				// delivery undecidable. Expiry ends redrive but may not claim it was never sent.
				terminalizePublication(&st, p, PublicationOutcomeUnknown)
				recordLocalPublicationOutcome(&st, *p, PublicationOutcomeUnknown)
				changed = true
			}
		case PublicationAdmitted:
			if !p.CreatedAt.IsZero() && !now.Before(p.CreatedAt.Add(PublicationOutcomeTimeout)) {
				terminalizePublication(&st, p, PublicationOutcomeUnknown)
				recordLocalPublicationOutcome(&st, *p, PublicationOutcomeUnknown)
				changed = true
			}
		}
	}
	if !changed {
		return nil
	}
	boundPublicationResults(&st)
	return c.persistLocked(st)
}

// SealPublication moves one prepared publication onto an exact sequence/envelope. Once this
// transition commits, only the same bytes at the same sequence are idempotent.
func (c *Core) SealPublication(operationID string, sequence uint64, envelope []byte) error {
	if operationID == "" || sequence == 0 || len(envelope) == 0 {
		return ErrPublicationState
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.st.clone()
	for i := range st.PendingPublications {
		p := &st.PendingPublications[i]
		if p.OperationID != operationID {
			continue
		}
		if p.Phase == PublicationSealed || p.Phase == PublicationAdmitted {
			if p.Sequence == sequence && bytes.Equal(p.Envelope, envelope) {
				return nil
			}
			return ErrPublicationConflict
		}
		if p.Phase != PublicationPrepared {
			return ErrPublicationState
		}
		for j := range st.PendingPublications {
			other := &st.PendingPublications[j]
			if other.OperationID != operationID && other.EpochID == p.EpochID && other.Sequence == sequence {
				return ErrPublicationConflict
			}
		}
		p.Phase, p.Sequence, p.Envelope = PublicationSealed, sequence, slices.Clone(envelope)
		return c.persistLocked(st)
	}
	return ErrPublicationNotFound
}

// MarkPublicationAdmitted records a successful relay append without removing the operation;
// later publications no longer wait on it, while its authenticated outcome may still arrive.
func (c *Core) MarkPublicationAdmitted(operationID string) error {
	if operationID == "" {
		return ErrPublicationState
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.st.clone()
	for i := range st.PendingPublications {
		p := &st.PendingPublications[i]
		if p.OperationID != operationID {
			continue
		}
		if p.Phase == PublicationAdmitted {
			return nil
		}
		if p.Phase != PublicationSealed {
			return ErrPublicationState
		}
		p.Phase = PublicationAdmitted
		return c.persistLocked(st)
	}
	return ErrPublicationNotFound
}

// PendingPublications returns the durable FIFO as a deep-copy projection. No caller receives
// the State-owned envelope or request pointers.
func (c *Core) PendingPublications() []PendingPublication {
	c.mu.Lock()
	defer c.mu.Unlock()
	return clonePendingPublications(c.st.PendingPublications)
}
