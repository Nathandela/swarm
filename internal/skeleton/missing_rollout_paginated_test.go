package skeleton

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/appserver"
)

// TestIsMissingRollout_RecognizesThePaginatedHistoryMissingSourceRollout pins the second recorded
// wording of the pre-first-turn race. Codex >= 0.156 with history_mode=paginated answers the
// same early thread/resume with "invalid paginated history lineage ... missing source rollout"
// (both strings are in the vendored 0.162.1 binary; 15 of 23 fresh 0.156.1-0.160.1 launches
// logged "can never subscribe" with it). Treating it as a hard failure withdraws structured chat
// for the life of the session.
func TestIsMissingRollout_RecognizesThePaginatedHistoryMissingSourceRollout(t *testing.T) {
	const id = "01a00339-a80e-72a0-966f-116427b6b9ce"
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"legacy wording", &appserver.RPCError{Code: -32600, Message: "no rollout found for thread id " + id}, true},
		{"paginated wording", &appserver.RPCError{Code: -32600, Message: "invalid paginated history lineage for " + id + ": missing source rollout"}, true},
		{"other paginated lineage fault", &appserver.RPCError{Code: -32600, Message: "invalid paginated history lineage for " + id + ": cycle detected"}, false},
		{"unrelated rpc error", &appserver.RPCError{Code: -32600, Message: "thread is archived"}, false},
		{"not an rpc error", errors.New("missing source rollout"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isMissingRollout(tc.err); got != tc.want {
				t.Errorf("isMissingRollout(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// missingSourceRolloutConn answers every thread/resume with the paginated-history wording.
type missingSourceRolloutConn struct{ calls atomic.Int32 }

func (c *missingSourceRolloutConn) Call(_ context.Context, method string, _, _ any) error {
	if method != "thread/resume" {
		return nil
	}
	c.calls.Add(1)
	return &appserver.RPCError{Code: -32600, Message: "invalid paginated history lineage for " +
		rejoinPersistedTarget + ": missing source rollout"}
}

func (c *missingSourceRolloutConn) Respond(context.Context, json.RawMessage, any) error { return nil }

func (c *missingSourceRolloutConn) Close() error { return nil }

// TestSubscribeSessionThread_AMissingSourceRolloutIsRetriedOnlyWithinTheJoinDeadline pins the
// bound on the paginated wording. It is retried like the pre-first-turn race (every recorded hit
// was a fresh launch, 3-45 s in), but a source rollout that is really gone never appears, so the
// retry gives up after the join deadline and the session degrades honestly instead of holding
// a composer whose transcript can never move.
func TestSubscribeSessionThread_AMissingSourceRolloutIsRetriedOnlyWithinTheJoinDeadline(t *testing.T) {
	r := newPersistedRejoinRig(t, rejoinPersistedTarget)
	r.sk.backendReady = 500 * time.Millisecond
	instance, ok := r.sk.sessionInstance(r.local)
	if !ok {
		t.Fatal("launched session has no instance")
	}
	conn := &missingSourceRolloutConn{}
	feed := newBackendFeed()
	if !r.sk.registerBackendFeedForInstance(r.local, instance, rejoinPersistedTarget, conn, feed) {
		t.Fatal("feed registration was refused")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.sk.subscribeSessionThread(r.local, conn, rejoinPersistedTarget, feed.epoch, instance, false)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		r.sk.forgetBackendForFeed(r.local, feed.epoch)
		<-done
		t.Fatal("a missing source rollout was retried past the join deadline; a rollout that is " +
			"really gone would keep the session retrying for its whole life")
	}
	if n := conn.calls.Load(); n < 2 {
		t.Errorf("thread/resume attempted %d time(s); the paginated wording is the pre-first-turn race "+
			"and must be retried before it is treated as permanent", n)
	}
	if _, live := r.sk.sessionBackendFor(r.local); live {
		t.Error("the session still holds a message sink after its thread proved unsubscribable")
	}
}
