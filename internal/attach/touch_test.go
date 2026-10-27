package attach

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/Nathandela/swarm/internal/vt"
	"github.com/charmbracelet/x/ansi"
	"io"
	"testing"
	"time"
)

func TestTouchClickRequiresCompleteGesture(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		detach      bool
	}{
		{"click", "\x1b[<0;2;24M\x1b[<0;2;24m", true},
		{"release", "\x1b[<0;2;24m", false},
		{"drag", "\x1b[<0;2;24M\x1b[<32;3;24M\x1b[<0;2;24m", false},
		{"modified", "\x1b[<4;2;24M\x1b[<4;2;24m", false},
		{"paste", "\x1b[200~\x1b[<0;2;24M\x1b[<0;2;24m\x1b[201~", false},
		{"string", "\x1b]0;\x1b[<0;2;24M\x1b[<0;2;24m\a", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			touch := &touchButton{enabled: true, rows: 24}
			var out bytes.Buffer
			f := newDetachInputFilter(DefaultDetachKey, func(p []byte) { out.Write(p) }, nil)
			f.mouse = touch.input
			detached := false
			for _, b := range []byte(tc.input) {
				if f.Feed([]byte{b}) {
					detached = true
					break
				}
			}
			if detached != tc.detach {
				t.Fatalf("detach=%v want %v", detached, tc.detach)
			}
			if tc.name == "paste" || tc.name == "string" {
				if out.String() != tc.input {
					t.Fatalf("payload changed: %q", out.String())
				}
			}
		})
	}
}

func TestTouchBodyForwarding(t *testing.T) {
	for _, tracking := range []int{0, 1000, 1002, 1003} {
		touch := &touchButton{enabled: true, rows: 24, tracking: tracking}
		press := []byte("\x1b[<0;2;2M")
		motion := []byte("\x1b[<32;3;2M")
		if handled, _ := touch.input(press); handled != (tracking == 0) {
			t.Fatalf("tracking %d press handled=%v", tracking, handled)
		}
		if handled, _ := touch.input(motion); handled != (tracking == 0 || tracking == 1000) {
			t.Fatalf("tracking %d motion handled=%v", tracking, handled)
		}
	}
}

func TestTouchHintHasPriorityAndCellWidth(t *testing.T) {
	for _, cols := range []int{7, 8, 15, 40} {
		s := hintText("界界界界界界界界界界", DefaultDetachKey, cols, true)
		if len(s) < 7 || s[:7] != "[swarm]" {
			t.Fatalf("button lost: %q", s)
		}
		if w := ansi.StringWidth(s); w > cols {
			t.Fatalf("width %d exceeds %d: %q", w, cols, s)
		}
	}
}

func TestTouchRunClickAndReadOnly(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		t.Run(fmt.Sprint(readOnly), func(t *testing.T) {
			term := newFakeTerm(80, 24)
			sess := newFakeSession(mustSnap(t, "GRID"))
			ch := runInBackground(Config{Term: term, Session: sess, Chrome: true, TouchBack: true, ReadOnly: readOnly})
			waitOut(t, term, func(b []byte) bool { return bytes.Contains(b, []byte("[swarm]")) })
			term.feed([]byte("\x1b[<0;2;24M\x1b[<0;2;24m"))
			if result := waitResult(t, ch); result.reason != ReasonDetached {
				t.Fatalf("result=%+v", result)
			}
			if got := sess.inputBytes(); len(got) != 0 {
				t.Fatalf("mouse entered PTY: %q", got)
			}
		})
	}
}

func TestTouchNativeGestureKeepsRelease(t *testing.T) {
	touch := &touchButton{enabled: true, rows: 24, tracking: 1002}
	for _, seq := range []string{"\x1b[<0;2;2M", "\x1b[<32;3;24M", "\x1b[<0;3;24m"} {
		if handled, detach := touch.input([]byte(seq)); handled || detach {
			t.Fatalf("native gesture intercepted: %q", seq)
		}
	}
	for _, seq := range []string{"\x1b[<0;2;24M", "\x1b[<32;3;2M", "\x1b[<0;3;2m"} {
		if handled, detach := touch.input([]byte(seq)); !handled || detach {
			t.Fatalf("footer gesture leaked: %q", seq)
		}
	}
}

func TestTouchMouseQueryReleasedBeforeForwarding(t *testing.T) {
	var out bytes.Buffer
	gate := mouseOutput{write: func(p []byte) { out.Write(p) }, fallback: func() { out.WriteString("RESTORE") }}
	for _, part := range []string{"abc\x1b[?10", "02$", "pdef"} {
		gate.feed([]byte(part))
	}
	if got, want := out.String(), "abcRESTORE\x1b[?1002$pdef"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	out.Reset()
	gate.feed([]byte("\x1b]0;\x1b[?1002$p\a"))
	if bytes.Contains(out.Bytes(), []byte("RESTORE")) {
		t.Fatalf("string query acted on: %q", out.Bytes())
	}
}

func TestTouchUnsupportedAndLegacySnapshotDoNotCapture(t *testing.T) {
	for _, mouse := range []any{nil, map[string]any{"known": true, "tracking": 1, "encoding": 0}} {
		raw := mustSnap(t, "GRID")
		var snap map[string]any
		if err := json.Unmarshal(raw, &snap); err != nil {
			t.Fatal(err)
		}
		if mouse == nil {
			delete(snap, "mouse")
		} else {
			snap["mouse"] = mouse
		}
		raw, err := json.Marshal(snap)
		if err != nil {
			t.Fatal(err)
		}
		term := newFakeTerm(80, 24)
		sess := newFakeSession(raw)
		ch := runInBackground(Config{Term: term, Session: sess, Chrome: true, TouchBack: true})
		out := waitOut(t, term, func(b []byte) bool { return bytes.Contains(b, []byte("returns to swarm")) })
		if bytes.Contains(out, []byte("[swarm]")) || bytes.Contains(out, []byte("\x1b[?1002h")) {
			t.Fatalf("unsupported capture: %q", out)
		}
		term.feed([]byte{DefaultDetachKey})
		_ = waitResult(t, ch)
	}
}

func TestTouchResizeCancelsGesture(t *testing.T) {
	touch := &touchButton{enabled: true, capturing: true, rows: 24}
	touch.input([]byte("\x1b[<0;2;24M"))
	touch.resize(80, 30)
	if handled, detach := touch.input([]byte("\x1b[<0;2;24m")); !handled || detach {
		t.Fatalf("resize release handled=%v detach=%v", handled, detach)
	}
}

func TestWriteAllReportsIncompletePaint(t *testing.T) {
	if writeAll(zeroWriter{}, []byte("footer")) {
		t.Fatal("zero write reported success")
	}
	if writeAll(errorWriter{}, []byte("footer")) {
		t.Fatal("error write reported success")
	}
}

type zeroWriter struct{}

func (zeroWriter) Write([]byte) (int, error) { return 0, nil }

type errorWriter struct{}

func (errorWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func touchSnapshot(t *testing.T, state vt.MouseState) []byte {
	t.Helper()
	snap, err := vt.DecodeSnapshot(mustSnap(t, "GRID"))
	if err != nil {
		t.Fatal(err)
	}
	snap.Mouse = &state
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestTouchRunPreservesNativeBodyReports(t *testing.T) {
	for _, tracking := range []uint16{0, vt.MouseTrackNormal, vt.MouseTrackButton, vt.MouseTrackAny} {
		t.Run(fmt.Sprint(tracking), func(t *testing.T) {
			term := newFakeTerm(80, 24)
			sess := newFakeSession(touchSnapshot(t, vt.MouseState{Known: true, Tracking: tracking, Encoding: vt.MouseEncodeSGR}))
			ch := runInBackground(Config{Term: term, Session: sess, Chrome: true, TouchBack: true})
			waitOut(t, term, func(b []byte) bool { return bytes.Contains(b, []byte("[swarm]")) })
			press := "\x1b[<0;2;2M"
			motion := "\x1b[<32;3;2M"
			release := "\x1b[<0;3;24m"
			term.feed([]byte(press + motion + release + "x"))
			expected := "x"
			if tracking != 0 {
				expected = press + release + "x"
				if tracking != vt.MouseTrackNormal {
					expected = press + motion + release + "x"
				}
			}
			eventually(t, func() bool { return string(sess.inputBytes()) == expected })
			term.feed([]byte{DefaultDetachKey})
			_ = waitResult(t, ch)
		})
	}
}

func TestTouchRunQueryFallbackRestoresNativeBeforeSplitQuery(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession(touchSnapshot(t, vt.MouseState{Known: true}))
	ch := runInBackground(Config{Term: term, Session: sess, Chrome: true, TouchBack: true})
	waitOut(t, term, func(b []byte) bool { return bytes.Contains(b, []byte("[swarm]")) })
	sess.frames <- []byte("\x1b[?10")
	sess.frames <- []byte("02$pQUERY")
	out := waitOut(t, term, func(b []byte) bool { return bytes.Contains(b, []byte("QUERY")) })
	at := bytes.Index(out, []byte("\x1b[?1002$pQUERY"))
	reset := []byte("\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l")
	if at < 0 || !bytes.HasSuffix(out[:at], reset) {
		t.Fatalf("query preceded native restore incorrectly: %q", out)
	}
	term.feed([]byte{DefaultDetachKey})
	_ = waitResult(t, ch)
}

func TestTouchOutputPreservesUTF8AndStringPayloads(t *testing.T) {
	for _, payload := range []string{
		"招\u009bZ",
		"\x9dtitle;\x1b[?1002$p\a",
		"\x90data\a\x1b[?1002$p\x9c",
		"\x1bPdata\a\x1b[?1002$p\x1b\\",
	} {
		var out bytes.Buffer
		fallback := false
		gate := mouseOutput{write: func(p []byte) { out.Write(p) }, fallback: func() { fallback = true }}
		for _, b := range []byte(payload) {
			gate.feed([]byte{b})
		}
		if out.String() != payload || fallback {
			t.Fatalf("payload=%q output=%q fallback=%v", payload, out.String(), fallback)
		}
	}
}

func TestTouchRunUnknownNativeStateDetachesBeforeOperation(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession(touchSnapshot(t, vt.MouseState{Known: true}))
	ch := runInBackground(Config{Term: term, Session: sess, Chrome: true, TouchBack: true})
	waitOut(t, term, func(b []byte) bool { return bytes.Contains(b, []byte("[swarm]")) })
	sess.frames <- []byte("\x1b[?1002")
	sess.frames <- []byte("r")
	if result := waitResult(t, ch); result.reason != ReasonDetached {
		t.Fatalf("result=%+v", result)
	}
	if out := term.outBytes(); bytes.Contains(out, []byte("\x1b[?1002r")) {
		t.Fatalf("unknown native operation forwarded: %q", out)
	}
}

func TestTouchMouseQueriesOnlyReleaseForMouseModes(t *testing.T) {
	for _, seq := range []string{"?9$p", "?1002$p", "?25;1006$p"} {
		if !mouseModeQuery([]byte(seq)) {
			t.Fatalf("mouse query missed: %q", seq)
		}
	}
	for _, seq := range []string{"?25$p", "?1004$p", "?1007$p"} {
		if mouseModeQuery([]byte(seq)) {
			t.Fatalf("unrelated query disabled touch: %q", seq)
		}
	}
}

func TestTouchIncompleteFooterPaintCannotDetach(t *testing.T) {
	base := newFakeTerm(80, 24)
	term := footerFailTerm{base}
	sess := newFakeSession(touchSnapshot(t, vt.MouseState{Known: true}))
	ch := runInBackground(Config{Term: term, Session: sess, Chrome: true, TouchBack: true})
	waitOut(t, base, func(b []byte) bool { return bytes.Contains(b, []byte("\x1b[?1002h")) })
	base.feed([]byte("\x1b[<0;2;24M\x1b[<0;2;24mx"))
	eventually(t, func() bool { return string(sess.inputBytes()) == "x" })
	base.feed([]byte{DefaultDetachKey})
	_ = waitResult(t, ch)
}

type footerFailTerm struct{ *fakeTerm }

func (t footerFailTerm) Out() io.Writer { return footerFailWriter{t.out} }

type footerFailWriter struct{ out io.Writer }

func (w footerFailWriter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("[swarm]")) {
		return 0, io.ErrClosedPipe
	}
	return w.out.Write(p)
}

func TestTouchOtherInputCancelsActivation(t *testing.T) {
	for _, middle := range []string{"x", "\x1b[O", "\x1b[I", "\x1b[200~paste\x1b[201~"} {
		touch := &touchButton{enabled: true, rows: 24}
		var out bytes.Buffer
		f := newDetachInputFilter(DefaultDetachKey, func(p []byte) { out.Write(p) }, nil)
		f.mouse = touch.input
		f.activity = touch.cancelPress
		if f.Feed([]byte("\x1b[<0;2;24M" + middle + "\x1b[<0;2;24m")) {
			t.Fatalf("nonmouse input %q did not cancel", middle)
		}
		if out.String() != middle {
			t.Fatalf("input changed: %q want %q", out.String(), middle)
		}
	}
}

func TestTouchWrongReleaseKeepsFooterOwnership(t *testing.T) {
	touch := &touchButton{enabled: true, rows: 24, tracking: 1002}
	for _, seq := range []string{"\x1b[<0;2;24M", "\x1b[<1;2;24m", "\x1b[<0;2;2m"} {
		if handled, detach := touch.input([]byte(seq)); !handled || detach {
			t.Fatalf("footer gesture leaked/detached: %q", seq)
		}
	}
}

func TestTouchMalformedParametersDoNotActivate(t *testing.T) {
	touch := &touchButton{enabled: true, rows: 24}
	for _, seq := range []string{"\x1b[<0;+2;24M", "\x1b[<0; 2;24M", "\x1b[<0;0;24M"} {
		if handled, detach := touch.input([]byte(seq)); handled || detach {
			t.Fatalf("malformed mouse accepted: %q", seq)
		}
	}
	if _, detach := touch.input([]byte("\x1b[<0;2;24m")); detach {
		t.Fatal("malformed press armed")
	}
}

func TestTouchC1InputAndPaste(t *testing.T) {
	touch := &touchButton{enabled: true, rows: 24}
	f := newDetachInputFilter(DefaultDetachKey, func([]byte) {}, nil)
	f.mouse = touch.input
	if !f.Feed([]byte("\x9b<0;2;24M\x9b<0;2;24m")) {
		t.Fatal("C1 mouse click did not detach")
	}
	var out bytes.Buffer
	f = newDetachInputFilter(DefaultDetachKey, func(p []byte) { out.Write(p) }, nil)
	f.mouse = touch.input
	paste := []byte("\x9b200~\x9b<0;2;24M\x9b<0;2;24m\x9b201~")
	for _, b := range paste {
		if f.Feed([]byte{b}) {
			t.Fatal("C1 pasted click detached")
		}
	}
	if !bytes.Equal(out.Bytes(), paste) {
		t.Fatalf("paste changed: %q", out.Bytes())
	}
}

func TestTouchOwnedReleaseConsumedAfterCaptureDisabled(t *testing.T) {
	touch := &touchButton{enabled: true, rows: 24, tracking: 1002}
	touch.input([]byte("\x1b[<0;2;24M"))
	touch.enabled = false
	touch.capturing = false
	touch.pressed = false
	if handled, detach := touch.input([]byte("\x1b[<0;2;2m")); !handled || detach {
		t.Fatalf("owned release after fallback handled=%v detach=%v", handled, detach)
	}
}

func TestTouchResizeDuringBlockedPaintCannotPublishOldHitbox(t *testing.T) {
	base := newFakeTerm(80, 24)
	writer := &blockingTouchWriter{out: base.out, blocked: make(chan struct{}), allow: make(chan struct{})}
	term := blockingTouchTerm{base, writer}
	sess := &blockingTouchResize{fakeSession: newFakeSession(touchSnapshot(t, vt.MouseState{Known: true})), entered: make(chan struct{}), allow: make(chan struct{})}
	ch := runInBackground(Config{Term: term, Session: sess, Chrome: true, TouchBack: true})
	waitOut(t, base, func(b []byte) bool { return bytes.Contains(b, []byte("[swarm]")) })
	sess.frames <- []byte("\x1b[2J")
	select {
	case <-writer.blocked:
	case <-time.After(2 * time.Second):
		t.Fatal("repaint did not block")
	}
	base.setSize(80, 30)
	select {
	case <-sess.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("resize did not cancel hitbox")
	}
	close(writer.allow)
	sess.frames <- []byte("ACK")
	waitOut(t, base, func(b []byte) bool { return bytes.Contains(b, []byte("ACK")) })
	base.feed([]byte("\x1b[<0;2;24M\x1b[<0;2;24mx"))
	eventually(t, func() bool { return string(sess.inputBytes()) == "x" })
	close(sess.allow)
	base.feed([]byte{DefaultDetachKey})
	_ = waitResult(t, ch)
}

type blockingTouchTerm struct {
	*fakeTerm
	writer io.Writer
}

func (t blockingTouchTerm) Out() io.Writer { return t.writer }

type blockingTouchWriter struct {
	out            io.Writer
	paints         int
	blocked, allow chan struct{}
}

func (w *blockingTouchWriter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("[swarm]")) {
		w.paints++
		if w.paints == 2 {
			close(w.blocked)
			<-w.allow
		}
	}
	return w.out.Write(p)
}

type blockingTouchResize struct {
	*fakeSession
	entered, allow chan struct{}
}

func (s *blockingTouchResize) Resize(cols, rows int) error {
	if rows == 29 {
		close(s.entered)
		<-s.allow
	}
	return s.fakeSession.Resize(cols, rows)
}

func TestTouchOutputRestartedQueryCannotBypassRestore(t *testing.T) {
	for _, abort := range []string{"\x1b[?1000", "\x1b[?1000\x18", "\x1b[?1000\x1a"} {
		var out bytes.Buffer
		gate := mouseOutput{write: func(p []byte) { out.Write(p) }, fallback: func() { out.WriteString("RESTORE") }}
		input := abort + "\x1b[?1006$p"
		for _, b := range []byte(input) {
			gate.feed([]byte{b})
		}
		want := abort + "RESTORE\x1b[?1006$p"
		if abort == "\x1b[?1000" {
			want = "RESTORE" + abort + "\x1b[?1006$p"
		}
		if got := out.String(); got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	}
}

func TestTouchRunDefersChromeAcrossUTF8AndC1String(t *testing.T) {
	for _, parts := range [][2]string{{"\xe6", "\x8b\x9bEND"}, {"\x9dtitle;", "\x9cEND"}} {
		t.Run(fmt.Sprintf("%x", parts[0]), func(t *testing.T) {
			term := newFakeTerm(80, 24)
			sess := newFakeSession(touchSnapshot(t, vt.MouseState{Known: true}))
			ch := runInBackground(Config{Term: term, Session: sess, Chrome: true, TouchBack: true})
			waitOut(t, term, func(b []byte) bool { return bytes.Contains(b, []byte("[swarm]")) })
			baseline := len(term.outBytes())
			sess.frames <- []byte(parts[0])
			waitOut(t, term, func(b []byte) bool { return len(b) >= baseline+len(parts[0]) })
			term.setSize(90, 30)
			eventually(t, func() bool {
				for _, size := range sess.resizeCalls() {
					if size == [2]int{90, 29} {
						return true
					}
				}
				return false
			})
			time.Sleep(chromeHealInterval + 20*time.Millisecond)
			if got := term.outBytes()[baseline:]; string(got) != parts[0] {
				t.Fatalf("chrome injected inside incomplete text/string: %q", got)
			}
			sess.frames <- []byte(parts[1])
			waitOut(t, term, func(b []byte) bool { return bytes.Contains(b, []byte("\x1b[30;1H")) })
			if got := term.outBytes()[baseline:]; !bytes.HasPrefix(got, []byte(parts[0]+parts[1])) {
				t.Fatalf("stream split by chrome: %q", got)
			}
			term.feed([]byte{DefaultDetachKey})
			_ = waitResult(t, ch)
		})
	}
}

func TestTouchRunFlushesHeldPrefixOnceBeforeTeardown(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession(touchSnapshot(t, vt.MouseState{Known: true}))
	ch := runInBackground(Config{Term: term, Session: sess, Chrome: true, TouchBack: true})
	waitOut(t, term, func(b []byte) bool { return bytes.Contains(b, []byte("[swarm]")) })
	prefix := "\x1b[?1002$"
	sess.frames <- []byte(prefix)
	sess.endSession()
	_ = waitResult(t, ch)
	out := term.outBytes()
	if bytes.Count(out, []byte(prefix)) != 1 || !bytes.Contains(out, []byte(prefix+"\x1b[r")) {
		t.Fatalf("prefix not flushed once before cleanup: %q", out)
	}
}

func TestTouchFailedModeWriteCannotArmOnLaterRepaint(t *testing.T) {
	base := newFakeTerm(80, 24)
	writer := &modeFailWriter{out: base.out}
	term := blockingTouchTerm{base, writer}
	sess := newFakeSession(touchSnapshot(t, vt.MouseState{Known: true}))
	ch := runInBackground(Config{Term: term, Session: sess, Chrome: true, TouchBack: true})
	waitOut(t, base, func(b []byte) bool { return bytes.Contains(b, []byte("[swarm]")) })
	sess.frames <- []byte("\x1b[2J")
	waitOut(t, base, func(b []byte) bool { return bytes.Contains(b, []byte("\x1b[2m  ctrl+q")) })
	base.feed([]byte("\x1b[<0;2;24M\x1b[<0;2;24mx"))
	eventually(t, func() bool { return bytes.HasSuffix(sess.inputBytes(), []byte("x")) })
	base.feed([]byte{DefaultDetachKey})
	_ = waitResult(t, ch)
}

type modeFailWriter struct {
	out    io.Writer
	failed bool
}

func (w *modeFailWriter) Write(p []byte) (int, error) {
	if !w.failed && bytes.Contains(p, []byte("\x1b[?1002h")) {
		w.failed = true
		return 0, io.ErrClosedPipe
	}
	return w.out.Write(p)
}

func TestTouchPartialMouseCandidateDoesNotTimeoutIntoPTY(t *testing.T) {
	var out bytes.Buffer
	touch := &touchButton{enabled: true, rows: 24}
	f := newDetachInputFilter(DefaultDetachKey, func(p []byte) { out.Write(p) }, nil)
	f.mouse = touch.input
	f.Feed([]byte("\x1b[<0;2;"))
	f.flushTimedOut(f.timerGen)
	if out.Len() != 0 {
		t.Fatalf("partial mouse leaked on timeout: %q", out.Bytes())
	}
	if !f.Feed([]byte("24M\x1b[<0;2;24m")) {
		t.Fatal("split mouse gesture lost after timeout")
	}
}

func TestTouchEncodingChangeDuringOwnedGestureReturnsBeforeLegacyRelease(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession(touchSnapshot(t, vt.MouseState{Known: true, Tracking: vt.MouseTrackNormal, Encoding: vt.MouseEncodeSGR}))
	ch := runInBackground(Config{Term: term, Session: sess, Chrome: true, TouchBack: true})
	waitOut(t, term, func(b []byte) bool { return bytes.Contains(b, []byte("[swarm]")) })
	term.feed([]byte("\x1b[<0;2;24Mx"))
	eventually(t, func() bool { return string(sess.inputBytes()) == "x" })
	sess.frames <- []byte("\x1b[?1006l")
	result := waitResult(t, ch)
	if result.reason != ReasonDetached || result.err == nil {
		t.Fatalf("encoding transition should explain return: %+v", result)
	}
	if got := sess.inputBytes(); string(got) != "x" {
		t.Fatalf("footer gesture entered native input: %q", got)
	}
}

func TestTouchSplitPasteEndSurvivesTimeout(t *testing.T) {
	for _, csi := range []string{"\x1b[", "\x9b"} {
		var out bytes.Buffer
		touch := &touchButton{enabled: true, rows: 24}
		f := newDetachInputFilter(DefaultDetachKey, func(p []byte) { out.Write(p) }, nil)
		f.mouse = touch.input
		f.Feed([]byte(csi + "200~data" + csi + "20"))
		f.flushTimedOut(f.timerGen)
		if f.Feed([]byte("1~")) {
			t.Fatal("paste end detached")
		}
		if !f.Feed([]byte{DefaultDetachKey}) {
			t.Fatal("split paste end lost after timeout")
		}
		if want := csi + "200~data" + csi + "201~"; out.String() != want {
			t.Fatalf("paste changed: %q want %q", out.String(), want)
		}
	}
}

func TestTouchInitialUnknownStaysLatchedAfterTinyResizeAndReset(t *testing.T) {
	var snapshot map[string]any
	if err := json.Unmarshal(mustSnap(t, "GRID"), &snapshot); err != nil {
		t.Fatal(err)
	}
	delete(snapshot, "mouse")
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	term := newFakeTerm(80, 2)
	sess := newFakeSession(raw)
	ch := runInBackground(Config{Term: term, Session: sess, Chrome: true, TouchBack: true})
	sess.frames <- []byte("\x1bcRESET")
	waitOut(t, term, func(b []byte) bool { return bytes.Contains(b, []byte("RESET")) })
	term.setSize(80, 24)
	out := waitOut(t, term, func(b []byte) bool { return bytes.Contains(b, []byte("returns to swarm")) })
	if bytes.Contains(out, []byte("[swarm]")) {
		t.Fatalf("initial unknown capture recovered unexpectedly: %q", out)
	}
	term.feed([]byte{DefaultDetachKey})
	_ = waitResult(t, ch)
}

func TestTouchNativeStartupCanEnterSGRAfterLegacyTracking(t *testing.T) {
	for _, split := range []bool{false, true} {
		t.Run(fmt.Sprint(split), func(t *testing.T) {
			term := newFakeTerm(80, 24)
			sess := newFakeSession(touchSnapshot(t, vt.MouseState{Known: true}))
			ch := runInBackground(Config{Term: term, Session: sess, Chrome: true, TouchBack: true})
			waitOut(t, term, func(b []byte) bool { return bytes.Contains(b, []byte("[swarm]")) })
			if split {
				sess.frames <- []byte("\x1b[?1003hLEGACY")
				waitOut(t, term, func(b []byte) bool {
					return bytes.Contains(b, []byte("LEGACY")) && bytes.Contains(b, []byte("\x1b[2m  ctrl+q"))
				})
				sess.frames <- []byte("\x1b[?1006hREADY")
			} else {
				sess.frames <- []byte("\x1b[?1003h\x1b[?1006hREADY")
			}
			waitOut(t, term, func(b []byte) bool {
				return bytes.Contains(b, []byte("READY")) && bytes.Count(b, []byte("[swarm]")) >= 2
			})
			term.feed([]byte("\x1b[<0;2;24M\x1b[<0;2;24m"))
			if result := waitResult(t, ch); result.reason != ReasonDetached || result.err != nil {
				t.Fatalf("startup click result=%+v", result)
			}
			if got := sess.inputBytes(); len(got) != 0 {
				t.Fatalf("button mouse leaked: %q", got)
			}
		})
	}
}

func TestTouchTimedOutOrdinaryEscapeCancelsPress(t *testing.T) {
	touch := &touchButton{enabled: true, rows: 24}
	var out bytes.Buffer
	f := newDetachInputFilter(DefaultDetachKey, func(p []byte) { out.Write(p) }, nil)
	f.mouse = touch.input
	f.activity = touch.cancelPress
	f.Feed([]byte("\x1b[<0;2;24M\x1b"))
	f.flushTimedOut(f.timerGen)
	if f.Feed([]byte("\x1b[<0;2;24m")) {
		t.Fatal("ordinary Escape did not cancel activation")
	}
	if out.String() != "\x1b" {
		t.Fatalf("ordinary Escape changed: %q", out.Bytes())
	}
}

func TestTouchLegacyFallbackPreservesNativeCoordinatesAndDropsQueuedSGR(t *testing.T) {
	touch := &touchButton{discardSGR: true, rows: 24}
	var out bytes.Buffer
	f := newDetachInputFilter(DefaultDetachKey, func(p []byte) { out.Write(p) }, nil)
	f.mouse = touch.input
	f.c1Input = touch.c1Input
	f.retainMouse = touch.retainMouse
	legacy := []byte("\x1b[M \x9b<")
	f.Feed(legacy)
	f.flushTimedOut(f.timerGen)
	if !bytes.Equal(out.Bytes(), legacy) {
		t.Fatalf("native legacy coordinates changed: %q", out.Bytes())
	}
	f.Feed([]byte("\x1b[<0;2;2M\x1b[<0;2;2m"))
	if !bytes.Equal(out.Bytes(), legacy) {
		t.Fatalf("queued outer SGR leaked during fallback: %q", out.Bytes())
	}
}

func TestTouchDetachDropsOwnedPartialMouseCandidate(t *testing.T) {
	for _, native := range []int{0, 1002} {
		touch := &touchButton{enabled: true, capturing: true, rows: 24, tracking: native}
		var out bytes.Buffer
		f := newDetachInputFilter(DefaultDetachKey, func(p []byte) { out.Write(p) }, nil)
		f.mouse = touch.input
		f.dropMouseCandidate = touch.dropMouseCandidate
		if native != 0 {
			f.Feed([]byte("\x1b[<0;2;24M"))
		}
		f.Feed([]byte("\x1b[<0;2;"))
		if !f.Feed([]byte{DefaultDetachKey}) {
			t.Fatal("CtrlQ did not detach")
		}
		if out.Len() != 0 {
			t.Fatalf("owned partial report entered PTY: %q", out.Bytes())
		}
	}
}

func TestTouchAmbiguousNativeMasksReturnBeforeReplay(t *testing.T) {
	for _, change := range []string{"\x1b[?1005h", "\x1b[?1002h"} {
		term := newFakeTerm(80, 24)
		sess := newFakeSession(touchSnapshot(t, vt.MouseState{Known: true, Tracking: vt.MouseTrackNormal, Encoding: vt.MouseEncodeSGR}))
		ch := runInBackground(Config{Term: term, Session: sess, Chrome: true, TouchBack: true})
		waitOut(t, term, func(b []byte) bool { return bytes.Contains(b, []byte("[swarm]")) })
		baseline := len(term.outBytes())
		sess.frames <- []byte(change)
		result := waitResult(t, ch)
		if result.reason != ReasonDetached || result.err == nil {
			t.Fatalf("ambiguous transition result=%+v", result)
		}
		if bytes.Contains(term.outBytes()[baseline:], []byte(change)) {
			t.Fatalf("ambiguous mode operation was forwarded: %q", term.outBytes()[baseline:])
		}
	}
}

func TestTouchPixelTransitionDuringCaptureReturns(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession(touchSnapshot(t, vt.MouseState{Known: true}))
	ch := runInBackground(Config{Term: term, Session: sess, Chrome: true, TouchBack: true})
	waitOut(t, term, func(b []byte) bool { return bytes.Contains(b, []byte("[swarm]")) })
	sess.frames <- []byte("\x1b[?1016h")
	result := waitResult(t, ch)
	if result.reason != ReasonDetached || result.err == nil {
		t.Fatalf("pixel transition result=%+v", result)
	}
}

func TestTouchPixelFallbackPreservesNativeReports(t *testing.T) {
	for _, legacyFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(legacyFirst), func(t *testing.T) {
			state := vt.MouseState{Known: true, Tracking: vt.MouseTrackAny, Encoding: vt.MouseEncodePixel}
			if legacyFirst {
				state = vt.MouseState{Known: true}
			}
			term := newFakeTerm(80, 24)
			sess := newFakeSession(touchSnapshot(t, state))
			ch := runInBackground(Config{Term: term, Session: sess, Chrome: true, TouchBack: true})
			waitOut(t, term, func(b []byte) bool { return bytes.Contains(b, []byte("returns to swarm")) })
			if legacyFirst {
				sess.frames <- []byte("\x1b[?1003hLEGACY")
				waitOut(t, term, func(b []byte) bool {
					return bytes.Contains(b, []byte("LEGACY")) && bytes.Contains(b, []byte("\x1b[2m  ctrl+q"))
				})
				sess.frames <- []byte("\x1b[?1016hPIXEL")
				waitOut(t, term, func(b []byte) bool { return bytes.Contains(b, []byte("PIXEL")) })
			}
			input := []byte("\x1b[<0;20;240M\x1b[<0;20;240mx")
			term.feed(input)
			eventually(t, func() bool { return bytes.Equal(sess.inputBytes(), input) })
			term.feed([]byte{DefaultDetachKey})
			_ = waitResult(t, ch)
		})
	}
}

func TestTouchNativeOffQueryFallbackDropsQueuedReports(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession(touchSnapshot(t, vt.MouseState{Known: true}))
	ch := runInBackground(Config{Term: term, Session: sess, Chrome: true, TouchBack: true})
	waitOut(t, term, func(b []byte) bool { return bytes.Contains(b, []byte("[swarm]")) })
	sess.frames <- []byte("\x1b[?1006$pREADY")
	waitOut(t, term, func(b []byte) bool {
		return bytes.Contains(b, []byte("READY")) && bytes.Contains(b, []byte("\x1b[2m  ctrl+q"))
	})
	paste := "\x1b[200~\x1b[<0;2;24M\x1b[201~"
	term.feed([]byte("\x1b[<0;2;2M\x1b[<0;2;2m\x9b<0;2;2M\x9b<0;2;2m" + paste + "x"))
	eventually(t, func() bool { return string(sess.inputBytes()) == paste+"x" })
	term.feed([]byte{DefaultDetachKey})
	_ = waitResult(t, ch)
}

func TestTouchTinyHeightPauseDropsQueuedReportsAndGrowRearms(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession(touchSnapshot(t, vt.MouseState{Known: true}))
	ch := runInBackground(Config{Term: term, Session: sess, Chrome: true, TouchBack: true})
	waitOut(t, term, func(b []byte) bool { return bytes.Contains(b, []byte("[swarm]")) })
	baseline := len(term.outBytes())
	term.setSize(80, 2)
	waitOut(t, term, func(b []byte) bool { return bytes.Contains(b[baseline:], []byte("\x1b[r")) })
	term.feed([]byte("\x1b[<0;2;2M\x1b[<0;2;2m\x9b<0;2;2M\x9b<0;2;2mx"))
	eventually(t, func() bool { return string(sess.inputBytes()) == "x" })
	term.setSize(80, 24)
	waitOut(t, term, func(b []byte) bool { return bytes.Count(b, []byte("[swarm]")) >= 2 })
	term.feed([]byte("\x1b[<0;2;24M\x1b[<0;2;24m"))
	if result := waitResult(t, ch); result.reason != ReasonDetached || result.err != nil {
		t.Fatalf("grow did not rearm button: %+v", result)
	}
}
