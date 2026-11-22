package discord

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"
	bolt "go.etcd.io/bbolt"
)

type teammateHistoryCursor struct {
	Discussion, Source, Upper, Before string
	PartHash                          string `json:",omitempty"`
	Offset                            int    `json:",omitempty"`
}

func (s *Store) teammateCursorKey() (key []byte, err error) {
	err = s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(teammateMetaBucket))
		k := []byte("history_cursor_key")
		if old := b.Get(k); old != nil {
			key = append([]byte(nil), old...)
			return nil
		}
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return err
		}
		return b.Put(k, key)
	})
	return
}

func encodeTeammateCursor(key []byte, c teammateHistoryCursor) string {
	raw, _ := json.Marshal(c)
	mac := hmac.New(sha256.New, key)
	mac.Write(raw)
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func decodeTeammateCursor(key []byte, value string) (c teammateHistoryCursor, err error) {
	if len(value) > 1024 {
		return c, ErrInvalid
	}
	encoded, signature, ok := strings.Cut(value, ".")
	if !ok {
		return c, ErrInvalid
	}
	raw, e := base64.RawURLEncoding.DecodeString(encoded)
	got, e2 := base64.RawURLEncoding.DecodeString(signature)
	mac := hmac.New(sha256.New, key)
	mac.Write(raw)
	if e != nil || e2 != nil || !hmac.Equal(got, mac.Sum(nil)) || json.Unmarshal(raw, &c) != nil ||
		!snowflake(c.Upper) || !snowflake(c.Before) || snowflakeLess(c.Upper, c.Before) || c.Offset < 0 || c.Offset > 2<<20 {
		return c, ErrInvalid
	}
	return c, nil
}

func teammateEvidence(m *discordgo.Message) TeammateHistoryMessage {
	entry := TeammateHistoryMessage{ContextMessage: ContextMessage{ID: m.ID, Text: m.Content, CreatedAt: m.Timestamp}, ReplyTo: teammateReply(m)}
	if m.Author != nil {
		entry.Author = Actor{ID: m.Author.ID, Name: m.Author.Username}
	}
	for _, attachment := range m.Attachments {
		if attachment == nil {
			continue
		}
		available := "metadata only, file access unverified"
		if attachment.URL == "" {
			available = "unavailable"
		}
		entry.Attachments = append(entry.Attachments, TeammateAttachment{ID: attachment.ID, Name: attachment.Filename, ContentType: attachment.ContentType, Size: attachment.Size, Availability: available})
	}
	return entry
}

func teammateHistoryRows(messages []*discordgo.Message) []TeammateHistoryMessage {
	sort.Slice(messages, func(i, j int) bool { return snowflakeLess(messages[i].ID, messages[j].ID) })
	entries := make([]TeammateHistoryMessage, 0, len(messages))
	for _, m := range messages {
		if m.Type == discordgo.MessageTypeThreadStarterMessage || len(entries) > 0 && entries[len(entries)-1].ID == m.ID {
			continue
		}
		entries = append(entries, teammateEvidence(m))
	}
	return entries
}

func (t *teammateConnector) history(d TeammateDiscussion, threadAnchor ...string) TeammateHistoryReader {
	anchor := ""
	if len(threadAnchor) == 1 {
		anchor = threadAnchor[0]
	}
	return func(ctx context.Context, q TeammateHistoryQuery) (TeammateHistoryPage, error) {
		return t.historyPage(ctx, d, q, anchor)
	}
}

func (t *teammateConnector) historyPage(ctx context.Context, d TeammateDiscussion, q TeammateHistoryQuery, threadAnchor string) (out TeammateHistoryPage, err error) {
	if q.Source == "publication" {
		if q.Publication == nil || q.Publication.ID == "" || q.Publication.AdmissionID == "" || q.Publication.SystemCause != "" && q.Publication.SystemCause != "dispatcher_reply_status" {
			return out, ErrInvalid
		}
		if err := t.checkDiscussion(ctx, d); err != nil {
			return out, err
		}
		out.ConfirmedMessageIDs, err = t.service.Store.confirmedPublication(d, *q.Publication)
		return out, err
	}
	if q.Source == "admission" || q.Source == "grant" {
		return out, t.verifyAdmission(ctx, d, q)
	}
	if q.Source == "audience" {
		if q.Cursor != "" || q.Anchor != "" {
			return out, ErrInvalid
		}
		if err := t.service.Store.db.View(func(tx *bolt.Tx) error {
			return checkPersonalRepairGate(tx, t.service.Config, d)
		}); err != nil {
			return out, err
		}
		return out, t.checkDiscussion(ctx, d)
	}
	if q.Source == "" {
		q.Source = "thread"
	}
	if q.Source != "thread" && q.Source != "parent" && q.Source != "starter" || q.Source != "parent" && q.Anchor != "" {
		return out, ErrInvalid
	}
	route := d.Route
	_, _, bits, err := teammateChannelScope(ctx, t.client, t.service.Config, route, t.service.botUserID())
	if err != nil {
		return out, err
	}
	if bits&discordgo.PermissionReadMessageHistory == 0 {
		return out, errors.New("discord: message history permission is missing")
	}
	if q.Source == "parent" {
		if d.ParentAnchor == "" || d.Starter.ChannelID != d.Route.ParentID {
			return out, ErrNotAuthorized
		}
		route = d.Starter
		out.Notice = "Parent-channel messages are context. Discord does not identify the topic boundary."
	}
	if d.Gap > 0 {
		out.Notice = strings.TrimSpace(out.Notice + " There was an unobserved interval. Deleted messages and intermediate edits may be missing.")
	}
	floor := ""
	if q.Source == "thread" && isPersonalDiscussion(t.service.Config, d) {
		floor, err = t.service.Store.personalHistoryConsent(t.service.Config, d, threadAnchor)
		if err != nil {
			return out, err
		}
		if floor == "" {
			return out, fmt.Errorf("%w: previous message boundary unavailable", errPersonalHistoryRepair)
		}
	}
	cursor := teammateHistoryCursor{Discussion: d.ID, Source: q.Source}
	if q.Cursor != "" {
		cursor, err = decodeTeammateCursor(t.cursorKey, q.Cursor)
		if err != nil || cursor.Discussion != d.ID || cursor.Source != q.Source {
			return out, ErrNotAuthorized
		}
	}
	if q.Source == "starter" {
		if d.Starter.MessageID == "" {
			return out, ErrNotAuthorized
		}
		if q.Cursor == "" {
			cursor.Upper = d.Starter.MessageID
			cursor.Before = d.Starter.MessageID
		}
		return t.historyFragment(ctx, d.Starter, out, cursor)
	}
	if cursor.PartHash != "" {
		if floor != "" && snowflakeLess(cursor.Before, floor) {
			return out, ErrNotAuthorized
		}
		route.MessageID = cursor.Before
		if q.Anchor != "" {
			return out, ErrInvalid
		}
		return t.historyFragment(ctx, route, out, cursor)
	}
	var leading []*discordgo.Message
	var topicStart string
	if q.Cursor == "" && d.Starter.MessageID != "" {
		starter, e := t.client.teammateMessage(ctx, d.Starter)
		if teammateAPIStatus(e, http.StatusNotFound) {
			out.Notice = strings.TrimSpace(out.Notice + " The starter is unavailable.")
		}
		if e != nil && !teammateAPIStatus(e, http.StatusNotFound) {
			return out, e
		}
		if starter != nil {
			starterEvidence := teammateEvidence(starter)
			out.Starter = &starterEvidence
			if q.Source == "parent" {
				cursor.Upper, cursor.Before = d.ParentAnchor, d.ParentAnchor
				leading = append(leading, starter)
				chain, missing, e := t.replyChain(ctx, route, starter, "")
				if e != nil {
					return out, e
				}
				if missing {
					out.Notice += " An earlier reply is unavailable."
				}
				if len(chain) > 0 {
					topicStart = chain[len(chain)-1].ID
					out.ReplyChain = teammateHistoryRows(append(chain, starter))
				}
			}
		}
	}
	if q.Source == "thread" && q.Cursor == "" && threadAnchor != "" {
		if floor != "" && snowflakeLess(threadAnchor, floor) {
			return out, ErrNotAuthorized
		}
		anchor, e := t.client.teammateMessage(ctx, Route{GuildID: route.GuildID, ChannelID: route.ChannelID, MessageID: threadAnchor})
		if teammateAPIStatus(e, http.StatusNotFound) {
			out.Notice = strings.TrimSpace(out.Notice + " The invoking message is unavailable.")
		}
		if e != nil && !teammateAPIStatus(e, http.StatusNotFound) {
			return out, e
		}
		if anchor != nil {
			if floor != "" {
				cursor.Upper, cursor.Before = anchor.ID, anchor.ID
			}
			chain, missing, e := t.replyChain(ctx, route, anchor, floor)
			if e != nil {
				return out, e
			}
			if missing {
				out.Notice = strings.TrimSpace(out.Notice + " An earlier reply is unavailable.")
			}
			if len(chain) > 0 {
				out.ReplyChain = teammateHistoryRows(append(chain, anchor))
			}
		}
	}
	if q.Source == "parent" && cursor.Upper == "" {
		cursor.Upper, cursor.Before = d.ParentAnchor, d.ParentAnchor
	}
	if q.Anchor != "" {
		if !snowflake(q.Anchor) || !snowflakeLess(q.Anchor, cursor.Before) {
			return out, ErrInvalid
		}
		anchor, e := t.client.teammateMessage(ctx, Route{GuildID: route.GuildID, ChannelID: route.ChannelID, MessageID: q.Anchor})
		if e != nil {
			return out, e
		}
		cursor.Before = anchor.ID
		leading = []*discordgo.Message{anchor}
	}
	if floor != "" && cursor.Before != "" && !snowflakeLess(floor, cursor.Before) {
		return out, nil
	}
	const pageLimit = 20
	raw, err := t.client.teammateMessages(ctx, route, cursor.Before, pageLimit)
	if err != nil {
		return out, err
	}
	more := len(raw) == pageLimit || q.Anchor != ""
	if floor != "" {
		page := raw[:0]
		for _, message := range raw {
			if snowflakeLess(message.ID, floor) {
				more = false
				continue
			}
			page = append(page, message)
		}
		raw = page
		chain := out.ReplyChain[:0]
		for _, message := range out.ReplyChain {
			if !snowflakeLess(message.ID, floor) {
				chain = append(chain, message)
			}
		}
		out.ReplyChain = chain
	}
	if topicStart != "" && q.Anchor == "" {
		// The reply chain locates the topic but other speakers can contribute between its links.
		page := raw[:0]
		for _, m := range raw {
			if snowflakeLess(m.ID, topicStart) {
				more = true
				continue
			}
			page = append(page, m)
		}
		raw = page
	}
	raw = append(raw, leading...)
	rows := teammateHistoryRows(raw)
	for _, m := range raw {
		if out.LowWater == "" || snowflakeLess(m.ID, out.LowWater) {
			out.LowWater = m.ID
		}
		if q.Cursor == "" && q.Source == "thread" && snowflakeLess(cursor.Upper, m.ID) {
			cursor.Upper = m.ID
		}
	}
	out.HighWater = cursor.Upper

	separateStarter := func() {
		if out.Starter == nil {
			return
		}
		rootCursor := teammateHistoryCursor{Discussion: d.ID, Source: "starter", Upper: d.Starter.MessageID, Before: d.Starter.MessageID}
		out.StarterCursor = encodeTeammateCursor(t.cursorKey, rootCursor)
		out.Starter = nil
		out.Notice = strings.TrimSpace(out.Notice + " The starter is available using source starter and StarterCursor.")
	}
	if out.Starter != nil {
		initial := out
		initial.ReplyChain = nil
		if historyJSONSize(initial) > 8192 {
			separateStarter()
		}
	}
	for len(out.ReplyChain) > 0 && historyJSONSize(out) > 8192 {
		out.ReplyChain = out.ReplyChain[1:]
		if !strings.Contains(out.Notice, "Earlier replies") {
			out.Notice = strings.TrimSpace(out.Notice + " Earlier replies are available in the paged discussion history.")
		}
	}
	// The tool transports this object as an escaped JSON string. Leave room for both encodings.
	const pageBytes = 24 << 10
	first := len(rows)
	for i := len(rows) - 1; i >= 0; i-- {
		out.Messages = rows[i:]
		if historyJSONSize(out) > pageBytes {
			if i == len(rows)-1 {
				if historyJSONSize(TeammateHistoryPage{Messages: rows[i:]}) <= pageBytes {
					separateStarter()
					out.ReplyChain = nil
					if historyJSONSize(out) <= pageBytes {
						first = i
						continue
					}
				}
				out.Messages = nil
				separateStarter()
				out.ReplyChain = nil
				route.MessageID = rows[i].ID
				cursor.Before = rows[i].ID
				if err = t.recordParentLow(d, q.Source, cursor.Before); err != nil {
					return out, err
				}
				return t.historyFragment(ctx, route, out, cursor)
			}
			break
		}
		first = i
	}
	out.Messages = rows[first:]
	if first > 0 && first < len(rows) {
		out.LowWater = rows[first].ID
		more = true
	}
	if out.LowWater != "" && more {
		cursor.Before = out.LowWater
		out.Cursor = encodeTeammateCursor(t.cursorKey, cursor)
	}
	return out, t.recordParentLow(d, q.Source, out.LowWater)
}

func (t *teammateConnector) recordParentLow(d TeammateDiscussion, source, low string) error {
	if source != "parent" || low == "" {
		return nil
	}
	return t.service.Store.teammateChangeBinding(d, func(b *teammateBinding) {
		if b.ParentLow == "" || snowflakeLess(low, b.ParentLow) {
			b.ParentLow = low
		}
	})
}

func historyJSONSize(value TeammateHistoryPage) int {
	raw, _ := json.Marshal(value)
	return len(raw)
}

func (t *teammateConnector) historyFragment(ctx context.Context, route Route, out TeammateHistoryPage, cursor teammateHistoryCursor) (TeammateHistoryPage, error) {
	out.HighWater, out.LowWater = cursor.Upper, cursor.Before
	out.FragmentMessageID = cursor.Before
	m, err := t.client.teammateMessage(ctx, route)
	if err != nil && !teammateAPIStatus(err, http.StatusNotFound) {
		return out, err
	}
	if m != nil {
		raw, _ := json.Marshal(teammateEvidence(m))
		revision := digest(string(raw))
		if (cursor.PartHash == "" || cursor.PartHash == revision) && cursor.Offset < len(raw) {
			end := min(cursor.Offset+8192, len(raw))
			for end < len(raw) && !utf8.RuneStart(raw[end]) {
				end--
			}
			out.EvidenceFragment = string(raw[cursor.Offset:end])
			if end < len(raw) {
				cursor.Offset, cursor.PartHash = end, revision
				out.Cursor = encodeTeammateCursor(t.cursorKey, cursor)
				return out, nil
			}
		} else {
			out.Notice = strings.TrimSpace(out.Notice + " This evidence changed between reads.")
		}
	} else {
		out.Notice = strings.TrimSpace(out.Notice + " This evidence is no longer available.")
	}
	if cursor.Source != "starter" {
		cursor.Offset, cursor.PartHash = 0, ""
		out.Cursor = encodeTeammateCursor(t.cursorKey, cursor)
	}
	return out, nil
}

func (t *teammateConnector) replyChain(ctx context.Context, route Route, m *discordgo.Message, floor string) (chain []*discordgo.Message, missing bool, err error) {
	for range 20 {
		reply := teammateReply(m)
		if reply == "" || !snowflakeLess(reply, m.ID) || floor != "" && snowflakeLess(reply, floor) {
			break
		}
		m, err = t.client.teammateMessage(ctx, Route{GuildID: route.GuildID, ChannelID: route.ChannelID, MessageID: reply})
		if teammateAPIStatus(err, http.StatusNotFound) {
			return chain, true, nil
		}
		if err != nil {
			return nil, false, err
		}
		chain = append(chain, m)
	}
	return chain, false, nil
}

func (t *teammateConnector) repair(ctx context.Context, b teammateBinding, epoch uint64) error {
	personal := b.Opened && isPersonalDiscussion(t.service.Config, b.TeammateDiscussion)
	if epoch == 0 || b.RepairTargetEpoch != epoch ||
		personal && (!personalRepairPending(b, epoch) || b.PersonalScanEpoch != epoch) ||
		!personal && b.RepairEpoch == epoch {
		return nil
	}
	_, _, bits, err := teammateChannelScope(ctx, t.client, t.service.Config, b.Route, t.service.botUserID())
	if err != nil {
		if personal {
			return fmt.Errorf("%w: could not verify DM audience: %w", errPersonalHistoryRepair, err)
		}
		return err
	}
	if bits&discordgo.PermissionReadMessageHistory == 0 {
		if personal {
			return fmt.Errorf("%w: %w", errPersonalHistoryRepair, errPersonalHistoryPermission)
		}
		return errors.New("discord: cannot repair thread context without history permission")
	}
	if personal {
		return t.repairPersonalHistory(ctx, b, epoch)
	}
	before := b.RepairAfter
	for range 4 {
		rows, e := t.client.teammateMessages(ctx, b.Route, before, 100)
		if e != nil {
			return e
		}
		upper, low := b.RepairUpper, ""
		for _, m := range rows {
			if before == "" && snowflakeLess(upper, m.ID) {
				upper = m.ID
			}
			if low == "" || snowflakeLess(m.ID, low) {
				low = m.ID
			}
		}
		done := len(rows) < 100 || !snowflakeLess(b.RepairLower, low)
		err = t.service.Store.teammateChangeBinding(b.TeammateDiscussion, func(current *teammateBinding) {
			if current.RepairTargetEpoch != epoch {
				return
			}
			current.RepairUpper = upper
			current.RepairAfter = low
			if done {
				current.RepairEpoch = epoch
				current.RepairAfter, current.RepairUpper, current.RepairLower = "", "", ""
				if snowflakeLess(current.Boundary, upper) {
					current.Boundary = upper
				}
			}
		})
		if err != nil || done {
			return err
		}
		b.RepairUpper, before = upper, low
	}
	return nil
}

func (t *teammateConnector) verifyAdmission(ctx context.Context, d TeammateDiscussion, q TeammateHistoryQuery) error {
	if q.Cursor != "" || q.Anchor != "" || q.AdmissionID == "" || q.Revision == "" || !snowflake(q.MessageID) || !snowflake(q.AuthorID) {
		return ErrInvalid
	}
	var record teammateCandidate
	var guard teammateIngressGuard
	err := t.service.Store.db.View(func(tx *bolt.Tx) error {
		if q.Source == "admission" {
			if err := checkPersonalRepairGate(tx, t.service.Config, d); err != nil {
				return err
			}
		}
		key := teammateKey(d.InstallationID, q.MessageID)
		err := teammateGet(tx.Bucket([]byte(teammateIngressBucket)), key, &record)
		if !errors.Is(err, ErrNotFound) || q.Source != "grant" {
			return err
		}
		return teammateGet(tx.Bucket([]byte(teammateIngressGuardBucket)), key, &guard)
	})
	if errors.Is(err, ErrNotFound) {
		return ErrTeammateSourceChanged
	}
	if err != nil {
		return err
	}
	if a := record.Admission; a != nil {
		if a.Discussion.ID != d.ID || a.Discussion.Route != d.Route || a.OperationID != q.AdmissionID ||
			a.Revision != q.Revision || a.Author.ID != q.AuthorID || record.Deleted ||
			record.State != StateSending && record.State != teammateStateAccepted && record.State != StateSent {
			return ErrTeammateSourceChanged
		}
		if q.Source == "grant" && record.State != StateSent {
			phase, err := t.manager.AdmissionState(ctx, *a)
			if err != nil {
				return err
			}
			if phase != TeammateAdmissionCutover {
				return ErrTeammateSourceChanged
			}
		}
		if q.Source == "admission" && record.State == StateSent {
			return ErrTeammateSourceChanged
		}
	} else {
		if q.Source != "grant" || guard.DiscussionID != d.ID || guard.Disposition != StateSent || guard.OperationID != q.AdmissionID ||
			guard.Revision != q.Revision || guard.ActorID != q.AuthorID || guard.LastChange == "deleted" {
			return ErrTeammateSourceChanged
		}
		record.Admission = &TeammateAdmission{Route: guard.Route, Author: Actor{ID: guard.ActorID}, RequireMention: guard.RequireMention}
	}
	route := record.Admission.Route
	if route.MessageID != q.MessageID || route.GuildID != d.Route.GuildID ||
		route.ChannelID != d.Route.ChannelID && route != d.Starter {
		return ErrTeammateSourceChanged
	}
	if err = t.checkDiscussion(ctx, d); err != nil {
		return err
	}
	m, err := t.readAdmissionSource(ctx, record)
	if err != nil {
		return err
	}
	if teammateRevision(m) != q.Revision {
		return ErrTeammateSourceChanged
	}
	return nil
}
