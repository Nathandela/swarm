package discord

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/coder/websocket/wsjson"
)

func TestCallRejoinFencesOldAudioAndDelayedAnswers(t *testing.T) {
	for _, route := range []string{"personal", "team"} {
		t.Run(route, func(t *testing.T) {
			f := newDispatcherCallFixture(t, route == "team")
			old, first := f.begin()
			if err := wsjson.Write(f.ctx, f.gateway.socket, map[string]int{"op": 7}); err != nil {
				t.Fatal(err)
			}
			f.gateway = callFixtureReceive(t, f.ctx, f.gateways, "replacement gateway")
			f.expectOpcode(6)
			f.expectConsent()
			f.send("RESUMED", json.RawMessage("null"))
			states := []string{callRosterVoice(f.speakers[0], "20")}
			if f.team {
				states = append(states, callRosterVoice(f.speakers[1], "20"))
			}
			f.send("GUILD_CREATE", json.RawMessage(callRosterGuild(states...)))
			if f.team {
				f.command("105", "30", "70", "join", 0)
				f.command("106", "30", "71", "consent", 0)
			}
			f.completeJoinHandshake()
			callFixtureReceive(t, f.ctx, f.ack, "replacement call ready")
			if err := old.cfg.Play(f.ctx, make([]byte, callMediaPCMBytes)); !errors.Is(err, ErrNotAuthorized) {
				t.Fatalf("retired speech callback returned %v", err)
			}
			f.audio(1)
			fresh := callFixtureReceive(t, f.ctx, f.native.accepted, "new call with same provider turn ID")
			if fresh.Voice.CallID == first.Voice.CallID || fresh.Voice.Generation <= first.Voice.Generation || fresh.Discussion != first.Discussion {
				t.Fatal("rejoin did not create a new generation in the original discussion")
			}
			f.audio(2)
			second := callFixtureReceive(t, f.ctx, f.native.accepted, "second turn after rejoin")
			fast := TeammatePublication{ID: "fast", AdmissionID: second.OperationID, Body: "second turn answered first", Sequence: 1}
			late := TeammatePublication{ID: "late", AdmissionID: first.OperationID, Body: "old call result", Sequence: 2}
			current := TeammatePublication{ID: "current", AdmissionID: fresh.OperationID, Body: "first turn answered last", Sequence: 3}
			f.publish(fast, late, late, current)
			for _, want := range []callFixtureRender{{"2", fast.Body}, {"1", current.Body}} {
				if got := callFixtureReceive(t, f.ctx, f.render, "current result with original turn ID"); got != want {
					t.Fatalf("rendered %+v, want %+v", got, want)
				}
			}
			for _, want := range []string{fast.Body, late.Body, current.Body} {
				if got := callFixtureReceive(t, f.ctx, f.written, "written result"); got != want {
					t.Fatalf("written result %q, want %q", got, want)
				}
			}
		})
	}
}

func TestConnectorRestartRetainsDiscussionAndFencesOldGeneration(t *testing.T) {
	for _, route := range []string{"personal", "team"} {
		t.Run(route, func(t *testing.T) {
			team := route == "team"
			dir := t.TempDir()
			var old TeammateAdmission
			var native *callFixtureNative
			if !t.Run("before_restart", func(t *testing.T) {
				f := openDispatcherCallFixture(t, team, dir, nil, nil)
				native = f.native
				_, old = f.begin()
			}) {
				return
			}
			// Reopen the same connector ledger while its native dispatcher survives.
			f := openDispatcherCallFixture(t, team, dir, native, nil)
			f.expectWaiting()
			f.expectConsent()
			states := []string{callRosterVoice(f.speakers[0], "20")}
			if team {
				states = append(states, callRosterVoice(f.speakers[1], "20"))
			}
			f.send("GUILD_CREATE", json.RawMessage(callRosterGuild(states...)))
			if team {
				f.command("105", "30", "70", "join", 0)
				f.command("106", "30", "71", "consent", 0)
			}
			f.completeJoinHandshake()
			callFixtureReceive(t, f.ctx, f.ack, "new connector media ready")
			f.audio(1)
			fresh := callFixtureReceive(t, f.ctx, native.accepted, "fresh input after restart")
			if fresh.Voice.CallID == old.Voice.CallID || fresh.Discussion != old.Discussion || fresh.OperationID == old.OperationID {
				t.Fatal("connector restart failed to separate the new call from the old admission")
			}
			f.publish(TeammatePublication{ID: "old", AdmissionID: old.OperationID, Body: "old receipt", Sequence: 1},
				TeammatePublication{ID: "fresh", AdmissionID: fresh.OperationID, Body: "current receipt", Sequence: 2})
			if got := callFixtureReceive(t, f.ctx, f.render, "render after restart"); got.body != "current receipt" {
				t.Fatalf("restarted connector spoke %q", got.body)
			}
			for _, want := range []string{"old receipt", "current receipt"} {
				if got := callFixtureReceive(t, f.ctx, f.written, "durable receipt"); got != want {
					t.Fatalf("restarted connector wrote %q, want %q", got, want)
				}
			}
		})
	}
}

func TestFailedPersonalRepairAndManualStopFencePendingRestart(t *testing.T) {
	dir := t.TempDir()
	var native *callFixtureNative
	var first TeammateAdmission
	if !t.Run("before_restart", func(t *testing.T) {
		f := openDispatcherCallFixture(t, false, dir, nil, nil)
		native = f.native
		_, first = f.begin()
	}) {
		return
	}
	gate := make(chan bool)
	f := openDispatcherCallFixture(t, false, dir, native, gate)
	f.send("GUILD_CREATE", json.RawMessage(callRosterGuild(callRosterVoice("70", "20"))))
	select {
	case gate <- false:
	case <-f.ctx.Done():
		t.Fatal("personal repair did not request history")
	}
	callFixtureReceive(t, f.ctx, f.written, "durable repair failure notice")
	if err := f.connector.checkCallSpeaker(f.ctx, first.Discussion, "70"); !errors.Is(err, errPersonalHistoryRepair) {
		t.Fatalf("failed repair opened the audience gate: %v", err)
	}
	f.calls.stopCall()
	close(gate)
	callFixtureReceive(t, f.ctx, f.calls.historyRepaired, "later successful history repair")
	f.expectWaiting()
	select {
	case <-f.gateway.writes:
		t.Fatal("old occupancy initiated a voice join after suspension")
	case <-native.accepted:
		t.Fatal("restart replayed a native admission")
	default:
	}
}
