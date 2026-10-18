package skeleton

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/adapter/opencode"
	"github.com/Nathandela/swarm/internal/opencodebridge"
	"github.com/Nathandela/swarm/internal/status"
)

const openCodeHierarchyScope = "current_activity_requests"

type OpenCodeDescendant struct {
	SessionID string
	ParentID  string
	Title     string
	CreatedAt int64
	Status    opencode.SessionStatus
	Error     *OpenCodeError
	depth     int
}

type OpenCodeRequest struct {
	Kind       string
	ID         string             `json:"id"`
	SessionID  string             `json:"sessionID"`
	Permission string             `json:"permission"`
	Patterns   []string           `json:"patterns"`
	Always     []string           `json:"always"`
	Questions  []OpenCodeQuestion `json:"questions"`
	Raw        json.RawMessage    `json:"-"`
}

type OpenCodeQuestion struct {
	Question string                   `json:"question"`
	Header   string                   `json:"header"`
	Options  []OpenCodeQuestionOption `json:"options"`
	Multiple bool                     `json:"multiple"`
	Custom   *bool                    `json:"custom"`
}

type OpenCodeQuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

// reconcileNative replaces current authority only after validating the whole scoped read.
func (s *OpenCodeState) reconcileNative(snapshot opencodebridge.Snapshot, epoch uint64) error {
	if epoch != s.epoch {
		return errors.New("stale native current-state epoch")
	}
	refuse := func() error {
		if s.scope == nil {
			s.scope = map[string]OpenCodeDescendant{}
		}
		s.nativeUnknown = "native current activity and requests could not be verified"
		return errors.New(s.nativeUnknown)
	}
	if snapshot.RootID != s.root || snapshot.BackendGeneration != s.generation || !snapshot.HierarchyComplete || snapshot.HierarchyScope != openCodeHierarchyScope || snapshot.Descendants == nil || len(snapshot.Descendants) > 128 || snapshot.Statuses == nil || snapshot.Permissions == nil || snapshot.Questions == nil || len(snapshot.Permissions)+len(snapshot.Questions) > 128 {
		return refuse()
	}
	scope := make(map[string]OpenCodeDescendant, len(snapshot.Descendants))
	for _, raw := range snapshot.Descendants {
		if len(raw) > 32<<10 {
			return refuse()
		}
		var row struct {
			ID       string `json:"id"`
			ParentID string `json:"parentID"`
			Title    string `json:"title"`
			Time     struct {
				Created int64 `json:"created"`
			} `json:"time"`
		}
		if json.Unmarshal(raw, &row) != nil || !ocIdentifier(row.ID) || !ocIdentifier(row.ParentID) || row.ID == s.root || row.Time.Created <= 0 {
			return refuse()
		}
		if _, exists := scope[row.ID]; exists {
			return refuse()
		}
		depth := 1
		if row.ParentID != s.root {
			parent, ok := scope[row.ParentID]
			if !ok {
				return refuse()
			}
			depth = parent.depth + 1
		}
		if depth > 8 {
			return refuse()
		}
		child := OpenCodeDescendant{SessionID: row.ID, ParentID: row.ParentID, Title: clampBytes(row.Title, 1024), CreatedAt: row.Time.Created, Status: opencode.SessionStatus{Type: "idle"}, depth: depth}
		if old, ok := s.scope[row.ID]; ok {
			if old.ParentID != row.ParentID || old.CreatedAt != row.Time.Created {
				return refuse()
			}
			child.Error = old.Error
		}
		scope[row.ID] = child
	}
	rawStatuses, err := json.Marshal(snapshot.Statuses)
	if err != nil {
		return refuse()
	}
	statuses, err := opencode.DecodeStatusSnapshot(rawStatuses)
	if err != nil {
		return refuse()
	}
	foreign := map[string]bool{}
	for id, st := range statuses {
		if child, ok := scope[id]; ok {
			child.Status = st
			if st.Type == "busy" || st.Type == "retry" {
				child.Error = nil
			}
			scope[id] = child
		} else if id != s.root {
			foreign[id] = true
		}
	}
	pending := map[string]OpenCodeRequest{}
	bytes := 0
	for _, list := range []struct {
		kind string
		rows []json.RawMessage
	}{{"permission", snapshot.Permissions}, {"question", snapshot.Questions}} {
		for _, raw := range list.rows {
			bytes += len(raw)
			if bytes > 1<<20 {
				return refuse()
			}
			request, err := decodeOpenCodeRequest(list.kind, raw)
			if err != nil {
				return refuse()
			}
			if _, related := scope[request.SessionID]; request.SessionID != s.root && !related {
				foreign[request.SessionID] = true
				continue
			}
			key := request.Kind + ":" + request.ID
			if _, exists := pending[key]; exists {
				return refuse()
			}
			pending[key] = request
		}
	}
	if len(foreign) > 256 {
		return refuse()
	}
	s.scope, s.foreignSessions, s.pendingRequests = scope, foreign, pending
	s.nativeUnknown = ""
	return s.ReconcileRootStatus(epoch, statuses)
}

func ocIdentifier(value string) bool {
	return value != "" && len(value) <= 128 && strings.IndexFunc(value, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) < 0
}

func decodeOpenCodeRequest(kind string, raw json.RawMessage) (OpenCodeRequest, error) {
	var request OpenCodeRequest
	if len(raw) > 64<<10 || !utf8.Valid(raw) || json.Unmarshal(raw, &request) != nil || !ocIdentifier(request.ID) || !ocIdentifier(request.SessionID) {
		return request, errors.New("invalid native request identity")
	}
	request.Kind = kind
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	if tool, exists := fields["tool"]; exists {
		var reference struct {
			MessageID string `json:"messageID"`
			CallID    string `json:"callID"`
		}
		if json.Unmarshal(tool, &reference) != nil || !ocIdentifier(reference.MessageID) || !ocIdentifier(reference.CallID) {
			return request, errors.New("invalid native request tool identity")
		}
	}
	switch kind {
	case "permission":
		var extra struct {
			Metadata json.RawMessage `json:"metadata"`
		}
		_ = json.Unmarshal(raw, &extra)
		metadata := bytes.TrimSpace(extra.Metadata)
		if request.Permission == "" || len(request.Permission) > 256 || !nativeStringArray(fields["patterns"]) || !nativeStringArray(fields["always"]) || len(request.Patterns) > 128 || len(request.Always) > 128 || len(metadata) < 2 || metadata[0] != '{' {
			return request, errors.New("invalid native permission")
		}
		for _, patterns := range [][]string{request.Patterns, request.Always} {
			for _, pattern := range patterns {
				if len(pattern) > 8192 {
					return request, errors.New("oversized native permission pattern")
				}
			}
		}
	case "question":
		if len(request.Questions) == 0 || len(request.Questions) > 16 {
			return request, errors.New("invalid native question")
		}
		var rawQuestions []map[string]json.RawMessage
		if json.Unmarshal(fields["questions"], &rawQuestions) != nil {
			return request, errors.New("invalid native question fields")
		}
		for i, question := range request.Questions {
			shape := rawQuestions[i]
			if !nativeString(shape["header"]) || !nativeString(shape["question"]) {
				return request, errors.New("missing native question text")
			}
			for _, key := range []string{"custom", "multiple"} {
				if value, exists := shape[key]; exists && !bytes.Equal(bytes.TrimSpace(value), []byte("true")) && !bytes.Equal(bytes.TrimSpace(value), []byte("false")) {
					return request, errors.New("invalid native question flag")
				}
			}
			if question.Question == "" || len(question.Question) > 8192 || len(question.Header) > 1024 || question.Options == nil || len(question.Options) > 64 {
				return request, errors.New("invalid native question choices")
			}
			var rawOptions []map[string]json.RawMessage
			if json.Unmarshal(shape["options"], &rawOptions) != nil {
				return request, errors.New("invalid native question options")
			}
			for j, option := range question.Options {
				if !nativeString(rawOptions[j]["label"]) || !nativeString(rawOptions[j]["description"]) {
					return request, errors.New("missing native question option text")
				}
				if option.Label == "" || len(option.Label) > 1024 || len(option.Description) > 4096 {
					return request, errors.New("invalid native question option")
				}
			}
		}
	default:
		return request, errors.New("unknown native request kind")
	}
	request.Raw = append(json.RawMessage(nil), raw...)
	return request, nil
}

func nativeString(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && raw[0] == '"'
}

func nativeStringArray(raw json.RawMessage) bool {
	var values []*string
	if json.Unmarshal(raw, &values) != nil || values == nil {
		return false
	}
	for _, value := range values {
		if value == nil {
			return false
		}
	}
	return true
}

func cloneOpenCodeRequest(request OpenCodeRequest) OpenCodeRequest {
	request.Raw = append(json.RawMessage(nil), request.Raw...)
	request.Patterns = append([]string(nil), request.Patterns...)
	request.Always = append([]string(nil), request.Always...)
	request.Questions = append([]OpenCodeQuestion(nil), request.Questions...)
	for i := range request.Questions {
		question := &request.Questions[i]
		question.Options = append([]OpenCodeQuestionOption(nil), question.Options...)
		if question.Custom != nil {
			custom := *question.Custom
			question.Custom = &custom
		}
	}
	return request
}

// Native request events trigger a current-set read; delayed asks cannot resurrect resolved cards.
func (s *OpenCodeState) applyNativeActivity(event opencode.Event, raw []byte) (OpenCodeDisposition, bool) {
	if s.scope == nil {
		return "", false
	}
	if strings.HasPrefix(event.Kind, "permission.") || strings.HasPrefix(event.Kind, "question.") {
		var wire struct {
			Properties struct {
				SessionID string `json:"sessionID"`
			} `json:"properties"`
		}
		_ = json.Unmarshal(raw, &wire)
		if s.foreignSessions[wire.Properties.SessionID] {
			return OCIgnoredForeign, true
		}
		s.nativeUnknown = "native request set changed; reconciliation required"
		return OCNeedsSnapshot, true
	}
	if event.Kind == "session.created" || event.Kind == "session.updated" || event.Kind == "session.deleted" {
		var wire struct {
			Properties struct {
				Info struct {
					ID       string `json:"id"`
					ParentID string `json:"parentID"`
				} `json:"info"`
			} `json:"properties"`
		}
		if json.Unmarshal(raw, &wire) != nil {
			return OCNeedsSnapshot, true
		}
		id, parent := wire.Properties.Info.ID, wire.Properties.Info.ParentID
		if !ocIdentifier(id) {
			s.nativeUnknown = "native hierarchy event has no verified identity"
			return OCNeedsSnapshot, true
		}
		_, owned := s.scope[id]
		_, ownedParent := s.scope[parent]
		if owned || parent == s.root || ownedParent || id == s.root && event.Kind == "session.deleted" {
			s.nativeUnknown = "native hierarchy changed; reconciliation required"
			return OCNeedsSnapshot, true
		}
		return OCIgnoredForeign, true
	}
	var id string
	var next *opencode.SessionStatus
	switch {
	case event.SessionStatus != nil:
		id = event.SessionStatus.SessionID
		next = &event.SessionStatus.Status
	case event.SessionIdle != nil:
		id = event.SessionIdle.SessionID
		next = &opencode.SessionStatus{Type: "idle"}
	case event.SessionError != nil:
		id = event.SessionError.SessionID
	default:
		return "", false
	}
	if id == s.root {
		return "", false
	}
	if child, ok := s.scope[id]; ok {
		if next != nil {
			child.Status = *cloneStatus(next)
			if next.Type == "busy" || next.Type == "retry" {
				child.Error = nil
			}
		}
		if event.SessionError != nil {
			child.Error = &OpenCodeError{Name: event.SessionError.Error.Name, Message: event.SessionError.Error.Data.Message}
		}
		s.scope[id] = child
		return OCApplied, true
	}
	if s.foreignSessions[id] {
		return OCIgnoredForeign, true
	}
	s.nativeUnknown = "native activity has unverified ancestry"
	return OCNeedsSnapshot, true
}

func (s *OpenCodeState) nativeView(view *OpenCodeView) {
	view.RootBusy, view.RootBusyKnown = view.Busy, view.BusyKnown
	view.Attention = s.sessionErr != nil
	for _, request := range s.pendingRequests {
		view.Pending = append(view.Pending, cloneOpenCodeRequest(request))
	}
	sort.Slice(view.Pending, func(i, j int) bool {
		a, b := view.Pending[i], view.Pending[j]
		return a.Kind+":"+a.SessionID+":"+a.ID < b.Kind+":"+b.SessionID+":"+b.ID
	})
	view.Attention = view.Attention || len(view.Pending) > 0
	for _, child := range s.scope {
		child.Status = *cloneStatus(&child.Status)
		if child.Error != nil {
			value := *child.Error
			child.Error = &value
		}
		view.Descendants = append(view.Descendants, child)
		switch child.Status.Type {
		case "busy", "retry":
			view.Busy = true
		case "idle":
		default:
			view.BusyKnown = false
			if !view.Degraded {
				view.Degraded = true
				view.DegradedReason = "unqualified descendant status"
			}
		}
	}
	sort.Slice(view.Descendants, func(i, j int) bool {
		a, b := view.Descendants[i], view.Descendants[j]
		if a.depth != b.depth {
			return a.depth < b.depth
		}
		return a.SessionID < b.SessionID
	})
	view.CurrentScopeKnown = s.scope != nil && s.nativeUnknown == ""
	if s.nativeUnknown != "" {
		view.BusyKnown = false
		if !view.Degraded {
			view.Degraded = true
			view.DegradedReason = s.nativeUnknown
		}
	}
	// A completed tool-bearing step cannot prove the requested input finished. Only
	// its latest genuine input raises idle attention; older unresolved work stays visible.
	if view.BusyKnown && !view.Busy {
		var latest *ocInputState
		for _, in := range s.inputs {
			if in.classified && !in.synthetic && !in.compaction && (latest == nil || ocAfter(in.at, in.msgID, latest.at, latest.msgID)) {
				latest = in
			}
		}
		if latest != nil {
			if latest.outcome == OpenCodeOutcomeNone && latest.unconfirmedTool {
				view.Attention = true
				view.AttentionReason = "Native input outcome is unconfirmed after tool execution"
			}
			if latest.outcome == OpenCodeOutcomeFailed && latest.outcomeSrc != OpenCodeOutcomeSourceImported {
				view.Attention = true
				view.AttentionReason = "Native input failed"
				if view.Error == nil {
					view.Error = &OpenCodeError{Name: latest.errorName, Message: "Native input failed"}
				}
			}
		}
	}

}

func openCodeProjectedStatus(view OpenCodeView) status.Status {
	projected := status.Status{Process: status.ProcessRunning, Turn: status.TurnUnknown, Interaction: status.InteractionNone, Attention: view.Attention}
	if view.BusyKnown {
		projected.Turn = status.TurnIdle
		if view.Busy {
			projected.Turn = status.TurnActive
		}
	}
	if view.Error != nil {
		projected.Interaction = status.InteractionError
	}
	for _, request := range view.Pending {
		if request.Kind == "question" {
			projected.Interaction = status.InteractionPrompt
		} else if projected.Interaction != status.InteractionPrompt {
			projected.Interaction = status.InteractionPermission
		}
	}
	return projected
}
