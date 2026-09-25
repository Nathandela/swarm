package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"math"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

type pilotJournalClient interface {
	JournalSubscribeFrom(context.Context, uint64) (protocol.JournalResume, <-chan protocol.JournalRecord, error)
}

// runPilotWatch streams one NDJSON envelope per relevant journal record. The host
// checkpoints a cursor only after consuming its line; this command stores no ack.
func runPilotWatch(path string, args []string, c agentClient, stdout, stderr io.Writer) int {
	var after uint64
	ids := make(map[string]bool)
	for i := 0; i < len(args); i++ {
		if args[i] == "--after" {
			if i+1 >= len(args) {
				return pilotMisuse(stderr, "watch --after needs a cursor")
			}
			value, err := strconv.ParseUint(args[i+1], 10, 64)
			if err != nil {
				return pilotMisuse(stderr, "watch --after needs a numeric cursor")
			}
			after = value
			i++
		} else if args[i] == "" || args[i][0] == '-' {
			return pilotMisuse(stderr, "watch needs explicit discussion IDs [--after cursor]")
		} else {
			ids[args[i]] = true
		}
	}
	if len(ids) == 0 {
		return pilotMisuse(stderr, "watch needs explicit discussion IDs")
	}
	ctxInfo, err := readPilotContext(path, c)
	if err != nil {
		return pilotMisuse(stderr, err.Error())
	}
	watchIDs := make(map[string]string)
	for id := range ids {
		row, err := pilotTarget(c, id)
		if err != nil {
			return pilotMisuse(stderr, "watch: "+err.Error())
		}
		watchIDs[row.ID] = row.ID
		if _, local, ok := protocol.ParseID(row.ID); ok {
			watchIDs[local] = row.ID
		}
	}
	normalize := func(rec protocol.JournalRecord) (protocol.JournalRecord, bool) {
		id, ok := watchIDs[rec.SessionID]
		if ok {
			rec.SessionID = id
		}
		return rec, ok
	}
	journal, ok := c.(pilotJournalClient)
	if !ok {
		return pilotMisuse(stderr, "watch requires atomic journal subscription")
	}
	stop, cleanup, err := pilotWaitStop(path)
	if err != nil {
		return pilotMisuse(stderr, err.Error())
	}
	defer cleanup()
	if _, err := readPilotContext(path, c); err != nil {
		return pilotMisuse(stderr, err.Error())
	}
	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	ctx, cancel := context.WithDeadline(signalCtx, ctxInfo.Expires)
	defer cancel()
	watchOutput, pollableOutput, closeOutput, err := pilotWatchOutput(stdout)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "pilot: watch stdout: %v\n", err)
		return 1
	}
	defer closeOutput()
	stdout = watchOutput
	go func() {
		select {
		case <-stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	from := after
	if after == 0 {
		// A fresh watch needs only the atomic roster/head. The high cursor avoids
		// replaying years of history; -1 avoids readFromLocked's from+1 overflow.
		from = math.MaxUint64 - 1
	}
	resume, live, err := journal.JournalSubscribeFrom(ctx, from)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "pilot: watch: %v\n", err)
		return 1
	}
	lastCursor := after
	emit := func(receipt string, cursor uint64, data any) bool {
		unlock, err := lockPilot(path)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "pilot: watch stopped: %v\n", err)
			return false
		}
		defer unlock()
		if _, err := readPilotContext(path, c); err != nil {
			_, _ = fmt.Fprintf(stderr, "pilot: watch stopped: %v\n", err)
			return false
		}
		body, err := json.Marshal(data)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "pilot: watch: %v\n", err)
			return false
		}
		boundary := uint64(0)
		if receipt == "snapshot" {
			boundary = resume.Cursor
		}
		line, err := json.Marshal(struct {
			TrustedGuidance string          `json:"trusted_guidance"`
			Receipt         string          `json:"receipt"`
			Cursor          uint64          `json:"cursor"`
			BoundaryCursor  uint64          `json:"boundary_cursor,omitempty"`
			WorkerData      json.RawMessage `json:"worker_data,omitempty"`
		}{pilotReminder, receipt, cursor, boundary, body})
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "pilot: watch: %v\n", err)
			return false
		}
		line = append(line, '\n')
		if err := writePilotWatchLine(ctx, stdout, line, pollableOutput); err != nil {
			_, _ = fmt.Fprintf(stderr, "pilot: watch: %v\n", err)
			return false
		}
		if receipt == "event" || receipt == "snapshot" || receipt == "history_gap" {
			lastCursor = cursor
		}
		return true
	}
	roster := make([]protocol.JournalRecord, 0)
	for _, rec := range resume.Roster {
		if record, ok := normalize(rec); ok {
			roster = append(roster, record)
		}
	}
	snapshotCursor := resume.Cursor
	if after != 0 {
		snapshotCursor = after
	}
	if !emit("snapshot", snapshotCursor, roster) {
		return 1
	}
	if resume.FullResync || after > resume.Cursor {
		if !emit("history_gap", resume.Cursor, map[string]any{"full_resync": resume.FullResync, "cursor_ahead": after > resume.Cursor}) {
			return 1
		}
	} else if after != 0 {
		for _, rec := range resume.Events {
			if record, ok := normalize(rec); ok && pilotWatchRelevant(record) && !emit("event", record.Cursor, record) {
				return 1
			}
		}
	}
	for {
		select {
		case <-ctx.Done():
			if _, err := readPilotContext(path, c); err == nil {
				_ = emit("disconnected", lastCursor, map[string]string{"reason": ctx.Err().Error()})
			}
			return 1
		case rec, open := <-live:
			if !open {
				_ = emit("disconnected", lastCursor, map[string]string{"reason": "journal connection closed"})
				return 1
			}
			if record, ok := normalize(rec); ok && pilotWatchRelevant(record) {
				if !emit("event", record.Cursor, record) {
					return 1
				}
			}
		}
	}
}

func pilotWatchRelevant(rec protocol.JournalRecord) bool {
	switch rec.Type {
	case "structured_gap", "exited", "lost", "deleted":
		return true
	case "group_transition":
		return rec.Group == status.GroupNeedsInput || rec.Group == status.GroupReadyForReview || rec.Group == status.GroupCompleted
	case "interaction":
		var item struct {
			Kind   string `json:"kind"`
			Status string `json:"status"`
		}
		if json.Unmarshal(rec.Item, &item) != nil {
			return false
		}
		return (item.Kind == "approval_request" && item.Status == "in_progress") || item.Kind == "agent_message" && (item.Status == "completed" || item.Status == "failed" || item.Status == "declined")
	}
	return false
}

// A blocked stdout pipe must not pin a revoked pilot context forever.
func writePilotWatchLine(ctx context.Context, stdout io.Writer, line []byte, pollable bool) error {
	if !pollable {
		_, err := io.Copy(stdout, bytes.NewReader(line))
		return err
	}
	file := stdout.(*os.File)
	done := make(chan error, 1)
	go func() { _, err := io.Copy(file, bytes.NewReader(line)); done <- err }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = file.Close()
		<-done // the watch owns a nonblocking pipe FD, so Close releases poll I/O.
		return ctx.Err()
	}
}

// Duplicate nonregular stdout for this watch, make its file description
// nonblocking while the watch runs, and restore the inherited flags at exit.
// Dup shares file status flags, so restoring them is part of cleanup.
func pilotWatchOutput(stdout io.Writer) (io.Writer, bool, func(), error) {
	file, ok := stdout.(*os.File)
	if !ok {
		return stdout, false, func() {}, nil
	}
	info, err := file.Stat()
	if err != nil {
		return nil, false, nil, err
	}
	if info.Mode().IsRegular() {
		return stdout, false, func() {}, nil
	}
	originalFD := int(file.Fd())
	flags, err := unix.FcntlInt(uintptr(originalFD), unix.F_GETFL, 0)
	if err != nil {
		return nil, false, nil, err
	}
	fd, err := unix.Dup(originalFD)
	if err != nil {
		return nil, false, nil, err
	}
	unix.CloseOnExec(fd)
	if err := unix.SetNonblock(fd, true); err != nil {
		_ = unix.Close(fd)
		return nil, false, nil, err
	}
	writer := os.NewFile(uintptr(fd), "pilot-watch-stdout")
	cleanup := func() {
		_ = writer.Close()
		_ = unix.SetNonblock(originalFD, flags&unix.O_NONBLOCK != 0)
	}
	return writer, true, cleanup, nil
}
