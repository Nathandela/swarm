package main

import (
	"encoding/json"
	"testing"
)

func TestShimLaunchKeepsNativeClaudeInputOptIn(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		raw := []byte(`{"claude_input":false}`)
		if enabled {
			raw = []byte(`{"claude_input":true}`)
		}
		var launch shimLaunchConfig
		if err := json.Unmarshal(raw, &launch); err != nil {
			t.Fatal(err)
		}
		if got := shimConfigFromLaunch(launch).ClaudeInput; got != enabled {
			t.Fatalf("native input flag=%v, want %v", got, enabled)
		}
	}
}
