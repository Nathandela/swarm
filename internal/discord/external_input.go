package discord

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"
	bolt "go.etcd.io/bbolt"
)

// ExternalInputConfig enrolls a local service and the one Discord route and
// named project it may submit work to.
type ExternalInputConfig struct {
	GuildID   string `json:"guild_id"`
	ChannelID string `json:"channel_id"`
	WebhookID string `json:"webhook_id"`
	Project   string `json:"project"`
	TokenFile string `json:"token_file"`
}

type externalInputScope struct {
	GuildID, ChannelID, WebhookID, Project string
}

type externalInput struct {
	EventID       string `json:"event_id"`
	MessageID     string `json:"message_id"`
	ContentSHA256 string `json:"content_sha256"`
	Text          string `json:"text"`
	ExpiresAt     string `json:"expires_at"`
}

type externalReceipt struct {
	EventID      string `json:"event_id"`
	State        string `json:"state"`
	DiscussionID string `json:"discussion_id,omitempty"`
	ThreadID     string `json:"thread_id,omitempty"`
}

type externalRecord struct {
	Scope   externalInputScope
	Input   externalInput
	Hash    string
	Receipt externalReceipt
}

const (
	externalInputSocket  = "external-input"
	externalInputHorizon = 10 * time.Minute
	externalQueued       = "queued"
	externalDispatching  = "dispatching"
	externalAccepted     = "accepted"
	externalBlocked      = "blocked"
)

func validExternalEventID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	return strings.IndexFunc(id, func(r rune) bool {
		return !strings.ContainsRune("0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ-_", r)
	}) < 0
}

func (e externalInput) expiry() (time.Time, error) {
	return time.Parse(time.RFC3339Nano, e.ExpiresAt)
}

func (e externalInput) validate(scope externalInputScope) error {
	hash, err := hex.DecodeString(e.ContentSHA256)
	if !validExternalEventID(e.EventID) || !snowflake(e.MessageID) || e.MessageID == scope.ChannelID ||
		err != nil || len(hash) != sha256.Size || strings.ToLower(e.ContentSHA256) != e.ContentSHA256 ||
		strings.TrimSpace(e.Text) == "" || len(e.Text) > 1000 || !utf8.ValidString(e.Text) || strings.ContainsRune(e.Text, 0) {
		return ErrInvalid
	}
	if _, err := e.expiry(); err != nil || len(e.ExpiresAt) > 35 {
		return ErrInvalid
	}
	return nil
}

func (t *teammateConnector) externalInputAuth() (string, error) {
	cfg := t.service.Config
	e := cfg.ExternalInput
	if e == nil {
		return "", nil
	}
	token, err := LoadToken(e.TokenFile)
	if err != nil || len(token) < 32 || strings.IndexFunc(token, func(r rune) bool { return r > unicode.MaxASCII || unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 ||
		subtle.ConstantTimeCompare([]byte(token), []byte(t.client.token)) == 1 {
		return "", errors.New("discord: external input requires a distinct private bearer of at least 32 characters")
	}
	project, err := filepath.EvalSymlinks(cfg.Projects[e.Project])
	if err != nil {
		return "", errors.New("discord: external input project is unavailable")
	}
	info, err := os.Stat(project)
	if err != nil || !info.IsDir() {
		return "", errors.New("discord: external input project is unavailable")
	}
	t.external = externalInputScope{GuildID: e.GuildID, ChannelID: e.ChannelID, WebhookID: e.WebhookID, Project: project}
	return "Bearer " + token, nil
}

func (t *teammateConnector) externalHandler(bearer string, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if bearer == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(bearer)) != 1 {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if r.URL.Path != "/admissions" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	store, install := t.service.Store, t.service.Config.InstallationID
	var receipt externalReceipt
	var err error
	switch r.Method {
	case http.MethodGet:
		id := r.URL.Query().Get("event_id")
		if validExternalEventID(id) {
			var record externalRecord
			record, err = store.externalRecord(install, id)
			if err == nil && record.Scope != t.external {
				err = ErrNotFound
			}
			receipt = record.Receipt
		} else {
			err = ErrInvalid
		}
	case http.MethodPost:
		var event externalInput
		if err = decodeLocal(w, r, &event); err == nil {
			err = event.validate(t.external)
		}
		if err == nil {
			receipt, err = store.queueExternal(install, t.external, event, t.service.Now())
		}
		if err == nil {
			t.signal()
		}
	default:
		w.Header().Set("Allow", "GET, POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err == nil {
		_ = json.NewEncoder(w).Encode(receipt)
		return
	}
	switch {
	case errors.Is(err, ErrInvalid):
		w.WriteHeader(http.StatusBadRequest)
	case errors.Is(err, ErrNotFound):
		w.WriteHeader(http.StatusNotFound)
	case errors.Is(err, ErrConflict):
		w.WriteHeader(http.StatusConflict)
	default:
		w.WriteHeader(http.StatusServiceUnavailable)
	}
}

func externalRecordKey(installation, event string) []byte {
	return teammateKey(installation, "external-input:"+event)
}

func (s *Store) externalRecord(installation, event string) (record externalRecord, err error) {
	err = s.db.View(func(tx *bolt.Tx) error {
		return teammateGet(tx.Bucket([]byte(teammateMetaBucket)), externalRecordKey(installation, event), &record)
	})
	return
}

func (s *Store) queueExternal(installation string, scope externalInputScope, input externalInput, now time.Time) (receipt externalReceipt, err error) {
	hash := digest(input)
	err = s.db.Update(func(tx *bolt.Tx) error {
		meta := tx.Bucket([]byte(teammateMetaBucket))
		key := externalRecordKey(installation, input.EventID)
		var prior externalRecord
		err := teammateGet(meta, key, &prior)
		if err == nil {
			if prior.Hash != hash || prior.Scope != scope {
				return ErrConflict
			}
			receipt = prior.Receipt
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		expiry, err := input.expiry()
		if err != nil || expiry.After(now.Add(externalInputHorizon)) {
			return ErrInvalid
		}
		starter := teammateKey(installation, input.MessageID)
		for _, bucket := range []string{teammateDiscussionBucket, teammateIngressBucket, teammateIngressGuardBucket} {
			if tx.Bucket([]byte(bucket)).Get(starter) != nil {
				return ErrConflict
			}
		}
		receipt = externalReceipt{EventID: input.EventID, State: externalQueued}
		if err := putJSON(tx, teammateMetaBucket, key, externalRecord{Scope: scope, Input: input, Hash: hash, Receipt: receipt}); err != nil {
			return err
		}
		if err := meta.Put(teammateKey(installation, "parent:"+scope.ChannelID), []byte{1}); err != nil {
			return err
		}
		candidate := teammateCandidate{
			Route:   Route{GuildID: scope.GuildID, ChannelID: scope.ChannelID, MessageID: input.MessageID},
			Author:  Actor{ID: scope.WebhookID, Name: "Webhook " + scope.WebhookID},
			Opening: true, ExternalEventID: input.EventID,
		}
		return teammateRecordCandidates(tx, installation, []teammateCandidate{candidate}, false)
	})
	return
}

func updateExternalReceipt(tx *bolt.Tx, installation string, candidate teammateCandidate) error {
	if candidate.ExternalEventID == "" {
		return nil
	}
	key := externalRecordKey(installation, candidate.ExternalEventID)
	var record externalRecord
	if err := teammateGet(tx.Bucket([]byte(teammateMetaBucket)), key, &record); err != nil {
		return err
	}
	switch candidate.State {
	case StateSending, teammateStateAccepted:
		record.Receipt.State = externalDispatching
	case StateSent:
		record.Receipt.State = externalAccepted
		record.Input.Text = ""
	case StateFailed:
		record.Receipt.State = externalBlocked
		record.Input.Text = ""
	}
	if a := candidate.Admission; a != nil {
		record.Receipt.DiscussionID = a.Discussion.ID
		record.Receipt.ThreadID = a.Discussion.Route.ChannelID
	}
	return putJSON(tx, teammateMetaBucket, key, record)
}

func (t *teammateConnector) externalAnchor(ctx context.Context, route Route, record externalRecord, initial bool) (int64, error) {
	expiry, err := record.Input.expiry()
	if err != nil || initial && !t.service.Now().Before(expiry) || t.external != record.Scope ||
		route.GuildID != record.Scope.GuildID || route.ChannelID != record.Scope.ChannelID || route.MessageID != record.Input.MessageID {
		return 0, ErrTeammateSourceChanged
	}
	channel, _, bits, err := teammateChannelScope(ctx, t.client, t.service.Config, route, t.service.botUserID())
	if err == nil && (channel.Type != discordgo.ChannelTypeGuildText || bits&discordgo.PermissionReadMessageHistory == 0) {
		err = ErrNotAuthorized
	}
	if err != nil {
		return 0, err
	}
	m, err := t.client.teammateMessage(ctx, route)
	if err != nil {
		return 0, err
	}
	hash := sha256.Sum256([]byte(m.Content))
	if m.WebhookID != record.Scope.WebhookID || hex.EncodeToString(hash[:]) != record.Input.ContentSHA256 ||
		initial && !t.service.Now().Before(expiry) {
		return 0, ErrTeammateSourceChanged
	}
	return bits, nil
}

func permanentExternalSource(err error) bool {
	return errors.Is(err, ErrTeammateSourceChanged) || errors.Is(err, ErrNotAuthorized) || errors.Is(err, ErrConflict) ||
		errors.Is(err, errTeammateUnsupported) || teammateAPIStatus(err, http.StatusForbidden) || teammateAPIStatus(err, http.StatusNotFound)
}

func (t *teammateConnector) processExternal(ctx context.Context, candidate teammateCandidate) error {
	cfg, store := t.service.Config, t.service.Store
	record, err := store.externalRecord(cfg.InstallationID, candidate.ExternalEventID)
	if err != nil {
		return err
	}
	fail := func(err error) error {
		if permanentExternalSource(err) {
			return store.teammateCandidateChange(cfg.InstallationID, candidate, StateFailed, err.Error(), nil)
		}
		return err
	}
	a := candidate.Admission
	if a != nil {
		phase, err := t.manager.AdmissionState(ctx, *a)
		if err != nil {
			return err
		}
		switch phase {
		case TeammateAdmissionSuperseded:
			if err = t.manager.CancelAdmission(ctx, *a); err != nil {
				return err
			}
			return t.saveAdmissionPhase(candidate, TeammateAdmissionCancelled)
		case TeammateAdmissionMissing:
			if candidate.State == teammateStateAccepted {
				return ErrConflict
			}
		default:
			return t.saveAdmissionPhase(candidate, phase)
		}
	}
	if candidate.Deleted {
		return fail(ErrTeammateSourceChanged)
	}
	// Discord can update the starter's metadata when it attaches a thread.
	bits, err := t.externalAnchor(ctx, candidate.Route, record, true)
	if err != nil {
		return fail(err)
	}
	if a == nil {
		route, err := t.client.teammateThread(ctx, candidate.Route, clipUTF16(cfg.Name+" discussion", 95), bits&discordgo.PermissionCreatePublicThreads != 0)
		if err != nil {
			return fail(err)
		}
		d := TeammateDiscussion{
			ID:             "dt_" + digest([]string{cfg.InstallationID, route.GuildID, route.ChannelID})[:32],
			InstallationID: cfg.InstallationID, SupervisorSocket: cfg.DaemonSocket,
			Route: route, Starter: candidate.Route, FixedProject: record.Scope.Project,
		}
		if err := t.checkDiscussion(ctx, d); err != nil {
			return fail(err)
		}
		checkpoint, err := store.teammateCheckpoint(cfg.InstallationID)
		if err != nil {
			return err
		}
		if err := store.teammateBind(teammateBinding{
			TeammateDiscussion: d, RepairEpoch: checkpoint.GapEpoch, NotifiedEpoch: checkpoint.GapEpoch,
		}); err != nil {
			return fail(err)
		}
		binding, err := store.teammateBindingByID(cfg.InstallationID, d.ID)
		if err != nil {
			return err
		}
		a = &TeammateAdmission{
			Discussion: binding.TeammateDiscussion, OperationID: "external_" + digest([]string{cfg.InstallationID, record.Input.EventID})[:32],
			Revision: record.Hash, Route: candidate.Route, Authority: AuthorityExternal,
			Author: candidate.Author, InvokerID: candidate.Author.ID, Sequence: candidate.Sequence, ExpectReply: true,
		}
		if err := store.teammateCandidateChange(cfg.InstallationID, candidate, StateSending, "", a); err != nil {
			return err
		}
		candidate.State, candidate.Admission = StateSending, a
	}
	admission := *a
	admission.Text = record.Input.Text
	if err := t.verifyAdmissionMessage(ctx, admission.Discussion, TeammateHistoryQuery{
		Source: "admission", AdmissionID: admission.OperationID, Revision: admission.Revision,
		MessageID: admission.Route.MessageID, AuthorID: admission.Author.ID,
	}, nil, nil); err != nil {
		return fail(err)
	}
	return t.dispatchAdmission(ctx, candidate, admission)
}
