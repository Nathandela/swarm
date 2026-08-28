package upgrade

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/shimwire"
)

// CompatManifest is the release's compatibility card: the wire and schema
// constants the archived build was compiled with, emitted at build time from
// the SAME source constants (scripts/releasemanifest) and read at activation
// time from the STAGED archive. It exists so activation is a pure disk
// comparison -- the committee killed both alternatives: executing a staged
// binary to ask it (R4's probe, withdrawn), and installing first so converge
// could ask afterwards, which put a new binary under an old daemon and broke
// every launch on a wire bump (the compat matrix's own pinned ProcessLost
// cell). The manifest rides INSIDE the tarball as compat.json, so the signed
// checksum that covers the archive covers it too -- signed compatibility
// metadata, not a hidden verb (committee: codex finding 1).
type CompatManifest struct {
	Version  string `json:"version"`  // the release tag
	Shimwire int    `json:"shimwire"` // internal/shimwire.Version
	Protocol int    `json:"protocol"` // internal/protocol.Version
	Schema   int    `json:"schema"`   // internal/persist.SchemaVersion
}

// CurrentManifest is THIS build's card -- what the emitter writes at release,
// and what activation compares a staged card against for the axes that gate.
func CurrentManifest(tag string) CompatManifest {
	return CompatManifest{
		Version:  tag,
		Shimwire: shimwire.Version,
		Protocol: protocol.Version,
		Schema:   persist.SchemaVersion,
	}
}

// readStagedManifest loads the card the stage step extracted. A staged build
// without one predates the manifest (or the archive was built by an older
// pipeline); the caller treats absence as "unknown", which gates conservatively.
func readStagedManifest(stage string) (CompatManifest, error) {
	data, err := os.ReadFile(stage + string(os.PathSeparator) + "compat.json")
	if err != nil {
		return CompatManifest{}, err
	}
	var m CompatManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return CompatManifest{}, fmt.Errorf("upgrade: staged compat.json does not decode: %w", err)
	}
	return m, nil
}
