package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

var errPersonalHistoryRepair = errors.New("discord: personal DM history recovery is in progress")
var errPersonalHistoryBoundary = errors.New("previous message boundary unavailable")
var errPersonalHistoryPermission = errors.New("read permission unavailable")

const personalHistoryNotice = "personal_history_recovery"
const personalHistoryNoticeDelay = 30 * time.Second

func isPersonalDiscussion(cfg Config, d TeammateDiscussion) bool {
	return cfg.Mode == "personal" && cfg.InstallationID == d.InstallationID &&
		d.Route == (Route{ChannelID: cfg.DMChannelID})
}

func personalRepairPending(b teammateBinding, epoch uint64) bool {
	return b.Opened && b.PersonalRepairEpoch < epoch
}

// A parked DM still needs an ingress scan on a new gateway epoch, without a human prompt.
func (s *Store) preparePersonalRepair(cfg Config, epoch uint64) error {
	if cfg.Mode != "personal" || epoch == 0 {
		return nil
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		var checkpoint teammateCheckpoint
		if err := teammateGet(tx.Bucket([]byte(teammateMetaBucket)), teammateKey(cfg.InstallationID, "checkpoint"), &checkpoint); err != nil {
			return err
		}
		if checkpoint.GapEpoch != epoch {
			return nil
		}
		key := teammateKey(cfg.InstallationID, cfg.DMChannelID)
		var b teammateBinding
		if err := teammateGet(tx.Bucket([]byte(teammateDiscussionBucket)), key, &b); err != nil {
			if errors.Is(err, ErrNotFound) {
				return nil
			}
			return err
		}
		if !b.Opened || !isPersonalDiscussion(cfg, b.TeammateDiscussion) || !personalRepairPending(b, epoch) {
			return nil
		}
		if err := teammateActivate(tx, &b); err != nil {
			return err
		}
		if b.PersonalScanEpoch == epoch && b.RepairTargetEpoch == epoch && b.RepairLower != "" {
			return nil
		}
		if b.PersonalRepairEpoch == 0 {
			// Legacy context repair could move Boundary past unadmitted messages.
			// The original owner admission guard is the durable consent boundary.
			if b.PersonalConsentID == "" {
				start, err := personalFirstAdmission(tx, cfg, b.TeammateDiscussion)
				if err != nil {
					return err
				}
				b.PersonalConsentID = start
			}
			b.RepairLower = b.PersonalConsentID
		} else if b.RepairLower == "" {
			b.RepairLower = b.Boundary
		}
		b.PersonalScanEpoch, b.RepairTargetEpoch = epoch, epoch
		b.RepairAfter, b.RepairUpper = "", ""
		return putJSON(tx, teammateDiscussionBucket, key, b)
	})
}

func personalFirstAdmission(tx *bolt.Tx, cfg Config, d TeammateDiscussion) (string, error) {
	var first string
	err := teammateEach(tx.Bucket([]byte(teammateIngressGuardBucket)), cfg.InstallationID, func(_, raw []byte) error {
		var guard teammateIngressGuard
		if err := json.Unmarshal(raw, &guard); err != nil {
			return err
		}
		if guard.DiscussionID == d.ID && guard.Disposition == StateSent && guard.OperationID != "" &&
			guard.ActorID == cfg.OwnerID && guard.Route.ChannelID == d.Route.ChannelID && guard.Route.GuildID == "" &&
			snowflake(guard.Route.MessageID) && (first == "" || snowflakeLess(guard.Route.MessageID, first)) {
			first = guard.Route.MessageID
		}
		return nil
	})
	return first, err
}

func (s *Store) personalHistoryConsent(cfg Config, d TeammateDiscussion, anchor string) (string, error) {
	var floor string
	err := s.db.View(func(tx *bolt.Tx) error {
		var b teammateBinding
		if err := teammateGet(tx.Bucket([]byte(teammateDiscussionBucket)), teammateKey(d.InstallationID, d.Route.ChannelID), &b); err != nil {
			return err
		}
		if b.ID != d.ID || b.Route != d.Route {
			return ErrNotAuthorized
		}
		if !b.Opened && snowflake(anchor) {
			floor = anchor
			return nil
		}
		floor = b.PersonalConsentID
		if floor != "" {
			return nil
		}
		var err error
		floor, err = personalFirstAdmission(tx, cfg, d)
		return err
	})
	return floor, err
}

func (t *teammateConnector) repairPersonalHistory(ctx context.Context, b teammateBinding, epoch uint64) error {
	if !snowflake(b.RepairLower) {
		return fmt.Errorf("%w: %w", errPersonalHistoryRepair, errPersonalHistoryBoundary)
	}
	for range 4 {
		rows, err := t.client.teammateHistoryMessages(ctx, b.Route, b.RepairAfter, 100)
		if err != nil {
			return fmt.Errorf("%w: could not read history page: %w", errPersonalHistoryRepair, err)
		}
		upper, low := b.RepairUpper, ""
		seen := make(map[string]bool, len(rows))
		var records []teammateCandidate
		var receipts []personalHistoryReceipt
		for _, m := range rows {
			if m.Author == nil || !snowflake(m.Author.ID) || seen[m.ID] {
				return fmt.Errorf("%w: invalid message identity in history page", errPersonalHistoryRepair)
			}
			seen[m.ID] = true
			if b.RepairAfter == "" && snowflakeLess(upper, m.ID) {
				upper = m.ID
			}
			if low == "" || snowflakeLess(m.ID, low) {
				low = m.ID
			}
			if m.Author.ID == t.service.botUserID() && m.WebhookID == "" {
				if nonce := m.Nonce; nonce != "" {
					receipts = append(receipts, personalHistoryReceipt{m.ID, nonce, m.Content})
				}
			}
			if snowflakeLess(b.RepairLower, m.ID) && t.admissibleHuman(&m.Message) {
				records = append(records, teammateCandidate{
					Route:  Route{ChannelID: b.Route.ChannelID, MessageID: m.ID},
					Author: Actor{ID: m.Author.ID, Name: m.Author.Username},
				})
			}
		}
		done := len(rows) < 100 || !snowflakeLess(b.RepairLower, low)
		if err := t.service.Store.recordPersonalRepairPage(b, epoch, upper, low, done, records, receipts, t.service.Now()); err != nil {
			return err
		}
		if done {
			return nil
		}
		b.RepairUpper, b.RepairAfter = upper, low
	}
	return errPersonalHistoryRepair
}

type personalHistoryReceipt struct{ id, nonce, content string }

func (s *Store) recordPersonalRepairPage(b teammateBinding, epoch uint64, upper, low string, done bool, records []teammateCandidate, receipts []personalHistoryReceipt, now time.Time) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		key := teammateKey(b.InstallationID, b.Route.ChannelID)
		bucket := tx.Bucket([]byte(teammateDiscussionBucket))
		var current teammateBinding
		if err := teammateGet(bucket, key, &current); err != nil {
			return err
		}
		if current.ID != b.ID || current.Route != b.Route || !current.Opened ||
			current.RepairTargetEpoch != epoch || current.PersonalScanEpoch != epoch || !personalRepairPending(current, epoch) ||
			current.RepairAfter != b.RepairAfter || current.RepairLower != b.RepairLower {
			return ErrConflict
		}
		if err := teammateRecordCandidates(tx, b.InstallationID, records, false); err != nil {
			return err
		}
		for _, receipt := range receipts {
			if err := teammateObserveReceipt(tx, b.InstallationID, b.Route, receipt.id, receipt.nonce, receipt.content, now); err != nil {
				return err
			}
		}
		if err := teammateGet(bucket, key, &current); err != nil {
			return err
		}
		current.RepairUpper, current.RepairAfter = upper, low
		if done {
			if current.PersonalHistoryFailureEpoch != 0 {
				id := personalHistoryNoticeID(b.TeammateDiscussion, current.PersonalHistoryFailureEpoch)
				if err := teammateCancelPreparedNotice(tx, b.TeammateDiscussion, id, personalHistoryNotice); err != nil {
					return err
				}
				current.PersonalHistoryFailureEpoch, current.PersonalHistoryFailureSince = 0, time.Time{}
			}
			if err := clearPreparedPersonalGapNotices(tx, b.TeammateDiscussion); err != nil {
				return err
			}
			current.RepairEpoch, current.NotifiedEpoch, current.PersonalRepairEpoch = epoch, epoch, epoch
			current.RepairAfter, current.RepairUpper, current.RepairLower = "", "", ""
			if strings.HasPrefix(current.Error, errPersonalHistoryRepair.Error()) {
				current.Error = ""
			}
			checkpointKey := teammateKey(b.InstallationID, "checkpoint")
			var checkpoint teammateCheckpoint
			err := teammateGet(tx.Bucket([]byte(teammateMetaBucket)), checkpointKey, &checkpoint)
			if err != nil && !errors.Is(err, ErrNotFound) {
				return err
			}
			if checkpoint.GapEpoch == epoch && checkpoint.Error == teammateContextGapMessage {
				checkpoint.Error = ""
				if err := putJSON(tx, teammateMetaBucket, checkpointKey, checkpoint); err != nil {
					return err
				}
			}
			if snowflakeLess(current.Boundary, upper) {
				current.Boundary = upper
			}
		}
		return putJSON(tx, teammateDiscussionBucket, key, current)
	})
}

func isLegacyGapNotice(out teammateOutput) bool {
	return out.Internal && out.Publication.SystemCause == "" && strings.HasPrefix(out.Publication.ID, "gap_")
}

func clearPreparedPersonalGapNotices(tx *bolt.Tx, d TeammateDiscussion) error {
	var ids []string
	err := teammateEach(tx.Bucket([]byte(teammateOutboxBucket)), d.InstallationID+"\x00"+d.ID, func(_, raw []byte) error {
		var out teammateOutput
		if err := json.Unmarshal(raw, &out); err != nil {
			return err
		}
		if out.Route == d.Route && isLegacyGapNotice(out) {
			ids = append(ids, out.Publication.ID)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := teammateCancelPreparedNotice(tx, d, id, ""); err != nil {
			return err
		}
	}
	return nil
}

func personalHistoryNoticeID(d TeammateDiscussion, epoch uint64) string {
	return personalHistoryNotice + "_" + digest([]any{d.ID, epoch})[:32]
}

func (t *teammateConnector) notifyPersonalHistoryHold(ctx context.Context, b teammateBinding, epoch uint64, problem error) error {
	if problem == nil || problem == errPersonalHistoryRepair || errors.Is(problem, ErrConflict) ||
		errors.Is(problem, context.Canceled) || !isPersonalDiscussion(t.service.Config, b.TeammateDiscussion) {
		return nil
	}
	d := b.TeammateDiscussion
	now := t.service.Now()
	store := t.service.Store
	var since time.Time
	var noticeEpoch uint64
	err := store.teammateChangeBinding(d, func(current *teammateBinding) {
		if current.PersonalScanEpoch != epoch || !personalRepairPending(*current, epoch) {
			return
		}
		if current.PersonalHistoryFailureEpoch == 0 {
			current.PersonalHistoryFailureEpoch, current.PersonalHistoryFailureSince = epoch, now
		}
		since, noticeEpoch = current.PersonalHistoryFailureSince, current.PersonalHistoryFailureEpoch
	})
	permanent := errors.Is(problem, errPersonalHistoryBoundary) || errors.Is(problem, errPersonalHistoryPermission) ||
		errors.Is(problem, ErrNotAuthorized) || teammateAPIStatus(problem, http.StatusForbidden)
	if err != nil || noticeEpoch == 0 || !permanent && now.Sub(since) < personalHistoryNoticeDelay {
		return err
	}
	id := personalHistoryNoticeID(d, noticeEpoch)
	p := TeammatePublication{ID: id, SystemCause: personalHistoryNotice,
		Body: "I can’t verify some earlier Discord messages yet. I’ll keep retrying. Requests I’ve already received are saved in order."}
	if err := store.teammateQueue(d.InstallationID, d.ID, d.Route, p, now, true); err != nil {
		return err
	}
	var out teammateOutput
	err = store.db.View(func(tx *bolt.Tx) error {
		return teammateGet(tx.Bucket([]byte(teammateOutboxBucket)), teammateOutputKey(d.InstallationID, d.ID, id), &out)
	})
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return t.sendOutput(ctx, d.InstallationID, out)
}
