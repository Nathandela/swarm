package discord

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/bwmarrin/discordgo"
	bolt "go.etcd.io/bbolt"
)

const callAdmissionBucket = "teammate_call_admissions"

type callAdmissionRecord struct {
	DiscussionID string
	Authority    Authority
	Route        Route
	Voice        schema.VoiceOrigin
}

func (t *teammateConnector) callDiscussion(ctx context.Context, guild, channel string) (TeammateDiscussion, error) {
	cfg := t.service.Config
	if cfg.Calls == nil || channel != cfg.Calls.DiscussionID || !snowflake(channel) || cfg.Mode == "personal" && (guild != "" || channel != cfg.DMChannelID) ||
		cfg.Mode == "teammate" && (guild != cfg.Calls.GuildID || !containsID(cfg.GuildIDs, guild)) || !cfg.TeammateMode() {
		return TeammateDiscussion{}, ErrNotAuthorized
	}
	id := "dt_" + digest([]string{cfg.InstallationID, guild, channel})[:32]
	b, err := t.service.Store.teammateBindingByID(cfg.InstallationID, id)
	if err != nil {
		return TeammateDiscussion{}, err
	}
	if !b.Opened || b.ProvisionalCancelled || b.Route.GuildID != guild || b.Route.ChannelID != channel {
		return TeammateDiscussion{}, ErrNotAuthorized
	}
	return b.TeammateDiscussion, t.checkDiscussion(ctx, b.TeammateDiscussion)
}

func (t *teammateConnector) callSupervisor(bound TeammateDiscussion) string {
	cfg := t.service.Config
	if cfg.PreviousSupervisor != nil && bound.SupervisorSocket == "" {
		return cfg.PreviousSupervisor.Socket
	}
	if bound.SupervisorSocket != "" {
		return bound.SupervisorSocket
	}
	return cfg.DaemonSocket
}

func (t *teammateConnector) checkCallSpeaker(ctx context.Context, bound TeammateDiscussion, speakerID string) error {
	return t.checkCallSpeakerAudience(ctx, bound, speakerID, true)
}

func (t *teammateConnector) checkCallSpeakerAudience(ctx context.Context, bound TeammateDiscussion, speakerID string, live bool) error {
	cfg := t.service.Config
	if bound.InstallationID != cfg.InstallationID || live && (cfg.Calls == nil || bound.Route.ChannelID != cfg.Calls.DiscussionID ||
		cfg.Mode == "teammate" && (bound.Route.GuildID != cfg.Calls.GuildID || !containsID(cfg.Calls.UserIDs, speakerID))) {
		return ErrNotAuthorized
	}
	if !snowflake(speakerID) || speakerID == t.service.botUserID() || cfg.Mode == "personal" && speakerID != cfg.OwnerID {
		return ErrNotAuthorized
	}
	if live && cfg.Mode == "personal" {
		if err := t.service.Store.db.View(func(tx *bolt.Tx) error {
			return checkPersonalRepairGate(tx, cfg, bound)
		}); err != nil {
			return err
		}
	}
	channel, parent, botBits, err := teammateChannelScope(ctx, t.client, cfg, bound.Route, t.service.botUserID())
	if err != nil || cfg.Mode == "personal" {
		return err
	}
	const botRequired = discordgo.PermissionReadMessageHistory | discordgo.PermissionSendMessagesInThreads
	if !channel.IsThread() || channel.ThreadMetadata.Locked || live && channel.ThreadMetadata.Archived || botBits&botRequired != botRequired {
		return ErrNotAuthorized
	}
	var permissions struct{ Permissions string }
	if err := t.client.REST(ctx, http.MethodGet, "/channels/"+parent.ID+"/permissions/member/"+speakerID, nil, &permissions); err != nil {
		return err
	}
	bits, err := strconv.ParseInt(permissions.Permissions, 10, 64)
	const participantRequired = discordgo.PermissionViewChannel | discordgo.PermissionReadMessageHistory | discordgo.PermissionSendMessagesInThreads
	if err != nil || bits < 0 || bits&discordgo.PermissionAdministrator == 0 && bits&participantRequired != participantRequired {
		return ErrNotAuthorized
	}
	if channel.Type == discordgo.ChannelTypeGuildPrivateThread && bits&(discordgo.PermissionManageThreads|discordgo.PermissionAdministrator) == 0 {
		var member discordgo.ThreadMember
		if err := t.client.REST(ctx, http.MethodGet, "/channels/"+channel.ID+"/thread-members/"+speakerID, nil, &member); err != nil {
			return err
		}
		if member.ID != channel.ID || member.UserID != speakerID {
			return ErrNotAuthorized
		}
	}
	return nil
}

func callAdmissionID(installation, call, turn string) string {
	return "dv_" + digest([]string{installation, call, turn})[:32]
}

func (t *teammateConnector) submitCallTurn(ctx context.Context, bound TeammateDiscussion, callID, turnID string, generation uint64, speaker Actor, text string) (string, error) {
	if strings.TrimSpace(text) == "" || len(text) > 4096 || !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return "", ErrInvalid
	}
	cfg := t.service.Config
	voice := schema.VoiceOrigin{CallID: callID, TurnID: turnID, Generation: generation, SpeakerID: speaker.ID,
		SupervisorSocket: t.callSupervisor(bound), BodyHash: digest(text)}
	op := callAdmissionID(cfg.InstallationID, callID, turnID)
	authority := AuthorityExternal
	if speaker.ID == cfg.OwnerID {
		authority = AuthorityOwnerDirect
	}
	origin := schema.MessageOrigin{Kind: string(authority), InstallationID: cfg.InstallationID, DiscussionID: bound.ID, RequestID: op,
		ActorID: speaker.ID, ActorName: speaker.Name, RequesterID: speaker.ID, ChannelID: bound.Route.ChannelID, GuildID: bound.Route.GuildID,
		Revision: voice.BodyHash, Voice: voice}
	if origin.Validate() != nil || bound.InstallationID != cfg.InstallationID || !snowflake(speaker.ID) {
		return "", ErrInvalid
	}
	a := TeammateAdmission{Discussion: bound, OperationID: op, Revision: voice.BodyHash, Text: text, Route: bound.Route,
		Author: speaker, InvokerID: speaker.ID, Authority: authority, Voice: voice}
	record := callAdmissionRecord{DiscussionID: bound.ID, Authority: authority, Route: bound.Route, Voice: voice}
	t.callAdmissionMu.Lock()
	if t.callAdmissions == nil {
		t.callAdmissions = make(map[string]int)
	}
	t.callAdmissions[bound.ID]++
	t.callAdmissionMu.Unlock()
	defer func() {
		t.callAdmissionMu.Lock()
		t.callAdmissions[bound.ID]--
		if t.callAdmissions[bound.ID] == 0 {
			delete(t.callAdmissions, bound.ID)
		}
		t.callAdmissionMu.Unlock()
	}()
	recordKey := teammateKey(cfg.InstallationID, op)
	if err := t.service.Store.db.Update(func(tx *bolt.Tx) error {
		bindingKey := teammateKey(cfg.InstallationID, bound.Route.ChannelID)
		var binding teammateBinding
		if err := teammateGet(tx.Bucket([]byte(teammateDiscussionBucket)), bindingKey, &binding); err != nil {
			return err
		}
		if binding.ID != bound.ID || binding.Route != bound.Route || t.callSupervisor(binding.TeammateDiscussion) != voice.SupervisorSocket ||
			!binding.Opened || binding.ProvisionalCancelled {
			return ErrNotAuthorized
		}
		var previous callAdmissionRecord
		err := teammateGet(tx.Bucket([]byte(callAdmissionBucket)), recordKey, &previous)
		if err == nil && previous != record {
			return ErrConflict
		}
		if errors.Is(err, ErrNotFound) {
			if t.verifyCallTurn == nil || cfg.Calls == nil {
				return ErrTeammateSourceChanged
			}
			if err = putJSON(tx, callAdmissionBucket, recordKey, record); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		return teammateActivate(tx, &binding)
	}); err != nil {
		return "", err
	}
	phase, err := t.manager.AdmissionState(ctx, a)
	if err != nil {
		return "", err
	}
	switch phase {
	case TeammateAdmissionMissing:
		q := TeammateHistoryQuery{Source: "admission", AdmissionID: op, Revision: voice.BodyHash, AuthorID: speaker.ID, Voice: voice, Authority: authority}
		if err := t.verifyCallAdmission(ctx, bound, q); err != nil {
			return "", err
		}
		if err := t.manager.Admit(ctx, a, t.history(bound)); err != nil {
			// An uncertain response may still have transferred custody.
			phase, stateErr := t.manager.AdmissionState(ctx, a)
			if stateErr != nil || phase != TeammateAdmissionAccepted && phase != TeammateAdmissionCutover {
				return "", errors.Join(err, stateErr)
			}
		}
	case TeammateAdmissionAccepted, TeammateAdmissionCutover:
	default:
		return op, ErrTeammateSourceChanged
	}
	// Fence the drain snapshot that could have started during this admission.
	err = t.service.Store.db.Update(func(tx *bolt.Tx) error {
		key := teammateKey(cfg.InstallationID, bound.Route.ChannelID)
		var binding teammateBinding
		if err := teammateGet(tx.Bucket([]byte(teammateDiscussionBucket)), key, &binding); err != nil {
			return err
		}
		if binding.ID != bound.ID || binding.Route != bound.Route {
			return ErrConflict
		}
		sequence, err := tx.Bucket([]byte(teammateIngressBucket)).NextSequence()
		if err != nil {
			return err
		}
		binding.LastIngressSequence = sequence
		return putJSON(tx, teammateDiscussionBucket, key, binding)
	})
	t.signal()
	return op, err
}

func (t *teammateConnector) parkCallDiscussion(bound TeammateDiscussion, through uint64, native *TeammateNativeStatus) error {
	t.callAdmissionMu.Lock()
	defer t.callAdmissionMu.Unlock()
	if t.callAdmissions[bound.ID] != 0 {
		return nil
	}
	return t.service.Store.teammatePark(bound, through, native)
}

func (t *teammateConnector) callPublicationOrigin(bound TeammateDiscussion, p TeammatePublication) (schema.VoiceOrigin, error) {
	op := p.callAdmission
	if op == "" {
		op = p.AdmissionID
	}
	record, err := t.callAdmission(bound, op)
	return record.Voice, err
}

func (t *teammateConnector) callAdmission(bound TeammateDiscussion, op string) (callAdmissionRecord, error) {
	if !strings.HasPrefix(op, "dv_") {
		return callAdmissionRecord{}, ErrNotFound
	}
	var record callAdmissionRecord
	err := t.service.Store.db.View(func(tx *bolt.Tx) error {
		return teammateGet(tx.Bucket([]byte(callAdmissionBucket)), teammateKey(bound.InstallationID, op), &record)
	})
	if err == nil && (record.DiscussionID != bound.ID || record.Route != bound.Route || record.Voice.SupervisorSocket != t.callSupervisor(bound)) {
		err = ErrTeammateSourceChanged
	}
	return record, err
}

func (t *teammateConnector) verifyCallAdmission(ctx context.Context, bound TeammateDiscussion, q TeammateHistoryQuery) error {
	if q.Cursor != "" || q.Anchor != "" || q.MessageID != "" || q.AuthorID != q.Voice.SpeakerID ||
		q.Revision != q.Voice.BodyHash || q.AdmissionID != callAdmissionID(bound.InstallationID, q.Voice.CallID, q.Voice.TurnID) {
		return ErrInvalid
	}
	record, err := t.callAdmission(bound, q.AdmissionID)
	if errors.Is(err, ErrNotFound) || err == nil && (record.Voice != q.Voice || record.Authority != q.Authority) {
		return ErrTeammateSourceChanged
	}
	if err != nil {
		return err
	}
	if err := t.checkCallSpeakerAudience(ctx, bound, q.AuthorID, q.Source != "grant"); err != nil {
		if errors.Is(err, ErrNotAuthorized) || teammateAPIStatus(err, http.StatusNotFound) || teammateAPIStatus(err, http.StatusForbidden) {
			return ErrTeammateSourceChanged
		}
		return err
	}
	// Once native input has cut over, work keeps its original discussion and
	// speaker grant. The live gate remains mandatory for a queued new input.
	if q.Source == "grant" {
		return nil
	}
	if t.verifyCallTurn == nil {
		return ErrTeammateSourceChanged
	}
	err = t.verifyCallTurn(ctx, bound, record.Voice, true)
	if errors.Is(err, ErrNotAuthorized) {
		return ErrTeammateSourceChanged
	}
	return err
}

func (t *teammateConnector) dispatchCallPublication(ctx context.Context, bound TeammateDiscussion, p TeammatePublication) {
	if t.observeCallPublication == nil || p.SystemCause != "" || p.JobID == "" && !strings.HasPrefix(p.AdmissionID, "dv_") {
		return
	}
	resolver, ok := t.manager.(interface {
		PublicationCallAdmission(context.Context, TeammateDiscussion, TeammatePublication) (string, error)
	})
	if !ok {
		return
	}
	p.BodyHash = PublicationBodyHash(p.Body)
	op, err := resolver.PublicationCallAdmission(ctx, bound, p)
	if err != nil {
		return
	}
	p.callAdmission = op
	origin, err := t.callPublicationOrigin(bound, p)
	if err != nil || t.checkCallSpeaker(ctx, bound, origin.SpeakerID) != nil {
		return
	}
	t.observeCallPublication(bound, p)
}
