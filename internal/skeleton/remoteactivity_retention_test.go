package skeleton

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

func TestRemoteActivityCompactsCompleteRecentRecords(t *testing.T) {
	a, dir := presetAPI(t)
	path := filepath.Join(dir, remoteActivityFile)
	var old bytes.Buffer
	for i := range 1600 {
		line, err := json.Marshal(schema.ActivityRecord{Action: "session_launch", OperationID: fmt.Sprintf("old-%04d", i), SessionID: strings.Repeat("s", 1024), Outcome: "applied"})
		if err != nil {
			t.Fatal(err)
		}
		old.Write(line)
		old.WriteByte('\n')
	}
	if err := os.WriteFile(path, old.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	custody := filepath.Join(dir, "idempotency.log")
	if err := os.WriteFile(custody, []byte("outstanding-operation-custody"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.RecordRemoteActivity(schema.ActivityRecord{Action: "session_launch", OperationID: "newest", Outcome: "refused"})
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > 1<<20 {
		t.Fatalf("audit log grew to %d bytes; want at most 1 MiB", len(raw))
	}
	var ids []string
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		var record schema.ActivityRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatalf("retained a partial record: %v", err)
		}
		ids = append(ids, record.OperationID)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(ids) < 2 || ids[0] == "old-0000" || ids[len(ids)-2] != "old-1599" || ids[len(ids)-1] != "newest" {
		t.Fatalf("retained %d records with unexpected history boundary: %v", len(ids), ids)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("audit mode: info=%v err=%v", info, err)
	}
	if got, err := os.ReadFile(custody); err != nil || string(got) != "outstanding-operation-custody" {
		t.Fatalf("audit compaction changed operation custody: %q, %v", got, err)
	}
}

func TestRemoteActivityRefusesSymlinkWithoutTouchingTarget(t *testing.T) {
	a, dir := presetAPI(t)
	target := filepath.Join(t.TempDir(), "owner-file")
	if err := os.WriteFile(target, []byte("preserve me"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, remoteActivityFile)); err != nil {
		t.Fatal(err)
	}
	a.RecordRemoteActivity(schema.ActivityRecord{Action: "session_launch", OperationID: "unsafe-target"})
	raw, err := os.ReadFile(target)
	if err != nil || string(raw) != "preserve me" {
		t.Fatalf("audit append changed a symlink target: %q, %v", raw, err)
	}
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("audit append changed target permissions: %v, %v", info, err)
	}
}

func TestRemoteActivityConcurrentAppendsKeepEverySmallRecord(t *testing.T) {
	a, dir := presetAPI(t)
	path := filepath.Join(dir, remoteActivityFile)
	seed := bytes.Repeat([]byte("{\"old\":true}\n"), ((1<<20)-256)/len("{\"old\":true}\n"))
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatal(err)
	}
	var writers sync.WaitGroup
	for i := range 64 {
		writers.Go(func() {
			a.RecordRemoteActivity(schema.ActivityRecord{Action: "session_launch", OperationID: fmt.Sprintf("op-%02d", i), Outcome: "applied"})
		})
	}
	writers.Wait()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		var record schema.ActivityRecord
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}
		if record.OperationID != "" {
			seen[record.OperationID] = true
		}
	}
	if len(seen) != 64 {
		t.Fatalf("retained %d unique records, want all 64", len(seen))
	}
}

func TestRemoteActivityDropsIncompleteTailAndOversizedRecord(t *testing.T) {
	a, dir := presetAPI(t)
	path := filepath.Join(dir, remoteActivityFile)
	previous := []byte("{\"operation_id\":\"complete\"}\n{\"partial\":")
	if err := os.WriteFile(path, previous, 0o600); err != nil {
		t.Fatal(err)
	}
	a.RecordRemoteActivity(schema.ActivityRecord{OperationID: strings.Repeat("x", 1<<20)})
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, previous) {
		t.Fatalf("oversized record changed log: %q, %v", got, err)
	}
	a.RecordRemoteActivity(schema.ActivityRecord{OperationID: "next"})
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
	if len(lines) != 2 || string(lines[0]) != "{\"operation_id\":\"complete\"}" {
		t.Fatalf("lost complete record or retained torn append: %q", raw)
	}
	var got schema.ActivityRecord
	if err := json.Unmarshal(lines[1], &got); err != nil || got.OperationID != "next" {
		t.Fatalf("new record: %+v, %v", got, err)
	}
}

func TestRemoteActivityRefusesHardlinkWithoutChangingTarget(t *testing.T) {
	a, dir := presetAPI(t)
	target := filepath.Join(t.TempDir(), "owner-file")
	if err := os.WriteFile(target, []byte("preserve me"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(target, filepath.Join(dir, remoteActivityFile)); err != nil {
		t.Fatal(err)
	}
	a.RecordRemoteActivity(schema.ActivityRecord{OperationID: "unsafe-target"})
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("audit append changed target permissions: %v, %v", info, err)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "preserve me" {
		t.Fatalf("audit append changed hardlink target: %q, %v", got, err)
	}
}
