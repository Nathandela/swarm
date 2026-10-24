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
	"strings"
	"sync"
	"syscall"

	"github.com/Nathandela/swarm/internal/status"
)

const ExecutionVersion = 2
const MaxExecutionActors = 128
const MaxExecutionBytes = 64 << 10
const MaxExecutionRecords = 1024

// ExecutionRecord holds control evidence, never native message bodies or credentials.
// Empty Incomplete means complete observation, not permission to perform an effect.
type ExecutionRecord struct {
	Codex               *CodexExecution           `json:"codex,omitempty"`
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
	if r.Codex != nil {
		if err := r.Codex.Validate(); err != nil {
			return err
		}
	}
	if r.Codex != nil && (r.Codex.ThreadID != r.ConversationID || r.Codex.TurnID != r.TurnID || r.Codex.OwnerRevision > r.OwnerRevision) {
		return fmt.Errorf("persist: inconsistent Codex execution ownership")
	}
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
	if (r.Version != ExecutionVersion && r.Version != 1) || (r.Version == 1 && r.Codex != nil) || !ValidID(r.SessionID) || r.Incarnation == "" || len(r.Incarnation) > 256 || len(r.ConversationID) > 256 || len(r.TurnID) > 256 || len(r.InputActor) > 256 || len(r.Incomplete) > 128 || len(r.Actors) > MaxExecutionActors {
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

// CodexExecution retains one observed native turn and independently live custody.
// No message, command, result body or credential belongs in these records.
type CodexExecution struct {
	NativeIncarnation   string                            `json:"native_incarnation"`
	FeedEpoch           string                            `json:"feed_epoch"`
	ThreadID            string                            `json:"thread_id"`
	Sequence            uint64                            `json:"sequence"`
	LastEventHash       string                            `json:"last_event_hash"`
	TurnID              string                            `json:"turn_id"`
	OwnerRevision       uint64                            `json:"owner_revision"`
	State               CodexTurnState                    `json:"state"`
	Cause               CodexTerminalCause                `json:"cause"`
	TerminalSequence    uint64                            `json:"terminal_sequence"`
	Incomplete          string                            `json:"incomplete"`
	UnknownCustody      bool                              `json:"unknown_custody"`
	Denied              string                            `json:"denied"`
	UserSequence        uint64                            `json:"user_sequence"`
	CommentarySequence  uint64                            `json:"commentary_sequence"`
	ToolFailureSequence uint64                            `json:"tool_failure_sequence"`
	ToolResultSequence  uint64                            `json:"tool_result_sequence"`
	LastItemSequence    uint64                            `json:"last_item_sequence"`
	Items               map[string]CodexExecutionItem     `json:"items"`
	Children            map[string]CodexExecutionChild    `json:"children"`
	Decisions           map[string]CodexExecutionDecision `json:"decisions"`
	Stimuli             map[string]CodexExecutionStimulus `json:"stimuli,omitempty"`
}

type CodexTurnState string
type CodexItemKind string
type CodexAssistantPhase string
type CodexTerminalCause string
type CodexDecisionKind string

type CodexExecutionItem struct {
	ActorThreadID     string              `json:"actor_thread_id,omitempty"`
	CollabTool        string              `json:"collab_tool,omitempty"`
	NativeIncarnation string              `json:"native_incarnation"`
	FeedEpoch         string              `json:"feed_epoch"`
	TurnID            string              `json:"turn_id"`
	Kind              CodexItemKind       `json:"kind"`
	Phase             CodexAssistantPhase `json:"phase"`
	StartSequence     uint64              `json:"start_sequence"`
	Complete          bool                `json:"complete"`
}
type CodexExecutionChild struct {
	ParentThreadID    string `json:"parent_thread_id,omitempty"`
	AwaitingTurn      bool   `json:"awaiting_turn,omitempty"`
	NativeIncarnation string `json:"native_incarnation"`
	FeedEpoch         string `json:"feed_epoch"`
	TurnID            string `json:"turn_id"`
	ChildTurnID       string `json:"child_turn_id"`
	Active            bool   `json:"active"`
}
type CodexExecutionDecision struct {
	ActorThreadID     string            `json:"actor_thread_id,omitempty"`
	RequestID         string            `json:"request_id,omitempty"`
	NativeIncarnation string            `json:"native_incarnation"`
	FeedEpoch         string            `json:"feed_epoch"`
	TurnID            string            `json:"turn_id"`
	Kind              CodexDecisionKind `json:"kind"`
}

type CodexExecutionStimulus struct {
	NativeIncarnation  string `json:"native_incarnation"`
	FeedEpoch          string `json:"feed_epoch"`
	ActorThreadID      string `json:"actor_thread_id"`
	TurnID             string `json:"turn_id"`
	ItemID             string `json:"item_id"`
	ReceiverThreadID   string `json:"receiver_thread_id"`
	PreviousTurnID     string `json:"previous_turn_id"`
	StartSequence      uint64 `json:"start_sequence"`
	CompletionSequence uint64 `json:"completion_sequence"`
	Phase              string `json:"phase"`
}

func executionID(id string) bool { return id != "" && len(id) <= 256 }
func (c CodexExecution) Validate() error {
	if !executionID(c.NativeIncarnation) || !executionID(c.FeedEpoch) || !executionID(c.ThreadID) || len(c.TurnID) > 256 || len(c.Incomplete) > 128 || len(c.Denied) > 128 || len(c.Items)+len(c.Children)+len(c.Decisions)+len(c.Stimuli) > MaxExecutionActors {
		return fmt.Errorf("persist: invalid Codex execution identity or bounds")
	}
	if c.State != "unobserved" && c.State != "active" && c.State != "terminal" {
		return fmt.Errorf("persist: invalid Codex turn state")
	}
	if c.Cause != "" && c.Cause != "quota" && c.Cause != "completed" && c.Cause != "cancelled" && c.Cause != "failed" && c.Cause != "unknown" {
		return fmt.Errorf("persist: invalid Codex terminal cause")
	}
	if c.Sequence == 0 {
		if c.LastEventHash != "" {
			return fmt.Errorf("persist: invalid Codex initial receipt")
		}
	} else {
		raw, err := hex.DecodeString(c.LastEventHash)
		if err != nil || len(raw) != 32 || hex.EncodeToString(raw) != c.LastEventHash {
			return fmt.Errorf("persist: invalid Codex event receipt")
		}
	}
	if c.TerminalSequence > c.Sequence || c.UserSequence > c.Sequence || c.CommentarySequence > c.Sequence || c.ToolResultSequence > c.Sequence || c.ToolFailureSequence > c.Sequence || c.LastItemSequence > c.Sequence {
		return fmt.Errorf("persist: invalid Codex event position")
	}
	for id, item := range c.Items {
		if len(id) == 0 || len(id) > 1024 || (item.Kind == "collabAgentToolCall") != (item.CollabTool != "") || len(item.ActorThreadID) > 256 || !validCodexCollabTool(item.CollabTool) || !executionID(item.TurnID) || !executionID(item.NativeIncarnation) || !executionID(item.FeedEpoch) || item.StartSequence == 0 || (item.FeedEpoch == c.FeedEpoch && item.NativeIncarnation == c.NativeIncarnation && item.StartSequence > c.Sequence) {
			return fmt.Errorf("persist: invalid Codex item identity")
		}
		switch item.Kind {
		case "userMessage", "agentMessage", "commandExecution", "fileChange", "mcpToolCall", "collabAgentToolCall", "contextCompaction", "reasoning", "plan", "unknown":
		default:
			return fmt.Errorf("persist: invalid Codex item kind")
		}
		switch item.Phase {
		case "", "commentary", "final_answer", "unknown":
		default:
			return fmt.Errorf("persist: invalid Codex assistant phase")
		}
	}
	for id, child := range c.Children {
		if !executionID(id) || id == c.ThreadID || (child.AwaitingTurn && !child.Active) || len(child.TurnID) > 256 || len(child.ChildTurnID) > 256 || len(child.ParentThreadID) > 256 || !executionID(child.NativeIncarnation) || !executionID(child.FeedEpoch) || (child.ParentThreadID != "" && !executionID(child.TurnID)) || (child.ChildTurnID == "" && !child.AwaitingTurn) {
			return fmt.Errorf("persist: invalid Codex child identity")
		}
	}
	for id := range c.Children {
		seen := make(map[string]bool)
		for actor := id; actor != "" && actor != c.ThreadID; {
			if seen[actor] {
				return fmt.Errorf("persist: cyclic Codex actors")
			}
			seen[actor] = true
			child, known := c.Children[actor]
			if !known {
				return fmt.Errorf("persist: missing Codex parent actor")
			}
			actor = child.ParentThreadID
		}
	}
	for id, decision := range c.Decisions {
		if len(id) == 0 || len(id) > 1024 || len(decision.ActorThreadID) > 256 || len(decision.RequestID) > 256 || !executionID(decision.TurnID) || !executionID(decision.NativeIncarnation) || !executionID(decision.FeedEpoch) || (decision.Kind != "approval" && decision.Kind != "question") {
			return fmt.Errorf("persist: invalid Codex decision identity")
		}
	}
	for id, stimulus := range c.Stimuli {
		if len(id) == 0 || len(id) > 1024 || !executionID(stimulus.NativeIncarnation) || !executionID(stimulus.FeedEpoch) || !executionID(stimulus.ActorThreadID) || !executionID(stimulus.TurnID) || !executionID(stimulus.ItemID) || !executionID(stimulus.ReceiverThreadID) || len(stimulus.PreviousTurnID) > 256 || stimulus.StartSequence == 0 || (stimulus.CompletionSequence != 0 && stimulus.CompletionSequence <= stimulus.StartSequence) || (stimulus.FeedEpoch == c.FeedEpoch && stimulus.NativeIncarnation == c.NativeIncarnation && (stimulus.StartSequence > c.Sequence || stimulus.CompletionSequence > c.Sequence)) || (stimulus.Phase != "started" && stimulus.Phase != "accepted" && stimulus.Phase != "failed") || (stimulus.Phase == "started") != (stimulus.CompletionSequence == 0) {
			return fmt.Errorf("persist: invalid Codex stimulus custody")
		}
		if _, known := c.Children[stimulus.ReceiverThreadID]; !known {
			return fmt.Errorf("persist: missing Codex stimulus receiver")
		}
	}

	return nil
}

func validCodexCollabTool(tool string) bool {
	switch tool {
	case "", "spawnAgent", "sendInput", "resumeAgent", "wait", "closeAgent":
		return true
	}
	return false
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
	var value func(int, string) error
	value = func(depth int, path string) error {
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
				allowed := ""
				switch path {
				case "":
					allowed = "version session_id incarnation conversation_id turn_id owner_revision incomplete turn_sequence interaction_sequence child_sequence typed_sequence main_turn interaction input_actor actors anonymous_actors unknown_actors pending_callbacks codex"
				case "actors/*":
					allowed = "sequence start_sequence active"
				case "codex":
					allowed = "native_incarnation feed_epoch thread_id sequence last_event_hash turn_id owner_revision state cause terminal_sequence incomplete unknown_custody denied user_sequence commentary_sequence tool_result_sequence tool_failure_sequence last_item_sequence items children decisions stimuli"
				case "codex/items/*":
					allowed = "actor_thread_id collab_tool turn_id kind phase start_sequence complete native_incarnation feed_epoch"
				case "codex/children/*":
					allowed = "parent_thread_id awaiting_turn turn_id child_turn_id active native_incarnation feed_epoch"
				case "codex/decisions/*":
					allowed = "actor_thread_id request_id turn_id kind native_incarnation feed_epoch"
				case "codex/stimuli/*":
					allowed = "native_incarnation feed_epoch actor_thread_id turn_id item_id receiver_thread_id previous_turn_id start_sequence completion_sequence phase"
				}
				if allowed != "" && !strings.Contains(" "+allowed+" ", " "+name+" ") {
					return invalid
				}
				next := name
				if path != "" {
					next = path + "/" + name
				}
				switch path {
				case "actors", "codex/items", "codex/children", "codex/decisions", "codex/stimuli":
					next = path + "/*"
				}

				if err = value(depth+1, next); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim('}') {
				return invalid
			}
		case '[':
			return invalid
		default:
			return invalid
		}
		return nil
	}
	if err := value(0, ""); err != nil {
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
		if c := record.Codex; c != nil {
			if c.UnknownCustody || c.State == "active" || len(c.Decisions) > 0 {
				return fmt.Errorf("persist: unresolved Codex execution custody")
			}
			for _, item := range c.Items {
				if !item.Complete {
					return fmt.Errorf("persist: unresolved Codex tool custody")
				}
			}
			for _, child := range c.Children {
				if child.Active {
					return fmt.Errorf("persist: unresolved Codex child custody")
				}
			}
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
