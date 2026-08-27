//go:build !compat_shim_v0 && !compat_shim_v2

package shimwire

// Version is the shimwire protocol version, carried in a hello message's
// WireVersion field.
//
// It is split across build-tagged files SOLELY as a test-only seam for the
// E14.3 old-shim x new-daemon compat matrix (internal/daemon/compatmatrix_test.go),
// which must compile `swarm shim` binaries that advertise and gate on an
// ADJACENT wire version:
//
//	go build -tags compat_shim_v0 ...   // Version == 0 (older shim)
//	go build -tags compat_shim_v2 ...   // Version == 2 (newer shim)
//
// The DEFAULT build — the only one ever shipped, and the one every non-tagged
// `go build` / `go test` / `go vet` / golangci-lint run selects — carries
// neither tag and is exactly `const Version = 1`, byte-for-byte unchanged from
// before this split. No production code path selects a non-default value; the
// shim/daemon handshake logic (shim/server.go, daemon/shimclient.go) is
// untouched and still references shimwire.Version directly.
//
// A BUMP HERE DROPS EVERY RUNNING SESSION FROM THE BOARD ACROSS AN UNATTENDED
// UPGRADE. dialShimHello's reconnect (internal/daemon/shimclient.go:59) rejects
// a shim whose WireVersion does not equal this constant and the session is
// marked lost, never driven over a mismatched protocol -- correct for a single
// running daemon, but auto-upgrade-plan.md's nightly converge (L2) restarts the
// daemon out from under shims that are still on the OLD Version, on a machine
// with nobody there to notice. Bumping this constant must follow the drain
// procedure in docs/ops/auto-upgrade.md, not land as an ordinary protocol
// change.
const Version = 1
