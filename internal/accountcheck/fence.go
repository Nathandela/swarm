package accountcheck

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"syscall"
	"time"

	"github.com/Nathandela/swarm/internal/accounts"
)

// WithCredentialFence shares the native worker's generation lock. The caller
// supplies native-writer proof before installing ordinary configuration; the
// lock excludes auth/status, refresh and availability workers through install.
func WithCredentialFence(stateRoot string, binding accounts.Binding, install func() error) error {
	root, err := openChecks(stateRoot)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	lock, err := acquireGenerationLock(root, binding)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	if !collectionStateSafe(stateRoot) || !writersStopped(root, binding) {
		return ErrCustodyUnknown
	}
	return install()
}

func acquireGenerationLock(root *os.Root, binding accounts.Binding) (*os.File, error) {
	return acquireNamedLock(root, generationLockName(binding), 0)
}

func acquireNamedLock(root *os.Root, name string, wait time.Duration) (*os.File, error) {
	before, err := root.Lstat(name)
	if err != nil && !errors.Is(err, os.ErrNotExist) || err == nil && !validLockInfo(before) {
		return nil, ErrUnavailable
	}
	lock, err := root.OpenFile(name, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, ErrUnavailable
	}
	closeError := func() (*os.File, error) { _ = lock.Close(); return nil, ErrUnavailable }
	after, err := lock.Stat()
	if err != nil || !validLockInfo(after) {
		return closeError()
	}
	current, err := root.Lstat(name)
	if err != nil || !validLockInfo(current) || !os.SameFile(current, after) || before != nil && !os.SameFile(before, after) {
		return closeError()
	}
	deadline := time.Now().Add(wait)
	for {
		err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) || wait == 0 || !time.Now().Before(deadline) {
			return closeError()
		}
		time.Sleep(20 * time.Millisecond)
	}
	return lock, nil
}

// Configuration reuse is independent from the native credential worker lock.
// Global cohort replacement takes config -> credential locks in that order.
func WithConfigurationFence(stateRoot string, binding accounts.Binding, install func() error) error {
	root, err := openChecks(stateRoot)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	key := sha256.Sum256([]byte("configuration:" + generationLockName(binding)))
	lock, err := acquireNamedLock(root, ".lock-"+hex.EncodeToString(key[:]), 2*time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	return install()
}
