package transcript

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestIdentityTailRetainsNewestBytesAndGenerationAfterWrap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tail")
	w, err := NewTail(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	first := bytes.Repeat([]byte("a"), TailBytes)
	if _, err := w.Write(first); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	got, generation, err := ReadTail(path, 0)
	if err != nil || !bytes.Equal(got, first) {
		t.Fatalf("initial tail: %d bytes, %v", len(got), err)
	}
	marker := []byte("exit-only native conversation identity\n")
	if _, err := w.Write(marker); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	got, next, err := ReadTail(path, generation)
	if err != nil || next <= generation || len(got) != TailBytes || !bytes.HasSuffix(got, marker) {
		t.Fatalf("wrapped tail lost newest marker or generation: %d bytes, %d -> %d, %v", len(got), generation, next, err)
	}
	if info, err := os.Stat(path); err != nil || info.Size() != tailHeaderBytes+TailBytes {
		t.Fatalf("tail exceeded fixed size: %v, %v", info, err)
	}
	if data, same, err := ReadTail(path, next); err != nil || data != nil || same != next {
		t.Fatalf("quiet generation reread: %d bytes, %d, %v", len(data), same, err)
	}
}

func TestIdentityTailOversizedWritePreservesItsEnd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tail")
	w, err := NewTail(path)
	if err != nil {
		t.Fatal(err)
	}
	s := w.sink.(*tailSink)
	data := append(bytes.Repeat([]byte("old"), TailBytes), bytes.Repeat([]byte("z"), TailBytes)...)
	if n, err := s.Write(data); err != nil || n != len(data) {
		t.Fatalf("oversized sink write: %d, %v", n, err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	got, _, err := ReadTail(path, 0)
	if err != nil || !bytes.Equal(got, data[len(data)-TailBytes:]) {
		t.Fatalf("oversized tail lost newest bytes: %d, %v", len(got), err)
	}
}

func TestIdentityTailRefusesUncommittedGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tail")
	w, err := NewTail(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	var stamp [8]byte
	binary.BigEndian.PutUint64(stamp[:], 1)
	_, writeErr := f.WriteAt(stamp[:], 8)
	_ = f.Close()
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	if _, _, err := ReadTail(path, 0); !errors.Is(err, ErrTailBusy) {
		t.Fatalf("uncommitted tail was accepted: %v", err)
	}
}

func TestIdentityTailQueuePressureRetainsFinalBytes(t *testing.T) {
	w := &Writer{latest: true}
	w.enqueueLocked(bytes.Repeat([]byte("old"), int(bufCap/3)))
	marker := []byte("final identity")
	w.enqueueLocked(marker)
	var queued []byte
	for _, part := range w.pending {
		queued = append(queued, part...)
	}
	if int64(len(queued)) > bufCap || !bytes.HasSuffix(queued, marker) || w.dropped == 0 {
		t.Fatalf("queue did not keep newest evidence under pressure: len=%d dropped=%d", len(queued), w.dropped)
	}
}

type failingTailFile struct {
	*os.File
	calls int
}

func (f *failingTailFile) WriteAt(p []byte, off int64) (int, error) {
	f.calls++
	if f.calls == 2 {
		n, _ := f.File.WriteAt(p[:min(len(p), 3)], off)
		return n, io.ErrShortWrite
	}
	return f.File.WriteAt(p, off)
}

func TestIdentityTailPartialFailureNeverRecommitsMixedData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tail")
	w, err := NewTail(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	s := w.sink.(*tailSink)
	if _, err := s.Write(bytes.Repeat([]byte("a"), TailBytes)); err != nil {
		t.Fatal(err)
	}
	file := s.file.(*os.File)
	s.file = &failingTailFile{File: file}
	if _, err := s.Write([]byte("overwritten native marker")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("injected partial write did not fail: %v", err)
	}
	s.file = file
	if _, err := s.Write([]byte("later bytes")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("failed sink accepted a later generation: %v", err)
	}
	if _, _, err := ReadTail(path, 0); !errors.Is(err, ErrTailBusy) {
		t.Fatalf("partially overwritten tail became committed: %v", err)
	}
	if err := s.Sync(); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("failed tail reported a successful flush: %v", err)
	}
}

func TestIdentityTailRejectsTornPayloadBehindAnEvenGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tail")
	w, err := NewTail(path)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte("conv-id=the-original-id\n"))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := f.WriteAt([]byte("CORRUPT"), tailHeaderBytes+8)
	_ = f.Close()
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	if _, _, err := ReadTail(path, 0); err == nil {
		t.Fatal("a torn payload was trusted because its generation was even")
	}
}

func TestIdentityTailInitializationPreservesLinkedFiles(t *testing.T) {
	for _, hard := range []bool{false, true} {
		t.Run(map[bool]string{false: "symlink", true: "hardlink"}[hard], func(t *testing.T) {
			source := filepath.Join(t.TempDir(), "project-file")
			if err := os.WriteFile(source, []byte("preserve user work"), 0600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "identity-tail")
			link := os.Symlink
			if hard {
				link = os.Link
			}
			if err := link(source, path); err != nil {
				t.Fatal(err)
			}
			w, err := NewTail(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			if data, err := os.ReadFile(source); err != nil || string(data) != "preserve user work" {
				t.Fatalf("initialization followed the old link: %q, %v", data, err)
			}
		})
	}
}
