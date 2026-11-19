// Package schedule stores a user's automation catalog, calendar claims, and
// execution history. Execution workers operate on persisted occurrence IDs.
package schedule

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	WorkspaceWorktree  = "worktree"
	WorkspaceDirectory = "directory"

	AuthorityObserve        = "observe"
	AuthorityWorkspaceWrite = "workspace-write"
	AuthorityAgentDefault   = "agent-default"

	RouteCodex    = "codex"
	RouteClaude   = "claude"
	RouteAgy      = "agy"
	RouteHermes   = "hermes"
	RouteOpenCode = "opencode"
)

type JobState string

const (
	JobActive   JobState = "active"
	JobPaused   JobState = "paused"
	JobArchived JobState = "archived"
)

type OccurrenceState string

const (
	OccurrenceQueued     OccurrenceState = "queued"
	OccurrenceWorking    OccurrenceState = "working"
	OccurrenceNeedsInput OccurrenceState = "needs-input"
	OccurrenceIncomplete OccurrenceState = "incomplete"
	OccurrenceExhausted  OccurrenceState = "exhausted"
	OccurrenceReady      OccurrenceState = "ready"
	OccurrenceAttention  OccurrenceState = "attention"
	OccurrenceBlocked    OccurrenceState = "blocked"
	OccurrenceMissed     OccurrenceState = "missed"
)

type AttemptState string

const (
	AttemptDispatching AttemptState = "dispatching"
	AttemptRunning     AttemptState = "running"
	AttemptStopping    AttemptState = "stopping"
	AttemptDeferred    AttemptState = "workspace_busy"
	AttemptRejected    AttemptState = "rejected"
	AttemptStopped     AttemptState = "stopped"
	AttemptComplete    AttemptState = "complete"
	AttemptUnknown     AttemptState = "unknown"
)

var (
	ErrNotFound = errors.New("schedule record not found")
	ErrArchived = errors.New("schedule job is archived")
	ErrConflict = errors.New("schedule job revision changed")
)

type JobSpec struct {
	Name         string        `json:"name"`
	Prompt       string        `json:"prompt"`
	Directory    string        `json:"directory"`
	Authority    string        `json:"authority"`
	Workspace    string        `json:"workspace"`
	Agents       []string      `json:"agents"`
	Calendar     Calendar      `json:"calendar"`
	Grace        time.Duration `json:"grace"`
	AllowOverlap bool          `json:"allow_overlap"`
}

type Job struct {
	ID         string    `json:"id"`
	RevisionID string    `json:"revision_id"`
	Spec       JobSpec   `json:"spec"`
	State      JobState  `json:"state"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type Revision struct {
	ID        string    `json:"id"`
	JobID     string    `json:"job_id"`
	Spec      JobSpec   `json:"spec"`
	CreatedAt time.Time `json:"created_at"`
}

type Template struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Prompt    string    `json:"prompt"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Occurrence struct {
	ID              string          `json:"id"`
	JobID           string          `json:"job_id"`
	RevisionID      string          `json:"revision_id"`
	RequestHash     string          `json:"request_hash"`
	Spec            JobSpec         `json:"spec"`
	DueAt           time.Time       `json:"due_at"`
	ClaimedAt       time.Time       `json:"claimed_at"`
	Manual          bool            `json:"manual"`
	State           OccurrenceState `json:"state"`
	Detail          string          `json:"detail,omitempty"`
	Workspace       string          `json:"workspace,omitempty"`
	SourceDirectory string          `json:"source_directory,omitempty"`
	Attempts        []Attempt       `json:"attempts,omitempty"`
	Completion      *Completion     `json:"completion,omitempty"`
	ProjectedAt     time.Time       `json:"projected_at,omitempty"`
	ReleasedAt      time.Time       `json:"released_at,omitempty"`
	CompletedAt     time.Time       `json:"completed_at,omitempty"`
}

type Completion struct {
	Summary string   `json:"summary"`
	Links   []string `json:"links,omitempty"`
}

type Attempt struct {
	ID                  string       `json:"id"`
	Agent               string       `json:"agent"`
	OperationID         string       `json:"operation_id,omitempty"`
	CompletionTokenHash string       `json:"completion_token_hash,omitempty"`
	PromptStarted       bool         `json:"prompt_started,omitempty"`
	PromptDelivered     bool         `json:"prompt_delivered,omitempty"`
	DispatchStarted     bool         `json:"dispatch_started,omitempty"`
	SessionID           string       `json:"session_id,omitempty"`
	Workspace           string       `json:"workspace,omitempty"`
	Detail              string       `json:"detail,omitempty"`
	State               AttemptState `json:"state"`
	StartedAt           time.Time    `json:"started_at"`
	UpdatedAt           time.Time    `json:"updated_at"`
}

// ValidateJob checks the common definition rules. The execution layer checks
// the capabilities of individual routes for the requested authority.
func ValidateJob(spec JobSpec) error {
	_, err := PrepareJob(spec)
	return err
}

// PrepareJob validates and canonicalizes the working directory.
func PrepareJob(spec JobSpec) (JobSpec, error) {
	if err := validateNamePrompt(spec.Name, spec.Prompt); err != nil {
		return spec, err
	}
	switch spec.Authority {
	case AuthorityObserve, AuthorityWorkspaceWrite, AuthorityAgentDefault:
	default:
		return spec, fmt.Errorf("select an authority")
	}
	switch spec.Workspace {
	case WorkspaceWorktree, WorkspaceDirectory:
	default:
		return spec, fmt.Errorf("select a workspace mode")
	}
	if len(spec.Agents) == 0 {
		return spec, fmt.Errorf("select at least one agent")
	}
	seen := make(map[string]bool, len(spec.Agents))
	for _, agent := range spec.Agents {
		switch agent {
		case RouteCodex, RouteClaude, RouteAgy, RouteHermes, RouteOpenCode:
		default:
			return spec, fmt.Errorf("unknown agent %q", agent)
		}
		if seen[agent] {
			return spec, fmt.Errorf("agent %q is listed twice", agent)
		}
		seen[agent] = true
	}
	if spec.Grace < 0 {
		return spec, fmt.Errorf("catch-up grace must be nonnegative")
	}
	if _, err := spec.Calendar.compile(); err != nil {
		return spec, err
	}
	dir, err := resolveDirectory(spec.Directory)
	if err != nil {
		return spec, err
	}
	spec.Directory = dir
	return spec, nil
}

func validateNamePrompt(name, prompt string) error {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(prompt) == "" {
		return fmt.Errorf("name and prompt are required")
	}
	if !utf8.ValidString(name) || !utf8.ValidString(prompt) ||
		strings.ContainsRune(name, 0) || strings.ContainsRune(prompt, 0) {
		return fmt.Errorf("name and prompt must contain valid UTF-8 text")
	}
	if strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return fmt.Errorf("name must be one line without control characters")
	}
	if strings.IndexFunc(prompt, func(r rune) bool {
		return unicode.IsControl(r) && r != '\n' && r != '\t' && r != '\r'
	}) >= 0 {
		return fmt.Errorf("prompt contains an unsupported control character")
	}
	return nil
}

func resolveDirectory(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("select a directory")
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), "/"))
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve directory: %w", err)
	}
	if !utf8.ValidString(path) {
		return "", fmt.Errorf("directory path must contain valid UTF-8 text")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%q is not a directory", path)
	}
	return path, nil
}

const ulidAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewID mints a ULID with a 48-bit timestamp and 80 bits of random entropy.
func NewID(now time.Time) string {
	var raw [16]byte
	binary.BigEndian.PutUint64(raw[:8], uint64(now.UnixMilli())<<16)
	rand.Read(raw[6:])
	var out [26]byte
	var acc uint32
	pending, pos := 2, 0 // A ULID's first group has two leading zero bits.
	for _, octet := range raw {
		acc = acc<<8 | uint32(octet)
		pending += 8
		for pending >= 5 {
			pending -= 5
			out[pos] = ulidAlphabet[(acc>>pending)&31]
			pos++
		}
	}
	return string(out[:])
}

func validULID(id string) bool {
	if len(id) != 26 || id[0] < '0' || id[0] > '7' {
		return false
	}
	for _, b := range id {
		if !strings.ContainsRune(ulidAlphabet, b) {
			return false
		}
	}
	return true
}

func scheduledID(jobID string, at time.Time) string {
	sum := sha256.Sum256([]byte(jobID + "/" + at.UTC().Format(time.RFC3339Nano)))
	return hex.EncodeToString(sum[:])
}

func requestHash(o Occurrence) (string, error) {
	// Only immutable dispatch inputs participate in this hash.
	data, err := json.Marshal(struct {
		ID, JobID, RevisionID string
		Spec                  JobSpec
		DueAt, ClaimedAt      time.Time
		Manual                bool
	}{o.ID, o.JobID, o.RevisionID, o.Spec, o.DueAt, o.ClaimedAt, o.Manual})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
