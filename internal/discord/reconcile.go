package discord

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	bolt "go.etcd.io/bbolt"
)

type DeliveryStatus struct {
	Ref           string `json:"ref"`
	OperationID   string `json:"operation_id"`
	DeliveryState string `json:"delivery_state"`
	NativeState   string `json:"native_state,omitempty"`
}
type PendingDelivery struct {
	Ref             string `json:"ref"`
	OperationID     string `json:"operation_id"`
	Session         string `json:"session"`
	SessionInstance string `json:"session_instance"`
	DeliveryState   string `json:"delivery_state"`
}
type receiptReader interface {
	Receipt(context.Context, string, string) (protocol.SemanticMessageReceipt, error)
}

func (s *Store) PendingDeliveries(now time.Time, limit int) (out []PendingDelivery, err error) {
	if limit < 1 || limit > MaxRequests {
		return nil, ErrInvalid
	}
	err = s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("requests")).ForEach(func(_, raw []byte) error {
			if len(out) >= limit {
				return nil
			}
			var record requestRecord
			if e := json.Unmarshal(raw, &record); e != nil {
				return e
			}
			r := record.Request
			if (r.DeliveryState != StateSending && r.DeliveryState != StateUncertain) || r.Target.ID == "routing" || r.Target.ID == "control" || strings.HasPrefix(r.EventID, "control:") || !now.Before(r.ExpiresAt) || optedOut(tx, r.Target) {
				return nil
			}
			out = append(out, PendingDelivery{r.Ref, r.OperationID, r.Target.ID, r.Target.Instance, r.DeliveryState})
			return nil
		})
	})
	return
}
func deliveryStatus(r Request) DeliveryStatus {
	status := DeliveryStatus{Ref: r.Ref, OperationID: r.OperationID, DeliveryState: r.DeliveryState}
	var receipt protocol.SemanticMessageReceipt
	if json.Unmarshal([]byte(r.NativeEvidence), &receipt) == nil {
		status.NativeState = receipt.State
	}
	return status
}
func receiptState(state string) string {
	switch state {
	case "submitted", "admitted", "completed", "failed", "cancelled", "declined":
		return StateSent
	case "rejected":
		return StateFailed
	}
	return ""
}
func receiptMatches(r Request, receipt protocol.SemanticMessageReceipt, installation string) bool {
	if receipt.OperationID != r.OperationID || receipt.Session != r.Target.ID || receipt.SessionInstance != r.Target.Instance {
		return false
	}
	return receipt.Origin == nil || (receipt.Origin.RequestID == r.Ref && receipt.Origin.InstallationID == installation)
}

// Reconcile reads the original operation's receipt; it never submits or reroutes input.
func (s *Service) Reconcile(ctx context.Context, ref string) (DeliveryStatus, error) {
	r, e := s.Store.Request(ref, s.Now())
	if e != nil {
		return DeliveryStatus{}, e
	}
	status := deliveryStatus(r)
	if (r.DeliveryState != StateSending && r.DeliveryState != StateUncertain) || r.Target.ID == "routing" || r.Target.ID == "control" || strings.HasPrefix(r.EventID, "control:") {
		return status, nil
	}
	reader, ok := s.Workers.(receiptReader)
	if !ok {
		return status, nil
	}
	receipt, e := reader.Receipt(ctx, r.Target.ID, r.OperationID)
	if e != nil {
		return status, e
	}
	if !receiptMatches(r, receipt, s.Config.InstallationID) {
		return status, ErrConflict
	}
	state := receiptState(receipt.State)
	if state == "" {
		return status, nil
	}
	evidence, e := json.Marshal(receipt)
	if e != nil {
		return status, e
	}
	if e = s.Store.FinishDelivery(ref, state, string(evidence)); e != nil {
		if !errors.Is(e, ErrConflict) {
			return status, e
		}
		current, readErr := s.Store.Request(ref, s.Now())
		if readErr != nil {
			return DeliveryStatus{}, readErr
		}
		if current.DeliveryState != state {
			return status, e
		}
		return deliveryStatus(current), nil
	}
	r.DeliveryState = state
	r.NativeEvidence = string(evidence)
	return deliveryStatus(r), nil
}
func (s *Service) ReconcilePending(ctx context.Context) error {
	if !s.reconcileMu.TryLock() {
		return nil
	}
	defer s.reconcileMu.Unlock()
	if e := ctx.Err(); e != nil {
		return e
	}
	pending, e := s.Store.PendingDeliveries(s.Now(), MaxRequests)
	if e != nil {
		return e
	}
	var first error
	start := 0
	for index, record := range pending {
		if record.Ref > s.reconcileAfter {
			start = index
			break
		}
	}
	for index := range pending {
		record := pending[(start+index)%len(pending)]
		if e = ctx.Err(); e != nil {
			return e
		}
		s.reconcileAfter = record.Ref
		if _, e = s.Reconcile(ctx, record.Ref); e != nil && !errors.Is(e, ErrExpired) && !errors.Is(e, ErrOptedOut) && first == nil {
			first = e
		}
	}
	return first
}
