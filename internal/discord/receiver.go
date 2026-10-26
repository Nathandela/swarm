package discord

import (
	"errors"
	"time"

	bolt "go.etcd.io/bbolt"
)

var ErrReceiverPending = errors.New("discord: launch receiver is unconfirmed; publication refused")

func publicationRequest(tx *bolt.Tx, ref string, now time.Time) (requestRecord, error) {
	record, e := activeRequest(tx, ref, now)
	if e != nil {
		return record, e
	}
	if record.Request.Target.ID == "control" && record.Request.Receiver == nil {
		return requestRecord{}, ErrReceiverPending
	}
	return record, nil
}

// BindReceiver records trusted launch acknowledgement separately from immutable input.
func (s *Store) BindReceiver(ref string, target Target) error {
	if !validTargetIdentity(target) || !controlText(target.Name, 512) || !controlText(target.Agent, 512) {
		return ErrInvalid
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		record, e := loadRequest(tx, ref)
		if e != nil {
			return e
		}
		if record.Request.Target.ID != "control" {
			return ErrInvalid
		}
		if record.Request.Receiver != nil {
			if *record.Request.Receiver != target {
				return ErrConflict
			}
			return nil
		}
		if record.Request.DeliveryState != StateSending && record.Request.DeliveryState != StateSent {
			return ErrInvalid
		}
		record.Request.Receiver = &target
		return putJSON(tx, "requests", []byte(ref), record)
	})
}
