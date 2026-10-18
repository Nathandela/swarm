package main

import (
	"bytes"
	"encoding/json"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
	"strings"
	"testing"
)

func TestOpenCodeObservationSurvivesOwnerReadCommands(t *testing.T) {
	row := view("local/native", "opencode", "native", status.GroupNeedsInput)
	row.OpenCode = &protocol.OpenCodeObservationView{Version: 1, RootID: "ses_native", Attention: true, Busy: true, BusyKnown: true}
	client := newFakeAgentClient(row)
	var out, errs bytes.Buffer
	check := func(code int) {
		t.Helper()
		var compact bytes.Buffer
		if err := json.Compact(&compact, out.Bytes()); err != nil {
			t.Fatalf("invalid native read JSON: code=%d err=%s: %v", code, errs.String(), err)
		}
		if code != 0 || !strings.Contains(compact.String(), `"root_id":"ses_native"`) || !strings.Contains(compact.String(), `"attention":true`) {
			t.Fatalf("native read lost observation: code=%d out=%s err=%s", code, out.String(), errs.String())
		}
		out.Reset()
		errs.Reset()
	}
	check(runLS([]string{"--json"}, client, &out, &errs))
	check(runWatch([]string{"local/native", "--until", string(status.GroupNeedsInput)}, client, &out, &errs))
	check(pilotRoster(client, &out, &errs))
}
