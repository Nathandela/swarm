package relay

// Public-service storage admission is tested in this package so the bound is
// proved at the transaction boundary, not only at one HTTP handler.

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newAdmissionRelay(t *testing.T, mutate func(*Config), opts ...Option) (*Server, *fakeClock) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.DBPath = dbPathForTest(t)
	cfg.Listen = "127.0.0.1:0"
	clk := newFakeClock()
	if mutate != nil {
		mutate(&cfg)
	}
	opts = append([]Option{WithClock(clk)}, opts...)
	srv, err := New(cfg, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv, clk
}

func requireAdmissionReason(t *testing.T, err error, want storageAdmissionReason) {
	t.Helper()
	var got *storageAdmissionError
	if !errors.As(err, &got) {
		t.Fatalf("error = %v, want storageAdmissionError(%s)", err, want)
	}
	if got.reason != want {
		t.Fatalf("admission reason = %q, want %q", got.reason, want)
	}
}

func TestPublicAdmission_GlobalDurableObjectCapIsAtomic(t *testing.T) {
	srv, _ := newAdmissionRelay(t, func(c *Config) {
		// One authorization creates exactly three caller-controlled durable rows:
		// the consent plus two directed pair edges. A second distinct pair must be
		// refused as a whole; none of its rows may leak through the failed tx.
		c.Quotas.MaxDurableObjects = 4
	})

	if err := srv.st.authorizePair("pairer-a", "device-a", "ceremony-a"); err != nil {
		t.Fatalf("first authorizePair: %v", err)
	}
	before := srv.st.storageSnapshot()
	if before.DurableObjects != 3 {
		t.Fatalf("durable objects after first pair = %d, want 3", before.DurableObjects)
	}
	err := srv.st.authorizePair("pairer-b", "device-b", "ceremony-b")
	requireAdmissionReason(t, err, admissionDurableObjects)
	after := srv.st.storageSnapshot()
	if after.DurableObjects != before.DurableObjects {
		t.Fatalf("refused transaction changed durable objects: before=%d after=%d", before.DurableObjects, after.DurableObjects)
	}
	if srv.st.isPaired("pairer-b", "device-b") {
		t.Fatal("refused second pair left a partial durable authorization")
	}
}

func TestPublicAdmission_ReopenRecountsExistingObjectsBeforeAdmittingGrowth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.db")
	clk := newFakeClock()
	cfg := DefaultConfig()
	cfg.DBPath = path
	cfg.Quotas.MaxDurableObjects = 4

	first, err := New(cfg, WithClock(clk))
	if err != nil {
		t.Fatalf("first New: %v", err)
	}
	if err := first.st.authorizePair("pairer-a", "device-a", "ceremony-a"); err != nil {
		t.Fatalf("first authorizePair: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	reopened, err := New(cfg, WithClock(clk))
	if err != nil {
		t.Fatalf("reopened New: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if got := reopened.st.storageSnapshot().DurableObjects; got != 3 {
		t.Fatalf("reopened durable object count = %d, want 3", got)
	}
	err = reopened.st.authorizePair("pairer-b", "device-b", "ceremony-b")
	requireAdmissionReason(t, err, admissionDurableObjects)
	if reopened.st.isPaired("pairer-b", "device-b") {
		t.Fatal("reopened store admitted a pair past its reconstructed global cap")
	}
}

func TestPublicAdmission_AppendAndTokenShareTheGlobalObjectCount(t *testing.T) {
	srv, _ := newAdmissionRelay(t, func(c *Config) { c.Quotas.MaxDurableObjects = 4 })
	if _, err := srv.st.appendItem("target", "source", []byte("one"), 1); err != nil {
		t.Fatalf("appendItem: %v", err)
	}
	if err := srv.st.putToken("target", "opaque-token"); err != nil {
		t.Fatalf("putToken: %v", err)
	}
	if got := srv.st.storageSnapshot().DurableObjects; got != 4 {
		t.Fatalf("objects after mailbox (seq+bucket+item) and token = %d, want 4", got)
	}
	_, err := srv.st.appendItem("target", "source", []byte("two"), 2)
	requireAdmissionReason(t, err, admissionDurableObjects)
	if got := srv.st.mailboxDepth("target"); got != 1 {
		t.Fatalf("mailbox depth after refused append = %d, want 1", got)
	}
}

func TestPublicAdmission_GlobalGrowthWriteWindowBoundsDistributedSources(t *testing.T) {
	srv, clk := newAdmissionRelay(t, func(c *Config) {
		c.Quotas.DurableGrowthWritesPerMin = 1
	})
	if err := srv.st.authorizePair("pairer-a", "device-a", "ceremony-a"); err != nil {
		t.Fatalf("first growth write: %v", err)
	}
	err := srv.st.authorizePair("pairer-b", "device-b", "ceremony-b")
	requireAdmissionReason(t, err, admissionGrowthWrites)

	clk.Advance(time.Minute + time.Second)
	if err := srv.st.authorizePair("pairer-b", "device-b", "ceremony-b"); err != nil {
		t.Fatalf("growth write after global window reset: %v", err)
	}
}

func TestPublicAdmission_RefusesGrowthWhenDBOrFreeDiskFenceIsHit(t *testing.T) {
	t.Run("database bytes", func(t *testing.T) {
		srv, _ := newAdmissionRelay(t, func(c *Config) { c.Quotas.MaxDBBytes = 1 })
		err := srv.st.authorizePair("pairer", "device", "ceremony")
		requireAdmissionReason(t, err, admissionDBBytes)
	})

	t.Run("free disk", func(t *testing.T) {
		srv, _ := newAdmissionRelay(t, func(c *Config) {
			c.Quotas.DiskFreeMinBytes = 100
		}, WithDiskFreeFunc(func() (uint64, error) { return 99, nil }))
		err := srv.st.authorizePair("pairer", "device", "ceremony")
		requireAdmissionReason(t, err, admissionFreeDisk)
	})
}

func TestPublicAdmission_PostMutationDBSizeFenceRollsBackWholeTransaction(t *testing.T) {
	srv, _ := newAdmissionRelay(t, nil)
	info, err := os.Stat(srv.cfg.DBPath)
	if err != nil {
		t.Fatalf("stat db: %v", err)
	}
	// Preflight passes by one byte. The transaction's exact logical-growth report
	// observes the large pending record before bbolt spills dirty pages and must roll
	// back seq, bucket and item together.
	srv.st.admission.mu.Lock()
	srv.st.admission.limits.MaxDBBytes = info.Size() + 1
	srv.st.admission.mu.Unlock()
	_, err = srv.st.appendItem("target", "source", make([]byte, 512<<10), 1)
	requireAdmissionReason(t, err, admissionDBBytes)
	if got := srv.st.storageSnapshot().DurableObjects; got != 0 {
		t.Fatalf("post-mutation DB refusal left %d durable objects, want 0", got)
	}
	if got := srv.st.mailboxDepth("target"); got != 0 {
		t.Fatalf("post-mutation DB refusal left mailbox depth %d, want 0", got)
	}
}

func TestPublicCleanup_RemovesEmptyMailboxBucketsButPreservesCursorHighWater(t *testing.T) {
	srv, _ := newAdmissionRelay(t, nil)
	if _, err := srv.st.appendItem("target", "source", []byte("opaque"), 10); err != nil {
		t.Fatalf("appendItem: %v", err)
	}
	if err := srv.st.purgeOlderThan(10); err != nil {
		t.Fatalf("purgeOlderThan: %v", err)
	}
	snap := srv.st.storageSnapshot()
	if snap.DurableObjects != 1 {
		t.Fatalf("durable objects after purging sole item = %d, want 1 retained seq high-water only", snap.DurableObjects)
	}
	if snap.CleanupItems != 1 || snap.CleanupMailboxes != 1 {
		t.Fatalf("cleanup metrics = items:%d mailboxes:%d, want 1/1", snap.CleanupItems, snap.CleanupMailboxes)
	}
	items, _, reset, _, err := srv.st.readItemsPageForIncarnation("target", "", 1, 10, 1024)
	if err != nil || reset || len(items) != 0 {
		t.Fatalf("read at retained cursor: items=%d reset=%v err=%v, want empty continuous mailbox", len(items), reset, err)
	}
}

func TestPublicMetrics_AreAggregateAndReadinessReflectsCapacity(t *testing.T) {
	srv, _ := newAdmissionRelay(t, func(c *Config) {
		c.AdminListen = "127.0.0.1:0"
		c.Quotas.MaxDurableObjects = 3
	})
	if err := srv.Start(testCtx(t)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := srv.st.authorizePair("private-pairer", "private-device", "private-ceremony"); err != nil {
		t.Fatalf("authorizePair to exact cap: %v", err)
	}
	if err := srv.st.authorizePair("other-pairer", "other-device", "other-ceremony"); err == nil {
		t.Fatal("growth past cap succeeded")
	}

	code, body := getAdmin(t, srv, "/metrics")
	if code != http.StatusOK {
		t.Fatalf("GET /metrics status=%d body=%q", code, body)
	}
	for _, want := range []string{
		"relay_durable_objects 3",
		`relay_storage_admission_refusals_total{reason="durable_objects"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q: %s", want, body)
		}
	}
	for _, private := range []string{"private-pairer", "private-device", "private-ceremony"} {
		if strings.Contains(body, private) {
			t.Errorf("metrics leaked caller-controlled identifier %q: %s", private, body)
		}
	}

	code, body = getAdmin(t, srv, "/readyz")
	if code != http.StatusServiceUnavailable || !strings.Contains(body, "durable object") {
		t.Fatalf("readyz at durable-object cap: status=%d body=%q, want 503 naming capacity", code, body)
	}
}
