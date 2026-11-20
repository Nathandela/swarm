package schedule

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// ClaimDue evaluates each active job and writes its latest due instant. An
// ineligible slot is recorded once as missed.
func (s *Store) ClaimDue(now time.Time) ([]Occurrence, error) {
	now = now.UTC()
	var due []jobEvaluation
	err := s.view(func(tx *bolt.Tx) error {
		var err error
		due, err = s.dueOccurrences(tx, now)
		return err
	})
	if err != nil || len(due) == 0 {
		return nil, err
	}
	var claimed []Occurrence
	err = s.update(func(tx *bolt.Tx) error {
		// A writer may have edited or claimed after the initial read.
		due, err := s.dueOccurrences(tx, now)
		if err != nil {
			return err
		}
		for _, item := range due {
			o, err := saveEvaluation(tx, item, "")
			if err != nil {
				return err
			}
			if o.ID != "" && o.State != OccurrenceMissed {
				claimed = append(claimed, o)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return claimed, nil
}

type jobEvaluation struct {
	jobID      string
	cursor     time.Time
	due        Occurrence
	hostZoneID string
	wallSlot   string
}

func (s *Store) dueOccurrences(tx *bolt.Tx, now time.Time) ([]jobEvaluation, error) {
	var due []jobEvaluation
	// Resolve the OS zone once per transaction, only if a job follows it.
	var zone *time.Location
	var zoneID string
	resolve := func() (*time.Location, string, error) {
		var err error
		if zone == nil {
			zone, zoneID, err = s.hostZone()
		}
		return zone, zoneID, err
	}
	err := eachRecord(tx, jobsBucket, func(job Job) error {
		evaluation, err := evaluateJob(tx, job, now, now, resolve)
		if err == nil && !evaluation.cursor.IsZero() {
			due = append(due, evaluation)
		}
		return err
	})
	slices.SortFunc(due, func(a, b jobEvaluation) int {
		return cmp.Or(a.cursor.Compare(b.cursor), cmp.Compare(a.jobID, b.jobID))
	})
	return due, err
}

func evaluateJob(tx *bolt.Tx, job Job, through, now time.Time, host hostZoneResolver) (jobEvaluation, error) {
	result := jobEvaluation{jobID: job.ID}
	if job.State != JobActive {
		return result, nil
	}
	from, err := record[time.Time](tx, evalBucket, job.ID)
	if err != nil {
		return result, err
	}
	if through.Before(from) {
		// Resume at the current wall minute when the clock reverses.
		// The deterministic key still suppresses already handled slots.
		rewound := through.Truncate(time.Minute).Add(-time.Nanosecond)
		if updated := job.UpdatedAt.Add(-time.Nanosecond); updated.After(rewound) {
			rewound = updated
		}
		if rewound.Before(from) {
			from, result.cursor = rewound, rewound
		}
	}
	rule, err := job.Spec.Calendar.compileWith(host)
	if err != nil {
		return result, err
	}
	if rule.hostZoneID != "" {
		oldZone, err := record[string](tx, evalBucket, job.ID+"/zone")
		if err != nil && !errors.Is(err, ErrNotFound) {
			return result, err
		}
		if rule.hostZoneID != oldZone {
			// A newly observed zone applies from the current minute. Never
			// reinterpret the elapsed catch-up window in the new zone.
			bound := through.Truncate(time.Minute).Add(-time.Nanosecond)
			if bound.After(from) {
				from = bound
			}
			result.cursor, result.hostZoneID = from, rule.hostZoneID
		}
	}
	at := rule.find(through, true)
	if rule.hostZoneID == "" {
		at = at.UTC()
	}
	if !at.After(from) {
		return result, nil
	}
	result.cursor = at.UTC()
	if rule.hostZoneID != "" {
		// Traveling west can present the same wall slot twice at different
		// instants. Retain the original UTC occurrence and dispatch identity.
		result.wallSlot = job.ID + "/wall/" + at.Format("2006-01-02T15:04")
	}
	result.due, err = newOccurrence(job, at, now, "")
	return result, err
}

func saveEvaluation(tx *bolt.Tx, evaluation jobEvaluation, skipReason string) (Occurrence, error) {
	if evaluation.cursor.IsZero() {
		return Occurrence{}, nil
	}
	if err := putRecord(tx, evalBucket, evaluation.jobID, evaluation.cursor); err != nil {
		return Occurrence{}, err
	}
	if evaluation.hostZoneID != "" {
		if err := putRecord(tx, evalBucket, evaluation.jobID+"/zone", evaluation.hostZoneID); err != nil {
			return Occurrence{}, err
		}
	}
	if evaluation.wallSlot != "" {
		b := tx.Bucket([]byte(evalBucket))
		if b.Get([]byte(evaluation.wallSlot)) != nil {
			return Occurrence{}, nil
		}
		if err := b.Put([]byte(evaluation.wallSlot), []byte{1}); err != nil {
			return Occurrence{}, err
		}
	}
	o := evaluation.due
	if o.ID == "" || tx.Bucket([]byte(runsBucket)).Get([]byte(o.ID)) != nil {
		return Occurrence{}, nil
	}
	age := o.ClaimedAt.Sub(o.DueAt)
	switch {
	case skipReason != "":
		o.State, o.Detail = OccurrenceMissed, skipReason
	case (o.Spec.Grace == 0 && age >= time.Minute) || (o.Spec.Grace > 0 && age > o.Spec.Grace):
		o.State, o.Detail = OccurrenceMissed, "latest trigger is outside the catch-up window"
	default:
		if err := checkOverlap(tx, &o); err != nil {
			return Occurrence{}, err
		}
	}
	return o, putOccurrence(tx, o, true)
}

// ClaimManual makes a distinct occurrence without moving the scheduler's
// evaluation cursor. Manual runs are allowed when a job's calendar is paused.
func (s *Store) ClaimManual(jobID string, now time.Time) (Occurrence, error) {
	return s.ClaimManualWithID(jobID, NewID(now), now)
}

// ClaimManualWithID returns the same occurrence when a request is retried
// with the same invocation ID. An invocation ID is bound to one job.
func (s *Store) ClaimManualWithID(jobID, invocationID string, now time.Time) (Occurrence, error) {
	if !validULID(invocationID) {
		return Occurrence{}, fmt.Errorf("invalid schedule invocation ID")
	}
	now = now.UTC()
	var result Occurrence
	err := s.update(func(tx *bolt.Tx) error {
		existing, err := record[Occurrence](tx, runsBucket, invocationID)
		if err == nil {
			if !existing.Manual || existing.JobID != jobID {
				return ErrConflict
			}
			result = existing
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		job, err := record[Job](tx, jobsBucket, jobID)
		if err != nil {
			return err
		}
		if job.State == JobArchived {
			return ErrArchived
		}
		due := now
		if job.Spec.Calendar.TimeZone == HostTimeZone {
			loc, _, err := s.hostZone()
			if err != nil {
				return err
			}
			due = now.In(loc)
		}
		result, err = newOccurrence(job, due, now, invocationID)
		if err != nil {
			return err
		}
		if err := checkOverlap(tx, &result); err != nil {
			return err
		}
		return putOccurrence(tx, result, true)
	})
	if err != nil {
		return Occurrence{}, err
	}
	return result, nil
}

func newOccurrence(job Job, due, now time.Time, manualID string) (Occurrence, error) {
	id := scheduledID(job.ID, due)
	if manualID != "" {
		id = manualID
	}
	o := Occurrence{
		ID: id, JobID: job.ID, RevisionID: job.RevisionID, Spec: job.Spec,
		DueAt: due, ClaimedAt: now, Manual: manualID != "", State: OccurrenceQueued,
	}
	var err error
	o.RequestHash, err = requestHash(o)
	return o, err
}

func checkOverlap(tx *bolt.Tx, o *Occurrence) error {
	var conflicting string
	err := eachActive(tx, func(other Occurrence) error {
		if !other.reservesWorkspace() {
			return nil
		}
		// Opted-in runs can isolate at execution time. Keep their claim
		// queued so a restart recovers the pending workspace decision.
		sameJobConflict := other.JobID == o.JobID && (!o.Spec.AllowOverlap || !other.Spec.AllowOverlap)
		directConflict := other.BlocksSelectedDirectory(o.Spec)
		if (sameJobConflict || directConflict) && (conflicting == "" || other.ID < conflicting) {
			conflicting = other.ID
		}
		return nil
	})
	if err != nil {
		return err
	}
	if conflicting != "" {
		o.State, o.Detail = OccurrenceBlocked, "overlap with active occurrence "+conflicting
	}
	return nil
}

// BlocksSelectedDirectory checks the same checkout reservation used before
// a new occurrence has an assigned execution workspace.
func (o Occurrence) BlocksSelectedDirectory(next JobSpec) bool {
	return !next.AllowOverlap && o.reservesWorkspace() && o.Spec.Directory != "" &&
		(next.Workspace == WorkspaceDirectory || o.Spec.Workspace == WorkspaceDirectory) &&
		pathsOverlap(next.Directory, o.Spec.Directory)
}

func pathsOverlap(a, b string) bool {
	return withinDirectory(a, b) || withinDirectory(b, a)
}

func withinDirectory(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (o Occurrence) reservesWorkspace() bool {
	switch o.State {
	case OccurrenceQueued, OccurrenceWorking, OccurrenceNeedsInput:
		return true
	case OccurrenceAttention:
		return len(o.Attempts) > 0 && o.Attempts[len(o.Attempts)-1].State == AttemptUnknown
	case OccurrenceReady:
		return o.ReleasedAt.IsZero()
	}
	return false
}

func putOccurrence(tx *bolt.Tx, o Occurrence, isNew bool) error {
	if err := putRecord(tx, runsBucket, o.ID, o); err != nil {
		return err
	}
	if isNew {
		if err := tx.Bucket([]byte(indexBucket)).Put([]byte(o.JobID+"/"+o.ID), []byte(o.ID)); err != nil {
			return err
		}
	}
	active := tx.Bucket([]byte(activeBucket))
	retryWorkspace := o.State == OccurrenceBlocked && o.Spec.AllowOverlap && len(o.Attempts) > 0 && o.Attempts[len(o.Attempts)-1].State == AttemptDeferred
	if o.reservesWorkspace() || retryWorkspace || (o.State == OccurrenceReady && o.ProjectedAt.IsZero()) {
		return active.Put([]byte(o.ID), []byte{1})
	}
	return active.Delete([]byte(o.ID))
}

func eachActive(tx *bolt.Tx, fn func(Occurrence) error) error {
	return tx.Bucket([]byte(activeBucket)).ForEach(func(id, _ []byte) error {
		o, err := record[Occurrence](tx, runsBucket, string(id))
		if err != nil {
			return err
		}
		return fn(o)
	})
}

// ActiveOccurrences excludes terminal history. A completed run remains
// active until its receipt is projected and its workspace is released.
func (s *Store) ActiveOccurrences() ([]Occurrence, error) {
	var list []Occurrence
	err := s.view(func(tx *bolt.Tx) error {
		return eachActive(tx, func(o Occurrence) error {
			list = append(list, o)
			return nil
		})
	})
	slices.SortFunc(list, func(a, b Occurrence) int {
		return cmp.Or(a.DueAt.Compare(b.DueAt), cmp.Compare(a.ID, b.ID))
	})
	return list, err
}

func (s *Store) Occurrences() ([]Occurrence, error) {
	return s.History("")
}

func (s *Store) History(jobID string) ([]Occurrence, error) {
	var list []Occurrence
	err := s.view(func(tx *bolt.Tx) error {
		if jobID == "" {
			return eachRecord(tx, runsBucket, func(o Occurrence) error {
				list = append(list, o)
				return nil
			})
		}
		if tx.Bucket([]byte(jobsBucket)).Get([]byte(jobID)) == nil {
			return ErrNotFound
		}
		prefix := []byte(jobID + "/")
		cursor := tx.Bucket([]byte(indexBucket)).Cursor()
		for key, id := cursor.Seek(prefix); bytes.HasPrefix(key, prefix); key, id = cursor.Next() {
			o, err := record[Occurrence](tx, runsBucket, string(id))
			if err != nil {
				return err
			}
			list = append(list, o)
		}
		return nil
	})
	slices.SortFunc(list, func(a, b Occurrence) int {
		return cmp.Or(b.DueAt.Compare(a.DueAt), cmp.Compare(a.ID, b.ID))
	})
	return list, err
}

func (s *Store) Occurrence(id string) (Occurrence, error) {
	return get[Occurrence](s, runsBucket, id)
}

// UpdateOccurrence changes execution metadata in a transaction. The callback
// runs under the store lock and must only examine and change the loaded record.
func (s *Store) UpdateOccurrence(id string, fn func(*Occurrence) error) (Occurrence, error) {
	var result Occurrence
	err := s.update(func(tx *bolt.Tx) error {
		before, err := record[Occurrence](tx, runsBucket, id)
		if err != nil {
			return err
		}
		o := before
		o.Spec.Agents = slices.Clone(before.Spec.Agents)
		o.Attempts = slices.Clone(before.Attempts)
		if before.Completion != nil {
			completion := *before.Completion
			completion.Links = slices.Clone(completion.Links)
			o.Completion = &completion
		}
		if err := fn(&o); err != nil {
			return err
		}
		hash, err := requestHash(o)
		if err != nil {
			return err
		}
		if hash != before.RequestHash || o.RequestHash != before.RequestHash {
			return fmt.Errorf("schedule occurrence inputs are immutable")
		}
		if !executionIdentitiesPreserved(before, o) {
			return fmt.Errorf("schedule execution identities are immutable")
		}
		result = o
		if reflect.DeepEqual(before, o) {
			return nil
		}
		return putOccurrence(tx, o, false)
	})
	if err != nil {
		return Occurrence{}, err
	}
	return result, nil
}

func executionIdentitiesPreserved(before, after Occurrence) bool {
	if len(before.Attempts) > len(after.Attempts) ||
		!writeOnce(before.Workspace, after.Workspace) ||
		!writeOnce(before.SourceDirectory, after.SourceDirectory) ||
		!writeTimeOnce(before.CompletedAt, after.CompletedAt) ||
		!writeTimeOnce(before.ProjectedAt, after.ProjectedAt) ||
		!writeTimeOnce(before.ReleasedAt, after.ReleasedAt) {
		return false
	}
	seen := make(map[string]bool, len(after.Attempts))
	for i, attempt := range after.Attempts {
		if !validULID(attempt.ID) || seen[attempt.ID] {
			return false
		}
		seen[attempt.ID] = true
		if i >= len(before.Attempts) {
			continue
		}
		prev := before.Attempts[i]
		if prev.ID != attempt.ID || prev.Agent != attempt.Agent ||
			!writeOnce(prev.OperationID, attempt.OperationID) ||
			!writeOnce(prev.CompletionTokenHash, attempt.CompletionTokenHash) ||
			!writeOnce(prev.SessionID, attempt.SessionID) ||
			!writeOnce(prev.Workspace, attempt.Workspace) ||
			!prev.StartedAt.Equal(attempt.StartedAt) ||
			(prev.DispatchStarted && !attempt.DispatchStarted) ||
			(prev.PromptStarted && !attempt.PromptStarted) || (prev.PromptDelivered && !attempt.PromptDelivered) {
			return false
		}
	}
	return before.Completion == nil || reflect.DeepEqual(before.Completion, after.Completion)
}

func writeTimeOnce(old, new time.Time) bool {
	return old.IsZero() || old.Equal(new)
}

func writeOnce(old, new string) bool {
	return old == "" || old == new
}
