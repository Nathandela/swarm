package skeleton

import (
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/protocol"
)

// ADR-021: a session's name follows the name its CLI shows, newest wins. The CLI
// publishes it in a per-process registry under the daemon user's home (the adapter's
// LiveNameSource says where and how to read one file); this file is the assembly's
// half, the I/O and the clock comparison. It runs on every authenticated hook
// callback for the session, which is the one trigger every Claude session already
// produces at each turn boundary, so a `/rename` in the CLI reaches the board by the
// next prompt or stop. Nothing here writes to the CLI: Claude's TUI has no rename
// transport, so a swarm rename stays swarm-side until the CLI renames again.
//
// ponytail: every callback re-reads the whole registry. It holds one small file per
// running Claude process (a handful, ~600 bytes each), so a chatty session costs well
// under a millisecond per hook. Cache the matched file by mtime if that ever shows up.
const (
	maxLiveNameFiles     = 256
	maxLiveNameFileBytes = 64 << 10
)

func (d *Daemon) adoptLiveSessionName(local string, ad adapter.Adapter) {
	src, ok := adapter.AsLiveNameSource(ad)
	if !ok || d.core == nil || d.home == "" {
		return
	}
	m, ok := d.core.Get(local)
	if !ok || m.ConversationID == "" {
		return
	}
	dir := filepath.Join(d.home, src.LiveNameDir())
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var best adapter.LiveName
	found := false
	for i, e := range entries {
		if i >= maxLiveNameFiles {
			break
		}
		if !e.Type().IsRegular() {
			continue
		}
		raw, ok := readBounded(filepath.Join(dir, e.Name()), maxLiveNameFileBytes)
		if !ok {
			continue
		}
		if ln, ok := src.LiveNameFromFile(raw, m.ConversationID); ok && (!found || ln.Since.After(best.Since)) {
			best, found = ln, true
		}
	}
	if !found || !best.Since.After(m.NameSetAt) {
		return
	}
	name := protocol.SanitizeName(best.Name)
	if name == "" || name == m.Name {
		return
	}
	if err := d.core.RenameAt(local, name, best.Since); err != nil {
		log.Printf("skeleton: could not adopt the CLI's name for session %s: %v", local, err)
		return
	}
	if d.api != nil {
		d.api.pokeWatch()
	}
}

// readBounded reads a file that must fit in max bytes; a larger one is not a
// registry file and is reported as unreadable rather than truncated.
func readBounded(path string, max int) ([]byte, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, int64(max)+1))
	if err != nil || len(raw) > max {
		return nil, false
	}
	return raw, true
}
