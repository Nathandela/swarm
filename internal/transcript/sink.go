package transcript

import (
	"fmt"
	"os"
)

// fileSink is the production sink: an append-only file at path, rotating to
// path.1, path.2, ... when it reaches cfg.MaxBytes, capped at cfg.MaxFiles
// total files (current + rotated). Every file it creates is 0600 (E3.3).
//
// fileSink has no internal locking: Writer's drain goroutine is its only
// caller, and it calls Write, Sync, and Close strictly one at a time.
type fileSink struct {
	path     string
	maxBytes int64
	maxFiles int
	file     *os.File
	size     int64
}

// newFileSink is the production newSink implementation.
func newFileSink(path string, cfg Config) (sink, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &fileSink{
		path:     path,
		maxBytes: cfg.MaxBytes,
		maxFiles: cfg.MaxFiles,
		file:     f,
		size:     info.Size(),
	}, nil
}

// Write appends p to the current file as a single unit, then rotates if that
// pushed the file to or past maxBytes. p is never split across a rotation.
func (s *fileSink) Write(p []byte) (int, error) {
	total := 0
	for total < len(p) {
		n, err := s.file.Write(p[total:])
		total += n
		s.size += int64(n)
		if err != nil {
			return total, err
		}
	}
	if s.maxBytes > 0 && s.size >= s.maxBytes {
		if err := s.rotate(); err != nil {
			return total, err
		}
	}
	return total, nil
}

func (s *fileSink) Sync() error {
	return s.file.Sync()
}

func (s *fileSink) Close() error {
	return s.file.Close()
}

// rotate closes the current file, shifts rotated generations up by one
// (path.1 -> path.2 -> ...) dropping whatever would exceed maxFiles, moves
// the just-filled current file to path.1, and opens a fresh empty current
// file (0600). With maxFiles <= 1, no generations are kept at all: the
// current file is simply truncated.
func (s *fileSink) rotate() error {
	if err := s.file.Close(); err != nil {
		return err
	}
	if s.maxFiles > 1 {
		for i := s.maxFiles - 1; i >= 2; i-- {
			src := fmt.Sprintf("%s.%d", s.path, i-1)
			dst := fmt.Sprintf("%s.%d", s.path, i)
			if _, err := os.Stat(src); err == nil {
				if err := os.Rename(src, dst); err != nil {
					return err
				}
			}
		}
		if err := os.Rename(s.path, s.path+".1"); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	s.file = f
	s.size = 0
	return nil
}
