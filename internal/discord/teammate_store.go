package discord

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/Nathandela/swarm/internal/workermail"
	bolt "go.etcd.io/bbolt"
)

const (
	teammatePendingBucket       = "teammate_pending"
	teammateIngressBucket       = "teammate_ingress"
	teammateIngressGuardBucket  = "teammate_ingress_guard"
	teammateDiscussionBucket    = "teammate_discussions"
	teammateActiveBindingBucket = "teammate_active_discussions"
	teammateBindingIDBucket     = "teammate_discussion_ids"
	teammateMetaBucket          = "teammate_meta"
	teammateOutboxBucket        = "teammate_outbox"
	teammateReceiptBucket       = "teammate_receipts"
	teammateIngressLimit        = 32768
	teammateDiscussionLimit     = 1024
)

type teammateCandidate struct {
	Mutation      uint64
	ParentChannel bool `json:"-"`
	Route         Route
	Author        Actor
	Opening       bool
	Edited        bool
	Deleted       bool
	Sequence      uint64
	State         string
	Error         string
	Admission     *TeammateAdmission
}

type teammateIngressGuard struct {
	RequireMention                                            bool `json:",omitempty"`
	Route                                                     Route
	ActorID, OperationID, Revision, DiscussionID, Disposition string
	LastChange                                                string
}

type teammateBinding struct {
	ProvisionalCancelled    bool
	InputNoticeID           string    `json:",omitempty"`
	InputNoticeAt           time.Time `json:",omitempty"`
	StartupNoticePending    bool
	StartupNoticeID         string `json:",omitempty"`
	StartupNoticeGeneration uint64
	TeammateDiscussion
	Opened                      bool
	LastIngressSequence         uint64
	Boundary                    string
	RepairUpper                 string
	RepairLower                 string
	RepairAfter                 string
	RepairEpoch                 uint64
	RepairTargetEpoch           uint64
	PersonalConsentID           string    `json:",omitempty"`
	PersonalHistoryFailureSince time.Time `json:",omitzero"`
	PersonalHistoryFailureEpoch uint64    `json:",omitempty"`
	PersonalScanEpoch           uint64    `json:",omitempty"`
	PersonalRepairEpoch         uint64    `json:",omitempty"`
	NotifiedEpoch               uint64
	ParentLow                   string
	Error                       string
}

type teammateCheckpoint struct {
	WorkerMailIssue string `json:",omitempty"`
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
		var binding teammateBinding
		err := teammateGet(tx.Bucket([]byte(teammateDiscussionBucket)), teammateKey(installation, id), &binding)
		if err == nil {
			candidate = !binding.ProvisionalCancelled && (binding.Opened || tx.Bucket([]byte(teammateActiveBindingBucket)).Get(teammateKey(installation, id)) != nil)
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		meta := tx.Bucket([]byte(teammateMetaBucket))
		candidate = meta.Get(teammateOpeningKey(installation, id)) != nil && meta.Get(teammateKey(installation, "parent:"+id)) == nil
		return nil
	})
	return
}

func (s *Store) teammateRecord(installation, session string, sequence int64, records []teammateCandidate, deleted bool) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		if err := teammateRecordCandidates(tx, installation, records, deleted); err != nil {
			return err
		}
		return teammateCheckpointUpdate(tx, installation, session, sequence, false, "")
	})
}

func teammateRecordCandidates(tx *bolt.Tx, installation string, records []teammateCandidate, deleted bool) error {
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
			if prior.Route != record.Route || !deleted && !record.Edited && prior.Author.ID != record.Author.ID {
				return ErrConflict
			}
			if deleted || record.Edited {
				prior.Edited = true
				prior.Mutation++
				prior.Deleted = prior.Deleted || deleted
				if deleted && prior.State == StatePrepared {
					if err := teammateSettleCandidate(tx, installation, key, prior, StateFailed); err != nil {
						return err
					}
					continue
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
		var guard teammateIngressGuard
		err = teammateGet(tx.Bucket([]byte(teammateIngressGuardBucket)), key, &guard)
		if err == nil {
			if (record.Edited || deleted) && record.Route.GuildID == "" {
				record.Route.GuildID = guard.Route.GuildID
			}
			if record.Route != guard.Route || !deleted && !record.Edited && guard.ActorID != record.Author.ID {
				return ErrConflict
			}
			if deleted || record.Edited && guard.LastChange != "deleted" {
				guard.LastChange = "edited"
				if deleted {
					guard.LastChange = "deleted"
				}
				if err = putJSON(tx, teammateIngressGuardBucket, key, guard); err != nil {
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
		bindingKey := teammateKey(installation, record.Route.ChannelID)
		err = teammateGet(tx.Bucket([]byte(teammateDiscussionBucket)), bindingKey, &binding)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		knownBinding := err == nil
		if knownBinding {
			if binding.Route.GuildID != record.Route.GuildID {
				return ErrConflict
			}
			if !record.Opening && (binding.ProvisionalCancelled || !binding.Opened && tx.Bucket([]byte(teammateActiveBindingBucket)).Get(teammateKey(installation, record.Route.ChannelID)) == nil) {
				continue
			}
			if err = teammateActivate(tx, &binding); err != nil {
				return err
			}
		}
		if !record.Opening && errors.Is(err, ErrNotFound) {
			// Hold early, untagged posts only after an opener has been durably observed.
			openingKey := teammateOpeningKey(installation, record.Route.ChannelID)
			meta := tx.Bucket([]byte(teammateMetaBucket))
			if meta.Get(openingKey) == nil || meta.Get(teammateKey(installation, "parent:"+record.Route.ChannelID)) != nil {
				continue
			}
		}
		if err = teammateIngressCount(tx, 1); err != nil {
			return err
		}
		record.Sequence, err = b.NextSequence()
		if err != nil {
			return err
		}
		record.State = StatePrepared
		if knownBinding {
			binding.LastIngressSequence = record.Sequence
			if err = putJSON(tx, teammateDiscussionBucket, bindingKey, binding); err != nil {
				return err
			}
		}
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
	return nil
}

func teammateOpeningKey(installation, id string) []byte {
	return teammateKey(installation, "opening:"+id)
}

const teammateContextGapMessage = "Unobserved Discord interval. Known threads are repaired as context; an unknown tag may have been missed."

func teammateCheckpointUpdate(tx *bolt.Tx, installation, session string, sequence int64, gap bool, issue string) error {
	key := teammateKey(installation, "checkpoint")
	var p teammateCheckpoint
	if err := teammateGet(tx.Bucket([]byte(teammateMetaBucket)), key, &p); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	if gap && p.Session != "" {
		p.GapEpoch++
		if err := teammateEach(tx.Bucket([]byte(teammateActiveBindingBucket)), installation, func(k, _ []byte) error {
			var b teammateBinding
			if err := teammateGet(tx.Bucket([]byte(teammateDiscussionBucket)), k, &b); err != nil {
				return err
			}
			teammatePrepareRepair(&b, p.GapEpoch)
			return putJSON(tx, teammateDiscussionBucket, k, b)
		}); err != nil {
			return err
		}
		p.Error = teammateContextGapMessage
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

func (s *Store) teammatePending(installation string) ([]teammateCandidate, error) {
	rows, _, err := s.teammatePendingSnapshot(installation)
	return rows, err
}

func (s *Store) teammatePendingSnapshot(installation string) (out []teammateCandidate, through uint64, err error) {
	err = s.db.View(func(tx *bolt.Tx) error {
		ingress := tx.Bucket([]byte(teammateIngressBucket))
		through = ingress.Sequence()
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

func (s *Store) teammateActiveBindings(installation string) (out []teammateBinding, err error) {
	err = s.db.View(func(tx *bolt.Tx) error {
		return teammateEach(tx.Bucket([]byte(teammateActiveBindingBucket)), installation, func(k, _ []byte) error {
			var b teammateBinding
			if err := teammateGet(tx.Bucket([]byte(teammateDiscussionBucket)), k, &b); err != nil {
				return err
			}
			out = append(out, b)
			return nil
		})
	})
	return
}

func (s *Store) teammateBindingByID(installation, id string) (binding teammateBinding, err error) {
	err = s.db.View(func(tx *bolt.Tx) error {
		k := tx.Bucket([]byte(teammateBindingIDBucket)).Get(teammateKey(installation, id))
		if k == nil {
			return ErrNotFound
		}
		if err := teammateGet(tx.Bucket([]byte(teammateDiscussionBucket)), k, &binding); err != nil {
			return err
		}
		if binding.ID != id || binding.InstallationID != installation {
			return ErrConflict
		}
		return nil
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
			return teammateActivate(tx, &old)
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		if err := teammateIndexBindingID(tx, binding, key); err != nil {
			return err
		}
		return teammateActivate(tx, &binding)
	})
}

func teammateIndexBindingID(tx *bolt.Tx, b teammateBinding, key []byte) error {
	ids := tx.Bucket([]byte(teammateBindingIDBucket))
	id := teammateKey(b.InstallationID, b.ID)
	if old := ids.Get(id); old != nil && !bytes.Equal(old, key) {
		return ErrConflict
	}
	return ids.Put(id, key)
}

func teammatePrepareRepair(b *teammateBinding, epoch uint64) {
	if epoch <= b.RepairTargetEpoch || epoch <= b.RepairEpoch {
		return
	}
	if b.RepairTargetEpoch <= b.RepairEpoch {
		b.RepairLower = b.Boundary
	}
	b.RepairAfter, b.RepairUpper = "", ""
	b.RepairTargetEpoch = epoch
}

func teammateActivate(tx *bolt.Tx, b *teammateBinding) error {
	key := teammateKey(b.InstallationID, b.Route.ChannelID)
	active := tx.Bucket([]byte(teammateActiveBindingBucket))
	if active.Get(key) != nil {
		return nil
	}
	count := 0
	if err := teammateEach(active, b.InstallationID, func(_, _ []byte) error {
		count++
		if count >= teammateDiscussionLimit {
			return ErrCapacity
		}
		return nil
	}); err != nil {
		return err
	}
	var p teammateCheckpoint
	err := teammateGet(tx.Bucket([]byte(teammateMetaBucket)), teammateKey(b.InstallationID, "checkpoint"), &p)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	teammatePrepareRepair(b, p.GapEpoch)
	if err = putJSON(tx, teammateDiscussionBucket, key, *b); err != nil {
		return err
	}
	return active.Put(key, []byte{1})
}

type parkedWorkerNotes struct {
	ReplyReceipts [][]PublicationProof `json:",omitempty"`
	Discussion    TeammateDiscussion
	Native        TeammateNativeStatus
	At            time.Time
}

func recentWorkerNotes(tx *bolt.Tx, installation string) ([]parkedWorkerNotes, error) {
	var recent []parkedWorkerNotes
	err := teammateGet(tx.Bucket([]byte(teammateMetaBucket)), teammateKey(installation, "parked-worker-notes"), &recent)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return recent, err
}

func (s *Store) teammatePark(d TeammateDiscussion, through uint64, native *TeammateNativeStatus) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		key := teammateKey(d.InstallationID, d.Route.ChannelID)
		var binding teammateBinding
		if err := teammateGet(tx.Bucket([]byte(teammateDiscussionBucket)), key, &binding); err != nil {
			return err
		}
		if binding.ID != d.ID || binding.Route != d.Route {
			return ErrConflict
		}
		// A concurrent new message to this thread supersedes the scheduler's drain snapshot.
		if binding.LastIngressSequence > through || binding.StartupNoticePending || binding.Opened && binding.RepairTargetEpoch > binding.RepairEpoch {
			return nil
		}
		hasOutput := false
		if err := teammateEach(tx.Bucket([]byte(teammateOutboxBucket)), d.InstallationID+"\x00"+d.ID, func(_, raw []byte) error {
			var output teammateOutput
			if err := json.Unmarshal(raw, &output); err != nil {
				return err
			}
			hasOutput = hasOutput || output.SettledAt.IsZero()
			return nil
		}); err != nil {
			return err
		}
		if hasOutput {
			return nil
		}
		var retainedJobs []TeammateJobStatus
		if native != nil && native.State == "parked" {
			for _, job := range native.Jobs {
				if job.RecoveryRequired {
					retainedJobs = append(retainedJobs, job)
					if len(retainedJobs) == 16 {
						break
					}
				}
			}
		}
		if native != nil && native.State == "parked" && (native.UnconfirmedReplies > 0 || native.RetirementOperation != "" || len(native.Notes) > 0 || len(retainedJobs) > 0) {
			recent, err := recentWorkerNotes(tx, d.InstallationID)
			if err != nil {
				return err
			}
			recent = slices.DeleteFunc(recent, func(v parkedWorkerNotes) bool { return v.Discussion.ID == d.ID })
			if len(recent) >= 16 {
				recent = recent[:15]
			}
			snapshot := *native
			snapshot.Jobs = retainedJobs
			snapshot.Notes = snapshot.Notes[:min(len(snapshot.Notes), 16)]
			row := parkedWorkerNotes{Discussion: d, Native: snapshot, ReplyReceipts: native.ReplyReceipts, At: time.Now().UTC()}
			recent = append([]parkedWorkerNotes{row}, recent...)
			if err := putJSON(tx, teammateMetaBucket, teammateKey(d.InstallationID, "parked-worker-notes"), recent); err != nil {
				return err
			}
		}
		return tx.Bucket([]byte(teammateActiveBindingBucket)).Delete(key)
	})
}

func initializeTeammateBindingIndices(tx *bolt.Tx) error {
	return tx.Bucket([]byte(teammateDiscussionBucket)).ForEach(func(k, raw []byte) error {
		var b teammateBinding
		if err := json.Unmarshal(raw, &b); err != nil {
			return err
		}
		if err := teammateIndexBindingID(tx, b, k); err != nil {
			return err
		}
		return tx.Bucket([]byte(teammateActiveBindingBucket)).Put(k, []byte{1})
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
		if record.State != prior.State || record.Mutation != prior.Mutation && (record.State == StatePrepared || state == StateSending) {
			return ErrConflict
		}
		record.State, record.Error = state, detail
		if state == StatePrepared {
			record.Admission = nil
		}
		if state == StateFailed {
			if err := teammateCheckpointUpdate(tx, installation, "", 0, false, "Discord input "+record.Route.MessageID+": "+detail); err != nil {
				return err
			}
		}
		if admission != nil {
			persisted := *admission
			persisted.Text = ""
			record.Admission = &persisted
		}
		if (state == StateSent || state == StateFailed) && record.Admission != nil {

			d := record.Admission.Discussion
			k := teammateKey(installation, d.Route.ChannelID)
			var binding teammateBinding
			if err := teammateGet(tx.Bucket([]byte(teammateDiscussionBucket)), k, &binding); err != nil {
				return err
			}
			if state == StateSent {
				if !binding.Opened && binding.PersonalConsentID != "" {
					binding.PersonalConsentID = record.Route.MessageID
				}
				binding.Opened = true
				binding.ProvisionalCancelled = false
				if record.Route.ChannelID == d.Route.ChannelID && snowflakeLess(binding.Boundary, record.Route.MessageID) {
					binding.Boundary = record.Route.MessageID
				}
			} else if !binding.Opened {
				binding.ProvisionalCancelled = true
			}
			if binding.StartupNoticePending && binding.StartupNoticeGeneration == 0 && (binding.Opened || binding.ProvisionalCancelled) {
				if err := teammateCancelPreparedNotice(tx, d, binding.startupNoticeID(), teammateStartupNotice); err != nil {
					return err
				}
				binding.StartupNoticePending = false
			}
			if err := putJSON(tx, teammateDiscussionBucket, k, binding); err != nil {
				return err
			}
		}
		if state == StateSent || state == StateFailed {
			return teammateSettleCandidate(tx, installation, key, record, state)
		}
		return putJSON(tx, teammateIngressBucket, key, record)
	})
}

func teammateIngressCount(tx *bolt.Tx, delta int) error {
	meta := tx.Bucket([]byte(teammateMetaBucket))
	key := []byte("active_ingress_count")
	raw := meta.Get(key)
	var count int
	if len(raw) == 8 {
		count = int(binary.BigEndian.Uint64(raw))
	} else {
		count = tx.Bucket([]byte(teammatePendingBucket)).Stats().KeyN
	}
	count += delta
	if count > teammateIngressLimit {
		return ErrCapacity
	}
	if count < 0 {
		return ErrConflict
	}
	return meta.Put(key, binary.BigEndian.AppendUint64(nil, uint64(count)))
}

func teammateSettleCandidate(tx *bolt.Tx, install string, key []byte, record teammateCandidate, state string) error {
	guard := teammateIngressGuard{Route: record.Route, ActorID: record.Author.ID, Disposition: state}
	if record.Admission != nil {
		guard.OperationID, guard.Revision, guard.DiscussionID = record.Admission.OperationID, record.Admission.Revision, record.Admission.Discussion.ID
		guard.RequireMention = record.Admission.RequireMention
	}
	if record.Edited {
		guard.LastChange = "edited"
	}
	if record.Deleted {
		guard.LastChange = "deleted"
	}
	if err := putJSON(tx, teammateIngressGuardBucket, key, guard); err != nil {
		return err
	}
	if err := teammateIngressCount(tx, -1); err != nil {
		return err
	}
	if err := tx.Bucket([]byte(teammatePendingBucket)).Delete(teammatePendingKey(install, record.Sequence)); err != nil {
		return err
	}
	return tx.Bucket([]byte(teammateIngressBucket)).Delete(key)
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
	WorkerMailIssue string                     `json:"worker_mail_issue,omitempty"`
	GatewaySequence int64                      `json:"gateway_sequence"`
	GapEpoch        uint64                     `json:"gap_epoch"`
	IngressIssue    string                     `json:"ingress_issue,omitempty"`
	Pending         int                        `json:"pending"`
	Discussions     []TeammateDiscussionStatus `json:"discussions,omitempty"`
	PendingNotices  int                        `json:"pending_notices,omitempty"`
}

type TeammateDiscussionStatus struct {
	SupervisorSocket string                 `json:"supervisor_socket,omitempty"`
	ID               string                 `json:"id"`
	Thread           Route                  `json:"thread"`
	Boundary         string                 `json:"boundary,omitempty"`
	RepairEpoch      uint64                 `json:"repair_epoch"`
	Diagnostic       string                 `json:"diagnostic,omitempty"`
	Pending          []TeammateInputStatus  `json:"pending,omitempty"`
	Outbox           []TeammateOutputStatus `json:"outbox,omitempty"`
	Native           *TeammateNativeStatus  `json:"native,omitempty"`
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

// Read both sides of compaction in one snapshot so an observed send stays visible.
func (s *Store) workerPublicationStatus(d TeammateDiscussion, native *TeammateNativeStatus) error {
	return s.db.View(func(tx *bolt.Tx) error {
		var pending [][]PublicationProof
		for _, group := range native.ReplyReceipts {
			delivered := len(group) != 0
			for _, proof := range group {
				ids, err := confirmedPublicationIDs(tx, d, proof)
				if err != nil && !errors.Is(err, ErrConflict) {
					return err
				}
				if len(ids) == 0 {
					delivered = false
					break
				}
			}
			if delivered && native.UnconfirmedReplies > 0 {
				native.UnconfirmedReplies--
			} else {
				pending = append(pending, group)
			}
		}
		native.ReplyReceipts = pending
		for _, job := range native.Jobs {
			if job.NativeTurnPublication != nil {
				if err := readPublicationStatus(tx, d, job.NativeTurnPublication, nil); err != nil {
					return err
				}
			}
		}
		for _, note := range native.Notes {
			for i := range note.Publications {
				if err := readPublicationStatus(tx, d, &note.Publications[i], nil); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// PublicationProof contains the same immutable hashes as a compacted outbox receipt.
// Parked status can reconcile a late acknowledgement without retaining message bodies.
type PublicationProof struct {
	ID, BodyHash, PublicationHash string
	Chunks                        int
}

func (p TeammatePublication) Proof() PublicationProof {
	proof := PublicationProof{ID: p.ID, BodyHash: PublicationBodyHash(p.Body), Chunks: len(splitUTF16(p.Body, 1900))}
	p.Body = ""
	proof.PublicationHash = digest(p)
	return proof
}

func (s *Store) confirmedPublication(d TeammateDiscussion, expected TeammatePublication) (ids []string, err error) {
	proof := expected.Proof()
	err = s.db.View(func(tx *bolt.Tx) error {
		var readErr error
		ids, readErr = confirmedPublicationIDs(tx, d, proof)
		return readErr
	})
	return ids, err
}

func confirmedPublicationIDs(tx *bolt.Tx, d TeammateDiscussion, proof PublicationProof) ([]string, error) {
	pub := workermail.PublicationStatus{ID: proof.ID}
	if err := readPublicationStatus(tx, d, &pub, &proof); err != nil {
		return nil, err
	}
	if pub.State != StateSent || proof.Chunks == 0 || len(pub.MessageIDs) != proof.Chunks {
		return nil, nil
	}
	for _, id := range pub.MessageIDs {
		if !snowflake(id) {
			return nil, ErrConflict
		}
	}
	return pub.MessageIDs, nil
}

func readPublicationStatus(tx *bolt.Tx, d TeammateDiscussion, pub *workermail.PublicationStatus, expected *PublicationProof) error {
	k := teammateOutputKey(d.InstallationID, d.ID, pub.ID)
	var out teammateOutput
	err := teammateGet(tx.Bucket([]byte(teammateOutboxBucket)), k, &out)
	if errors.Is(err, ErrNotFound) {
		var receipt teammateReceipt
		err = teammateGet(tx.Bucket([]byte(teammateReceiptBucket)), k, &receipt)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		ids := receipt.DiscordMessageIDs
		if expected == nil && len(receipt.UnverifiedMessageIDs) > 0 {
			ids = receipt.UnverifiedMessageIDs
		}
		if receipt.Route != d.Route || len(ids) == 0 {
			return nil
		}
		if expected != nil && (receipt.BodyHash != expected.BodyHash || receipt.PublicationHash != expected.PublicationHash) {
			return ErrConflict
		}
		pub.State, pub.MessageIDs = StateSent, ids
		return nil
	}
	if err != nil {
		return err
	}
	if out.Route != d.Route || len(out.Chunks) == 0 {
		return nil
	}
	if expected != nil && (out.DiscussionID != d.ID || out.BodyHash != expected.BodyHash || digest(out.Publication) != expected.PublicationHash || out.Internal) {
		return ErrConflict
	}
	pub.State = StateSent
	for _, part := range out.Chunks {
		if part.State == StateSent && snowflake(part.MessageID) && (expected == nil || part.GatewaySeen || part.RESTAcknowledged) {
			pub.MessageIDs = append(pub.MessageIDs, part.MessageID)
		} else if pub.State == StateSent {
			pub.State = part.State
			if pub.State == StateSent {
				pub.State = StateUncertain
			}
		}
	}
	return nil
}

func (s *Store) teammateStatus(install string, native func(TeammateDiscussion) (TeammateNativeStatus, error)) (status TeammateStatus, err error) {
	checkpoint, err := s.teammateCheckpoint(install)
	if err != nil {
		return status, err
	}
	status.WorkerMailIssue = checkpoint.WorkerMailIssue
	status.GatewaySequence, status.GapEpoch, status.IngressIssue = checkpoint.GatewaySequence, checkpoint.GapEpoch, checkpoint.Error
	rows, err := s.teammateActiveBindings(install)
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
		d := TeammateDiscussionStatus{ID: b.ID, SupervisorSocket: b.SupervisorSocket, Thread: b.Route, Boundary: b.Boundary, RepairEpoch: b.RepairEpoch, Diagnostic: b.Error, Pending: byThread[b.Route.ChannelID]}
		if native != nil {
			view, err := native(b.TeammateDiscussion)
			if err != nil {
				view.Diagnostic = err.Error()
			}
			if err == nil && (len(view.ReplyReceipts) > 0 || len(view.Notes) > 0 || len(view.Jobs) > 0) {
				if e := s.workerPublicationStatus(b.TeammateDiscussion, &view); e != nil {
					return status, e
				}
			}
			d.Native = &view
		}
		for _, p := range outbox {
			d.Outbox = append(d.Outbox, TeammateOutputStatus{ID: p.Publication.ID, JobID: p.Publication.JobID, AttemptID: p.Publication.AttemptID, Error: p.Error})
		}
		status.Discussions = append(status.Discussions, d)
	}
	var recent []parkedWorkerNotes
	if err = s.db.View(func(tx *bolt.Tx) error {
		var readErr error
		recent, readErr = recentWorkerNotes(tx, install)
		return readErr
	}); err != nil {
		return status, err
	}
	for _, row := range recent {
		if time.Since(row.At) > workermail.ReceiptRetention || slices.ContainsFunc(rows, func(b teammateBinding) bool { return b.ID == row.Discussion.ID }) {
			continue
		}
		row.Native.ReplyReceipts = row.ReplyReceipts
		if err := s.workerPublicationStatus(row.Discussion, &row.Native); err != nil {
			return status, err
		}
		status.Discussions = append(status.Discussions, TeammateDiscussionStatus{ID: row.Discussion.ID, SupervisorSocket: row.Discussion.SupervisorSocket, Thread: row.Discussion.Route, Native: &row.Native})
	}
	notices, err := s.teammateOutputs(install, "")
	status.PendingNotices = len(notices)
	return status, err
}

func (s *Store) teammateActivateWorkerMail(installation string, matches func(TeammateDiscussion) bool) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		active := tx.Bucket([]byte(teammateActiveBindingBucket))
		// Activation can update the row; finish iterating the bucket before writing.
		var wanted []teammateBinding
		err := teammateEach(tx.Bucket([]byte(teammateDiscussionBucket)), installation, func(k, raw []byte) error {
			if active.Get(k) != nil {
				return nil
			}
			var b teammateBinding
			if err := json.Unmarshal(raw, &b); err != nil {
				return err
			}
			if b.Opened && matches(b.TeammateDiscussion) {
				wanted = append(wanted, b)
			}
			return nil
		})
		if err != nil {
			return err
		}
		for _, b := range wanted {
			err = teammateActivate(tx, &b)
			if errors.Is(err, ErrCapacity) {
				break
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) teammateMailIssue(installation, issue string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		key := teammateKey(installation, "checkpoint")
		var checkpoint teammateCheckpoint
		if err := teammateGet(tx.Bucket([]byte(teammateMetaBucket)), key, &checkpoint); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		checkpoint.WorkerMailIssue = issue
		return putJSON(tx, teammateMetaBucket, key, checkpoint)
	})
}
