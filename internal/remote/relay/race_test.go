package relay

// R1b review HIGH-1 — regression test for the data race between handleAuthResp
// (which wrote serverConn.rid outside any lock) and removeConn (which scans every
// other connection's rid under s.mu for the H3 reap walk). Driven under -race,
// concurrent full authentications and disconnects flag the unsynchronized rid
// write until it is moved under s.mu. It also passes without -race, so it is a
// permanent guard, not a probe.

import (
	"crypto/ed25519"
	"sync"
	"testing"
)

func TestRelay_ConcurrentAuthAndDisconnectRaceFree(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, func(c *Config) {
		// Keep rate limiting and the concurrency cap out of the way: we are hunting
		// the rid data race in the auth/teardown paths, not exercising quotas.
		c.Quotas.ConnPerMin = 1 << 20
		c.Quotas.OpsPerMin = 1 << 20
		c.Quotas.MaxConcurrentConnections = 0
	})
	ctx := testCtx(t)

	const workers = 24
	const iters = 8

	// Pre-generate keys on the test goroutine: newRelayAuthKey may call t.Fatalf,
	// which is illegal from a spawned goroutine.
	type kp struct {
		pub  ed25519.PublicKey
		priv ed25519.PrivateKey
	}
	keys := make([]kp, 0, workers*iters)
	for i := 0; i < workers*iters; i++ {
		pub, priv := newRelayAuthKey(t)
		keys = append(keys, kp{pub: pub, priv: priv})
	}

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(base int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				k := keys[base+i]
				// Each full Dial authenticates (writing serverConn.rid); the immediate
				// Close triggers removeConn's rid scan on other live connections. A
				// transient error under churn is irrelevant to the race hunt.
				cl, err := Dial(ctx, srv.URL(), authFor(k.pub, k.priv))
				if err != nil {
					continue
				}
				_ = cl.Close()
			}
		}(w * iters)
	}
	wg.Wait()
}
