// Package callspeech carries one authorized Discord call generation's speech.
package callspeech

import (
	"context"
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
	Speaker, Text, DelegationID, ID string
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
	delegationID string
	text         string
	epoch        uint64
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
	ready     chan struct{}
	done      chan struct{}
	failOnce  sync.Once
	workers   sync.WaitGroup

	mu            sync.Mutex
	delegations   map[string]struct{}
	epoch         uint64
	playCancel    context.CancelFunc
	speakingUntil time.Time
	live          liveSession
}

// New starts the speech workers. Personal mode waits for session.started
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
		output: make(chan publication, 8), ready: make(chan struct{}), done: make(chan struct{}),
		delegations: make(map[string]struct{}),
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

// Publish queues verified native text for an isolated speech render.
func (l *Lane) Publish(text string) error {
	return l.PublishFor("", text)
}

// PublishFor additionally sends a copy of the verified publication to its
// original Live delegation. The interactive session's audio is never played.
func (l *Lane) PublishFor(delegationID, text string) error {
	text = strings.TrimSpace(text)
	if text == "" || !utf8.ValidString(text) {
		return ErrPublication
	}
	return l.queue(delegationID, text)
}

// Acknowledge queues a local nonverbal cue after the caller verified admission.
func (l *Lane) Acknowledge() error {
	return l.queue("", "")
}

func (l *Lane) queue(delegationID, text string) error {
	if l.ctx.Err() != nil {
		return ErrClosed
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if delegationID != "" {
		if _, ok := l.delegations[delegationID]; !ok {
			return ErrPublication
		}
	}
	select {
	case l.output <- publication{delegationID: delegationID, text: text, epoch: l.epoch}:
		return nil
	default:
		return ErrBacklog
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
			if u.turn.ID == "" || (!l.cfg.Team && u.turn.Text == "") {
				ctx, cancel := context.WithTimeout(l.ctx, 8*time.Second)
				text, err := l.provider.transcribe(ctx, l.cfg.Key, u.pcm, l.cfg.Language)
				cancel()
				if err != nil {
					l.fail(err)
					return
				}
				if l.cfg.Team {
					u.turn.Text = addressedText(text)
					if u.turn.Text != "" && len(u.turn.Text) <= maxTurnText {
						select {
						case l.addressed <- u:
						case <-l.ctx.Done():
							return
						default:
							l.fail(ErrBacklog)
							return
						}
					}
					continue
				}
				u.turn.Text = strings.TrimSpace(text)
			}
			if l.ctx.Err() == nil && u.turn.Text != "" && len(u.turn.Text) <= maxTurnText && l.cfg.Turn != nil {
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

func (l *Lane) voiced(now time.Time, start bool) {
	l.mu.Lock()
	l.speakingUntil = now.Add(quietTime)
	if !start {
		l.mu.Unlock()
		return
	}
	l.epoch++
	if l.playCancel != nil {
		l.playCancel()
	}
	l.mu.Unlock()
	if l.cfg.Stop != nil && l.ctx.Err() == nil {
		l.cfg.Stop()
	}
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
	l.mu.Lock()
	live := l.live
	current := l.epoch == p.epoch && l.ctx.Err() == nil && !time.Now().Before(l.speakingUntil)
	ctx, cancel := context.WithCancel(l.ctx)
	if current {
		l.playCancel = cancel
	}
	l.mu.Unlock()
	defer cancel()
	if l.ctx.Err() != nil {
		return
	}
	text := boundedSpeech(p.text)
	if p.delegationID != "" && live != nil {
		commentaryCtx, stop := context.WithTimeout(l.ctx, 500*time.Millisecond)
		err := live.SpeakFor(commentaryCtx, p.delegationID, text)
		stop()
		if err != nil {
			l.fail(err)
			return
		}
	}
	if !current || ctx.Err() != nil || l.cfg.Play == nil {
		return
	}
	var pcm []byte
	if text == "" {
		pcm = localCue()
	} else {
		var err error
		pcm, err = l.provider.render(ctx, l.cfg.Key, l.cfg.Voice, text)
		if err != nil {
			if ctx.Err() == nil {
				slog.Debug("call publication speech rendering failed", "error", err)
			}
			return
		}
	}
	ticker := time.NewTicker(framePeriod)
	defer ticker.Stop()
	for len(pcm) != 0 && ctx.Err() == nil {
		var chunk [frameBytes]byte
		n := copy(chunk[:], pcm)
		if err := l.cfg.Play(ctx, chunk[:]); err != nil {
			if ctx.Err() == nil {
				l.fail(err)
			}
			return
		}
		pcm = pcm[n:]
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
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
