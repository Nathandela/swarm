package schema

import (
	"crypto/sha256"
	"encoding/json"
	"time"
)

const ActionNativeDecide = "native_decide"
const MaxNativeRequestViewBytes = 12 << 10

// NativeDecisionReq binds an answer to one observed native request and worker incarnation.
type NativeDecisionReq struct {
	Session         string     `json:"session"`
	SessionInstance string     `json:"session_instance"`
	RootID          string     `json:"root_id"`
	Generation      string     `json:"generation"`
	NativeSessionID string     `json:"native_session_id"`
	RequestID       string     `json:"request_id"`
	RequestHash     string     `json:"request_hash"`
	ExpiresAt       *time.Time `json:"expires_at"`
	Kind            string     `json:"kind"`
	Reply           string     `json:"reply,omitempty"`
	Answers         [][]string `json:"answers,omitempty"`
}

// NativeDecisionContentHash signs the complete body, including ordered answers.
func NativeDecisionContentHash(req *NativeDecisionReq) []byte {
	if req == nil {
		req = &NativeDecisionReq{}
	}
	h := sha256.New()
	for _, value := range []string{req.Session, req.SessionInstance, req.RootID, req.Generation, req.NativeSessionID, req.RequestID, req.RequestHash, req.Kind, req.Reply} {
		writeHashField(h, []byte(value))
	}
	expiry := ""
	if req.ExpiresAt != nil {
		expiry = req.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	writeHashField(h, []byte(expiry))
	var values [][]string
	if len(req.Answers) != 0 {
		values = req.Answers
	}
	answers, _ := json.Marshal(values)
	writeHashField(h, answers)
	return h.Sum(nil)
}

type NativeDecisionReceipt struct {
	OperationID     string `json:"operation_id"`
	Session         string `json:"session"`
	SessionInstance string `json:"session_instance"`
	RootID          string `json:"root_id"`
	Generation      string `json:"generation"`
	NativeSessionID string `json:"native_session_id"`
	RequestID       string `json:"request_id"`
	Kind            string `json:"kind"`
	State           string `json:"state"`
	Reason          string `json:"reason,omitempty"`
	NativeEffect    string `json:"native_effect,omitempty"`
}

// NativeRequestView carries bounded decision authority issued by this daemon.
type NativeRequestView struct {
	Session         string                 `json:"session"`
	SessionInstance string                 `json:"session_instance"`
	RootID          string                 `json:"root_id"`
	Generation      string                 `json:"generation"`
	NativeSessionID string                 `json:"native_session_id"`
	RequestID       string                 `json:"request_id"`
	RequestHash     string                 `json:"request_hash"`
	ExpiresAt       *time.Time             `json:"expires_at"`
	Kind            string                 `json:"kind"`
	DetailsJSON     string                 `json:"details_json,omitempty"`
	Permission      string                 `json:"permission,omitempty"`
	Patterns        []string               `json:"patterns,omitempty"`
	Always          []string               `json:"always,omitempty"`
	Questions       []NativeQuestionView   `json:"questions,omitempty"`
	Decisions       []NativeDecisionOption `json:"decisions,omitempty"`
}

type NativeQuestionView struct {
	Question string                 `json:"question"`
	Header   string                 `json:"header"`
	Options  []NativeQuestionOption `json:"options"`
	Multiple bool                   `json:"multiple"`
	Custom   bool                   `json:"custom"`
}
type NativeQuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}
type NativeDecisionOption struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Effect string `json:"effect"`
}
