package persist

import (
	"os"
	"testing"

	"github.com/Nathandela/swarm/internal/testbin"
)

func TestMain(m *testing.M) {
	if code, parent := testbin.Supervise(); parent {
		os.Exit(code)
	}
	os.Exit(m.Run())
}
