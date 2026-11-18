package managerproof

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	bolt "go.etcd.io/bbolt"
)

const ReplayFile = "manager-proof.db"
const replayMarker = "manager-proof.json"
const (
	ReplayNonce       byte = 'n'
	ReplayOperation   byte = 'o'
	ReplayTurn        byte = 't'
	ReplayCall        byte = 'c'
	ReplayMessage     byte = 'm'
	ReplayInterrupt   byte = 'i'
	replayGuardLimit       = 1 << 18
	replayFileRefusal      = 192 << 20
	replayMaxBatch         = 128
)

type ReplayKey struct {
	Kind              byte
	ID, Owner, Detail string
}
type TerminalReceipt struct {
	State             string  `json:"state,omitempty"`
	Binding           Binding `json:"binding"`
	Nonce             string  `json:"nonce"`
	OperationID       string  `json:"operation_id"`
	TextHash          string  `json:"text_hash"`
	NativeTurnID      string  `json:"native_turn_id"`
	NativeMessageID   string  `json:"native_message_id,omitempty"`
	WorkerGeneration  string  `json:"worker_generation,omitempty"`
	ProviderVersion   string  `json:"provider_version,omitempty"`
	Reason            string  `json:"reason"`
	ExternallySteered bool    `json:"externally_steered,omitempty"`
}
type replayHeader struct {
	Version                          int `json:"version"`
	ID, Session, Conversation, Owner string
	Count, Guards                    int
	Hash                             [32]byte
}

// Replay owns only negative guards and compact terminal history, never live proof.
// Its writer is called by custody workers or pre-admission RPCs, never native readers.
type Replay struct {
	mu              sync.Mutex
	db              *bolt.DB
	path            string
	fileInfo        os.FileInfo
	markerInfo      os.FileInfo
	boundScope      [32]byte
	bound           bool
	markerRaw       []byte
	header          replayHeader
	transaction     int
	claimed, closed bool
	maxGuards       int
	poisoned        bool
	descriptor      *os.File
}

func ReplayFingerprint(parts ...string) string {
	raw, _ := json.Marshal(parts)
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}
func replayScope(b Binding) [32]byte { raw, _ := json.Marshal(b); return sha256.Sum256(raw) }
func replayPrivate(info os.FileInfo) bool {
	s, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.Mode().IsRegular() && info.Mode().Perm() == 0600 && s.Uid == uint32(os.Getuid()) && s.Nlink == 1
}
func replayDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return ErrUnavailable
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm() != 0700 || st.Uid != uint32(os.Getuid()) {
		return ErrUnavailable
	}
	return nil
}
func readReplayPrivate(path string, max int64) ([]byte, os.FileInfo, error) {
	entry, err := os.Lstat(path)
	if err != nil || !replayPrivate(entry) || entry.Size() > max {
		return nil, nil, ErrUnavailable
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, ErrUnavailable
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !replayPrivate(info) || !os.SameFile(entry, info) {
		return nil, nil, ErrUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil || int64(len(raw)) > max {
		return nil, nil, ErrUnavailable
	}
	return raw, info, nil
}
func syncReplayDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return ErrUnavailable
	}
	defer func() { _ = f.Close() }()
	if f.Sync() != nil {
		return ErrUnavailable
	}
	return nil
}
func InitializeReplay(dir, session, conversation string) (string, error) {
	if replayDir(dir) != nil || !validID(session) || !validID(conversation) {
		return "", ErrInvalid
	}
	for _, name := range []string{ReplayFile, replayMarker} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			return "", ErrUnavailable
		}
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", ErrUnavailable
	}
	id := hex.EncodeToString(random[:])
	h := replayHeader{Version: 1, ID: id, Session: session, Conversation: conversation}
	db, err := openReplayDB(filepath.Join(dir, ReplayFile), true)
	if err != nil {
		return "", err
	}
	err = db.Update(func(tx *bolt.Tx) error {
		if _, e := tx.CreateBucket([]byte("records")); e != nil {
			return e
		}
		if _, e := tx.CreateBucket([]byte("meta")); e != nil {
			return e
		}
		return writeReplayHeader(tx, h)
	})
	closeErr := db.Close()
	if err != nil || closeErr != nil {
		return "", ErrUnavailable
	}
	raw, _ := json.Marshal(h)
	file, err := os.OpenFile(filepath.Join(dir, replayMarker), os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return "", ErrUnavailable
	}
	_, err = file.Write(raw)
	if err == nil {
		err = file.Sync()
	}
	closeErr = file.Close()
	if err != nil || closeErr != nil || syncReplayDir(dir) != nil {
		return "", ErrUnavailable
	}
	return id, nil
}
func openReplayDB(path string, create bool, capture ...**os.File) (db *bolt.DB, err error) {
	defer func() {
		if recover() != nil {
			if db != nil {
				_ = db.Close()
			}
			db = nil
			err = ErrUnavailable
		}
	}()
	db, err = bolt.Open(path, 0600, &bolt.Options{Timeout: 100 * time.Millisecond, OpenFile: func(path string, flag int, mode os.FileMode) (*os.File, error) {
		entry, e := os.Lstat(path)
		if create {
			if !errors.Is(e, os.ErrNotExist) {
				return nil, ErrUnavailable
			}
			flag |= os.O_CREATE | os.O_EXCL
		} else {
			if e != nil || !replayPrivate(entry) {
				return nil, ErrUnavailable
			}
			flag &^= os.O_CREATE
		}
		f, e := os.OpenFile(path, flag|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, mode)
		if e != nil {
			return nil, ErrUnavailable
		}
		info, e := f.Stat()
		if e != nil || !replayPrivate(info) || (entry != nil && !os.SameFile(entry, info)) {
			_ = f.Close()
			return nil, ErrUnavailable
		}
		if len(capture) > 0 {
			*capture[0] = f
		}
		return f, nil
	}})
	if err != nil {
		return nil, ErrUnavailable
	}
	db.AllocSize = 1 << 20
	return db, nil
}
func OpenReplay(dir, session, conversation, id string) (s *Replay, err error) {
	if replayDir(dir) != nil || len(id) != 32 {
		return nil, ErrUnavailable
	}
	marker := filepath.Join(dir, replayMarker)
	raw, markerInfo, e := readReplayPrivate(marker, 4096)
	var expected replayHeader
	if e != nil || json.Unmarshal(raw, &expected) != nil || expected.Version != 1 || expected.ID != id || expected.Session != session || expected.Conversation != conversation {
		return nil, ErrUnavailable
	}
	var descriptor *os.File
	db, e := openReplayDB(filepath.Join(dir, ReplayFile), false, &descriptor)
	if e != nil {
		return nil, e
	}
	s = &Replay{db: db, path: filepath.Join(dir, ReplayFile), markerRaw: raw, markerInfo: markerInfo, descriptor: descriptor, maxGuards: replayGuardLimit}
	defer func() {
		if recover() != nil {
			err = ErrUnavailable
		}
		if err != nil {
			_ = db.Close()
			s = nil
		}
	}()
	s.fileInfo, e = os.Lstat(s.path)
	if e != nil {
		return s, ErrUnavailable
	}
	err = db.View(func(tx *bolt.Tx) error {
		h, e := readReplayHeader(tx)
		if e != nil || h.ID != id || h.Session != session || h.Conversation != conversation || h.Guards > replayGuardLimit {
			return ErrUnavailable
		}
		var sum [32]byte
		count, guards := 0, 0
		b := tx.Bucket([]byte("records"))
		if b == nil {
			return ErrUnavailable
		}
		if e = b.ForEach(func(k, v []byte) error {
			if validateReplayRow(k, v) != nil {
				return ErrUnavailable
			}
			replayXor(&sum, k, v)
			count++
			if len(k) == 33 && k[0] != 'r' && k[0] != 'p' {
				guards++
			}
			return nil
		}); e != nil {
			return e
		}
		if sum != h.Hash || count != h.Count || guards != h.Guards {
			return ErrUnavailable
		}
		broken := false
		for range tx.Check() {
			broken = true
		}
		if broken {
			return ErrUnavailable
		}
		s.header = h
		s.transaction = tx.ID()
		return nil
	})
	return s, err
}
func writeReplayHeader(tx *bolt.Tx, h replayHeader) error {
	raw, _ := json.Marshal(h)
	b := tx.Bucket([]byte("meta"))
	sum := sha256.Sum256(raw)
	if err := b.Put([]byte("header"), raw); err != nil {
		return err
	}
	return b.Put([]byte("checksum"), sum[:])
}
func readReplayHeader(tx *bolt.Tx) (replayHeader, error) {
	var h replayHeader
	b := tx.Bucket([]byte("meta"))
	if b == nil {
		return h, ErrUnavailable
	}
	raw := b.Get([]byte("header"))
	sum := sha256.Sum256(raw)
	if !bytes.Equal(sum[:], b.Get([]byte("checksum"))) || json.Unmarshal(raw, &h) != nil || h.Version != 1 || h.Count < 0 || h.Guards < 0 {
		return h, ErrUnavailable
	}
	return h, nil
}
func replayXor(sum *[32]byte, k, v []byte) {
	h := sha256.New()
	_, _ = h.Write(k)
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(v)
	for i, b := range h.Sum(nil) {
		sum[i] ^= b
	}
}
func replayKind(kind byte) bool {
	return kind == ReplayNonce || kind == ReplayOperation || kind == ReplayTurn || kind == ReplayCall || kind == ReplayMessage || kind == ReplayInterrupt
}
func replayKey(kind byte, id string) ([]byte, error) {
	if !replayKind(kind) || !validID(id) {
		return nil, ErrInvalid
	}
	sum := sha256.Sum256([]byte(id))
	return append([]byte{kind}, sum[:]...), nil
}
func receiptKey(kind byte, id string) []byte {
	sum := sha256.Sum256([]byte(id))
	return append([]byte{kind}, sum[:]...)
}
func validateReplayRow(k, v []byte) error {
	if len(k) != 33 {
		return ErrUnavailable
	}
	if replayKind(k[0]) {
		if len(v) != 98 || v[0] != 1 || v[1] > 1 {
			return ErrUnavailable
		}
		return nil
	}
	if k[0] == 'r' || k[0] == 'p' {
		if len(v) > 16<<10 {
			return ErrUnavailable
		}
		var r TerminalReceipt
		if json.Unmarshal(v, &r) != nil || !validTerminal(r) {
			return ErrUnavailable
		}
		return nil
	}
	return ErrUnavailable
}
func validTerminal(r TerminalReceipt) bool {
	if r.Binding.Validate() != nil || !ValidHash(r.Nonce) || !ValidHash(r.TextHash) || !validID(r.OperationID) {
		return false
	}
	if r.State == "rejected" {
		return r.NativeTurnID == "" && r.Reason == "native_queue_rejected"
	}
	return (r.State == "" || r.State == "completed") && validID(r.NativeTurnID) && (r.Reason == "answer" || r.Reason == "aborted" || r.Reason == "error" || r.Reason == "refusal")
}
func (s *Replay) checkFile() error {
	if s.closed {
		return ErrUnavailable
	}
	info, err := os.Lstat(s.path)
	if err != nil || !replayPrivate(info) || !os.SameFile(info, s.fileInfo) {
		return ErrUnavailable
	}
	marker, info, err := readReplayPrivate(filepath.Join(filepath.Dir(s.path), replayMarker), 4096)
	if err != nil || !os.SameFile(info, s.markerInfo) || !bytes.Equal(marker, s.markerRaw) {
		return ErrUnavailable
	}
	return nil
}
func (s *Replay) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.db.Close()
}
func (s *Replay) mutate(admission bool, fn func(*bolt.Bucket, *replayHeader) error) error {
	if s.checkFile() != nil {
		return ErrUnavailable
	}
	if admission {
		info, err := os.Stat(s.path)
		if err != nil || info.Size() >= replayFileRefusal {
			return ErrCapacity
		}
	}
	h := s.header
	err := s.db.Update(func(tx *bolt.Tx) error {
		if tx.ID() != s.transaction+1 {
			return ErrStale
		}
		if err := fn(tx.Bucket([]byte("records")), &h); err != nil {
			return err
		}
		return writeReplayHeader(tx, h)
	})
	if err != nil {
		if !errors.Is(err, ErrCapacity) && !errors.Is(err, ErrConflict) && !errors.Is(err, ErrStale) && !errors.Is(err, ErrUnbound) && !errors.Is(err, ErrInvalid) {
			s.poisoned = true
			return ErrQuarantined
		}
		return err
	}
	s.header = h
	s.transaction++
	if admission {
		info, err := os.Stat(s.path)
		if err != nil {
			return ErrUnavailable
		}
		if info.Size() >= replayFileRefusal {
			return ErrCapacity
		}
	}
	return nil
}
func putReplayRow(b *bolt.Bucket, h *replayHeader, k, v []byte) error {
	old := b.Get(k)
	if bytes.Equal(old, v) {
		return nil
	}
	if old != nil {
		replayXor(&h.Hash, k, old)
	} else {
		h.Count++
		if replayKind(k[0]) {
			h.Guards++
		}
	}
	replayXor(&h.Hash, k, v)
	return b.Put(k, v)
}
func (s *Replay) Claim(observation string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validID(observation) {
		return ErrInvalid
	}
	if s.poisoned {
		return ErrQuarantined
	}
	if s.claimed {
		if s.header.Owner == observation {
			return nil
		}
		return ErrQuarantined
	}
	if s.header.Owner != "" {
		return ErrQuarantined
	}
	err := s.mutate(false, func(_ *bolt.Bucket, h *replayHeader) error { h.Owner = observation; return nil })
	if err == nil {
		s.claimed = true
	}
	return err
}
func (s *Replay) writeKeys(binding Binding, closed bool, receipt *TerminalReceipt, keys ...ReplayKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.claimed || s.poisoned {
		return ErrQuarantined
	}
	if binding.Validate() != nil || binding.Session != s.header.Session || binding.ConversationID != s.header.Conversation || len(keys) > replayMaxBatch {
		return ErrInvalid
	}
	scope := replayScope(binding)
	if s.bound && s.boundScope != scope {
		return ErrQuarantined
	}
	s.boundScope, s.bound = scope, true
	return s.mutate(!closed, func(b *bolt.Bucket, h *replayHeader) error {
		for _, key := range keys {
			k, err := replayKey(key.Kind, key.ID)
			if err != nil || !ValidHash(key.Owner) || key.Detail != "" && !ValidHash(key.Detail) {
				return ErrInvalid
			}
			owner, _ := hex.DecodeString(key.Owner)
			detail := make([]byte, 32)
			if key.Detail != "" {
				detail, _ = hex.DecodeString(key.Detail)
			}
			old := b.Get(k)
			v := make([]byte, 98)
			v[0] = 1
			copy(v[2:34], scope[:])
			copy(v[34:66], owner)
			copy(v[66:98], detail)
			if old != nil {
				if validateReplayRow(k, old) != nil {
					return ErrUnavailable
				}
				if !bytes.Equal(old[34:66], owner) {
					return ErrConflict
				}
				if key.Detail != "" && !bytes.Equal(old[66:98], make([]byte, 32)) && !bytes.Equal(old[66:98], detail) {
					return ErrConflict
				}
				if !closed && (old[1] == 1 || !bytes.Equal(old[2:34], scope[:])) {
					return ErrStale
				}
				copy(v, old)
				if key.Detail != "" {
					copy(v[66:98], detail)
				}
			} else if h.Guards >= s.maxGuards {
				return ErrCapacity
			}
			if closed {
				v[1] = 1
			}
			if err := putReplayRow(b, h, k, v); err != nil {
				return err
			}
		}
		if receipt != nil {
			guards := []struct {
				kind byte
				id   string
			}{{ReplayNonce, receipt.Nonce}, {ReplayOperation, receipt.OperationID}}
			if receipt.State != "rejected" {
				guards = append(guards, struct {
					kind byte
					id   string
				}{ReplayTurn, receipt.NativeTurnID})
			}
			for _, guard := range guards {
				key, e := replayKey(guard.kind, guard.id)
				if e != nil {
					return ErrInvalid
				}
				raw := b.Get(key)
				if len(raw) != 98 || raw[1] != 1 {
					return ErrUnbound
				}
				if !bytes.Equal(raw[2:34], scope[:]) {
					return ErrStale
				}
			}
			if !validTerminal(*receipt) || receipt.Binding != binding {
				return ErrInvalid
			}
			raw, _ := json.Marshal(receipt)
			if len(raw) > 16<<10 {
				return ErrCapacity
			}
			for _, k := range [][]byte{receiptKey('r', receipt.Nonce), receiptKey('p', receipt.OperationID)} {
				if old := b.Get(k); old != nil && !bytes.Equal(old, raw) {
					return ErrConflict
				}
				if err := putReplayRow(b, h, k, raw); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
func (s *Replay) Reserve(b Binding, keys ...ReplayKey) error {
	return s.writeKeys(b, false, nil, keys...)
}
func (s *Replay) CloseKeys(b Binding, keys ...ReplayKey) error {
	return s.writeKeys(b, true, nil, keys...)
}
func (s *Replay) RecordTerminal(r TerminalReceipt, keys ...ReplayKey) error {
	return s.writeKeys(r.Binding, true, &r, keys...)
}
func (s *Replay) Seen(kind byte, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.checkFile() != nil {
		return false, ErrUnavailable
	}
	k, err := replayKey(kind, id)
	if err != nil {
		return false, err
	}
	seen := false
	err = s.db.View(func(tx *bolt.Tx) error {
		if tx.ID() != s.transaction {
			return ErrStale
		}
		v := tx.Bucket([]byte("records")).Get(k)
		if v != nil {
			if validateReplayRow(k, v) != nil {
				return ErrUnavailable
			}
			seen = true
		}
		return nil
	})
	return seen, err
}
func (s *Replay) terminal(b Binding, kind byte, id string) (TerminalReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out TerminalReceipt
	if s.checkFile() != nil {
		return out, ErrUnavailable
	}
	err := s.db.View(func(tx *bolt.Tx) error {
		if tx.ID() != s.transaction {
			return ErrStale
		}
		raw := tx.Bucket([]byte("records")).Get(receiptKey(kind, id))
		if raw == nil {
			return ErrUnavailable
		}
		if json.Unmarshal(raw, &out) != nil || !validTerminal(out) {
			return ErrUnavailable
		}
		if out.Binding != b {
			return ErrStale
		}
		return nil
	})
	return out, err
}
func (s *Replay) Terminal(b Binding, nonce string) (TerminalReceipt, error) {
	return s.terminal(b, 'r', nonce)
}
func (s *Replay) Operation(b Binding, id string) (TerminalReceipt, error) {
	return s.terminal(b, 'p', id)
}

func (s *Replay) AssertReserved(binding Binding, keys ...ReplayKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.claimed || s.poisoned || !s.bound || s.boundScope != replayScope(binding) {
		return ErrQuarantined
	}
	if s.checkFile() != nil {
		return ErrUnavailable
	}
	return s.db.View(func(tx *bolt.Tx) error {
		if tx.ID() != s.transaction {
			return ErrStale
		}
		for _, key := range keys {
			k, err := replayKey(key.Kind, key.ID)
			if err != nil {
				return err
			}
			raw := tx.Bucket([]byte("records")).Get(k)
			if len(raw) != 98 || raw[1] != 0 {
				return ErrStale
			}
			owner, err := hex.DecodeString(key.Owner)
			if err != nil || !bytes.Equal(raw[2:34], s.boundScope[:]) || !bytes.Equal(raw[34:66], owner) {
				return ErrStale
			}
		}
		return nil
	})
}

type ReplayStats struct {
	GuardKeys, Records int
	PhysicalBytes      int64
}

func (s *Replay) Stats() (ReplayStats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.checkFile() != nil {
		return ReplayStats{}, ErrUnavailable
	}
	info, err := os.Stat(s.path)
	if err != nil {
		return ReplayStats{}, ErrUnavailable
	}
	return ReplayStats{GuardKeys: s.header.Guards, Records: s.header.Count, PhysicalBytes: info.Size()}, nil
}

// ClosedKeyDetail reads a compact disposition without restoring live authority.
func (s *Replay) ClosedKeyDetail(binding Binding, key ReplayKey) (string, error) {
	k, err := replayKey(key.Kind, key.ID)
	if err != nil || !ValidHash(key.Owner) {
		return "", ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned || s.checkFile() != nil {
		return "", ErrQuarantined
	}
	scope := replayScope(binding)
	var detail string
	err = s.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket([]byte("records")).Get(k)
		if raw == nil || len(raw) == 98 && raw[1] != 1 {
			return ErrUnbound
		}
		if validateReplayRow(k, raw) != nil {
			return ErrQuarantined
		}
		if !bytes.Equal(raw[2:34], scope[:]) || hex.EncodeToString(raw[34:66]) != key.Owner {
			return ErrConflict
		}
		detail = hex.EncodeToString(raw[66:98])
		return nil
	})
	return detail, err
}
