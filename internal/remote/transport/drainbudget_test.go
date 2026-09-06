package transport_test

// Relay-v2 SUBSCRIBE pushes deliveries; only ACK remains a metered inbound message.

import (
	"testing"

	"github.com/Nathandela/swarm/internal/remote/transport"
)

func TestS6B_AckBudgetIsOneMeteredOperationPerSecond(t *testing.T) {
	if transport.MaxDrainAcksPerSec != 1 {
		t.Fatalf("MaxDrainAcksPerSec = %d, want 1 metered ACK/s", transport.MaxDrainAcksPerSec)
	}
}
