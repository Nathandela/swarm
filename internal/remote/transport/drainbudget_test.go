package transport_test

// Section 6.0's INBOUND DRAIN BUDGET, rescued from the dead package's test file (ADR-007 B98).
//
// WHY THIS FILE EXISTS. These assertions used to live in s6b_input_test.go, whose other tests
// drive transport.Session -- zero production constructions (B94) -- and which B98 step 4
// deletes. But MaxDrainReadsPerSec and MaxDrainAcksPerSec are NOT dead: they configure
// NewDrainPacer and NewAckBatcher, which internal/remotegw/command_loop.go:309,317 uses in
// production on the gateway's inbound hop. Deleting the file wholesale would have deleted the
// only fence on a LIVE budget -- the third instance of B98's own finding, found while acting
// on it.
//
// WHAT SURVIVED AND WHAT DID NOT. The numeric assertions and the arithmetic against the
// relay's OpsPerMin window survive verbatim, because their subject is the constants. The
// tests that drove Session.Follow around them do not: their subject was the dead type, and
// re-homing them would be re-pointing a fence at a convenient subject rather than the one the
// requirement names.
//
// WHAT IT DOES NOT COVER. DrainPacer's and AckBatcher's BEHAVIOUR is still unfenced -- nothing
// in the tree constructs either and asserts what they do, which B98 records as an open gap.
// This file pins the numbers they are built from, not the pacing they produce.

import (
	"testing"

	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/remote/transport"
)

// TestS6B_DrainBudgetArithmeticFitsTheRelayOpsWindow pins section 6.0's per-hop inbound
// ceilings and checks they still fit inside the relay quota they are chosen against. A budget
// that quietly exceeds OpsPerMin spends the allowance of the live tail it is trying to receive.
func TestS6B_DrainBudgetArithmeticFitsTheRelayOpsWindow(t *testing.T) {
	if transport.MaxDrainReadsPerSec != 3 {
		t.Fatalf("MaxDrainReadsPerSec = %d, want 3 (§6.0 inbound drain rate, each hop)", transport.MaxDrainReadsPerSec)
	}
	if transport.MaxDrainAcksPerSec != 1 {
		t.Fatalf("MaxDrainAcksPerSec = %d, want 1 (§6.0: batched acks <= 1/s per routing id)", transport.MaxDrainAcksPerSec)
	}

	// Both mailbox_read and mailbox_ack meter against the relay's OpsPerMin window (unlike
	// mailbox_append, which has its own). A drain at the ceiling must leave room for the
	// commands and acks the same connection carries.
	budgetPerMin := (transport.MaxDrainReadsPerSec + transport.MaxDrainAcksPerSec) * 60
	quota := relay.DefaultConfig().Quotas.OpsPerMin
	if budgetPerMin >= quota {
		t.Fatalf("the drain budget is %d ops/min against the relay's OpsPerMin of %d: a drain at "+
			"§6.0's ceiling would exhaust the window it shares with every other control op, so the "+
			"hop dies with codeQuotaExceeded exactly when it is busiest", budgetPerMin, quota)
	}
}
