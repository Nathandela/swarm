//go:build compat_shim_v0

package shimwire

// Version is stamped to 0 for the E14.3 compat-matrix's OLD-shim binary, built
// with `-tags compat_shim_v0`. TEST-ONLY and never shipped: it exists so the
// matrix can exercise a new-daemon (v1) x old-shim (v0) reconnect and assert the
// skew is detected (session marked lost), never a silent corrupt reconnect. The
// default build stays at 1 (see version.go).
const Version = 0
