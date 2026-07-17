//go:build compat_shim_v2

package shimwire

// Version is stamped to 2 for the E14.3 compat-matrix's NEW-shim binary, built
// with `-tags compat_shim_v2`. TEST-ONLY and never shipped: it exists so the
// matrix can exercise an old-daemon (v1) x new-shim (v2) reconnect and assert the
// skew is detected (session marked lost), never a silent corrupt reconnect. The
// default build stays at 1 (see version.go).
const Version = 2
