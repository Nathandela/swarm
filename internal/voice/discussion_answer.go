package voice

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

type DiscussionAnswerInput struct {
	RequestID string `json:"request_id,omitempty"`
	Hash      string `json:"hash,omitempty"`
	After     string `json:"after,omitempty"`
}

func (c *Controller) humanDiscussionTurn(a Admission) bool {
	return c.discussion != nil && a.Origin != nil && c.discussion.validateOrigin(*a.Origin) && !a.RecoveryOnly &&
		strings.HasPrefix(a.SourceID, "discussion:") && a.Phase == "running" && a.SettledAt.IsZero() && !a.NativeBindingRetired
}

func eligibleQuestion(r Request, a Admission, now time.Time) bool {
	return r.Kind == "manager" && r.Exported && r.ConversationID == a.ConversationID && r.OutputSequence > 0 &&
		r.OutputSequence <= a.QuestionBoundary && r.State == "pending" && now.Before(r.ExpiresAt) && r.NativeInputID != a.NativeInputID
}

func (c *Controller) discussionQuestions(st State, a Admission, after string) (json.RawMessage, error) {
	if !c.humanDiscussionTurn(a) {
		return nil, ErrExpired
	}
	type question struct {
		ID       string          `json:"request_id"`
		Hash     string          `json:"hash"`
		Question json.RawMessage `json:"question"`
	}
	ids := []string{}
	now := time.Now()
	for _, r := range st.Requests {
		if r.ID > after && eligibleQuestion(r, a, now) && !st.CancelledTasks[key(r.ConversationID, r.TaskID)] {
			ids = append(ids, r.ID)
		}
	}
	sort.Strings(ids)
	next := ""
	if len(ids) > 2 {
		ids = ids[:2]
		next = ids[1]
	}
	items := make([]question, 0, len(ids))
	for _, id := range ids {
		r := st.Requests[key(a.ConversationID, id)]
		items = append(items, question{id, r.Hash, r.Payload})
	}
	return controllerResult(struct {
		Questions []question `json:"questions"`
		Next      string     `json:"next,omitempty"`
	}{items, next})
}

// Use the stored human text; the model chooses only the matching question.
func (c *Controller) settleDiscussionAnswer(expected Admission, input DiscussionAnswerInput) (Request, error) {
	var result Request
	err := c.store.update(func(st *State) error {
		if err := checkBinding(st, c.binding); err != nil {
			return err
		}
		a := st.Admissions[admissionKey(c.binding.ConversationID, expected.Sequence)]
		if !c.humanDiscussionTurn(a) || a.NativeInputID != expected.NativeInputID || a.NativeTurnID != expected.NativeTurnID || st.Conversations[a.ConversationID].CustodyLost {
			return ErrExpired
		}
		k := key(a.ConversationID, input.RequestID)
		r, ok := st.Requests[k]
		if !ok {
			return ErrNotFound
		}
		if r.Kind != "manager" || r.Hash != input.Hash {
			return ErrConflict
		}
		raw, err := json.Marshal(a.HumanReply)
		if err != nil {
			return err
		}
		answerID := a.Origin.RequestID
		if r.State == "answered" && r.AnswerID == answerID && string(r.Answer) == string(raw) {
			result = r
			return nil
		}
		if !eligibleQuestion(r, a, time.Now()) || st.CancelledTasks[key(a.ConversationID, r.TaskID)] {
			return ErrExpired
		}
		for _, prior := range st.Requests {
			if prior.ConversationID == a.ConversationID && prior.AnswerID == answerID {
				return ErrConflict
			}
		}
		r.State = "answered"
		r.AnswerID = answerID
		r.Answer = raw
		putRecord(st, "requests", &st.Requests, k, r)
		result = r
		return nil
	})
	return cloneRequest(result), err
}
