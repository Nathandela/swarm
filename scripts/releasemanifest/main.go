// releasemanifest emits the release's compatibility card (compat.json) from
// the source tree's own constants -- one source of truth with the running
// code, so the card cannot drift from the build it describes. goreleaser runs
// it as a before hook and ships the file inside every swarm archive, where the
// signed checksum covers it (internal/upgrade.CompatManifest is the reader).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/Nathandela/swarm/internal/upgrade"
)

func main() {
	tag := flag.String("tag", os.Getenv("GORELEASER_CURRENT_TAG"), "release tag (defaults to GORELEASER_CURRENT_TAG)")
	out := flag.String("out", "compat.json", "file to write")
	flag.Parse()
	data, err := json.MarshalIndent(upgrade.CurrentManifest(*tag), "", "  ")
	if err != nil {
		fatal("releasemanifest: %v", err)
	}
	if err := os.WriteFile(*out, append(data, '\n'), 0o644); err != nil {
		fatal("releasemanifest: %v", err)
	}
}

func fatal(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
