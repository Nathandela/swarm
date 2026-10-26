package shim

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return nil, ErrHookRetirementUnproven
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	f, err := os.OpenFile(path, flags|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	if errors.Is(err, syscall.ELOOP) {
		return nil, ErrHookRetirementUnproven
	}
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

// HookCleanupResult describes observations of the proof-bound spool, not free space.
type HookCleanupResult struct {
	Before, After os.FileInfo
	EligibleBytes int64
	Changed       bool
}

// ErrHookCleanupTooLarge retains spools beyond the bounded manual cleanup workload.
var ErrHookCleanupTooLarge = errors.New("shim: hook cleanup spool exceeds 64 MiB")

const hookCleanupMaxBytes = 64 << 20

func hookRetirementReadError(err error) error {
	if errors.Is(err, ErrHookRetirementUnproven) {
		return err
	}
	var syntax *json.SyntaxError
	var shape *json.UnmarshalTypeError
	if errors.Is(err, os.ErrNotExist) || errors.As(err, &syntax) || errors.As(err, &shape) {
		return ErrHookRetirementUnproven
	}
	return err
}

func privateHookParent(path string) (os.FileInfo, error) {
	info, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || st.Uid != uint32(os.Getuid()) || info.Mode().Perm()&0022 != 0 {
		return nil, ErrHookRetirementUnproven
	}
	return info, nil
}

// CleanStoppedHookSpool inspects or retires only an already-certified clean prefix.
// The caller excludes daemon cursor/lifecycle changes; producer custody is held here.
// checkPath revalidates the caller's directory/lock custody before side-file writes
// and replacement. A nil check is reserved for existing lifecycle callers.
func CleanStoppedHookSpool(path, incarnation, certificate string, seq uint64, pid int, start int64, apply bool, checkPath func() error) (result HookCleanupResult, err error) {
	parent, err := privateHookParent(path)
	if err != nil {
		return result, err
	}
	check := func() error {
		current, err := privateHookParent(path)
		if err != nil {
			return err
		}
		if !os.SameFile(parent, current) {
			return ErrHookRetirementUnproven
		}
		if checkPath != nil {
			return checkPath()
		}
		return nil
	}
	if err := check(); err != nil {
		return result, err
	}
	lock, err := hookProducerLock(path, false)
	if err != nil {
		return result, err
	}
	defer func() { _ = lock.Close() }()
	proof, err := readHookProducerProof(path)
	if err != nil {
		return result, hookRetirementReadError(err)
	}
	if seq == 0 || proof.Incarnation != incarnation || proof.Nonce != certificate {
		return result, ErrHookRetirementUnproven
	}
	if !proof.Closed {
		if pid != proof.PID || start != proof.Start {
			return result, ErrHookRetirementUnproven
		}
		observed, err := procstart.StartTime(pid)
		if err == nil {
			if observed == start {
				return result, ErrHookProducerBusy
			}
		} else if !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return result, ErrHookRetirementUnproven
		}
	}
	if testHookBeforeStoppedSpoolOpen != nil {
		testHookBeforeStoppedSpoolOpen()
	}
	openLock, err := acquireHookSpoolOpenLock(path, true)
	if err != nil {
		return result, err
	}
	defer releaseHookSpoolOpenLock(openLock)
	if err := check(); err != nil {
		return result, err
	}
	flags := os.O_RDONLY
	if apply {
		flags = os.O_RDWR
	}
	f, err := openHookOwnedFile(path, flags)
	if err != nil {
		return result, err
	}
	s := &HookSpool{path: path, f: f, readLimit: hookCleanupMaxBytes, producerProof: &proof}
	defer func() { _ = s.Close() }() // No producerLock on this handle: preview never publishes proof.
	result.Before, err = f.Stat()
	if err != nil {
		return result, err
	}
	if err := validHookProducerProof(path, f, proof); err != nil {
		return result, hookRetirementReadError(err)
	}
	floorBytes, err := readHookOwnedSidecar(hookFloorPath(path), 64)
	var floor uint64
	if err == nil {
		floor, err = strconv.ParseUint(strings.TrimSpace(string(floorBytes)), 10, 64)
		if err != nil {
			return result, ErrHookRetirementUnproven
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, hookRetirementReadError(err)
	}
	s.lastSeq = floor
	data, err := s.readBytes()
	if err != nil {
		return result, err
	}
	for off := 0; off < len(data); {
		end, sequence, clean := hookRecordEnd(data, off)
		if !clean {
			break
		}
		if sequence > s.lastSeq {
			s.lastSeq = sequence
		}
		off = end
	}
	cut, err := hookCompactCut(data, seq, s.lastSeq, floor)
	if err != nil {
		return result, errors.Join(ErrHookRetirementUnproven, err)
	}
	result.EligibleBytes = int64(cut)
	result.After = result.Before
	if !apply || cut == 0 {
		return result, nil
	}
	result.Changed, err = s.compact(seq, check)
	result.After = nil
	if current, statErr := os.Lstat(path); statErr == nil && check() == nil {
		if owned, descriptorErr := s.f.Stat(); descriptorErr == nil && current.Mode().IsRegular() && os.SameFile(current, owned) {
			result.After = owned
		}
	}
	return result, err
}

// CompactStoppedHookSpool retains the lifecycle caller's existing proof contract.
func CompactStoppedHookSpool(path, incarnation, certificate string, seq uint64, pid int, start int64) error {
	_, err := CleanStoppedHookSpool(path, incarnation, certificate, seq, pid, start, true, nil)
	return err
}
