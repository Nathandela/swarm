package skeleton

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter/claude"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/status"
)

type a1SpaceReader struct{}

func (a1SpaceReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = ' '
	}
	return len(p), nil
}

func a1DrainReplySocket(t *testing.T, dir string, reply io.Reader) string {
	t.Helper()
	path := filepath.Join(dir, "h")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(hookDrainTimeout))
		var tag [1]byte
		if _, err := io.ReadFull(conn, tag[:]); err != nil || tag[0] != shim.HookDrainTag {
			return
		}
		var request shim.HookDrainRequest
		if json.NewDecoder(conn).Decode(&request) != nil {
			return
		}
		_, _ = io.Copy(conn, reply) // A bounded decoder may close before the whole reply.
	}()
	t.Cleanup(func() {
		_ = l.Close()
		select {
		case <-done:
		case <-time.After(hookDrainTimeout):
			t.Error("controlled DRAIN handler did not join")
		}
	})
	return path
}

func TestHookDrainerReplyBoundsPreserveGapAuthority(t *testing.T) {
	for _, mode := range []string{"truncated", "oversized", "complete"} {
		t.Run(mode, func(t *testing.T) {
			dir, err := os.MkdirTemp("", "a1d")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			const id, token = "a1-drain", "a1-token"
			store, err := persist.NewStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Save(persist.Meta{ID: id, AgentType: "claude", Status: status.Status{Process: status.ProcessExited}}); err != nil {
				t.Fatal(err)
			}
			core, err := daemon.Open(daemon.Config{StateDir: dir, SocketPath: filepath.Join(dir, "d"), LockPath: filepath.Join(dir, "l"), LogPath: filepath.Join(dir, "log"), MaxSessions: 1})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = core.Close() })
			var stored atomic.Int64
			e := engine.New(engine.Config{StoreExecution: func(persist.ExecutionRecord) error { stored.Add(1); return nil }})
			if err := e.RegisterExecutionSession(id, token, os.Getpid(), claude.New().SignalSources(), status.Status{Process: status.ProcessRunning, Turn: status.TurnUnknown, Interaction: status.InteractionNone}, engine.ExecutionIdentity{Incarnation: "a1", ObservedFromStart: true}, nil); err != nil {
				t.Fatal(err)
			}
			d := &Daemon{core: core, stateDir: dir, eng: e}
			d.registerSessionCapabilities(id, protocol.SessionCapabilities{Provider: "claude", SessionInstance: "a1-instance", StructuredChat: true})
			body, err := json.Marshal(engine.Callback{SessionID: id, Token: token, Sequence: 8, Event: "SubagentStart", Payload: map[string]string{"agent_id": "a1-child"}})
			if err != nil {
				t.Fatal(err)
			}
			response := shim.HookDrainResponse{Records: []shim.HookRecord{{Seq: 8, Body: body}}, Gap: true, GapBoundary: 9, SpoolIncarnation: "a1-spool", ProducerCertificate: "a1-producer"}
			raw, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			var reply io.Reader = bytes.NewReader(raw)
			switch mode {
			case "truncated":
				reply = bytes.NewReader(raw[:len(raw)-1])
			case "oversized":
				// Valid JSON after the cap distinguishes an omitted bounded decoder
				// from an ordinary syntax failure. The stream needs no large test buffer.
				reply = io.MultiReader(io.LimitReader(a1SpaceReader{}, int64(shim.HookDrainResponseMaxBytes)+1), bytes.NewReader(raw))
			}
			cursor := filepath.Join(dir, "fold")
			if err := persistHookCursor(cursor, 7); err != nil {
				t.Fatal(err)
			}
			certificate, err := json.Marshal(hookCursorCertificate{Incarnation: "a1-spool", Producer: "a1-producer", Sequence: 7})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(cursor+".cert", certificate, 0600); err != nil {
				t.Fatal(err)
			}
			before, err := core.JournalReadFrom(0)
			if err != nil {
				t.Fatal(err)
			}
			hd := NewHookDrainer(d, id, a1DrainReplySocket(t, dir, reply), cursor)
			applied, skipped, drainErr := hd.DrainOnce()
			if mode == "complete" {
				if !errors.Is(drainErr, ErrHookDrainGap) || applied != 1 || skipped != 0 || stored.Load() == 0 {
					t.Fatalf("complete pre-gap response changed: applied=%d skipped=%d stored=%d err=%v", applied, skipped, stored.Load(), drainErr)
				}
				if hd.cursor() != 0 || readHookGapCheckpoint(hookGapCursorPath(cursor)).Boundary != 9 {
					t.Fatal("complete response did not preserve existing gap checkpoint/reset")
				}
				return
			}
			if drainErr == nil || errors.Is(drainErr, ErrHookDrainGap) || applied != 0 || skipped != 0 || stored.Load() != 0 {
				t.Fatalf("invalid reply gained apply/gap authority: applied=%d skipped=%d stored=%d err=%v", applied, skipped, stored.Load(), drainErr)
			}
			gotCertificate, err := os.ReadFile(cursor + ".cert")
			if err != nil || !bytes.Equal(gotCertificate, certificate) || hd.cursor() != 7 || hd.recoveringGap() {
				t.Fatalf("invalid reply advanced/reset cursor/certificate: cursor=%d err=%v", hd.cursor(), err)
			}
			if _, err := os.Stat(hookGapCursorPath(cursor)); !os.IsNotExist(err) {
				t.Fatalf("invalid reply wrote a gap checkpoint: %v", err)
			}
			after, err := core.JournalReadFrom(0)
			if err != nil || after.Cursor != before.Cursor || len(after.Events) != len(before.Events) {
				t.Fatalf("invalid reply changed journal authority: %v", err)
			}
			caps, ok := d.sessionCapabilities(id)
			if !ok || !caps.StructuredChat {
				t.Fatal("invalid transport reply published an authoritative gap transition")
			}
		})
	}
}
