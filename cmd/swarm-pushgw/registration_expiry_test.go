package main

import (
	"testing"
	"time"
)

func TestProductionPlayVerdictFreshnessEndsBeforeRegistrationReplayExpiry(t *testing.T) {
	// POST /v1/installations retains a completed idempotency result for ten minutes.
	// A saved request must become impossible to attest again before that result expires,
	// or an uncertain client replay could mint a second installation after row expiry.
	const registrationReplayWindow = 10 * time.Minute
	if verdictLifetime := productionPlayVerdictMaxFutureSkew + productionPlayVerdictMaxAge; verdictLifetime >= registrationReplayWindow {
		t.Fatalf("production Play verdict lifetime %v must be shorter than registration replay window %v", verdictLifetime, registrationReplayWindow)
	}
}
