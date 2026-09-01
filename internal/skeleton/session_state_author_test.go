package skeleton

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/Nathandela/swarm/internal/adapter/claude"
	"github.com/Nathandela/swarm/internal/adapter/opencode"
)

func TestSessionStateAuthor_FailedAppendRetriesAndSuccessDeduplicates(t *testing.T) {
	d := &Daemon{}
	const session, incarnation = "s", 77
	instance := mintSessionInstance()
	if err := d.recordSessionInstance(session, instance, incarnation); err != nil {
		t.Fatal(err)
	}
	wantFailure := errors.New("append failed once")
	attempts := 0
	d.sessionStatePublisher = func(_ string, gotIncarnation int, gotStart int64, author func() (json.RawMessage, error)) (bool, error) {
		if gotIncarnation != incarnation {
			t.Fatalf("publisher incarnation = %d, want %d", gotIncarnation, incarnation)
		}
		if gotStart != 0 {
			t.Fatalf("publisher start = %d, want legacy unknown 0", gotStart)
		}
		payload, err := author()
		if err != nil {
			return true, err
		}
		if payload == nil {
			return true, nil
		}
		attempts++
		if attempts == 1 {
			return true, wantFailure
		}
		return true, nil
	}

	if _, err := d.authorSessionCapabilities(session, instance, "claude", claude.New(), "1", "r", false); !errors.Is(err, wantFailure) {
		t.Fatalf("first author error = %v, want injected append failure", err)
	}
	if _, err := d.authorSessionCapabilities(session, instance, "claude", claude.New(), "1", "r", false); err != nil {
		t.Fatalf("retry author: %v", err)
	}
	if _, err := d.authorSessionCapabilities(session, instance, "claude", claude.New(), "1", "r", false); err != nil {
		t.Fatalf("deduplicated author: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("append attempts = %d, want failed first + successful retry only", attempts)
	}
}

func TestSessionStateAuthor_StaleInstanceCannotOverwriteCapabilityRecord(t *testing.T) {
	d := &Daemon{}
	const session = "s"
	oldInstance := mintSessionInstance()
	if err := d.recordSessionInstance(session, oldInstance, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := d.authorSessionCapabilities(session, oldInstance, "claude", claude.New(), "1", "r", false); err != nil {
		t.Fatal(err)
	}
	before, _ := d.sessionCapabilities(session)
	newInstance := mintSessionInstance()
	if err := d.recordSessionInstance(session, newInstance, 20); err != nil {
		t.Fatal(err)
	}
	if _, err := d.authorSessionCapabilities(session, oldInstance, "opencode", opencode.New(), "2", "r", false); err == nil {
		t.Fatal("stale old-instance author succeeded")
	}
	after, _ := d.sessionCapabilities(session)
	if after != before {
		t.Fatalf("stale author overwrote capability record: before=%+v after=%+v", before, after)
	}
}
