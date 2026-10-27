package main

import (
	"encoding/json"
	"io"
	"testing"
)

func TestShimLaunchKeepsNativeAgyBootstrap(t *testing.T) {
	var launch shimLaunchConfig
	if err := json.Unmarshal([]byte(`{"agy_input":{"session_id":"own-session","executable":"/native/agy","version":"1.4.0"}}`), &launch); err != nil {
		t.Fatal(err)
	}
	got := shimConfigFromLaunch(launch).AgyInput
	if got == nil || got.SessionID != "own-session" || got.Executable != "/native/agy" {
		t.Fatalf("native bootstrap config lost: %+v", got)
	}
}

func TestNativeInputHelperRefusesWrongRoleAndMissingOwnContext(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"register-agy", "relative/agy-input.json"}, {"register-agy", "/private/tmp/agy-input.json"}} {
		if code := runNativeInput(args, io.Discard); code == 0 {
			t.Fatalf("invalid helper context accepted: %v", args)
		}
	}
}
