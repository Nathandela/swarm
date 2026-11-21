package voice

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/Nathandela/swarm/internal/protocol/schema"
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

// A worker's completion note and its full native result can wake two manager turns.
func priorWorkerCompletion(st *State, p Publication) (Publication, bool) {
	src := p.DiscussionSource
	if !p.Final || src.AttemptID == "" || src.JobID == "" {
		return Publication{}, false
	}
	var latest Publication
	for _, old := range st.Publications {
		if old.ConversationID == p.ConversationID && old.Final && old.DiscussionSource.JobID == src.JobID &&
			old.DiscussionSource.AttemptID == src.AttemptID && old.OutputSequence > latest.OutputSequence {
			latest = old
		}
	}
	if latest.ID == "" || latest.Text != p.Text || latest.DiscussionProjects != p.DiscussionProjects || latest.DiscussionAttempts != p.DiscussionAttempts {
		return Publication{}, false
	}
	current, note := workerCompletionEvidence(st, p)
	if current == "" {
		return Publication{}, false
	}
	previous, wasNote := workerCompletionEvidence(st, latest)
	return latest, previous == current && wasNote != note
}

// The bool identifies the worker's own note. Empty text means the evidence cannot be matched.
func workerCompletionEvidence(st *State, pub Publication) (string, bool) {
	src := pub.DiscussionSource
	a := st.Admissions[admissionKey(pub.ConversationID, pub.Admission)]
	wake := st.Wakes[key(pub.ConversationID, pub.DiscussionSource.AdmissionID)]
	e := st.Events[key(pub.ConversationID, wake.EventID)]
	if a.Origin != nil || a.RecoveryOnly || a.SourceID != "wake:"+wake.ID || wake.Admission != a.Sequence ||
		e.WakeID != wake.ID || e.RecoveryOnly || e.JobID != src.JobID || e.AttemptID != src.AttemptID {
		return "", false
	}
	var meta struct {
		Session, Instance, Kind, Text string
		Data                          schema.WorkerTurnEvidence
	}
	if json.Unmarshal(e.Metadata, &meta) != nil {
		return "", false
	}
	target := key(meta.Session, meta.Instance)
	w := st.Workers[key(pub.ConversationID, target)]
	if w.RecoveryRequired || w.VMChild || w.ActionID != src.AttemptID || w.AttemptID != src.AttemptID || w.JobID != src.JobID ||
		!singleReportScope(pub.DiscussionAttempts, src.AttemptID) || !singleReportScope(pub.DiscussionProjects, w.Project) {
		return "", false
	}
	for _, a := range st.Actions {
		if a.ConversationID == pub.ConversationID && a.Target == target && a.State != "settled" {
			return "", false
		}
	}
	if e.WorkerMessage && meta.Kind == "complete" {
		return meta.Text, true
	}
	if e.WorkerProof && e.ID == discussionEvidenceID(w) && e.NativeTurnID != "" && meta.Data.TurnID == e.NativeTurnID &&
		meta.Data.State == "completed" && !meta.Data.TextTruncated {
		return meta.Data.Text, false
	}
	return "", false
}
