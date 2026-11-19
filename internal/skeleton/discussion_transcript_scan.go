package skeleton

import (
	"bufio"
	"errors"
	"io"
	"os"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

const discussionTranscriptMaxBytes = 64 << 20
const discussionTranscriptActiveScans = 128

var errDiscussionScanWindow = errors.New("worker transcript scan window complete")
var errDiscussionRecordLimit = errors.New("worker transcript record exceeds limit")

type discussionScanKey struct {
	Local, Conversation string
	Scope               schema.WorkerHistoryScope
}

type discussionTranscriptScan struct {
	file              os.FileInfo
	offset, windowEnd int64
	turn, previous    string
	matched           bool
	limited, finished bool
	evidence          schema.WorkerTurnEvidence
}

func (scan *discussionTranscriptScan) readLine(reader *bufio.Reader) ([]byte, error) {
	if scan.offset >= scan.windowEnd {
		return nil, errDiscussionScanWindow
	}
	line, err := readNativeLine(reader)
	if errors.Is(err, errLoginObservationPending) {
		return nil, errDiscussionRecordLimit
	}
	if err == nil {
		if int64(len(line)) > discussionTranscriptMaxBytes-scan.offset {
			scan.offset = discussionTranscriptMaxBytes
			return nil, errDiscussionScanWindow
		}
		scan.offset += int64(len(line))
	}
	return line, err
}

func (d *Daemon) readDiscussionTranscript(file *os.File, meta persist.Meta, scope schema.WorkerHistoryScope) (schema.WorkerTurnEvidence, bool, error) {
	info, err := file.Stat()
	if err != nil {
		return schema.WorkerTurnEvidence{}, false, err
	}
	key := discussionScanKey{meta.ID, meta.ConversationID, scope}
	d.discussionScanMu.Lock()
	defer d.discussionScanMu.Unlock()
	scan := d.discussionScans[key]
	if scan == nil || !os.SameFile(scan.file, info) || scan.offset > info.Size() {
		if len(d.discussionScans) >= discussionTranscriptActiveScans {
			for stale := range d.discussionScans {
				delete(d.discussionScans, stale)
				break
			}
		}
		if d.discussionScans == nil {
			d.discussionScans = make(map[discussionScanKey]*discussionTranscriptScan)
		}
		scan = &discussionTranscriptScan{file: info}
		d.discussionScans[key] = scan
	}
	if scan.finished {
		return scan.evidence, scan.limited, nil
	}
	if _, err = file.Seek(scan.offset, io.SeekStart); err != nil {
		return schema.WorkerTurnEvidence{}, false, err
	}
	scan.windowEnd = min(scan.offset+loginTranscriptTailBytes, discussionTranscriptMaxBytes)
	reader := bufio.NewReaderSize(file, 64<<10)
	if scan.offset == 0 && meta.AgentType == "codex" {
		header, err := scan.readLine(reader)
		if err != nil || !codexTranscriptNamesItsConversation(header, meta.ConversationID) {
			delete(d.discussionScans, key)
			return schema.WorkerTurnEvidence{}, false, errLoginObservationPending
		}
	}
	switch meta.AgentType {
	case "claude":
		scan.evidence, err = scan.readClaude(reader, meta.ConversationID, scope.TurnID, scope.PromptHash)
	default:
		scan.evidence, err = scan.readCodex(reader, scope.TurnID, scope.InputID, scope.PromptHash)
	}
	switch err {
	case nil:
		scan.finished = true
	case errDiscussionRecordLimit:
		scan.finished, scan.limited = true, true
		err = nil
	case io.EOF, errDiscussionScanWindow:
		if scan.offset >= discussionTranscriptMaxBytes {
			scan.finished, scan.limited = true, true
		}
		err = nil
	default:
		// A malformed complete record cannot establish ancestry to a later turn.
		scan.finished = true
	}
	return scan.evidence, scan.limited, err
}
