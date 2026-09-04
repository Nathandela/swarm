package tui

// layout_store.go gives the board layout durable custody. The grouping and
// ordering picked in the options window used to live with the running client
// only, so every swarm start threw the owner's choice away; ADR-026 reverses
// that decision.
//
// The layout is a CLIENT preference, not a daemon policy -- it is how THIS
// terminal renders its board, unlike the context-guard settings sharing the same
// window, which are daemon-global and travel the protocol under a CAS revision.
// So it is a small local document, reached through a seam (LayoutStore) that
// keeps the router itself free of filesystem knowledge and lets every unit test
// drive an in-memory store.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	tea "charm.land/bubbletea/v2"
)

// BoardLayout is the persisted choice, named by the labels the options window
// renders ("status"/"repo"/"tag" and "arrival"/"activity"/"created"/"name")
// rather than by the enum's numbering, so the document stays readable and
// survives any later reordering of the modes. The zero value means "no stored
// preference", which reads back as the built-in defaults.
type BoardLayout struct {
	Grouping string `json:"grouping,omitempty"`
	Ordering string `json:"ordering,omitempty"`
}

// LayoutStore is the durable custody of the board layout. Both halves are
// best-effort by contract: a Load that fails leaves the client on its defaults,
// and a Save that fails is bannered but never costs the running client the
// layout it just applied.
type LayoutStore interface {
	LoadLayout() (BoardLayout, error)
	SaveLayout(BoardLayout) error
}

// WithLayoutStore injects the durable custody. Without it the client behaves
// exactly as it did before ADR-026: the layout lives for one process.
func WithLayoutStore(s LayoutStore) Option {
	return func(m *rootModel) { m.layoutStore = s }
}

// layoutSavedMsg reports the outcome of one durable write.
type layoutSavedMsg struct{ err error }

// saveLayoutCmd writes the layout off the update loop. A nil store yields no
// command, so a storeless client dispatches nothing at all.
func saveLayoutCmd(s LayoutStore, g groupingMode, o orderingMode) tea.Cmd {
	if s == nil {
		return nil
	}
	l := BoardLayout{Grouping: groupingLabels[g], Ordering: orderingLabels[o]}
	return func() tea.Msg { return layoutSavedMsg{err: s.SaveLayout(l)} }
}

// applyLayout commits a chosen layout to the board and returns the command that
// persists it. The board is regrouped first and unconditionally: the durable
// write is a consequence of the choice, never a precondition for it.
func (m *rootModel) applyLayout(g groupingMode, o orderingMode) tea.Cmd {
	m.general.setLayout(g, o)
	return saveLayoutCmd(m.layoutStore, g, o)
}

// restoreLayout seeds the board from durable custody before the first paint. An
// unreadable store, and any label this build does not know, falls back FIELD BY
// FIELD to the built-in default rather than discarding the whole document: a
// swarm that gained or lost a mode still honours the half it understands.
func (m *rootModel) restoreLayout() {
	if m.layoutStore == nil {
		return
	}
	l, err := m.layoutStore.LoadLayout()
	if err != nil {
		return
	}
	g, o := m.general.grouping, m.general.ordering
	if v, ok := groupingFromLabel(l.Grouping); ok {
		g = v
	}
	if v, ok := orderingFromLabel(l.Ordering); ok {
		o = v
	}
	m.general.setLayout(g, o)
}

// groupingFromLabel resolves a persisted grouping label, reporting whether this
// build has that mode.
func groupingFromLabel(label string) (groupingMode, bool) {
	for i, l := range groupingLabels {
		if l == label {
			return groupingMode(i), true
		}
	}
	return groupByStatus, false
}

// orderingFromLabel resolves a persisted ordering label, reporting whether this
// build has that mode.
func orderingFromLabel(label string) (orderingMode, bool) {
	for i, l := range orderingLabels {
		if l == label {
			return orderingMode(i), true
		}
	}
	return orderByArrival, false
}

// ---------------------------------------------------------------------------
// The file-backed store.
// ---------------------------------------------------------------------------

// layoutSchemaVersion stamps the document so a later shape change is
// recognizable rather than guessed at.
const layoutSchemaVersion = 1

// layoutDocument is the on-disk shape. It is deliberately a superset of
// BoardLayout so the version rides along without leaking into the router.
type layoutDocument struct {
	SchemaVersion int    `json:"schema_version"`
	Grouping      string `json:"grouping,omitempty"`
	Ordering      string `json:"ordering,omitempty"`
}

// FileLayoutStore keeps the layout in one small JSON document.
type FileLayoutStore struct {
	path string
}

// NewFileLayoutStore returns the store for a document path. The path is not
// touched until the first Load or Save.
func NewFileLayoutStore(path string) *FileLayoutStore {
	return &FileLayoutStore{path: path}
}

// DefaultLayoutPath is the owner's board-layout document under the XDG config
// directory. The layout is a user preference, so it belongs beside the user's
// other configuration rather than inside the daemon's session state directory,
// which the daemon owns and hardens.
func DefaultLayoutPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve config dir: %w", err)
	}
	return filepath.Join(dir, "swarm", "board-layout.json"), nil
}

// LoadLayout reads the stored layout. A MISSING document is the first run, not a
// failure: it reads as the zero layout and no error. Anything else -- unreadable,
// truncated, not JSON -- is reported, so the caller falls back deliberately
// rather than on a silently half-parsed document.
func (s *FileLayoutStore) LoadLayout() (BoardLayout, error) {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return BoardLayout{}, nil
	}
	if err != nil {
		return BoardLayout{}, fmt.Errorf("read board layout: %w", err)
	}
	var doc layoutDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return BoardLayout{}, fmt.Errorf("parse board layout %s: %w", s.path, err)
	}
	return BoardLayout{Grouping: doc.Grouping, Ordering: doc.Ordering}, nil
}

// SaveLayout replaces the document atomically: a temp file in the same directory
// is written and renamed over the target, so an interrupted write can never
// leave a half-document that the next start would refuse to parse. The temp file
// is removed on every failure path, so a failing store leaves no litter beside
// the document it could not replace.
func (s *FileLayoutStore) SaveLayout(l BoardLayout) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	data, err := json.Marshal(layoutDocument{
		SchemaVersion: layoutSchemaVersion,
		Grouping:      l.Grouping,
		Ordering:      l.Ordering,
	})
	if err != nil {
		return fmt.Errorf("encode board layout: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".board-layout-*.json")
	if err != nil {
		return fmt.Errorf("create board layout temp: %w", err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }() // no-op once the rename has consumed it
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write board layout: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write board layout: %w", err)
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return fmt.Errorf("harden board layout: %w", err)
	}
	if err := os.Rename(name, s.path); err != nil {
		return fmt.Errorf("commit board layout: %w", err)
	}
	return nil
}
