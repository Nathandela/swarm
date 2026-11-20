package voice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

const DiscussionConversationPrefix = schema.DiscussionConversationPrefix

var ErrDiscussionSourceChanged = errors.New("voice: verified Discord source changed before dispatch")
var errDiscussionGrantUnverified = errors.New("voice: Discord authority could not be verified before dispatch; retry the same request after access is restored")

// DiscussionScope comes from the verified connector destination and local enrollment.
type DiscussionScope struct {
	VMProjectAccess    bool
	DMOwnerID          string
	ManagerSettingsDir string
	DiscussionID       string
	InstallationID     string
	GuildID            string
	ThreadID           string
	VerifyAudience     func(context.Context) error
	ProjectEnrolled    func(string) bool
	VerifyOrigin       func(context.Context, schema.MessageOrigin) error
	VerifyGrant        func(context.Context, schema.MessageOrigin) error
	journalReady       func() bool
	History            func(context.Context, DiscussionHistoryInput) (json.RawMessage, error)
}

type DiscussionHistoryInput struct {
	Source string `json:"source,omitempty"`
	Cursor string `json:"cursor,omitempty"`
	Anchor string `json:"anchor,omitempty"`
}

func (s *DiscussionScope) validateOrigin(origin schema.MessageOrigin) bool {
	if s.DMOwnerID != "" && (origin.Kind != "owner_direct" || origin.ActorID != s.DMOwnerID || origin.RequesterID != s.DMOwnerID) {
		return false
	}
	return (origin.Kind == "external" || origin.Kind == "owner_direct") && origin.Validate() == nil &&
		origin.DiscussionID == s.DiscussionID && origin.InstallationID == s.InstallationID && origin.GuildID == s.GuildID && origin.ChannelID == s.ThreadID && origin.MessageID != ""
}

func (c *Controller) submitDiscussion(origin schema.MessageOrigin, text, questionID, questionHash string) (Admission, error) {
	return c.submitDiscussionInProject(origin, text, text, questionID, questionHash, "", false)
}

func (c *Controller) submitDiscussionInProject(origin schema.MessageOrigin, text, answer, questionID, questionHash, project string, explicit bool) (Admission, error) {
	if c.discussion == nil || !c.discussion.validateOrigin(origin) || (text == "" || len(text) > MaxUtteranceBytes || !utf8.ValidString(text) || strings.ContainsRune(text, 0)) {
		return Admission{}, ErrInvalid
	}
	if questionID != "" {
		raw, err := json.Marshal(answer)
		if err != nil {
			return Admission{}, err
		}
		a, err := c.answerDiscussionQuestion(questionID, questionHash, origin, project, explicit, raw)
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
		a, err := findDiscussionAdmission(*st, origin)
		if err != nil {
			return err
		}
		if a.Origin != nil && *a.Origin == origin {
			result = a
			return nil
		}
		a, err = admitDiscussionRevision(st, c.binding, a, source)
		if err != nil {
			return err
		}
		a.Origin = &origin
		a.InputText = text
		a.HumanReply = answer
		a.QuestionBoundary = st.Conversations[c.binding.ConversationID].LastOutput
		a.Project, a.ProjectExplicit = project, explicit
		putRecord(st, "admissions", &st.Admissions, admissionKey(c.binding.ConversationID, a.Sequence), a)
		result = a
		return nil
	})
	return cloneAdmission(result), err
}

func admitDiscussionRevision(st *State, b ManagerBinding, previous Admission, source string) (Admission, error) {
	if previous.Origin == nil {
		return allocateAdmission(st, b, source)
	}
	if previous.Phase != "superseded" || previous.SourceCutover || !previous.SettledAt.IsZero() {
		return Admission{}, ErrConflict
	}
	return Admission{ConversationID: b.ConversationID, Sequence: previous.Sequence, SourceID: source}, nil
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
	verifiedResult := event.WorkerProof && event.NativeTurnID != "" && event.ID == discussionEvidenceID(worker)
	if (!verifiedResult && !event.WorkerMessage) || event.JobID == "" || event.AttemptID == "" ||
		worker.JobID != event.JobID || worker.AttemptID != event.AttemptID || worker.RecoveryRequired || worker.Ended ||
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
	return c.discussion.projectPath(root, candidate)
}

func (s *DiscussionScope) projectPath(root, candidate string) bool {
	if !filepath.IsAbs(root) || root != candidate || s != nil && s.ProjectEnrolled != nil && !s.ProjectEnrolled(root) {
		return false
	}
	resolved, err := filepath.EvalSymlinks(root)
	return err == nil && resolved == root
}

// Only an explicit switch overrides a project selected by an earlier queued human.
func (c *Controller) prepareDiscussionProject(expected Admission) (Admission, error) {
	result := expected
	err := c.store.update(func(st *State) error {
		if err := checkBinding(st, c.binding); err != nil {
			return err
		}
		k := admissionKey(c.binding.ConversationID, expected.Sequence)
		current, ok := st.Admissions[k]
		if !ok || current.Phase != "" || current.Origin == nil || *current.Origin != *expected.Origin {
			return ErrConflict
		}
		inherited := st.Conversations[c.binding.ConversationID].Project
		if !current.ProjectExplicit && c.projectPath(inherited, inherited) {
			current.Project = inherited
		} else if !c.projectPath(current.Project, current.Project) {
			current.Project = ""
		}
		putRecord(st, "admissions", &st.Admissions, k, current)
		result = current
		return nil
	})
	return result, err
}

func (c *Controller) verifyDiscussionLaunch(ctx context.Context, request controllerActionRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if request.Origin == nil || !c.discussion.validateOrigin(*request.Origin) || request.Launch == nil {
		return ErrExpired
	}
	path := request.Project
	if request.Tool == "swarm_create" {
		path = request.Launch.Cwd
		if path != request.Project {
			return fmt.Errorf("%w: select an enrolled project before dispatching work", ErrInvalid)
		}
	}
	if !c.projectPath(request.Project, path) {
		return fmt.Errorf("%w: select a currently enrolled project for delegated work", ErrExpired)
	}
	if c.discussion.VerifyAudience != nil {
		if err := c.discussion.VerifyAudience(ctx); err != nil {
			return errDiscussionGrantUnverified
		}
	}
	if c.discussion.VerifyGrant != nil {
		if err := c.discussion.VerifyGrant(ctx, *request.Origin); err != nil {
			if errors.Is(err, ErrDiscussionSourceChanged) {
				return ErrDiscussionSourceChanged
			}
			return errDiscussionGrantUnverified
		}
	}
	return nil
}
