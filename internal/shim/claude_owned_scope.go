package shim

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"syscall"
)

// verifyOwnedRecord permits publication only over our own exact durable bytes.
// Matching decoded subsets would overlook unknown/duplicate routing fields.
func (s *managedNativeScope) verifyOwnedRecord() error {
	f, err := s.root.OpenFile(NativeProcessFile, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return errors.New("shim: owned native scope is unavailable")
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	stat, ok := infoSys(info)
	if err != nil || !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 || info.Size() > 64<<10 {
		return errors.New("shim: owned native scope is unsafe")
	}
	raw, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 {
		return errors.New("shim: owned native scope is unreadable")
	}
	for _, expected := range []NativeProcessInfo{s.info, s.preclaim} {
		if expected.SchemaVersion != ClaudeNativeWriterSchemaVersion {
			continue
		}
		encoded, err := json.Marshal(expected)
		if err == nil && bytes.Equal(raw, encoded) {
			return nil
		}
	}
	return errors.New("shim: owned native scope changed")
}
