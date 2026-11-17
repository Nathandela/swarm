package voice

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/managerproof"
	"github.com/Nathandela/swarm/internal/voice/openailive"
)

// MaxPTTBytes is the maximum size of one mono 24 kHz PCM16LE utterance.
const MaxPTTBytes = openailive.MaxUtteranceBytes

const localPTTDevice = "local-ptt"

var (
	ErrPTTUnconfirmed = errors.New("voice: push-to-talk transcription in progress or unconfirmed; query the same utterance ID")
	ErrPTTUnavailable = errors.New("voice: push-to-talk transcription unavailable")
)

type pttTranscriber func(context.Context, []byte) (string, error)

// PTTCapture is the durable acknowledgment of an explicit capture.
type PTTCapture struct {
	ID        string `json:"id"`
	Phase     string `json:"phase"`
	Admission uint64 `json:"admission,omitempty"`
}

func (c *managerControl) beginPTT(id string) (PTTCapture, error) {
	return c.beginScopedPTT(id, localPTTDevice)
}

func (c *managerControl) beginScopedPTT(id, source string) (PTTCapture, error) {
	if c.controller == nil {
		return PTTCapture{}, ErrStale
	}
	if c.preparePTT == nil {
		return PTTCapture{}, ErrPTTUnavailable
	}
	if err := c.store.checkControllerBinding(c.binding); err != nil {
		if errors.Is(err, managerproof.ErrQuarantined) {
			return PTTCapture{}, ErrStale
		}
		return PTTCapture{}, err
	}
	u, err := c.controller.BeginUtterance(Utterance{
		ID: id, DeviceID: source, VoiceSessionID: id, MediaEpoch: 1,
	})
	return PTTCapture{ID: u.ID, Phase: u.Phase, Admission: u.Admission}, err
}

func (c *managerControl) receivePTT(r *http.Request) (Admission, error) {
	if c.controller == nil {
		return Admission{}, ErrStale
	}
	ids := r.Header.Values("Swarm-Utterance-ID")
	if len(ids) != 1 || !configText(ids[0], 128) || r.Header.Get("Content-Type") != "application/octet-stream" || r.ContentLength > MaxPTTBytes {
		return Admission{}, ErrInvalid
	}
	id := ids[0]
	state, err := c.store.Snapshot()
	if err != nil {
		return Admission{}, err
	}
	if _, err = pttUtterance(&state, c.binding, id, localPTTDevice); err != nil {
		return Admission{}, err
	}
	select {
	case c.pttReads <- struct{}{}:
		defer func() { <-c.pttReads }()
	default:
		return Admission{}, ErrCapacity
	}
	pcm, err := io.ReadAll(io.LimitReader(r.Body, MaxPTTBytes+1))
	if err != nil || !validPTTAudio(pcm) {
		return Admission{}, ErrInvalid
	}
	if err := r.Context().Err(); err != nil {
		return Admission{}, err
	}

	return c.finalizeScopedPTT(r.Context(), id, localPTTDevice, pcm, nil, nil)
}

func (c *managerControl) finalizeScopedPTT(parent context.Context, id, source string, pcm []byte, claimFence, commitFence func(func() error) error) (Admission, error) {
	var transcribe pttTranscriber
	if c.preparePTT != nil {
		transcribe, _ = c.preparePTT()
	}
	digest := hashBytes(pcm)
	var receipt Admission
	claim := func() (err error) {
		receipt, err = c.store.claimPTT(parent, c.binding, id, source, digest, transcribe != nil)
		return err
	}
	var err error
	if claimFence != nil {
		err = claimFence(claim)
	} else {
		err = claim()
	}
	if err != nil || receipt.Sequence != 0 {
		return receipt, err
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	if ctx.Err() != nil {
		return Admission{}, ErrPTTUnconfirmed
	}
	text, err := transcribe(ctx, pcm)
	if err != nil || ctx.Err() != nil {
		return Admission{}, ErrPTTUnconfirmed
	}
	commit := func() error {
		var err error
		receipt, err = c.store.commitPTT(ctx, c.binding, id, source, digest, text)
		if err != nil {
			return ErrPTTUnconfirmed
		}
		return nil
	}
	if commitFence != nil {
		err = commitFence(commit)
	} else {
		err = commit()
	}
	if err != nil {
		return Admission{}, err
	}
	select {
	case c.wake <- struct{}{}:
	default:
	}
	return receipt, nil
}

func validPTTAudio(pcm []byte) bool {
	return len(pcm) > 0 && len(pcm) <= MaxPTTBytes && len(pcm)%2 == 0
}

func pttUtterance(st *State, b ManagerBinding, id, source string) (Utterance, error) {
	if err := checkBinding(st, b); err != nil {
		return Utterance{}, err
	}
	if st.Conversations[b.ConversationID].CustodyLost {
		return Utterance{}, ErrStale
	}
	u, ok := st.Utterances[key(b.ConversationID, id)]
	if !ok {
		return Utterance{}, ErrNotFound
	}
	if u.DeviceID != source {
		return Utterance{}, ErrConflict
	}
	return u, nil
}

// claimPTT commits the digest and in-flight state before any provider request.
func (s *Store) claimPTT(ctx context.Context, b ManagerBinding, id, source, digest string, ready bool) (Admission, error) {
	var result Admission
	err := s.update(func(st *State) error {
		u, err := pttUtterance(st, b, id, source)
		if err != nil {
			return err
		}
		if u.AudioHash != "" && u.AudioHash != digest {
			return ErrConflict
		}
		if u.Admission != 0 && u.AudioHash == digest {
			var retained bool
			result, retained = st.Admissions[admissionKey(b.ConversationID, u.Admission)]
			if !retained {
				return ErrExpired
			}
			return nil
		}
		if u.Phase != "capturing" || u.AudioHash != "" {
			return ErrPTTUnconfirmed
		}
		if !ready {
			return ErrPTTUnavailable
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		u.AudioHash, u.Phase = digest, "finalizing"
		putRecord(st, "utterances", &st.Utterances, key(b.ConversationID, id), u)
		return nil
	})
	result.Request, result.InputText = nil, ""
	return result, err
}

func (s *Store) commitPTT(ctx context.Context, b ManagerBinding, id, source, digest, text string) (Admission, error) {
	var result Admission
	if strings.TrimSpace(text) == "" || len(text) > MaxUtteranceBytes || !utf8.ValidString(text) {
		return result, ErrInvalid
	}
	err := s.update(func(st *State) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		u, err := pttUtterance(st, b, id, source)
		if err != nil {
			return err
		}
		if u.Phase != "finalizing" || u.AudioHash != digest || u.Admission != 0 {
			return ErrConflict
		}
		a, err := allocateAdmission(st, b, "utterance:"+id)
		if err != nil {
			return err
		}
		a.RecoveryOnly = u.RecoveryOnly
		putRecord(st, "admissions", &st.Admissions, admissionKey(b.ConversationID, a.Sequence), a)
		u.Text, u.TextHash, u.Phase, u.Admission = text, hashBytes([]byte(text)), "admitted", a.Sequence
		putRecord(st, "utterances", &st.Utterances, key(b.ConversationID, id), u)
		result = a
		return nil
	})
	return result, err
}

// BeginPTT durably allocates the caller's stable ID before audio is accepted.
func (c *ManagerClient) BeginPTT(ctx context.Context, id string) (PTTCapture, error) {
	var capture PTTCapture
	if err := c.call(ctx, http.MethodPost, "/v1/ptt/begin", &managerControlRequest{ID: id}, &capture); err != nil {
		return PTTCapture{}, err
	}
	if capture.ID != id || capture.Phase == "" {
		return PTTCapture{}, ErrCorrupt
	}
	return capture, nil
}

// FinalizePTT submits one exact PCM utterance. If the reply is lost, Receipt
// queries the ID without resending audio. An exact repeat cannot call the provider again.
func (c *ManagerClient) FinalizePTT(ctx context.Context, id string, pcm []byte) (Admission, error) {
	if !configText(id, 128) || !validPTTAudio(pcm) {
		return Admission{}, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://voice-control/v1/ptt/finalize", bytes.NewReader(pcm))
	if err != nil {
		return Admission{}, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Swarm-Utterance-ID", id)
	var receipt Admission
	if err = c.perform(req, &receipt); err != nil {
		return Admission{}, err
	}
	if receipt.Sequence == 0 || receipt.SourceID != "utterance:"+id || receipt.ConversationID == "" {
		return Admission{}, ErrCorrupt
	}
	return receipt, nil
}
