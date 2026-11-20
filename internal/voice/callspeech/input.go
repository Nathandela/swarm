package callspeech

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/voice/openailive"
)

const (
	quietTime   = 420 * time.Millisecond
	tailTime    = 1_100 * time.Millisecond
	stableTime  = 220 * time.Millisecond
	hardTail    = 2_200 * time.Millisecond
	maxSamples  = 20 * 50 // Twenty seconds of 20ms frames.
	maxTurnText = 4096
	maxEvents   = 8192
)

type candidate struct {
	turn                           Turn
	start, end                     int64
	first, last, eventAt           time.Time
	text                           strings.Builder
	pcm                            []byte
	transcriptStart, transcriptEnd int64
	voicedFrames                   int
	invalid                        bool
}

type transcript struct {
	current      *candidate
	pending      []*candidate
	events       map[string]struct{}
	delegations  map[string]struct{}
	retiredUntil int64
}

func (l *Lane) run() {
	startup, cancel := context.WithTimeout(l.ctx, 10*time.Second)
	live, err := l.provider.open(startup, l.cfg.Key, l.cfg.Voice)
	cancel()
	if err != nil {
		l.fail(err)
		return
	}
	defer func() { _ = live.Close() }()
	l.mu.Lock()
	l.live = live
	l.mu.Unlock()
	close(l.ready)

	events := make(chan openailive.Event, queueSize)
	l.workers.Go(func() { l.read(live, events) })
	ticker := time.NewTicker(framePeriod)
	defer ticker.Stop()
	tr := transcript{events: make(map[string]struct{}), delegations: make(map[string]struct{})}
	var team *utterance
	var buffered utterance
	var lastVoiced time.Time
	var clock int64
	var overlap bool
	var silence [frameBytes]byte
	var teamIngress <-chan frame
	if l.cfg.Team {
		teamIngress = l.ingress
	}

	send := func(pcm []byte) bool {
		ctx, cancel := context.WithTimeout(l.ctx, 200*time.Millisecond)
		defer cancel()
		if err := live.SendAudio(ctx, pcm); err != nil {
			l.fail(err)
			return false
		}
		clock += int64(framePeriod / time.Millisecond)
		return true
	}

	for l.ctx.Err() == nil {
		now := time.Now()
		if tr.current != nil && now.Sub(tr.current.last) >= quietTime && team == nil {
			tr.closeCurrent()
		}
		tr.settle(l, now)
		var next <-chan utterance
		if team == nil && tr.current == nil {
			next = l.addressed
		}
		select {
		case <-l.ctx.Done():
			return
		case u := <-next:
			team = &u
		case event := <-events:
			if err := tr.add(event, time.Now()); err != nil {
				l.fail(err)
				return
			}
		case f := <-teamIngress:
			if now.Sub(f.at) > 160*time.Millisecond {
				l.fail(ErrBacklog)
				return
			}
			voiced := hasSignal(f.pcm[:])
			if gap := f.at.Sub(lastVoiced); buffered.turn.Speaker != "" && gap >= quietTime {
				l.finishTeam(buffered, overlap)
				buffered, overlap = utterance{}, false
			}
			if voiced {
				l.voiced(f.at, buffered.turn.Speaker == "")
				if buffered.turn.Speaker == "" {
					buffered.turn.Speaker = f.speaker
				}
				lastVoiced = f.at
				if f.speaker != buffered.turn.Speaker {
					overlap = true
				}
			}
			if !overlap && buffered.turn.Speaker == f.speaker {
				if len(buffered.pcm) >= frameBytes*maxSamples {
					overlap = true
				} else {
					buffered.pcm = append(buffered.pcm, f.pcm[:]...)
				}
			}
		case <-ticker.C:
			now := time.Now()
			if !l.cfg.Team {
				var f frame
				select {
				case f = <-l.ingress:
					if now.Sub(f.at) > 160*time.Millisecond {
						l.fail(ErrBacklog)
						return
					}
				default:
				}
				voiced := f.speaker != "" && hasSignal(f.pcm[:])
				if voiced {
					l.voiced(f.at, tr.current == nil)
					if tr.current == nil {
						tr.current = &candidate{turn: Turn{Speaker: f.speaker}, first: f.at, start: clock}
					}
					c := tr.current
					if c.turn.Speaker != f.speaker || f.at.Sub(c.first) >= maxSamples*framePeriod {
						c.invalid = true
					}
					c.last = f.at
					c.voicedFrames++
					c.end = clock + int64(framePeriod/time.Millisecond)
				}
				pcm := silence[:]
				if voiced && !tr.current.invalid {
					pcm = f.pcm[:]
				}
				if c := tr.current; c != nil && !c.invalid && len(c.pcm) < maxSamples*frameBytes {
					c.pcm = append(c.pcm, pcm...)
				}
				if !send(pcm) {
					return
				}

				continue
			}
			if buffered.turn.Speaker != "" && now.Sub(lastVoiced) >= quietTime {
				l.finishTeam(buffered, overlap)
				buffered, overlap = utterance{}, false
			}
			if team != nil {
				if tr.current == nil {
					tr.current = &candidate{turn: team.turn, first: now, start: clock}
				}
				c := tr.current
				c.last = now
				c.end = clock + int64(framePeriod/time.Millisecond)
				if !send(team.pcm[:frameBytes]) {
					return
				}
				team.pcm = team.pcm[frameBytes:]
				if len(team.pcm) == 0 {
					team = nil
				}
			} else if !send(silence[:]) {
				return
			}
		}
	}
}

func (l *Lane) read(live liveSession, events chan<- openailive.Event) {
	for l.ctx.Err() == nil {
		event, err := live.Next(l.ctx)
		if err != nil {
			l.fail(err)
			return
		}
		switch event.Type {
		case "session.closed":
			l.fail(errors.New("call Live session closed"))
			return
		case "session.input_transcript.delta", "session.delegation.created":
			select {
			case <-l.ctx.Done():
				return
			case events <- event:
			}
		}
	}
}

func (l *Lane) finishTeam(u utterance, overlap bool) {
	if !overlap && len(u.pcm) >= 5*frameBytes {
		l.enqueue(u)
	}
}

func (t *transcript) closeCurrent() {
	t.pending = append(t.pending, t.current)
	t.current = nil
}

func (t *transcript) add(e openailive.Event, now time.Time) error {
	if e.EventID == "" || len(e.EventID) > 512 {
		return errors.New("invalid call Live event ID")
	}
	if _, duplicate := t.events[e.EventID]; duplicate {
		return nil
	}
	if len(t.events) >= maxEvents {
		return errors.New("call Live transcript capacity exceeded")
	}
	t.events[e.EventID] = struct{}{}
	var choices []*candidate
	choices = append(choices, t.pending...)
	if t.current != nil {
		choices = append(choices, t.current)
	}
	switch e.Type {
	case "session.delegation.created":
		if e.DelegationID == "" || len(e.DelegationID) > 512 || e.OffsetMS < 0 || strings.IndexFunc(e.DelegationID, unicode.IsControl) >= 0 {
			return errors.New("invalid call Live delegation")
		}
		if _, duplicate := t.delegations[e.DelegationID]; duplicate {
			return nil
		}
		if len(t.delegations) >= maxDelegations {
			return errors.New("call Live delegation capacity exceeded")
		}
		t.delegations[e.DelegationID] = struct{}{}
		if e.OffsetMS < t.retiredUntil {
			return nil
		}
		var match *candidate
		for _, c := range choices {
			if e.OffsetMS >= c.start && e.OffsetMS <= c.end+quietTime.Milliseconds()+350 {
				if match != nil {
					return nil
				}
				match = c
			}
		}
		if match != nil {
			// Multiple model handoffs from one local utterance share a single native input.
			match.turn.DelegationID = e.DelegationID
			match.eventAt = now
		}
	case "session.input_transcript.delta":
		if e.StartMS < 0 || e.EndMS < e.StartMS || !utf8.ValidString(e.Text) {
			return errors.New("invalid call Live transcript cursor")
		}
		for i := len(choices) - 1; i >= 0; i-- {
			c := choices[i]
			if c.start-100 <= e.StartMS && c.end+quietTime.Milliseconds() >= e.EndMS && e.EndMS >= c.start {
				if c.text.Len()+len(e.Text) > maxTurnText {
					c.invalid = true
				} else if e.Text != "" {
					if c.text.Len() == 0 {
						c.transcriptStart = e.StartMS
					}
					c.text.WriteString(e.Text)
					c.transcriptEnd = max(c.transcriptEnd, e.EndMS)
					c.eventAt = now
				}
				return nil
			}
		}
	}
	return nil
}

func (t *transcript) settle(l *Lane, now time.Time) {
	for len(t.pending) > 0 {
		c := t.pending[0]
		quiet := now.Sub(c.last)
		if quiet < tailTime || (now.Sub(c.eventAt) < stableTime && quiet < hardTail) {
			return
		}
		covered := c.turn.Text != "" || (c.text.Len() > 0 && c.transcriptStart <= c.start+240 && c.transcriptEnd >= c.end-240)
		if (!covered || c.turn.DelegationID == "") && quiet < hardTail && !c.invalid {
			return
		}
		t.pending[0] = nil
		t.pending = t.pending[1:]
		t.retiredUntil = max(t.retiredUntil, c.end+quietTime.Milliseconds()+351)
		if c.invalid {
			continue
		}
		if c.turn.DelegationID != "" && covered && now.Sub(c.eventAt) >= stableTime {
			if c.turn.Text == "" {
				c.turn.Text = strings.TrimSpace(c.text.String())
			}
			if c.turn.Text == "" {
				continue
			}
			c.turn.ID = c.turn.DelegationID
			l.mu.Lock()
			l.delegations[c.turn.DelegationID] = struct{}{}
			l.mu.Unlock()
			l.enqueue(utterance{turn: c.turn})
			continue
		}
		// A provider can omit delegation even for a clear request. This turn
		// receives a local ID and never writes commentary to a guessed handoff.
		if c.turn.Text != "" || c.voicedFrames >= 8 {
			c.turn.DelegationID = ""
			c.turn.ID = rand.Text()
			l.enqueue(utterance{turn: c.turn, pcm: c.pcm})
		}
	}
}

func addressedText(text string) string {
	text = strings.TrimSpace(text)
	const address = "dispatcher"
	if len(text) <= len(address) || !strings.EqualFold(text[:len(address)], address) {
		return ""
	}
	remainder := strings.TrimSpace(text[len(address):])
	r, size := utf8.DecodeRuneInString(remainder)
	const separators = ",.:!?;—–"
	// shortcut: require vocative punctuation from isolated STT, calibrate on hosted team speech before broadening the wake phrase.
	if !strings.ContainsRune(separators, r) {
		return ""
	}
	return strings.TrimLeftFunc(remainder[size:], func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune(separators, r)
	})
}

func hasSignal(pcm []byte) bool {
	count := 0
	for i := 0; i < len(pcm); i += 2 {
		s := int16(binary.LittleEndian.Uint16(pcm[i:]))
		if s > 420 || s < -420 {
			count++
		}
	}
	return count >= 24
}

func localCue() []byte {
	const samples = 2400
	pcm := make([]byte, samples*2)
	for i := range samples {
		envelope := math.Sin(math.Pi * float64(i) / samples)
		sample := int16(2700 * envelope * math.Sin(2*math.Pi*660*float64(i)/24000))
		binary.LittleEndian.PutUint16(pcm[2*i:], uint16(sample))
	}
	return pcm
}
