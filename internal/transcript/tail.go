package transcript

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// TailBytes is identity-discovery evidence, not a terminal archive.
const TailBytes = 64 << 10

const tailHeaderBytes = 32

var tailMagic = [8]byte{'S', 'W', 'T', 'a', 'i', 'l', '0', '1'}

var tailChecksumTable = crc32.MakeTable(crc32.Castagnoli)

var ErrTailBusy = errors.New("transcript: identity tail changed during read")

type tailSink struct {
	file       tailFile
	written    uint64
	generation uint64
	failed     error
	buffer     [TailBytes]byte
}

type tailFile interface {
	WriteAt([]byte, int64) (int, error)
	Sync() error
	Close() error
}

// NewTail keeps a fixed-size ring and favors newest queued bytes under pressure.
func NewTail(path string) (*Writer, error) {
	f, err := os.CreateTemp(filepath.Dir(path), ".identity-tail-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return nil, err
	}
	var header [tailHeaderBytes]byte
	copy(header[:], tailMagic[:])
	if _, err := f.WriteAt(header[:], 0); err != nil {
		_ = f.Close()
		return nil, err
	}
	// Replace the reserved filename without truncating a symlink/hardlink target.
	if err := os.Rename(f.Name(), path); err != nil {
		_ = f.Close()
		return nil, err
	}
	w := newWriter(&tailSink{file: f})
	w.latest = true
	return w, nil
}

func (s *tailSink) Write(p []byte) (written int, writeErr error) {
	if s.failed != nil {
		return 0, s.failed
	}
	defer func() {
		if writeErr != nil {
			s.failed = writeErr // partial ring data must never acquire a later valid generation
		}
	}()
	n := len(p)
	if n == 0 {
		return 0, nil
	}
	var stamp [8]byte
	binary.BigEndian.PutUint64(stamp[:], s.generation+1)
	if _, err := s.file.WriteAt(stamp[:], 8); err != nil {
		return 0, err
	}
	end := s.written + uint64(n)
	if len(p) > TailBytes {
		p = p[len(p)-TailBytes:]
	}
	start := (end - uint64(len(p))) % TailBytes
	first := min(len(p), TailBytes-int(start))
	if _, err := s.file.WriteAt(p[:first], tailHeaderBytes+int64(start)); err != nil {
		return 0, err
	}
	if _, err := s.file.WriteAt(p[first:], tailHeaderBytes); err != nil {
		return 0, err
	}
	copy(s.buffer[int(start):], p[:first])
	copy(s.buffer[:], p[first:])
	var checksum uint32
	if end <= TailBytes {
		checksum = crc32.Checksum(s.buffer[:int(end)], tailChecksumTable)
	} else {
		position := end % TailBytes
		checksum = crc32.Update(0, tailChecksumTable, s.buffer[int(position):])
		checksum = crc32.Update(checksum, tailChecksumTable, s.buffer[:int(position)])
	}
	var commit [16]byte
	binary.BigEndian.PutUint64(commit[:8], end)
	binary.BigEndian.PutUint32(commit[8:12], checksum)
	if _, err := s.file.WriteAt(commit[:], 16); err != nil {
		return 0, err
	}
	binary.BigEndian.PutUint64(stamp[:], s.generation+2)
	if _, err := s.file.WriteAt(stamp[:], 8); err != nil {
		return 0, err
	}
	s.written, s.generation = end, s.generation+2
	return n, nil
}

func (s *tailSink) Sync() error {
	if s.failed != nil {
		return s.failed
	}
	return s.file.Sync()
}
func (s *tailSink) Close() error { return s.file.Close() }

// ReadTail returns chronological bytes only for a stable committed generation.
// Passing the previous generation avoids reading quiet tails again.
func ReadTail(path string, after uint64) ([]byte, uint64, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, after, err
	}
	defer func() { _ = f.Close() }()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return nil, after, errors.New("transcript: identity tail is not a regular file")
	}
	var header, confirm [tailHeaderBytes]byte
	if _, err := io.ReadFull(f, header[:]); err != nil {
		return nil, after, err
	}
	if string(header[:8]) != string(tailMagic[:]) {
		return nil, after, fmt.Errorf("transcript: invalid identity tail")
	}
	gen := binary.BigEndian.Uint64(header[8:16])
	if gen%2 != 0 {
		return nil, after, ErrTailBusy
	}
	if gen == after {
		return nil, gen, nil
	}
	total := binary.BigEndian.Uint64(header[16:])
	length := min(total, uint64(TailBytes))
	tail := make([]byte, int(length))
	start := uint64(0)
	if total > TailBytes {
		start = total % TailBytes
	}
	first := min(length, uint64(TailBytes)-start)
	if _, err := f.ReadAt(tail[:int(first)], tailHeaderBytes+int64(start)); err != nil {
		return nil, after, err
	}
	if _, err := f.ReadAt(tail[int(first):], tailHeaderBytes); err != nil {
		return nil, after, err
	}
	if _, err := f.ReadAt(confirm[:], 0); err != nil {
		return nil, after, err
	}
	if header != confirm {
		return nil, after, ErrTailBusy
	}
	if crc32.Checksum(tail, tailChecksumTable) != binary.BigEndian.Uint32(header[24:28]) {
		return nil, after, errors.New("transcript: torn identity tail")
	}
	return tail, gen, nil
}
