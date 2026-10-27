package skeleton

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/procstart"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/status"
)

// This checks stale consumer state after a real stream ends, not native Claude
// compatibility or whether Raw alone can prove a terminating LF.
func TestClaudeChatEndedStreamCannotPromoteRetainedResult(t *testing.T) {
	d := assemble(t)
	m := launchClaudeMessageFixture(t, d)
	s := d.claudeFixtureState(m.ID)
	channel, ok := d.core.SessionHookChannel(m.ID)
	if s == nil || !ok {
		t.Fatal("fresh fixture channel unavailable")
	}
	hd := NewHookDrainer(d, m.ID, channel.SocketPath, channel.CursorPath)
	hd.claudeFixture = s
	hd.SetSpoolPath(channel.SpoolPath)
	if applied, _, err := hd.DrainOnce(); err != nil || applied != 1 {
		t.Fatal("real init did not fold", applied, err)
	}
	primary, err := d.core.ClaudeFixtureSnapshot(m.ID)
	if err != nil || !primary.Healthy || primary.ObservedSequence != 1 || primary.DurableSequence != 1 {
		t.Fatal("initial real primary prefix unavailable", err)
	}
	instance, ok := d.sessionInstance(m.ID)
	if !ok {
		t.Fatal("fresh fixture has no instance")
	}
	const text = "retained result is not a live stream"
	const operation = "ended-stream-consumer-control"
	uuid, err := newClaudeFixtureUUID()
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(text))
	intent := claudeFixtureIntent{Version: 1, Kind: "claude_fixture_message", Identity: primary.Identity, SessionInstance: instance, NativeUserUUID: uuid, TextSHA256: hex.EncodeToString(digest[:]), TextBytes: len(text), OwnerRevision: 1}
	bound, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = d.core.ClaimComposerOperation(operation, "semantic_send", m.ID, instance, intent.TextSHA256); err != nil {
		t.Fatal(err)
	}
	if err = d.eng.ObserveExecutionOwnerRevision(m.ID, 0, 1); err != nil {
		t.Fatal(err)
	}
	if err = d.core.BindSemanticRequest(operation, bound); err != nil {
		t.Fatal(err)
	}
	if err = d.core.BeginComposerOperation(operation); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.operation, s.intent, s.ownerRevision = operation, intent, 1
	s.evidence = claudeFixtureEvidence{Version: 1, Kind: "claude_fixture_message", OperationID: operation, Intent: intent, State: "prepared", RawHashes: []string{s.initHash}}
	s.mu.Unlock()
	write := shimwire.ClaudeMessageRequest{Version: 1, OperationID: operation, NativeUserUUID: uuid, Text: text, Identity: primary.Identity, OwnerRevision: 1, ThroughStreamSequence: primary.ObservedSequence, ReaderRevision: primary.ReaderRevision}
	prepared, err := d.core.PrepareClaudeFixtureDispatch(m.ID, write, bound)
	if err != nil {
		t.Fatal(err)
	}
	result, writeErr := prepared.Write()
	prepared.Close()
	if writeErr != nil || result.Status != "written" {
		t.Fatal("actual fixture input was not written", result.Status, writeErr)
	}
	deadline := time.Now().Add(5 * time.Second)
	var response shim.HookDrainResponse
	for time.Now().Before(deadline) {
		response, err = shim.ReadHookSpoolFileResponse(channel.SpoolPath, 0)
		if err == nil && len(response.Records) == 4 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err != nil || len(response.Records) != 4 {
		t.Fatal("genuine fixture turn did not finish capture", err)
	}
	// Fold the real user and assistant; retain the genuine result unconsumed.
	for _, record := range response.Records[1:3] {
		for {
			err = d.consumeClaudeFixture(s, record.Body)
			if !errors.Is(err, errClaudeFixturePending) || !time.Now().Before(deadline) {
				break
			}
			time.Sleep(time.Millisecond)
		}
		if err != nil {
			t.Fatal(err)
		}
		if err = persistHookDrainCursor(channel.CursorPath, record.Seq); err != nil {
			t.Fatal(err)
		}
		s.mu.Lock()
		s.folded = s.sequence
		s.mu.Unlock()
	}
	rows, err := d.readClaudeFixtureHistory(m.ID, s)
	if err != nil || len(rows) != 3 {
		t.Fatal("real fixture-written transcript chain missing", err)
	}
	if err = d.core.KillClaudeFixture(m.ID, primary.Identity); err != nil {
		t.Fatal(err)
	}
	ended := func(pid int, birth int64) bool {
		current, e := procstart.StartTime(pid)
		if e == nil && current > 0 {
			return current != birth
		}
		return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
	}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && time.Now().Before(m.CreatedAt.Add(11*time.Second)) {
		if ended(primary.Identity.PrimaryPID, primary.Identity.PrimaryStartTime) && ended(m.ShimPID, m.ShimStartTime) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !ended(primary.Identity.PrimaryPID, primary.Identity.PrimaryStartTime) || !ended(m.ShimPID, m.ShimStartTime) {
		t.Fatal("owned stream did not actually end before finite autoexit")
	}
	if _, err = os.Stat(filepath.Join(d.stateDir, m.ID, shim.ExitFile)); err != nil {
		t.Fatal("ended shim did not publish exit", err)
	}
	captureRaw, err := os.ReadFile(filepath.Join(d.stateDir, m.ID, "claude-capture.json"))
	var capture struct {
		StdoutEOF bool `json:"stdout_eof"`
		StderrEOF bool `json:"stderr_eof"`
		Loss      bool `json:"loss"`
	}
	if err != nil || json.Unmarshal(captureRaw, &capture) != nil || !capture.StdoutEOF || !capture.StderrEOF || capture.Loss {
		t.Fatal("ended fixture did not publish complete reader/capture joins", err)
	}
	retired := false
	for time.Now().Before(deadline) {
		if row, found := d.core.Get(m.ID); found && row.Status.Process != status.ProcessRunning && d.claudeFixtureState(m.ID) == nil {
			retired = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !retired {
		t.Fatal("owned session callback did not retire the fixture within original stop bound")
	}
	// Recreate the stale cached readiness a fresh live-source check must defeat.
	s.mu.Lock()
	s.healthy, s.initialized, s.ready = true, true, true
	s.mu.Unlock()
	beforeJournal, err := d.core.JournalReadFrom(0)
	if err != nil {
		t.Fatal(err)
	}
	beforeCursor, err := os.ReadFile(channel.CursorPath)
	if err != nil {
		t.Fatal(err)
	}
	before, exists := d.core.ComposerOperation(operation)
	if !exists || len(before.Evidence) == 0 {
		t.Fatal("real admitted evidence missing")
	}
	applied, _, drainErr := hd.DrainOnce()
	afterJournal, journalErr := d.core.JournalReadFrom(0)
	afterCursor, cursorErr := os.ReadFile(channel.CursorPath)
	after, exists := d.core.ComposerOperation(operation)
	if drainErr == nil || applied != 0 || journalErr != nil || cursorErr != nil || !exists || afterJournal.Cursor != beforeJournal.Cursor || !bytes.Equal(beforeCursor, afterCursor) || !bytes.Equal(before.Evidence, after.Evidence) {
		t.Fatal("ended stream promoted retained result or advanced journal/cursor", applied, drainErr, journalErr, cursorErr)
	}
	s.mu.Lock()
	sequence, resultSequence := s.sequence, s.evidence.ResultSequence
	s.mu.Unlock()
	if sequence != 3 || resultSequence != 0 {
		t.Fatal("ended stream gained completion coordinates")
	}
}
