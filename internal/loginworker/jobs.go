package loginworker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/Nathandela/swarm/internal/processcontain"
)

const receiptFile = "completion.json"

// ReadConfig reads durable presentation even after the worker's deadline or a
// native CLI upgrade. Execution always uses the stricter validateConfig path.
func ReadConfig(stateRoot, jobID string) (Config, error) {
	f, err := openJob(stateRoot, jobID, false)
	if err != nil {
		return Config{}, err
	}
	defer f.Close()
	raw, err := readPrivate(f.root, ConfigFile, 16<<10)
	if err != nil {
		return Config{}, err
	}
	var c Config
	if json.Unmarshal(raw, &c) != nil || c.SchemaVersion != SchemaVersion || c.StateRoot != stateRoot || c.JobID != jobID || c.Generation == 0 || !validLabel(c.Label) || (c.LoginID != "" && !validID(c.LoginID)) || (c.Provider != "claude" && c.Provider != "codex") {
		return Config{}, ErrInvalid
	}
	return c, nil
}

func Jobs(stateRoot string) ([]Config, error) {
	path := filepath.Join(stateRoot, "logins", "enrollment")
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if _, err := safeAbsolute(path, true, true); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	var jobs []Config
	for _, entry := range entries {
		c, err := ReadConfig(stateRoot, entry.Name())
		// A crash before config publication cannot have executed a worker. Keep
		// any unrecognized bytes untouched; it must not disable unrelated jobs.
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, c)
	}
	return jobs, nil
}

// Stopped verifies live process inventory again; a progress flag is not custody.
func Stopped(c Config) error {
	p, err := ReadProgress(c.StateRoot, c.JobID)
	if err != nil {
		return err
	}
	if p.Generation != c.Generation || !p.WritersStopped || !p.NativeWritersStopped || p.Worker.Alive() || p.Runner.Alive() {
		return ErrUnavailable
	}
	for _, child := range p.Children {
		if child.Alive() {
			return ErrUnavailable
		}
	}
	writers, err := processcontain.EnvironmentWriters([]string{NativeHome(c.StateRoot, c.JobID)}, []string{c.JobID})
	if err != nil || len(writers) != 0 {
		return ErrUnavailable
	}
	return nil
}

// CancelJob fences first; the supervisor must drain its native children before
// the daemon can consume or erase the temporary credentials.
func CancelJob(ctx context.Context, c Config) error {
	if err := FenceCancel(c.StateRoot, c.JobID, c.Generation); err != nil {
		return err
	}
	for {
		p, err := ReadProgress(c.StateRoot, c.JobID)
		if err == nil {
			if _, err := Cancel(ctx, Ref{StateRoot: c.StateRoot, JobID: c.JobID, Generation: c.Generation, Worker: p.Worker}); err != nil {
				return err
			}
			if err := Stopped(c); err == nil {
				return nil
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		} else {
			// The supervisor must publish progress before starting its runner.
			// With cancellation fenced and no tagged process, no writer can start.
			writers, err := processcontain.EnvironmentWriters([]string{NativeHome(c.StateRoot, c.JobID)}, []string{c.JobID})
			if err != nil {
				return err
			}
			if len(writers) == 0 {
				f, err := openJob(c.StateRoot, c.JobID, false)
				if err != nil {
					return err
				}
				defer f.Close()
				return f.write(ProgressFile, Progress{SchemaVersion: SchemaVersion, JobID: c.JobID, Generation: c.Generation, Phase: PhaseCanceled, WritersStopped: true, NativeWritersStopped: true, UpdatedAt: time.Now().UTC()})
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// Reject records an actionable enrollment result after all native writers stop.
func Reject(c Config, code string) error {
	if code != "wrong_login" && code != "duplicate_login" {
		return ErrInvalid
	}
	if err := Stopped(c); err != nil {
		return err
	}
	p, err := ReadProgress(c.StateRoot, c.JobID)
	if err != nil {
		return err
	}
	if p.Phase != PhaseReady {
		return ErrInvalid
	}
	p.Phase, p.ErrorCode, p.UpdatedAt = PhaseFailed, code, time.Now().UTC()
	f, err := openJob(c.StateRoot, c.JobID, false)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.write(ProgressFile, p)
}

func Completed(c Config) (bool, error) {
	return hasReceipt(c, receiptFile)
}

func Cleaned(c Config) (bool, error) {
	return hasReceipt(c, "cleaned.json")
}

func hasReceipt(c Config, name string) (bool, error) {
	f, err := openJob(c.StateRoot, c.JobID, false)
	if err != nil {
		return false, err
	}
	defer f.Close()
	raw, err := readPrivate(f.root, name, 128)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var receipt struct {
		JobID string `json:"job_id"`
	}
	if json.Unmarshal(raw, &receipt) != nil || receipt.JobID != c.JobID {
		return false, ErrInvalid
	}
	return true, nil
}

func MarkCompleted(c Config) error {
	return writeReceipt(c, receiptFile)
}

func MarkCleaned(c Config) error {
	return writeReceipt(c, "cleaned.json")
}

func writeReceipt(c Config, name string) error {
	if err := Stopped(c); err != nil {
		return err
	}
	f, err := openJob(c.StateRoot, c.JobID, false)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.write(name, struct {
		JobID string `json:"job_id"`
	}{c.JobID})
}

// Prune removes only a terminal, credential-cleaned private enrollment after a
// day. Native discussion history never lives in this temporary directory.
func Prune(c Config) error {
	if time.Since(c.Deadline) < 24*time.Hour {
		return nil
	}
	cleaned, err := Cleaned(c)
	if err != nil || !cleaned {
		return err
	}
	if err := Stopped(c); err != nil {
		return err
	}
	p, err := ReadProgress(c.StateRoot, c.JobID)
	if err != nil {
		return err
	}
	if p.Phase != PhaseFailed && p.Phase != PhaseCanceled {
		done, err := Completed(c)
		if err != nil || !done {
			return err
		}
	}
	parentPath := filepath.Join(c.StateRoot, "logins", "enrollment")
	if _, err := safeAbsolute(parentPath, true, true); err != nil {
		return err
	}
	parent, err := os.OpenRoot(parentPath)
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	info, err := parent.Lstat(c.JobID)
	if err != nil || !privateInfo(info, true) {
		return ErrUnsafe
	}
	if err := parent.RemoveAll(c.JobID); err != nil {
		return err
	}
	dir, err := parent.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}
