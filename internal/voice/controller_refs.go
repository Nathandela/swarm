package voice

import (
	"encoding/json"
	"strconv"
	"strings"
)

func (c *Controller) businessReference(kind string, admission uint64, turn, logical string) string {
	hash := hashValue(struct {
		Conversation  string
		Admission     uint64
		Turn, Logical string
	}{c.binding.ConversationID, admission, turn, logical})
	return kind + ":" + strconv.FormatUint(admission, 10) + ":" + hash
}
func (c *Controller) actionReference(st State, id string, admission uint64, mutation bool) (string, error) {
	if strings.HasPrefix(id, "action:") {
		prior, ok := st.Actions[key(c.binding.ConversationID, id)]
		if !ok {
			return "", ErrNotFound
		}
		if mutation && prior.Admission != admission {
			return "", ErrExpired
		}
		return id, nil
	}
	return c.businessReference("action", admission, "", id), nil
}
func (c *Controller) publicationReference(st State, id string, admission uint64, turn string) (string, error) {
	if strings.HasPrefix(id, "publication:") {
		prior, ok := st.Publications[key(c.binding.ConversationID, id)]
		if !ok {
			return "", ErrNotFound
		}
		if prior.Admission != admission || prior.NativeTurnID != turn {
			return "", ErrExpired
		}
		return id, nil
	}
	return c.businessReference("publication", admission, turn, id), nil
}
func withoutBusinessID(raw json.RawMessage, field string) (json.RawMessage, error) {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return nil, ErrInvalid
	}
	delete(object, field)
	encoded, err := json.Marshal(object)
	return encoded, err
}
func controllerActionResult(id string, raw json.RawMessage) (json.RawMessage, error) {
	var result map[string]json.RawMessage
	if json.Unmarshal(raw, &result) != nil || result == nil {
		return nil, ErrCorrupt
	}
	reference, _ := json.Marshal(id)
	result["action_id"] = reference
	return controllerResult(result)
}
