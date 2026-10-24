package diagnosticlog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// ErrUnsafe marks a diagnostic log that fails the ownership or identity checks.
var ErrUnsafe = errors.New("diagnostic log: unsafe target")

// CleanResult describes the existing log descriptor before and after inspection.
type CleanResult struct {
	Before        os.FileInfo
	After         os.FileInfo
	EligibleBytes int64
	Changed       bool
}

// CleanExisting inspects an existing diagnostic log, truncating it in place only
// when apply is true and its logical size exceeds MaxBytes.
func CleanExisting(path string, apply bool) (CleanResult, error) {
	var result CleanResult
	if filepath.Clean(path) != path {
		return result, fmt.Errorf("%w: non-canonical path %q", ErrUnsafe, path)
	}
	parent := filepath.Dir(path)
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		return result, err
	}
	if err := privateParent(path); err != nil {
		return result, err
	}
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return result, err
	}
	if !pathInfo.Mode().IsRegular() {
		return result, fmt.Errorf("%w: non-regular path %q", ErrUnsafe, path)
	}
	flags := syscall.O_RDONLY
	if apply {
		flags = syscall.O_WRONLY | syscall.O_APPEND
	}
	fd, err := syscall.Open(path, flags|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return result, fmt.Errorf("%w: %q", ErrUnsafe, path)
		}
		return result, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer func() { _ = f.Close() }()
	if err := validateCleanTarget(path, f, parentInfo); err != nil {
		return result, err
	}
	result.Before, err = f.Stat()
	if err != nil {
		return result, err
	}
	if result.Before.Size() > MaxBytes {
		result.EligibleBytes = result.Before.Size()
	}
	if apply && result.EligibleBytes > 0 {
		if err := validateCleanTarget(path, f, parentInfo); err != nil {
			return result, err
		}
		current, err := f.Stat()
		if err != nil {
			return result, err
		}
		result.EligibleBytes = 0
		if current.Size() > MaxBytes {
			result.EligibleBytes = current.Size()
			if err := f.Truncate(0); err != nil {
				_ = validateCleanAfter(path, f, parentInfo, &result)
				return result, err
			}
			result.Changed = true
		}
	}
	if err := validateCleanAfter(path, f, parentInfo, &result); err != nil {
		return result, err
	}
	return result, nil
}

func validateCleanTarget(path string, f *os.File, parentBefore os.FileInfo) error {
	parentNow, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: parent changed for %q", ErrUnsafe, path)
		}
		return err
	}
	if !os.SameFile(parentBefore, parentNow) {
		return fmt.Errorf("%w: parent changed for %q", ErrUnsafe, path)
	}
	if err := validate(path, f); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: path changed for %q", ErrUnsafe, path)
		}
		return err
	}
	return nil
}

func validateCleanAfter(path string, f *os.File, parent os.FileInfo, result *CleanResult) error {
	if err := validateCleanTarget(path, f, parent); err != nil {
		return err
	}
	after, err := f.Stat()
	if err != nil {
		return err
	}
	result.After = after
	return nil
}
