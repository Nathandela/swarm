package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/opencodebridge"
)

func TestOpenCodeReadyMinimumNativeVersion(t *testing.T) {
	f := newOpenCodeRuntimeFixture(t)
	client, err := opencodebridge.NewClient(f.b.SocketPath, f.b.Token)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	for _, tc := range []struct {
		version string
		allowed bool
	}{{"1.19.3", true}, {"1.19.4", true}, {"1.20.0", true}, {"2.0.0", true}, {"1.19.2", false}, {"1.19.3-rc.1", false}, {"1.20.0+build", false}, {"01.20.0", false}, {"1.20", false}, {"invalid", false}} {
		t.Run(tc.version, func(t *testing.T) {
			f.nativeVersion.Store(tc.version)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err := waitOpenCodeReady(ctx, client, f.b, false)
			if (err == nil) != tc.allowed {
				t.Fatalf("native version %q accepted=%v, want %v: %v", tc.version, err == nil, tc.allowed, err)
			}
			if f.initialCalls.Load() != 0 || f.released.Load() {
				t.Fatal("version readiness check submitted input or released its fence")
			}
		})
	}
}
