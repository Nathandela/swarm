package discord

import (
	"context"
	"encoding/json"
	"errors"
	bolt "go.etcd.io/bbolt"
	"strings"
)

const MaxReceptionistRoles = 8
const MaxReceptionistGenerations = 8

var ErrReceptionistUnavailable = errors.New("discord: receptionist is stopped, unavailable, or changed; use the directory or explicitly reset its stopped role")

type receptionistRole struct {
	Key        string  `json:"key"`
	CLI        string  `json:"cli"`
	Model      string  `json:"model"`
	Generation int     `json:"generation"`
	State      string  `json:"state"`
	FirstRef   string  `json:"first_ref"`
	Target     *Target `json:"target,omitempty"`
}

func (b *Bridge) claimReceptionist(cli, model, ref string) (role receptionistRole, fresh bool, err error) {
	key := digest([]string{b.Service.Config.InstallationID, cli, model})[:32]
	err = b.Service.Store.db.Update(func(tx *bolt.Tx) error {
		bucket, e := tx.CreateBucketIfNotExists([]byte("receptionists"))
		if e != nil {
			return e
		}
		if raw := bucket.Get([]byte(key)); raw != nil {
			if e = json.Unmarshal(raw, &role); e != nil {
				return e
			}
			if role.State != StatePrepared {
				return nil
			}
		} else {
			if bucket.Stats().KeyN >= MaxReceptionistRoles {
				return ErrCapacity
			}
			role = receptionistRole{Key: key, CLI: cli, Model: model, Generation: 1, State: StatePrepared}
		}
		role.State = StateSending
		role.FirstRef = ref
		raw, e := json.Marshal(role)
		if e != nil {
			return e
		}
		if e = bucket.Put([]byte(key), raw); e != nil {
			return e
		}
		fresh = true
		return nil
	})
	return
}
func (b *Bridge) saveReceptionist(role receptionistRole) error {
	return b.Service.Store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("receptionists"))
		if bucket == nil {
			return ErrNotFound
		}
		var previous receptionistRole
		if e := json.Unmarshal(bucket.Get([]byte(role.Key)), &previous); e != nil {
			return e
		}
		if previous.Generation != role.Generation {
			return ErrConflict
		}
		raw, e := json.Marshal(role)
		if e != nil {
			return e
		}
		return bucket.Put([]byte(role.Key), raw)
	})
}
func (b *Bridge) useReceptionist(ctx context.Context, role receptionistRole, source Request) (bool, error) {
	if role.State != "ready" || role.Target == nil {
		return true, ErrDeliveryUncertain
	}
	if role.FirstRef == source.Ref {
		return true, nil
	}
	row, e := b.exactWorker(ctx, role.Target.ID)
	if e != nil || row.State != "running" && row.State != "working" || row.Agent != role.CLI || row.Instance == "" || strings.HasPrefix(row.Instance, "unavailable:") {
		return false, ErrReceptionistUnavailable
	}
	if excluded, e := b.Service.Store.IsOptedOut(row.Target); e != nil {
		return false, e
	} else if excluded {
		return false, ErrOptedOut
	}
	if role.Target.Instance == "" {
		role.Target = &row.Target
		if e = b.saveReceptionist(role); e != nil {
			return false, e
		}
	} else if role.Target.Instance != row.Instance {
		return false, ErrReceptionistUnavailable
	}
	eventID := "receptionist-turn:" + source.Ref
	if previous, e := b.Service.Store.ExistingEvent(eventID, b.Service.Now()); e == nil {
		_, e = b.Service.dispatch(ctx, previous)
		return true, e
	} else if !errors.Is(e, ErrNotFound) {
		return false, e
	}
	requester := source.RequesterID
	if requester == "" {
		requester = source.Author.ID
	}
	_, e = b.Service.dispatch(ctx, Request{EventID: eventID, EventAt: b.Service.Now(), Author: source.Author, RequesterID: requester, EndorserID: source.EndorserID, Authority: source.Authority, Route: source.Route, Target: row.Target, RoutingRef: source.Ref, Text: source.Text, Context: source.Context, Snapshot: true, MessageRevision: source.MessageRevision})
	return true, e
}
func (b *Bridge) resetReceptionist(ctx context.Context, row Worker) error {
	if row.State != "exited" {
		return ErrReceptionistUnavailable
	}
	return b.Service.Store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("receptionists"))
		if bucket == nil {
			return ErrNotFound
		}
		var role receptionistRole
		found := false
		if e := bucket.ForEach(func(_, raw []byte) error {
			var candidate receptionistRole
			if e := json.Unmarshal(raw, &candidate); e != nil {
				return e
			}
			if candidate.Target != nil && candidate.Target.ID == row.ID {
				role = candidate
				found = true
			}
			return nil
		}); e != nil {
			return e
		}
		if !found {
			return ErrNotFound
		}
		if role.Generation >= MaxReceptionistGenerations {
			return ErrCapacity
		}
		role.Generation++
		role.State = StatePrepared
		role.FirstRef = ""
		role.Target = nil
		raw, e := json.Marshal(role)
		if e != nil {
			return e
		}
		return bucket.Put([]byte(role.Key), raw)
	})
}
