package tui

// The board layout the options window applies outlives the client that chose it
// (ADR-026). New seeds the board from a LayoutStore before the first paint, and
// every apply writes the choice back through the same seam. A store that cannot
// read or cannot write never costs the owner the layout in the running client.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// fakeLayoutStore is the in-memory LayoutStore the router tests drive. saved
// records every write in order, including the ones a saveErr then refuses.
type fakeLayoutStore struct {
	layout  BoardLayout
	loadErr error
	saveErr error
	saved   []BoardLayout
	loads   int
}

func (f *fakeLayoutStore) LoadLayout() (BoardLayout, error) {
	f.loads++
	if f.loadErr != nil {
		return BoardLayout{}, f.loadErr
	}
	return f.layout, nil
}

func (f *fakeLayoutStore) SaveLayout(l BoardLayout) error {
	f.saved = append(f.saved, l)
	if f.saveErr != nil {
		return f.saveErr
	}
	f.layout = l
	return nil
}

// newLayoutModel builds and sizes a router wired to a layout store.
func newLayoutModel(t *testing.T, c Client, d DetectFunc, s LayoutStore) tea.Model {
	t.Helper()
	m := New(c, d, WithLayoutStore(s))
	m, _ = m.Update(tea.WindowSizeMsg{Width: testCols, Height: testRows})
	return m
}

// TestNewSeedsBoardFromPersistedLayout is the whole point of the feature: a
// client that starts with a stored layout comes up on that layout, not on the
// built-in status/arrival defaults.
func TestNewSeedsBoardFromPersistedLayout(t *testing.T) {
	store := &fakeLayoutStore{layout: BoardLayout{Grouping: "tag", Ordering: "name"}}
	s := sWorking("endpoint/a", "claude", "/code/repo", "working", time.Minute)
	m := newLayoutModel(t, newFakeClient(s), detectMixed(), store)

	rm := m.(rootModel)
	if rm.general.grouping != groupByTag || rm.general.ordering != orderByName {
		t.Fatalf("first paint grouping=%v ordering=%v, want tag/name from the store",
			rm.general.grouping, rm.general.ordering)
	}
	if got := stripANSI(rm.general.header()); !strings.Contains(got, "group: tag · order: name") {
		t.Fatalf("restored layout is not visible in the header: %q", got)
	}
	// Reopening the options window seeds the form from the restored layout too.
	m = send(m, keyRune('o'))
	if rm = m.(rootModel); rm.options.grouping != groupByTag || rm.options.ordering != orderByName {
		t.Fatalf("options form grouping=%v ordering=%v, want tag/name", rm.options.grouping, rm.options.ordering)
	}
}

// TestOptionsApplyPersistsLayout pins the write half: Enter on the options window
// commits the chosen layout to durable custody, spelled with the same stable
// labels the window renders.
func TestOptionsApplyPersistsLayout(t *testing.T) {
	store := &fakeLayoutStore{}
	m := newLayoutModel(t, newFakeClient(), detectMixed(), store)

	m = send(m, keyRune('o'))
	m = send(m, keyRight) // group: status -> repo
	m = send(m, keyDown)  // focus: order
	m = send(m, keyRight) // order: arrival -> activity
	m2, cmd := m.Update(keyEnter)
	execCmd(cmd)

	if rm := m2.(rootModel); rm.general.grouping != groupByRepo || rm.general.ordering != orderByActivity {
		t.Fatalf("applied grouping=%v ordering=%v, want repo/activity", rm.general.grouping, rm.general.ordering)
	}
	if len(store.saved) != 1 {
		t.Fatalf("options apply wrote %d layouts, want exactly 1: %+v", len(store.saved), store.saved)
	}
	if want := (BoardLayout{Grouping: "repo", Ordering: "activity"}); store.saved[0] != want {
		t.Fatalf("persisted %+v, want %+v", store.saved[0], want)
	}
}

// TestOptionsEscDiscardsWithoutPersisting: a cancelled window changes nothing,
// durably or otherwise.
func TestOptionsEscDiscardsWithoutPersisting(t *testing.T) {
	store := &fakeLayoutStore{}
	m := newLayoutModel(t, newFakeClient(), detectMixed(), store)

	m = send(m, keyRune('o'))
	m = send(m, keyRight)
	m2, cmd := m.Update(keyEsc)
	execCmd(cmd)

	if rm := m2.(rootModel); rm.general.grouping != groupByStatus {
		t.Fatalf("esc applied grouping=%v, want the untouched status grouping", rm.general.grouping)
	}
	if len(store.saved) != 0 {
		t.Fatalf("esc persisted %+v, want no write at all", store.saved)
	}
}

// TestUnreadableLayoutFallsBackToDefaults: a store that cannot answer must not
// stop the client coming up, and must not invent a layout.
func TestUnreadableLayoutFallsBackToDefaults(t *testing.T) {
	store := &fakeLayoutStore{loadErr: errors.New("permission denied")}
	m := newLayoutModel(t, newFakeClient(), detectMixed(), store)

	rm := m.(rootModel)
	if rm.general.grouping != groupByStatus || rm.general.ordering != orderByArrival {
		t.Fatalf("unreadable store yielded grouping=%v ordering=%v, want the status/arrival defaults",
			rm.general.grouping, rm.general.ordering)
	}
}

// TestUnknownLayoutLabelsFallBackPerField: a document naming a mode this build
// does not have (an older or newer swarm) keeps the field it does understand.
func TestUnknownLayoutLabelsFallBackPerField(t *testing.T) {
	store := &fakeLayoutStore{layout: BoardLayout{Grouping: "constellation", Ordering: "created"}}
	m := newLayoutModel(t, newFakeClient(), detectMixed(), store)

	rm := m.(rootModel)
	if rm.general.grouping != groupByStatus {
		t.Fatalf("unknown grouping label yielded %v, want the status default", rm.general.grouping)
	}
	if rm.general.ordering != orderByCreated {
		t.Fatalf("known ordering label yielded %v, want created", rm.general.ordering)
	}
}

// TestLayoutSaveFailureStillAppliesAndBanners: durable custody is best-effort.
// A write that fails is reported, and the running client still gets its layout.
func TestLayoutSaveFailureStillAppliesAndBanners(t *testing.T) {
	store := &fakeLayoutStore{saveErr: errors.New("disk full")}
	m := newLayoutModel(t, newFakeClient(), detectMixed(), store)

	m = send(m, keyRune('o'))
	m = send(m, keyRight) // group: status -> repo
	m2, cmd := m.Update(keyEnter)
	if rm := m2.(rootModel); rm.general.grouping != groupByRepo {
		t.Fatalf("a failed save cost the running client its layout: grouping=%v", rm.general.grouping)
	}
	// The save command resolves to the failure message the router banners.
	m2 = send(m2, layoutSavedMsg{err: store.saveErr})
	if got := stripANSI(m2.(rootModel).View().Content); !strings.Contains(got, "layout not saved") {
		t.Fatalf("a failed layout save is silent; view:\n%s", got)
	}
	execCmd(cmd)
}

// TestNoLayoutStoreKeepsTheDefaults: every test that builds a router without the
// seam (and cmd/swarm, when the path cannot be resolved) still works.
func TestNoLayoutStoreKeepsTheDefaults(t *testing.T) {
	m := newModel(t, newFakeClient(), detectMixed())
	m = send(m, keyRune('o'))
	m2, cmd := m.Update(keyEnter)
	execCmd(cmd) // must not panic on a nil store
	if rm := m2.(rootModel); rm.general.grouping != groupByStatus || rm.general.ordering != orderByArrival {
		t.Fatalf("storeless client grouping=%v ordering=%v, want the defaults", rm.general.grouping, rm.general.ordering)
	}
}

// ---------------------------------------------------------------------------
// The file-backed store.
// ---------------------------------------------------------------------------

func TestFileLayoutStoreRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "board-layout.json")
	s := NewFileLayoutStore(path)

	want := BoardLayout{Grouping: "tag", Ordering: "name"}
	if err := s.SaveLayout(want); err != nil {
		t.Fatalf("SaveLayout: %v", err)
	}
	got, err := NewFileLayoutStore(path).LoadLayout()
	if err != nil {
		t.Fatalf("LoadLayout: %v", err)
	}
	if got != want {
		t.Fatalf("round-tripped %+v, want %+v", got, want)
	}

	// The document is a readable, versioned JSON object -- the shape a later
	// swarm must still be able to widen.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read document: %v", err)
	}
	var doc struct {
		SchemaVersion int    `json:"schema_version"`
		Grouping      string `json:"grouping"`
		Ordering      string `json:"ordering"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("document is not JSON (%v): %s", err, data)
	}
	if doc.SchemaVersion != 1 || doc.Grouping != "tag" || doc.Ordering != "name" {
		t.Fatalf("document %s, want schema_version 1 and tag/name", data)
	}
}

// TestFileLayoutStoreMissingDocumentIsTheDefault: a first run is not an error.
func TestFileLayoutStoreMissingDocumentIsTheDefault(t *testing.T) {
	got, err := NewFileLayoutStore(filepath.Join(t.TempDir(), "board-layout.json")).LoadLayout()
	if err != nil {
		t.Fatalf("a missing document must read as the empty default, got error: %v", err)
	}
	if (got != BoardLayout{}) {
		t.Fatalf("a missing document read as %+v, want the zero layout", got)
	}
}

// TestFileLayoutStoreCorruptDocumentIsReported: a damaged file is an error the
// caller falls back from -- never a silent half-parse.
func TestFileLayoutStoreCorruptDocumentIsReported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "board-layout.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileLayoutStore(path).LoadLayout(); err == nil {
		t.Fatal("a corrupt document loaded without error; want a reported failure")
	}
}

// TestFileLayoutStoreOverwritesInPlace: the second save replaces the first, and
// leaves no temp file behind.
func TestFileLayoutStoreOverwritesInPlace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "board-layout.json")
	s := NewFileLayoutStore(path)
	if err := s.SaveLayout(BoardLayout{Grouping: "repo", Ordering: "activity"}); err != nil {
		t.Fatalf("first save: %v", err)
	}
	if err := s.SaveLayout(BoardLayout{Grouping: "status", Ordering: "created"}); err != nil {
		t.Fatalf("second save: %v", err)
	}
	got, err := s.LoadLayout()
	if err != nil {
		t.Fatalf("LoadLayout: %v", err)
	}
	if want := (BoardLayout{Grouping: "status", Ordering: "created"}); got != want {
		t.Fatalf("after two saves %+v, want %+v", got, want)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "board-layout.json" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("directory holds %v, want only board-layout.json", names)
	}
}
