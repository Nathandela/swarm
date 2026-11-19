package voice

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

const DiscussionConversationPrefix = "discord-teammate-"

var ErrDiscussionSourceChanged = errors.New("voice: verified Discord source changed before dispatch")

// DiscussionScope comes from the verified connector destination and local enrollment.
type DiscussionScope struct {
	DiscussionID   string
	InstallationID string
	GuildID        string
	ThreadID       string
	VerifyAudience func(context.Context) error
	VerifyOrigin   func(context.Context, schema.MessageOrigin) error
	journalReady   func() bool
	History        func(context.Context, DiscussionHistoryInput) (json.RawMessage, error)
}

type DiscussionHistoryInput struct {
	Source string `json:"source,omitempty"`
	Cursor string `json:"cursor,omitempty"`
	Anchor string `json:"anchor,omitempty"`
}

func (s *DiscussionScope) validateOrigin(origin schema.MessageOrigin) bool {
	return (origin.Kind == "external" || origin.Kind == "owner_direct") && origin.Validate() == nil &&
		origin.DiscussionID == s.DiscussionID && origin.InstallationID == s.InstallationID && origin.GuildID == s.GuildID && origin.ChannelID == s.ThreadID && origin.MessageID != ""
}

func (c *Controller) submitDiscussion(origin schema.MessageOrigin, text, questionID, questionHash string) (Admission, error) {
	return c.submitDiscussionInProject(origin, text, text, questionID, questionHash, "")
}

func (c *Controller) submitDiscussionInProject(origin schema.MessageOrigin, text, answer, questionID, questionHash, project string) (Admission, error) {
	if c.discussion == nil || !c.discussion.validateOrigin(origin) || (text == "" || len(text) > MaxUtteranceBytes || !utf8.ValidString(text) || strings.ContainsRune(text, 0)) {
		return Admission{}, ErrInvalid
	}
	if questionID != "" {
		raw, err := json.Marshal(answer)
		if err != nil {
			return Admission{}, err
		}
		a, err := c.answerDiscussionQuestion(questionID, questionHash, origin, project, raw)
		if !errors.Is(err, ErrExpired) && !errors.Is(err, ErrConflict) && !errors.Is(err, ErrNotFound) {
			return a, err
		}
	}
	var result Admission
	err := c.store.update(func(st *State) error {
		if err := checkBinding(st, c.binding); err != nil {
			return err
		}
		source := "discussion:" + origin.RequestID
		for _, a := range st.Admissions {
			if a.ConversationID != c.binding.ConversationID || a.SourceID != source {
				continue
			}
			if a.Origin == nil || *a.Origin != origin {
				return ErrConflict
			}
			result = a
			return nil
		}
		a, err := allocateAdmission(st, c.binding, source)
		if err != nil {
			return err
		}
		a.QuestionBoundary = st.Conversations[c.binding.ConversationID].LastOutput
		if a.QuestionBoundary != 0 {
			a.HumanReply = answer
		}
		a.Origin = &origin
		a.InputText = text
		a.Project = project
		putRecord(st, "admissions", &st.Admissions, admissionKey(c.binding.ConversationID, a.Sequence), a)
		result = a
		return nil
	})
	return cloneAdmission(result), err
}

func (c *Controller) discussionEffect(admission Admission, name string) error {
	if c.discussion == nil {
		return nil
	}
	switch name {
	case "swarm_current_request", "swarm_decision", "swarm_interrupt":
		return ErrControllerUnsupported
	case "swarm_send":
		if !admission.RecoveryOnly && strings.HasPrefix(admission.SourceID, "wake:") {
			return nil
		}
		fallthrough
	case "swarm_create", "swarm_resume":
		if admission.Origin == nil || !c.discussion.validateOrigin(*admission.Origin) {
			return ErrExpired
		}
	}
	return nil
}

func (c *Controller) discussionContinuation(st State, admission Admission, target, actionID string) (*schema.MessageOrigin, string, error) {
	wakeID, ok := strings.CutPrefix(admission.SourceID, "wake:")
	if !ok || admission.RecoveryOnly {
		return nil, "", ErrExpired
	}
	wake := st.Wakes[key(c.binding.ConversationID, wakeID)]
	event := st.Events[key(c.binding.ConversationID, wake.EventID)]
	worker := st.Workers[key(c.binding.ConversationID, target)]
	if !event.WorkerProof || event.NativeTurnID == "" || event.JobID == "" || event.AttemptID == "" ||
		worker.JobID != event.JobID || worker.AttemptID != event.AttemptID || event.ID != discussionEvidenceID(worker) || worker.RecoveryRequired || worker.Ended ||
		!c.projectPath(worker.Project, worker.Project) {
		return nil, "", ErrExpired
	}
	var coordinates struct{ Session, Instance string }
	if json.Unmarshal(event.Metadata, &coordinates) != nil || key(coordinates.Session, coordinates.Instance) != target {
		return nil, "", ErrStale
	}
	actionReference, err := c.actionReference(st, actionID, admission.Sequence, true)
	if err != nil {
		return nil, "", err
	}
	for _, prior := range st.Actions {
		if prior.Admission == admission.Sequence && prior.ConversationID == c.binding.ConversationID && prior.ID != actionReference {
			return nil, "", ErrExpired
		}
	}
	for _, original := range st.Admissions {
		if original.ConversationID == c.binding.ConversationID && original.SourceID == worker.InitialSourceID &&
			original.Origin != nil && c.discussion.validateOrigin(*original.Origin) {
			return original.Origin, wakeID, nil
		}
	}
	return nil, "", ErrExpired
}

func (c *Controller) projectPath(root, candidate string) bool {
	if !filepath.IsAbs(root) || root != candidate {
		return false
	}
	resolved, err := filepath.EvalSymlinks(root)
	return err == nil && resolved == root
}
