package adapter

// Capability derives the per-CLI capability-matrix entry (E9.6 / T-6): the
// harness's OUTPUT and an adapter's acceptance baseline. The entry records which
// signal styles the adapter declares, whether it resumes, whether it can pull a
// conversation id out of the RECORDED capture (not merely claim to), and how
// many launch options it exposes — derived from the adapter itself and
// cross-checked against the fixture.

import (
	"sort"

	"github.com/Nathandela/swarm/internal/vt"
)

// CapabilityEntry is one row of the capability matrix.
type CapabilityEntry struct {
	CLI            string   `json:"cli"`
	Version        string   `json:"version"`
	Hooks          bool     `json:"hooks"`
	Resume         bool     `json:"resume"`
	ConversationID bool     `json:"conversation_id"`
	Options        int      `json:"options"`
	Signals        []string `json:"signals"` // sorted, de-duped SignalSource kinds
}

// Capability derives a's capability entry, cross-checked against fx. CLI and
// Version are copied from the fixture; Hooks/Resume/Options/Signals come from
// the adapter's declarations; ConversationID is proven by extracting an id from
// the fixture's REAL rendered grid + raw capture — grid is the *vt.Snap the
// harness builds from fx.PTYCapture (not nil), so extraction is exercised
// exactly as the engine drives it at runtime.
func Capability(a Adapter, fx Fixture, grid *vt.Snap) CapabilityEntry {
	entry := CapabilityEntry{
		CLI:     fx.CLI,
		Version: fx.Version,
		Options: len(a.Options()),
	}

	kinds := make(map[string]bool)
	for _, s := range a.SignalSources() {
		kinds[s.Kind] = true
		if s.Kind == "hook" {
			entry.Hooks = true
		}
	}
	entry.Signals = make([]string, 0, len(kinds))
	for k := range kinds {
		entry.Signals = append(entry.Signals, k)
	}
	sort.Strings(entry.Signals)

	if argv, err := a.Resume(ResumeSpec{ConversationID: probeConversationID}); err == nil && len(argv) > 0 {
		entry.Resume = true
	}
	if id, ok := a.ExtractConversationID(grid, fx.PTYCapture); ok && id != "" {
		entry.ConversationID = true
	}
	return entry
}
