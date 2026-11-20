package discord

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func callRosterConfig(mode string) Config {
	return Config{
		OwnerID: "1", ApplicationID: "99",
		Calls: &DispatcherCallsConfig{Mode: mode, GuildID: "10", RoomID: "20", DiscussionID: "30", UserIDs: []string{"1", "2", "11"}},
	}
}

const callRosterReady = `{"session_id":"gateway-one","user":{"id":"99","bot":true}}`

func callRosterState(user, channel string, member, audio string) string {
	return fmt.Sprintf(`{"user_id":%q,"channel_id":%s,"session_id":"voice-%s"%s,%s}`, user, channel, user, member, audio)
}

func callRosterVoice(user, channel string) string {
	channelID := "null"
	if channel != "" {
		channelID = fmt.Sprintf("%q", channel)
	}
	return callRosterState(user, channelID, fmt.Sprintf(`,"member":{"user":{"id":%q}}`, user), callRosterAudio)
}

const callRosterAudio = `"mute":false,"deaf":false,"self_mute":false,"self_deaf":false,"suppress":false`

func callRosterGuild(states ...string) string {
	return `{"id":"10","channels":[{"id":"20","type":2,"parent_id":"21"}],"threads":[{"id":"30","type":12,"parent_id":"31"}],"voice_states":[` + strings.Join(states, ",") + `]}`
}

func callRosterDelta(state string) string {
	return `{"guild_id":"10",` + state[1:]
}

func applyCallRoster(t *testing.T, r *callRoster, cfg Config, seq int64, event, data string, want bool) {
	t.Helper()
	f := gatewayFrame{Type: event, Data: json.RawMessage(data)}
	if seq != 0 {
		f.Sequence = &seq
	}
	if got := r.apply(cfg, f); got != want {
		t.Fatalf("%s at %d changed = %t, want %t", event, seq, got, want)
	}
}

func checkCallRoster(t *testing.T, r *callRoster, cfg Config, want ...string) {
	t.Helper()
	got, ok := r.speakers(cfg)
	if ok != (len(want) > 0) || !slices.Equal(got, want) {
		t.Fatalf("speakers = %v, %t, want %v, %t", got, ok, want, len(want) > 0)
	}
}

func TestCallRosterPersonal(t *testing.T) {
	cfg := callRosterConfig("personal")
	var r callRoster
	owner := callRosterVoice("1", "20")
	self := callRosterState("99", `"20"`, "", callRosterAudio)

	applyCallRoster(t, &r, cfg, 1, "GUILD_CREATE", callRosterGuild(owner), false)
	applyCallRoster(t, &r, cfg, 2, "READY", callRosterReady, false)
	applyCallRoster(t, &r, cfg, 3, "VOICE_STATE_UPDATE", callRosterDelta(owner), false)
	checkCallRoster(t, &r, cfg)
	applyCallRoster(t, &r, cfg, 4, "GUILD_CREATE", callRosterGuild(owner), true)
	checkCallRoster(t, &r, cfg, "1")

	applyCallRoster(t, &r, cfg, 5, "VOICE_STATE_UPDATE", callRosterDelta(self), false)
	applyCallRoster(t, &r, cfg, 6, "VOICE_STATE_UPDATE", callRosterDelta(self), false)
	checkCallRoster(t, &r, cfg, "1")
	deaf := strings.Replace(self, `"deaf":false`, `"deaf":true`, 1)
	applyCallRoster(t, &r, cfg, 7, "VOICE_STATE_UPDATE", callRosterDelta(deaf), true)
	checkCallRoster(t, &r, cfg)
	applyCallRoster(t, &r, cfg, 8, "VOICE_STATE_UPDATE", callRosterDelta(self), true)
	checkCallRoster(t, &r, cfg, "1")
	applyCallRoster(t, &r, cfg, 9, "VOICE_STATE_UPDATE", callRosterDelta(strings.Replace(self, `"20"`, `"22"`, 1)), true)
	checkCallRoster(t, &r, cfg)
	applyCallRoster(t, &r, cfg, 10, "VOICE_STATE_UPDATE", callRosterDelta(callRosterVoice("99", "")), true)
	checkCallRoster(t, &r, cfg, "1")
	applyCallRoster(t, &r, cfg, 11, "VOICE_STATE_UPDATE", callRosterDelta(self), false)
	applyCallRoster(t, &r, cfg, 12, "VOICE_STATE_UPDATE", callRosterDelta(callRosterVoice("99", "")), true)

	muted := strings.Replace(owner, `"self_mute":false`, `"self_mute":true`, 1)
	applyCallRoster(t, &r, cfg, 13, "VOICE_STATE_UPDATE", callRosterDelta(muted), true)
	checkCallRoster(t, &r, cfg)
	applyCallRoster(t, &r, cfg, 14, "VOICE_STATE_UPDATE", callRosterDelta(owner), true)
	applyCallRoster(t, &r, cfg, 15, "VOICE_STATE_UPDATE", callRosterDelta(callRosterVoice("2", "20")), true)
	checkCallRoster(t, &r, cfg)
	applyCallRoster(t, &r, cfg, 16, "VOICE_STATE_UPDATE", callRosterDelta(callRosterVoice("2", "22")), true)
	checkCallRoster(t, &r, cfg, "1")
	applyCallRoster(t, &r, cfg, 17, "VOICE_STATE_UPDATE", callRosterDelta(callRosterVoice("1", "")), true)
	checkCallRoster(t, &r, cfg)
	applyCallRoster(t, &r, cfg, 16, "VOICE_STATE_UPDATE", callRosterDelta(owner), false)
	checkCallRoster(t, &r, cfg)
}

func TestCallRosterTeamChecksEveryOccupant(t *testing.T) {
	cfg := callRosterConfig("team")
	var r callRoster
	applyCallRoster(t, &r, cfg, 1, "READY", callRosterReady, false)
	applyCallRoster(t, &r, cfg, 2, "GUILD_CREATE", callRosterGuild(callRosterVoice("11", "20"), callRosterVoice("2", "20")), true)
	checkCallRoster(t, &r, cfg, "2", "11")

	outsider := strings.Replace(callRosterVoice("3", "20"), `"self_mute":false`, `"self_mute":true`, 1)
	applyCallRoster(t, &r, cfg, 3, "VOICE_STATE_UPDATE", callRosterDelta(outsider), true)
	checkCallRoster(t, &r, cfg)
	applyCallRoster(t, &r, cfg, 4, "VOICE_STATE_UPDATE", callRosterDelta(callRosterVoice("3", "")), true)
	checkCallRoster(t, &r, cfg, "2", "11")

	unknown := callRosterState("1", `"20"`, "", callRosterAudio)
	applyCallRoster(t, &r, cfg, 5, "VOICE_STATE_UPDATE", callRosterDelta(unknown), true)
	checkCallRoster(t, &r, cfg)
	bot := callRosterState("1", `"20"`, `,"member":{"user":{"id":"1","bot":true}}`, callRosterAudio)
	applyCallRoster(t, &r, cfg, 6, "VOICE_STATE_UPDATE", callRosterDelta(bot), false)
	checkCallRoster(t, &r, cfg)
	applyCallRoster(t, &r, cfg, 7, "VOICE_STATE_UPDATE", callRosterDelta(callRosterVoice("1", "")), true)
	applyCallRoster(t, &r, cfg, 8, "VOICE_STATE_UPDATE", callRosterDelta(callRosterVoice("1", "20")), true)
	checkCallRoster(t, &r, cfg, "1", "2", "11")
	applyCallRoster(t, &r, cfg, 9, "VOICE_STATE_UPDATE", callRosterDelta(unknown), false)
	checkCallRoster(t, &r, cfg, "1", "2", "11")

	muted := strings.Replace(callRosterVoice("1", "20"), `"mute":false`, `"mute":true`, 1)
	applyCallRoster(t, &r, cfg, 10, "VOICE_STATE_UPDATE", callRosterDelta(muted), true)
	checkCallRoster(t, &r, cfg)
}

func TestCallRosterAnchorInvalidation(t *testing.T) {
	owner := callRosterVoice("1", "20")
	guild := callRosterGuild(owner)
	tests := []struct {
		name, event, data string
		seq               int64
	}{
		{"unavailable", "GUILD_CREATE", `{"id":"10","unavailable":true}`, 3},
		{"deleted guild", "GUILD_DELETE", `{"id":"10"}`, 3},
		{"missing roster", "GUILD_CREATE", `{"id":"10","channels":[{"id":"20","type":2}]}`, 3},
		{"null roster", "GUILD_CREATE", strings.Replace(guild, `[`+owner+`]`, "null", 1), 3},
		{"duplicate occupants", "GUILD_CREATE", callRosterGuild(owner, owner), 3},
		{"missing channels", "GUILD_CREATE", `{"id":"10","voice_states":[]}`, 3},
		{"stage", "GUILD_CREATE", strings.Replace(guild, `"type":2`, `"type":13`, 1), 3},
		{"unavailable update", "GUILD_UPDATE", `{"id":"10","unavailable":true}`, 3},
		{"room deleted", "CHANNEL_DELETE", `{"guild_id":"10","id":"20"}`, 3},
		{"missing channel", "VOICE_STATE_UPDATE", `{"guild_id":"10","user_id":"1"}`, 3},
		{"missing flags", "VOICE_STATE_UPDATE", callRosterDelta(callRosterState("1", `"20"`, "", `"mute":false`)), 3},
		{"mismatched member", "VOICE_STATE_UPDATE", callRosterDelta(strings.Replace(owner, `"id":"1"`, `"id":"2"`, 1)), 3},
		{"missing guild", "VOICE_STATE_UPDATE", owner, 3},
		{"missing sequence", "VOICE_STATE_UPDATE", callRosterDelta(owner), 0},
		{"oversize", "GUILD_CREATE", guild + strings.Repeat(" ", callRosterMaxPayload), 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := callRosterConfig("team")
			var r callRoster
			applyCallRoster(t, &r, cfg, 1, "READY", callRosterReady, false)
			applyCallRoster(t, &r, cfg, 2, "GUILD_CREATE", guild, true)
			checkCallRoster(t, &r, cfg, "1")
			applyCallRoster(t, &r, cfg, tt.seq, tt.event, tt.data, true)
			checkCallRoster(t, &r, cfg)
			applyCallRoster(t, &r, cfg, 4, "RESUMED", `{}`, false)
			applyCallRoster(t, &r, cfg, 5, "VOICE_STATE_UPDATE", callRosterDelta(owner), false)
			applyCallRoster(t, &r, cfg, 2, "GUILD_CREATE", guild, false)
			checkCallRoster(t, &r, cfg)
			applyCallRoster(t, &r, cfg, 6, "GUILD_CREATE", guild, true)
			checkCallRoster(t, &r, cfg, "1")
		})
	}
}

func TestCallRosterPermissionChangesPreserveOccupancy(t *testing.T) {
	cfg := callRosterConfig("team")
	var r callRoster
	owner := callRosterVoice("1", "20")
	applyCallRoster(t, &r, cfg, 1, "READY", callRosterReady, false)
	applyCallRoster(t, &r, cfg, 2, "GUILD_CREATE", callRosterGuild(owner), true)
	for i, event := range []struct{ kind, data string }{
		{"GUILD_UPDATE", `{"id":"10"}`},
		{"CHANNEL_UPDATE", `{"guild_id":"10","id":"20"}`},
		{"CHANNEL_DELETE", `{"guild_id":"10","id":"21"}`},
		{"CHANNEL_UPDATE", `{"guild_id":"10","id":"31"}`},
		{"GUILD_ROLE_UPDATE", `{"guild_id":"10","role":{"id":"10"}}`},
		{"GUILD_MEMBER_UPDATE", `{"guild_id":"10","user":{"id":"1"}}`},
		{"GUILD_MEMBER_REMOVE", `{"guild_id":"10","user":{"id":"99"}}`},
		{"THREAD_MEMBERS_UPDATE", `{"guild_id":"10","id":"30","removed_member_ids":["1"]}`},
	} {
		applyCallRoster(t, &r, cfg, int64(3+i), event.kind, event.data, true)
		checkCallRoster(t, &r, cfg, "1")
		if r.occupancyAt != 2 {
			t.Fatalf("%s changed the voice occupancy at %d", event.kind, r.occupancyAt)
		}
	}
	applyCallRoster(t, &r, cfg, 11, "VOICE_STATE_UPDATE", callRosterDelta(callRosterVoice("1", "")), true)
	checkCallRoster(t, &r, cfg)
	applyCallRoster(t, &r, cfg, 12, "VOICE_STATE_UPDATE", callRosterDelta(owner), true)
	checkCallRoster(t, &r, cfg, "1")
	if r.occupancyAt != 12 {
		t.Fatal("a leave and return must invalidate the previous occupancy")
	}
}

func TestCallRosterGatewaySessionAndScope(t *testing.T) {
	cfg := callRosterConfig("team")
	var r callRoster
	guild := callRosterGuild(callRosterState("1", `"20"`, "", callRosterAudio))
	guild = strings.Replace(guild, `"channels":`, `"members":[{"user":{"id":"1"}}],"channels":`, 1)

	badReady := strings.Replace(callRosterReady, `"99"`, `"98"`, 1)
	applyCallRoster(t, &r, cfg, 1, "READY", badReady, false)
	applyCallRoster(t, &r, cfg, 2, "GUILD_CREATE", guild, false)
	applyCallRoster(t, &r, cfg, 1, "READY", callRosterReady, false)
	applyCallRoster(t, &r, cfg, 2, "GUILD_CREATE", guild, true)
	applyCallRoster(t, &r, cfg, 1, "READY", callRosterReady, false)
	applyCallRoster(t, &r, cfg, 3, "PRESENCE_UPDATE", `{"guild_id":"10","user":{"id":"1"}}`, false)
	applyCallRoster(t, &r, cfg, 4, "GUILD_DELETE", `{"id":"11","unavailable":true}`, false)
	applyCallRoster(t, &r, cfg, 5, "CHANNEL_UPDATE", `{"guild_id":"10","id":"22"}`, false)
	applyCallRoster(t, &r, cfg, 6, "THREAD_DELETE", `{"guild_id":"10","id":"33"}`, false)
	applyCallRoster(t, &r, cfg, 7, "GUILD_MEMBER_REMOVE", `{"guild_id":"10","user":{"id":"4"}}`, false)
	checkCallRoster(t, &r, cfg, "1")
	r.reset()
	applyCallRoster(t, &r, cfg, 2, "GUILD_CREATE", guild, false)
	applyCallRoster(t, &r, cfg, 8, "RESUMED", `{}`, false)
	checkCallRoster(t, &r, cfg)
	applyCallRoster(t, &r, cfg, 9, "GUILD_CREATE", guild, true)
	checkCallRoster(t, &r, cfg, "1")
	nextReady := strings.Replace(callRosterReady, "gateway-one", "gateway-two", 1)
	applyCallRoster(t, &r, cfg, 1, "READY", nextReady, true)
	checkCallRoster(t, &r, cfg)
	applyCallRoster(t, &r, cfg, 2, "GUILD_CREATE", callRosterGuild(), true)
	checkCallRoster(t, &r, cfg)
	applyCallRoster(t, &r, cfg, 3, "VOICE_STATE_UPDATE", callRosterDelta(callRosterVoice("1", "20")), true)
	checkCallRoster(t, &r, cfg, "1")
}
