package voice

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"sync"
	"time"
)

var ErrCaptureBusy = errors.New("voice: another microphone capture is active; inspect swarm voice capture")

// CaptureLease is an owner-only view. The opaque ID fences an explicit end or takeover.
type CaptureLease struct {
	ID         string    `json:"lease_id,omitempty"`
	Source     string    `json:"source,omitempty"`
	Generation uint64    `json:"generation,omitempty"`
	ExpiresAt  time.Time `json:"expires_at,omitzero"`
}

type captureOwner struct {
	CaptureLease
	utterance, session string
}

type captureSlot struct {
	mu      sync.Mutex
	current captureOwner
}

func (s *captureSlot) activeLocked() captureOwner {
	if s.current.ID != "" && !time.Now().Before(s.current.ExpiresAt) {
		s.current = captureOwner{}
	}
	return s.current
}

func (s *captureSlot) matchesLocked(id, source, session string) bool {
	owner := s.activeLocked()
	return owner.ID != "" && owner.utterance == id && owner.Source == source && owner.session == session
}

func (s *captureSlot) release(id, source, session string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.matchesLocked(id, source, session) {
		s.current = captureOwner{}
	}
}

func (c *managerControl) beginCapture(id, source, session, expected string) (PTTCapture, error) {
	if c.controller == nil {
		return PTTCapture{}, ErrStale
	}
	if c.preparePTT == nil {
		return PTTCapture{}, ErrPTTUnavailable
	}
	slot := &c.capture
	slot.mu.Lock()
	defer slot.mu.Unlock()
	owner := slot.activeLocked()
	if expected != "" && owner.ID != expected {
		return PTTCapture{}, ErrStale
	}
	st, err := c.store.readView()
	if err != nil {
		return PTTCapture{}, err
	}
	if err := checkBinding(&st, c.binding); err != nil {
		return PTTCapture{}, err
	}
	if st.Conversations[c.binding.ConversationID].CustodyLost {
		return PTTCapture{}, ErrStale
	}
	u, exists := st.Utterances[key(c.binding.ConversationID, id)]
	if exists {
		if expected != "" {
			return PTTCapture{}, ErrConflict
		}
		if u.DeviceID != source {
			return PTTCapture{}, ErrConflict
		}
		if u.Phase != "capturing" {
			return PTTCapture{ID: id, Phase: u.Phase, Admission: u.Admission}, nil
		}
		if !slot.matchesLocked(id, source, session) {
			return PTTCapture{}, ErrExpired
		}
		return PTTCapture{ID: id, Phase: u.Phase}, nil
	}
	if owner.ID != "" && expected == "" {
		return PTTCapture{}, ErrCaptureBusy
	}
	if transcribe, err := c.preparePTT(); err != nil || transcribe == nil {
		return PTTCapture{}, ErrPTTUnavailable
	}
	u, err = c.controller.BeginUtterance(Utterance{ID: id, DeviceID: source, VoiceSessionID: id, MediaEpoch: c.binding.Generation})
	if err != nil {
		return PTTCapture{}, err
	}
	// The longest supported recording is two minutes; allow a further minute for upload and finalization.
	slot.current = captureOwner{CaptureLease: CaptureLease{ID: rand.Text(), Source: source, Generation: c.binding.Generation, ExpiresAt: time.Now().UTC().Add(3 * time.Minute)}, utterance: id, session: session}
	return PTTCapture{ID: u.ID, Phase: u.Phase, Admission: u.Admission}, nil
}

type captureChange struct {
	Expected string `json:"expected"`
	ID       string `json:"id,omitempty"`
}

func (c *managerControl) ownerCaptureAction(r *http.Request) (any, error) {
	if r.Method == http.MethodGet && r.URL.Path == "/v1/capture/status" {
		c.capture.mu.Lock()
		defer c.capture.mu.Unlock()
		return c.capture.activeLocked().CaptureLease, nil
	}
	if r.Method != http.MethodPost {
		return nil, ErrNotFound
	}
	var req captureChange
	if r.URL.Path == "/v1/capture/take" {
		if err := readDeviceJSON(r, &req, "expected", "id"); err != nil || !configText(req.Expected, 128) || !configText(req.ID, 128) {
			return nil, ErrInvalid
		}
		return c.beginCapture(req.ID, localPTTDevice, "", req.Expected)
	}
	if r.URL.Path != "/v1/capture/end" {
		return nil, ErrNotFound
	}
	if err := readDeviceJSON(r, &req, "expected"); err != nil || !configText(req.Expected, 128) {
		return nil, ErrInvalid
	}
	c.capture.mu.Lock()
	defer c.capture.mu.Unlock()
	if c.capture.activeLocked().ID != req.Expected {
		return nil, ErrStale
	}
	c.capture.current = captureOwner{}
	return struct{}{}, nil
}

func (c *ManagerClient) Capture(ctx context.Context) (CaptureLease, error) {
	var lease CaptureLease
	err := c.callJSON(ctx, http.MethodGet, "/v1/capture/status", nil, &lease)
	return lease, err
}
func (c *ManagerClient) EndCapture(ctx context.Context, expected string) error {
	var empty struct{}
	return c.callJSON(ctx, http.MethodPost, "/v1/capture/end", captureChange{Expected: expected}, &empty)
}
func (c *ManagerClient) TakeCapture(ctx context.Context, expected, id string) (PTTCapture, error) {
	var out PTTCapture
	err := c.callJSON(ctx, http.MethodPost, "/v1/capture/take", captureChange{Expected: expected, ID: id}, &out)
	return out, err
}

func (s *captureSlot) end(id, source, session string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.matchesLocked(id, source, session) {
		return ErrStale
	}
	s.current = captureOwner{}
	return nil
}

func (c *ManagerClient) EndPTT(ctx context.Context, id string) error {
	var empty struct{}
	return c.callJSON(ctx, http.MethodPost, "/v1/ptt/end", &managerControlRequest{ID: id}, &empty)
}
