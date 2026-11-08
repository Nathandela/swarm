package skeleton

import (
	"context"
	"encoding/json"
	"github.com/Nathandela/swarm/internal/protocol"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestNativeOwnerInputRequiresLiveEnrolledClaude(t *testing.T) {
	r := newClaudeReceiptRig(t)
	if !r.sk.nativeOwnerInput(r.local, r.instance) {
		t.Fatal("qualified enrolled Claude input was not exposed")
	}
	if r.sk.nativeOwnerInput(r.local, "replacement") {
		t.Fatal("replacement inherited owner input capability")
	}
	if err := r.client.Revoke(t.Context(), r.worker); err != nil {
		t.Fatal(err)
	}
	awaitNativeOwnerHint(t, r.sk, r.local, r.instance, false)
}

func TestNativeOwnerInputDoesNotTrustPersistedHintOrUnknownEnrollment(t *testing.T) {
	r := newClaudeReceiptRig(t)
	caps, ok := r.sk.sessionCapabilities(r.local)
	if !ok {
		t.Fatal("no session capabilities")
	}
	caps.NativeOwnerInput = true
	r.sk.registerSessionCapabilities(r.local, caps)
	if persisted, _ := r.sk.sessionCapabilities(r.local); persisted.NativeOwnerInput {
		t.Fatal("persisted hint created current native proof")
	}
	if err := os.Remove(filepath.Join(r.sk.stateDir, r.local, "shim-launch.json")); err != nil {
		t.Fatal(err)
	}
	awaitNativeOwnerHint(t, r.sk, r.local, r.instance, false)
}

func TestNativeOwnerInputCodexRequiresExactLiveBackendAndThread(t *testing.T) {
	d, b, feed := newCodexManagerSendRig(t)
	d.startNativeOwnerHints(context.Background())
	t.Cleanup(d.ownerInputHints.close)
	d.registerSessionCapabilities(b.Session, protocol.SessionCapabilities{Provider: "codex", SessionInstance: b.SessionInstance, StructuredChat: true})
	backend := d.backend.live[b.Session]
	backend.conn = &codexManagerTestConn{}
	awaitNativeOwnerHint(t, d, b.Session, b.SessionInstance, true)
	backend.threadID = ""
	d.ownerInputHints.invalidate(b.Session)
	awaitNativeOwnerHint(t, d, b.Session, b.SessionInstance, false)
	backend.threadID = b.NativeSessionID
	feed.retired.Store(true)
	d.ownerInputHints.invalidate(b.Session)
	awaitNativeOwnerHint(t, d, b.Session, b.SessionInstance, false)
}

func TestNativeOwnerHintCannotWaitForHungBroker(t *testing.T) {
	r := newClaudeReceiptRig(t)
	if !r.sk.nativeOwnerInput(r.local, r.instance) {
		t.Fatal("fixture did not start with a proven sink")
	}
	desc := r.broker.Descriptor()
	if e := r.broker.Close(); e != nil {
		t.Fatal(e)
	}
	listener, e := net.Listen("unix", desc.SocketPath)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Chmod(desc.SocketPath, 0600); e != nil {
		t.Fatal(e)
	}
	release := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
		select {
		case <-release:
		case <-req.Context().Done():
		}
	}), ReadHeaderTimeout: time.Second}
	serverDone := make(chan struct{})
	go func() { defer close(serverDone); _ = server.Serve(listener) }()
	defer func() { close(release); _ = server.Close(); <-serverDone }()
	done := make(chan bool, 1)
	start := time.Now()
	go func() { done <- r.sk.nativeOwnerInput(r.local, r.instance) }()
	select {
	case <-done:
		t.Logf("native hint latency=%s", time.Since(start))
	case <-time.After(75 * time.Millisecond):
		t.Errorf("capability hint synchronously waits on broker from shared event fanout path")
		select {
		case <-done:
			t.Logf("native hint latency=%s", time.Since(start))
		case <-time.After(time.Second):
			t.Fatal("hint did not finish")
		}
	}
}

func awaitNativeOwnerHint(t *testing.T, d *Daemon, local, instance string, want bool) {
	t.Helper()
	until := time.Now().Add(3 * time.Second)
	for time.Now().Before(until) {
		got := d.nativeOwnerInput(local, instance)
		hint, ready := d.ownerInputHints.snapshot(local)
		if ready && hint.caps.SessionInstance == instance && got == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("native owner hint never reached %v", want)
}

func TestNativeOwnerHintsBoundQueueAndCancelHungRefreshOnShutdown(t *testing.T) {
	r := newClaudeReceiptRig(t)
	desc := r.broker.Descriptor()
	if err := r.broker.Close(); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", desc.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(desc.SocketPath, 0600); err != nil {
		t.Fatal(err)
	}
	seen := make(chan struct{}, 1)
	release := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
		select {
		case seen <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-req.Context().Done():
		}
	}), ReadHeaderTimeout: time.Second}
	serverDone := make(chan struct{})
	go func() { defer close(serverDone); _ = server.Serve(listener) }()
	defer func() { close(release); _ = server.Close(); <-serverDone }()
	h := r.sk.ownerInputHints
	h.invalidate(r.local)
	if r.sk.nativeOwnerInput(r.local, r.instance) {
		t.Fatal("invalidated proof survived")
	}
	select {
	case <-seen:
	case <-time.After(time.Second):
		t.Fatal("bounded refresh did not start")
	}
	started := time.Now()
	for i := range 512 {
		_ = r.sk.nativeOwnerInput("cold-"+strconv.Itoa(i), "unknown")
	}
	t.Logf("512 cold hint lookups=%s", time.Since(started))
	if time.Since(started) > 200*time.Millisecond {
		t.Fatal("cold roster reads performed synchronous probes")
	}
	h.mu.Lock()
	pending, queued := len(h.pending), len(h.queue)
	h.mu.Unlock()
	if pending > ownerInputHintQueue+1 || queued > ownerInputHintQueue {
		t.Fatal("native hint refresh work grew beyond its bound")
	}
	closed := make(chan struct{})
	go func() { h.close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("shutdown did not cancel hung native refresh")
	}
	if r.sk.nativeOwnerInput(r.local, r.instance) {
		t.Fatal("closed cache retained native hint")
	}
}

func TestNativeOwnerHintRetainsEndedWorkerInstance(t *testing.T) {
	r := newClaudeReceiptRig(t)
	if err := r.sk.core.Kill(r.local); err != nil {
		t.Fatal(err)
	}
	awaitNativeOwnerHint(t, r.sk, r.local, r.instance, false)
	meta, ok := r.sk.core.Get(r.local)
	if !ok {
		t.Fatal("ended worker disappeared")
	}
	caps, present := r.sk.ownerSessionCapabilities(r.local, meta.ShimPID, meta.ShimStartTime)
	if !present || caps.SessionInstance != r.instance || caps.NativeOwnerInput {
		t.Fatal("ended worker lost immutable instance or retained native readiness")
	}
}

func TestNativeOwnerSnapshotRejectsWrongPhysicalIncarnation(t *testing.T) {
	r := newClaudeReceiptRig(t)
	meta, _ := r.sk.core.Get(r.local)
	if _, ok := r.sk.ownerSessionCapabilities(r.local, meta.ShimPID, meta.ShimStartTime+1); ok {
		t.Fatal("replacement inherited cached capabilities")
	}
	r.sk.ownerInputHints.invalidate(r.local)
	if _, ok := r.sk.ownerSessionCapabilities(r.local, meta.ShimPID, meta.ShimStartTime); ok {
		t.Fatal("invalidated snapshot remained visible")
	}
}

func TestNativeOwnerRefreshCannotRetagOldBaseCapabilities(t *testing.T) {
	r := newClaudeReceiptRig(t)
	caps, _ := r.sk.sessionCapabilities(r.local)
	caps.SessionInstance = "retired-instance"
	r.sk.registerSessionCapabilities(r.local, caps)
	_ = r.sk.nativeOwnerInput(r.local, r.instance)
	deadline := time.Now().Add(time.Second)
	for {
		h := r.sk.ownerInputHints
		h.mu.Lock()
		_, completed := h.entries[r.local]
		pending := h.pending[r.local]
		h.mu.Unlock()
		if completed && pending == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("refresh did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	meta, _ := r.sk.core.Get(r.local)
	if _, present := r.sk.ownerSessionCapabilities(r.local, meta.ShimPID, meta.ShimStartTime); present {
		t.Fatal("retired base instance was tagged as current physical custody")
	}
}

func TestNativeOwnerInvalidationDiscardsLateSuccessfulRefresh(t *testing.T) {
	r := newClaudeReceiptRig(t)
	desc := r.broker.Descriptor()
	if err := r.broker.Close(); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", desc.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(desc.SocketPath, 0600); err != nil {
		t.Fatal(err)
	}
	seen := make(chan struct{}, 1)
	release := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		select {
		case seen <- struct{}{}:
		default:
		}
		select {
		case <-release:
			_ = json.NewEncoder(w).Encode(r.worker)
		case <-req.Context().Done():
		}
	}), ReadHeaderTimeout: time.Second}
	serverDone := make(chan struct{})
	go func() { defer close(serverDone); _ = server.Serve(listener) }()
	defer func() { _ = server.Close(); <-serverDone }()
	// Keep the refresh owner isolated from the full assembly's status fanout,
	// which may legitimately request a new qualification after invalidation.
	caps, _ := r.sk.sessionCapabilities(r.local)
	meta, _ := r.sk.core.Get(r.local)
	r.sk.ownerInputHints.close()
	d := &Daemon{core: r.sk.core, stateDir: r.sk.stateDir}
	d.capStore.dir = r.sk.stateDir
	d.registerSessionCapabilities(r.local, caps)
	if err := d.recordSessionInstance(r.local, r.instance, meta.ShimPID, meta.ShimStartTime); err != nil {
		t.Fatal(err)
	}
	d.startNativeOwnerHints(t.Context())
	t.Cleanup(d.ownerInputHints.close)
	h := d.ownerInputHints
	_ = d.nativeOwnerInput(r.local, r.instance)
	select {
	case <-seen:
	case <-time.After(time.Second):
		t.Fatal("refresh did not reach broker")
	}
	h.invalidate(r.local)
	close(release)
	// One worker makes this absent-session request a completion barrier for the
	// preceding in-flight native response, without scheduling a new target refresh.
	_, _ = h.snapshot("absent-refresh-barrier")
	deadline := time.Now().Add(time.Second)
	for {
		h.mu.Lock()
		_, barrier := h.entries["absent-refresh-barrier"]
		_, resurrected := h.entries[r.local]
		h.mu.Unlock()
		if barrier {
			if resurrected {
				t.Fatal("late response republished invalidated native hint")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("refresh completion barrier did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}
