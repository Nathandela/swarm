package voice

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
)

const discussionEvidenceLimitNotice = "I couldn't verify the worker's final result within the configured evidence limit. The job and its native session are available in Swarm."

func discussionQuestionText(r Request) (string, error) {
	var question struct {
		Question string
		Choices  []string
	}
	if json.Unmarshal(r.Payload, &question) != nil {
		return "", ErrCorrupt
	}
	text := question.Question
	if len(question.Choices) > 0 {
		text += "\n" + strings.Join(question.Choices, "\n")
	}
	return text, nil
}

func addDiscussionScopeValue(encoded, value string) string {
	if value == "" {
		return encoded
	}
	var roots []string
	if encoded != "" && json.Unmarshal([]byte(encoded), &roots) != nil {
		return encoded
	}
	if slices.Contains(roots, value) {
		return encoded
	}
	roots = append(roots, value)
	slices.Sort(roots)
	raw, _ := json.Marshal(roots)
	return string(raw)
}

// ValidatePublication checks the immutable exported receipt and its durable job
// grant without needing a live manager or the original Discord message.
func (d *DiscussionController) ValidatePublication(ctx context.Context, output DiscussionOutput, bodyHash string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if (output.JobID == "") != (output.AttemptID == "") || bodyHash == "" {
		return ErrInvalid
	}
	st, err := d.store.readView()
	if err != nil {
		return err
	}
	conversation := d.cfg.ConversationID
	k := key(conversation, output.ID)
	var projects, readAttempts string
	if output.QuestionID != "" {
		r, ok := st.Requests[k]
		if !ok || !r.Exported || r.Kind != "manager" || r.ID != output.QuestionID || r.Hash != output.QuestionHash ||
			r.DiscussionSource != output.DiscussionOutputSource || r.OutputSequence != output.Sequence {
			return ErrStale
		}
		projects, readAttempts = r.DiscussionProjects, r.DiscussionAttempts
		body, err := discussionQuestionText(r)
		if err != nil {
			return err
		}
		if hashValue(body) != bodyHash {
			return ErrStale
		}
	} else {
		p, ok := st.Publications[k]
		if !ok || !p.Exported || output.QuestionHash != "" || p.DiscussionSource != output.DiscussionOutputSource ||
			p.OutputSequence != output.Sequence || hashValue(p.Text) != bodyHash {
			return ErrStale
		}
		projects, readAttempts = p.DiscussionProjects, p.DiscussionAttempts
	}
	if projects != "" {
		var roots []string
		if json.Unmarshal([]byte(projects), &roots) != nil {
			return ErrCorrupt
		}
		for _, root := range roots {
			if !d.scope.projectPath(root, root) {
				return ErrExpired
			}
		}
	}
	var attempts []string
	if readAttempts != "" && json.Unmarshal([]byte(readAttempts), &attempts) != nil {
		return ErrCorrupt
	}
	if output.AttemptID != "" && !slices.Contains(attempts, output.AttemptID) {
		attempts = append(attempts, output.AttemptID)
	}
	for _, id := range attempts {
		if err := d.validatePublicationAttempt(st, id); err != nil {
			return err
		}
	}
	return nil
}

func (d *DiscussionController) validatePublicationAttempt(st State, id string) error {
	if strings.HasPrefix(id, "vm-child-") {
		for _, w := range st.Workers {
			if w.VMChild && w.ConversationID == d.cfg.ConversationID && w.AttemptID == id {
				return nil
			}
		}
		return ErrStale
	}
	attempt, ok := st.Actions[key(d.cfg.ConversationID, id)]
	if !ok || attempt.State != "settled" {
		return ErrStale
	}
	var intent controllerActionRequest
	var receipt createdWorkerResult
	if json.Unmarshal(attempt.Request, &intent) != nil || json.Unmarshal(attempt.Receipt, &receipt) != nil {
		return ErrCorrupt
	}
	launchSettled := intent.Tool == "swarm_create" && receipt.State == "created" || intent.Tool == "swarm_resume" && receipt.State == "resumed"
	if intent.Origin == nil || !d.scope.validateOrigin(*intent.Origin) || receipt.Session == "" || receipt.SessionInstance == "" || !launchSettled {
		return ErrStale
	}
	if !d.scope.projectPath(intent.Project, intent.Project) {
		return ErrExpired
	}
	return nil
}

// PublicationCallAdmission resolves the exact human input for a validated
// publication. A worker wake needs an exact durable attempt and origin.
func (d *DiscussionController) PublicationCallAdmission(ctx context.Context, output DiscussionOutput, bodyHash string) (string, error) {
	if err := d.ValidatePublication(ctx, output, bodyHash); err != nil {
		return "", err
	}
	st, err := d.store.readView()
	if err != nil {
		return "", err
	}
	conversation := d.cfg.ConversationID
	request := output.AdmissionID
	if output.JobID != "" && !strings.HasPrefix(request, "dv_") {
		wake, ok := st.Wakes[key(conversation, request)]
		event := st.Events[key(conversation, wake.EventID)]
		if !ok || event.JobID != output.JobID || event.AttemptID != output.AttemptID {
			return "", ErrNotFound
		}
		attempt := st.Actions[key(conversation, output.AttemptID)]
		var intent controllerActionRequest
		if attempt.State != "settled" || json.Unmarshal(attempt.Request, &intent) != nil || intent.Origin == nil || intent.Origin.Voice.Generation == 0 {
			return "", ErrNotFound
		}
		origin := st.Admissions[admissionKey(conversation, attempt.Admission)]
		if origin.Origin == nil || *origin.Origin != *intent.Origin || !origin.SourceCutover {
			return "", ErrStale
		}
		// An intervening steer can change the result without changing its attempt.
		// Require both the launch/resume and current worker custody to agree.
		matches := false
		for _, worker := range st.Workers {
			if worker.ConversationID == conversation && worker.JobID == output.JobID &&
				worker.AttemptID == output.AttemptID && !worker.Reserved {
				if worker.ActionID != attempt.ID || worker.SourceID != origin.SourceID {
					return "", ErrStale
				}
				matches = true
			}
		}
		if !matches {
			return "", ErrNotFound
		}
		request = intent.Origin.RequestID
	}
	for _, admission := range st.Admissions {
		if admission.ConversationID == conversation && admission.SourceCutover && admission.Origin != nil &&
			admission.Origin.RequestID == request && admission.Origin.Voice.Generation != 0 {
			return request, nil
		}
	}
	return "", ErrNotFound
}
