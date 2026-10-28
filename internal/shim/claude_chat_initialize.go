package shim

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

// Only the managed capture owns this request. No public arbitrary-control API.
func (c *claudeChatCapture) initializeNative() error {
	if !c.admission.TryLock() {
		return errClaudeChatCapture
	}
	defer c.admission.Unlock()
	c.mu.Lock()
	if !c.native || c.stdin == nil || c.identity.PrimaryPID <= 0 || c.identity.PrimaryStartTime <= 0 || c.captureLost() || c.eof[0] || c.eof[1] {
		c.mu.Unlock()
		return errClaudeChatCapture
	}
	if c.initializeRequestID != "" {
		written := c.initializeWritten
		c.mu.Unlock()
		if written {
			return nil
		}
		return errClaudeChatCapture
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		c.mu.Unlock()
		return err
	}
	c.initializeRequestID = hex.EncodeToString(nonce[:])
	body, err := json.Marshal(struct {
		Type      string `json:"type"`
		RequestID string `json:"request_id"`
		Request   struct {
			Subtype string `json:"subtype"`
		} `json:"request"`
	}{Type: "control_request", RequestID: c.initializeRequestID, Request: struct {
		Subtype string `json:"subtype"`
	}{"initialize"}})
	c.mu.Unlock()
	if err != nil {
		return err
	}
	if c.writePrimaryLine(append(body, '\n'), time.Now().Add(3*time.Second)) != "written" {
		return errClaudeChatCapture
	}
	c.mu.Lock()
	c.initializeWritten = true
	c.mu.Unlock()
	return nil
}

// Called under capture.mu, before queue publication. Extra response metadata is
// retained raw; only exact authoritative fields establish handshake identity.
func (c *claudeChatCapture) observeNativeInitialize(raw []byte, sequence uint64) bool {
	head, ok := nativeInitializeObject(raw, "type", "response")
	if !ok {
		return false
	}
	var kind string
	if json.Unmarshal(head["type"], &kind) != nil {
		return false
	}
	if kind == "control_request" {
		c.nativeInputBlocked = true
		return true
	}
	if kind != "control_response" {
		return true
	}
	if !uniqueBirthJSON(raw) {
		return false
	}
	response, ok := nativeInitializeObject(head["response"], "subtype", "request_id", "response")
	if !ok {
		return false
	}
	detail, ok := nativeInitializeObject(response["response"], "pid", "claude_code_version", "session_state")
	if !ok {
		return false
	}
	var subtype, requestID, version, state string
	var pid int
	if json.Unmarshal(response["subtype"], &subtype) != nil || json.Unmarshal(response["request_id"], &requestID) != nil || json.Unmarshal(detail["pid"], &pid) != nil || json.Unmarshal(detail["claude_code_version"], &version) != nil || json.Unmarshal(detail["session_state"], &state) != nil || c.initializeRequestID == "" || c.initializeSequence != 0 || subtype != "success" || requestID != c.initializeRequestID || pid != c.identity.PrimaryPID || version != "2.1.310" {
		return false
	}
	c.initializeSequence, c.initializeState = sequence, state
	return true
}
func (c *claudeChatCapture) initialized() bool {
	return c.native && c.initializeWritten && c.initializeSequence > 0 && c.initializeSequence <= c.durableSequence
}

// Native field names are case-sensitive; Go's struct decoder accepts aliases.
func nativeInitializeObject(raw []byte, authoritative ...string) (map[string]json.RawMessage, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, false
	}
	for key := range fields {
		for _, exact := range authoritative {
			if key != exact && strings.EqualFold(key, exact) {
				return nil, false
			}
		}
	}
	return fields, true
}
