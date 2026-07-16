// Package transcript tests (Epic 3: transcript capture, build-plan.md).
//
// These tests are white-box (package transcript) and written BEFORE any
// implementation exists. They pin the frozen public API:
//
//	type Config struct {
//	    MaxBytes int64 // rotation threshold per file
//	    MaxFiles int   // rotated generations kept (current + N-1 older)
//	}
//	func New(path string, cfg Config) (*Writer, error)
//	func (w *Writer) Write(p []byte) (int, error)
//	func (w *Writer) Close() error
//
// plus two additions required by E3.4/E3.5 (recorded in implementation-goals.md
// as part of this epic's contract, not a deviation from the frozen surface):
//
//	func (w *Writer) Dropped() int64 // cumulative bytes never handed to the sink
//	func (w *Writer) Flush() error   // blocks until all previously-accepted
//	                                 // Write calls have reached the sink (best-
//	                                 // effort fsync); needed for deterministic
//	                                 // tests of an otherwise async writer.
//
// # Seam the implementation MUST provide (in-package, unexported)
//
// Write must never block or error on a stalled/failing disk (S9, E3.5), which
// means the real implementation buffers internally and drains asynchronously
// into some destination. To inject sink stalls/failures deterministically
// without touching a real filesystem, the implementation must expose exactly
// this seam:
//
//	// sink is the destination a Writer drains its internal buffer into.
//	// Production wraps the rotating on-disk file(s); tests substitute a fake.
//	type sink interface {
//	    Write(p []byte) (int, error)
//	    Sync() error
//	    Close() error
//	}
//
//	// newSink constructs the production sink for New(path, cfg). It is a var
//	// (not a plain function) so tests can save the original, point it at a
//	// fake for the duration of one test, and restore it afterward.
//	var newSink = func(path string, cfg Config) (sink, error) { ... }
//
//	// bufCap is the bounded internal buffer capacity, in bytes, that a Writer
//	// queues ahead of the sink before it starts dropping the incoming tail
//	// (E3.5). Production sets a fixed default; tests shrink it via this var
//	// to get a small, deterministic K.
//	var bufCap = <production default, e.g. 64*1024>
//
// fakeSink (below) implements sink for tests. Rotation/collapse/perms/crash
// tests do NOT touch this seam at all — they call New with the real
// production sink and inspect the resulting files on disk directly, exactly
// as a recovering process would.
package transcript

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

// fakeSink is the test double for the sink seam described above.
type fakeSink struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	writeErr error           // if set, every Write fails with this error (disk-full simulation)
	block    <-chan struct{} // if set, every Write blocks until this channel closes (wedged-sink simulation)
	slow     time.Duration   // if set, every Write sleeps this long first (slow-disk simulation)
}

func (f *fakeSink) Write(p []byte) (int, error) {
	if f.block != nil {
		<-f.block
	}
	if f.slow > 0 {
		time.Sleep(f.slow)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	return f.buf.Write(p)
}

func (f *fakeSink) Sync() error { return nil }
func (f *fakeSink) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return nil
}

// installFakeSink points the package's newSink seam at fs for the duration of
// one test and returns a restore func. Callers are responsible for unblocking
// any gated fakeSink (closing its block channel) before the test ends, so the
// drain goroutine can exit cleanly.
func installFakeSink(fs *fakeSink) (restore func()) {
	orig := newSink
	newSink = func(path string, cfg Config) (sink, error) { return fs, nil }
	return func() { newSink = orig }
}

// waitFor polls cond until it returns true or timeout elapses, returning
// whether cond was observed true. Used instead of a fixed sleep so tests are
// fast when the condition is met quickly and don't flake under load.
func waitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(time.Millisecond)
	}
}

// ---------------------------------------------------------------------------
// E3.1 — size cap + rotation, boundary-tested at exactly MaxBytes and MaxBytes+1.
// Naming convention pinned: current file at path; most recently rotated file
// is path.1; older generations shift to path.2, path.3, ...; at most
// cfg.MaxFiles files (current + rotated) ever exist simultaneously.
// ---------------------------------------------------------------------------

func TestRotatesAtExactlyMaxBytesBoundary(t *testing.T) { // E3.1
	dir := t.TempDir()
	path := filepath.Join(dir, "session.log")
	w, err := New(path, Config{MaxBytes: 16, MaxFiles: 3})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	payload := bytes.Repeat([]byte{'a'}, 16) // exactly MaxBytes in one Write
	if n, err := w.Write(payload); err != nil || n != len(payload) {
		t.Fatalf("Write = (%d, %v), want (%d, nil)", n, err, len(payload))
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	rotated, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("read rotated file %s.1: %v", path, err)
	}
	if !bytes.Equal(rotated, payload) {
		t.Fatalf("rotated file = %q, want %q (reaching MaxBytes exactly must rotate)", rotated, payload)
	}
	cur, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read current file: %v", err)
	}
	if len(cur) != 0 {
		t.Fatalf("current file after rotation = %d bytes, want 0 (fresh file for further writes)", len(cur))
	}
}

func TestRotatesJustPastMaxBytesBoundary(t *testing.T) { // E3.1
	dir := t.TempDir()
	path := filepath.Join(dir, "session.log")
	w, err := New(path, Config{MaxBytes: 16, MaxFiles: 3})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	first := bytes.Repeat([]byte{'a'}, 15) // MaxBytes-1: must NOT rotate yet
	if _, err := w.Write(first); err != nil {
		t.Fatalf("Write(first): %v", err)
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if _, err := os.Stat(path + ".1"); err == nil {
		t.Fatalf("rotated prematurely at %d/%d bytes", len(first), 16)
	}

	second := []byte("bb") // pushes total to 17 = MaxBytes+1
	if _, err := w.Write(second); err != nil {
		t.Fatalf("Write(second): %v", err)
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	want := append(append([]byte{}, first...), second...)
	rotated, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("read rotated file: %v", err)
	}
	if !bytes.Equal(rotated, want) {
		t.Fatalf("rotated file = %q, want %q (MaxBytes+1 must rotate, keeping the whole write together)", rotated, want)
	}
	cur, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read current file: %v", err)
	}
	if len(cur) != 0 {
		t.Fatalf("current file after rotation = %d bytes, want 0", len(cur))
	}
}

func TestRotationCapsTotalFilesAtMaxFiles(t *testing.T) { // E3.1
	dir := t.TempDir()
	path := filepath.Join(dir, "session.log")
	const maxBytes = 4
	const maxFiles = 3 // current + path.1 + path.2; path.3 must never exist
	w, err := New(path, Config{MaxBytes: maxBytes, MaxFiles: maxFiles})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	// Force 5 rotations (well past MaxFiles) plus a partial current generation.
	for i := 0; i < 5; i++ {
		gen := bytes.Repeat([]byte{byte('A' + i)}, maxBytes)
		if _, err := w.Write(gen); err != nil {
			t.Fatalf("Write generation %d: %v", i, err)
		}
	}
	if _, err := w.Write([]byte("z")); err != nil {
		t.Fatalf("Write partial current: %v", err)
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	for _, suffix := range []string{"", ".1", ".2"} {
		if _, err := os.Stat(path + suffix); err != nil {
			t.Fatalf("expected %s%s to exist: %v", path, suffix, err)
		}
	}
	if _, err := os.Stat(path + ".3"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("path.3 must never exist with MaxFiles=%d: stat err=%v", maxFiles, err)
	}
}

func TestRotatedFileNamingConvention(t *testing.T) { // E3.1
	dir := t.TempDir()
	path := filepath.Join(dir, "session.log")
	w, err := New(path, Config{MaxBytes: 4, MaxFiles: 3})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	for _, gen := range [][]byte{[]byte("AAAA"), []byte("BBBB"), []byte("CCCC")} {
		if _, err := w.Write(gen); err != nil {
			t.Fatalf("Write(%q): %v", gen, err)
		}
		if err := w.Flush(); err != nil {
			t.Fatalf("Flush: %v", err)
		}
	}

	// AAAA, BBBB, CCCC each exactly fill MaxBytes, so each write rotates: the
	// most recently rotated generation is CCCC (path.1); BBBB shifted down to
	// path.2; AAAA fell off the end (MaxFiles=3 keeps only 2 rotated + current).
	got1, err := os.ReadFile(path + ".1")
	if err != nil || !bytes.Equal(got1, []byte("CCCC")) {
		t.Fatalf("path.1 = %q, err=%v; want %q (most recently rotated generation)", got1, err, "CCCC")
	}
	got2, err := os.ReadFile(path + ".2")
	if err != nil || !bytes.Equal(got2, []byte("BBBB")) {
		t.Fatalf("path.2 = %q, err=%v; want %q (previous generation, shifted down)", got2, err, "BBBB")
	}
	if _, err := os.Stat(path + ".3"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("path.3 must not exist: MaxFiles=3 caps total generations, AAAA must be dropped")
	}
	cur, err := os.ReadFile(path)
	if err != nil || len(cur) != 0 {
		t.Fatalf("current file = %q, err=%v; want empty (CCCC just rotated out of it)", cur, err)
	}
}

// ---------------------------------------------------------------------------
// E3.2 — spinner/redraw collapse. Heuristic pinned: a frame is one Write call;
// a frame is a "repaint frame" iff it begins with ESC[H (cursor-home) or
// begins with \r and contains no \n ("carriage-return-only redraw"). When both
// the current AND the immediately preceding frame are repaint frames, the
// still-unflushed previous frame is replaced (not appended) by the current
// one. A non-repaint frame (or Close/Flush) commits whatever repaint frame is
// currently pending.
// ---------------------------------------------------------------------------

const spinnerFixtureFrames = 100

func spinnerFrame(n int) []byte {
	return []byte(fmt.Sprintf("\rspinner-frame-%d", n))
}

func TestSpinnerCollapseCarriageReturnFrames(t *testing.T) { // E3.2
	dir := t.TempDir()
	path := filepath.Join(dir, "session.log")
	w, err := New(path, Config{MaxBytes: 1 << 20, MaxFiles: 2})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	maxFrameLen := len(spinnerFrame(spinnerFixtureFrames - 1))
	const collapseBudgetFrames = 3 // "a couple of frames' worth" (E3.2)
	budget := collapseBudgetFrames * maxFrameLen

	for i := 0; i < spinnerFixtureFrames; i++ {
		if _, err := w.Write(spinnerFrame(i)); err != nil {
			t.Fatalf("Write frame %d: %v", i, err)
		}
	}
	trailing := []byte("\ndone\n") // normal, newline-terminated line: not a repaint frame
	if _, err := w.Write(trailing); err != nil {
		t.Fatalf("Write trailing line: %v", err)
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}

	lastFrame := spinnerFrame(spinnerFixtureFrames - 1)
	if !bytes.Contains(got, lastFrame) {
		t.Fatalf("on-disk content missing final spinner frame %q byte-identical; got %q", lastFrame, got)
	}
	if !bytes.HasSuffix(got, trailing) {
		t.Fatalf("on-disk content = %q, want it to end with the preserved trailing line %q", got, trailing)
	}
	if len(got) > budget+len(trailing) {
		t.Fatalf("on-disk bytes = %d, want <= %d (collapse budget) + %d (trailing line); "+
			"%d repaint frames were not collapsed before disk", len(got), budget, len(trailing), spinnerFixtureFrames)
	}
}

func cursorHomeFrame(n int) []byte {
	return []byte(fmt.Sprintf("\x1b[Hspinner-frame-%d", n))
}

func TestSpinnerCollapseCursorHomeFrames(t *testing.T) { // E3.2
	dir := t.TempDir()
	path := filepath.Join(dir, "session.log")
	w, err := New(path, Config{MaxBytes: 1 << 20, MaxFiles: 2})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	const frames = 10
	for i := 0; i < frames; i++ {
		if _, err := w.Write(cursorHomeFrame(i)); err != nil {
			t.Fatalf("Write frame %d: %v", i, err)
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	last := cursorHomeFrame(frames - 1)
	if !bytes.Equal(got, last) {
		t.Fatalf("ESC[H repaint frames not collapsed: on-disk = %q, want exactly the final frame %q", got, last)
	}
}

func TestFrameWithEmbeddedNewlineIsNotTreatedAsRepaint(t *testing.T) { // E3.2
	dir := t.TempDir()
	path := filepath.Join(dir, "session.log")
	w, err := New(path, Config{MaxBytes: 1 << 20, MaxFiles: 2})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	first := []byte("\rspinner-frame-0")          // pure repaint frame: \r, no \n
	second := []byte("\rpartial\nrest-of-line\n") // starts with \r but contains \n: not a "carriage-return-only redraw"
	if _, err := w.Write(first); err != nil {
		t.Fatalf("Write(first): %v", err)
	}
	if _, err := w.Write(second); err != nil {
		t.Fatalf("Write(second): %v", err)
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	want := append(append([]byte{}, first...), second...)
	if !bytes.Equal(got, want) {
		t.Fatalf("a frame containing \\n must not collapse the preceding repaint frame: got %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// E3.3 — files (including rotated ones) created 0600, verified under a forced
// permissive umask so the assertion proves the writer sets perms explicitly.
// ---------------------------------------------------------------------------

func TestFileCreatedWithMode0600UnderPermissiveUmask(t *testing.T) { // E3.3
	old := syscall.Umask(0)
	defer syscall.Umask(old)

	dir := t.TempDir()
	path := filepath.Join(dir, "session.log")
	w, err := New(path, Config{MaxBytes: 1 << 20, MaxFiles: 2})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("file mode = %o, want 0600 (umask forced to 0)", perm)
	}
}

func TestRotatedFilesInheritMode0600(t *testing.T) { // E3.3
	old := syscall.Umask(0)
	defer syscall.Umask(old)

	dir := t.TempDir()
	path := filepath.Join(dir, "session.log")
	w, err := New(path, Config{MaxBytes: 4, MaxFiles: 2})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	if _, err := w.Write([]byte("AAAA")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	info, err := os.Stat(path + ".1")
	if err != nil {
		t.Fatalf("Stat rotated file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("rotated file mode = %o, want 0600", perm)
	}
}

// ---------------------------------------------------------------------------
// E3.4 — crash tolerance: content flushed before an ungraceful death (no
// Close) is a readable, uncorrupted prefix when the path is reopened cold.
// ---------------------------------------------------------------------------

func TestCrashWithoutCloseLeavesReadablePrefix(t *testing.T) { // E3.4
	dir := t.TempDir()
	path := filepath.Join(dir, "session.log")
	w, err := New(path, Config{MaxBytes: 1 << 20, MaxFiles: 2})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	line1, line2, line3 := []byte("line1\n"), []byte("line2\n"), []byte("line3\n")
	if _, err := w.Write(line1); err != nil {
		t.Fatalf("Write(line1): %v", err)
	}
	if _, err := w.Write(line2); err != nil {
		t.Fatalf("Write(line2): %v", err)
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("Flush: %v", err) // deterministic sync point: line1+line2 are guaranteed on disk
	}
	flushed := append(append([]byte{}, line1...), line2...)

	// Simulate the crash: one more write is accepted (fire-and-forget, may or
	// may not reach the sink) and then the Writer is simply abandoned, with no
	// call to Close — exactly what happens when the process is killed here.
	if _, err := w.Write(line3); err != nil {
		t.Fatalf("Write(line3): %v", err)
	}

	// A recovering process reopens the path cold, not through the abandoned Writer.
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reopen path after simulated crash: %v", err)
	}
	if !bytes.HasPrefix(got, flushed) {
		t.Fatalf("on-disk content = %q, want the flushed prefix %q intact (no corruption before the truncation point)", got, flushed)
	}
}

// ---------------------------------------------------------------------------
// E3.5 — disk-full degradation: the sink fails every write; Write must still
// report (len(p), nil) to the caller and never panic, with the loss recorded
// via Dropped(). Separately: with a permanently wedged sink and a shrunk
// buffer capacity K, writes beyond K must be dropped (tail-drop), and no
// Write call may block on the wedged sink (S9).
// ---------------------------------------------------------------------------

var errDiskFull = errors.New("fake sink: disk full")

func TestWriteNeverErrorsWhenSinkFailsEveryCall(t *testing.T) { // E3.5
	restore := installFakeSink(&fakeSink{writeErr: errDiskFull})
	defer restore()

	w, err := New(filepath.Join(t.TempDir(), "session.log"), Config{MaxBytes: 1 << 20, MaxFiles: 2})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	payload := []byte("this write can never reach disk")
	n, err := w.Write(payload)
	if err != nil {
		t.Fatalf("Write returned an error (%v); the drain path must never surface sink errors to the caller (S9)", err)
	}
	if n != len(payload) {
		t.Fatalf("Write returned n=%d, want len(p)=%d even though the sink rejects everything", n, len(payload))
	}

	if !waitFor(2*time.Second, func() bool { return w.Dropped() >= int64(len(payload)) }) {
		t.Fatalf("Dropped() never reached %d after a permanently failing sink", len(payload))
	}
}

func TestDropsIncomingTailWhenBufferFull(t *testing.T) { // E3.5
	const bufK = 4096
	origCap := bufCap
	bufCap = bufK
	defer func() { bufCap = origCap }()

	gate := make(chan struct{})
	fs := &fakeSink{block: gate}
	restore := installFakeSink(fs)
	defer restore()
	defer close(gate) // release the wedged sink so the drain goroutine (and Close) can proceed

	w, err := New(filepath.Join(t.TempDir(), "session.log"), Config{MaxBytes: 1 << 20, MaxFiles: 2})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	chunk := bytes.Repeat([]byte{'x'}, 256)
	const totalWrite = 10 * bufK // far beyond capacity while the sink is wedged

	start := time.Now()
	var written int
	for written < totalWrite {
		n, err := w.Write(chunk)
		if err != nil {
			t.Fatalf("Write: %v", err)
		}
		if n != len(chunk) {
			t.Fatalf("Write n=%d, want %d (caller must always see its full length written)", n, len(chunk))
		}
		written += n
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("writes took %s while the sink was permanently wedged; Write must never block on a stalled sink (S9)", elapsed)
	}

	// At most bufK bytes (plus the one chunk already handed to the wedged sink
	// call) can ever be retained; everything else is incoming tail that must
	// be dropped, not silently buffered without bound.
	minWantDropped := int64(totalWrite - bufK - len(chunk))
	if dropped := w.Dropped(); dropped < minWantDropped {
		t.Fatalf("Dropped() = %d, want >= %d (writes beyond buffer capacity K=%d must be dropped)", dropped, minWantDropped, bufK)
	}
}

// ---------------------------------------------------------------------------
// Concurrency: Write from multiple goroutines while the sink is slow (not
// wedged) must stay -race clean and complete within a bounded time — no
// deadlock, no data race on the shared Dropped counter or internal buffer.
// ---------------------------------------------------------------------------

func TestConcurrentWritesWithSlowSinkNoDeadlock(t *testing.T) {
	fs := &fakeSink{slow: 5 * time.Millisecond} // every sink Write sleeps briefly: simulates a slow disk
	restore := installFakeSink(fs)
	defer restore()

	w, err := New(filepath.Join(t.TempDir(), "session.log"), Config{MaxBytes: 1 << 20, MaxFiles: 2})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	const goroutines = 8
	const writesPerGoroutine = 200
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < writesPerGoroutine; i++ {
				if _, err := w.Write([]byte(fmt.Sprintf("g%d-%d\n", id, i))); err != nil {
					t.Errorf("goroutine %d write %d: %v", id, i, err)
				}
			}
		}(g)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent writes did not complete within bound; suspect deadlock")
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- w.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return within bound; suspect deadlock")
	}
}
