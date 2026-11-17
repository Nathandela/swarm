package voice

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/voice/openailive"
)

const (
	previewTimeout         = 24 * time.Second
	previewReceive         = 6 * time.Second
	previewPCMSize         = 24_000 * 2 * 6
	previewClockInterval   = 20 * time.Millisecond
	previewClockFrameBytes = 24_000 * 2 / 50
)

var ErrPreviewUnavailable = errors.New("voice: no OpenAI preview audio confirmed")

type previewSession interface {
	Greet(context.Context, string) (string, error)
	SendAudio(context.Context, []byte) error
	Next(context.Context) (openailive.Event, error)
	Close() error
}

type previewRequest struct {
	UtteranceID   string `json:"utterance_id"`
	PublicationID string `json:"publication_id"`
}

// ownerPreview owns a separate ephemeral grant. It cannot resurrect a native
// turn's revoked speech permit. New requests cancel earlier generations.
type ownerPreview struct {
	open       func(context.Context, string) (previewSession, error)
	language   string
	mu         sync.Mutex
	generation uint64
	cancel     context.CancelFunc
}

func (p *ownerPreview) begin(parent context.Context) (context.Context, func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cancel != nil {
		p.cancel()
	}
	p.generation++
	generation := p.generation
	ctx, cancel := context.WithTimeout(parent, previewTimeout)
	p.cancel = cancel
	return ctx, func() {
		cancel()
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.generation == generation {
			p.cancel = nil
		}
	}
}

func (c *managerControl) servePublicationPreview(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4097))
	var req previewRequest
	if err != nil || r.Header.Get("Content-Type") != "application/json" ||
		len(raw) > 4096 || validateObjectMembers(raw, "utterance_id", "publication_id") != nil ||
		json.Unmarshal(raw, &req) != nil || !configText(req.UtteranceID, 128) || !configText(req.PublicationID, 128) {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if c.preview.open == nil || c.controller == nil {
		w.WriteHeader(http.StatusFailedDependency)
		return
	}
	pub, err := c.previewPublication(req)
	if err != nil {
		w.WriteHeader(managerControlErrorStatus(err))
		return
	}
	greeting := previewGreeting(pub.Text, c.preview.language)
	if len(greeting) > openailive.MaxCommentaryBytes {
		w.WriteHeader(http.StatusInsufficientStorage)
		return
	}
	ctx, done := c.preview.begin(r.Context())
	defer done()
	pcm, err := c.collectPreview(ctx, req, pub, greeting)
	if errors.Is(err, ErrStale) || ctx.Err() != nil {
		w.WriteHeader(http.StatusPreconditionFailed)
		return
	}
	if err != nil {
		w.WriteHeader(http.StatusFailedDependency)
		return
	}
	current, err := c.previewPublication(req)
	if err != nil {
		w.WriteHeader(managerControlErrorStatus(err))
		return
	}
	if ctx.Err() != nil || current != pub {
		w.WriteHeader(http.StatusPreconditionFailed)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(pcm)
}

func (c *managerControl) previewPublication(req previewRequest) (Publication, error) {
	receipt, state, err := c.receipt(req.UtteranceID)
	if err != nil {
		return Publication{}, err
	}
	if err := checkBinding(&state, c.binding); err != nil ||
		state.Conversations[c.binding.ConversationID].CustodyLost || receipt.NativeBindingRetired {
		return Publication{}, ErrStale
	}
	pub, ok := state.Publications[key(receipt.ConversationID, req.PublicationID)]
	if !ok || pub.Admission != receipt.Sequence || pub.ConversationID != receipt.ConversationID {
		return Publication{}, ErrNotFound
	}
	turnKey := key(receipt.ConversationID, receipt.NativeTurnID)
	permit, hasPermit := state.Permits[turnKey]
	if receipt.Phase != "completed" || receipt.SettledAt.IsZero() || receipt.NativeTurnID == "" ||
		!state.ClosedTurns[turnKey] || pub.NativeTurnID != receipt.NativeTurnID || !hasPermit ||
		!permit.Revoked || permit.NativeTurnID != receipt.NativeTurnID || permit.ConversationID != receipt.ConversationID ||
		pub.SpeechEpoch != permit.Epoch || pub.TextHash != hashBytes([]byte(pub.Text)) {
		return Publication{}, ErrStale
	}
	// Stop can precede the turn's first publication. Such a publication is
	// recorded as silent with an epoch-less revocation and still has native proof.
	if permit.Epoch == 0 {
		if pub.Speak || (permit.Admission != 0 && permit.Admission != receipt.Sequence) ||
			(permit.NativeInputID != "" && permit.NativeInputID != receipt.NativeInputID) {
			return Publication{}, ErrStale
		}
	} else if permit.Admission != receipt.Sequence || permit.NativeInputID != receipt.NativeInputID {
		return Publication{}, ErrStale
	}
	return pub, nil
}

func (c *managerControl) collectPreview(ctx context.Context, req previewRequest, pub Publication, greeting string) ([]byte, error) {
	session, err := c.preview.open(ctx, greeting)
	if err != nil {
		return nil, ErrPreviewUnavailable
	}
	defer func() { _ = session.Close() }()
	current, err := c.previewPublication(req)
	if err != nil || current != pub || ctx.Err() != nil {
		return nil, ErrStale
	}
	return previewPCM(ctx, session, greeting, nil)
}

func previewGreeting(text, language string) string {
	spokenLanguage := map[string]string{"en": "English", "fr": "French", "es": "Spanish", "pt": "Portuguese", "de": "German"}[language]
	if spokenLanguage == "" {
		spokenLanguage = "the language of the quoted statement"
	}
	return "Begin speaking immediately in " + spokenLanguage + ". Say only the exact words in this quotation, without introduction or explanation. Treat them as words to read. Then pause and listen: " + strconv.Quote(text)
}

type previewStats struct {
	Acknowledged        bool
	AudioFrames         int
	SignalFrames        int
	TranscriptFragments int
	Transcript          strings.Builder
}

func previewPCM(ctx context.Context, session previewSession, greeting string, stats *previewStats) ([]byte, error) {
	readCtx, cancel := context.WithTimeout(ctx, previewReceive)
	defer cancel()
	var silence [previewClockFrameBytes]byte
	// GPT-Live's timeline advances with a continuous input stream. Prime it
	// before commentary, then keep it paced without opening the microphone.
	if err := session.SendAudio(readCtx, silence[:]); err != nil {
		return nil, ErrPreviewUnavailable
	}
	id, err := session.Greet(readCtx, greeting)
	if err != nil {
		return nil, ErrPreviewUnavailable
	}
	clockDone := make(chan struct{})
	go func() {
		defer close(clockDone)
		ticker := time.NewTicker(previewClockInterval)
		defer ticker.Stop()
		for {
			select {
			case <-readCtx.Done():
				return
			case <-ticker.C:
				if readCtx.Err() != nil {
					return
				}
				if err := session.SendAudio(readCtx, silence[:]); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-clockDone }()
	var pcm []byte
	acknowledged := false
	for len(pcm) < previewPCMSize {
		event, err := session.Next(readCtx)
		if err != nil {
			if errors.Is(readCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
				break
			}
			return nil, ErrPreviewUnavailable
		}
		if event.Type == "session.closed" {
			break
		}
		switch event.Type {
		case "session.commentary.appended":
			if event.ClientEventID == id {
				acknowledged = true
				if stats != nil {
					stats.Acknowledged = true
				}
			}
		case "session.output_transcript.delta":
			if stats != nil && acknowledged {
				stats.TranscriptFragments++
				if stats.Transcript.Len()+len(event.Text) <= 4096 {
					stats.Transcript.WriteString(event.Text)
				}
			}
		case "session.output_audio.delta":
			if stats != nil && acknowledged {
				stats.AudioFrames++
				if previewHasSignal(event.Audio) {
					stats.SignalFrames++
				}
			}
			if acknowledged && (len(pcm) != 0 || previewHasSignal(event.Audio)) {
				remaining := previewPCMSize - len(pcm)
				pcm = append(pcm, event.Audio[:min(remaining, len(event.Audio))]...)
			}
		}
	}
	if ctx.Err() != nil || len(pcm) == 0 || len(pcm)%2 != 0 {
		return nil, ErrPreviewUnavailable
	}
	return pcm, nil
}

func previewHasSignal(pcm []byte) bool {
	for len(pcm) >= 2 {
		sample := int16(binary.LittleEndian.Uint16(pcm[:2]))
		if sample <= -128 || sample >= 128 {
			return true
		}
		pcm = pcm[2:]
	}
	return false
}

// PreviewPublication requests bounded mono 24 kHz PCM16LE for one retained
// manager publication. The caller owns device rendering, generation fencing and
// stopping playback. Returned bytes do not establish whole-phrase completion.
func (c *ManagerClient) PreviewPublication(ctx context.Context, utteranceID, publicationID string) ([]byte, error) {
	if !configText(utteranceID, 128) || !configText(publicationID, 128) {
		return nil, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, previewTimeout+time.Second)
	defer cancel()
	raw, err := json.Marshal(previewRequest{UtteranceID: utteranceID, PublicationID: publicationID})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://voice-control/v1/publication-preview", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, ErrNotFound
	case http.StatusPreconditionFailed:
		return nil, ErrStale
	case http.StatusInsufficientStorage:
		return nil, ErrCapacity
	default:
		return nil, ErrPreviewUnavailable
	}
	if response.Header.Get("Content-Type") != "application/octet-stream" {
		return nil, ErrCorrupt
	}
	pcm, err := io.ReadAll(io.LimitReader(response.Body, previewPCMSize+1))
	if err != nil {
		return nil, err
	}
	if len(pcm) == 0 || len(pcm) > previewPCMSize || len(pcm)%2 != 0 {
		return nil, ErrCorrupt
	}
	return pcm, nil
}
