// Package callspeech carries one authorized Discord call generation's speech.
package callspeech

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/voice/openailive"
)

const (
	frameBytes             = 960
	framePeriod            = 20 * time.Millisecond
	queueSize              = 32
	maxDelegations         = 256
	spokenPublicationBytes = 160
)

var (
	ErrClosed      = errors.New("call speech closed")
	ErrBacklog     = errors.New("call speech queue full")
	ErrPCM         = errors.New("invalid call speech frame")
	ErrPublication = errors.New("invalid call speech publication")
)

type Turn struct {
	Speaker, Text, ID string
	LastVoicedAt      time.Time
}

// Config callbacks belong to a single call generation. The caller must fence
// Turn and Play against current authorization and associate publication IDs
// with their native receipts before requesting playback. Stop clears the media
// bridge's current output queue synchronously.
type Config struct {
	Key, Voice, Language string
	Team                 bool
	Turn                 func(context.Context, Turn)
	Failed               func(error)
	Play                 func(context.Context, []byte) error
	PlaybackStarted      func(string)
	ReadoutFailed        func(string)
	Stop                 func()
}

type liveSession interface {
	SendAudio(context.Context, []byte) error
	SpeakFor(context.Context, string, string) error
	Next(context.Context) (openailive.Event, error)
	Close() error
}

type provider struct {
	open       func(context.Context, string, string) (liveSession, error)
	transcribe func(context.Context, string, []byte, string) (string, error)
	render     func(context.Context, string, string, string) ([]byte, error)
}

type frame struct {
	speaker string
	pcm     [frameBytes]byte
	at      time.Time
}

type utterance struct {
	turn Turn
	pcm  []byte
}

type publication struct {
	turnID string
	text   string
	epoch  uint64
}

type liveCommentary struct {
	delegationID, text string
}

type turnScope struct {
	delegationID string
	pending      []string
	settled      bool
}

type Lane struct {
	cfg       Config
	provider  provider
	ctx       context.Context
	cancel    context.CancelFunc
	ingress   chan frame
	turns     chan utterance
	addressed chan utterance
	output    chan publication
	comments  chan liveCommentary
	ready     chan struct{}
	done      chan struct{}
	failOnce  sync.Once
	workers   sync.WaitGroup

	mu            sync.Mutex
	scopes        map[string]turnScope
	epoch         uint64
	playCancel    context.CancelFunc
	playStarted   time.Time
	stopping      chan struct{}
	speakingUntil time.Time
}

// New starts the speech workers. Both modes wait for session.started
// before sending PCM. The failure callback reports startup or later failure.
func New(ctx context.Context, cfg Config) *Lane {
	return newLane(ctx, cfg, provider{
		open: func(ctx context.Context, key, voice string) (liveSession, error) {
			return openailive.OpenDispatcher(ctx, key, voice)
		},
		transcribe: openailive.Transcribe,
		render:     openailive.Render,
	})
}

func newLane(ctx context.Context, cfg Config, provider provider) *Lane {
	ctx, cancel := context.WithCancel(ctx)
	l := &Lane{cfg: cfg, provider: provider, ctx: ctx, cancel: cancel,
		ingress: make(chan frame, queueSize), turns: make(chan utterance, 4), addressed: make(chan utterance, 2),
		output: make(chan publication, 8), comments: make(chan liveCommentary, 16), ready: make(chan struct{}), done: make(chan struct{}),
		scopes: make(map[string]turnScope),
	}
	for _, worker := range []func(){l.run, l.admit, l.play} {
		l.workers.Go(worker)
	}
	go func() {
		l.workers.Wait()
		close(l.done)
	}()
	return l
}

// Push takes one attributed 20ms frame. A missing frame can invalidate the
// utterance, so an overflowing ingress fails the call rather than splicing it.
func (l *Lane) Push(speaker string, pcm []byte) error {
	if speaker == "" || len(speaker) > 128 || len(pcm) != frameBytes {
		return ErrPCM
	}
	if l.ctx.Err() != nil {
		return ErrClosed
	}
	next := frame{speaker: speaker, at: time.Now()}
	copy(next.pcm[:], pcm)
	select {
	case l.ingress <- next:
		return nil
	default:
		l.fail(ErrBacklog)
		return ErrBacklog
	}
}

// PublishForTurn queues verified publication text for isolated playback and
// forwards commentary only once that turn has a correlated Live delegation.
func (l *Lane) PublishForTurn(turnID, text string) error {
	text = strings.TrimSpace(text)
	if turnID == "" || text == "" || !utf8.ValidString(text) {
		return ErrPublication
	}
	text = boundedSpeech(text)
	l.mu.Lock()
	defer l.mu.Unlock()
	scope, ok := l.scopes[turnID]
	if !ok {
		return ErrPublication
	}
	if err := l.queueLocked(turnID, text); err != nil {
		return err
	}
	if scope.delegationID != "" {
		l.comment(liveCommentary{scope.delegationID, text})
	} else if !scope.settled && len(scope.pending) < 4 {
		scope.pending = append(scope.pending, text)
		l.scopes[turnID] = scope
	}
	return nil
}

// Acknowledge queues a local nonverbal cue after the caller verified admission.
func (l *Lane) Acknowledge() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.queueLocked("", "")
}

func (l *Lane) queueLocked(turnID, text string) error {
	if l.ctx.Err() != nil {
		return ErrClosed
	}
	select {
	case l.output <- publication{turnID: turnID, text: text, epoch: l.epoch}:
		return nil
	default:
		return ErrBacklog
	}
}

func (l *Lane) registerTurn(id string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.scopes) >= maxDelegations {
		l.fail(ErrBacklog)
		return false
	}
	l.scopes[id] = turnScope{}
	return true
}

func (l *Lane) routeCommentary(turnID, delegationID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	scope, ok := l.scopes[turnID]
	if !ok {
		return
	}
	for _, text := range scope.pending {
		if delegationID != "" {
			l.comment(liveCommentary{delegationID, text})
		}
	}
	l.scopes[turnID] = turnScope{delegationID: delegationID, settled: true}
}

func (l *Lane) comment(c liveCommentary) {
	select {
	case l.comments <- c:
	default:
		l.fail(ErrBacklog)
	}
}

// Close immediately revokes provider work and pending playback. The caller
// owns generation fencing and may call Close while holding the call lock.
func (l *Lane) Close() { l.cancel() }

// Ready closes after the provider validates session.started.
func (l *Lane) Ready() <-chan struct{} { return l.ready }

// Done closes after the audio and provider workers have exited.
func (l *Lane) Done() <-chan struct{} { return l.done }

func (l *Lane) fail(err error) {
	l.failOnce.Do(func() {
		if l.ctx.Err() != nil {
			return
		}
		l.cancel()
		if l.cfg.Failed != nil {
			go l.cfg.Failed(err)
		}
	})
}

func (l *Lane) admit() {
	for {
		select {
		case <-l.ctx.Done():
			return
		case u := <-l.turns:
			if l.ctx.Err() != nil {
				return
			}
			ctx, cancel := context.WithTimeout(l.ctx, 8*time.Second)
			text, err := l.provider.transcribe(ctx, l.cfg.Key, u.pcm, l.cfg.Language)
			cancel()
			if err != nil {
				l.fail(err)
				return
			}
			u.turn.Text = strings.TrimSpace(text)
			if l.cfg.Team {
				u.turn.Text = addressedText(text)
			}
			if l.ctx.Err() != nil || u.turn.Text == "" || len(u.turn.Text) > maxTurnText {
				continue
			}
			if l.cfg.Team {
				u.turn.ID = rand.Text()
				if !l.registerTurn(u.turn.ID) {
					return
				}
				l.interruptPlayback(u.turn.LastVoicedAt)
				select {
				case l.addressed <- u:
				case <-l.ctx.Done():
					return
				default:
					l.fail(ErrBacklog)
					return
				}
			}
			if l.cfg.Turn != nil {
				l.cfg.Turn(l.ctx, u.turn)
			}
		}
	}
}

func (l *Lane) enqueue(u utterance) {
	select {
	case l.turns <- u:
	default:
		l.fail(ErrBacklog)
	}
}

func (l *Lane) holdPlayback(until time.Time) {
	l.mu.Lock()
	if until.After(l.speakingUntil) {
		l.speakingUntil = until
	}
	l.mu.Unlock()
}

// Recognition can finish after the floor reopens and a new readout starts.
func (l *Lane) interruptPlayback(lastVoiced time.Time) {
	l.mu.Lock()
	if l.playStarted.After(lastVoiced) {
		l.mu.Unlock()
		return
	}
	l.epoch++
	if l.playCancel != nil {
		l.playCancel()
	}
	if l.cfg.Stop == nil || l.ctx.Err() != nil {
		l.mu.Unlock()
		return
	}
	stopping := make(chan struct{})
	l.stopping = stopping
	l.mu.Unlock()
	l.cfg.Stop()
	l.mu.Lock()
	close(stopping)
	l.stopping = nil
	l.mu.Unlock()
}

func (l *Lane) play() {
	for {
		select {
		case <-l.ctx.Done():
			return
		case p := <-l.output:
			l.playPublication(p)
		}
	}
}

func (l *Lane) playPublication(p publication) {
	if l.ctx.Err() != nil || l.cfg.Play == nil {
		return
	}
	ctx, cancel := context.WithCancel(l.ctx)
	defer cancel()
	var pcm []byte
	if p.text == "" {
		pcm = localCue()
	} else {
		var err error
		pcm, err = l.provider.render(ctx, l.cfg.Key, l.cfg.Voice, p.text)
		if err != nil {
			if ctx.Err() == nil {
				slog.Debug("call publication speech rendering failed", "error", err)
				if l.cfg.ReadoutFailed != nil {
					l.cfg.ReadoutFailed(p.turnID)
				}
			}
			return
		}
	}
	if !l.beginPlayback(p, cancel) {
		return
	}
	defer func() {
		l.mu.Lock()
		l.playCancel = nil
		l.mu.Unlock()
	}()
	ticker := time.NewTicker(framePeriod)
	defer ticker.Stop()
	firstFrame := true
	for len(pcm) != 0 && ctx.Err() == nil {
		var chunk [frameBytes]byte
		n := copy(chunk[:], pcm)
		if err := l.cfg.Play(ctx, chunk[:]); err != nil {
			if ctx.Err() == nil {
				l.fail(err)
			}
			return
		}
		if firstFrame && p.turnID != "" && l.cfg.PlaybackStarted != nil {
			l.cfg.PlaybackStarted(p.turnID)
		}
		firstFrame = false
		pcm = pcm[n:]
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (l *Lane) beginPlayback(p publication, cancel context.CancelFunc) bool {
	for {
		l.mu.Lock()
		wait := time.Until(l.speakingUntil)
		stopping := l.stopping
		if l.ctx.Err() != nil || (p.text == "" && (p.epoch != l.epoch || wait > 0)) {
			l.mu.Unlock()
			return false
		}
		if stopping == nil && wait <= 0 {
			l.playCancel = cancel
			l.playStarted = time.Now()
			l.mu.Unlock()
			return true
		}
		l.mu.Unlock()
		if stopping != nil {
			select {
			case <-l.ctx.Done():
				return false
			case <-stopping:
			}
			continue
		}
		timer := time.NewTimer(wait)
		select {
		case <-l.ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
}

func boundedSpeech(text string) string {
	if len(text) <= spokenPublicationBytes {
		return text
	}
	end := spokenPublicationBytes - len("…")
	for !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end] + "…"
}
