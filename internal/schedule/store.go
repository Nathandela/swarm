package schedule

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	bolt "go.etcd.io/bbolt"
	"golang.org/x/sys/unix"
)

const (
	stateFile    = "state.db"
	jobsBucket   = "jobs"
	revsBucket   = "revisions"
	evalBucket   = "evaluated"
	runsBucket   = "occurrences"
	indexBucket  = "job_occurrences"
	activeBucket = "active_occurrences"
	tmplBucket   = "templates"
)

type Store struct {
	dir string
}

// Open opens the private automation catalog under the daemon's state directory.
// Metadata and the corresponding indexes commit in the same durable transaction.
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("schedule state path is required")
	}
	dir, err := filepath.Abs(filepath.Join(path, "schedules"))
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("schedule state directory must be private: %s", dir)
	}
	s := &Store{dir: dir}
	err = s.database(true, func(db *bolt.DB) error {
		if err := db.View(func(tx *bolt.Tx) error {
			if tx.Bucket([]byte(tmplBucket)) == nil {
				return ErrNotFound
			}
			return nil
		}); !errors.Is(err, ErrNotFound) {
			return err
		}
		return db.Update(func(tx *bolt.Tx) error {
			for _, name := range []string{jobsBucket, revsBucket, evalBucket, runsBucket, indexBucket, activeBucket, tmplBucket} {
				if _, err := tx.CreateBucketIfNotExists([]byte(name)); err != nil {
					return err
				}
			}
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) database(write bool, fn func(*bolt.DB) error) error {
	// Use a per-user lock so local contenders block in the kernel instead
	// of polling for bbolt's lock. Closing after each transaction also lets
	// the daemon, CLI and TUI share the catalog across processes.
	lockPath := filepath.Join(s.dir, "state.lock")
	fd, err := unix.Open(lockPath, unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	lock := os.NewFile(uintptr(fd), lockPath)
	defer func() { _ = lock.Close() }()
	mode := unix.LOCK_SH
	if write {
		mode = unix.LOCK_EX
	}
	for {
		err = unix.Flock(fd, mode)
		if !errors.Is(err, unix.EINTR) {
			break
		}
	}
	if err != nil {
		return err
	}
	path := filepath.Join(s.dir, stateFile)
	info, err := os.Lstat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if info != nil && (!info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0) {
		return fmt.Errorf("schedule state file must be private: %s", path)
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{ReadOnly: !write})
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	return fn(db)
}

func (s *Store) view(fn func(*bolt.Tx) error) error {
	return s.database(false, func(db *bolt.DB) error { return db.View(fn) })
}

func (s *Store) update(fn func(*bolt.Tx) error) error {
	return s.database(true, func(db *bolt.DB) error { return db.Update(fn) })
}

func record[T any](tx *bolt.Tx, bucket, id string) (T, error) {
	var result T
	data := tx.Bucket([]byte(bucket)).Get([]byte(id))
	if data == nil {
		return result, ErrNotFound
	}
	err := json.Unmarshal(data, &result)
	return result, err
}

func putRecord[T any](tx *bolt.Tx, bucket, id string, record T) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return tx.Bucket([]byte(bucket)).Put([]byte(id), data)
}

func eachRecord[T any](tx *bolt.Tx, bucket string, fn func(T) error) error {
	return tx.Bucket([]byte(bucket)).ForEach(func(_, data []byte) error {
		var item T
		if err := json.Unmarshal(data, &item); err != nil {
			return err
		}
		return fn(item)
	})
}

func (s *Store) CreateJob(spec JobSpec, now time.Time) (Job, error) {
	spec, err := PrepareJob(spec)
	if err != nil {
		return Job{}, err
	}
	now = now.UTC()
	if err := validateFuture(spec.Calendar, now); err != nil {
		return Job{}, err
	}
	job := Job{ID: NewID(now), RevisionID: NewID(now), Spec: spec,
		State: JobActive, CreatedAt: now, UpdatedAt: now}
	err = s.update(func(tx *bolt.Tx) error { return setRevision(tx, job, now) })
	return job, err
}

func (s *Store) EditJob(id string, spec JobSpec, now time.Time) (Job, error) {
	return s.EditJobRevision(id, "", spec, now)
}

// EditJobRevision applies an edit if expectedRevision still identifies the
// current definition. An empty expected revision applies it unconditionally.
func (s *Store) EditJobRevision(id, expectedRevision string, spec JobSpec, now time.Time) (Job, error) {
	spec, err := PrepareJob(spec)
	if err != nil {
		return Job{}, err
	}
	now = now.UTC()
	var result Job
	err = s.update(func(tx *bolt.Tx) error {
		job, err := record[Job](tx, jobsBucket, id)
		if err != nil {
			return err
		}
		if job.State == JobArchived {
			return ErrArchived
		}
		if expectedRevision != "" && job.RevisionID != expectedRevision {
			return ErrConflict
		}
		if spec.Calendar != job.Spec.Calendar {
			if err := validateFuture(spec.Calendar, now); err != nil {
				return err
			}
		}
		old, err := evaluateJob(tx, job, now.Add(-time.Nanosecond), now)
		if err != nil {
			return err
		}
		if _, err := saveEvaluation(tx, old, ""); err != nil {
			return err
		}
		job.Spec, job.RevisionID, job.UpdatedAt = spec, NewID(now), now
		result = job
		return setRevision(tx, job, now)
	})
	if err != nil {
		return Job{}, err
	}
	return result, nil
}

func validateFuture(calendar Calendar, now time.Time) error {
	next, err := calendar.Next(now.Add(-time.Nanosecond))
	if err != nil {
		return err
	}
	if next.IsZero() {
		return fmt.Errorf("schedule has no future trigger")
	}
	return nil
}

func setRevision(tx *bolt.Tx, job Job, now time.Time) error {
	if err := putRecord(tx, jobsBucket, job.ID, job); err != nil {
		return err
	}
	if err := putRecord(tx, revsBucket, job.RevisionID, Revision{
		ID: job.RevisionID, JobID: job.ID, Spec: job.Spec, CreatedAt: now,
	}); err != nil {
		return err
	}
	return putRecord(tx, evalBucket, job.ID, now.Add(-time.Nanosecond))
}

func (s *Store) SetJobState(id string, next JobState, now time.Time) (Job, error) {
	switch next {
	case JobActive, JobPaused, JobArchived:
	default:
		return Job{}, fmt.Errorf("unknown job state %q", next)
	}
	var result Job
	err := s.update(func(tx *bolt.Tx) error {
		job, err := record[Job](tx, jobsBucket, id)
		if err != nil {
			return err
		}
		if job.State == next {
			result = job
			return nil
		}
		if job.State == JobArchived {
			return ErrArchived
		}
		now = now.UTC()
		old, err := evaluateJob(tx, job, now.Add(-time.Nanosecond), now)
		if err != nil {
			return err
		}
		if _, err := saveEvaluation(tx, old, "job was "+string(next)+" before this trigger was claimed"); err != nil {
			return err
		}
		job.State, job.UpdatedAt = next, now
		result = job
		if err := putRecord(tx, jobsBucket, id, job); err != nil {
			return err
		}
		return putRecord(tx, evalBucket, id, now.Add(-time.Nanosecond))
	})
	if err != nil {
		return Job{}, err
	}
	return result, nil
}

func (s *Store) Jobs() ([]Job, error) {
	var list []Job
	err := s.view(func(tx *bolt.Tx) error {
		return eachRecord(tx, jobsBucket, func(job Job) error {
			list = append(list, job)
			return nil
		})
	})
	slices.SortFunc(list, func(a, b Job) int {
		return cmp.Or(cmp.Compare(a.Spec.Name, b.Spec.Name), cmp.Compare(a.ID, b.ID))
	})
	return list, err
}

func (s *Store) Job(id string) (Job, error) {
	return get[Job](s, jobsBucket, id)
}

func (s *Store) Revision(id string) (Revision, error) {
	return get[Revision](s, revsBucket, id)
}

func get[T any](s *Store, bucket, id string) (T, error) {
	var item T
	err := s.view(func(tx *bolt.Tx) error {
		var err error
		item, err = record[T](tx, bucket, id)
		return err
	})
	return item, err
}
