package tui

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	teatest "github.com/charmbracelet/x/exp/teatest/v2"
)

// Suppress the app's timers so this test can drive renderer updates explicitly.
type repaintRendererModel struct{ inner tea.Model }

func (m repaintRendererModel) Init() tea.Cmd  { return nil }
func (m repaintRendererModel) View() tea.View { return m.inner.View() }
func (m repaintRendererModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.inner.Update(msg)
	return repaintRendererModel{inner: next}, cmd
}

func TestRepaint_UnchangedFrameWritesNoRendererBytes(t *testing.T) {
	const marker = "idle-renderer-marker"
	m := newModel(t, newFakeClient(sReview("endpoint/idle", "codex", "~/probe", marker, 2*time.Hour)), detectMixed())
	tm := startTM(t, repaintRendererModel{inner: m})
	defer quitTM(t, tm)
	waitContains(t, tm, marker)

	tm.Send(repaintMsg{})
	time.Sleep(100 * time.Millisecond)
	if got, err := io.ReadAll(tm.Output()); err != nil {
		t.Fatal(err)
	} else if len(got) != 0 {
		t.Fatalf("unchanged repaint wrote %d renderer bytes: %q", len(got), stripANSIB(got))
	} else {
		t.Logf("unchanged repaint renderer output: %d bytes", len(got))
	}
}

func TestRepaint_ElapsedBucketChangeReachesRenderer(t *testing.T) {
	const marker = "elapsed-boundary-marker"
	s := sReview("endpoint/idle", "codex", "~/probe", marker, 59*time.Second)
	s.LastActivity = time.Now().Add(-59 * time.Second)
	m := newModel(t, newFakeClient(s), detectMixed())
	tm := startTM(t, repaintRendererModel{inner: m})
	defer quitTM(t, tm)
	waitContains(t, tm, marker)
	if !strings.Contains(view(m), "59s") {
		t.Fatalf("fixture does not start in seconds bucket:\n%s", view(m))
	}
	time.Sleep(1100 * time.Millisecond)
	tm.Send(repaintMsg{})
	var output []byte
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		output = append(output, b...)
		return bytes.Contains(stripANSIB(output), []byte("1m"))
	}, teatest.WithDuration(time.Second), teatest.WithCheckInterval(10*time.Millisecond))
	if !bytes.Contains(stripANSIB(output), []byte("1m")) {
		t.Fatalf("elapsed bucket change did not reach renderer:\n%s", stripANSIB(output))
	}
}

func TestWorkingAnimationWritesOnlyChangedGlyph(t *testing.T) {
	const marker = "working-output-marker"
	m := newModel(t, newFakeClient(sWorking("endpoint/working", "codex", "~/probe", marker, 2*time.Hour)), detectMixed())
	output := &rendererCapture{}
	p, stop := startRendererProgram(t, m, output)
	defer stop()
	waitRendererContains(t, output, marker)
	offset := output.Len()

	p.Send(workingAnimationMsg{})
	got := waitRendererContainsAfter(t, output, offset, "⠙")
	if bytes.Contains(stripANSIB(got), []byte(marker)) {
		t.Fatalf("spinner diff retransmitted unchanged board marker: %s", stripANSIB(got))
	}
}

func TestBannerExpiryChangesReachRenderer(t *testing.T) {
	const banner = "banner-render-marker"
	m := newModel(t, newFakeClient(sReview("endpoint/idle", "codex", "~/probe", "board-marker", 2*time.Hour)), detectMixed()).(rootModel)
	m.general.bannerText = banner
	m.general.bannerExpiry = time.Now().Add(-time.Second)
	tm := startTM(t, repaintRendererModel{inner: m})
	defer quitTM(t, tm)
	waitContainsAll(t, tm, "board-marker", banner)
	tm.Send(bannerExpireMsg{})
	time.Sleep(100 * time.Millisecond)
	got, err := io.ReadAll(tm.Output())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || bytes.Contains(stripANSIB(got), []byte(banner)) {
		t.Fatalf("expired banner did not produce its incremental renderer update: %q", stripANSIB(got))
	}
}

func TestResizeReachesRenderer(t *testing.T) {
	const marker = "resized-board-marker"
	m := newModel(t, newFakeClient(sReview("endpoint/idle", "codex", "~/probe", marker, 2*time.Hour)), detectMixed())
	tm := startTM(t, repaintRendererModel{inner: m})
	defer quitTM(t, tm)
	waitContains(t, tm, marker)

	tm.Send(tea.WindowSizeMsg{Width: testCols + 4, Height: testRows + 2})
	if got, err := rendererOutputAfter(t, tm); err != nil || len(got) == 0 {
		t.Fatalf("resize produced no renderer update (%d bytes, err %v)", len(got), err)
	}
}

func TestTerminalRestoreRedrawsUnchangedBoard(t *testing.T) {
	const marker = "terminal-restore-board-marker"
	m := newModel(t, newFakeClient(sReview("endpoint/idle", "codex", "~/probe", marker, 2*time.Hour)), detectMixed())
	output := &rendererCapture{}
	p, stop := startRendererProgram(t, m, output)
	defer stop()
	waitRendererContains(t, output, marker)
	if err := p.ReleaseTerminal(); err != nil {
		t.Fatalf("ReleaseTerminal: %v", err)
	}
	if _, err := output.Write([]byte("external-terminal-damage")); err != nil {
		t.Fatalf("write external terminal damage: %v", err)
	}
	offset := output.Len()
	if err := p.RestoreTerminal(); err != nil {
		t.Fatalf("RestoreTerminal: %v", err)
	}
	got := waitRendererContainsAfter(t, output, offset, marker)
	all := output.Bytes()
	damageAt := bytes.Index(all, []byte("external-terminal-damage"))
	boardAt := offset + bytes.Index(got, []byte(marker))
	if damageAt < 0 || boardAt <= damageAt {
		t.Fatal("unchanged board was not rendered after external terminal damage")
	}
}

func rendererOutputAfter(t *testing.T, tm *teatest.TestModel) ([]byte, error) {
	t.Helper()
	time.Sleep(100 * time.Millisecond)
	return io.ReadAll(tm.Output())
}

type rendererCapture struct {
	sync.Mutex
	bytes.Buffer
}

func (b *rendererCapture) Write(p []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	return b.Buffer.Write(p)
}

func (b *rendererCapture) Len() int {
	b.Lock()
	defer b.Unlock()
	return b.Buffer.Len()
}

func (b *rendererCapture) Bytes() []byte {
	b.Lock()
	defer b.Unlock()
	return append([]byte(nil), b.Buffer.Bytes()...)
}

func startRendererProgram(t *testing.T, model tea.Model, output *rendererCapture) (*tea.Program, func()) {
	t.Helper()
	input, inputWriter := io.Pipe()
	p := tea.NewProgram(repaintRendererModel{inner: model}, tea.WithInput(input), tea.WithOutput(output), tea.WithWindowSize(testCols, testRows), tea.WithoutSignalHandler(), tea.WithoutSignals())
	done := make(chan error, 1)
	go func() {
		_, err := p.Run()
		done <- err
	}()
	return p, func() {
		p.Quit()
		_ = inputWriter.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("program exit: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("program did not stop")
		}
	}
}

func waitRendererContains(t *testing.T, output *rendererCapture, want string) {
	t.Helper()
	waitRendererContainsAfter(t, output, 0, want)
}

func waitRendererContainsAfter(t *testing.T, output *rendererCapture, offset int, want string) []byte {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got := output.Bytes()
		if offset <= len(got) && bytes.Contains(stripANSIB(got[offset:]), []byte(want)) {
			return got[offset:]
		}
		time.Sleep(10 * time.Millisecond)
	}
	got := output.Bytes()
	if offset > len(got) {
		t.Fatalf("renderer output shrank from offset %d to %d", offset, len(got))
	}
	t.Fatalf("renderer output did not contain %q after byte %d: %s", want, offset, stripANSIB(got[offset:]))
	return nil
}
