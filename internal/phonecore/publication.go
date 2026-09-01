package phonecore

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

const MaxPendingPublications = 64

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

const PublicationAuthorityChanged = "authority_changed"

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
	CreatedAt       time.Time                     `json:"created_at"`
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
		if p.Sequence != 0 || len(p.Envelope) != 0 || p.TerminalCode != "" {
			return ErrPublicationState
		}
	case PublicationSealed, PublicationAdmitted:
		if p.Sequence == 0 || len(p.Envelope) == 0 || p.TerminalCode != "" {
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
	if len(publications) > MaxPendingPublications {
		return ErrPublicationQueueFull
	}
	seenOperations := make(map[string]struct{}, len(publications))
	seenLogical := make(map[string]struct{}, len(publications))
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

func publicationsForIdentity(publications []PendingPublication, machine string, epoch uint32, authorityPub []byte) []PendingPublication {
	out := clonePendingPublications(publications)
	for i := range out {
		p := &out[i]
		if p.Machine != machine || p.EpochID != epoch || !bytes.Equal(p.AuthorityPub, authorityPub) {
			p.Phase = PublicationTerminal
			p.TerminalCode = PublicationAuthorityChanged
		}
	}
	return out
}

// PreparePublication durably appends one unsequenced publication. Exact repetition is
// idempotent; the same operation id carrying different content is refused.
func (c *Core) PreparePublication(p PendingPublication) error {
	if p.Phase == "" {
		p.Phase = PublicationPrepared
	}
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
	if len(c.st.PendingPublications) >= MaxPendingPublications {
		return ErrPublicationQueueFull
	}
	st := c.st.clone()
	st.PendingPublications = append(st.PendingPublications, p.clone())
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
