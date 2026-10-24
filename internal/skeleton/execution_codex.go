package skeleton

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/codex"
	"github.com/Nathandela/swarm/internal/appserver"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/procstart"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/status"
)

const qualifiedCodexExecutionVersion = "0.163.0"

type codexExecutionFeed struct {
	mu                                      sync.Mutex
	captured, prepared, born, begun, unsafe bool
	instance                                string
	endpoint                                *appserver.UnixEndpoint
	native                                  shim.BackendInfo
	shimPID                                 int
	shimStart                               int64
	cli                                     persist.CLIIdentity
	preparedAt                              time.Time
	thread                                  string
	sequence                                uint64
}

func codexProcessMatches(pid int, start int64) bool {
	if pid <= 0 || start <= 0 || syscall.Kill(pid, 0) != nil {
		return false
	}
	actual, err := procstart.StartTime(pid)
	return err == nil && actual == start
}

func (d *Daemon) readCodexExecutionNative(local, instance, socket string) (persist.Meta, shim.BackendInfo, error) {
	var info shim.BackendInfo
	if d.core == nil || !persist.ValidID(local) {
		return persist.Meta{}, info, fmt.Errorf("native execution owner unavailable")
	}
	meta, ok := d.core.Get(local)
	current, currentOK := d.sessionInstance(local)
	if !ok || !currentOK || current != instance || meta.AgentType != "codex" || meta.Status.Process != status.ProcessRunning || !validCLIIdentity(meta.CLIIdentity) || meta.CLIIdentity.Version != qualifiedCodexExecutionVersion || !codexProcessMatches(meta.ShimPID, meta.ShimStartTime) {
		return meta, info, fmt.Errorf("native execution identity unavailable")
	}
	root, err := openRecoveryRoot(filepath.Join(d.stateDir, local))
	if err != nil {
		return meta, info, err
	}
	defer func() { _ = root.Close() }()
	before, err := root.Lstat(shim.BackendFile)
	if err != nil {
		return meta, info, err
	}
	file, err := root.OpenFile(shim.BackendFile, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return meta, info, err
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	stat, valid := infoSysNative(opened)
	if err != nil || !valid || !opened.Mode().IsRegular() || opened.Mode().Perm()&0077 != 0 || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 || opened.Size() > 64<<10 || !os.SameFile(before, opened) {
		return meta, info, fmt.Errorf("unsafe native execution metadata")
	}
	raw, err := io.ReadAll(io.LimitReader(file, 64<<10+1))
	if err != nil || len(raw) > 64<<10 {
		return meta, info, fmt.Errorf("native execution metadata bound")
	}
	currentInfo, err := root.Lstat(shim.BackendFile)
	if err != nil || !os.SameFile(opened, currentInfo) {
		return meta, info, fmt.Errorf("native execution metadata changed")
	}
	fields, valid := decodeStrictObject(raw)
	if !valid {
		return meta, info, fmt.Errorf("ambiguous native execution metadata")
	}
	for field := range fields {
		switch field {
		case "pid", "pgid", "started_at_ms", "socket_path":
		default:
			return meta, info, fmt.Errorf("unknown native execution metadata")
		}
	}
	if json.Unmarshal(raw, &info) != nil || info.PID == meta.ShimPID || info.PGID != info.PID || info.SocketPath != socket || !codexProcessMatches(info.PID, info.StartedAtMS) {
		return meta, info, fmt.Errorf("native execution process unavailable")
	}
	if !persist.MatchCLIProcessImage(info.PID, info.StartedAtMS, meta.CLIIdentity.Path, meta.CLIIdentity.Fingerprint) {
		return meta, info, fmt.Errorf("native execution image unavailable")
	}
	return meta, info, nil
}

// Capture process identity before opening the socket. Missing backend.json can be
// ordinary shim startup; an invalid or changing proof never blesses a later dial.
func (d *Daemon) prepareCodexExecutionNative(ctx context.Context, local, instance string, ch daemon.BackendChannel, feed *backendFeed) {
	if d.core == nil {
		return
	}
	meta, ok := d.core.Get(local)
	if !ok || meta.AgentType != "codex" {
		return
	}
	for {
		meta, native, err := d.readCodexExecutionNative(local, instance, ch.SocketPath)
		if errors.Is(err, os.ErrNotExist) && validCLIIdentity(meta.CLIIdentity) && meta.CLIIdentity.Version == qualifiedCodexExecutionVersion {
			select {
			case <-ctx.Done():
				return
			case <-time.After(backendReadyInterval):
				continue
			}
		}
		var endpoint *appserver.UnixEndpoint
		if err == nil {
			endpoint, err = appserver.CaptureUnixEndpoint(ch.SocketPath)
		}
		feed.codexExecution.mu.Lock()
		if err != nil {
			feed.codexExecution.unsafe = true
		} else {
			feed.codexExecution.endpoint = endpoint
			feed.codexExecution.captured = true
			feed.codexExecution.instance = instance
			feed.codexExecution.native = native
			feed.codexExecution.shimPID = meta.ShimPID
			feed.codexExecution.shimStart = meta.ShimStartTime
			feed.codexExecution.cli = *meta.CLIIdentity
		}
		feed.codexExecution.mu.Unlock()
		return
	}
}

func qualifiedEmptyCodexThreads(raw json.RawMessage) bool {
	if len(raw) > 64<<10 {
		return false
	}
	object, ok := decodeStrictObject(raw)
	if !ok {
		return false
	}
	for key, raw := range object {
		switch key {
		case "data":
		case "nextCursor":
			var cursor *string
			if json.Unmarshal(raw, &cursor) != nil || cursor != nil {
				return false
			}
		default:
			return false
		}
	}
	data, present := object["data"]
	if !present {
		return false
	}
	var threads []json.RawMessage
	return len(data) > 0 && data[0] == '[' && json.Unmarshal(data, &threads) == nil && len(threads) == 0
}

func qualifiedCodexExecutionHandshake(raw json.RawMessage, userAgent string) bool {
	if len(raw) > 64<<10 {
		return false
	}
	fields, ok := decodeStrictObject(raw)
	if !ok {
		return false
	}
	for key := range fields {
		if key != "userAgent" && strings.EqualFold(key, "userAgent") {
			return false
		}
	}
	var native string
	if json.Unmarshal(fields["userAgent"], &native) != nil || native != userAgent {
		return false
	}
	version, ok := codex.New().ParseVersion(native)
	return ok && version == qualifiedCodexExecutionVersion
}

func (d *Daemon) finishCodexExecutionPreparation(ctx context.Context, local, instance string, ch daemon.BackendChannel, conn backendConn, feed *backendFeed, initialized json.RawMessage, fresh bool) {
	state := &feed.codexExecution
	state.mu.Lock()
	captured := state.captured
	native, shimPID, shimStart, cli := state.native, state.shimPID, state.shimStart, state.cli
	endpoint := state.endpoint
	state.mu.Unlock()
	if !captured {
		return
	}
	meta, current, err := d.readCodexExecutionNative(local, instance, ch.SocketPath)
	valid := codexExecutionPeerMatches(conn, native.PID, endpoint) && err == nil && current == native && meta.ShimPID == shimPID && meta.ShimStartTime == shimStart && *meta.CLIIdentity == cli && qualifiedCodexExecutionHandshake(initialized, feed.userAgent)
	born := false
	if valid && fresh && meta.LoginRuntime != nil && meta.LoginRuntime.ExternalAuth {
		// Only this managed shim mode refuses to start a standalone TUI on timeout.
		// Old conversation history does not imply this new process missed live work.
		root, openErr := openRecoveryRoot(filepath.Join(d.stateDir, local))
		if openErr == nil {
			_, nativeErr := root.Lstat(shim.NativeProcessFile)
			_ = root.Close()
			if errors.Is(nativeErr, os.ErrNotExist) {
				var loaded json.RawMessage
				if conn.Call(ctx, "thread/loaded/list", map[string]any{}, &loaded) == nil {
					born = qualifiedEmptyCodexThreads(loaded)
				}
			}
		}
	}
	// Recheck after the RPC and before go-ahead, not merely before initialize.
	finalMeta, current, err := d.readCodexExecutionNative(local, instance, ch.SocketPath)
	state.mu.Lock()
	defer state.mu.Unlock()
	state.prepared = valid && codexExecutionPeerMatches(conn, native.PID, endpoint) && err == nil && current == native && finalMeta.ShimPID == shimPID && finalMeta.ShimStartTime == shimStart && *finalMeta.CLIIdentity == cli
	if !state.prepared {
		state.unsafe = true
	}
	state.born = born && !state.unsafe
	state.preparedAt = time.Now()
}

// This runs on the existing physical-feed FIFO after generic thread adoption.
func (d *Daemon) captureCodexExecutionFrame(local, instance string, feed *backendFeed, method string, raw []byte, at time.Time) {
	if d.eng == nil || d.core == nil {
		return
	}
	meta, ok := d.core.Get(local)
	if !ok || meta.AgentType != "codex" {
		return
	}
	state := &feed.codexExecution
	state.mu.Lock()
	defer state.mu.Unlock()
	thread := meta.ConversationID
	event, relevant := parseCodexExecutionFrame(method, raw, thread)
	controlNamespace := method == "error" || method == "serverRequest/resolved" || strings.HasPrefix(method, "turn/") || strings.HasPrefix(method, "item/") || strings.HasPrefix(method, "thread/")
	if thread == "" && controlNamespace {
		relevant = true
	}
	if (!state.prepared || at.Before(state.preparedAt)) && controlNamespace {
		state.unsafe = true
		return
	}
	if !state.prepared {
		return
	}
	if !adapter.IsCanonicalConversationID(thread) {
		if relevant {
			state.unsafe = true
		}
		return
	}
	snapshot, err := d.eng.ExecutionSnapshot(local)
	if err != nil {
		state.unsafe = true
		return
	}
	if snapshot.Incomplete == "persistence_failure" {
		state.unsafe = true
	}
	if !state.begun {
		identity := engine.CodexFeedIdentity{NativeIncarnation: fmt.Sprintf("%d:%d", state.native.PID, state.native.StartedAtMS), FeedEpoch: feed.epoch, ThreadID: thread, OwnerRevision: snapshot.OwnerRevision, ObservedFromStart: state.born && !state.unsafe}
		if err = d.eng.BeginCodexExecutionFeed(local, identity); err != nil {
			state.unsafe = true
			return
		}
		state.begun = true
		state.thread = thread
	}
	if !relevant {
		return
	}
	state.sequence++
	event.NativeIncarnation = fmt.Sprintf("%d:%d", state.native.PID, state.native.StartedAtMS)
	event.FeedEpoch = feed.epoch
	event.ThreadID = state.thread
	event.Sequence = state.sequence
	if err := d.eng.ApplyCodexExecutionEvent(local, event); err != nil {
		state.unsafe = true
	}
}

// This is still evidence inspection. A future dispatcher must revalidate at the
// existing owner/lifecycle/native write fence, not treat this return as a lease.
func (d *Daemon) codexQuotaCandidate(local string) (engine.CodexQuotaObligation, bool) {
	if d.eng == nil || d.core == nil {
		return engine.CodexQuotaObligation{}, false
	}
	backend, ok := d.sessionBackendFor(local)
	if !ok || backend.feed == nil || backend.feed.ingress == nil || !backend.feed.ingressAutomationSafe() {
		return engine.CodexQuotaObligation{}, false
	}
	feed := backend.feed
	q := feed.ingress
	received := q.received.Load()
	state := &feed.codexExecution
	state.mu.Lock()
	if !state.prepared || !state.begun || state.unsafe || state.instance != backend.sessionInstance || state.thread != backend.threadID {
		state.mu.Unlock()
		return engine.CodexQuotaObligation{}, false
	}
	native, cli, shimPID, shimStart := state.native, state.cli, state.shimPID, state.shimStart
	endpoint := state.endpoint
	state.mu.Unlock()
	meta, current, err := d.readCodexExecutionNative(local, backend.sessionInstance, native.SocketPath)
	if !codexExecutionPeerMatches(backend.conn, native.PID, endpoint) || err != nil || current != native || meta.ShimPID != shimPID || meta.ShimStartTime != shimStart || *meta.CLIIdentity != cli {
		return engine.CodexQuotaObligation{}, false
	}
	candidate, ok := d.eng.CodexQuotaCandidate(local)
	if !ok || candidate.NativeIncarnation != fmt.Sprintf("%d:%d", native.PID, native.StartedAtMS) || candidate.FeedEpoch != feed.epoch || candidate.ThreadID != backend.threadID {
		return engine.CodexQuotaObligation{}, false
	}
	_, finalNative, finalErr := d.readCodexExecutionNative(local, backend.sessionInstance, native.SocketPath)
	currentInstance, instanceOK := d.sessionInstance(local)
	latest, live := d.sessionBackendFor(local)
	if !codexExecutionPeerMatches(backend.conn, native.PID, endpoint) || finalErr != nil || finalNative != native || !instanceOK || currentInstance != backend.sessionInstance || !live || latest.feed != feed || latest.sessionInstance != backend.sessionInstance || received != q.received.Load() || q.completed.Load() != received || !feed.ingressAutomationSafe() {
		return engine.CodexQuotaObligation{}, false
	}
	return candidate, true
}

func codexExecutionPeerMatches(conn backendConn, pid int, endpoint *appserver.UnixEndpoint) bool {
	peer, ok := conn.(interface{ PeerProcess() (int, uint32, bool) })
	if !ok || endpoint == nil || !endpoint.Unchanged() {
		return false
	}
	actual, uid, available := peer.PeerProcess()
	return available && actual == pid && uid == uint32(os.Getuid())
}
