package discord

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	bolt "go.etcd.io/bbolt"
)

const teammateNonceWindow = time.Minute
const teammateStartupNotice = "manager_startup_unavailable"

func (s *Store) teammateQueueOutput(d TeammateDiscussion, p TeammatePublication, now time.Time) error {
	if d.ID == "" || d.Route.MessageID != "" || p.BodyHash != "" || p.ActivationGeneration != 0 ||
		strings.HasPrefix(p.ID, teammateStartupNotice+"_") {
		return ErrInvalid
	}
	switch p.SystemCause {
	case "":
		if p.AdmissionID == "" && p.JobID == "" {
			return ErrInvalid
		}
	case TeammateSystemContinuityGap:
		if p.AdmissionID != "" || p.JobID != "" || p.AttemptID != "" || p.QuestionID != "" || p.QuestionHash != "" {
			return ErrInvalid
		}
	case TeammateSystemWorkerEvidenceLimited:
		if p.AdmissionID != "" || p.JobID == "" || p.AttemptID == "" || p.QuestionID != "" || p.QuestionHash != "" {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return s.teammateQueue(d.InstallationID, d.ID, d.Route, p, now, false)
}

func (s *Store) teammateQueueNotice(route Route, install, text string, now time.Time) error {
	id := "notice_" + digest(route)[:32]
	return s.teammateQueue(install, "", route, TeammatePublication{ID: id, Body: text}, now, true)
}

func (t *teammateConnector) queueGap(d TeammateDiscussion) error {
	p := TeammatePublication{ID: "gap_" + digest([]any{d.ID, d.Gap})[:32], Body: "I may have missed requests while disconnected. Please restate anything you still want me to do."}
	return t.service.Store.teammateQueue(d.InstallationID, d.ID, d.Route, p, t.service.Now(), true)
}

func teammateStartupNoticeID(d TeammateDiscussion, generation uint64) string {
	if generation == 0 {
		return teammateStartupNotice + "_" + digest(d.ID)[:32]
	}
	return teammateStartupNotice + "_" + digest([]any{d.ID, d.Route, generation})[:32]
}

func teammateCancelUnsentStartupNotice(tx *bolt.Tx, d TeammateDiscussion, generation uint64) error {
	key := teammateOutputKey(d.InstallationID, d.ID, teammateStartupNoticeID(d, generation))
	var out teammateOutput
	err := teammateGet(tx.Bucket([]byte(teammateOutboxBucket)), key, &out)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !out.Internal || out.Publication.SystemCause != teammateStartupNotice {
		return ErrConflict
	}
	for _, part := range out.Chunks {
		if part.State != StatePrepared {
			return nil
		}
	}
	if err = teammateOutboxCount(tx, d.InstallationID, -1); err != nil {
		return err
	}
	return teammateCompactOutput(tx, key, d, out)
}

func (t *teammateConnector) managerStartupError(ctx context.Context, d TeammateDiscussion, managerErr error) error {
	if !errors.Is(managerErr, ErrTeammateManagerUnavailable) {
		return managerErr
	}
	return errors.Join(managerErr, t.sendStartupNotice(ctx, d, true))
}

func (t *teammateConnector) sendStartupNotice(ctx context.Context, d TeammateDiscussion, prepare bool) error {
	store := t.service.Store
	var binding teammateBinding
	err := store.db.View(func(tx *bolt.Tx) error {
		return teammateGet(tx.Bucket([]byte(teammateDiscussionBucket)), teammateKey(d.InstallationID, d.Route.ChannelID), &binding)
	})
	if err != nil {
		return err
	}
	if binding.ID != d.ID || binding.Route != d.Route {
		return ErrConflict
	}
	if binding.ProvisionalCancelled || !prepare && !binding.StartupNoticePending {
		return nil
	}
	var gen uint64
	if binding.Opened {
		provider, ok := t.manager.(interface {
			Status(TeammateDiscussion) (TeammateNativeStatus, error)
		})
		if !ok {
			return ErrTeammateManagerUnavailable
		}
		status, err := provider.Status(d)
		if err != nil {
			return err
		}
		if status.Generation == 0 {
			return ErrInvalid
		}
		gen = status.Generation
	}
	if binding.StartupNoticePending && binding.StartupNoticeGeneration != gen {
		err = store.db.Update(func(tx *bolt.Tx) error {
			key := teammateKey(d.InstallationID, d.Route.ChannelID)
			var current teammateBinding
			if err := teammateGet(tx.Bucket([]byte(teammateDiscussionBucket)), key, &current); err != nil {
				return err
			}
			if current.ID != d.ID || current.Route != d.Route {
				return ErrConflict
			}
			if !current.StartupNoticePending || current.StartupNoticeGeneration != binding.StartupNoticeGeneration {
				return nil
			}
			if err := teammateCancelUnsentStartupNotice(tx, d, current.StartupNoticeGeneration); err != nil {
				return err
			}
			current.StartupNoticePending = false
			return putJSON(tx, teammateDiscussionBucket, key, current)
		})
		if err != nil || !prepare {
			return err
		}
	}
	id := teammateStartupNoticeID(d, gen)
	if prepare {
		p := TeammatePublication{ID: id, Body: "I couldn’t start my configured manager. Ask the Swarm operator to check the teammate status for details.",
			SystemCause: teammateStartupNotice, ActivationGeneration: gen}
		if err = store.teammateQueue(d.InstallationID, d.ID, d.Route, p, t.service.Now(), true); err != nil {
			return err
		}
	}
	var pending teammateOutput
	err = store.db.View(func(tx *bolt.Tx) error {
		return teammateGet(tx.Bucket([]byte(teammateOutboxBucket)), teammateOutputKey(d.InstallationID, d.ID, id), &pending)
	})
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return t.sendOutput(ctx, d.InstallationID, pending)
}

func (s *Store) teammateQueue(install, id string, route Route, p TeammatePublication, now time.Time, internal bool) error {
	if p.ID == "" || len(p.ID) > 128 || strings.IndexFunc(p.ID, func(r rune) bool { return r < 33 || r > 126 }) >= 0 ||
		strings.TrimSpace(p.Body) == "" || len(p.Body) > MaxPublicationBytes || (p.QuestionID == "") != (p.QuestionHash == "") {
		return ErrInvalid
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(teammateOutboxBucket))
		k := teammateOutputKey(install, id, p.ID)
		var old teammateOutput
		err := teammateGet(b, k, &old)
		bodyHash := PublicationBodyHash(p.Body)
		if err == nil {
			p.Body = ""
			prior := old.Publication
			prior.Body = ""
			if prior != p || old.BodyHash != bodyHash || old.Route != route {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		var compacted teammateReceipt
		err = teammateGet(tx.Bucket([]byte(teammateReceiptBucket)), k, &compacted)
		if err == nil {
			p.Body = ""
			if compacted.PublicationHash != digest(p) || compacted.BodyHash != bodyHash || compacted.Route != route {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		if err = teammateOutboxCount(tx, install, 1); err != nil {
			return err
		}
		if id != "" {
			var binding teammateBinding
			if err = teammateGet(tx.Bucket([]byte(teammateDiscussionBucket)), teammateKey(install, route.ChannelID), &binding); err != nil {
				return err
			}
			if binding.ID != id || binding.Route != route {
				return ErrConflict
			}
			if err = teammateActivate(tx, &binding); err != nil {
				return err
			}
			if internal && p.SystemCause == teammateStartupNotice {
				binding.StartupNoticePending = true
				binding.StartupNoticeGeneration = p.ActivationGeneration
				if err = putJSON(tx, teammateDiscussionBucket, teammateKey(install, route.ChannelID), binding); err != nil {
					return err
				}
			}
		}
		out := teammateOutput{Publication: p, Route: route, DiscussionID: id, BodyHash: bodyHash, Created: now, Internal: internal}
		out.Publication.Body = ""
		index := tx.Bucket([]byte(teammateMetaBucket))
		for n, part := range splitUTF16(p.Body, 1900) {
			nonce := digest([]any{install, id, p.ID, n})[:24]
			if err = index.Put(teammateKey(install, "nonce:"+nonce), k); err != nil {
				return err
			}
			out.ChunkHashes = append(out.ChunkHashes, digest(part))
			out.Chunks = append(out.Chunks, Chunk{ID: nonce, Body: part, State: StatePrepared})
		}
		return putJSON(tx, teammateOutboxBucket, k, out)
	})
}

func teammateOutputKey(install, discussion, id string) []byte {
	return teammateKey(install, discussion+"\x00"+id)
}

func (s *Store) teammateQuestion(d TeammateDiscussion, reply string) (id, hash string, err error) {
	err = s.db.View(func(tx *bolt.Tx) error {
		k := teammateKey(d.InstallationID, "question:"+d.ID+":"+reply)
		raw := tx.Bucket([]byte(teammateMetaBucket)).Get(k)
		if raw == nil {
			return nil
		}
		var p teammateOutput
		if err := teammateGet(tx.Bucket([]byte(teammateOutboxBucket)), raw, &p); err != nil {
			return err
		}
		if p.DiscussionID != d.ID || p.Route != d.Route {
			return ErrConflict
		}
		id, hash = p.Publication.QuestionID, p.Publication.QuestionHash
		return nil
	})
	return
}

func teammateFinishChunk(tx *bolt.Tx, key []byte, install string, out *teammateOutput, n int, id string, observed bool, now time.Time) error {
	c := &out.Chunks[n]
	if !snowflake(id) || c.MessageID != "" && c.MessageID != id {
		return ErrConflict
	}
	c.MessageID, c.State, c.Body = id, StateSent, ""
	c.GatewaySeen = c.GatewaySeen || observed
	c.RESTAcknowledged = c.RESTAcknowledged || !observed
	if out.Publication.QuestionID != "" {
		k := teammateKey(install, "question:"+out.DiscussionID+":"+id)
		b := tx.Bucket([]byte(teammateMetaBucket))
		if prev := b.Get(k); prev != nil && string(prev) != string(key) {
			return ErrConflict
		}
		if err := b.Put(k, key); err != nil {
			return err
		}
	}
	if out.SettledAt.IsZero() {
		settled := true
		for _, part := range out.Chunks {
			if part.State != StateSent {
				settled = false
				break
			}
		}
		if settled {
			out.SettledAt = now
			if out.Internal && out.Publication.SystemCause == teammateStartupNotice {
				k := teammateKey(install, out.Route.ChannelID)
				var binding teammateBinding
				if err := teammateGet(tx.Bucket([]byte(teammateDiscussionBucket)), k, &binding); err != nil {
					return err
				}
				if binding.ID != out.DiscussionID || binding.Route != out.Route {
					return ErrConflict
				}
				if binding.StartupNoticeGeneration == out.Publication.ActivationGeneration {
					binding.StartupNoticePending = false
					if err := putJSON(tx, teammateDiscussionBucket, k, binding); err != nil {
						return err
					}
				}
			}
			return teammateOutboxCount(tx, install, -1)
		}
	}
	return nil
}

func (s *Store) teammateObserve(install string, route Route, id, nonce, body string, now time.Time) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		key := tx.Bucket([]byte(teammateMetaBucket)).Get(teammateKey(install, "nonce:"+nonce))
		if key == nil {
			return nil
		}
		var out teammateOutput
		if err := teammateGet(tx.Bucket([]byte(teammateOutboxBucket)), key, &out); err != nil {
			return err
		}
		if out.Route.GuildID != route.GuildID || out.Route.ChannelID != route.ChannelID {
			return nil
		}
		for n, c := range out.Chunks {
			if c.ID != nonce || out.ChunkHashes[n] != digest(body) || c.State == StatePrepared {
				continue
			}
			if err := teammateFinishChunk(tx, key, install, &out, n, id, true, now); err != nil {
				return err
			}
			return putJSON(tx, teammateOutboxBucket, key, out)
		}
		return nil
	})
}

func (s *Store) teammateOutputs(install, discussion string) (out []teammateOutput, err error) {
	err = s.db.View(func(tx *bolt.Tx) error {
		return teammateEach(tx.Bucket([]byte(teammateOutboxBucket)), install+"\x00"+discussion, func(_ []byte, raw []byte) error {
			var p teammateOutput
			if err := json.Unmarshal(raw, &p); err != nil {
				return err
			}
			for _, c := range p.Chunks {
				if c.State != StateSent {
					out = append(out, p)
					break
				}
			}
			return nil
		})
	})
	sort.Slice(out, func(i, j int) bool {
		if out[i].Publication.Sequence != out[j].Publication.Sequence {
			return out[i].Publication.Sequence < out[j].Publication.Sequence
		}
		if !out[i].Created.Equal(out[j].Created) {
			return out[i].Created.Before(out[j].Created)
		}
		return out[i].Publication.ID < out[j].Publication.ID
	})
	return
}

func (s *Store) teammateChangeOutput(install string, out teammateOutput, change func(*bolt.Tx, []byte, *teammateOutput) error) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		key := teammateOutputKey(install, out.DiscussionID, out.Publication.ID)
		if err := teammateGet(tx.Bucket([]byte(teammateOutboxBucket)), key, &out); err != nil {
			return err
		}
		if err := change(tx, key, &out); err != nil {
			return err
		}
		return putJSON(tx, teammateOutboxBucket, key, out)
	})
}

func (t *teammateConnector) sendPending(ctx context.Context, d TeammateDiscussion) error {
	store := t.service.Store
	outputs, err := store.teammateOutputs(d.InstallationID, d.ID)
	if err != nil {
		return err
	}
	var sendErr error
	for _, out := range outputs {
		if err := ctx.Err(); err != nil {
			return errors.Join(sendErr, err)
		}
		if out.Publication.SystemCause != teammateStartupNotice {
			sendErr = errors.Join(sendErr, t.sendOutput(ctx, d.InstallationID, out))
		}
	}
	return sendErr
}

func (t *teammateConnector) sendOutput(ctx context.Context, install string, out teammateOutput) error {
	store := t.service.Store
	for n, chunk := range out.Chunks {
		if chunk.State == StateSent {
			continue
		}
		if out.Publication.JobID != "" && (chunk.State == StateSending || chunk.State == StateUncertain) {
			const awaitingReceipt = "Worker publication awaiting an authenticated Discord receipt"
			if out.Error == awaitingReceipt {
				return ErrDeliveryUncertain
			}
			held := false
			if err := store.teammateChangeOutput(install, out, func(_ *bolt.Tx, _ []byte, p *teammateOutput) error {
				if p.Chunks[n].State != StateSent {
					held = true
					p.Chunks[n].State = StateUncertain
					p.Error = awaitingReceipt
				}
				return nil
			}); err != nil {
				return err
			}
			if held {
				return ErrDeliveryUncertain
			}
			continue
		}
		if (chunk.State == StateSending || chunk.State == StateUncertain) && t.service.Now().Sub(out.Attempted) >= teammateNonceWindow {
			return store.teammateChangeOutput(install, out, func(_ *bolt.Tx, _ []byte, p *teammateOutput) error {
				if p.Chunks[n].State != StateSent {
					p.Chunks[n].State = StateUncertain
					p.Error = "Discord post outcome unconfirmed beyond nonce window"
				}
				return nil
			})
		}
		if err := t.checkPublication(ctx, install, out); err != nil {
			if e := store.teammateChangeOutput(install, out, func(_ *bolt.Tx, _ []byte, p *teammateOutput) error { p.Error = err.Error(); return nil }); e != nil {
				return e
			}
			return err
		}
		var attempt Chunk
		var alreadyUncertain bool
		var deadline time.Time
		err := store.teammateChangeOutput(install, out, func(_ *bolt.Tx, _ []byte, p *teammateOutput) error {
			now := t.service.Now()
			c := &p.Chunks[n]
			if c.State == StateSent {
				return nil
			}
			if (c.State == StateSending || c.State == StateUncertain) && now.Sub(p.Attempted) >= teammateNonceWindow {
				c.State = StateUncertain
				p.Error = "Discord post outcome unconfirmed beyond nonce window"
				return nil
			}
			alreadyUncertain = c.State != StatePrepared
			if !alreadyUncertain {
				p.Attempted = now
			}
			deadline = p.Attempted.Add(teammateNonceWindow)
			c.State = StateSending
			p.Error = ""
			attempt = *c
			return nil
		})
		if err != nil {
			return err
		}
		if attempt.ID == "" {
			continue
		}
		postCtx, cancel := context.WithTimeout(ctx, deadline.Sub(t.service.Now()))
		message, err := t.client.postMessage(postCtx, Outbound{Route: out.Route, Text: attempt.Body, Nonce: attempt.ID, ReplyTo: out.Route.MessageID})
		cancel()
		if err == nil && (message.ChannelID != out.Route.ChannelID || message.GuildID != "" && message.GuildID != out.Route.GuildID ||
			message.Author == nil || message.Author.ID != t.service.botUserID() || message.WebhookID != "" || message.Content != attempt.Body || !snowflake(message.ID)) {
			err = ErrConflict
		}
		finishErr := store.teammateChangeOutput(install, out, func(tx *bolt.Tx, key []byte, p *teammateOutput) error {
			c := &p.Chunks[n]
			if err == nil {
				return teammateFinishChunk(tx, key, install, p, n, message.ID, false, t.service.Now())
			}
			if c.State == StateSent {
				err = nil
				return nil
			}
			c.State = StateUncertain
			if !alreadyUncertain && (teammateAPIStatus(err, http.StatusForbidden) || teammateAPIStatus(err, http.StatusNotFound)) {
				c.State = StatePrepared
			}
			p.Error = err.Error()
			return nil
		})
		if finishErr != nil {
			return finishErr
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (t *teammateConnector) checkPublication(ctx context.Context, install string, out teammateOutput) error {
	if err := t.checkAudience(ctx, out); err != nil {
		return err
	}
	if out.Internal {
		return nil
	}
	validator, ok := t.manager.(interface {
		ValidatePublication(context.Context, TeammateDiscussion, TeammatePublication) error
	})
	if !ok || out.Publication.JobID != "" && out.Publication.AttemptID == "" {
		return ErrNotAuthorized
	}
	p := out.Publication
	p.BodyHash = out.BodyHash
	return validator.ValidatePublication(ctx, TeammateDiscussion{ID: out.DiscussionID, InstallationID: install, Route: out.Route}, p)
}

func (t *teammateConnector) checkAudience(ctx context.Context, out teammateOutput) error {
	route := out.Route
	checked, _, bits, err := teammateChannelScope(ctx, t.client, t.service.Config, route, t.service.botUserID())
	if err != nil {
		return err
	}
	if out.DiscussionID == "" {
		if checked.IsThread() || bits&discordgo.PermissionSendMessages == 0 {
			return ErrNotAuthorized
		}
		return nil
	}
	if !checked.IsThread() || route.ParentID != checked.ParentID || checked.ThreadMetadata.Locked ||
		bits&discordgo.PermissionSendMessagesInThreads == 0 {
		return ErrNotAuthorized
	}
	// Discord automatically reopens an unlocked archived thread when its reply is posted.
	return nil
}

type teammateReceipt struct {
	PublicationHash, BodyHash string
	Route                     Route
	DiscordMessageIDs         []string
}

func teammateOutboxCount(tx *bolt.Tx, install string, delta int64) error {
	b := tx.Bucket([]byte(teammateMetaBucket))
	key := teammateKey(install, "outbox_count")
	value := b.Get(key)
	var count int64
	if len(value) == 8 {
		count = int64(binary.BigEndian.Uint64(value))
	}
	if count == 0 && delta < 0 {
		return nil
	}
	count += delta
	if count > MaxPublications {
		return ErrCapacity
	}
	return b.Put(key, binary.BigEndian.AppendUint64(nil, uint64(count)))
}

func (s *Store) teammateAckOutputs(d TeammateDiscussion, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(teammateOutboxBucket))
		for _, id := range ids {
			key := teammateOutputKey(d.InstallationID, d.ID, id)
			var out teammateOutput
			if err := teammateGet(b, key, &out); errors.Is(err, ErrNotFound) {
				continue
			} else if err != nil {
				return err
			}
			if out.Route != d.Route {
				return ErrConflict
			}
			out.ExportAck = true
			if err := putJSON(tx, teammateOutboxBucket, key, out); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) teammateCompactOutbox(d TeammateDiscussion, now time.Time, acknowledged func(TeammateDiscussion, string, bool) (bool, error)) error {
	var confirmed, reconcile []teammateOutput
	err := s.db.View(func(tx *bolt.Tx) error {
		return teammateEach(tx.Bucket([]byte(teammateOutboxBucket)), d.InstallationID+"\x00"+d.ID, func(_ []byte, raw []byte) error {
			var out teammateOutput
			if err := json.Unmarshal(raw, &out); err != nil {
				return err
			}
			if !out.SettledAt.IsZero() && now.Sub(out.SettledAt) >= GuardTTL {
				if (out.Internal || out.ExportAck) && len(confirmed) < 32 {
					confirmed = append(confirmed, out)
				} else if !out.Internal && !out.ExportAck && len(reconcile) < 8 {
					reconcile = append(reconcile, out)
				}
			}
			return nil
		})
	})
	if err != nil {
		return err
	}
	for _, out := range append(confirmed, reconcile...) {
		if !out.Internal && !out.ExportAck {
			if acknowledged == nil {
				continue
			}
			yes, err := acknowledged(d, out.Publication.ID, out.Publication.QuestionID != "")
			if err != nil {
				return err
			}
			if !yes {
				continue
			}
		}
		err = s.db.Update(func(tx *bolt.Tx) error {
			key := teammateOutputKey(d.InstallationID, d.ID, out.Publication.ID)
			return teammateCompactOutput(tx, key, d, out)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func teammateCompactOutput(tx *bolt.Tx, key []byte, d TeammateDiscussion, out teammateOutput) error {
	meta := tx.Bucket([]byte(teammateMetaBucket))
	receipt := teammateReceipt{PublicationHash: digest(out.Publication), BodyHash: out.BodyHash, Route: out.Route}
	for _, part := range out.Chunks {
		if part.MessageID != "" {
			receipt.DiscordMessageIDs = append(receipt.DiscordMessageIDs, part.MessageID)
		}
		if err := meta.Delete(teammateKey(d.InstallationID, "nonce:"+part.ID)); err != nil {
			return err
		}
		if out.Publication.QuestionID != "" {
			if err := meta.Delete(teammateKey(d.InstallationID, "question:"+d.ID+":"+part.MessageID)); err != nil {
				return err
			}
		}
	}
	if err := putJSON(tx, teammateReceiptBucket, key, receipt); err != nil {
		return err
	}
	return tx.Bucket([]byte(teammateOutboxBucket)).Delete(key)
}
