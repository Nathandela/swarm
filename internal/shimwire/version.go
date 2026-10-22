//go:build !compat_shim_old && !compat_shim_new

package shimwire

// Version 2 carries the provider-login runtime and generic input embargo
// contract. Upgrades from version 1 require an offline daemon/shim drain before
// migration; old workers cannot launch the replacement binary safely.
const Version = 2
