// Package diagnosticlog retains disposable diagnostics without replacing live log inodes.
package diagnosticlog

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

const (
	MaxBytes = 8 << 20
	Interval = 5 * time.Second
)

// Open rejects unsafe targets before changing them, and hardens the owned descriptor.
// Retention is best effort: bursts may exceed MaxBytes until the next check.
func Open(path string) (*os.File, error) {
	if err := privateParent(path); err != nil {
		return nil, err
	}
	fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_APPEND|syscall.O_CREAT|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	if err := validate(path, f); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := trim(path, f); err != nil {
		report(err)
	}
	return f, nil
}

func privateParent(path string) error {
	info, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0022 != 0 {
		return fmt.Errorf("diagnostic log: unsafe parent directory %q", filepath.Dir(path))
	}
	return nil
}

func validate(path string, f *os.File) error {
	if err := privateParent(path); err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		return fmt.Errorf("diagnostic log: unsafe descriptor for %q", path)
	}
	linked, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !linked.Mode().IsRegular() || !os.SameFile(info, linked) {
		return fmt.Errorf("diagnostic log: descriptor does not match %q", path)
	}
	return nil
}

func trim(path string, f *os.File) error {
	if err := validate(path, f); err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() > MaxBytes {
		return f.Truncate(0)
	}
	return nil
}

func report(err error) { fmt.Fprintf(os.Stderr, "diagnostic log retention: %v\n", err) }

// Start maintains only the descriptor matching the configured owned path. The
// caller retains descriptor custody and must stop/join maintenance before closing it.
func Start(path string, f *os.File) (func(), error) {
	return start(path, f, Interval, report)
}

func start(path string, f *os.File, interval time.Duration, report func(error)) (func(), error) {
	if err := validate(path, f); err != nil {
		return nil, err
	}
	if err := f.Chmod(0600); err != nil {
		return nil, err
	}
	check := func() {
		if err := trim(path, f); err != nil {
			report(err)
		}
	}
	check()
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				check()
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(stop) }); <-done }, nil
}
