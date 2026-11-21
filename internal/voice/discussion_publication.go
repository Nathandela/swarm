package voice

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/Nathandela/swarm/internal/claudeinput"
	"github.com/Nathandela/swarm/internal/protocol"
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
	if !p.Final {
		return Publication{}, false
	}
	prior, ok := relatedWorkerFinal(st, p)
	return prior, ok && prior.Text == p.Text && prior.DiscussionProjects == p.DiscussionProjects && prior.DiscussionAttempts == p.DiscussionAttempts
}

func relatedWorkerFinal(st *State, p Publication) (Publication, bool) {
	src := p.DiscussionSource
	if src.AttemptID == "" || src.JobID == "" {
		return Publication{}, false
	}
	var latest Publication
	for _, old := range st.Publications {
		if old.ConversationID == p.ConversationID && old.Final && old.DiscussionSource.JobID == src.JobID &&
			old.DiscussionSource.AttemptID == src.AttemptID && old.OutputSequence > latest.OutputSequence {
			latest = old
		}
	}
	if latest.ID == "" || latest.Admission == p.Admission {
		return Publication{}, false
	}
	current := workerCompletionEvidence(st, p)
	previous := workerCompletionEvidence(st, latest)
	if current.text == "" || previous.text == "" {
		return Publication{}, false
	}
	if current.note == previous.note {
		// A retried completion may have a new semantic ID after its bridge receipt was lost.
		return latest, current.note && (current.recovered || previous.recovered) && current.text == previous.text
	}
	// After a handoff the worker's note and its verified native final can describe
	// the same result in different words. Only identical public finals are reused.
	return latest, current.text == previous.text || current.recovered || previous.recovered
}

// Attach the disposition at dispatch, after an earlier queued wake has had a chance to publish.
func discussionPriorFinalNotice(st State, a Admission, input, provider, nonce string) string {
	p := Publication{ConversationID: a.ConversationID, Admission: a.Sequence, DiscussionSource: discussionOutputSource(&st, a.ConversationID, a)}
	if _, ok := relatedWorkerFinal(&st, p); !ok {
		return input
	}
	text := input + "\nA final report for this worker attempt is already scheduled. Check Discord history for its delivery. Publish again only if this evidence changes or corrects the reported result."
	if len(text) > protocol.MaxSendInputText {
		return input
	}
	if provider == "claude" {
		if _, err := claudeinput.ManagerEnvelope(nonce, text); err != nil {
			return input
		}
	}
	return text
}

type completionEvidence struct {
	text            string
	note, recovered bool
}

func workerCompletionEvidence(st *State, pub Publication) completionEvidence {
	src := pub.DiscussionSource
	a := st.Admissions[admissionKey(pub.ConversationID, pub.Admission)]
	wake := st.Wakes[key(pub.ConversationID, src.AdmissionID)]
	e := st.Events[key(pub.ConversationID, wake.EventID)]
	if a.Origin != nil || a.RecoveryOutput != nil || wake.ID == "" || a.SourceID != "wake:"+wake.ID || wake.Admission != a.Sequence ||
		e.WakeID != wake.ID || e.JobID != src.JobID || e.AttemptID != src.AttemptID {
		return completionEvidence{}
	}
	var meta struct {
		Session, Instance, Kind, Text, NoteID string
		Data                                  schema.WorkerTurnEvidence
	}
	if json.Unmarshal(e.Metadata, &meta) != nil {
		return completionEvidence{}
	}
	target := key(meta.Session, meta.Instance)
	w := st.Workers[key(pub.ConversationID, target)]
	if w.VMChild || w.ActionID != src.AttemptID || w.AttemptID != src.AttemptID || w.JobID != src.JobID ||
		!singleReportScope(pub.DiscussionAttempts, src.AttemptID) || !singleReportScope(pub.DiscussionProjects, w.Project) {
		return completionEvidence{}
	}
	for _, a := range st.Actions {
		if a.ConversationID == pub.ConversationID && a.Target == target && a.State != "settled" {
			return completionEvidence{}
		}
	}
	if e.WorkerMessage && meta.Kind == "complete" {
		if e.RecoveryOnly && (meta.NoteID == "" || e.ID != "worker-message-"+hashValue([]string{w.Session, w.SessionInstance, w.AttemptID, meta.NoteID}) || e.ID != wake.ID) {
			return completionEvidence{}
		}
		return completionEvidence{text: meta.Text, note: true, recovered: e.RecoveryOnly}
	}
	if e.WorkerProof && e.ID == discussionEvidenceID(w) && e.NativeTurnID != "" && meta.Data.TurnID == e.NativeTurnID &&
		meta.Data.State == "completed" && !meta.Data.TextTruncated {
		return completionEvidence{text: meta.Data.Text, recovered: e.RecoveryOnly || a.RecoveryOnly}
	}
	return completionEvidence{}
}
