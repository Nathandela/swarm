package shim

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/Nathandela/swarm/internal/procstart"
)

// ErrHookProducerBusy means a producer still owns the spool. Retirement never waits.
var ErrHookProducerBusy = errors.New("shim: hook producer still owns spool")
var ErrHookRetirementUnproven = errors.New("shim: hook spool retirement proof unavailable")

// The nonce binds cursor certificates to a cooperating producer; device/inode bind
// that authority to physical bytes, even if an incarnation sidecar was left behind.
type hookProducerProof struct {
	Incarnation string `json:"incarnation"`
	Nonce       string `json:"nonce"`
	Device      uint64 `json:"device"`
	Inode       uint64 `json:"inode"`
	PID         int    `json:"pid"`
	Start       int64  `json:"start"`
	Closed      bool   `json:"closed,omitempty"`
}

func openHookOwnedFile(path string, flags int) (*os.File, error) {
	f, err := os.OpenFile(path, flags|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || st.Uid != uint32(os.Getuid()) || st.Nlink != 1 {
		_ = f.Close()
		return nil, ErrHookRetirementUnproven
	}
	return f, nil
}

func readHookOwnedSidecar(path string, limit int64) ([]byte, error) {
	f, err := openHookOwnedFile(path, os.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err == nil && int64(len(data)) > limit {
		return nil, ErrHookRetirementUnproven
	}
	return data, err
}

func hookProducerLock(path string, create bool) (*os.File, error) {
	flags := os.O_RDWR
	if create {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		flags |= os.O_CREATE
	}
	f, err := openHookOwnedFile(path+".producer.lock", flags)
	if err != nil {
		return nil, err
	}
	if create {
		if err := f.Chmod(0o600); err != nil {
			_ = f.Close()
			return nil, err
		}
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrHookProducerBusy
		}
		return nil, err
	}
	return f, nil
}

// OpenHookSpoolProducer is the sole production writer entry point. Its lock lives
// until Close, including while Compact swaps the data inode.
func OpenHookSpoolProducer(path string, maxBytes int) (*HookSpool, error) {
	lock, err := hookProducerLock(path, true)
	if err != nil {
		return nil, err
	}
	s, err := OpenHookSpool(path, maxBytes)
	if err != nil {
		_ = lock.Close()
		return nil, err
	}
	s.producerLock = lock
	if s.created {
		nonce, err := mintHookSpoolIncarnation()
		if err != nil {
			_ = s.Close()
			return nil, err
		}
		start, err := procstart.StartTime(os.Getpid())
		if err != nil {
			_ = s.Close()
			return nil, err
		}
		s.producerProof = &hookProducerProof{Incarnation: s.incarnation, Nonce: nonce, PID: os.Getpid(), Start: start}
	} else if proof, err := readHookProducerProof(path); err == nil && validHookProducerProof(path, s.f, proof) == nil {
		start, err := procstart.StartTime(os.Getpid())
		if err != nil {
			_ = s.Close()
			return nil, err
		}
		proof.PID, proof.Start, proof.Closed = os.Getpid(), start, false
		s.producerProof = &proof
	}
	// Existing unknown/legacy spools stay readable but never acquire cleanup authority.
	if s.producerProof != nil {
		if err := s.persistProducerProof(); err != nil {
			s.producerProof = nil
			_ = s.Close()
			return nil, err
		}
	}
	return s, nil
}

func readHookProducerProof(path string) (hookProducerProof, error) {
	var p hookProducerProof
	raw, err := readHookOwnedSidecar(path+".producer", 2048)
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return p, err
	}
	if p.Nonce == "" || p.Incarnation == "" || p.PID <= 0 || p.Start <= 0 {
		return p, ErrHookRetirementUnproven
	}
	return p, nil
}

func validHookProducerProof(path string, f *os.File, p hookProducerProof) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	current, err := os.Lstat(path)
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || st.Uid != uint32(os.Getuid()) || st.Nlink != 1 || !os.SameFile(info, current) || !current.Mode().IsRegular() || uint64(st.Dev) != p.Device || st.Ino != p.Inode {
		return ErrHookRetirementUnproven
	}
	id, legacy, err := ReadHookSpoolIncarnation(path)
	if err != nil {
		return err
	}
	if legacy || id != p.Incarnation {
		return ErrHookRetirementUnproven
	}
	return nil
}

var persistHookProducerProof = func(path string, data []byte) error {
	if err := writeFileAtomic(filepath.Dir(path), filepath.Base(path)+".producer", data); err != nil {
		return err
	}
	return fsyncDir(filepath.Dir(path))
}

func (s *HookSpool) persistProducerProof() error {
	info, err := s.f.Stat()
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ErrHookRetirementUnproven
	}
	s.producerProof.Device, s.producerProof.Inode = uint64(st.Dev), st.Ino
	data, err := json.Marshal(s.producerProof)
	if err != nil {
		return err
	}
	return persistHookProducerProof(s.path, data)
}

// ProducerCertificate returns authority only while this handle still names the
// capability's physical spool. It does not certify any numeric fold cursor.
func (s *HookSpool) ProducerCertificate() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.producerProof == nil || validHookProducerProof(s.path, s.f, *s.producerProof) != nil {
		return ""
	}
	return s.producerProof.Nonce
}

var testHookBeforeStoppedSpoolOpen func()

// CompactStoppedHookSpool holds the producer lock throughout validation and
// replacement. A closed producer or exact recorded dead process is required.
func CompactStoppedHookSpool(path, incarnation, certificate string, seq uint64, pid int, start int64) error {
	lock, err := hookProducerLock(path, false)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	proof, err := readHookProducerProof(path)
	if err != nil {
		return ErrHookRetirementUnproven
	}
	if seq == 0 || proof.Incarnation != incarnation || proof.Nonce != certificate {
		return ErrHookRetirementUnproven
	}
	if !proof.Closed {
		if pid != proof.PID || start != proof.Start {
			return ErrHookRetirementUnproven
		}
		observed, err := procstart.StartTime(pid)
		if err == nil {
			if observed == start {
				return ErrHookProducerBusy
			}
		} else if !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return ErrHookRetirementUnproven
		}
	}
	f, err := openHookOwnedFile(path, os.O_RDONLY)
	if err != nil {
		return err
	}
	err = validHookProducerProof(path, f, proof)
	_ = f.Close()
	if err != nil {
		return err
	}
	if _, err := readHookOwnedSidecar(hookFloorPath(path), 64); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if testHookBeforeStoppedSpoolOpen != nil {
		testHookBeforeStoppedSpoolOpen()
	}
	s, err := openHookSpool(path, 0, true)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()
	if err := validHookProducerProof(path, s.f, proof); err != nil {
		return err
	}
	s.producerProof = &proof
	if err := s.Compact(seq); err != nil {
		return fmt.Errorf("shim: compact stopped hook spool: %w", err)
	}
	return nil
}
