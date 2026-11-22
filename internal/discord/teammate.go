package discord

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/Nathandela/swarm/internal/workermail"

	"github.com/bwmarrin/discordgo"
)

// TeammateManager admits stable operations and acknowledges publications through a durable callback.
type TeammateManager interface {
	AdmissionState(context.Context, TeammateAdmission) (string, error)
	CancelAdmission(context.Context, TeammateAdmission) error
	Admit(context.Context, TeammateAdmission, TeammateHistoryReader) error
	Sync(context.Context, TeammateDiscussion, TeammateHistoryReader, func(TeammatePublication) error) error
}

const (
	TeammateAdmissionMissing            = "missing"
	TeammateAdmissionAccepted           = "accepted"
	TeammateAdmissionCutover            = "cutover"
	TeammateAdmissionSuperseded         = "superseded"
	TeammateAdmissionCancelled          = "cancelled"
	TeammateSystemContinuityGap         = "manager_continuity_gap"
	TeammateSystemWorkerEvidenceLimited = "worker_evidence_limited"
	teammateStateAccepted               = "accepted"
)

type TeammateNativeStatus struct {
	ReplyReceipts       [][]PublicationProof    `json:"-"`
	UnconfirmedReplies  int                     `json:"unconfirmed_replies,omitempty"`
	PendingReplies      int                     `json:"pending_replies,omitempty"`
	PendingAdmissions   int                     `json:"pending_admissions,omitempty"`
	Notes               []workermail.NoteStatus `json:"worker_notes,omitempty"`
	RetirementOperation string                  `json:"retirement_operation,omitempty"`
	OldestPendingNote   time.Time               `json:"oldest_pending_note,omitzero"`
	State               string                  `json:"state"`
	ManagerSession      string                  `json:"manager_session,omitempty"`
	ManagerInstance     string                  `json:"manager_instance,omitempty"`
	Project             string                  `json:"project,omitempty"`
	Diagnostic          string                  `json:"diagnostic,omitempty"`
	ExternalInput       uint64                  `json:"external_input"`
	Generation          uint64                  `json:"generation"`
	Jobs                []TeammateJobStatus     `json:"jobs,omitempty"`
}

type TeammateJobStatus struct {
	NativeTurnID          string                        `json:"native_turn_id,omitempty"`
	RecoveryRequired      bool                          `json:"recovery_required,omitempty"`
	NativeTurnPublication *workermail.PublicationStatus `json:"native_turn_publication,omitempty"`
	ID                    string                        `json:"id"`
	AttemptID             string                        `json:"attempt_id,omitempty"`
	Session               string                        `json:"session,omitempty"`
	Instance              string                        `json:"instance,omitempty"`
	Project               string                        `json:"project,omitempty"`
	State                 string                        `json:"state"`
	Diagnostic            string                        `json:"diagnostic,omitempty"`
}

type TeammateDiscussion struct {
	ID, InstallationID string
	SupervisorSocket   string `json:"supervisor_socket,omitempty"`
	Route, Starter     Route
	ParentAnchor       string
	Gap                uint64
}

type TeammateAdmission struct {
	ExpectReply                          bool   `json:",omitempty"`
	AttachmentManifest                   string `json:",omitempty"`
	RequireMention                       bool
	Supersedes                           string
	Discussion                           TeammateDiscussion
	OperationID, Revision, Text, ReplyTo string
	QuestionID, QuestionHash             string
	Route                                Route
	Author                               Actor
	InvokerID                            string
	Authority                            Authority
	Sequence                             uint64
}

type TeammatePublication struct {
	BodyHash                                                          string `json:",omitempty"`
	ActivationGeneration                                              uint64 `json:",omitempty"`
	SystemCause                                                       string
	ID, Body, AdmissionID, JobID, AttemptID, QuestionID, QuestionHash string
	Sequence                                                          uint64
}

func PublicationBodyHash(body string) string { return digest(body) }

type TeammateHistoryReader func(context.Context, TeammateHistoryQuery) (TeammateHistoryPage, error)

var ErrTeammateSourceChanged = errors.New("discord: source message changed before native submission")

// TeammateInputHold describes a durable human admission awaiting native input.
type TeammateInputHold struct {
	AdmissionID     string
	DirectInput     bool
	CustodyLost     bool
	EffectUncertain bool
}

func (h *TeammateInputHold) Error() string {
	if h.EffectUncertain {
		return "discord: previous dispatcher submission outcome is unconfirmed"
	}
	if h.DirectInput {
		return "discord: dispatcher delivery paused by direct terminal input"
	}
	return "discord: dispatcher delivery awaits native turn confirmation"
}

// TeammateStartupNotice carries locally generated, audience-safe guidance only.
type TeammateStartupNotice struct{ Body string }

func (n *TeammateStartupNotice) Error() string { return n.Body }
func (n *TeammateStartupNotice) Unwrap() error { return ErrTeammateManagerUnavailable }

var ErrTeammateManagerUnavailable = errors.New("discord: teammate manager is unavailable")

type TeammateHistoryQuery struct {
	Publication                                *TeammatePublication
	Source, Cursor, Anchor                     string
	AdmissionID, Revision, MessageID, AuthorID string
}
type TeammateHistoryPage struct {
	ExpectsReply                        bool     `json:",omitempty"`
	ConfirmedMessageIDs                 []string `json:",omitempty"`
	EvidenceFragment                    string   `json:",omitempty"`
	FragmentMessageID                   string   `json:",omitempty"`
	StarterCursor                       string   `json:",omitempty"`
	Messages                            []TeammateHistoryMessage
	Cursor, HighWater, LowWater, Notice string
	Starter                             *TeammateHistoryMessage
	ReplyChain                          []TeammateHistoryMessage
}
type TeammateHistoryMessage struct {
	ContextMessage
	ReplyTo     string
	Attachments []TeammateAttachment
}
type TeammateAttachment struct {
	ID, Name, ContentType, Availability string
	Size                                int
}

func snowflake(id string) bool {
	value, err := strconv.ParseUint(id, 10, 64)
	return err == nil && value != 0 && strconv.FormatUint(value, 10) == id
}

func snowflakeLess(a, b string) bool {
	return len(a) < len(b) || len(a) == len(b) && a < b
}

func teammateHuman(m *discordgo.Message, botID string) bool {
	return m != nil && m.Author != nil && snowflake(m.Author.ID) && m.Author.ID != botID &&
		!m.Author.Bot && !m.Author.System && m.WebhookID == "" &&
		(m.Type == discordgo.MessageTypeDefault || m.Type == discordgo.MessageTypeReply)
}

func teammateMention(m *discordgo.Message, botID string) bool {
	native := false
	for _, mention := range m.Mentions {
		if mention != nil && mention.ID == botID {
			native = true
			break
		}
	}
	if !native || botID == "" {
		return false
	}
	fenced := false
	for _, line := range strings.Split(m.Content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, ">>>") && !fenced {
			break
		}
		if strings.HasPrefix(trimmed, ">") && !fenced {
			continue
		}
		for i := 0; i < len(line); i++ {
			switch {
			case line[i] == '\\':
				i++
			case strings.HasPrefix(line[i:], "```"):
				fenced = !fenced
				i += 2
			case line[i] == '`' && !fenced:
				width := strings.IndexFunc(line[i:], func(r rune) bool { return r != '`' })
				if width < 0 {
					break
				}
				end := strings.Index(line[i+width:], strings.Repeat("`", width))
				if end >= 0 {
					i += width + end + width - 1
				}
			case line[i] == '<' && !fenced:
				if strings.HasPrefix(line[i:], "<@"+botID+">") || strings.HasPrefix(line[i:], "<@!"+botID+">") {
					return true
				}
			}
		}
	}
	return false
}

func teammateReply(m *discordgo.Message) string {
	if ref := m.MessageReference; ref != nil && ref.Type == discordgo.MessageReferenceTypeDefault &&
		(ref.ChannelID == "" || ref.ChannelID == m.ChannelID) && (ref.GuildID == "" || m.GuildID == "" || ref.GuildID == m.GuildID) && snowflake(ref.MessageID) {
		return ref.MessageID
	}
	return ""
}
