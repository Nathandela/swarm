package tui

import (
	"errors"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/Nathandela/swarm/internal/protocol"
)

type contextGuardOptionsClient struct {
	*fakeClient
	mu       sync.Mutex
	caps     []string
	settings protocol.ContextGuardSettings
	getErr   error
	setErr   error
	gets     int
	sets     []contextGuardSetCall
}

type contextGuardSetCall struct {
	revision uint64
	compact  protocol.ContextGuardAutoCompact
}

func newContextGuardOptionsClient() *contextGuardOptionsClient {
	return &contextGuardOptionsClient{
		fakeClient: newFakeClient(),
		caps:       []string{protocol.CapContextGuardSettings},
		settings: protocol.ContextGuardSettings{
			SchemaVersion: 1,
			Revision:      4,
			AutoCompact:   protocol.ContextGuardAutoCompact{ThresholdPercent: 80},
		},
	}
}

func (c *contextGuardOptionsClient) Capabilities() []string { return c.caps }

func (c *contextGuardOptionsClient) ContextGuardSettings() (protocol.ContextGuardSettings, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gets++
	return c.settings, c.getErr
}

func (c *contextGuardOptionsClient) SetContextGuardSettings(revision uint64, compact protocol.ContextGuardAutoCompact) (protocol.ContextGuardSettings, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sets = append(c.sets, contextGuardSetCall{revision: revision, compact: compact})
	if c.setErr != nil {
		return protocol.ContextGuardSettings{}, c.setErr
	}
	if revision != c.settings.Revision {
		return protocol.ContextGuardSettings{}, protocol.ErrContextGuardSettingsStaleRevision
	}
	if compact != c.settings.AutoCompact {
		c.settings.AutoCompact = compact
		c.settings.Revision++
	}
	return c.settings, nil
}

func TestOptionsContextGuard_LoadToggleAndSave(t *testing.T) {
	c := newContextGuardOptionsClient()
	m := newModel(t, c, detectMixed())
	m2, cmd := m.Update(keyRune('o'))
	if cmd == nil {
		t.Fatal("opening options did not start async context-guard load")
	}
	rm := m2.(rootModel)
	if rm.screen != screenOptions || !rm.options.contextGuard.loading {
		t.Fatalf("open options = screen %v settings %#v; want options/loading", rm.screen, rm.options.contextGuard)
	}
	m3, _ := m2.Update(cmd())
	rm = m3.(rootModel)
	if rm.options.contextGuard.loading || rm.options.contextGuard.revision != 4 {
		t.Fatalf("loaded settings = %#v; want revision 4 and not loading", rm.options.contextGuard)
	}
	view := stripANSI(rm.View().Content)
	for _, want := range []string{"auto compact", "threshold", "Codex only", "compacts when quiet and unattended"} {
		if !strings.Contains(view, want) {
			t.Fatalf("options view missing %q:\n%s", want, view)
		}
	}

	// focus: group -> order -> auto compact, then toggle and save.
	m3 = send(m3, keyDown)
	m3 = send(m3, keyDown)
	m3 = send(m3, keyRune(' '))
	rm = m3.(rootModel)
	if !rm.options.contextGuard.autoCompact.Enabled || rm.options.contextGuard.threshold.text != "80" {
		t.Fatalf("toggle = %#v; want enabled with remembered threshold", rm.options.contextGuard)
	}
	m4, save := m3.Update(keyEnter)
	if save == nil || m4.(rootModel).screen != screenOptions {
		t.Fatal("save must be asynchronous and retain the options form until success")
	}
	m5, _ := m4.Update(save())
	rm = m5.(rootModel)
	if rm.screen != screenGeneral || len(c.sets) != 1 || !c.sets[0].compact.Enabled {
		t.Fatalf("successful save = screen %v calls %#v; want general and one enabled CAS", rm.screen, c.sets)
	}
}

func TestOptionsContextGuard_ThresholdBoundsBeforeSave(t *testing.T) {
	for _, threshold := range []string{"39", "40", "95", "96"} {
		t.Run(threshold, func(t *testing.T) {
			c := newContextGuardOptionsClient()
			m := loadedContextGuardOptions(t, c)
			rm := m.(rootModel)
			rm.options.focus = optionsFocusThreshold
			rm.options.contextGuard.threshold.set(threshold)
			m = rm
			m2, cmd := m.Update(keyEnter)
			valid := threshold == "40" || threshold == "95"
			if valid && cmd == nil {
				t.Fatalf("threshold %s did not start save", threshold)
			}
			if !valid {
				if cmd != nil || len(c.sets) != 0 || !strings.Contains(stripANSI(m2.(rootModel).View().Content), "40–95") {
					t.Fatalf("threshold %s did not fail locally before save", threshold)
				}
			}
		})
	}
}

func TestOptionsContextGuard_StaleGenerationsConflictAndUnavailable(t *testing.T) {
	c := newContextGuardOptionsClient()
	m := loadedContextGuardOptions(t, c)
	rm := m.(rootModel)
	old := rm.options.contextGuard.generation
	rm.options.contextGuard.generation++ // simulate close/reopen before an old load lands
	m, _ = rm.Update(contextGuardSettingsLoadedMsg{generation: old, settings: protocol.ContextGuardSettings{Revision: 99}})
	if got := m.(rootModel).options.contextGuard.revision; got != 4 {
		t.Fatalf("stale load overwrote reopened settings revision %d", got)
	}
	rm = m.(rootModel)
	current := rm.options.contextGuard.generation
	m, _ = rm.Update(contextGuardSettingsSavedMsg{generation: current - 1, settings: protocol.ContextGuardSettings{Revision: 99}})
	if got := m.(rootModel).options.contextGuard.revision; got != 4 {
		t.Fatalf("stale save overwrote current settings revision %d", got)
	}

	c.setErr = protocol.ErrContextGuardSettingsStaleRevision
	rm = m.(rootModel)
	originalGrouping := rm.general.grouping
	rm.options.grouping = groupByRepo
	rm.options.focus = optionsFocusAutoCompact
	rm.options.contextGuard.autoCompact.Enabled = true
	m2, cmd := rm.Update(keyEnter)
	if cmd == nil {
		t.Fatal("conflict setup did not start save")
	}
	m3, _ := m2.Update(cmd())
	rm = m3.(rootModel)
	if rm.screen != screenOptions || !strings.Contains(stripANSI(rm.View().Content), "changed elsewhere") {
		t.Fatalf("CAS conflict did not remain visible in options:\n%s", stripANSI(rm.View().Content))
	}
	if rm.general.grouping != originalGrouping {
		t.Fatalf("CAS conflict partially applied grouping=%v, want %v", rm.general.grouping, originalGrouping)
	}

	oldClient := newFakeClient()
	m = newModel(t, oldClient, detectMixed())
	m = send(m, keyRune('o'))
	rm = m.(rootModel)
	if rm.options.contextGuard.available || strings.Contains(stripANSI(rm.View().Content), "auto compact") {
		t.Fatalf("old daemon exposed unsupported context-guard settings: %#v", rm.options.contextGuard)
	}

	unavailable := newContextGuardOptionsClient()
	unavailable.getErr = errors.New("raw daemon path /private/secret must not render")
	m = newModel(t, unavailable, detectMixed())
	m2, load := m.Update(keyRune('o'))
	m3, _ = m2.Update(load())
	rm = m3.(rootModel)
	text := stripANSI(rm.View().Content)
	if rm.screen != screenOptions || rm.options.contextGuard.loaded || !strings.Contains(text, "context guard settings unavailable") || strings.Contains(text, "private/secret") {
		t.Fatalf("unavailable settings did not remain visible in options:\n%s", stripANSI(rm.View().Content))
	}
}

func TestOptionsContextGuard_GuardsEnterAndDirectionalNavigation(t *testing.T) {
	c := newContextGuardOptionsClient()
	m := newModel(t, c, detectMixed())
	opening, load := m.Update(keyRune('o'))
	if load == nil {
		t.Fatal("open did not dispatch load")
	}
	// Loading is an error/uncertain state: Enter must leave the menu open and must
	// not run a duplicate request or silently discard the visible state.
	stillOpen, cmd := opening.Update(keyEnter)
	if cmd != nil || stillOpen.(rootModel).screen != screenOptions {
		t.Fatal("Enter while loading closed options or started another command")
	}

	m, _ = opening.Update(load())
	rm := m.(rootModel)
	if rm.options.focus != optionsFocusGroup {
		t.Fatalf("initial focus=%d, want group", rm.options.focus)
	}
	m = send(m, keyUp)
	if got := m.(rootModel).options.focus; got != optionsFocusThreshold {
		t.Fatalf("Up focus=%d, want threshold", got)
	}
	m = send(m, keyDown)
	if got := m.(rootModel).options.focus; got != optionsFocusGroup {
		t.Fatalf("Down focus=%d, want group", got)
	}

	// Start one save, then hammer Enter before it resolves. Only the first CAS is
	// permitted; a second same-revision request manufactures a false conflict.
	rm = m.(rootModel)
	rm.options.focus = optionsFocusAutoCompact
	rm.options.contextGuard.autoCompact.Enabled = true
	first, save := rm.Update(keyEnter)
	if save == nil {
		t.Fatal("first save missing")
	}
	second, duplicate := first.Update(keyEnter)
	if duplicate != nil || second.(rootModel).screen != screenOptions {
		t.Fatal("Enter while saving started a duplicate CAS or closed options")
	}
	settled, _ := second.Update(save())
	if len(c.sets) != 1 || settled.(rootModel).screen != screenGeneral {
		t.Fatalf("save calls=%d screen=%v; want one call then general", len(c.sets), settled.(rootModel).screen)
	}

	// A local threshold error is not a cancel gesture; Enter leaves it displayed.
	m = loadedContextGuardOptions(t, newContextGuardOptionsClient())
	rm = m.(rootModel)
	rm.options.focus = optionsFocusThreshold
	originalGrouping := rm.general.grouping
	rm.options.grouping = groupByRepo
	rm.options.contextGuard.threshold.set("39")
	failed, _ := rm.Update(keyEnter)
	kept, next := failed.Update(keyEnter)
	if next != nil || kept.(rootModel).screen != screenOptions || !strings.Contains(stripANSI(kept.(rootModel).View().Content), "40–95") {
		t.Fatal("Enter on local validation error did not keep options and error visible")
	}
	if got := kept.(rootModel).general.grouping; got != originalGrouping {
		t.Fatalf("local validation error partially applied grouping=%v, want %v", got, originalGrouping)
	}
}

func loadedContextGuardOptions(t *testing.T, c *contextGuardOptionsClient) tea.Model {
	t.Helper()
	m := newModel(t, c, detectMixed())
	m2, cmd := m.Update(keyRune('o'))
	if cmd == nil {
		t.Fatal("options load command missing")
	}
	m3, _ := m2.Update(cmd())
	return m3
}
