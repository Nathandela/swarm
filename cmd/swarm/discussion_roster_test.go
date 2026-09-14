package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
)

func TestLSProjectsResumeHistory(t *testing.T) {
	rows := []protocol.SessionView{
		{ID: "ep/old", SupersededBy: "ep/current"},
		{ID: "ep/current"},
		{ID: "ep/archived", RosterHidden: true},
	}
	for _, args := range [][]string{nil, {"--json"}} {
		var stdout, stderr bytes.Buffer
		if code := runLS(args, newFakeAgentClient(rows...), &stdout, &stderr); code != 0 {
			t.Fatalf("ls failed: %s", stderr.String())
		}
		if len(args) == 0 {
			if strings.Contains(stdout.String(), "ep/old") || strings.Contains(stdout.String(), "ep/archived") || !strings.Contains(stdout.String(), "ep/current") {
				t.Fatalf("table includes history: %s", stdout.String())
			}
		} else {
			var got []protocol.SessionView
			if err := json.Unmarshal(stdout.Bytes(), &got); err != nil || len(got) != 1 || got[0].ID != "ep/current" {
				t.Fatalf("JSON includes history: %s", stdout.String())
			}
		}
	}
}
