package phonecore

// Slice S11 REVIEW ROUND 4 -- the cost of putting the COMMAND sealers under the bucket lock.
//
// WHY IT IS MEASURED HERE AND NOT THROUGH THE LATENCY HARNESS. PB-NET-5's harness drives
// internal/phonesim and never enters mobile/commands.go, so it cannot see this change at all;
// quoting its number as evidence about the facade would be measuring a different program. The
// critical section is therefore benchmarked directly, exactly as round 3 did for the input
// half.
//
// WHAT THE INTERVAL ACTUALLY IS. relay.Conn.roundtrip already holds its own mutex across
// write-then-read, so appends on one connection were serialised before any of this. What the
// lock ADDS to that existing interval is the seq draw and the AEAD seal -- which is what this
// measures. The append itself is not new contention.
//
// This file contains NO implementation.

import (
	"testing"

	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// BenchmarkS11R4_CommandSealerCriticalSection is the interval mobile/commands.go
// sealSignedCommand and unsignedCommand newly hold the bucket lock across: allocate the seq
// from the shared Sequencer, then seal the command envelope under the epoch content key.
func BenchmarkS11R4_CommandSealerCriticalSection(b *testing.B) {
	key, err := crypto.NewEpochKeys()
	if err != nil {
		b.Fatalf("epoch keys: %v", err)
	}
	var seqr Sequencer
	cmd := schema.DeviceCommandAuth{
		Action:  schema.ActionTakeControl,
		Machine: "machine-endpoint-0001",
		Session: "machine-endpoint-0001/sess-1",
	}
	b.ReportAllocs()
	for b.Loop() {
		seq, err := seqr.NextCommand()
		if err != nil {
			b.Fatalf("NextCommand: %v", err)
		}
		if _, err := SealCommandEnvelope(key.ContentKey, 7, seq, cmd); err != nil {
			b.Fatalf("SealCommandEnvelope: %v", err)
		}
	}
}

// BenchmarkS11R4_InputSealerCriticalSection is the same interval on the input half, which
// round 3 already put under the lock. It is the control: the two are the same order of cost,
// which is why extending the lock to cover the whole bucket costs the hot path nothing it was
// not already paying.
func BenchmarkS11R4_InputSealerCriticalSection(b *testing.B) {
	key, err := crypto.NewEpochKeys()
	if err != nil {
		b.Fatalf("epoch keys: %v", err)
	}
	var seqr Sequencer
	data := []byte("ls -la\r")
	b.ReportAllocs()
	for b.Loop() {
		seq, err := seqr.NextInput()
		if err != nil {
			b.Fatalf("NextInput: %v", err)
		}
		if _, err := SealInputData(key.ContentKey, 7, seq, "machine-endpoint-0001/sess-1", data); err != nil {
			b.Fatalf("SealInputData: %v", err)
		}
	}
}
