package schema

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestLoginUsageWirePreservesUnknownAndZeroRemaining(t *testing.T) {
	raw, err := json.Marshal(LoginView{ID: "saved", Active: true})
	if err != nil || strings.Contains(string(raw), "weekly") || strings.Contains(string(raw), "five_hour") {
		t.Fatalf("unknown readings became wire values: %s %v", raw, err)
	}
	now := time.Now().UTC()
	want := LoginView{Weekly: &LoginUsageWindow{RemainingPercent: 0, ObservedAt: now, ResetsAt: now.Add(time.Hour)}}
	raw, err = json.Marshal(want)
	if err != nil || !strings.Contains(string(raw), `"remaining_percent":0`) {
		t.Fatalf("zero remaining was omitted: %s %v", raw, err)
	}
	var got LoginView
	if err = json.Unmarshal(raw, &got); err != nil || got.Weekly == nil || got.Weekly.RemainingPercent != 0 || !got.Weekly.Fresh(now) {
		t.Fatalf("reading changed on wire: %+v %v", got, err)
	}
	var legacy struct {
		ID string `json:"id"`
	}
	if err = json.Unmarshal(raw, &legacy); err != nil {
		t.Fatal("additive reading broke legacy decoder", err)
	}
}
