// Package voice owns bounded controller metadata; native CLIs own conversation history.
package voice

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

const (
	MaxReadCursorBytes   = 16 << 10
	MaxRecords           = 10000
	MaxStoreBytes        = 64 << 20
	MaxUtteranceBytes    = 64 << 10
	MaxControlFrameBytes = 128 << 10
	MaxStreamEvents      = 256
	MaxStreamBytes       = 1 << 20
)

var (
	ErrInvalid  = errors.New("voice: invalid input or transition")
	ErrConflict = errors.New("voice: identity already bound to different input")
	ErrStale    = errors.New("voice: stale controller or manager binding")
	ErrExpired  = errors.New("voice: expired or retired authority")
	ErrCapacity = errors.New("voice: durable capacity reached")
	ErrNotFound = errors.New("voice: record unavailable")
	ErrCorrupt  = errors.New("voice: corrupt or unsupported state; recovery required")
)

type ManagerBinding struct {
	NativeCLI             bool   `json:"native_cli,omitempty"`
	ConversationID        string `json:"conversation_id"`
	Provider              string `json:"provider"`
	SessionID             string `json:"session_id"`
	SessionInstance       string `json:"session_instance"`
	NativeSessionID       string `json:"native_session_id"`
	NativeGeneration      string `json:"native_generation"`
	ObservationGeneration string `json:"observation_generation"`
	LoginEpoch            string `json:"login_epoch"`
	Generation            uint64 `json:"generation"`
}

type Conversation struct {
	ReplacementAfter  int64                    `json:"replacement_after,omitempty"`
	FailedResume      *failedResume            `json:"failed_resume,omitempty"`
	NeedsHandoff      bool                     `json:"needs_handoff,omitempty"`
	LastOutput        uint64                   `json:"last_output,omitempty"`
	ManagerGeneration uint64                   `json:"manager_generation,omitempty"`
	Project           string                   `json:"project,omitempty"`
	Retirement        *schema.ManagerRetireReq `json:"retirement,omitempty"`
	Parked            bool                     `json:"parked,omitempty"`
	CustodyLost       bool                     `json:"custody_lost,omitempty"`
	LastSpeechEpoch   uint64                   `json:"last_speech_epoch,omitempty"`
	SchedulerPaused   bool                     `json:"scheduler_paused,omitempty"`
	ExternalObserved  bool                     `json:"external_observed,omitempty"`
	ExternalInput     uint64                   `json:"external_input,omitempty"`
	Binding           ManagerBinding           `json:"binding"`
	LastAdmission     uint64                   `json:"last_admission"`
	RetiredThrough    uint64                   `json:"retired_through"`
}

type Utterance struct {
	RecoveryOnly   bool   `json:"recovery_only,omitempty"`
	ID             string `json:"id"`
	ConversationID string `json:"conversation_id"`
	DeviceID       string `json:"device_id"`
	VoiceSessionID string `json:"voice_session_id"`
	MediaEpoch     uint64 `json:"media_epoch"`
	Phase          string `json:"phase"`
	Text           string `json:"text,omitempty"`
	TextHash       string `json:"text_hash,omitempty"`
	AudioHash      string `json:"audio_hash,omitempty"`
	Admission      uint64 `json:"admission,omitempty"`
}

type ReusedFinalReceipt struct {
	PublicationID string `json:"publication_id"`
	OutputID      string `json:"output_id"`
	NativeTurnID  string `json:"native_turn_id"`
	Sequence      uint64 `json:"sequence"`
}

type Admission struct {
	ReplyStallSince      int64                   `json:"reply_stall_since,omitempty"`
	ReplyReceiptChecks   int                     `json:"reply_receipt_checks,omitempty"`
	ReplyDeliveryUnknown bool                    `json:"reply_delivery_unknown,omitempty"`
	ReplyTracked         bool                    `json:"reply_tracked,omitempty"`
	ReplyWaitingForHuman bool                    `json:"reply_waiting_for_human,omitempty"`
	ReplyProgress        uint64                  `json:"reply_progress,omitempty"`
	ReplyCheckAfter      int64                   `json:"reply_check_after,omitempty"`
	ReplyRecovery        uint64                  `json:"reply_recovery,omitempty"`
	WaitingOnInput       string                  `json:"waiting_on_input,omitempty"`
	WaitingNotice        bool                    `json:"waiting_notice,omitempty"`
	ReusedFinal          *ReusedFinalReceipt     `json:"reused_final,omitempty"`
	RecoveryOutput       *DiscussionOutputSource `json:"recovery_output,omitempty"`
	WorkerAttempts       string                  `json:"worker_attempts,omitempty"`
	WorkerProjects       string                  `json:"worker_projects,omitempty"`
	SourceCutover        bool                    `json:"source_cutover,omitempty"`
	ProjectExplicit      bool                    `json:"project_explicit,omitempty"`
	QuestionBoundary     uint64                  `json:"question_boundary,omitempty"`
	HumanReply           string                  `json:"human_reply,omitempty"`
	Origin               *schema.MessageOrigin   `json:"origin,omitempty"`
	Project              string                  `json:"project,omitempty"`
	NativeBindingRetired bool                    `json:"native_binding_retired,omitempty"`
	RecoveryOnly         bool                    `json:"recovery_only,omitempty"`
	RecoveryAcknowledged bool                    `json:"recovery_acknowledged,omitempty"`
	OperationID          string                  `json:"operation_id,omitempty"`
	Request              json.RawMessage         `json:"request,omitempty"`
	Phase                string                  `json:"phase,omitempty"`
	NativeTurnID         string                  `json:"native_turn_id,omitempty"`
	Reason               string                  `json:"reason,omitempty"`
	ExternalInput        uint64                  `json:"external_input,omitempty"`
	InputText            string                  `json:"input_text,omitempty"`
	ConversationID       string                  `json:"conversation_id"`
	Sequence             uint64                  `json:"sequence"`
	SourceID             string                  `json:"source_id"`
	NativeInputID        string                  `json:"native_input_id,omitempty"`
	SettledAt            time.Time               `json:"settled_at,omitempty"`
}

type Action struct {
	ID             string          `json:"id"`
	ConversationID string          `json:"conversation_id"`
	Admission      uint64          `json:"admission"`
	NativeInputID  string          `json:"native_input_id"`
	Target         string          `json:"target"`
	OperationID    string          `json:"operation_id"`
	Request        json.RawMessage `json:"request"`
	RequestHash    string          `json:"request_hash"`
	State          string          `json:"state"`
	Receipt        json.RawMessage `json:"receipt,omitempty"`
	SettledAt      time.Time       `json:"settled_at,omitempty"`
}

type Request struct {
	DiscussionAttempts string                 `json:"discussion_attempts,omitempty"`
	DiscussionProjects string                 `json:"discussion_projects,omitempty"`
	DiscussionSource   DiscussionOutputSource `json:"discussion_source,omitzero"`
	OutputSequence     uint64                 `json:"output_sequence,omitempty"`
	Exported           bool                   `json:"exported,omitempty"`
	RecoveryContext    bool                   `json:"recovery_context,omitempty"`
	ID                 string                 `json:"id"`
	ConversationID     string                 `json:"conversation_id"`
	Kind               string                 `json:"kind"` // manager or native
	NativeInputID      string                 `json:"native_input_id"`
	NativeTurnID       string                 `json:"native_turn_id"`
	TaskID             string                 `json:"task_id"`
	Payload            json.RawMessage        `json:"payload"`
	Hash               string                 `json:"hash"`
	ExpiresAt          time.Time              `json:"expires_at"`
	State              string                 `json:"state"`
	AnswerID           string                 `json:"answer_id,omitempty"`
	Answer             json.RawMessage        `json:"answer,omitempty"`
}

type SpeechPermit struct {
	ConversationID string `json:"conversation_id"`
	Admission      uint64 `json:"admission"`
	NativeInputID  string `json:"native_input_id"`
	NativeTurnID   string `json:"native_turn_id"`
	Epoch          uint64 `json:"epoch"`
	Revoked        bool   `json:"revoked"`
}

type Publication struct {
	ReplyMonitor       bool                   `json:"reply_monitor,omitempty"`
	DiscussionAttempts string                 `json:"discussion_attempts,omitempty"`
	DiscussionProjects string                 `json:"discussion_projects,omitempty"`
	DiscussionSource   DiscussionOutputSource `json:"discussion_source,omitzero"`
	OutputSequence     uint64                 `json:"output_sequence,omitempty"`
	Exported           bool                   `json:"exported,omitempty"`
	Admission          uint64                 `json:"admission"`
	SpeechEpoch        uint64                 `json:"speech_epoch"`
	ID                 string                 `json:"id"`
	ConversationID     string                 `json:"conversation_id"`
	NativeTurnID       string                 `json:"native_turn_id"`
	Sequence           uint64                 `json:"sequence"`
	Text               string                 `json:"text"`
	TextHash           string                 `json:"text_hash"`
	Final              bool                   `json:"final"`
	Speak              bool                   `json:"speak"`
}

type JournalCursor struct {
	Binding  string `json:"binding"`
	Sequence uint64 `json:"sequence"`
}

// JournalSnapshot is supplied only after the source adapter reconciles a trusted
// daemon snapshot. Previous fences an out-of-order snapshot or source replacement.
type JournalSnapshot struct {
	Previous JournalCursor   `json:"previous"`
	Cursor   JournalCursor   `json:"cursor"`
	Metadata json.RawMessage `json:"metadata"`
}

type Event struct {
	VMWorkerStatus  bool            `json:"vm_worker_status,omitempty"`
	WorkerMessage   bool            `json:"worker_message,omitempty"`
	WorkerProof     bool            `json:"worker_proof,omitempty"`
	NativeTurnID    string          `json:"native_turn_id,omitempty"`
	JobID           string          `json:"job_id,omitempty"`
	AttemptID       string          `json:"attempt_id,omitempty"`
	RecoveryOnly    bool            `json:"recovery_only,omitempty"`
	ID              string          `json:"id"`
	ConversationID  string          `json:"conversation_id"`
	Cursor          JournalCursor   `json:"cursor"`
	ExaminedThrough JournalCursor   `json:"examined_through"`
	Metadata        json.RawMessage `json:"metadata"`
	WakeID          string          `json:"wake_id"`
}

type Wake struct {
	WorkerReplyExported bool            `json:"worker_reply_exported,omitempty"`
	WorkerReply         string          `json:"worker_reply,omitempty"`
	RecoveryOnly        bool            `json:"recovery_only,omitempty"`
	ID                  string          `json:"id"`
	ConversationID      string          `json:"conversation_id"`
	EventID             string          `json:"event_id"`
	OperationID         string          `json:"operation_id"`
	Request             json.RawMessage `json:"request"`
	RequestHash         string          `json:"request_hash"`
	State               string          `json:"state"`
	Admission           uint64          `json:"admission,omitempty"`
	NativeInputID       string          `json:"native_input_id,omitempty"`
}

type WorkerAssociation struct {
	Mailbox          bool   `json:"mailbox,omitempty"`
	VMChild          bool   `json:"vm_child,omitempty"`
	EvidenceLimited  bool   `json:"evidence_limited,omitempty"`
	Parent           string `json:"parent,omitempty"`
	Ended            bool   `json:"ended,omitempty"`
	Project          string `json:"project,omitempty"`
	InitialSourceID  string `json:"initial_source_id,omitempty"`
	AttemptID        string `json:"attempt_id,omitempty"`
	JobID            string `json:"job_id,omitempty"`
	RecoveryRequired bool   `json:"recovery_required,omitempty"`
	Reserved         bool   `json:"reserved,omitempty"`
	ConversationID   string `json:"conversation_id"`
	Session          string `json:"session"`
	SessionInstance  string `json:"session_instance"`
	Admission        uint64 `json:"admission"`
	SourceID         string `json:"source_id"`
	ActionID         string `json:"action_id"`
}

// Enrolled launches retain an action receipt even if an older roster observer set VM flags.
func (w WorkerAssociation) supervisorMailbox() bool {
	return w.VMChild && w.Mailbox && w.ActionID == ""
}

type State struct {
	changes             *stateChanges
	Version             int                          `json:"version"`
	Generation          uint64                       `json:"generation"`
	CurrentConversation string                       `json:"current_conversation"`
	Workers             map[string]WorkerAssociation `json:"workers"`
	Conversations       map[string]Conversation      `json:"conversations"`
	Utterances          map[string]Utterance         `json:"utterances"`
	Admissions          map[string]Admission         `json:"admissions"`
	Actions             map[string]Action            `json:"actions"`
	ClosedTurns         map[string]bool              `json:"closed_turns"`
	CancelledTasks      map[string]bool              `json:"cancelled_tasks"`
	Requests            map[string]Request           `json:"requests"`
	Permits             map[string]SpeechPermit      `json:"permits"`
	Publications        map[string]Publication       `json:"publications"`
	Journal             map[string]JournalCursor     `json:"journal"`
	JournalSnapshots    map[string]JournalSnapshot   `json:"journal_snapshots"`
	Events              map[string]Event             `json:"events"`
	Wakes               map[string]Wake              `json:"wakes"`
}
