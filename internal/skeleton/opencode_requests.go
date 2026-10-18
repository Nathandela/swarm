package skeleton

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/status"
)

type nativeRequestAuthority struct {
	hash    string
	expires time.Time
}
type openCodeRequestAuthorities struct {
	mu       sync.Mutex
	sessions map[string]map[string]nativeRequestAuthority
	now      func() time.Time
}

// openCodeRequestCards uses a copied, accepted view; callers must not hold openCode.mu.
func (d *Daemon) openCodeRequestCards(local string, view OpenCodeView) ([]protocol.NativeRequestView, error) {
	meta, ok := d.core.Get(local)
	instance, current := d.sessionInstance(local)
	d.nativeRequests.mu.Lock()
	defer d.nativeRequests.mu.Unlock()
	refuse := func() ([]protocol.NativeRequestView, error) {
		delete(d.nativeRequests.sessions, local)
		return nil, errors.New("native request authority is unavailable")
	}
	if !ok || !current || instance == "" || d.machineID() == "" || meta.Status.Process != status.ProcessRunning || meta.RequiredRuntime != persist.RequiredRuntimeOpenCode || meta.OpenCodeBinding == nil || meta.OpenCodeBinding.Phase != persist.OpenCodePhaseReady || meta.ConversationID != view.Root || meta.OpenCodeBinding.Generation != view.Generation || !view.CurrentScopeKnown || len(view.Pending) > 128 {
		return refuse()
	}
	now := time.Now().UTC()
	if d.nativeRequests.now != nil {
		now = d.nativeRequests.now().UTC()
	}
	old := d.nativeRequests.sessions[local]
	next := make(map[string]nativeRequestAuthority, len(view.Pending))
	cards := make([]protocol.NativeRequestView, 0, len(view.Pending))
	for _, pending := range view.Pending {
		if len(pending.Raw) > 32<<10 {
			continue
		}
		if len(pending.Raw) == 0 {
			return refuse()
		}
		request, err := decodeOpenCodeRequest(pending.Kind, pending.Raw)
		if err != nil || request.ID != pending.ID || request.SessionID != pending.SessionID {
			return refuse()
		}
		card := protocol.NativeRequestView{Session: protocol.NamespacedID(d.machineID(), local), SessionInstance: instance, RootID: view.Root, Generation: view.Generation, NativeSessionID: request.SessionID, RequestID: request.ID, Kind: request.Kind, DetailsJSON: string(request.Raw), Permission: request.Permission, Patterns: append([]string(nil), request.Patterns...), Always: append([]string(nil), request.Always...)}
		var content any
		decoder := json.NewDecoder(bytes.NewReader(pending.Raw))
		decoder.UseNumber()
		if decoder.Decode(&content) != nil {
			return refuse()
		}
		canonical, err := json.Marshal([]any{card.Session, instance, view.Root, view.Generation, request.Kind, content})
		if err != nil {
			return refuse()
		}
		hash := sha256.Sum256(canonical)
		card.RequestHash = hex.EncodeToString(hash[:])
		key := request.Kind + ":" + request.ID
		if _, duplicate := next[key]; duplicate {
			return refuse()
		}
		guard := old[key]
		if guard.hash != card.RequestHash || !now.Before(guard.expires) {
			guard = nativeRequestAuthority{hash: card.RequestHash, expires: now.Add(5 * time.Minute)}
		}
		expires := guard.expires
		card.ExpiresAt = &expires
		if request.Kind == "permission" {
			card.Decisions = []protocol.NativeDecisionOption{
				{ID: "once", Label: "Allow this request once", Effect: "single_request"},
				{ID: "always", Label: "Approve native patterns; allow matching pending requests in this session", Effect: "approve_native_patterns_and_matching_session_requests"},
				{ID: "reject", Label: "Reject pending permission requests in this session", Effect: "reject_session_pending_permissions"},
			}
		} else {
			for _, question := range request.Questions {
				item := protocol.NativeQuestionView{Question: question.Question, Header: question.Header, Multiple: question.Multiple, Custom: question.Custom == nil || *question.Custom, Options: make([]protocol.NativeQuestionOption, 0, len(question.Options))}
				for _, option := range question.Options {
					item.Options = append(item.Options, protocol.NativeQuestionOption{Label: option.Label, Description: option.Description})
				}
				card.Questions = append(card.Questions, item)
			}
			card.Decisions = []protocol.NativeDecisionOption{{ID: "question_reply", Label: "Send answers", Effect: "single_request"}, {ID: "question_reject", Label: "Reject this question", Effect: "single_request"}}
		}
		encoded, err := json.Marshal(card)
		if err != nil {
			return refuse()
		}
		if len(encoded) > schema.MaxNativeRequestViewBytes {
			continue
		}
		next[key] = guard
		cards = append(cards, card)
	}
	if d.nativeRequests.sessions == nil {
		d.nativeRequests.sessions = make(map[string]map[string]nativeRequestAuthority)
	}
	d.nativeRequests.sessions[local] = next
	return cards, nil
}
