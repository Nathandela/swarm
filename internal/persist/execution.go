package persist

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"

	"github.com/Nathandela/swarm/internal/status"
)

const ExecutionVersion = 1
const MaxExecutionActors = 128
const MaxExecutionBytes = 64 << 10
const MaxExecutionRecords = 1024

// ExecutionRecord holds control evidence, never native message bodies or credentials.
// Empty Incomplete means complete observation, not permission to perform an effect.
type ExecutionRecord struct {
	PendingCallbacks    map[string]string         `json:"pending_callbacks"`
	AnonymousActors     int                       `json:"anonymous_actors"`
	UnknownActors       bool                      `json:"unknown_actors"`
	Version             int                       `json:"version"`
	SessionID           string                    `json:"session_id"`
	Incarnation         string                    `json:"incarnation"`
	ConversationID      string                    `json:"conversation_id,omitempty"`
	TurnID              string                    `json:"turn_id,omitempty"`
	OwnerRevision       uint64                    `json:"owner_revision"`
	Incomplete          string                    `json:"incomplete"`
	TurnSequence        uint64                    `json:"turn_sequence"`
	InteractionSequence uint64                    `json:"interaction_sequence"`
	ChildSequence       uint64                    `json:"child_sequence"`
	TypedSequence       uint64                    `json:"typed_sequence"`
	MainTurn            status.Turn               `json:"main_turn"`
	Interaction         status.Interaction        `json:"interaction"`
	InputActor          string                    `json:"input_actor,omitempty"`
	Actors              map[string]ExecutionActor `json:"actors"`
}

type ExecutionActor struct {
	Sequence      uint64 `json:"sequence"`
	StartSequence uint64 `json:"start_sequence"`
	Active        bool   `json:"active"`
}

func (r ExecutionRecord) Validate() error {
	if len(r.PendingCallbacks) > MaxExecutionActors {
		return fmt.Errorf("persist: callback custody capacity")
	}
	for seq, fingerprint := range r.PendingCallbacks {
		n, err := strconv.ParseUint(seq, 10, 64)
		if err != nil || n == 0 || strconv.FormatUint(n, 10) != seq || len(fingerprint) != 64 {
			return fmt.Errorf("persist: invalid callback custody")
		}
		raw, err := hex.DecodeString(fingerprint)
		if err != nil || hex.EncodeToString(raw) != fingerprint {
			return fmt.Errorf("persist: invalid callback fingerprint")
		}
	}
	if r.AnonymousActors < 0 || r.AnonymousActors > MaxExecutionActors {
		return fmt.Errorf("persist: invalid anonymous actor bound")
	}
	if r.Version != ExecutionVersion || !ValidID(r.SessionID) || r.Incarnation == "" || len(r.Incarnation) > 256 || len(r.ConversationID) > 256 || len(r.TurnID) > 256 || len(r.InputActor) > 256 || len(r.Incomplete) > 128 || len(r.Actors) > MaxExecutionActors {
		return fmt.Errorf("persist: invalid execution identity or bounds")
	}
	if r.MainTurn != status.TurnActive && r.MainTurn != status.TurnIdle && r.MainTurn != status.TurnUnknown {
		return fmt.Errorf("persist: invalid execution turn")
	}
	if r.Interaction != status.InteractionNone && r.Interaction != status.InteractionUnknown && r.Interaction != status.InteractionPrompt && r.Interaction != status.InteractionPermission && r.Interaction != status.InteractionError {
		return fmt.Errorf("persist: invalid execution interaction")
	}
	for id, a := range r.Actors {
		if id == "" || len(id) > 256 || a.Sequence == 0 || a.StartSequence > a.Sequence || (a.Active && a.StartSequence == 0 && r.Incomplete == "") {
			return fmt.Errorf("persist: invalid execution actor")
		}
	}
	return nil
}

// Capacity reservations are per root, shared across Store handles. Only accounting
// holds mu: independent sessions never hold it while writing or syncing disk.
type executionCapacity struct {
	once          sync.Once
	err           error
	mu            sync.Mutex
	entries       map[string]*executionSlot
	bytes         int64
	retainedBytes int64
}
type executionSlot struct {
	retiringIncarnation string
	deleted             bool
	mu                  sync.Mutex
	size                int64
	committedSize       int64
	scratchSize         int64
	retained            bool
	refs                int
}

var executionCapacities sync.Map

func (s *Store) executionCapacity() (*executionCapacity, error) {
	absolute, err := filepath.Abs(s.root)
	if err != nil {
		return nil, err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, err
	}
	value, _ := executionCapacities.LoadOrStore(canonical, &executionCapacity{})
	capacity := value.(*executionCapacity)
	capacity.once.Do(func() {
		capacity.entries = make(map[string]*executionSlot)
		root, e := executionRoot(s.root)
		if e != nil {
			capacity.err = e
			return
		}
		defer root.Close()
		dir, e := root.Open(".")
		if e != nil {
			capacity.err = e
			return
		}
		defer dir.Close()
		for {
			entries, readErr := dir.ReadDir(128)
			for _, entry := range entries {
				if !entry.IsDir() || !ValidID(entry.Name()) {
					continue
				}
				size := int64(0)
				committed, scratch := int64(0), int64(0)
				found := false
				for _, name := range []string{"execution.json", "execution.pending"} {
					info, e := root.Lstat(filepath.Join(entry.Name(), name))
					if os.IsNotExist(e) {
						continue
					}
					if e != nil {
						capacity.err = e
						return
					}
					if !executionPrivate(info, false) || info.Size() > MaxExecutionBytes {
						capacity.err = fmt.Errorf("persist: unsafe or over-capacity execution store")
						return
					}
					size += info.Size()
					if name == "execution.json" {
						committed = info.Size()
					} else {
						scratch = info.Size()
					}
					found = true
				}
				if !found {
					continue
				}
				if len(capacity.entries) >= MaxExecutionRecords {
					capacity.err = fmt.Errorf("persist: execution record capacity")
					return
				}
				capacity.entries[entry.Name()] = &executionSlot{size: size, committedSize: committed, scratchSize: scratch, retained: true}
				capacity.bytes += size
				capacity.retainedBytes += committed
			}
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				capacity.err = readErr
				return
			}
		}
	})
	return capacity, capacity.err
}

// PrepareExecutionStore performs bounded startup accounting before ingress opens.
func (s *Store) PrepareExecutionStore() error { _, err := s.executionCapacity(); return err }

func (s *Store) SaveExecution(r ExecutionRecord) error {
	if err := r.Validate(); err != nil {
		return err
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if len(data) > MaxExecutionBytes {
		return fmt.Errorf("persist: execution record capacity")
	}
	capacity, err := s.executionCapacity()
	if err != nil {
		return err
	}
	capacity.mu.Lock()
	slot, exists := capacity.entries[r.SessionID]
	if !exists {
		if len(capacity.entries) >= MaxExecutionRecords {
			capacity.mu.Unlock()
			return fmt.Errorf("persist: aggregate execution record capacity")
		}
		slot = &executionSlot{}
		capacity.entries[r.SessionID] = slot
	}
	slot.refs++
	capacity.mu.Unlock()
	slot.mu.Lock()
	defer func() {
		slot.mu.Unlock()
		capacity.mu.Lock()
		slot.refs--
		if slot.refs == 0 && !slot.retained {
			delete(capacity.entries, r.SessionID)
		}
		capacity.mu.Unlock()
	}()
	capacity.mu.Lock()
	if slot.retiringIncarnation != "" || slot.deleted {
		capacity.mu.Unlock()
		return fmt.Errorf("persist: execution retirement durability pending")
	}
	previous := slot.size
	committed := slot.committedSize
	incoming := int64(len(data))
	reserved := max(int64(0), incoming-slot.scratchSize)
	// Committed/projected records leave one spare inode; in-flight replacements
	// may consume that headroom without allowing the retained set to grow into it.
	growth := max(committed, incoming) - committed
	limit := int64(MaxExecutionRecords * MaxExecutionBytes)
	if capacity.retainedBytes+growth+MaxExecutionBytes > limit || capacity.bytes+reserved > limit {
		capacity.mu.Unlock()
		return fmt.Errorf("persist: aggregate execution byte capacity")
	}
	capacity.bytes += reserved
	capacity.retainedBytes += growth
	capacity.mu.Unlock()
	visible, err := s.replaceExecution(r.SessionID, data)
	capacity.mu.Lock()
	if visible {
		capacity.bytes += incoming - previous - reserved
		capacity.retainedBytes += incoming - committed - growth
		slot.size = incoming
		slot.committedSize = incoming
		slot.scratchSize = 0
		slot.retained = true
	} else {
		capacity.bytes -= reserved
		capacity.retainedBytes -= growth
	}
	// The slot reference prevents deleting admission beneath another waiting writer.
	capacity.mu.Unlock()
	return err
}

func executionPrivate(info os.FileInfo, directory bool) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) || info.Mode().Perm()&0077 != 0 {
		return false
	}
	if directory {
		return info.IsDir() && info.Mode()&os.ModeSymlink == 0
	}
	return info.Mode().IsRegular() && stat.Nlink == 1
}

func executionRoot(path string) (*os.Root, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !executionPrivate(before, true) {
		return nil, fmt.Errorf("persist: unsafe execution directory")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	current, currentErr := os.Lstat(path)
	if err != nil || currentErr != nil || !os.SameFile(before, opened) || !os.SameFile(current, opened) {
		root.Close()
		return nil, fmt.Errorf("persist: execution directory replaced")
	}
	return root, nil
}

func executionSession(root *os.Root, id string) (*os.Root, error) {
	before, err := root.Lstat(id)
	if err != nil {
		return nil, err
	}
	if !executionPrivate(before, true) {
		return nil, fmt.Errorf("persist: unsafe execution session")
	}
	session, err := root.OpenRoot(id)
	if err != nil {
		return nil, err
	}
	opened, err := session.Stat(".")
	current, currentErr := root.Lstat(id)
	if err != nil || currentErr != nil || !os.SameFile(before, opened) || !os.SameFile(current, opened) {
		session.Close()
		return nil, fmt.Errorf("persist: execution session replaced")
	}
	return session, nil
}

func syncExecutionDir(root *os.Root) error {
	f, err := root.OpenFile(".", os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func checkExecutionFile(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !executionPrivate(info, false) {
		return fmt.Errorf("persist: unsafe execution file")
	}
	return nil
}

// Test seam for directory durability faults after visibility.
var executionSyncDir = syncExecutionDir

func (s *Store) replaceExecution(id string, data []byte) (bool, error) {
	root, err := executionRoot(s.root)
	if err != nil {
		return false, err
	}
	defer root.Close()
	if err = root.Mkdir(id, 0700); err == nil {
		if err = executionSyncDir(root); err != nil {
			return false, err
		}
	} else if !os.IsExist(err) {
		return false, err
	}
	session, err := executionSession(root, id)
	if err != nil {
		return false, err
	}
	defer session.Close()
	if err = checkExecutionFile(session, "execution.json"); err != nil {
		return false, err
	}
	const scratch = "execution.pending"
	// Exactly one private scratch inode per session survives any number of crashes.
	if err = checkExecutionFile(session, scratch); err != nil {
		return false, err
	}
	f, err := session.OpenFile(scratch, os.O_WRONLY|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return false, err
	}
	info, err := f.Stat()
	if err != nil || !executionPrivate(info, false) {
		f.Close()
		return false, fmt.Errorf("persist: unsafe execution scratch")
	}
	defer session.Remove(scratch)
	if err = f.Truncate(0); err == nil {
		var n int
		n, err = writeTemp(f, data)
		if err == nil && n != len(data) {
			err = io.ErrShortWrite
		}
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return false, err
	}
	if closeErr != nil {
		return false, closeErr
	}
	if err = session.Rename(scratch, "execution.json"); err != nil {
		return false, err
	}
	if err = executionSyncDir(session); err != nil {
		return true, fmt.Errorf("persist: execution durability uncertain: %w", err)
	}
	return true, nil
}

func (s *Store) LoadExecution(id string) (ExecutionRecord, error) {
	var r ExecutionRecord
	if err := validateID(id); err != nil {
		return r, err
	}
	root, err := executionRoot(s.root)
	if err != nil {
		return r, err
	}
	defer root.Close()
	session, err := executionSession(root, id)
	if err != nil {
		return r, err
	}
	defer session.Close()
	before, err := session.Lstat("execution.json")
	if err != nil {
		return r, err
	}
	if !executionPrivate(before, false) || before.Size() > MaxExecutionBytes {
		return r, fmt.Errorf("persist: unsafe execution file")
	}
	f, err := session.OpenFile("execution.json", os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return r, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !executionPrivate(info, false) || !os.SameFile(before, info) {
		return r, fmt.Errorf("persist: execution file replaced")
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxExecutionBytes+1))
	if err != nil {
		return r, err
	}
	if len(data) > MaxExecutionBytes {
		return r, fmt.Errorf("persist: execution record capacity")
	}
	current, err := session.Lstat("execution.json")
	if err != nil || !os.SameFile(current, info) {
		return r, fmt.Errorf("persist: execution file replaced")
	}
	if err = executionJSON(data); err != nil {
		return r, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&r); err != nil {
		return r, err
	}
	if r.SessionID != id {
		return r, fmt.Errorf("persist: execution session mismatch")
	}
	return r, r.Validate()
}

// Reject duplicate keys before decoding authority fields into Go structs.
func executionJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	invalid := fmt.Errorf("persist: ambiguous execution JSON")
	var value func(int) error
	value = func(depth int) error {
		if depth > 8 {
			return invalid
		}
		token, err := decoder.Token()
		if err != nil {
			return invalid
		}
		delimiter, composite := token.(json.Delim)
		if !composite {
			return nil
		}
		switch delimiter {
		case '{':
			seen := make(map[string]bool)
			for decoder.More() {
				key, err := decoder.Token()
				name, ok := key.(string)
				if err != nil || !ok || seen[name] {
					return invalid
				}
				seen[name] = true
				if depth == 0 {
					switch name {
					case "version", "session_id", "incarnation", "conversation_id", "turn_id", "owner_revision", "incomplete", "turn_sequence", "interaction_sequence", "child_sequence", "typed_sequence", "main_turn", "interaction", "input_actor", "actors", "anonymous_actors", "unknown_actors", "pending_callbacks":
					default:
						return invalid
					}
				} else if depth == 2 {
					switch name {
					case "sequence", "start_sequence", "active":
					default:
						return invalid
					}
				}
				if err = value(depth + 1); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim('}') {
				return invalid
			}
		case '[':
			for decoder.More() {
				if err = value(depth + 1); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim(']') {
				return invalid
			}
		default:
			return invalid
		}
		return nil
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return invalid
	}
	return nil
}

// RetireExecution removes resolved evidence only for the caller's exact native
// incarnation. The caller must independently prove native termination and finish
// its final ingress drain; status alone never supplies that proof.
func (s *Store) RetireExecution(id, incarnation string) error {
	if !ValidID(id) || incarnation == "" {
		return fmt.Errorf("persist: invalid retirement identity")
	}
	capacity, err := s.executionCapacity()
	if err != nil {
		return err
	}
	capacity.mu.Lock()
	slot, exists := capacity.entries[id]
	if !exists {
		capacity.mu.Unlock()
		return fmt.Errorf("persist: execution custody missing")
	}
	slot.refs++
	capacity.mu.Unlock()
	slot.mu.Lock()
	defer func() {
		slot.mu.Unlock()
		capacity.mu.Lock()
		slot.refs--
		if slot.refs == 0 && !slot.retained {
			delete(capacity.entries, id)
		}
		capacity.mu.Unlock()
	}()
	root, err := executionRoot(s.root)
	if err != nil {
		return err
	}
	defer root.Close()
	session, err := executionSession(root, id)
	if err != nil {
		return err
	}
	defer session.Close()
	if _, err = session.Lstat("execution.pending"); !os.IsNotExist(err) {
		return fmt.Errorf("persist: unresolved execution scratch")
	}
	if slot.retiringIncarnation != "" {
		if slot.retiringIncarnation != incarnation {
			return fmt.Errorf("persist: execution retirement incarnation mismatch")
		}
		if _, err = session.Lstat("execution.json"); !os.IsNotExist(err) {
			return fmt.Errorf("persist: execution custody replaced during retirement")
		}
	} else {
		record, err := s.LoadExecution(id)
		if err != nil {
			return err
		}
		if record.Incarnation != incarnation {
			return fmt.Errorf("persist: execution retirement incarnation mismatch")
		}
		if len(record.PendingCallbacks) > 0 || record.UnknownActors || record.AnonymousActors > 0 || record.MainTurn != status.TurnIdle || record.Interaction != status.InteractionNone {
			return fmt.Errorf("persist: unresolved execution custody")
		}
		for _, actor := range record.Actors {
			if actor.Active {
				return fmt.Errorf("persist: unresolved actor custody")
			}
		}
		if err = session.Remove("execution.json"); err != nil {
			return err
		}
		// Keep exact ownership until unlink's directory entry is durably committed.
		slot.retiringIncarnation = incarnation
	}
	if err = executionSyncDir(session); err != nil {
		return err
	}
	capacity.mu.Lock()
	capacity.bytes -= slot.size
	capacity.retainedBytes -= slot.committedSize
	slot.size = 0
	slot.committedSize = 0
	slot.scratchSize = 0
	slot.retained = false
	slot.retiringIncarnation = ""
	capacity.mu.Unlock()
	return nil
}

// Session deletion already has lifecycle authorization. Share its serialization
// with execution writers so queued stale saves cannot resurrect deleted custody.
func (s *Store) withExecutionDeletion(id string, remove func() error) error {
	capacity, _ := s.executionCapacity()
	if capacity == nil {
		return remove()
	}
	capacity.mu.Lock()
	slot, exists := capacity.entries[id]
	if !exists {
		slot = &executionSlot{}
		capacity.entries[id] = slot
	}
	slot.refs++
	capacity.mu.Unlock()
	slot.mu.Lock()
	defer func() {
		slot.mu.Unlock()
		capacity.mu.Lock()
		slot.refs--
		if slot.refs == 0 && !slot.retained {
			delete(capacity.entries, id)
		}
		capacity.mu.Unlock()
	}()
	if err := remove(); err != nil {
		return err
	}
	capacity.mu.Lock()
	capacity.bytes -= slot.size
	capacity.retainedBytes -= slot.committedSize
	slot.size = 0
	slot.committedSize = 0
	slot.scratchSize = 0
	slot.retained = false
	slot.retiringIncarnation = ""
	slot.deleted = true
	capacity.mu.Unlock()
	return nil
}
