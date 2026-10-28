package daemon

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/idempotency"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/wire"
)

type ClaudeFixtureIntent struct {
	Version               int                            `json:"v"`
	Kind                  string                         `json:"kind"`
	Identity              shimwire.ClaudePrimaryIdentity `json:"identity"`
	SessionInstance       string                         `json:"session_instance"`
	NativeUserUUID        string                         `json:"native_user_uuid"`
	TextSHA256            string                         `json:"text_sha256"`
	TextBytes             int                            `json:"text_bytes"`
	ExpectedOwnerRevision uint64                         `json:"expected_owner_revision"`
	OwnerRevision         uint64                         `json:"owner_revision"`
}

func DecodeClaudeFixtureIntent(raw []byte) (ClaudeFixtureIntent, error) {
	return decodeClaudeMessageIntent(raw, false)
}

func DecodeClaudeNativeIntent(raw []byte) (ClaudeFixtureIntent, error) {
	return decodeClaudeMessageIntent(raw, true)
}

func decodeClaudeMessageIntent(raw []byte, native bool) (ClaudeFixtureIntent, error) {
	var i ClaudeFixtureIntent
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&i) != nil {
		return i, errClaudeFixtureDispatch
	}
	h, err := hex.DecodeString(i.TextSHA256)
	canonical, _ := json.Marshal(i)
	id := i.Identity
	if !bytes.Equal(canonical, raw) || i.Version != 1 || (!native && i.Kind != "claude_fixture_message" || native && i.Kind != "claude_native_message") || !persist.ValidID(id.SessionID) || id.RequiredRuntime != persist.RequiredRuntimeClaudeChat || !adapter.IsCanonicalConversationID(id.NativeSessionID) || id.ShimPID <= 0 || id.ShimStartTime <= 0 || id.PrimaryPID <= 0 || id.PrimaryStartTime <= 0 || i.SessionInstance == "" || len(i.SessionInstance) > 256 || !adapter.IsCanonicalConversationID(i.NativeUserUUID) || err != nil || len(h) != 32 || hex.EncodeToString(h) != i.TextSHA256 || i.TextBytes <= 0 || i.TextBytes > shimwire.MaxClaudeFixtureTextBytes || i.ExpectedOwnerRevision == ^uint64(0) || (!native && i.OwnerRevision != i.ExpectedOwnerRevision+1 || native && i.OwnerRevision <= i.ExpectedOwnerRevision) {
		return i, errClaudeFixtureDispatch
	}
	return i, nil
}

var errClaudeFixtureDispatch = errors.New("daemon: fixture primary input custody unavailable")

func claudeFixtureMetaMatches(m persist.Meta, id shimwire.ClaudePrimaryIdentity) bool {
	return m.SchemaVersion == 6 && m.AgentType == "fake" && m.RequiredRuntime == persist.RequiredRuntimeClaudeChat && m.ID == id.SessionID && m.ConversationID == id.NativeSessionID && m.ShimPID == id.ShimPID && m.ShimStartTime == id.ShimStartTime && m.Status.Process == status.ProcessRunning && m.LoginRuntime == nil && m.CLIIdentity == nil && id.PrimaryPID > 0 && id.PrimaryStartTime > 0
}

func claudeNativeMetaMatches(m persist.Meta, id shimwire.ClaudePrimaryIdentity) bool {
	return m.InputEmbargo == "" && m.SchemaVersion == 6 && m.AgentType == "claude" && m.RequiredRuntime == persist.RequiredRuntimeClaudeChat && m.ID == id.SessionID && m.ConversationID == id.NativeSessionID && m.ShimPID == id.ShimPID && m.ShimStartTime == id.ShimStartTime && m.Status.Process == status.ProcessRunning && m.LoginRuntime != nil && m.LoginRuntime.Provider == "claude" && m.CLIIdentity != nil && m.CLIIdentity.Version == "2.1.310" && m.ClaudeRetirement == nil && id.PrimaryPID > 0 && id.PrimaryStartTime > 0
}
func dialClaudeFixture(sock string) (net.Conn, shimwire.ClaudePrimarySnapshot, error) {
	deadline := time.Now().Add(3 * time.Second)
	c, err := net.DialTimeout("unix", sock, time.Until(deadline))
	if err != nil {
		return nil, shimwire.ClaudePrimarySnapshot{}, err
	}
	_ = c.SetDeadline(deadline)
	b, _ := shimwire.Encode(shimwire.Control{Type: shimwire.TypeHello, WireVersion: shimwire.Version})
	if err = wire.WriteFrame(c, wire.TControl, b); err == nil {
		var typ wire.Type
		typ, b, err = wire.ReadFrame(c)
		if err == nil && typ == wire.TControl {
			reply, e := shimwire.Decode(b)
			if e == nil && reply.Type == shimwire.TypeHello && reply.WireVersion == shimwire.Version && reply.ClaudePrimaryV1 && reply.ClaudePrimary != nil {
				return c, *reply.ClaudePrimary, nil
			}
		}
	}
	_ = c.Close()
	return nil, shimwire.ClaudePrimarySnapshot{}, errClaudeFixtureDispatch
}

func (d *Daemon) ClaudeFixtureSnapshot(local string) (shimwire.ClaudePrimarySnapshot, error) {
	m, ok := d.Get(local)
	if !ok || (m.AgentType != "fake" && m.AgentType != "claude") || m.RequiredRuntime != persist.RequiredRuntimeClaudeChat || !d.shimIdentityMatches(m) {
		return shimwire.ClaudePrimarySnapshot{}, errClaudeFixtureDispatch
	}
	c, snapshot, err := dialClaudeFixture(shimSocketPath(d.cfg.StateDir, local))
	if c != nil {
		_ = c.Close()
	}
	if err != nil || !claudeFixtureMetaMatches(m, snapshot.Identity) && !claudeNativeMetaMatches(m, snapshot.Identity) {
		return shimwire.ClaudePrimarySnapshot{}, errClaudeFixtureDispatch
	}
	return snapshot, nil
}

// Preparation may wait for persistence, before the lifecycle fence is entered.
type ClaudeFixtureDispatch struct {
	d            *Daemon
	local        string
	native       bool
	request      shimwire.ClaudeMessageRequest
	unlock       func()
	mu           sync.Mutex
	closed, used bool
}

func (d *Daemon) PrepareClaudeFixtureDispatch(local string, request shimwire.ClaudeMessageRequest, bound []byte) (*ClaudeFixtureDispatch, error) {
	return d.prepareClaudeMessageDispatch(local, request, bound, false)
}
func (d *Daemon) PrepareClaudeNativeDispatch(local string, request shimwire.ClaudeMessageRequest, bound []byte) (*ClaudeFixtureDispatch, error) {
	return d.prepareClaudeMessageDispatch(local, request, bound, true)
}
func (d *Daemon) prepareClaudeMessageDispatch(local string, request shimwire.ClaudeMessageRequest, bound []byte, native bool) (*ClaudeFixtureDispatch, error) {
	unlock, err := d.lockComposerCustody()
	if err != nil {
		return nil, err
	}
	fail := func() (*ClaudeFixtureDispatch, error) { unlock(); return nil, errClaudeFixtureDispatch }
	records, err := d.idem.NativeInputRecords(local, 32)
	if err != nil {
		return fail()
	}
	matched := false
	for _, rec := range records {
		// One fixture turn has no authority to supersede a different operation.
		if rec.OperationID != request.OperationID {
			if native {
				other, e := DecodeClaudeNativeIntent(rec.Request)
				if e != nil || other.OwnerRevision >= request.OwnerRevision {
					return fail()
				}
				continue
			}
			return fail()
		}
		intent, e := decodeClaudeMessageIntent(rec.Request, native)
		textHash := sha256.Sum256([]byte(request.Text))
		matched = e == nil && request.Version == 1 && utf8.ValidString(request.Text) && len(request.Text) == intent.TextBytes && hex.EncodeToString(textHash[:]) == intent.TextSHA256 && rec.Phase == idempotency.PhaseExecuting && bytes.Equal(rec.Request, bound) && intent.Identity == request.Identity && intent.NativeUserUUID == request.NativeUserUUID && intent.OwnerRevision == request.OwnerRevision && intent.ExpectedOwnerRevision == request.ExpectedOwnerRevision && rec.SessionInstance == intent.SessionInstance
	}
	if !matched {
		return fail()
	}

	return &ClaudeFixtureDispatch{native: native, d: d, local: local, request: request, unlock: unlock}, nil
}

func (g *ClaudeFixtureDispatch) Close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.closed {
		g.closed = true
		g.unlock()
	}
}

func (g *ClaudeFixtureDispatch) Write() (shimwire.ClaudeMessageResult, error) {
	r := g.request
	result := shimwire.ClaudeMessageResult{Version: 1, OperationID: r.OperationID, NativeUserUUID: r.NativeUserUUID, Identity: r.Identity, OwnerRevision: r.OwnerRevision, Status: "refused"}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed || g.used {
		return result, errClaudeFixtureDispatch
	}
	g.used = true
	if !g.d.mu.TryLock() {
		return result, errClaudeFixtureDispatch
	}
	s, ok := g.d.sessions[g.local]
	valid := !g.d.closed && ok && ((!g.native && claudeFixtureMetaMatches(s.meta, r.Identity)) || (g.native && claudeNativeMetaMatches(s.meta, r.Identity)))
	var m persist.Meta
	if valid {
		m = s.meta
	}
	g.d.mu.Unlock()
	if !valid || !g.d.shimIdentityMatches(m) {
		return result, errClaudeFixtureDispatch
	}
	c, snapshot, err := dialClaudeFixture(shimSocketPath(g.d.cfg.StateDir, g.local))
	if err != nil {
		return result, err
	}
	defer func() { _ = c.Close() }()
	if snapshot.Identity != r.Identity || !snapshot.Healthy || snapshot.PendingPartial || snapshot.StdoutEOF || snapshot.StderrEOF || snapshot.ReaderRevision != r.ReaderRevision || snapshot.ObservedSequence != r.ThroughStreamSequence || snapshot.DurableSequence != r.ThroughStreamSequence || snapshot.OwnerRevision != r.ExpectedOwnerRevision {
		return result, errClaudeFixtureDispatch
	}
	b, err := shimwire.Encode(shimwire.Control{Type: shimwire.TypeClaudeMessage, ClaudeMessage: &r})
	if err != nil || len(b)+1 > wire.MaxFrame {
		return result, errClaudeFixtureDispatch
	}
	result.Status = "uncertain"
	if err = wire.WriteFrame(c, wire.TControl, b); err != nil {
		return result, err
	}
	typ, b, err := wire.ReadFrame(c)
	if err != nil {
		return result, err
	}
	reply, err := shimwire.Decode(b)
	if err != nil || typ != wire.TControl || reply.Type != shimwire.TypeClaudeMessageResult || reply.ClaudeMessageResult == nil {
		return result, errClaudeFixtureDispatch
	}
	next := *reply.ClaudeMessageResult
	if next.Version != 1 || next.OperationID != r.OperationID || next.NativeUserUUID != r.NativeUserUUID || next.Identity != r.Identity || next.OwnerRevision != r.OwnerRevision || (next.Status != "written" && next.Status != "refused" && next.Status != "uncertain") {
		return result, errClaudeFixtureDispatch
	}
	return next, nil
}
