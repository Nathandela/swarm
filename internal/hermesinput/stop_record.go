package hermesinput

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
)

type stopRecord struct {
	Descriptor   Descriptor `json:"descriptor"`
	Intent       bool       `json:"intent"`
	Acknowledged bool       `json:"acknowledged"`
}

func WriteStopIntent(dir string, d Descriptor) error {
	return writeRecord(dir, StopFile, stopRecord{Descriptor: d, Intent: true})
}

func readStop(dir string, d Descriptor) (stopRecord, bool) {
	var r stopRecord
	path := filepath.Join(dir, StopFile)
	if privateNode(path, 0) != nil {
		return r, false
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > 32768 || json.Unmarshal(raw, &r) != nil {
		return r, false
	}
	return r, r.Descriptor == d && r.Intent
}

// ViewerEnded performs an explicit native stop only when Swarm already recorded
// that intent. Ordinary viewer closure detaches accepted native work.
func ViewerEnded(ctx context.Context, dir string, d Descriptor) error {
	r, intent := readStop(dir, d)
	if intent {
		if err := Stop(ctx, d.Binding); err != nil {
			return err
		}
	}
	if err := CloseViewer(ctx, d.Binding); err != nil {
		return err
	}
	if intent {
		r.Acknowledged = true
		return writeRecord(dir, StopFile, r)
	}
	return nil
}
