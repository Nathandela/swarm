package main

import (
	"github.com/Nathandela/swarm/internal/hermesinput"
	"testing"
)

func TestShimLaunchCarriesHermesSharedSessionConfiguration(t *testing.T) {
	want := &hermesinput.SessionConfig{Program: "hermes", ProviderVersion: "0.22.5", WorkerID: "worker", Profile: "custom"}
	got := shimConfigFromLaunch(shimLaunchConfig{HermesInput: want})
	if got.HermesInput != want {
		t.Fatalf("Hermes native config lost: %+v", got)
	}
}
