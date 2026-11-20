package discord

import (
	"encoding/json"
	"sort"

	"github.com/bwmarrin/discordgo"
)

const callRosterMaxPayload = 1 << 20

// callRoster is owned by the coordinator's mutex. An anchor covers exactly one
// guild and ordinary voice room. Gateway lifecycle and discussion access are
// additional gates owned by the coordinator.
type callRoster struct {
	botID            string
	sessionID        string
	sequence         int64
	guildID          string
	roomID           string
	discussionID     string
	roomParent       string
	discussionParent string
	occupants        map[string]callRosterOccupant
	selfChannel      string
	self             callRosterOccupant
}

type callRosterUser struct {
	ID     string `json:"id"`
	Bot    bool   `json:"bot"`
	System bool   `json:"system"`
}

type callRosterMember struct {
	User *callRosterUser `json:"user"`
}

type callRosterOccupant struct {
	human   bool
	blocked bool
	session string
}

type callRosterVoiceState struct {
	GuildID   string            `json:"guild_id"`
	UserID    string            `json:"user_id"`
	ChannelID json.RawMessage   `json:"channel_id"`
	Member    *callRosterMember `json:"member"`
	SessionID string            `json:"session_id"`
	Mute      *bool             `json:"mute"`
	Deaf      *bool             `json:"deaf"`
	SelfMute  *bool             `json:"self_mute"`
	SelfDeaf  *bool             `json:"self_deaf"`
	Suppress  *bool             `json:"suppress"`
}

func (v callRosterVoiceState) channel() (string, bool) {
	var id string
	if !snowflake(v.UserID) || json.Unmarshal(v.ChannelID, &id) != nil {
		return "", false
	}
	return id, snowflake(id) || string(v.ChannelID) == "null"
}

func (v callRosterVoiceState) occupant(user *callRosterUser, wasHuman bool) (callRosterOccupant, bool) {
	if v.Member != nil {
		if v.Member.User == nil || v.Member.User.ID != v.UserID {
			return callRosterOccupant{}, false
		}
		if user != nil && *user != *v.Member.User {
			return callRosterOccupant{}, false
		}
		user = v.Member.User
	}
	if user != nil {
		wasHuman = user.ID == v.UserID && !user.Bot && !user.System
	}
	if v.Mute == nil || v.Deaf == nil || v.SelfMute == nil || v.SelfDeaf == nil || v.Suppress == nil {
		return callRosterOccupant{}, false
	}
	return callRosterOccupant{
		human: wasHuman, blocked: *v.Mute || *v.Deaf || *v.SelfMute || *v.SelfDeaf || *v.Suppress,
		session: v.SessionID,
	}, true
}

// reset drops the anchor but keeps the authenticated Gateway session and its
// sequence watermark. RESUMED and voice deltas alone cannot restore the anchor.
func (r *callRoster) reset() {
	*r = callRoster{botID: r.botID, sessionID: r.sessionID, sequence: r.sequence}
}

func (r *callRoster) invalidate() bool {
	changed := r.occupants != nil
	r.reset()
	return changed
}

// apply reports a change that can retire the current call generation. A healthy
// self join is part of establishing that generation, so it does not report one.
func (r *callRoster) apply(cfg Config, f gatewayFrame) bool {
	calls := cfg.Calls
	if calls == nil {
		return r.invalidate()
	}
	if f.Op != 0 {
		return false
	}
	if f.Type == "READY" {
		var ready struct {
			SessionID string         `json:"session_id"`
			User      callRosterUser `json:"user"`
		}
		if len(f.Data) > callRosterMaxPayload || json.Unmarshal(f.Data, &ready) != nil || ready.SessionID == "" ||
			!ready.User.Bot || ready.User.System || !snowflake(ready.User.ID) || ready.User.ID != cfg.ApplicationID ||
			f.Sequence == nil || *f.Sequence <= 0 {
			changed := r.occupants != nil
			*r = callRoster{}
			return changed
		}
		if ready.SessionID == r.sessionID {
			return false
		}
		changed := r.occupants != nil
		*r = callRoster{botID: ready.User.ID, sessionID: ready.SessionID, sequence: *f.Sequence}
		return changed
	}
	if f.Sequence != nil && *f.Sequence > 0 && *f.Sequence <= r.sequence {
		return false
	}
	sequenced := f.Sequence != nil && *f.Sequence > 0
	if sequenced {
		r.sequence = *f.Sequence
	}

	switch f.Type {
	case "GUILD_CREATE", "GUILD_DELETE", "GUILD_UPDATE", "VOICE_STATE_UPDATE",
		"CHANNEL_UPDATE", "CHANNEL_DELETE", "GUILD_ROLE_CREATE", "GUILD_ROLE_UPDATE", "GUILD_ROLE_DELETE",
		"GUILD_MEMBER_ADD", "GUILD_MEMBER_UPDATE", "GUILD_MEMBER_REMOVE",
		"THREAD_CREATE", "THREAD_UPDATE", "THREAD_DELETE", "THREAD_MEMBER_UPDATE", "THREAD_MEMBERS_UPDATE", "THREAD_LIST_SYNC":
	default:
		return false
	}

	var scope struct {
		ID         string         `json:"id"`
		GuildID    string         `json:"guild_id"`
		User       callRosterUser `json:"user"`
		ChannelIDs []string       `json:"channel_ids"`
	}
	if len(f.Data) > callRosterMaxPayload || json.Unmarshal(f.Data, &scope) != nil {
		return r.invalidate()
	}
	guildID := scope.GuildID
	switch f.Type {
	case "GUILD_CREATE", "GUILD_DELETE", "GUILD_UPDATE":
		guildID = scope.ID
	}
	if snowflake(guildID) && guildID != calls.GuildID {
		return false
	}
	if f.Type == "CHANNEL_UPDATE" || f.Type == "CHANNEL_DELETE" {
		if snowflake(scope.ID) && scope.ID != calls.RoomID && scope.ID != r.roomParent &&
			(calls.Mode != "team" || r.discussionParent != "" && scope.ID != r.discussionParent && scope.ID != calls.DiscussionID) {
			return false
		}
	}
	switch f.Type {
	case "THREAD_CREATE", "THREAD_UPDATE", "THREAD_DELETE", "THREAD_MEMBER_UPDATE", "THREAD_MEMBERS_UPDATE":
		if calls.Mode != "team" || snowflake(scope.ID) && scope.ID != calls.DiscussionID {
			return false
		}
	case "THREAD_LIST_SYNC":
		if calls.Mode != "team" || r.discussionParent != "" && scope.ChannelIDs != nil && !containsID(scope.ChannelIDs, r.discussionParent) {
			return false
		}
	case "GUILD_MEMBER_ADD", "GUILD_MEMBER_UPDATE", "GUILD_MEMBER_REMOVE":
		if snowflake(scope.User.ID) && scope.User.ID != r.botID && scope.User.ID != cfg.OwnerID {
			if _, present := r.occupants[scope.User.ID]; !present {
				return false
			}
		}
	}

	if !sequenced || !snowflake(guildID) {
		return r.invalidate()
	}
	switch f.Type {
	case "GUILD_CREATE":
		return r.anchor(calls, f.Data)
	case "VOICE_STATE_UPDATE":
		if r.matches(calls) {
			return r.update(f.Data)
		}
		return r.invalidate()
	default:
		return r.invalidate()
	}
}

type callRosterChannel struct {
	ID       string                 `json:"id"`
	GuildID  string                 `json:"guild_id"`
	ParentID string                 `json:"parent_id"`
	Type     *discordgo.ChannelType `json:"type"`
}

func (r *callRoster) anchor(calls *DispatcherCallsConfig, raw json.RawMessage) bool {
	changed := r.invalidate()
	var guild struct {
		Unavailable bool                   `json:"unavailable"`
		Channels    []callRosterChannel    `json:"channels"`
		Threads     []callRosterChannel    `json:"threads"`
		VoiceStates []callRosterVoiceState `json:"voice_states"`
		Members     []callRosterMember     `json:"members"`
	}
	if !snowflake(r.botID) || !snowflake(calls.RoomID) || json.Unmarshal(raw, &guild) != nil ||
		guild.Unavailable || guild.Channels == nil || guild.VoiceStates == nil {
		return changed
	}
	var room *callRosterChannel
	for i := range guild.Channels {
		channel := &guild.Channels[i]
		if channel.ID != calls.RoomID {
			continue
		}
		if room != nil || channel.Type == nil || *channel.Type != discordgo.ChannelTypeGuildVoice ||
			channel.GuildID != "" && channel.GuildID != calls.GuildID ||
			channel.ParentID != "" && !snowflake(channel.ParentID) {
			return changed
		}
		room = channel
	}
	if room == nil {
		return changed
	}
	members := make(map[string]*callRosterUser, len(guild.Members))
	for _, m := range guild.Members {
		if m.User != nil && snowflake(m.User.ID) {
			if _, duplicate := members[m.User.ID]; duplicate {
				return changed
			}
			members[m.User.ID] = m.User
		}
	}
	occupants := make(map[string]callRosterOccupant)
	seen := make(map[string]bool)
	var self callRosterOccupant
	var selfChannel string
	for _, v := range guild.VoiceStates {
		channel, ok := v.channel()
		if !ok || channel == "" || seen[v.UserID] || v.GuildID != "" && v.GuildID != calls.GuildID {
			return changed
		}
		seen[v.UserID] = true
		if channel != calls.RoomID && v.UserID != r.botID {
			continue
		}
		occupant, ok := v.occupant(members[v.UserID], false)
		if !ok {
			return changed
		}
		if v.UserID == r.botID {
			self, selfChannel = occupant, channel
		} else {
			occupants[v.UserID] = occupant
		}
	}
	for _, thread := range guild.Threads {
		if thread.ID == calls.DiscussionID && snowflake(thread.ParentID) {
			r.discussionParent = thread.ParentID
			break
		}
	}
	r.guildID, r.roomID, r.discussionID = calls.GuildID, calls.RoomID, calls.DiscussionID
	r.roomParent = room.ParentID
	r.occupants, r.self, r.selfChannel = occupants, self, selfChannel
	return true
}

func (r *callRoster) matches(calls *DispatcherCallsConfig) bool {
	return r.occupants != nil && r.guildID == calls.GuildID && r.roomID == calls.RoomID && r.discussionID == calls.DiscussionID
}

func (r *callRoster) update(raw json.RawMessage) bool {
	var v callRosterVoiceState
	if json.Unmarshal(raw, &v) != nil {
		return r.invalidate()
	}
	channel, ok := v.channel()
	if !ok {
		return r.invalidate()
	}
	if v.UserID == r.botID {
		var self callRosterOccupant
		if channel != "" {
			self, ok = v.occupant(nil, false)
			if !ok {
				return r.invalidate()
			}
		}
		previous, previousChannel := r.self, r.selfChannel
		r.self, r.selfChannel = self, channel
		if previousChannel == "" && channel == r.roomID && !self.blocked {
			return false
		}
		return previousChannel != channel || previous != self
	}
	previous, present := r.occupants[v.UserID]
	if channel != r.roomID {
		delete(r.occupants, v.UserID)
		return present
	}
	occupant, ok := v.occupant(nil, previous.human)
	if !ok {
		return r.invalidate()
	}
	r.occupants[v.UserID] = occupant
	return !present || previous != occupant
}

func (r *callRoster) speakers(cfg Config) ([]string, bool) {
	calls := cfg.Calls
	if calls == nil || !r.matches(calls) || !snowflake(r.botID) || r.botID != cfg.ApplicationID || cfg.OwnerID == r.botID ||
		r.selfChannel != "" && (r.selfChannel != r.roomID || r.self.blocked) {
		return nil, false
	}
	switch calls.Mode {
	case "personal":
		owner, present := r.occupants[cfg.OwnerID]
		if !snowflake(cfg.OwnerID) || len(r.occupants) != 1 || !present || !owner.human || owner.blocked {
			return nil, false
		}
		return []string{cfg.OwnerID}, true
	case "team":
		speakers := make([]string, 0, len(r.occupants))
		for id, occupant := range r.occupants {
			// A muted listener still hears the call and cannot be omitted
			// from the audience the coordinator checks.
			if !occupant.human || occupant.blocked || !containsID(calls.UserIDs, id) {
				return nil, false
			}
			speakers = append(speakers, id)
		}
		sort.Slice(speakers, func(i, j int) bool {
			return snowflakeLess(speakers[i], speakers[j])
		})
		return speakers, len(speakers) > 0
	default:
		return nil, false
	}
}
