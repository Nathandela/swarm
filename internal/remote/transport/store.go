package transport

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

// Store is the session's durable coordinate set, the transport's contract with
// PB-STATE (S7). Two coordinates live here:
//
//   - Cursor is the relay's storage cursor, the transport's own: it decides what
//     the relay is asked for next, so losing it re-delivers a drained backlog.
//   - HighWater is the per-(sender, epoch) authenticated sequence number, which
//     belongs to the caller that holds the content key: losing it makes a
//     restarted process accept a frame it already accepted (PB-NET-6).
//
// They are two INDEPENDENT saves, not one transaction, and Drain does not pretend
// otherwise. What makes that safe is the ORDERING: the caller advances its
// high-water inside fn, and the cursor is written only after the whole page has
// been handed over. A crash in between therefore re-delivers a page the caller's
// own high-water already rejects -- duplicate work, never a gap. PB-STATE-7 is
// where the two become one sealed transaction.
//
// Neither is key material or plaintext, so a Store never violates PB-NET-3.
type Store interface {
	// Cursor returns the last relay storage cursor drained, or 0.
	Cursor() (uint64, error)
	// SetCursor records the last relay storage cursor drained.
	SetCursor(cursor uint64) error
	// HighWater returns the highest accepted seq for a (sender, epoch) pair, and
	// whether one has ever been recorded.
	HighWater(sender [8]byte, epoch uint32) (uint64, bool, error)
	// SetHighWater records the highest accepted seq for a (sender, epoch) pair.
	SetHighWater(sender [8]byte, epoch uint32, seq uint64) error
}

// state is the persisted shape, and the in-memory one.
type state struct {
	Cursor    uint64            `json:"cursor"`
	HighWater map[string]uint64 `json:"high_water,omitempty"`
}

func hwKey(sender [8]byte, epoch uint32) string {
	return hex.EncodeToString(sender[:]) + ":" + strconv.FormatUint(uint64(epoch), 10)
}

// memStore is the default Store: correct within one process, and deliberately
// nothing more. A caller that needs the PB-NET-6 guarantees supplies a FileStore
// (or S7's sealed store).
type memStore struct {
	mu sync.Mutex
	st state
}

func newMemStore() *memStore { return &memStore{st: state{HighWater: map[string]uint64{}}} }

func (m *memStore) Cursor() (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.st.Cursor, nil
}

func (m *memStore) SetCursor(cursor uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.st.Cursor = cursor
	return nil
}

func (m *memStore) HighWater(sender [8]byte, epoch uint32) (uint64, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.st.HighWater[hwKey(sender, epoch)]
	return v, ok, nil
}

func (m *memStore) SetHighWater(sender [8]byte, epoch uint32, seq uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.st.HighWater[hwKey(sender, epoch)] = seq
	return nil
}

// stateFile is the FileStore's single JSON file.
const stateFile = "transport-state.json"

// FileStore persists the session's coordinates to one JSON file, written
// atomically (temp file + rename) so a process killed mid-write reopens either
// the previous state or the new one, never a truncated one.
type FileStore struct {
	path string

	mu sync.Mutex
	st state
}

// NewFileStore opens (or creates) the coordinate file under dir.
func NewFileStore(dir string) (*FileStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("transport: store dir: %w", err)
	}
	f := &FileStore{path: filepath.Join(dir, stateFile), st: state{HighWater: map[string]uint64{}}}
	b, err := os.ReadFile(f.path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return f, nil
	case err != nil:
		return nil, fmt.Errorf("transport: read store: %w", err)
	}
	if err := json.Unmarshal(b, &f.st); err != nil {
		return nil, fmt.Errorf("transport: parse store: %w", err)
	}
	if f.st.HighWater == nil {
		f.st.HighWater = map[string]uint64{}
	}
	return f, nil
}

// save writes the current state; the caller holds f.mu.
func (f *FileStore) save() error {
	b, err := json.Marshal(f.st)
	if err != nil {
		return err
	}
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("transport: write store: %w", err)
	}
	if err := os.Rename(tmp, f.path); err != nil {
		return fmt.Errorf("transport: commit store: %w", err)
	}
	return nil
}

// Cursor returns the last relay storage cursor drained.
func (f *FileStore) Cursor() (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.st.Cursor, nil
}

// SetCursor records the last relay storage cursor drained.
func (f *FileStore) SetCursor(cursor uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.st.Cursor = cursor
	return f.save()
}

// HighWater returns the highest accepted seq for a (sender, epoch) pair.
func (f *FileStore) HighWater(sender [8]byte, epoch uint32) (uint64, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.st.HighWater[hwKey(sender, epoch)]
	return v, ok, nil
}

// SetHighWater records the highest accepted seq for a (sender, epoch) pair.
func (f *FileStore) SetHighWater(sender [8]byte, epoch uint32, seq uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.st.HighWater[hwKey(sender, epoch)] = seq
	return f.save()
}
