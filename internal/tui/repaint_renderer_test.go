package tui

import (
	"bytes"
	"io"
	"strings"
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

func TestResizeAndAttachRestoreReachRenderer(t *testing.T) {
	const marker = "restored-board-marker"
	m := newModel(t, newFakeClient(sReview("endpoint/idle", "codex", "~/probe", marker, 2*time.Hour)), detectMixed())
	tm := startTM(t, repaintRendererModel{inner: m})
	defer quitTM(t, tm)
	waitContains(t, tm, marker)

	tm.Send(tea.WindowSizeMsg{Width: testCols + 4, Height: testRows + 2})
	if got, err := rendererOutputAfter(t, tm); err != nil || len(got) == 0 {
		t.Fatalf("resize produced no renderer update (%d bytes, err %v)", len(got), err)
	}
	tm.Send(keyEnter)
	waitContains(t, tm, "attach passthrough is Epic 8")
	tm.Send(attachDoneMsg{})
	if got, err := rendererOutputAfter(t, tm); err != nil || !bytes.Contains(stripANSIB(got), []byte(marker)) {
		t.Fatalf("returning from attach did not redraw the board (%d bytes, err %v): %s", len(got), err, stripANSIB(got))
	}
}

func rendererOutputAfter(t *testing.T, tm *teatest.TestModel) ([]byte, error) {
	t.Helper()
	time.Sleep(100 * time.Millisecond)
	return io.ReadAll(tm.Output())
}
