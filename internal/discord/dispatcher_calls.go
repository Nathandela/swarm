package discord

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/voice"
	"github.com/Nathandela/swarm/internal/voice/callspeech"
	"github.com/bwmarrin/discordgo"
)

const callIdleTimeout = 2 * time.Minute

// Gateway callbacks only change in-memory authority and enqueue bounded work.
type dispatcherCalls struct {
	lastMetrics     dispatcherCallMetrics
	dependencies    dispatcherCallDependencies
	checkStartup    bool
	lastControl     map[string]string
	lastLeave       string
	statusReason    string
	t               *teammateConnector
	ctx             context.Context
	mu              sync.Mutex
	roster          callRoster
	active          *dispatcherCall
	leaving         bool
	rearmOnLeave    bool
	generation      uint64
	consent         map[string]time.Time
	invitedUntil    time.Time
	rearmAfter      time.Time
	suspended       bool
	stopped         bool
	wake            chan struct{}
	historyRepaired chan struct{}
	wg              sync.WaitGroup
}

type dispatcherCall struct {
	lastTurnID   string
	lastTurnEnd  time.Time
	metrics      dispatcherCallMetrics
	started      time.Time
	failure      string
	ctx          context.Context
	cancel       context.CancelFunc
	id           string
	generation   uint64
	speakers     []string
	input        chan any
	turns        chan callspeech.Turn
	publications chan TeammatePublication
	bound        TeammateDiscussion
	ready        bool
	lastSpeech   time.Time
	idleTimer    *time.Timer
	joinAt       int64
	occupancyAt  int64
	daveChanged  bool
	retriedDAVE  bool
	mediaFault   bool
	mediaLost    bool
}

func enableDispatcherCalls(ctx context.Context, t *teammateConnector, injected ...dispatcherCallDependencies) *dispatcherCalls {
	dependencies := defaultDispatcherCallDependencies()
	if len(injected) != 0 {
		dependencies = injected[0]
	}
	cfg := t.service.Config.Calls
	d := &dispatcherCalls{dependencies: dependencies, checkStartup: len(injected) == 0, ctx: ctx, t: t, lastControl: make(map[string]string), consent: make(map[string]time.Time), wake: make(chan struct{}, 1), historyRepaired: make(chan struct{}, 1)}
	t.service.calls = d
	t.client.onDispatch = d.gateway
	t.client.callGateway = &callGateway{guild: cfg.GuildID, room: cfg.RoomID, dispatch: d.dispatch, disconnect: d.disconnect, unavailable: func(reason string) {
		d.disconnect()
		d.mu.Lock()
		d.statusReason = reason
		d.mu.Unlock()
	}}
	t.verifyCallTurn = d.verify
	t.observeCallPublication = d.publication
	d.wg.Go(d.run)
	return d
}

func (d *dispatcherCalls) invalidateLocked() {
	if d.active != nil {
		d.active.cancel()
	}
}

func (d *dispatcherCalls) signalLocked() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

func (d *dispatcherCalls) disconnect() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.roster.reset()
	d.leaving, d.rearmOnLeave = false, false
	clear(d.consent)
	d.invitedUntil = time.Time{}
	d.invalidateLocked()
}

func (d *dispatcherCalls) close() {
	d.mu.Lock()
	d.stopped = true
	d.invalidateLocked()
	d.mu.Unlock()
	d.wg.Wait()
}

func (d *dispatcherCalls) dispatch(f gatewayFrame) {
	d.mu.Lock()
	defer d.mu.Unlock()
	cfg := d.t.service.Config
	if f.Type != "READY" && f.Sequence != nil && *f.Sequence <= d.roster.sequence {
		return
	}
	before, wasEligible := d.roster.speakers(cfg)
	changed := d.roster.apply(cfg, f)
	selfLeave := f.Sequence != nil && d.roster.selfLeftAt == *f.Sequence
	freshEmptyBot := f.Type == "GUILD_CREATE" && d.roster.occupants != nil && d.roster.selfChannel == ""
	if d.leaving && (freshEmptyBot || selfLeave) {
		d.leaving = false
		if d.rearmOnLeave {
			d.signalLocked()
		}
		d.rearmOnLeave = false
	}
	if changed {
		d.invalidateLocked()
		after, eligible := d.roster.speakers(cfg)
		if eligible && !selfLeave && (!wasEligible || !slices.Equal(before, after) || f.Type == "GUILD_CREATE" || callPermissionEvent(f.Type)) {
			d.signalLocked()
		}
	}
	c := d.active
	if c == nil || c.ctx.Err() != nil || c.joinAt == 0 {
		return
	}
	if f.Type != "VOICE_STATE_UPDATE" && f.Type != "VOICE_SERVER_UPDATE" {
		return
	}
	var coordinate struct {
		Guild   string `json:"guild_id"`
		User    string `json:"user_id"`
		Channel string `json:"channel_id"`
	}
	if json.Unmarshal(f.Data, &coordinate) != nil || coordinate.Guild != cfg.Calls.GuildID {
		return
	}
	kind := "voice_server"
	if f.Type == "VOICE_STATE_UPDATE" {
		if coordinate.User != cfg.ApplicationID || coordinate.Channel != cfg.Calls.RoomID {
			return
		}
		kind = "voice_state"
	}
	d.queueLocked(c, map[string]any{"type": kind, "data": f.Data})
}

func (d *dispatcherCalls) queueLocked(c *dispatcherCall, v any) bool {
	if d.active != c || c.ctx.Err() != nil {
		return false
	}
	select {
	case c.input <- v:
		return true
	default:
		c.metrics.QueueOverflows++
		c.cancel()
		return false
	}
}

type callCommand struct {
	action, user, id string
	issued           time.Time
}

// Consent commands belong exclusively to the call control surface. Suppress them even on replay.
func callControl(cfg Config, f gatewayFrame) (callCommand, bool) {
	if cfg.Calls == nil || cfg.Mode != "teammate" || f.Type != "MESSAGE_CREATE" {
		return callCommand{}, false
	}
	var msg discordgo.Message
	if json.Unmarshal(f.Data, &msg) != nil || msg.GuildID != cfg.Calls.GuildID || msg.ChannelID != cfg.Calls.DiscussionID {
		return callCommand{}, false
	}
	cmd := strings.TrimSpace(msg.Content)
	for _, prefix := range []string{"<@" + cfg.ApplicationID + "> ", "<@!" + cfg.ApplicationID + "> "} {
		if !strings.HasPrefix(cmd, prefix) {
			continue
		}
		action := strings.TrimPrefix(cmd, prefix)
		if action != "voice join" && action != "voice consent" && action != "voice leave" {
			return callCommand{}, false
		}
		if !snowflake(msg.ID) || msg.Author == nil || msg.Author.Bot || msg.WebhookID != "" || msg.Type != discordgo.MessageTypeDefault || !containsID(cfg.Calls.UserIDs, msg.Author.ID) || msg.Timestamp.IsZero() || time.Since(msg.Timestamp) > 30*time.Second || time.Until(msg.Timestamp) > 2*time.Second {
			return callCommand{}, true
		}
		return callCommand{action: action, user: msg.Author.ID, id: msg.ID, issued: msg.Timestamp}, true
	}
	return callCommand{}, false
}

// A control may arrive after expiry while the timer or the retiring manager is still blocked.
func (d *dispatcherCalls) expireDueLocked(c *dispatcherCall, now time.Time) {
	if c == nil || errors.Is(c.ctx.Err(), context.Canceled) {
		if !now.Before(d.invitedUntil) && d.invitedUntil.After(d.rearmAfter) {
			d.rearmAfter = d.invitedUntil
		}
		return
	}
	cutoff, ok := c.ctx.Deadline()
	if !ok {
		return
	}
	idleAt := c.lastSpeech.Add(callIdleTimeout)
	idle := c.idleTimer != nil && idleAt.Before(cutoff)
	if idle {
		cutoff = idleAt
	}
	if now.Before(cutoff) || !cutoff.After(d.rearmAfter) {
		return
	}
	d.rearmAfter = cutoff
	d.invitedUntil = time.Time{}
	if idle {
		c.failure = "call ended after two minutes without speech"
	}
	c.cancel()
}

func (d *dispatcherCalls) command(cmd callCommand) {
	d.mu.Lock()
	defer d.mu.Unlock()
	previous := d.lastControl[cmd.user]
	if cmd.id == "" || cmd.id == previous || d.lastLeave != "" && !snowflakeLess(d.lastLeave, cmd.id) {
		return
	}
	if previous == "" || snowflakeLess(previous, cmd.id) {
		d.lastControl[cmd.user] = cmd.id
	} else if cmd.action != "voice leave" {
		return
	}
	if cmd.action == "voice leave" {
		d.lastLeave = cmd.id
		d.invitedUntil = time.Time{}
		clear(d.consent)
		d.invalidateLocked()
		return
	}
	now := time.Now()
	d.expireDueLocked(d.active, now)
	if !cmd.issued.After(d.rearmAfter) {
		return
	}
	if cmd.action != "voice join" && cmd.action != "voice consent" {
		return
	}
	speakers, eligible := d.roster.speakers(d.t.service.Config)
	if !eligible || !slices.Contains(speakers, cmd.user) {
		return
	}
	grantedAt := cmd.issued
	if grantedAt.After(now) {
		grantedAt = now
	}
	d.consent[cmd.user] = grantedAt.Add(30 * time.Minute)
	if d.active != nil && d.active.ctx.Err() == nil {
		return
	}
	if cmd.action == "voice join" {
		d.invitedUntil = d.consent[cmd.user]
	}
	d.signalLocked()
}

func (d *dispatcherCalls) gateway(f gatewayFrame, session string) error {
	if cmd, reserved := callControl(d.t.service.Config, f); reserved {
		if !f.replayed {
			d.command(cmd)
		}
		return nil
	}
	return d.t.gateway(f, session)
}

func (d *dispatcherCalls) permittedLocked() ([]string, bool) {
	cfg := d.t.service.Config
	speakers, ok := d.roster.speakers(cfg)
	if !ok || len(speakers) == 0 || d.stopped || d.suspended || d.ctx.Err() != nil {
		return nil, false
	}
	if cfg.Mode == "teammate" {
		if !time.Now().Before(d.invitedUntil) {
			return nil, false
		}
		for _, id := range speakers {
			if !time.Now().Before(d.consent[id]) {
				return nil, false
			}
		}
	}
	return speakers, true
}

func (d *dispatcherCalls) run() {
	if d.checkStartup {
		if reason := d.preflight(); reason != "" {
			d.mu.Lock()
			d.statusReason = reason
			d.mu.Unlock()
			return
		}
	}
	var retry *dispatcherCall
	for {
		select {
		case <-d.ctx.Done():
			return
		case <-d.wake:
		}
		d.mu.Lock()
		// All triggers already received refer to the roster evaluated below.
		select {
		case <-d.wake:
		default:
		}
		ids, ok := d.permittedLocked()
		if !ok || d.active != nil {
			retry = nil
			d.mu.Unlock()
			continue
		}
		gatewayCtx := d.t.client.callVoiceContext(d.ctx)
		if retry != nil && retry.ctx.Value(callGatewaySessionKey{}) != gatewayCtx.Value(callGatewaySessionKey{}) {
			// A fresh Gateway anchor is an independent authorization trigger.
			retry = nil
		}
		if retry != nil && (retry.occupancyAt != d.roster.occupancyAt || !slices.Equal(retry.speakers, ids)) {
			retry = nil
			d.mu.Unlock()
			continue
		}
		if d.leaving {
			d.rearmOnLeave = true
			d.mu.Unlock()
			continue
		}
		deadline := time.Now().Add(30 * time.Minute)
		if d.t.service.Config.Mode == "teammate" {
			deadline = d.invitedUntil
			for _, user := range ids {
				if d.consent[user].Before(deadline) {
					deadline = d.consent[user]
				}
			}
		}
		if retry != nil {
			previousDeadline, _ := retry.ctx.Deadline()
			if previousDeadline.Before(deadline) {
				deadline = previousDeadline
			}
		}
		if d.t.service.Config.Mode == "teammate" {
			d.invitedUntil = deadline
		}
		mediaCtx, cancel := context.WithDeadline(gatewayCtx, deadline)
		ctx, revoke := context.WithCancel(mediaCtx)
		d.generation++
		c := &dispatcherCall{ctx: ctx, cancel: cancel, id: rand.Text(), generation: d.generation, speakers: slices.Clone(ids), input: make(chan any, 24), turns: make(chan callspeech.Turn, 8), publications: make(chan TeammatePublication, 16), lastSpeech: time.Now()}
		c.started = time.Now()
		c.metrics.CallID = c.id
		c.occupancyAt, c.retriedDAVE = d.roster.occupancyAt, retry != nil
		retry = nil
		d.active = c
		d.mu.Unlock()
		err := d.serve(c, mediaCtx, revoke)
		cancel()
		d.mu.Lock()
		d.expireDueLocked(c, time.Now())
		// Wait for the bot's leave dispatch before a replacement helper can join.
		left := d.roster.selfLeftAt > c.joinAt
		bound, _ := c.ctx.Value(callGatewaySessionKey{}).(*callGatewaySession)
		current, _ := d.t.client.callVoiceContext(d.ctx).Value(callGatewaySessionKey{}).(*callGatewaySession)
		leave := c.joinAt != 0 && !left && bound != nil && bound == current
		d.leaving = leave
		c.metrics.DurationMS = time.Since(c.started).Milliseconds()
		d.lastMetrics = c.metrics
		d.active = nil
		d.statusReason = ""
		if c.mediaFault || err != nil && !errors.Is(err, context.Canceled) {
			d.statusReason = "call ended before completion; inspect the voice room and text discussion"
		}
		if c.failure != "" {
			d.statusReason = c.failure
		}
		if c.daveChanged && !c.retriedDAVE && c.occupancyAt == d.roster.occupancyAt {
			retry = c
			d.signalLocked()
		}
		d.mu.Unlock()
		if leave {
			_ = d.t.client.setCallVoiceState(context.WithoutCancel(c.ctx), d.t.service.Config.Calls.GuildID, "")
		}
	}
}

func (d *dispatcherCalls) readyLocked(c *dispatcherCall, now time.Time) bool {
	if c == nil || c != d.active || !c.ready {
		return false
	}
	d.expireDueLocked(c, now)
	return c.ctx.Err() == nil
}

func (d *dispatcherCalls) checkReady(c *dispatcherCall) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.readyLocked(c, time.Now()) {
		return nil
	}
	if err := c.ctx.Err(); err != nil {
		return err
	}
	return ErrNotAuthorized
}

func (d *dispatcherCalls) verify(ctx context.Context, b TeammateDiscussion, o schema.VoiceOrigin, live bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !live {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	c := d.active
	if _, eligible := d.permittedLocked(); !eligible || !d.readyLocked(c, time.Now()) || c.bound.ID != b.ID || c.id != o.CallID || c.generation != o.Generation || !slices.Contains(c.speakers, o.SpeakerID) {
		return ErrNotAuthorized
	}
	return nil
}

func (d *dispatcherCalls) publication(b TeammateDiscussion, p TeammatePublication) {
	d.mu.Lock()
	defer d.mu.Unlock()
	c := d.active
	if !d.readyLocked(c, time.Now()) || c.bound.ID != b.ID {
		return
	}
	select {
	case c.publications <- p:
	default:
		c.cancel()
	}
}

func (d *dispatcherCalls) checkAudience(ctx context.Context, b TeammateDiscussion, ids []string) error {
	for _, id := range ids {
		if err := d.t.checkCallSpeaker(ctx, b, id); err != nil {
			return err
		}
	}
	return nil
}

func (d *dispatcherCalls) serve(c *dispatcherCall, mediaCtx context.Context, revoke context.CancelFunc) (serveErr error) {
	fail := func(reason string) {
		d.mu.Lock()
		if c.failure == "" {
			c.failure = reason
		}
		d.mu.Unlock()
	}
	cfg := d.t.service.Config
	originGuild := cfg.Calls.GuildID
	if cfg.Mode == "personal" {
		originGuild = ""
	}
	b, err := d.t.callDiscussion(c.ctx, originGuild, cfg.Calls.DiscussionID)
	if err != nil {
		fail("configured dispatcher discussion unavailable; open it by text first")
		return err
	}
	for {
		err = d.checkAudience(c.ctx, b, c.speakers)
		if !errors.Is(err, errPersonalHistoryRepair) {
			break
		}
		// Fresh Discord occupancy can precede completion of owner DM history repair.
		select {
		case <-c.ctx.Done():
			return c.ctx.Err()
		case <-d.historyRepaired:
		}
	}
	if err != nil {
		fail("participant no longer has access to the linked discussion")
		return err
	}
	key, err := voice.LoadOpenAIKey(cfg.Calls.SpeechStateDir)
	if err != nil {
		fail("speech credential unavailable; check the configured speech directory")
		return err
	}
	d.mu.Lock()
	if err = c.ctx.Err(); err != nil {
		d.mu.Unlock()
		return err
	}
	c.bound = b
	d.mu.Unlock()
	play := func(ctx context.Context, pcm []byte) error {
		d.mu.Lock()
		defer d.mu.Unlock()
		if ctx.Err() != nil || !d.readyLocked(c, time.Now()) || !d.queueLocked(c, map[string]any{"type": "play", "pcm": slices.Clone(pcm)}) {
			return ErrNotAuthorized
		}
		return nil
	}
	lane := d.dependencies.newSpeech(c.ctx, callspeech.Config{Key: key, Team: cfg.Mode == "teammate", Play: play, ReadoutFailed: func(_ string) {
		d.mu.Lock()
		defer d.mu.Unlock()
		if c.ctx.Err() == nil {
			c.metrics.WrittenOnly++
		}
	}, PlaybackStarted: func(turnID string) {
		d.mu.Lock()
		defer d.mu.Unlock()
		if c.ctx.Err() == nil && turnID == c.lastTurnID && !c.lastTurnEnd.IsZero() && c.metrics.LastResultFrameMS == 0 {
			c.metrics.LastResultFrameMS = time.Since(c.lastTurnEnd).Milliseconds()
		}
	},
		Stop:   func() { d.mu.Lock(); defer d.mu.Unlock(); d.queueLocked(c, map[string]any{"type": "clear"}) },
		Failed: func(error) { fail("speech provider unavailable; continue in text"); c.cancel() },
		Turn: func(ctx context.Context, turn callspeech.Turn) {
			d.mu.Lock()
			defer d.mu.Unlock()
			if !d.readyLocked(c, time.Now()) {
				return
			}
			select {
			case <-ctx.Done():
			case <-c.ctx.Done():
			case c.turns <- turn:
			default:
				c.cancel()
			}
		},
	})
	mediaDone := make(chan error, 1)
	started := make(chan struct{})
	onLoss := sync.OnceFunc(func() {
		revoke()
		d.mu.Lock()
		c.ready, c.mediaLost = false, true
		d.mu.Unlock()
	})
	go func() {
		mediaErr := d.dependencies.runMedia(mediaCtx, cfg.Calls.HelperPath, cfg.Calls.GuildID, cfg.Calls.RoomID, cfg.ApplicationID, c.speakers, c.input, onLoss, func(e callMediaEvent) error {
			if e.Type == "fault" {
				d.mu.Lock()
				c.mediaFault = true
				select {
				case <-started:
					c.daveChanged = e.Code == "dave_changed" && mediaCtx.Err() == nil && c.joinAt != 0
				default:
				}
				d.mu.Unlock()
				return errors.New("discord call media: helper reported a fault")
			}
			if c.ctx.Err() != nil {
				return c.ctx.Err()
			}
			switch e.Type {
			case "gateway":
				var op struct {
					Op int
					D  struct {
						Guild   string `json:"guild_id"`
						Channel string `json:"channel_id"`
						Mute    bool   `json:"self_mute"`
						Deaf    bool   `json:"self_deaf"`
					}
				}
				if json.Unmarshal(e.Data, &op) != nil || op.Op != 4 || op.D.Guild != cfg.Calls.GuildID || op.D.Channel != cfg.Calls.RoomID || op.D.Mute || op.D.Deaf {
					return ErrNotAuthorized
				}
				d.mu.Lock()
				if c.joinAt != 0 || c.ctx.Err() != nil {
					d.mu.Unlock()
					return ErrNotAuthorized
				}
				c.joinAt = d.roster.sequence
				d.mu.Unlock()
				return d.t.client.setCallVoiceState(c.ctx, cfg.Calls.GuildID, cfg.Calls.RoomID)
			case "ready":
				d.mu.Lock()
				defer d.mu.Unlock()
				if err := c.ctx.Err(); err != nil {
					return err
				}
				select {
				case <-started:
					return ErrConflict
				default:
					close(started)
				}
			case "audio":
				if !slices.Contains(c.speakers, e.UserID) {
					return ErrNotAuthorized
				}
				d.mu.Lock()
				now := time.Now()
				ready := d.readyLocked(c, now)
				if ready {
					if callspeech.HasSignal(e.PCM) {
						c.lastSpeech = now
					}
					c.metrics.InputFrames++
				}
				d.mu.Unlock()
				if !ready {
					return nil
				}
				return lane.Push(e.UserID, e.PCM)
			default:
				return ErrInvalid
			}
			return nil
		})
		onLoss()
		mediaDone <- mediaErr
	}()
	ended := false
	defer func() {
		// Helper loss cancels authority while the media runner drains its terminal fault.
		if c.ctx.Err() == nil {
			c.cancel()
		}
		if !ended {
			if mediaErr := <-mediaDone; mediaErr != nil && errors.Is(serveErr, context.Canceled) {
				serveErr = mediaErr
			}
		}
		c.cancel()
		lane.Close()
		<-lane.Done()
	}()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	providerReady := lane.Ready()
	mediaReady := started
	for providerReady != nil || mediaReady != nil {
		select {
		case <-c.ctx.Done():
			return c.ctx.Err()
		case err = <-mediaDone:
			ended = true
			return err
		case <-timer.C:
			return errors.New("discord: voice join timed out")
		case <-providerReady:
			providerReady = nil
		case <-mediaReady:
			mediaReady = nil
		}
	}
	d.mu.Lock()
	if c.mediaLost || c.ctx.Err() != nil {
		d.mu.Unlock()
		return c.ctx.Err()
	}
	// A blocked native admission cannot keep media authorized past the idle deadline.
	c.idleTimer = time.AfterFunc(callIdleTimeout, func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		if c.ctx.Err() != nil {
			return
		}
		if remaining := time.Until(c.lastSpeech.Add(callIdleTimeout)); remaining > 0 {
			c.idleTimer.Reset(remaining)
			return
		}
		d.expireDueLocked(c, time.Now())
	})
	defer c.idleTimer.Stop()
	c.lastSpeech = time.Now()
	c.ready = true
	c.metrics.ReadyMS = time.Since(c.started).Milliseconds()
	d.mu.Unlock()
	if err = lane.Acknowledge(); err != nil {
		return err
	}
	monitor := time.NewTicker(15 * time.Second)
	defer monitor.Stop()
	seen := map[string]bool{}
	delegations := map[string]string{}
	for {
		select {
		case <-c.ctx.Done():
			return c.ctx.Err()
		case err = <-mediaDone:
			ended = true
			return err
		case <-monitor.C:
			d.mu.Lock()
			ids, ok := d.permittedLocked()
			d.mu.Unlock()
			if !ok || !slices.Equal(ids, c.speakers) {
				return ErrNotAuthorized
			}
			if err = d.checkAudience(c.ctx, b, c.speakers); err != nil {
				fail("participant no longer has access to the linked discussion")
				return err
			}
		case turn := <-c.turns:
			if err = d.checkReady(c); err != nil {
				return err
			}
			if !slices.Contains(c.speakers, turn.Speaker) || turn.ID == "" {
				return ErrInvalid
			}
			turnID := digest([]string{c.id, turn.ID})
			if _, ok := delegations[turnID]; ok {
				continue
			}
			if len(delegations) >= 256 {
				return ErrConflict
			}
			delegations[turnID] = turn.ID
			if _, err = d.t.submitCallTurn(c.ctx, b, c.id, turnID, c.generation, Actor{ID: turn.Speaker, Name: turn.Speaker}, turn.Text); err != nil {
				return err
			}
			d.mu.Lock()
			c.metrics.AcceptedTurns++
			c.lastTurnID, c.lastTurnEnd = turn.ID, turn.LastVoicedAt
			c.metrics.LastAdmissionMS, c.metrics.LastResultFrameMS = 0, 0
			if !turn.LastVoicedAt.IsZero() {
				c.metrics.LastAdmissionMS = time.Since(turn.LastVoicedAt).Milliseconds()
			}
			d.mu.Unlock()
			if err = lane.Acknowledge(); err != nil {
				return err
			}
		case p := <-c.publications:
			if err = d.checkReady(c); err != nil {
				return err
			}
			o, err := d.t.callPublicationOrigin(b, p)
			if _, known := delegations[o.TurnID]; err != nil || o.CallID != c.id || o.Generation != c.generation || !known || seen[p.ID] {
				continue
			}
			if err = d.checkAudience(c.ctx, b, c.speakers); err != nil {
				fail("participant no longer has access to the linked discussion")
				return err
			}
			if err = d.checkReady(c); err != nil {
				return err
			}
			if len(seen) >= 1024 {
				return ErrConflict
			}
			d.mu.Lock()
			c.metrics.Publications++
			d.mu.Unlock()
			seen[p.ID] = true
			if err = lane.PublishForTurn(delegations[o.TurnID], p.Body); err != nil {
				return err
			}
		}
	}
}

func (d *dispatcherCalls) status() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.suspended {
		return "calls suspended until connector restart"
	}
	if c := d.active; c != nil && c.ctx.Err() == nil {
		if c.ready {
			return "active"
		}
		return "connecting"
	}
	if d.statusReason != "" {
		return d.statusReason
	}
	if d.roster.occupants == nil {
		return "waiting for fresh Discord guild roster; restart the connector to request one"
	}
	return "waiting for authorized room and participants"
}

// Durations are host-observed. Discord playback and human hearing need a device measurement.
type dispatcherCallMetrics struct {
	WrittenOnly       uint64 `json:"written_only_readouts"`
	LastAdmissionMS   int64  `json:"last_turn_end_to_admission_ms,omitempty"`
	LastResultFrameMS int64  `json:"last_turn_end_to_result_frame_ms,omitempty"`
	CallID            string `json:"call_id"`
	ReadyMS           int64  `json:"ready_ms"`
	DurationMS        int64  `json:"duration_ms"`
	InputFrames       uint64 `json:"input_frames"`
	AcceptedTurns     uint64 `json:"accepted_turns"`
	Publications      uint64 `json:"verified_publications"`
	QueueOverflows    uint64 `json:"queue_overflows"`
}

func (d *dispatcherCalls) metricsSnapshot() *dispatcherCallMetrics {
	d.mu.Lock()
	defer d.mu.Unlock()
	result := d.lastMetrics
	if c := d.active; c != nil {
		result = c.metrics
		result.DurationMS = time.Since(c.started).Milliseconds()
	}
	if result.CallID == "" {
		return nil
	}
	return &result
}
