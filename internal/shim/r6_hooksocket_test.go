package shim

// R6 (bd agents-tracker-hggx.7) FAILING-FIRST (TDD RED, GG-5) tests for the LIVE hook
// socket: the second per-session listener playbook §6.1 requires ("Claude hooks post to a
// per-session shim-owned socket"), demuxed on its own leading byte exactly the way the
// existing control socket demuxes daemon.VersionProbeTag/'{'/0x00 in internal/skeleton/
// conn.go's handleConn -- except this demux lives IN THE SHIM, on cfg.HookSocketPath,
// entirely separate from and never touching the PTY/control socket (server.go, listen()).
//
// THE SEAMS THIS FILE PINS, beyond r6_hookspool_test.go's HookSpool (undefined symbols ->
// compile-fail RED):
//
//	// Config gains:
//	    HookSocketPath    string // per-session hook UDS; "" disables the listener entirely
//	                              // (requirement 7's compat: an old-shim launch config that
//	                              // never sets this field runs exactly as it does today)
//	    HookSpoolMaxBytes int    // 0 => hookSpoolDefaultMaxBytes; forwarded to OpenHookSpool
//
//	const HookPostTag  byte = '{'  // hook socket, first byte: one self-delimited JSON POST
//	const HookDrainTag byte = 'D'  // hook socket, first byte: one drain request/response
//	const HookAckByte  byte = 0x01 // written back after Append returns nil; nothing else is
//	                                // ever written on that connection before close, so an
//	                                // honest "no ack" is simply an absent/closed read.
//
//	type HookDrainRequest struct {
//	    FromSeq uint64 `json:"from_seq"` // records wanted: Seq>FromSeq
//	    FoldSeq uint64 `json:"fold_seq"` // 0 = nothing new to fold; else Compact(FoldSeq) FIRST
//	}
//	type HookDrainResponse struct {
//	    Records     []HookRecord    `json:"records"`
//	    Gap         bool            `json:"gap,omitempty"`
//	    GapBoundary uint64          `json:"gap_boundary,omitempty"`
//	}
//	// HookRecord (r6_hookspool_test.go) gains JSON tags: Seq uint64 `json:"seq"`,
//	// Body json.RawMessage `json:"body"`.
//
// PROTOCOL, stated precisely because both sides are exercised here:
//
//	POST ('{'): client writes one self-delimited JSON value (any bytes -- the shim does not
//	  parse it, exactly as HookSpool.Append is body-oblivious); the shim Appends it to the
//	  session's HookSpool (SessionDir/HookSpoolFile) and, ONLY when Append returns nil,
//	  writes back ONE HookAckByte and closes. A refusal (ErrHookSpoolFull, disk error) closes
//	  WITHOUT writing the byte: the client's ack-read (bounded by its own deadline) sees EOF
//	  or a closed connection either way, which is the single honest "not durably accepted"
//	  signal for every failure mode.
//	DRAIN ('D'): client writes one self-delimited JSON HookDrainRequest; the shim compacts up
//	  to FoldSeq (when >0), reads every record with Seq>FromSeq up to the first gap, writes
//	  back one self-delimited JSON HookDrainResponse, and closes. Stateless per connection --
//	  no live-follow; a poller reconnects for its next batch (skeleton's HookDrainer, R6's
//	  daemon-side half, owns the poll cadence).
//
// A "shim crash between accept and ack" is proven the way this repo proves every such
// window: the record's durability never depends on whether the ACK made it back to the
// client (fsync happens inside Append, strictly before the handler is even ELIGIBLE to write
// HookAckByte), so severing the CLIENT's view of the connection at that exact point and then
// independently reading the spool is the deterministic, non-racy equivalent of the process
// dying there -- see TestHookSocket_RecordSurvivesEvenWhenTheClientNeverSeesTheAck.
// TestHookSocket_RestartOverTheSameSessionDirSeesEveryAckedPost additionally kills the WHOLE
// shim (the existing SigKill control-socket path) and starts a second one, for the literal
// restart case.

import (
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/shimwire"
)

// hookCfg is helperConfig plus a hook socket path derived the same short-path way
// newSocketPath does (the 104-byte sun_path limit binds this socket too).
func hookCfg(t *testing.T) Config {
	t.Helper()
	cfg := helperConfig(t, modeIdle, nil, nil)
	cfg.HookSocketPath = newSocketPath(t)
	return cfg
}

// dialHookSocket connects to the hook socket, retrying until the shim has bound it.
func dialHookSocket(t *testing.T, path string) net.Conn {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.Dial("unix", path)
		if err == nil {
			return conn
		}
		if time.Now().After(deadline) {
			t.Fatalf("dial hook socket %s: %v", path, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// postHook writes tag+body (a self-delimited JSON value) and returns whether the shim's
// single ack byte arrived within timeout. It always closes the connection.
func postHook(t *testing.T, path string, body []byte, timeout time.Duration) (acked bool) {
	t.Helper()
	conn := dialHookSocket(t, path)
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write(body); err != nil {
		t.Fatalf("write hook post: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	var ack [1]byte
	n, err := conn.Read(ack[:])
	if err != nil || n != 1 {
		return false
	}
	return ack[0] == HookAckByte
}

func killShim(c *shimClient) {
	c.writeControl(shimwire.Control{Type: shimwire.TypeSignal, Sig: shimwire.SigKill})
}

// ---------------------------------------------------------------------------
// (1) fsync-before-ack, over the wire.
// ---------------------------------------------------------------------------

func TestHookSocket_PostIsAckedAfterDurableAccept(t *testing.T) {
	cfg := hookCfg(t)
	ch := runShimAsync(cfg)

	if !postHook(t, cfg.HookSocketPath, []byte(`{"event":"UserPromptSubmit","sequence":1}`), 3*time.Second) {
		t.Fatalf("hook post to the shim's hook socket was never acked")
	}

	ctl := dialShim(t, cfg.SocketPath)
	ctl.startReader()
	ctl.hello(shimwire.Version)
	killShim(ctl)
	waitRun(t, ch, 10*time.Second)

	spool, err := OpenHookSpool(filepath.Join(cfg.SessionDir, HookSpoolFile), 0)
	if err != nil {
		t.Fatalf("open the shim's own spool file after exit: %v", err)
	}
	defer func() { _ = spool.Close() }()
	recs, _, hasGap, err := spool.ReadFrom(0)
	if err != nil || hasGap {
		t.Fatalf("ReadFrom(0): recs=%v hasGap=%v err=%v", recs, hasGap, err)
	}
	if len(recs) != 1 {
		t.Fatalf("spool holds %d record(s), want 1 (the acked post)", len(recs))
	}
}

func TestHookSocket_RecordSurvivesEvenWhenTheClientNeverSeesTheAck(t *testing.T) {
	// The client hangs up the INSTANT it has written its post -- before any ack could
	// possibly have been read, whatever the shim does server-side. Durability must not
	// depend on the client's cooperation: fsync happens inside Append, unconditionally,
	// before the handler is even eligible to write HookAckByte.
	cfg := hookCfg(t)
	ch := runShimAsync(cfg)

	conn := dialHookSocket(t, cfg.HookSocketPath)
	if _, err := conn.Write([]byte(`{"event":"Stop","sequence":1}`)); err != nil {
		t.Fatalf("write hook post: %v", err)
	}
	_ = conn.Close() // sever before reading any reply

	ctl := dialShim(t, cfg.SocketPath)
	ctl.startReader()
	ctl.hello(shimwire.Version)
	killShim(ctl)
	waitRun(t, ch, 10*time.Second)

	spool, err := OpenHookSpool(filepath.Join(cfg.SessionDir, HookSpoolFile), 0)
	if err != nil {
		t.Fatalf("open spool: %v", err)
	}
	defer func() { _ = spool.Close() }()
	recs, _, hasGap, err := spool.ReadFrom(0)
	if err != nil || hasGap {
		t.Fatalf("ReadFrom(0): recs=%v hasGap=%v err=%v", recs, hasGap, err)
	}
	if len(recs) != 1 {
		t.Fatalf("spool holds %d record(s), want 1 — a post whose ack the client never saw must still be durably spooled", len(recs))
	}
}

func TestHookSocket_RestartOverTheSameSessionDirSeesEveryAckedPost(t *testing.T) {
	// The literal restart: kill the WHOLE shim after an acked post, start a second one
	// pointed at the same SessionDir (a fresh socket, since the old one was torn down on
	// exit), and prove the new instance's spool already holds what the old one acked.
	dir := t.TempDir()
	cfg1 := helperConfig(t, modeIdle, nil, nil)
	cfg1.SessionDir = dir
	cfg1.HookSocketPath = newSocketPath(t)
	ch1 := runShimAsync(cfg1)

	if !postHook(t, cfg1.HookSocketPath, []byte(`{"event":"Notification"}`), 3*time.Second) {
		t.Fatalf("first shim never acked the post")
	}
	ctl1 := dialShim(t, cfg1.SocketPath)
	ctl1.startReader()
	ctl1.hello(shimwire.Version)
	killShim(ctl1)
	waitRun(t, ch1, 10*time.Second)

	cfg2 := helperConfig(t, modeIdle, nil, nil)
	cfg2.SessionDir = dir // SAME session dir -> same spool file
	cfg2.HookSocketPath = newSocketPath(t)
	ch2 := runShimAsync(cfg2)
	ctl2 := dialShim(t, cfg2.SocketPath)
	ctl2.startReader()
	ctl2.hello(shimwire.Version)

	if !postHook(t, cfg2.HookSocketPath, []byte(`{"event":"Stop"}`), 3*time.Second) {
		t.Fatalf("second (restarted) shim never acked its own post")
	}
	killShim(ctl2)
	waitRun(t, ch2, 10*time.Second)

	spool, err := OpenHookSpool(filepath.Join(dir, HookSpoolFile), 0)
	if err != nil {
		t.Fatalf("open spool: %v", err)
	}
	defer func() { _ = spool.Close() }()
	recs, _, hasGap, err := spool.ReadFrom(0)
	if err != nil || hasGap {
		t.Fatalf("ReadFrom(0): recs=%v hasGap=%v err=%v", recs, hasGap, err)
	}
	if len(recs) != 2 {
		t.Fatalf("spool across the restart holds %d record(s), want 2 (one from each shim instance, same session dir)", len(recs))
	}
	if recs[0].Seq != 1 || recs[1].Seq != 2 {
		t.Fatalf("sequences across restart = [%d, %d], want [1, 2] (monotonic across the restart, not reset)", recs[0].Seq, recs[1].Seq)
	}
}

// ---------------------------------------------------------------------------
// (5) bounds, over the wire: a full spool is honestly un-acked, not silently dropped.
// ---------------------------------------------------------------------------

func TestHookSocket_PostAgainstAFullSpoolIsNeverAcked(t *testing.T) {
	cfg := hookCfg(t)
	cfg.HookSpoolMaxBytes = 200 // small: a handful of posts fill it
	ch := runShimAsync(cfg)

	var acked, refused int
	for i := 0; i < 200; i++ {
		if postHook(t, cfg.HookSocketPath, []byte(`{"event":"PostToolUse","tool":"Read"}`), time.Second) {
			acked++
		} else {
			refused++
			break
		}
	}
	if refused == 0 {
		t.Fatalf("200 posts under a 200-byte spool cap were all acked — the bound never engaged")
	}
	if acked == 0 {
		t.Fatalf("no post was ever acked before the spool filled — the harness or the bound is miscalibrated")
	}

	ctl := dialShim(t, cfg.SocketPath)
	ctl.startReader()
	ctl.hello(shimwire.Version)
	killShim(ctl)
	waitRun(t, ch, 10*time.Second)

	spool, err := OpenHookSpool(filepath.Join(cfg.SessionDir, HookSpoolFile), 0)
	if err != nil {
		t.Fatalf("open spool: %v", err)
	}
	defer func() { _ = spool.Close() }()
	recs, _, hasGap, err := spool.ReadFrom(0)
	if err != nil || hasGap {
		t.Fatalf("ReadFrom(0): recs=%v hasGap=%v err=%v", recs, hasGap, err)
	}
	if len(recs) != acked {
		t.Fatalf("spool holds %d record(s), want exactly %d (the number the shim itself acked) — an un-acked post must never sneak in, and an acked one must never be lost", len(recs), acked)
	}
}

// ---------------------------------------------------------------------------
// Drain wire protocol: request/response, fold-then-refetch, stateless per connection.
// ---------------------------------------------------------------------------

func drainOnce(t *testing.T, path string, fromSeq, foldSeq uint64) HookDrainResponse {
	t.Helper()
	conn := dialHookSocket(t, path)
	defer func() { _ = conn.Close() }()
	req, err := json.Marshal(HookDrainRequest{FromSeq: fromSeq, FoldSeq: foldSeq})
	if err != nil {
		t.Fatalf("marshal HookDrainRequest: %v", err)
	}
	if _, err := conn.Write(append([]byte{HookDrainTag}, req...)); err != nil {
		t.Fatalf("write drain request: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var resp HookDrainResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil && err != io.EOF {
		t.Fatalf("decode HookDrainResponse: %v", err)
	}
	return resp
}

func TestHookSocket_DrainReturnsEveryPostedRecordInOrder(t *testing.T) {
	cfg := hookCfg(t)
	ch := runShimAsync(cfg)

	for i, ev := range []string{"UserPromptSubmit", "PreToolUse", "PostToolUse", "Stop"} {
		if !postHook(t, cfg.HookSocketPath, []byte(`{"event":"`+ev+`"}`), 3*time.Second) {
			t.Fatalf("post %d (%s) was never acked", i, ev)
		}
	}

	resp := drainOnce(t, cfg.HookSocketPath, 0, 0)
	if resp.Gap {
		t.Fatalf("drain reports a gap at %d over four cleanly-posted records", resp.GapBoundary)
	}
	if len(resp.Records) != 4 {
		t.Fatalf("drain returned %d record(s), want 4", len(resp.Records))
	}
	for i, r := range resp.Records {
		if r.Seq != uint64(i+1) {
			t.Errorf("record %d has seq %d, want %d (drain order must match spool order)", i, r.Seq, i+1)
		}
	}

	// Fold through seq 4 and drain again: nothing left.
	resp2 := drainOnce(t, cfg.HookSocketPath, 4, 4)
	if len(resp2.Records) != 0 {
		t.Fatalf("drain after folding everything returned %d record(s), want 0", len(resp2.Records))
	}

	ctl := dialShim(t, cfg.SocketPath)
	ctl.startReader()
	ctl.hello(shimwire.Version)
	killShim(ctl)
	waitRun(t, ch, 10*time.Second)
}

func TestHookSocket_DrainIsStatelessPerConnection_APollerJustReconnects(t *testing.T) {
	// No live-follow: a record posted AFTER one drain response closed is invisible to that
	// (already-closed) connection and must be picked up by the NEXT drain attempt, at the
	// same fromSeq the caller tracks itself.
	cfg := hookCfg(t)
	ch := runShimAsync(cfg)

	if !postHook(t, cfg.HookSocketPath, []byte(`{"event":"UserPromptSubmit"}`), 3*time.Second) {
		t.Fatalf("post 1 not acked")
	}
	first := drainOnce(t, cfg.HookSocketPath, 0, 0)
	if len(first.Records) != 1 {
		t.Fatalf("first drain returned %d record(s), want 1", len(first.Records))
	}

	if !postHook(t, cfg.HookSocketPath, []byte(`{"event":"Stop"}`), 3*time.Second) {
		t.Fatalf("post 2 not acked")
	}
	second := drainOnce(t, cfg.HookSocketPath, 1, 0)
	if len(second.Records) != 1 || second.Records[0].Seq != 2 {
		t.Fatalf("second drain (fromSeq=1) = %+v, want exactly the seq=2 record posted after the first drain", second.Records)
	}

	ctl := dialShim(t, cfg.SocketPath)
	ctl.startReader()
	ctl.hello(shimwire.Version)
	killShim(ctl)
	waitRun(t, ch, 10*time.Second)
}

// ---------------------------------------------------------------------------
// (7) compat: HookSocketPath=="" disables the listener entirely -- an old-shim launch
// config that never sets it must behave exactly as it does today, with no second socket
// ever created under the session dir.
// ---------------------------------------------------------------------------

func TestHookSocket_EmptyPathDisablesTheListener(t *testing.T) {
	cfg := helperConfig(t, modeIdle, nil, nil) // HookSocketPath left zero-value
	ch := runShimAsync(cfg)

	// The control socket must still work unaffected.
	ctl := dialShim(t, cfg.SocketPath)
	ctl.startReader()
	reply := ctl.hello(shimwire.Version)
	if reply.Type != shimwire.TypeHello {
		t.Fatalf("control socket hello failed with HookSocketPath unset: %+v", reply)
	}

	entries, err := os.ReadDir(cfg.SessionDir)
	if err != nil {
		t.Fatalf("read session dir: %v", err)
	}
	for _, e := range entries {
		if e.Name() == HookSpoolFile {
			t.Errorf("a hook spool file was created even though HookSocketPath was never set")
		}
	}

	killShim(ctl)
	waitRun(t, ch, 10*time.Second)
}

// ---------------------------------------------------------------------------
// R6 REVIEW FIX-PACK (security, HIGH): a configured HookToken gates DRAIN -- the
// verb is both destructive (FoldSeq compacts on the caller's say-so) and reveals the
// session's captured content, and file permissions (0600 + a 0700 session dir) are
// otherwise the ONLY control on it. POST needs no token: a spooled record's own
// embedded auth is checked downstream at daemon apply time, not at accept time.
// ---------------------------------------------------------------------------

func TestHookSocket_DrainWithoutTheConfiguredTokenIsRefused(t *testing.T) {
	cfg := hookCfg(t)
	cfg.HookToken = "s3cr3t"
	ch := runShimAsync(cfg)

	if !postHook(t, cfg.HookSocketPath, []byte(`{"event":"UserPromptSubmit"}`), 3*time.Second) {
		t.Fatalf("post not acked")
	}

	// No token at all.
	resp := drainOnceWithToken(t, cfg.HookSocketPath, 0, 0, "")
	if len(resp.Records) != 0 || resp.Gap {
		t.Fatalf("drain with no token returned %+v, want an empty/refused response", resp)
	}
	// The wrong token.
	resp = drainOnceWithToken(t, cfg.HookSocketPath, 0, 0, "wrong")
	if len(resp.Records) != 0 || resp.Gap {
		t.Fatalf("drain with the wrong token returned %+v, want an empty/refused response", resp)
	}
	// The right token succeeds.
	resp = drainOnceWithToken(t, cfg.HookSocketPath, 0, 0, "s3cr3t")
	if len(resp.Records) != 1 {
		t.Fatalf("drain with the correct token returned %d record(s), want 1", len(resp.Records))
	}

	ctl := dialShim(t, cfg.SocketPath)
	ctl.startReader()
	ctl.hello(shimwire.Version)
	killShim(ctl)
	waitRun(t, ch, 10*time.Second)
}

func TestHookSocket_EmptyTokenMeansNoAuthCheck_CompatDefault(t *testing.T) {
	// cfg.HookToken left unset: every existing caller (every OTHER test in this
	// file) predates this field, and must keep working unchanged.
	cfg := hookCfg(t)
	ch := runShimAsync(cfg)

	if !postHook(t, cfg.HookSocketPath, []byte(`{"event":"Stop"}`), 3*time.Second) {
		t.Fatalf("post not acked")
	}
	resp := drainOnceWithToken(t, cfg.HookSocketPath, 0, 0, "")
	if len(resp.Records) != 1 {
		t.Fatalf("drain with no configured token returned %d record(s), want 1 (no auth check should run at all)", len(resp.Records))
	}

	ctl := dialShim(t, cfg.SocketPath)
	ctl.startReader()
	ctl.hello(shimwire.Version)
	killShim(ctl)
	waitRun(t, ch, 10*time.Second)
}

// R6 REVIEW FIX-PACK (security, HIGH): a POST body is bounded BEFORE it is decoded,
// independent of the spool's own size bound -- Append's refusal only runs once a
// value is already fully buffered, which on a local UDS with a multi-second deadline
// is gigabytes.
func TestHookSocket_OversizedPostIsRefusedNotBuffered(t *testing.T) {
	cfg := hookCfg(t)
	ch := runShimAsync(cfg)

	huge := []byte(`{"padding":"` + strings.Repeat("x", hookPostMaxBytes+1024) + `"}`)
	if postHook(t, cfg.HookSocketPath, huge, 3*time.Second) {
		t.Fatalf("a POST over the size bound was acked, want refused")
	}
	// A normal-sized post afterward must still work: the oversized post must not have
	// wedged the listener or its spool.
	if !postHook(t, cfg.HookSocketPath, []byte(`{"event":"Stop"}`), 3*time.Second) {
		t.Fatalf("post after an oversized refusal was never acked")
	}

	ctl := dialShim(t, cfg.SocketPath)
	ctl.startReader()
	ctl.hello(shimwire.Version)
	killShim(ctl)
	waitRun(t, ch, 10*time.Second)
}

// R6 REVIEW FIX-PACK (security, MEDIUM): a DRAIN request is bounded BEFORE it is
// decoded, same as POST -- previously only the connection's 5s deadline bounded it,
// permitting hundreds of megabytes of allocation inside the shim, the process that
// owns the PTY (ADR-013's sacred plane).
func TestHookSocket_OversizedDrainIsRefusedNotBuffered(t *testing.T) {
	cfg := hookCfg(t)
	ch := runShimAsync(cfg)

	if !postHook(t, cfg.HookSocketPath, []byte(`{"event":"Stop"}`), 3*time.Second) {
		t.Fatalf("post not acked")
	}

	huge, err := json.Marshal(HookDrainRequest{Token: strings.Repeat("x", hookDrainMaxBytes+1024)})
	if err != nil {
		t.Fatalf("marshal oversized HookDrainRequest: %v", err)
	}
	conn := dialHookSocket(t, cfg.HookSocketPath)
	if _, err := conn.Write(append([]byte{HookDrainTag}, huge...)); err != nil {
		t.Fatalf("write oversized drain request: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var resp HookDrainResponse
	if derr := json.NewDecoder(conn).Decode(&resp); derr != nil && derr != io.EOF {
		t.Fatalf("decode after an oversized drain request: %v", derr)
	}
	_ = conn.Close()
	if len(resp.Records) != 0 || resp.Gap {
		t.Fatalf("an oversized DRAIN request was answered with %+v, want refused (empty)", resp)
	}

	// A normal-sized drain afterward must still work: the oversized request must not
	// have wedged the listener.
	resp2 := drainOnce(t, cfg.HookSocketPath, 0, 0)
	if len(resp2.Records) != 1 {
		t.Fatalf("drain after an oversized-request refusal returned %d record(s), want 1", len(resp2.Records))
	}

	ctl := dialShim(t, cfg.SocketPath)
	ctl.startReader()
	ctl.hello(shimwire.Version)
	killShim(ctl)
	waitRun(t, ch, 10*time.Second)
}

func drainOnceWithToken(t *testing.T, path string, fromSeq, foldSeq uint64, token string) HookDrainResponse {
	t.Helper()
	conn := dialHookSocket(t, path)
	defer func() { _ = conn.Close() }()
	req, err := json.Marshal(HookDrainRequest{FromSeq: fromSeq, FoldSeq: foldSeq, Token: token})
	if err != nil {
		t.Fatalf("marshal HookDrainRequest: %v", err)
	}
	if _, err := conn.Write(append([]byte{HookDrainTag}, req...)); err != nil {
		t.Fatalf("write drain request: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var resp HookDrainResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil && err != io.EOF {
		t.Fatalf("decode HookDrainResponse: %v", err)
	}
	return resp
}
