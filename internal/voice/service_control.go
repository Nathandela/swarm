package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/jsontext"
	"github.com/Nathandela/swarm/internal/managerproof"
	"github.com/Nathandela/swarm/internal/protocol"
)

// ManagerStatus describes the controller's current binding and scheduling state.
type ManagerStatus struct {
	Ready           bool           `json:"ready"`
	Binding         ManagerBinding `json:"binding"`
	SchedulerPaused bool           `json:"scheduler_paused"`
	ExternalInput   uint64         `json:"external_input"`
	CustodyLost     bool           `json:"custody_lost"`
	NativeInputID   string         `json:"native_input_id,omitempty"`
	NativeTurnID    string         `json:"native_turn_id,omitempty"`
	Attention       string         `json:"attention,omitempty"`
}

type managerControlRequest struct {
	ID    string `json:"id"`
	Text  string `json:"text,omitempty"`
	After uint64 `json:"after,omitempty"`
}

func (c *managerControl) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if r.Host != "voice-control" || r.Header.Get("Origin") != "" || r.Header.Get("Authorization") != "" || r.URL.RawQuery != "" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var data any
	var err error
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/publication-preview":
		c.servePublicationPreview(w, r)
		return
	case r.Method == http.MethodPost && r.URL.Path == "/v1/ptt/begin":
		var req managerControlRequest
		if err = readManagerControlRequest(r, &req); err == nil {
			data, err = c.beginPTT(req.ID)
		}
	case r.Method == http.MethodPost && r.URL.Path == "/v1/ptt/end":
		var req managerControlRequest
		if err = readManagerControlRequest(r, &req); err == nil {
			err = c.capture.end(req.ID, localPTTDevice, "")
			data = struct{}{}
		}
	case r.Method == http.MethodPost && r.URL.Path == "/v1/ptt/finalize":
		data, err = c.receivePTT(r)
	case strings.HasPrefix(r.URL.Path, "/v1/capture/"):
		data, err = c.ownerCaptureAction(r)
	case strings.HasPrefix(r.URL.Path, "/v1/devices/"):
		data, err = c.ownerDeviceAction(r)
	case strings.HasPrefix(r.URL.Path, "/v1/recovery/"):
		data, err = c.recoveryAction(r)
	case r.Method == http.MethodGet && r.URL.Path == "/v1/status":
		data, err = c.status()
	case r.Method == http.MethodPost && (r.URL.Path == "/v1/text" || r.URL.Path == "/v1/receipt" || r.URL.Path == "/v1/publications"):
		var req managerControlRequest
		if err = readManagerControlRequest(r, &req); err != nil {
			break
		}
		switch r.URL.Path {
		case "/v1/text":
			data, err = c.submit(req)
		case "/v1/receipt":
			data, _, err = c.receipt(req.ID)
		case "/v1/publications":
			var receipt Admission
			var state State
			receipt, state, err = c.receipt(req.ID)
			if err == nil {
				page := publicationPage{Publication: nextPublication(state, receipt, req.After)}
				if page.Publication != nil {
					page.Text = []byte(page.Publication.Text)
					page.Publication.Text = ""
				}
				data = page
			}
		}
	default:
		w.WriteHeader(http.StatusNotFound)
		return
	}
	var raw []byte
	if err == nil {
		raw, err = json.Marshal(data)
		if len(raw) > MaxControlFrameBytes {
			err = ErrCapacity
		}
	}
	if err != nil {
		w.WriteHeader(managerControlErrorStatus(err))
		return
	}
	_, _ = w.Write(raw)
	if r.Method == http.MethodPost && r.URL.Path == "/v1/recovery/replacement" && c.replacement != nil {
		_ = http.NewResponseController(w).Flush()
		c.replacement.signal()
	}
}

func readManagerControlRequest(r *http.Request, req *managerControlRequest) error {
	raw, err := io.ReadAll(io.LimitReader(r.Body, MaxControlFrameBytes+1))
	if err != nil || r.Header.Get("Content-Type") != "application/json" || len(raw) > MaxControlFrameBytes {
		return ErrInvalid
	}
	allowed := []string{"id"}
	switch r.URL.Path {
	case "/v1/text":
		allowed = append(allowed, "text")
	case "/v1/publications":
		allowed = append(allowed, "after")
	}
	if validateObjectMembers(raw, allowed...) != nil || json.Unmarshal(raw, req) != nil || !configText(req.ID, 128) {
		return ErrInvalid
	}
	return nil
}

func (c *managerControl) status() (ManagerStatus, error) {
	state, err := c.store.readView()
	if err != nil {
		return ManagerStatus{}, err
	}
	binding := c.binding
	active := state.Conversations[binding.ConversationID]
	c.mu.Lock()
	defer c.mu.Unlock()
	attention := c.tickAttention
	if attention == "" {
		attention = c.journalAttention
	}
	inputID, turnID := activeManagerInterruptTarget(state, binding)
	return ManagerStatus{
		Ready:   c.controller != nil && !active.CustodyLost && state.CurrentConversation == binding.ConversationID,
		Binding: binding, SchedulerPaused: active.SchedulerPaused, ExternalInput: active.ExternalInput,
		CustodyLost: active.CustodyLost, Attention: attention, NativeInputID: inputID, NativeTurnID: turnID,
	}, nil
}

func (c *managerControl) submit(req managerControlRequest) (Admission, error) {
	if strings.TrimSpace(req.Text) == "" || !utf8.ValidString(req.Text) || len(req.Text) > MaxUtteranceBytes {
		return Admission{}, ErrInvalid
	}
	if c.controller == nil {
		return Admission{}, ErrStale
	}
	if err := c.store.checkControllerBinding(c.binding); err != nil {
		if errors.Is(err, managerproof.ErrQuarantined) {
			return Admission{}, ErrStale
		}
		return Admission{}, err
	}
	// Each text operation owns its capture slot across a retry or reconnect.
	receipt, err := c.controller.SubmitText(Utterance{ID: req.ID, DeviceID: "local-control", VoiceSessionID: req.ID, MediaEpoch: 1}, req.Text)
	if errors.Is(err, ErrExpired) {
		// FinalizeUtterance already checked the digest. Retired authority can still have a retained receipt.
		receipt, _, err = c.receipt(req.ID)
		return receipt, err
	}
	if err == nil {
		select {
		case c.wake <- struct{}{}:
		default:
		}
	}
	receipt.Request, receipt.InputText = nil, ""
	return receipt, err
}

func (c *managerControl) receipt(id string) (Admission, State, error) {
	state, err := c.store.readView()
	if err != nil {
		return Admission{}, State{}, err
	}
	receipt, err := receiptForUtterance(state, c.binding.ConversationID, id)
	return receipt, state, err
}

func receiptForUtterance(state State, conversation, id string) (Admission, error) {
	utterance, ok := state.Utterances[key(conversation, id)]
	if !ok {
		return Admission{}, ErrNotFound
	}
	if utterance.Admission == 0 {
		if utterance.AudioHash != "" && utterance.Phase == "finalizing" {
			return Admission{}, ErrPTTUnconfirmed
		}
		return Admission{}, ErrNotFound
	}
	receipt, ok := state.Admissions[admissionKey(conversation, utterance.Admission)]
	if !ok {
		if utterance.Admission <= state.Conversations[conversation].RetiredThrough {
			return Admission{}, ErrExpired
		}
		return Admission{}, ErrCorrupt
	}
	receipt.Request, receipt.InputText = nil, ""
	return receipt, nil
}

func managerControlErrorStatus(err error) int {
	switch {
	case errors.Is(err, ErrInvalid):
		return http.StatusBadRequest
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrCaptureBusy):
		return http.StatusTooManyRequests
	case errors.Is(err, ErrConflict):
		return http.StatusConflict
	case errors.Is(err, ErrExpired):
		return http.StatusGone
	case errors.Is(err, ErrStale):
		return http.StatusPreconditionFailed
	case errors.Is(err, ErrControllerUncertain):
		return http.StatusLocked
	case errors.Is(err, ErrPTTUnconfirmed):
		return http.StatusTooEarly
	case errors.Is(err, ErrPTTUnavailable):
		return http.StatusFailedDependency
	case errors.Is(err, ErrCapacity):
		return http.StatusInsufficientStorage
	default:
		return http.StatusServiceUnavailable
	}
}

// ManagerClient talks only to the same-user foreground controller socket.
type ManagerClient struct{ http *http.Client }

func NewManagerClient(dir string) *ManagerClient {
	socket := filepath.Join(dir, managerControlSocket)
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
			return nil, ErrInvalid
		}
		if err := validateManagerSocket(socket); err != nil {
			return nil, err
		}
		var dialer net.Dialer
		return dialer.DialContext(ctx, "unix", socket)
	}}
	client := &http.Client{
		Transport: transport, Timeout: 40 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return &ManagerClient{http: client}
}

func (c *ManagerClient) Close() { c.http.CloseIdleConnections() }

func (c *ManagerClient) Status(ctx context.Context) (ManagerStatus, error) {
	var result ManagerStatus
	if err := c.call(ctx, http.MethodGet, "/v1/status", nil, &result); err != nil {
		return ManagerStatus{}, err
	}
	binding := result.Binding
	if result.CustodyLost && !result.Ready && binding.ObservationGeneration == "" {
		// Older ledgers lack an observation identity. Show their quarantine without inventing one.
		_, _, validEndpoint := protocol.ParseID(binding.SessionID)
		if !validEndpoint || binding.ConversationID == "" || binding.SessionInstance == "" {
			return ManagerStatus{}, ErrCorrupt
		}
	} else if !validBinding(binding) || runtimeBinding(binding).Validate() != nil {
		return ManagerStatus{}, ErrCorrupt
	}
	if binding.Generation == 0 {
		return ManagerStatus{}, ErrCorrupt
	}
	return result, nil
}

// SubmitText durably queues text using the caller's stable operation ID. Repeat
// the same ID and exact text on an ambiguous reply.
func (c *ManagerClient) SubmitText(ctx context.Context, id, text string) (Admission, error) {
	var result Admission
	if err := c.call(ctx, http.MethodPost, "/v1/text", &managerControlRequest{ID: id, Text: text}, &result); err != nil {
		return Admission{}, err
	}
	if result.Sequence == 0 || result.SourceID != "utterance:"+id || result.ConversationID == "" {
		return Admission{}, ErrCorrupt
	}
	return result, nil
}

// Receipt gives the last durable admission state for an exact operation ID.
func (c *ManagerClient) Receipt(ctx context.Context, id string) (Admission, error) {
	var result Admission
	if err := c.call(ctx, http.MethodPost, "/v1/receipt", &managerControlRequest{ID: id}, &result); err != nil {
		return Admission{}, err
	}
	if result.Sequence == 0 || result.SourceID != "utterance:"+id || result.ConversationID == "" {
		return Admission{}, ErrCorrupt
	}
	return result, nil
}

func nextPublication(state State, receipt Admission, after uint64) *Publication {
	var next *Publication
	for _, publication := range state.Publications {
		if publication.ConversationID == receipt.ConversationID && publication.Admission == receipt.Sequence &&
			publication.Sequence > after && (next == nil || publication.Sequence < next.Sequence) {
			item := publication
			next = &item
		}
	}
	return next
}

// publicationPage uses base64 for text so one full-sized canonical phrase fits
// even when its Unicode or control characters would expand as JSON escapes.
type publicationPage struct {
	Publication *Publication `json:"publication,omitempty"`
	Text        []byte       `json:"text,omitempty"`
}

// Publications retrieves the native manager's canonical text for this admission.
func (c *ManagerClient) Publications(ctx context.Context, id string) ([]Publication, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	result := make([]Publication, 0)
	request := managerControlRequest{ID: id}
	for len(result) < MaxRecords {
		var page publicationPage
		if err := c.call(ctx, http.MethodPost, "/v1/publications", &request, &page); err != nil {
			return nil, err
		}
		if page.Publication == nil {
			return result, nil
		}
		if page.Publication.Sequence <= request.After || len(page.Text) == 0 || len(page.Text) > MaxUtteranceBytes || !utf8.Valid(page.Text) || hashBytes(page.Text) != page.Publication.TextHash {
			return nil, ErrCorrupt
		}
		page.Publication.Text = string(page.Text)
		result = append(result, *page.Publication)
		request.After = page.Publication.Sequence
	}
	return nil, ErrCapacity
}

func (c *ManagerClient) call(ctx context.Context, method, path string, value *managerControlRequest, result any) error {
	if value == nil {
		return c.callJSON(ctx, method, path, nil, result)
	}
	if !utf8.ValidString(value.ID) || !utf8.ValidString(value.Text) || len(value.Text) > MaxUtteranceBytes {
		return ErrInvalid
	}
	return c.callJSON(ctx, method, path, value, result)
}

func (c *ManagerClient) callJSON(ctx context.Context, method, path string, value any, result any) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var body io.Reader
	if value != nil {
		raw, err := json.Marshal(value)
		if err != nil || len(raw) > MaxControlFrameBytes {
			return ErrInvalid
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://voice-control"+path, body)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.perform(req, result)
}

func (c *ManagerClient) perform(req *http.Request, result any) error {
	response, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusBadRequest:
		return ErrInvalid
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusTooManyRequests:
		return ErrCaptureBusy
	case http.StatusConflict:
		return ErrConflict
	case http.StatusGone:
		return ErrExpired
	case http.StatusPreconditionFailed:
		return ErrStale
	case http.StatusLocked:
		return ErrControllerUncertain
	case http.StatusTooEarly:
		return ErrPTTUnconfirmed
	case http.StatusFailedDependency:
		return ErrPTTUnavailable
	case http.StatusInsufficientStorage:
		return ErrCapacity
	default:
		return fmt.Errorf("voice: manager control request failed (HTTP %d)", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, MaxControlFrameBytes+1))
	if err != nil {
		return err
	}
	if len(raw) > MaxControlFrameBytes || response.Header.Get("Content-Type") != "application/json" ||
		jsontext.ValidateUnicode(raw) != nil || json.Unmarshal(raw, result) != nil {
		return ErrCorrupt
	}
	return nil
}
