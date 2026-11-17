package voice

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"

	"github.com/Nathandela/swarm/internal/managerproof"
)

type managerHandler struct {
	mu                                sync.RWMutex
	conversation, credential, session string
	call                              ManagerToolCaller
}

func newManagerHandler(conversation, credential string) *managerHandler {
	return &managerHandler{conversation: conversation, credential: credential}
}

// bind is called only after bootstrap verifies typed native ownership and private bridge references.
func (h *managerHandler) bind(localSession string, call ManagerToolCaller) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.session, h.call = localSession, call
}

func (h *managerHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost || r.URL.Path != "/manager/call" || r.Host != "voice" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	credentials := r.Header.Values("Authorization")
	if len(h.credential) != 64 || len(credentials) != 1 || subtle.ConstantTimeCompare([]byte(credentials[0]), []byte("Bearer "+h.credential)) != 1 || r.Header.Get("Origin") != "" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if r.Header.Get("Content-Type") != "application/json" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, MaxControlFrameBytes+1))
	if err != nil || len(raw) > MaxControlFrameBytes || validateObjectMembers(raw, "version", "conversation_id", "session_id", "call") != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || validateObjectMembers(fields["call"], "name", "arguments", "meta") != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var req ManagerBridgeRequest
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(&req) != nil || req.Version != 1 || req.ConversationID != h.conversation {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if _, err := managerproof.ArgumentsHash(req.Call.Arguments); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var callFields map[string]json.RawMessage
	if json.Unmarshal(fields["call"], &callFields) != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if meta, ok := callFields["meta"]; ok {
		if _, err := managerproof.CanonicalJSON(meta); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
	}
	h.mu.RLock()
	session, call := h.session, h.call
	h.mu.RUnlock()
	if session == "" || req.SessionID != session || call == nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	data, err := call(r.Context(), req.Call)
	reply := ManagerBridgeReply{Data: data}
	if err != nil {
		reply = ManagerBridgeReply{Error: bridgeErrorCode(err)}
	}
	raw, err = json.Marshal(reply)
	if err != nil || len(raw) > MaxControlFrameBytes {
		raw = []byte(`{"error":"capacity"}`)
	}
	_, _ = w.Write(raw)
}

func bridgeErrorCode(err error) string {
	for _, pair := range []struct {
		err  error
		code string
	}{
		{ErrInvalid, "invalid"}, {ErrConflict, "conflict"}, {ErrStale, "stale"},
		{ErrExpired, "expired"}, {ErrCapacity, "capacity"}, {ErrNotFound, "not_found"},
	} {
		if errors.Is(err, pair.err) {
			return pair.code
		}
	}
	return "unconfirmed"
}
