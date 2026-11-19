package discord

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"time"

	bolt "go.etcd.io/bbolt"
)

const (
	teammatePendingBucket    = "teammate_pending"
	teammateIngressBucket    = "teammate_ingress"
	teammateDiscussionBucket = "teammate_discussions"
	teammateMetaBucket       = "teammate_meta"
	teammateOutboxBucket     = "teammate_outbox"
	teammateReceiptBucket    = "teammate_receipts"
	teammateIngressLimit     = 32768
	teammateDiscussionLimit  = 1024
)

type teammateCandidate struct {
	Mutation      uint64
	ParentChannel bool `json:"-"`
	Route         Route
	Author        Actor
	Opening       bool
	Edited        bool
	Sequence      uint64
	State         string
	Error         string
	Admission     *TeammateAdmission
}

type teammateBinding struct {
	TeammateDiscussion
	Opened            bool
	Boundary          string
	RepairUpper       string
	RepairLower       string
	RepairAfter       string
	RepairEpoch       uint64
	RepairTargetEpoch uint64
	NotifiedEpoch     uint64
	ParentLow         string
	Error             string
}

type teammateCheckpoint struct {
	Session         string
	GatewaySequence int64
	GapEpoch        uint64
	Error           string
}

func teammateKey(installation, id string) []byte { return []byte(installation + "\x00" + id) }
func teammatePendingKey(installation string, sequence uint64) []byte {
	return binary.BigEndian.AppendUint64(teammateKey(installation, ""), sequence)
}
func teammateGet(b *bolt.Bucket, key []byte, value any) error {
	raw := b.Get(key)
	if raw == nil {
		return ErrNotFound
	}
	return json.Unmarshal(raw, value)
}
func teammateEach(b *bolt.Bucket, installation string, fn func([]byte, []byte) error) error {
	prefix := teammateKey(installation, "")
	c := b.Cursor()
	for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
		if err := fn(k, v); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) teammateInterested(installation, id string) (candidate bool, err error) {
	err = s.db.View(func(tx *bolt.Tx) error {
		if tx.Bucket([]byte(teammateDiscussionBucket)).Get(teammateKey(installation, id)) != nil {
			candidate = true
			return nil
		}
		meta := tx.Bucket([]byte(teammateMetaBucket))
		candidate = meta.Get(teammateOpeningKey(installation, id)) != nil && meta.Get(teammateKey(installation, "parent:"+id)) == nil
		return nil
	})
	return
}

func (s *Store) teammateRecord(installation, session string, sequence int64, records []teammateCandidate, deleted bool) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(teammateIngressBucket))
		queue := tx.Bucket([]byte(teammatePendingBucket))
		for _, record := range records {
			key := teammateKey(installation, record.Route.MessageID)
			var prior teammateCandidate
			err := teammateGet(b, key, &prior)
			if err == nil {
				if (deleted || record.Edited) && record.Route.GuildID == "" {
					record.Route.GuildID = prior.Route.GuildID
				}
				if prior.Route != record.Route {
					return ErrConflict
				}
				if deleted || record.Edited {
					prior.Edited = true
					prior.Mutation++
					if deleted && prior.State == StatePrepared {
						prior.State = StateFailed
						prior.Error = "source deleted before submission"
						if err := queue.Delete(teammatePendingKey(installation, prior.Sequence)); err != nil {
							return err
						}
					}
					if err = putJSON(tx, teammateIngressBucket, key, prior); err != nil {
						return err
					}
				}
				continue
			}
			if !errors.Is(err, ErrNotFound) {
				return err
			}
			if record.Edited || deleted {
				continue
			}
			var binding teammateBinding
			err = teammateGet(tx.Bucket([]byte(teammateDiscussionBucket)), teammateKey(installation, record.Route.ChannelID), &binding)
			if err != nil && !errors.Is(err, ErrNotFound) {
				return err
			}
			if !record.Opening && err != nil {
				// Hold early, untagged posts only after an opener has been durably observed.
				openingKey := teammateOpeningKey(installation, record.Route.ChannelID)
				meta := tx.Bucket([]byte(teammateMetaBucket))
				if meta.Get(openingKey) == nil || meta.Get(teammateKey(installation, "parent:"+record.Route.ChannelID)) != nil {
					continue
				}
			}
			if b.Stats().KeyN >= teammateIngressLimit {
				return ErrCapacity
			}
			record.Sequence, err = b.NextSequence()
			if err != nil {
				return err
			}
			record.State = StatePrepared
			if err = putJSON(tx, teammateIngressBucket, key, record); err != nil {
				return err
			}
			if err = queue.Put(teammatePendingKey(installation, record.Sequence), key); err != nil {
				return err
			}
			if record.Opening {
				meta := tx.Bucket([]byte(teammateMetaBucket))
				for _, id := range []string{record.Route.ChannelID, record.Route.MessageID} {
					if err = meta.Put(teammateOpeningKey(installation, id), []byte{1}); err != nil {
						return err
					}
				}
			}
		}
		return teammateCheckpointUpdate(tx, installation, session, sequence, false, "")
	})
}

func teammateOpeningKey(installation, id string) []byte {
	return teammateKey(installation, "opening:"+id)
}

func teammateCheckpointUpdate(tx *bolt.Tx, installation, session string, sequence int64, gap bool, issue string) error {
	key := teammateKey(installation, "checkpoint")
	var p teammateCheckpoint
	if err := teammateGet(tx.Bucket([]byte(teammateMetaBucket)), key, &p); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	if gap && p.Session != "" {
		p.GapEpoch++
		if err := teammateEach(tx.Bucket([]byte(teammateDiscussionBucket)), installation, func(k, raw []byte) error {
			var b teammateBinding
			if err := json.Unmarshal(raw, &b); err != nil {
				return err
			}
			if b.RepairEpoch == p.GapEpoch-1 {
				b.RepairLower = b.Boundary
			}
			b.RepairAfter, b.RepairUpper = "", ""
			b.RepairTargetEpoch = p.GapEpoch
			return putJSON(tx, teammateDiscussionBucket, k, b)
		}); err != nil {
			return err
		}
		p.Error = "Unobserved Discord interval. Known threads are repaired as context; an unknown tag may have been missed."
	}
	if issue != "" {
		p.Error = issue
	}
	if session != "" {
		p.Session = session
		p.GatewaySequence = sequence
	}
	return putJSON(tx, teammateMetaBucket, key, p)
}

func (s *Store) teammateCheckpoint(installation string) (out teammateCheckpoint, err error) {
	err = s.db.View(func(tx *bolt.Tx) error {
		e := teammateGet(tx.Bucket([]byte(teammateMetaBucket)), teammateKey(installation, "checkpoint"), &out)
		if errors.Is(e, ErrNotFound) {
			return nil
		}
		return e
	})
	return
}

func (s *Store) teammatePending(installation string) (out []teammateCandidate, err error) {
	err = s.db.View(func(tx *bolt.Tx) error {
		ingress := tx.Bucket([]byte(teammateIngressBucket))
		meta := tx.Bucket([]byte(teammateMetaBucket))
		return teammateEach(tx.Bucket([]byte(teammatePendingBucket)), installation, func(_ []byte, key []byte) error {
			var record teammateCandidate
			if err := teammateGet(ingress, key, &record); err != nil {
				return err
			}
			record.ParentChannel = meta.Get(teammateKey(installation, "parent:"+record.Route.ChannelID)) != nil
			out = append(out, record)
			return nil
		})
	})
	return
}

func (s *Store) teammateBindings(installation string) (out []teammateBinding, err error) {
	err = s.db.View(func(tx *bolt.Tx) error {
		return teammateEach(tx.Bucket([]byte(teammateDiscussionBucket)), installation, func(_ []byte, raw []byte) error {
			var binding teammateBinding
			if e := json.Unmarshal(raw, &binding); e != nil {
				return e
			}
			out = append(out, binding)
			return nil
		})
	})
	return
}

func (s *Store) teammateBind(binding teammateBinding) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		key := teammateKey(binding.InstallationID, binding.Route.ChannelID)
		b := tx.Bucket([]byte(teammateDiscussionBucket))
		var old teammateBinding
		err := teammateGet(b, key, &old)
		if err == nil {
			if old.ID != binding.ID || old.Route != binding.Route {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		if b.Stats().KeyN >= teammateDiscussionLimit {
			return ErrCapacity
		}
		return putJSON(tx, teammateDiscussionBucket, key, binding)
	})
}

func (s *Store) teammateChangeBinding(d TeammateDiscussion, change func(*teammateBinding)) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		key := teammateKey(d.InstallationID, d.Route.ChannelID)
		var b teammateBinding
		if err := teammateGet(tx.Bucket([]byte(teammateDiscussionBucket)), key, &b); err != nil {
			return err
		}
		if b.ID != d.ID || b.Route != d.Route {
			return ErrConflict
		}
		change(&b)
		return putJSON(tx, teammateDiscussionBucket, key, b)
	})
}

func (s *Store) teammateCandidateChange(installation string, prior teammateCandidate, state, detail string, admission *TeammateAdmission) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		key := teammateKey(installation, prior.Route.MessageID)
		var record teammateCandidate
		if err := teammateGet(tx.Bucket([]byte(teammateIngressBucket)), key, &record); err != nil {
			return err
		}
		if record.State != prior.State || record.Mutation != prior.Mutation && record.State == StatePrepared {
			return ErrConflict
		}
		record.State, record.Error = state, detail
		if state == StateSent || state == StateFailed {
			if err := tx.Bucket([]byte(teammatePendingBucket)).Delete(teammatePendingKey(installation, record.Sequence)); err != nil {
				return err
			}
		}
		if state == StatePrepared {
			record.Admission = nil
		}
		if state == StateFailed {
			if err := teammateCheckpointUpdate(tx, installation, "", 0, false, "Discord input "+record.Route.MessageID+": "+detail); err != nil {
				return err
			}
		}
		if admission != nil {
			record.Admission = admission
		}
		if state == StateSent && record.Admission != nil {
			record.Admission.Text = ""
			record.Admission.Author.Name = ""
			d := record.Admission.Discussion
			k := teammateKey(installation, d.Route.ChannelID)
			var binding teammateBinding
			if err := teammateGet(tx.Bucket([]byte(teammateDiscussionBucket)), k, &binding); err != nil {
				return err
			}
			binding.Opened = true
			if record.Route.ChannelID == d.Route.ChannelID && snowflakeLess(binding.Boundary, record.Route.MessageID) {
				binding.Boundary = record.Route.MessageID
			}
			if err := putJSON(tx, teammateDiscussionBucket, k, binding); err != nil {
				return err
			}
		}
		return putJSON(tx, teammateIngressBucket, key, record)
	})
}

type teammateOutput struct {
	ExportAck    bool
	Internal     bool
	SettledAt    time.Time
	Created      time.Time
	Publication  TeammatePublication
	Route        Route
	DiscussionID string
	BodyHash     string
	Chunks       []Chunk
	ChunkHashes  []string
	Attempted    time.Time
	Error        string
}

type TeammateStatus struct {
	GatewaySequence int64                      `json:"gateway_sequence"`
	GapEpoch        uint64                     `json:"gap_epoch"`
	IngressIssue    string                     `json:"ingress_issue,omitempty"`
	Pending         int                        `json:"pending"`
	Discussions     []TeammateDiscussionStatus `json:"discussions,omitempty"`
	PendingNotices  int                        `json:"pending_notices,omitempty"`
}

type TeammateDiscussionStatus struct {
	ID          string                 `json:"id"`
	Thread      Route                  `json:"thread"`
	Boundary    string                 `json:"boundary,omitempty"`
	RepairEpoch uint64                 `json:"repair_epoch"`
	Diagnostic  string                 `json:"diagnostic,omitempty"`
	Pending     []TeammateInputStatus  `json:"pending,omitempty"`
	Outbox      []TeammateOutputStatus `json:"outbox,omitempty"`
	Native      *TeammateNativeStatus  `json:"native,omitempty"`
}

type TeammateInputStatus struct {
	MessageID   string `json:"message_id"`
	OperationID string `json:"operation_id,omitempty"`
	State       string `json:"state"`
}
type TeammateOutputStatus struct {
	ID        string `json:"id"`
	JobID     string `json:"job_id,omitempty"`
	AttemptID string `json:"attempt_id,omitempty"`
	Error     string `json:"error,omitempty"`
}

func (s *Store) teammateStatus(install string, native func(TeammateDiscussion) (TeammateNativeStatus, error)) (status TeammateStatus, err error) {
	checkpoint, err := s.teammateCheckpoint(install)
	if err != nil {
		return status, err
	}
	status.GatewaySequence, status.GapEpoch, status.IngressIssue = checkpoint.GatewaySequence, checkpoint.GapEpoch, checkpoint.Error
	rows, err := s.teammateBindings(install)
	if err != nil {
		return status, err
	}
	pending, err := s.teammatePending(install)
	if err != nil {
		return status, err
	}
	status.Pending = len(pending)
	byThread := make(map[string][]TeammateInputStatus)
	for _, record := range pending {
		thread := record.Route.ChannelID
		v := TeammateInputStatus{MessageID: record.Route.MessageID, State: record.State}
		if record.Admission != nil {
			thread = record.Admission.Discussion.Route.ChannelID
			v.OperationID = record.Admission.OperationID
		}
		if len(byThread[thread]) < 16 {
			byThread[thread] = append(byThread[thread], v)
		}
	}
	for _, b := range rows {
		outbox, e := s.teammateOutputs(install, b.ID)
		if e != nil {
			return status, e
		}
		d := TeammateDiscussionStatus{ID: b.ID, Thread: b.Route, Boundary: b.Boundary, RepairEpoch: b.RepairEpoch, Diagnostic: b.Error, Pending: byThread[b.Route.ChannelID]}
		if native != nil {
			view, err := native(b.TeammateDiscussion)
			if err != nil {
				view.Diagnostic = err.Error()
			}
			d.Native = &view
		}
		for _, p := range outbox {
			d.Outbox = append(d.Outbox, TeammateOutputStatus{ID: p.Publication.ID, JobID: p.Publication.JobID, AttemptID: p.Publication.AttemptID, Error: p.Error})
		}
		status.Discussions = append(status.Discussions, d)
	}
	notices, err := s.teammateOutputs(install, "")
	status.PendingNotices = len(notices)
	return status, err
}
