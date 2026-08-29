package skeleton

import "github.com/Nathandela/swarm/internal/adapter"

// setAdapterForTest replaces the adapter resolver under the same lock used by
// resolveAdapter. Once a daemon may be published, tests must use this seam
// instead of assigning adapterFor directly: capture and capability authoring
// run in background goroutines as soon as a session launches. A struct-literal
// initializer before publication remains safe. The callback is invoked outside
// itemMu by resolveAdapter, so a resolver may safely re-enter daemon code
// without deadlocking.
func (d *Daemon) setAdapterForTest(fn func(string) (adapter.Adapter, bool)) {
	d.itemMu.Lock()
	d.adapterFor = fn
	d.itemMu.Unlock()
}
