// Package testbin builds the swarm and swarm-fake-agent binaries once per test
// binary run, shared by the skeleton, protocol, and e2e integration suites that
// each need a real subprocess to shim/agent against.
package testbin

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// Binaries resolves the swarm and swarm-fake-agent binary paths, building them
// exactly once (guarded by an internal sync.Once) no matter how many times
// Build is called.
type Binaries struct {
	once      sync.Once
	Swarm     string
	FakeAgent string
	err       error
}

// Build compiles cmd/swarm and cmd/swarm-fake-agent into a fresh temp dir named
// tmpPrefix*, once. On a build failure every call (not just the first) invokes
// onFail with the recorded error, so each caller keeps its own skip-vs-fail
// policy instead of Build choosing one for everybody.
func (b *Binaries) Build(t *testing.T, tmpPrefix string, onFail func(t *testing.T, err error)) {
	t.Helper()
	b.once.Do(func() {
		dir, err := os.MkdirTemp("", tmpPrefix)
		if err != nil {
			b.err = err
			return
		}
		b.Swarm = filepath.Join(dir, "swarm")
		b.FakeAgent = filepath.Join(dir, "swarm-fake-agent")
		for _, bin := range []struct{ out, pkg string }{
			{b.Swarm, "github.com/Nathandela/swarm/cmd/swarm"},
			{b.FakeAgent, "github.com/Nathandela/swarm/cmd/swarm-fake-agent"},
		} {
			cmd := exec.Command("go", "build", "-o", bin.out, bin.pkg)
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				b.err = err
				return
			}
		}
	})
	if b.err != nil {
		onFail(t, b.err)
	}
}
