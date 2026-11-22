package discord

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	bolt "go.etcd.io/bbolt"
)

type teammateConnector struct {
	service   *Service
	client    *DiscordClient
	manager   TeammateManager
	cursorKey []byte
	wake      chan struct{}
}

func (t *teammateConnector) signal() {
	select {
	case t.wake <- struct{}{}:
	default:
	}
}

func (t *teammateConnector) gateway(f gatewayFrame, session string) error {
	cfg := t.service.Config
	var sequence int64
	if f.Sequence != nil {
		sequence = *f.Sequence
	}
	if f.Type == "READY" {
		var ready discordgo.Ready
		if json.Unmarshal(f.Data, &ready) != nil || ready.Application == nil || ready.Application.ID != cfg.ApplicationID || ready.User == nil || !snowflake(ready.User.ID) || cfg.Mode == "personal" && ready.User.ID != cfg.ApplicationID {
			return errors.New("discord: bot token does not match configured application")
		}
		t.service.SetBotID(ready.User.ID)
		err := t.service.Store.db.Update(func(tx *bolt.Tx) error {
			return teammateCheckpointUpdate(tx, cfg.InstallationID, session, sequence, true, "")
		})
		t.signal()
		return err
	}
	if f.Type != "MESSAGE_CREATE" && f.Type != "MESSAGE_UPDATE" && f.Type != "MESSAGE_DELETE" && f.Type != "MESSAGE_DELETE_BULK" {
		return nil
	}
	var envelope struct {
		ID        string
		IDs       []string
		GuildID   string `json:"guild_id"`
		ChannelID string `json:"channel_id"`
	}
	if json.Unmarshal(f.Data, &envelope) != nil || !snowflake(envelope.ChannelID) ||
		envelope.GuildID != "" && !containsID(cfg.GuildIDs, envelope.GuildID) ||
		cfg.Mode != "personal" && f.Type == "MESSAGE_CREATE" && envelope.GuildID == "" {
		return nil
	}
	if cfg.Mode == "personal" && (envelope.GuildID != "" || envelope.ChannelID != cfg.DMChannelID) {
		return nil
	}
	route := Route{GuildID: envelope.GuildID, ChannelID: envelope.ChannelID}
	var records []teammateCandidate
	switch f.Type {
	case "MESSAGE_CREATE":
		var m discordgo.Message
		if json.Unmarshal(f.Data, &m) != nil {
			return ErrInvalid
		}
		if m.Author != nil && m.Author.ID == t.service.botUserID() && m.WebhookID == "" {
			if nonce := messageNonce(f.Data); nonce != "" && snowflake(m.ID) {
				return t.service.Store.teammateObserve(cfg.InstallationID, route, m.ID, nonce, m.Content, t.service.Now())
			}
		}
		if !t.admissibleHuman(&m) || !snowflake(m.ID) {
			return nil
		}
		route.MessageID = m.ID
		opening := t.addressed(&m)
		if !opening {
			relevant, err := t.service.Store.teammateInterested(cfg.InstallationID, route.ChannelID)
			if err != nil {
				return err
			}
			if !relevant {
				return nil
			}
		}
		records = append(records, teammateCandidate{Route: route, Author: Actor{ID: m.Author.ID, Name: m.Author.Username}, Opening: opening})
	case "MESSAGE_DELETE_BULK":
		for _, id := range envelope.IDs {
			if !snowflake(id) {
				return ErrInvalid
			}
			route.MessageID = id
			records = append(records, teammateCandidate{Route: route})
		}
	default:
		if !snowflake(envelope.ID) {
			return ErrInvalid
		}
		route.MessageID = envelope.ID
		records = append(records, teammateCandidate{Route: route, Edited: f.Type == "MESSAGE_UPDATE"})
	}
	err := t.service.Store.teammateRecord(cfg.InstallationID, session, sequence, records, f.Type == "MESSAGE_DELETE" || f.Type == "MESSAGE_DELETE_BULK")
	if err != nil {
		_ = t.service.Store.db.Update(func(tx *bolt.Tx) error {
			return teammateCheckpointUpdate(tx, cfg.InstallationID, "", 0, false, "Discord ingress paused: "+err.Error())
		})
		return err
	}
	t.signal()
	return nil
}

func (t *teammateConnector) resolve(ctx context.Context, c teammateCandidate) (teammateBinding, *discordgo.Message, error) {
	cfg := t.service.Config
	botID := t.service.botUserID()
	channel, parent, bits, err := teammateChannelScope(ctx, t.client, cfg, c.Route, botID)
	if err != nil {
		return teammateBinding{}, nil, err
	}
	if channel.Type == discordgo.ChannelTypeGuildText {
		// An untagged parent-channel message can race classification of an earlier opener.
		err = t.service.Store.db.Update(func(tx *bolt.Tx) error {
			return tx.Bucket([]byte(teammateMetaBucket)).Put(teammateKey(cfg.InstallationID, "parent:"+channel.ID), []byte{1})
		})
		if err != nil {
			return teammateBinding{}, nil, err
		}
		if !c.Opening {
			return teammateBinding{}, nil, ErrNotAuthorized
		}
	}
	personal := cfg.Mode == "personal"
	if !personal && !channel.IsThread() && channel.Type != discordgo.ChannelTypeGuildText {
		return teammateBinding{}, nil, errTeammateUnsupported
	}
	if channel.IsThread() && parent.Type != discordgo.ChannelTypeGuildText && parent.Type != discordgo.ChannelTypeGuildNews && parent.Type != discordgo.ChannelTypeGuildForum {
		return teammateBinding{}, nil, errTeammateUnsupported
	}
	if bits&discordgo.PermissionReadMessageHistory == 0 {
		return teammateBinding{}, nil, errors.New("discord: teammate needs permission to read message history")
	}
	m, err := t.client.teammateMessage(ctx, c.Route)
	if err != nil {
		return teammateBinding{}, nil, err
	}
	if !t.admissibleHuman(m) || m.Author.ID != c.Author.ID {
		return teammateBinding{}, nil, ErrConflict
	}
	route := Route{GuildID: c.Route.GuildID, ChannelID: channel.ID, ParentID: channel.ParentID}
	var binding teammateBinding
	if channel.IsThread() || personal {
		err = t.service.Store.db.View(func(tx *bolt.Tx) error {
			return teammateGet(tx.Bucket([]byte(teammateDiscussionBucket)), teammateKey(cfg.InstallationID, channel.ID), &binding)
		})
		if err == nil {
			if binding.Route != route {
				return binding, nil, ErrConflict
			}
			if !personal && (channel.ThreadMetadata.Locked || bits&discordgo.PermissionSendMessagesInThreads == 0) {
				return binding, nil, ErrNotAuthorized
			}
			if !binding.Opened && (!c.Opening || !t.addressed(m)) {
				return binding, nil, ErrConflict
			}
			return binding, m, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return binding, nil, err
		}
	}
	if !c.Opening || !t.addressed(m) {
		return binding, nil, ErrConflict
	}
	if !personal && bits&discordgo.PermissionSendMessagesInThreads == 0 {
		return binding, nil, ErrNotAuthorized
	}
	starter := Route{GuildID: c.Route.GuildID, ChannelID: parent.ID, MessageID: channel.ID}
	anchor := ""
	if personal {
		starter = Route{}
	} else if channel.Type == discordgo.ChannelTypeGuildText {
		route, err = t.client.teammateThread(ctx, c.Route, clipUTF16(cfg.Name+" discussion", 95), bits&discordgo.PermissionCreatePublicThreads != 0)
		if err != nil {
			return binding, nil, err
		}
		channel, _, bits, err = teammateChannelScope(ctx, t.client, cfg, route, botID)
		if err != nil {
			return binding, nil, err
		}
		starter, anchor = c.Route, c.Route.MessageID
	} else {
		if parent.Type == discordgo.ChannelTypeGuildForum {
			starter.ChannelID = channel.ID
		}
		if _, e := t.client.teammateMessage(ctx, starter); teammateAPIStatus(e, http.StatusNotFound) {
			starter = Route{}
		} else if e != nil {
			return binding, nil, e
		}
	}
	if !personal && (channel.ThreadMetadata.Locked || bits&discordgo.PermissionSendMessagesInThreads == 0) {
		return binding, nil, ErrNotAuthorized
	}
	p, err := t.service.Store.teammateCheckpoint(cfg.InstallationID)
	if err != nil {
		return binding, nil, err
	}
	binding = teammateBinding{TeammateDiscussion: TeammateDiscussion{SupervisorSocket: cfg.DaemonSocket,
		ID:             "dt_" + digest([]string{cfg.InstallationID, route.GuildID, route.ChannelID})[:32],
		InstallationID: cfg.InstallationID, Route: route, Starter: starter, ParentAnchor: anchor,
	}, RepairEpoch: p.GapEpoch, NotifiedEpoch: p.GapEpoch}
	if personal {
		binding.PersonalConsentID = m.ID
		binding.PersonalRepairEpoch = p.GapEpoch
	}
	// For a newly bound thread, the invoking message forms the first context boundary.
	if route.ChannelID == c.Route.ChannelID {
		binding.Boundary = m.ID
	}
	if err = t.service.Store.teammateBind(binding); err != nil {
		return binding, nil, err
	}
	// A replayed parent opener may rediscover an already bound thread after cutover.
	// The durable winner owns this admission, including a historic unmarked binding.
	binding, err = t.service.Store.teammateBindingByID(cfg.InstallationID, binding.ID)
	return binding, m, err
}

func (t *teammateConnector) admissibleHuman(m *discordgo.Message) bool {
	cfg := t.service.Config
	return teammateHuman(m, t.service.botUserID()) && (cfg.Mode != "personal" || m.Author.ID == cfg.OwnerID)
}

func (t *teammateConnector) addressed(m *discordgo.Message) bool {
	return t.service.Config.Mode == "personal" || teammateMention(m, t.service.botUserID())
}

func (t *teammateConnector) expectsReply(m *discordgo.Message) bool {
	return t.addressed(m) || m.ReferencedMessage != nil && m.ReferencedMessage.Author != nil && m.ReferencedMessage.Author.ID == t.service.botUserID()
}

func teammateRevision(m *discordgo.Message) string {
	return digest([]any{m.Author.ID, m.Content, m.ChannelID, m.GuildID, m.ID, m.EditedTimestamp, teammateReply(m), teammateEvidence(m).Attachments})
}

func (t *teammateConnector) process(ctx context.Context, c teammateCandidate) error {
	cfg := t.service.Config
	store := t.service.Store
	var a TeammateAdmission
	var noticeErr error
	if c.State == StateSending && c.Admission != nil {
		a = *c.Admission
		state, err := t.manager.AdmissionState(ctx, a)
		if err != nil {
			return err
		}
		switch state {
		case TeammateAdmissionMissing:
			if err = t.checkDiscussion(ctx, a.Discussion); err != nil {
				return err
			}
			current, err := t.readAdmissionSource(ctx, c)
			if errors.Is(err, ErrTeammateSourceChanged) {
				if a.Supersedes != "" {
					a.Revision = a.Supersedes
					if err = t.manager.CancelAdmission(ctx, a); err != nil {
						return err
					}
				}
				return store.teammateCandidateChange(cfg.InstallationID, c, StateFailed, ErrTeammateSourceChanged.Error(), nil)
			}
			if err != nil {
				return err
			}
			if teammateRevision(current) != a.Revision {
				if a.Supersedes != "" {
					return t.submitRevision(ctx, c, a, current)
				}
				return store.teammateCandidateChange(cfg.InstallationID, c, StatePrepared, "source changed before native submission", nil)
			}
			a.Text = current.Content
			previousManifest := a.AttachmentManifest
			a.AttachmentManifest, err = t.captureAttachments(ctx, a, current)
			if err != nil {
				return err
			}
			if previousManifest != a.AttachmentManifest {
				if err = store.teammateCandidateChange(cfg.InstallationID, c, StateSending, "", &a); err != nil {
					return err
				}
			}
			noticeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			noticeErr = t.sendStartupNotice(noticeCtx, a.Discussion, "")
			cancel()
		case TeammateAdmissionSuperseded:
			return t.refreshAdmission(ctx, c)
		default:
			return t.saveAdmissionPhase(c, state)
		}
	} else {
		b, m, err := t.resolve(ctx, c)
		if err != nil {
			if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotAuthorized) || errors.Is(err, errTeammateUnsupported) || teammateAPIStatus(err, http.StatusForbidden) || teammateAPIStatus(err, http.StatusNotFound) {
				if c.Opening && !errors.Is(err, ErrConflict) && !teammateAPIStatus(err, http.StatusNotFound) {
					if e := t.explainOpening(ctx, c.Route); e != nil {
						return e
					}
				}
				return store.teammateCandidateChange(cfg.InstallationID, c, StateFailed, err.Error(), nil)
			}
			return err
		}
		// Read again after routing so edits and deletes during thread creation cannot enter a native turn.
		current, err := t.client.teammateMessage(ctx, c.Route)
		if teammateAPIStatus(err, http.StatusNotFound) {
			return store.teammateCandidateChange(cfg.InstallationID, c, StateFailed, "source deleted before submission", nil)
		}
		if err != nil {
			return err
		}
		if !t.admissibleHuman(current) || current.Author.ID != m.Author.ID || !b.Opened && (!c.Opening || !t.addressed(current)) {
			return store.teammateCandidateChange(cfg.InstallationID, c, StateFailed, "verified opening message changed", nil)
		}
		p, err := store.teammateCheckpoint(cfg.InstallationID)
		if err != nil {
			return err
		}
		if cfg.Mode != "personal" && b.Opened && p.GapEpoch > b.NotifiedEpoch {
			b.Gap = p.GapEpoch
			if err = t.queueGap(b.TeammateDiscussion); err != nil {
				return err
			}
			if err = store.teammateChangeBinding(b.TeammateDiscussion, func(binding *teammateBinding) { binding.NotifiedEpoch = p.GapEpoch; binding.Gap = p.GapEpoch }); err != nil {
				return err
			}
		}
		authority := AuthorityExternal
		if current.Author.ID == cfg.OwnerID {
			authority = AuthorityOwnerDirect
		}
		a = TeammateAdmission{RequireMention: !b.Opened && cfg.Mode != "personal", Discussion: b.TeammateDiscussion, OperationID: "da_" + digest([]string{cfg.InstallationID, current.ID})[:32],
			Route: c.Route, Author: Actor{ID: current.Author.ID, Name: current.Author.Username},
			InvokerID: current.Author.ID, Authority: authority, Sequence: c.Sequence,
		}
		return t.submitRevision(ctx, c, a, current)
	}
	return errors.Join(noticeErr, t.dispatchAdmission(ctx, c, a))
}

func (t *teammateConnector) submitRevision(ctx context.Context, c teammateCandidate, a TeammateAdmission, m *discordgo.Message) error {
	if c.State == teammateStateAccepted {
		a.Supersedes = a.Revision
	}
	a.ExpectReply = t.expectsReply(m)
	a.Revision, a.Text, a.ReplyTo = teammateRevision(m), m.Content, teammateReply(m)
	var captureErr error
	a.AttachmentManifest, captureErr = t.captureAttachments(ctx, a, m)
	if captureErr != nil {
		return captureErr
	}
	a.QuestionID, a.QuestionHash = "", ""
	if a.ReplyTo != "" {
		var err error
		a.QuestionID, a.QuestionHash, err = t.service.Store.teammateQuestion(a.Discussion, a.ReplyTo)
		if err != nil {
			return err
		}
	}
	if err := t.service.Store.teammateCandidateChange(a.Discussion.InstallationID, c, StateSending, "", &a); err != nil {
		return err
	}
	c.State, c.Admission = StateSending, &a
	return t.dispatchAdmission(ctx, c, a)
}

func (t *teammateConnector) dispatchAdmission(ctx context.Context, c teammateCandidate, a TeammateAdmission) error {
	anchor := ""
	if a.Route.ChannelID == a.Discussion.Route.ChannelID {
		anchor = a.Route.MessageID
	}
	if err := t.manager.Admit(ctx, a, t.history(a.Discussion, anchor)); err != nil {
		return t.managerStartupError(ctx, a.Discussion, err)
	}
	phase, err := t.manager.AdmissionState(ctx, a)
	if err != nil {
		return err
	}
	return t.saveAdmissionPhase(c, phase)
}

func (t *teammateConnector) saveAdmissionPhase(c teammateCandidate, phase string) error {
	store, install := t.service.Store, t.service.Config.InstallationID
	switch phase {
	case TeammateAdmissionCutover:
		return store.teammateCandidateChange(install, c, StateSent, "", nil)
	case TeammateAdmissionCancelled:
		return store.teammateCandidateChange(install, c, StateFailed, "source cancelled before native submission", nil)
	case TeammateAdmissionAccepted, TeammateAdmissionSuperseded:
		if c.State == teammateStateAccepted {
			return nil
		}
		return store.teammateCandidateChange(install, c, teammateStateAccepted, "", nil)
	}
	return ErrConflict
}

func (t *teammateConnector) reconcileAccepted(ctx context.Context, c teammateCandidate) error {
	if c.Admission == nil || c.State != teammateStateAccepted {
		return ErrInvalid
	}
	state, err := t.manager.AdmissionState(ctx, *c.Admission)
	if err != nil {
		return err
	}
	if state == TeammateAdmissionSuperseded {
		return t.refreshAdmission(ctx, c)
	}
	return t.saveAdmissionPhase(c, state)
}

func (t *teammateConnector) refreshAdmission(ctx context.Context, c teammateCandidate) error {
	a := *c.Admission
	a.Supersedes = a.Revision
	m, err := t.readAdmissionSource(ctx, c)
	if errors.Is(err, ErrTeammateSourceChanged) {
		if err = t.manager.CancelAdmission(ctx, a); err != nil {
			return err
		}
		return t.saveAdmissionPhase(c, TeammateAdmissionCancelled)
	}
	if err != nil {
		return err
	}
	if err = t.checkDiscussion(ctx, a.Discussion); err != nil {
		return err
	}
	return t.submitRevision(ctx, c, a, m)
}

func (t *teammateConnector) readAdmissionSource(ctx context.Context, c teammateCandidate) (*discordgo.Message, error) {
	if c.Deleted {
		return nil, ErrTeammateSourceChanged
	}
	a := c.Admission
	m, err := t.client.teammateMessage(ctx, a.Route)
	if teammateAPIStatus(err, http.StatusNotFound) || errors.Is(err, ErrConflict) {
		return nil, ErrTeammateSourceChanged
	}
	if err != nil {
		return nil, err
	}
	if !t.admissibleHuman(m) || m.Author.ID != a.Author.ID || a.RequireMention && !t.addressed(m) {
		return nil, ErrTeammateSourceChanged
	}
	return m, nil
}

type teammateTaskResult struct {
	id, reservation string
	retry, sync     bool
}

func (t *teammateConnector) run(ctx context.Context) error {
	const parallel = 4
	active := make(map[string]bool)
	reserved := make(map[string]bool)
	retryAfter := make(map[string]time.Time)
	syncAfter := make(map[string]time.Time)
	done := make(chan teammateTaskResult, parallel)
	var tasks sync.WaitGroup
	defer tasks.Wait()
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		if t.service.botUserID() != "" && len(active) < parallel {
			if err := t.schedule(ctx, parallel, active, reserved, retryAfter, syncAfter, done, &tasks); err != nil {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case result := <-done:
			delete(active, result.id)
			delete(reserved, result.reservation)
			if result.sync {
				syncAfter[result.id] = time.Now().Add(2 * time.Second)
			}
			if !result.sync && result.retry {
				retryAfter[result.id] = time.Now().Add(3 * time.Second)
			}
			if !result.sync && !result.retry {
				delete(retryAfter, result.id)
			}
		case <-t.wake:
		case <-tick.C:
		}
	}
}

func (t *teammateConnector) schedule(ctx context.Context, parallel int, active, reserved map[string]bool, retryAfter, syncAfter map[string]time.Time, done chan<- teammateTaskResult, tasks *sync.WaitGroup) error {
	store, install := t.service.Store, t.service.Config.InstallationID
	pending, through, err := store.teammatePendingSnapshot(install)
	if err != nil {
		return err
	}
	checkpoint, err := store.teammateCheckpoint(install)
	if err != nil {
		return err
	}
	if err := store.preparePersonalRepair(t.service.Config, checkpoint.GapEpoch); err != nil {
		return err
	}
	if source, ok := t.manager.(interface {
		PendingWorkerDiscussions() (func(TeammateDiscussion) bool, error)
	}); ok {
		matches, readErr := source.PendingWorkerDiscussions()
		issue := ""
		if readErr != nil {
			issue = readErr.Error()
		}
		if checkpoint.WorkerMailIssue != issue {
			if err = store.teammateMailIssue(install, issue); err != nil {
				return err
			}
		}
		if matches != nil {
			if err = store.teammateActivateWorkerMail(install, matches); err != nil {
				return err
			}
		}
	}
	bindings, err := store.teammateActiveBindings(install)
	if err != nil {
		return err
	}
	sort.Slice(bindings, func(i, j int) bool {
		return syncAfter[bindings[i].Route.ChannelID].Before(syncAfter[bindings[j].Route.ChannelID])
	})
	bound := make(map[string]teammateBinding, len(bindings))
	for _, b := range bindings {
		bound[b.Route.ChannelID] = b
	}
	for id := range syncAfter {
		if _, known := bound[id]; !known && id != "notices" {
			delete(syncAfter, id)
		}
	}
	launch := func(id, reservation string, syncing bool, work func(context.Context) error) {
		active[id] = true
		if reservation != "" {
			reserved[reservation] = true
		}
		tasks.Go(func() {
			attempt, cancel := context.WithTimeout(ctx, 90*time.Second)
			err := work(attempt)
			cancel()
			if err != nil && ctx.Err() == nil && !errors.Is(err, ErrConflict) {
				if b, ok := bound[id]; ok {
					_ = store.teammateChangeBinding(b.TeammateDiscussion, func(current *teammateBinding) { current.Error = err.Error() })
				} else {
					_ = store.db.Update(func(tx *bolt.Tx) error { return teammateCheckpointUpdate(tx, install, "", 0, false, err.Error()) })
				}
			}
			select {
			case done <- teammateTaskResult{id, reservation, err != nil, syncing}:
			case <-ctx.Done():
			}
		})
	}
	now := time.Now()
	if t.service.Config.Mode == "personal" {
		// Backward REST pages and live Gateway events can enter custody in either
		// order. Reconcile any already-started admission, then send in source order.
		sort.SliceStable(pending, func(i, j int) bool {
			a, b := pending[i], pending[j]
			if (a.Admission == nil) != (b.Admission == nil) {
				return a.Admission != nil
			}
			return snowflakeLess(a.Route.MessageID, b.Route.MessageID)
		})
	}
	predecessors := make(map[string]bool)
	accepted := make(map[string]teammateCandidate)
	for _, c := range pending {
		id := c.Route.ChannelID
		if c.ParentChannel && c.Opening {
			id = c.Route.MessageID
		}
		if c.Admission != nil {
			id = c.Admission.Discussion.Route.ChannelID
		}
		if predecessors[id] || isPersonalDiscussion(t.service.Config, bound[id].TeammateDiscussion) && personalRepairPending(bound[id], checkpoint.GapEpoch) {
			continue
		}
		// Once a discussion is open, accepted inputs already have FIFO slots in
		// the manager ledger. A later message can join while the first waits.
		if c.State == teammateStateAccepted && bound[id].Opened {
			if _, exists := accepted[id]; !exists {
				accepted[id] = c
			}
			continue
		}
		predecessors[id] = true
		if c.Opening {
			predecessors[c.Route.MessageID] = true
		}
		if c.State == teammateStateAccepted {
			accepted[id] = c
			continue
		}
		if active[id] || reserved[id] || active[c.Route.MessageID] || reserved[c.Route.MessageID] || now.Before(retryAfter[id]) ||
			bound[id].Opened && !now.Before(syncAfter[id]) {
			continue
		}
		reservation := c.Route.MessageID
		if reservation == id {
			reservation = ""
		}
		launch(id, reservation, false, func(ctx context.Context) error { return t.process(ctx, c) })
		if len(active) >= parallel {
			return nil
		}
	}
	for _, b := range bindings {
		if active[b.Route.ChannelID] || reserved[b.Route.ChannelID] || now.Before(syncAfter[b.Route.ChannelID]) {
			continue
		}
		waiting, hasAccepted := accepted[b.Route.ChannelID]
		if !b.Opened && !hasAccepted && !b.ProvisionalCancelled {
			if !predecessors[b.Route.ChannelID] {
				if err := store.teammatePark(b.TeammateDiscussion, through, nil); err != nil {
					return err
				}
			}
			continue
		}
		launch(b.Route.ChannelID, "", true, func(ctx context.Context) error {
			repairErr := t.repair(ctx, b, checkpoint.GapEpoch)
			if isPersonalDiscussion(t.service.Config, b.TeammateDiscussion) && personalRepairPending(b, checkpoint.GapEpoch) {
				// The pending snapshot predates any newly staged history rows.
				return errors.Join(repairErr, t.notifyPersonalHistoryHold(ctx, b, checkpoint.GapEpoch, repairErr))
			}
			if hasAccepted {
				if err := t.reconcileAccepted(ctx, waiting); err != nil {
					return errors.Join(repairErr, err)
				}
			}
			if checkpoint.GapEpoch > b.NotifiedEpoch {
				b.Gap = checkpoint.GapEpoch
			}
			var noticeErr error
			if b.StartupNoticePending && !b.ProvisionalCancelled {
				noticeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				noticeErr = t.sendStartupNotice(noticeCtx, b.TeammateDiscussion, "")
				cancel()
			}
			var emitted []string
			managerErr := t.manager.Sync(ctx, b.TeammateDiscussion, t.history(b.TeammateDiscussion), func(p TeammatePublication) error {
				if err := store.teammateQueueOutput(b.TeammateDiscussion, p, t.service.Now()); err != nil {
					return err
				}
				emitted = append(emitted, p.ID)
				return nil
			})
			if managerErr == nil && b.InputNoticeID != "" {
				noticeErr = errors.Join(noticeErr, store.teammateClearInputNotice(b.TeammateDiscussion, b.InputNoticeID))
			}
			managerErr = t.managerStartupError(ctx, b.TeammateDiscussion, managerErr)
			var ackErr error
			if managerErr == nil {
				ackErr = store.teammateAckOutputs(b.TeammateDiscussion, emitted)
			}
			var acknowledged func(TeammateDiscussion, string, bool) (bool, error)
			if manager, ok := t.manager.(interface {
				PublicationExported(TeammateDiscussion, string, bool) (bool, error)
			}); ok {
				acknowledged = manager.PublicationExported
			}
			compactErr := store.teammateCompactOutbox(b.TeammateDiscussion, t.service.Now(), acknowledged)
			var outputErr error
			if b.Opened {
				outputErr = t.sendPending(ctx, b.TeammateDiscussion)
			}
			if err := errors.Join(repairErr, noticeErr, managerErr, ackErr, compactErr, outputErr); err != nil {
				return err
			}
			// A successful idle sync can interleave with retries of a blocked human message.
			if b.Error != "" && !hasAccepted && !predecessors[b.Route.ChannelID] {
				if err := store.teammateChangeBinding(b.TeammateDiscussion, func(current *teammateBinding) { current.Error = "" }); err != nil {
					return err
				}
			}
			if !hasAccepted && !predecessors[b.Route.ChannelID] {
				if native, ok := t.manager.(interface {
					Dormant(TeammateDiscussion) (bool, error)
				}); ok {
					dormant, err := native.Dormant(b.TeammateDiscussion)
					if err != nil {
						return err
					}
					if dormant {
						var parked *TeammateNativeStatus
						if reporter, ok := t.manager.(interface {
							Status(TeammateDiscussion) (TeammateNativeStatus, error)
						}); ok {
							view, err := reporter.Status(b.TeammateDiscussion)
							if err != nil {
								return err
							}
							parked = &view
						}
						return store.teammatePark(b.TeammateDiscussion, through, parked)
					}
				}
			}
			return nil
		})
		if len(active) >= parallel {
			return nil
		}
	}
	if !active["notices"] && !now.Before(syncAfter["notices"]) {
		launch("notices", "", true, func(ctx context.Context) error {
			pending, err := store.teammateOutputs(install, "")
			if err != nil {
				return err
			}
			var failure error
			for _, out := range pending {
				if e := t.sendOutput(ctx, install, out); e != nil {
					failure = e
				}
			}
			return errors.Join(failure, store.teammateCompactOutbox(TeammateDiscussion{InstallationID: install}, t.service.Now(), nil))
		})
	}
	return nil
}

func (t *teammateConnector) explainOpening(ctx context.Context, route Route) error {
	channel, _, bits, err := teammateChannelScope(ctx, t.client, t.service.Config, route, t.service.botUserID())
	if err != nil || channel.IsThread() || bits&discordgo.PermissionSendMessages == 0 || t.service.Config.Mode == "personal" {
		return nil
	}
	text := "I couldn’t open a public thread here. Mention me in a server text thread or ask a moderator to check my thread and history permissions."
	return t.service.Store.teammateQueueNotice(route, t.service.Config.InstallationID, strings.TrimSpace(text), t.service.Now())
}

func runTeammateConnected(parent context.Context, s *Service, c *DiscordClient, m TeammateManager) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	s.Stop = cancel
	s.teammateManager = m
	key, err := s.Store.teammateCursorKey()
	if err != nil {
		return err
	}
	t := &teammateConnector{service: s, client: c, manager: m, cursorKey: key, wake: make(chan struct{}, 1)}
	c.onDispatch = t.gateway
	if s.Config.Mode == "personal" {
		c.UsePersonalDMs()
		verifyCtx, verifyCancel := context.WithTimeout(ctx, 20*time.Second)
		_, _, _, err = teammateChannelScope(verifyCtx, c, s.Config, Route{ChannelID: s.Config.DMChannelID}, s.Config.ApplicationID)
		verifyCancel()
		if err != nil {
			return err
		}
	}
	failures := make(chan error, 3)
	c.OnError = func(err error) { failures <- err }
	var tasks sync.WaitGroup
	defer func() { cancel(); _ = c.Close(); tasks.Wait() }()
	tasks.Go(func() { failures <- s.ServeLocal(ctx, s.StateDir) })
	if err = waitLocal(ctx, s.StateDir, failures); err != nil {
		return err
	}
	if err = c.Open(ctx); err != nil {
		return err
	}
	tasks.Go(func() { failures <- t.run(ctx) })
	select {
	case <-ctx.Done():
		return nil
	case err = <-failures:
		return err
	}
}
