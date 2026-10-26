package protocol

import (
	"github.com/Nathandela/swarm/internal/testbin"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	if code, parent := testbin.Supervise(); parent {
		os.Exit(code)
	}
	os.Exit(m.Run())
}
