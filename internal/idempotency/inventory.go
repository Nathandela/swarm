package idempotency

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"syscall"
)

// UnresolvedRecord is the public part of an unsettled receipt. Request,
// evidence and outcome payloads can contain private inputs.
type UnresolvedRecord struct {
	OperationID     string `json:"operation_id"`
	Action          string `json:"action"`
	SessionID       string `json:"session_id,omitempty"`
	SessionInstance string `json:"session_instance,omitempty"`
	Phase           Phase  `json:"phase"`
}

// InspectUnresolved reads a bounded snapshot without sealing or truncating
// incomplete appends, creating state, or acquiring the writer's lock.
func InspectUnresolved(ctx context.Context, dir string) ([]UnresolvedRecord, error) {
	f, err := os.OpenFile(filepath.Join(dir, logFile), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	before, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Size() > 256<<20 {
		return nil, errors.New("idempotency: receipt log is not a regular file or exceeds the inventory limit")
	}

	pending := make(map[string]UnresolvedRecord)
	_, seal, truncate, err := replayRecords(io.LimitReader(inventoryReader{ctx, f}, before.Size()), func(r Record) {
		switch r.Phase {
		case PhaseCompleted:
			if len(r.Request) > 0 && (r.Action == "semantic_send" || r.Action == "composer_send") && !settledNativeInputEvidence(r.Evidence) {
				pending[r.OperationID] = UnresolvedRecord{r.OperationID, r.Action, r.SessionID, r.SessionInstance, "native_outcome_unresolved"}
			} else {
				delete(pending, r.OperationID)
			}
		case PhaseFailed:
			delete(pending, r.OperationID)
		default:
			pending[r.OperationID] = UnresolvedRecord{r.OperationID, r.Action, r.SessionID, r.SessionInstance, r.Phase}
		}
	})
	if err != nil {
		return nil, err
	}
	after, err := os.Lstat(f.Name())
	if err != nil {
		return nil, err
	}
	if seal || truncate || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, errors.New("idempotency: receipt log changed or has an unsealed append; inventory is incomplete")
	}
	res := make([]UnresolvedRecord, 0, len(pending))
	for _, r := range pending {
		res = append(res, r)
	}
	sort.Slice(res, func(i, j int) bool { return res[i].OperationID < res[j].OperationID })
	return res, nil
}

type inventoryReader struct {
	ctx context.Context
	io.Reader
}

func (r inventoryReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}
