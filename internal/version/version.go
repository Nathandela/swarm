// Package version holds the build-time version identity stamped into the
// swarm binary. It is reported by `swarm version` and rides the D-8
// client<->daemon hello handshake (internal/protocol) as an ADDITIVE signal
// alongside the wire skew gate (protocol.Version / daemon.ProtocolVersion): it
// lets a client notice it is talking to a different-build daemon even when the
// wire protocol still matches.
package version

// Version, Commit, and Date are overridden at release build time via:
//
//	-ldflags "-X github.com/Nathandela/swarm/internal/version.Version=v1.2.3 \
//	          -X github.com/Nathandela/swarm/internal/version.Commit=<sha> \
//	          -X github.com/Nathandela/swarm/internal/version.Date=<rfc3339>"
//
// (.goreleaser.yaml wires this via goreleaser's {{.Version}}/{{.Commit}}/{{.Date}}
// template vars.) An unstamped build — `go build`/`go run`/`go test` with no
// -ldflags, e.g. local dev — defaults Version to "dev"; Commit and Date stay
// empty.
var (
	Version = "dev"
	Commit  = ""
	Date    = ""
)
