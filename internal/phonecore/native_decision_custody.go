package phonecore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

const maxNativeDecisionCustody = 256

// NativeDecisionCustody retains identity and hashes, never an answer or replayable envelope.
type NativeDecisionCustody struct {
	ItemID      string                       `json:"item_id"`
	RequestHash string                       `json:"request_hash"`
	ChoiceHash  string                       `json:"choice_hash"`
	DeadlineMS  int64                        `json:"deadline_ms"`
	SetCursor   uint64                       `json:"set_cursor"`
	Receipt     schema.NativeDecisionReceipt `json:"receipt"`
}

func validNativeDecisionCustody(records []NativeDecisionCustody) bool {
	if len(records) > maxNativeDecisionCustody {
		return false
	}
	seen := map[string]bool{}
	for _, r := range records {
		for _, value := range []string{r.ItemID, r.Receipt.OperationID, r.Receipt.Session, r.Receipt.SessionInstance, r.Receipt.RootID, r.Receipt.Generation, r.Receipt.NativeSessionID, r.Receipt.RequestID} {
			if value == "" || len(value) > 256 || !utf8.ValidString(value) || strings.ContainsFunc(value, unicode.IsControl) {
				return false
			}
		}
		for _, hash := range []string{r.RequestHash, r.ChoiceHash} {
			raw, err := hex.DecodeString(hash)
			if err != nil || len(raw) != 32 || hex.EncodeToString(raw) != hash {
				return false
			}
		}
		if r.DeadlineMS <= 0 || r.DeadlineMS > 1<<53-1 || seen[r.Receipt.OperationID] {
			return false
		}
		seen[r.Receipt.OperationID] = true
		if r.Receipt.Kind != "permission_reply" && r.Receipt.Kind != "question_reply" && r.Receipt.Kind != "question_reject" {
			return false
		}
		if r.Receipt.State != "pending" && r.Receipt.State != "uncertain" && r.Receipt.State != "admitted" {
			return false
		}
		if len(r.Receipt.Reason) > 1024 || len(r.Receipt.NativeEffect) > 128 {
			return false
		}
	}
	return true
}
func nativeDecisionReceipt(id string, req schema.NativeDecisionReq) schema.NativeDecisionReceipt {
	return schema.NativeDecisionReceipt{OperationID: id, Session: req.Session, SessionInstance: req.SessionInstance, RootID: req.RootID, Generation: req.Generation, NativeSessionID: req.NativeSessionID, RequestID: req.RequestID, Kind: req.Kind, State: "pending"}
}
func sameNativeDecisionRequest(a, b schema.NativeDecisionReceipt) bool {
	return a.Session == b.Session && a.SessionInstance == b.SessionInstance && a.RootID == b.RootID && a.Generation == b.Generation && a.NativeSessionID == b.NativeSessionID && a.RequestID == b.RequestID
}

// BeginNativeDecision atomically gives one exact pending request one local operation.
func (c *Core) BeginNativeDecision(id, itemID string, req schema.NativeDecisionReq, now time.Time) (string, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	receipt := nativeDecisionReceipt(id, req)
	choice, _ := json.Marshal([]any{req.Kind, req.Reply, req.Answers})
	sum := sha256.Sum256(choice)
	choiceHash := hex.EncodeToString(sum[:])
	for _, r := range c.st.NativeDecisions {
		if !sameNativeDecisionRequest(r.Receipt, receipt) {
			continue
		}
		if r.RequestHash != req.RequestHash || r.ChoiceHash != choiceHash {
			return "", false, errors.New("phonecore: an earlier native answer remains tracked; inspect its outcome before choosing another answer")
		}
		return r.Receipt.OperationID, false, nil
	}
	if len(c.st.NativeDecisions) >= maxNativeDecisionCustody {
		return "", false, errors.New("phonecore: too many native answers await qualified request resolution")
	}
	if _, exists := c.st.OpOutcomes[id]; exists {
		return "", false, errors.New("phonecore: native operation identity is already used")
	}
	if req.ExpiresAt == nil || !now.Before(*req.ExpiresAt) {
		return "", false, errors.New("phonecore: native request expired before publication")
	}
	deadline := now.Add(CommandTTLFor(schema.ActionNativeDecide))
	if req.ExpiresAt.Before(deadline) {
		deadline = *req.ExpiresAt
	}
	r := NativeDecisionCustody{ItemID: itemID, RequestHash: req.RequestHash, ChoiceHash: choiceHash, DeadlineMS: deadline.UnixMilli(), Receipt: receipt}
	for _, item := range c.st.Items {
		if item.SessionID == req.Session && item.Kind == "request_set" && item.LastCursor > r.SetCursor {
			r.SetCursor = item.LastCursor
		}
	}
	st := c.st.clone()
	st.NativeDecisions = append(st.NativeDecisions, r)
	if !validNativeDecisionCustody(st.NativeDecisions) {
		return "", false, errors.New("phonecore: native answer custody is invalid")
	}
	if err := c.persistLocked(st); err != nil {
		return "", false, err
	}
	if c.nativeDecisionLive == nil {
		c.nativeDecisionLive = map[string]bool{}
	}
	c.nativeDecisionLive[id] = true
	return id, true, nil
}

// NativeDecisionOutcome keeps normal live publication pending; a new process cannot infer delivery.
func (c *Core) NativeDecisionOutcome(id string, now time.Time) (schema.Control, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range c.st.NativeDecisions {
		if r.Receipt.OperationID != id {
			continue
		}
		receipt := r.Receipt
		if receipt.State == "pending" && (!c.nativeDecisionLive[id] || now.UnixMilli() >= r.DeadlineMS) {
			receipt.State = "uncertain"
			receipt.Reason = "Native answer admission is unconfirmed; inspect this operation and do not submit another answer."
		}
		return schema.Control{Op: schema.ActionNativeDecide, OperationID: id, SessionID: receipt.Session, NativeDecisionReceipt: &receipt}, true
	}
	return schema.Control{}, false
}

// NativeDecisionOperations restores locks on the exact visible native cards after a screen rebuild.
func (c *Core) NativeDecisionOperations(session string) map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]string{}
	for _, r := range c.st.NativeDecisions {
		if r.Receipt.Session == session {
			out[r.ItemID] = r.Receipt.OperationID
		}
	}
	return out
}

func foldNativeDecisionOutcome(st *State, ctrl schema.Control) (schema.Control, bool) {
	if prior, ok := st.OpOutcomes[ctrl.OperationID]; ok && prior.Op == schema.ActionNativeDecide && prior.NativeDecisionReceipt != nil && prior.NativeDecisionReceipt.State == "admitted" && prior.NativeDecisionReceipt.OperationID == ctrl.OperationID && prior.NativeDecisionReceipt.Session == prior.SessionID {
		return ctrl, false
	}
	for i, r := range st.NativeDecisions {
		if r.Receipt.OperationID != ctrl.OperationID {
			continue
		}
		if r.Receipt.State == "admitted" {
			return ctrl, false
		}
		if ctrl.Op == "error" {
			receipt := r.Receipt
			receipt.State = "uncertain"
			receipt.Reason = "The machine did not confirm native admission; inspect the request before sending another answer."
			ctrl = schema.Control{Op: schema.ActionNativeDecide, OperationID: ctrl.OperationID, SessionID: receipt.Session, NativeDecisionReceipt: &receipt}
		}
		receipt := ctrl.NativeDecisionReceipt
		if ctrl.Op != schema.ActionNativeDecide || receipt == nil || ctrl.SessionID != r.Receipt.Session || receipt.OperationID != ctrl.OperationID || !sameNativeDecisionRequest(*receipt, r.Receipt) || receipt.Kind != r.Receipt.Kind || len(receipt.Reason) > 1024 || len(receipt.NativeEffect) > 128 {
			return ctrl, false
		}
		switch receipt.State {
		case "rejected":
			if receipt.NativeEffect != "none" {
				return ctrl, false
			}
			st.NativeDecisions = append(st.NativeDecisions[:i:i], st.NativeDecisions[i+1:]...)
		case "admitted", "uncertain":
			st.NativeDecisions[i].Receipt = *receipt
		default:
			return ctrl, false
		}
		return ctrl, true
	}
	return ctrl, true
}

// Only a later complete current native set can release a retained unresolved request.
func settleNativeDecisionSets(st *State) {
	kept := st.NativeDecisions[:0]
	for _, r := range st.NativeDecisions {
		resolved := false
		for _, item := range st.Items {
			if item.SessionID != r.Receipt.Session || item.Kind != "request_set" || item.LastCursor <= r.SetCursor {
				continue
			}
			w, ok := decodeNativeItem(item.Body)
			if !ok || w.Kind != "request_set" || !w.Complete || w.Provenance != "reconciled" || w.Requests == nil || len(w.Requests) > 128 || w.SessionInstance != r.Receipt.SessionInstance || w.RootID != r.Receipt.RootID || w.Generation != r.Receipt.Generation {
				continue
			}
			if _, present := w.Requests[r.ItemID]; !present {
				resolved = true
			}
		}
		if !resolved {
			kept = append(kept, r)
		} else if _, exists := st.OpOutcomes[r.Receipt.OperationID]; !exists {
			if st.OpOutcomes == nil {
				st.OpOutcomes = map[string]schema.Control{}
			}
			receipt := r.Receipt
			if receipt.State != "admitted" {
				receipt.State = "uncertain"
				receipt.Reason = "The native request is no longer pending; this answer's admission remains unconfirmed."
			}
			st.OpOutcomes[receipt.OperationID] = schema.Control{Op: schema.ActionNativeDecide, OperationID: receipt.OperationID, SessionID: receipt.Session, NativeDecisionReceipt: &receipt}
		}
	}
	st.NativeDecisions = kept
}
