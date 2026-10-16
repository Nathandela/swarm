package skeleton

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/testbin"
)

func TestMain(m *testing.M) {
	runAccountFixture()
	code := m.Run()
	testBins.Sweep()
	testbin.KillProcessesOf(fakeCodexBin, s19GatewayPath)
	for _, exe := range []string{fakeCodexBin, s19GatewayPath} {
		if exe != "" {
			_ = os.RemoveAll(filepath.Dir(exe))
		}
	}
	os.Exit(code)
}
