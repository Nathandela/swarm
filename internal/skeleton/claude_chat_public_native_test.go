package skeleton

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/idempotency"
	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/loginworker"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/shim"
)

// Separate opt-in qualification: no fake image/birth bypass. The owner supplies
// an isolated preprovisioned synthetic profile, trusted CWD and mock transport.
// A normal test run skips this gate; a skip leaves public native launch UNQUALIFIED.
func TestClaudeChatActualNativePublicLaunchWiresDeterministicInitialOnce(t *testing.T) {
	if os.Getenv("SWARM_CLAUDE_CHAT_NATIVE_SMOKE") != "1" {
		t.Skip("separately authorized fixed-image native public caller qualification")
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Fatal("native chat qualification requires Linux amd64")
	}
	image, err := filepath.EvalSymlinks(os.Getenv("SWARM_CLAUDE_AUTH_NATIVE_BINARY"))
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := persist.CLIFingerprint(image)
	if err != nil {
		t.Fatal(err)
	}
	held, err := loginworker.OpenClaudeNativeImage(image, "2.1.310", fingerprint)
	if err != nil {
		t.Fatal("existing fixed native image gate refused", err)
	}
	_ = held.Close()
	root := os.Getenv("SWARM_CLAUDE_CHAT_NATIVE_STATE")
	cwd := os.Getenv("SWARM_CLAUDE_CHAT_NATIVE_CWD")
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || !filepath.IsAbs(cwd) || filepath.Clean(cwd) != cwd {
		t.Fatal("exact isolated preprovisioned native state/CWD prerequisites required")
	}
	selected, err := logins.OpenExisting(root)
	if err != nil {
		t.Fatal(err)
	}
	rt, err := selected.Runtime("claude")
	_ = selected.Close()
	if err != nil || !logins.IsClaudeProfileRuntime(root, rt) {
		t.Fatal("selected native profile prerequisite absent", err)
	}
	bin := t.TempDir()
	if err := os.Symlink(image, filepath.Join(bin, "claude")); err != nil {
		t.Fatal(err)
	}
	env := append([]string(nil), os.Environ()...)
	for i, entry := range env {
		if strings.HasPrefix(entry, "PATH=") {
			env[i] = "PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")
		}
	}
	d := assemble(t, func(c *Config) {
		c.StateDir = root
		c.SocketPath = filepath.Join(root, "d.sock")
		c.LockPath = filepath.Join(root, "d.lock")
		c.LogPath = filepath.Join(root, "d.log")
		c.FakeAgentBin = ""
	})
	if len(d.core.List()) != 0 {
		t.Fatal("native qualification requires an empty session roster")
	}
	t.Cleanup(func() {
		for _, m := range d.core.List() {
			if m.RequiredRuntime == "claude-chat" {
				_ = d.ownerKill(m.ID)
			}
		}
	})
	client := dialClient(t, d, protocol.CapSemanticSend, protocol.CapJournal)
	const text = "Reply exactly: SWARM_NATIVE_INITIAL_ONCE"
	for _, mode := range []string{"owner-no-launch-operation", "winning-launch-replay"} {
		t.Run(mode, func(t *testing.T) {
			spec := daemon.LaunchSpec{AgentType: "claude", Cwd: cwd, ClientEnv: env, Cols: 80, Rows: 24, InitialPrompt: text, Options: map[string]string{"claude_interface": "swarm-chat"}}
			var meta persist.Meta
			if mode == "owner-no-launch-operation" {
				id, _, err := client.Launch(protocol.LaunchReq{Agent: "claude", Cwd: cwd, Env: env, Cols: 80, Rows: 24, InitialPrompt: text, Options: map[string]string{"claude_interface": "swarm-chat"}})
				if err != nil {
					t.Fatal("public owner launch refused or hid created session", err)
				}
				local := strings.TrimPrefix(id, d.api.endpointID+"/")
				var found bool
				meta, found = d.core.Get(local)
				if !found {
					t.Fatal("public launch returned no durable session")
				}
			} else {
				spec.OperationID = "native-caller-winning-launch"
				var err error
				meta, err = d.api.Launch(spec)
				if err != nil {
					t.Fatal(err)
				}
			}
			if meta.RequiredRuntime != "claude-chat" || meta.SchemaVersion != 6 || meta.AgentType != "claude" || meta.ShimPID <= 0 {
				t.Fatal("public opt-in did not reach managed native runtime")
			}
			if _, ok := d.claudeNativeStops.Load(meta.ID); !ok {
				t.Fatal("caller was published before independent Stop custody")
			}
			deadline := time.Now().Add(15 * time.Second)
			var record idempotency.Record
			for {
				operations := d.core.SemanticRequestOperations(meta.ID)
				if len(operations) > 1 {
					t.Fatal("public launch minted multiple initial semantic operations")
				}
				if len(operations) == 1 {
					record, _ = d.core.ComposerOperation(operations[0])
					if len(record.Request) > 0 {
						break
					}
				}
				if time.Now().After(deadline) {
					t.Fatal("public launch never wired initial prompt to durable semantic dispatch")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if mode == "owner-no-launch-operation" && record.OperationID != "claude-initial:"+meta.ID {
				t.Fatal("owner launch did not derive initial identity from winning persisted session")
			}
			intent, err := daemon.DecodeClaudeNativeIntent(record.Request)
			if err != nil {
				t.Fatal(err)
			}
			hash := sha256.Sum256([]byte(text))
			if intent.Identity.SessionID != meta.ID || intent.TextSHA256 != hex.EncodeToString(hash[:]) || intent.TextBytes != len(text) {
				t.Fatal("public launch initial operation lost exact text/birth binding")
			}
			if mode == "winning-launch-replay" {
				replay, err := d.api.Launch(spec)
				if err != nil || replay.ID != meta.ID || replay.ShimPID != meta.ShimPID || replay.ShimStartTime != meta.ShimStartTime {
					t.Fatal("public launch replay replaced winning native session", err)
				}
				after, _ := d.core.ComposerOperation(record.OperationID)
				if len(d.core.SemanticRequestOperations(meta.ID)) != 1 || !bytes.Equal(after.Request, record.Request) {
					t.Fatal("winning launch replay created another initial operation or UUID")
				}
			}
			echoes := 0
			for {
				response, err := shim.ReadHookSpoolFileResponse(filepath.Join(root, meta.ID, shim.HookSpoolFile), 0)
				if err != nil {
					t.Fatal(err)
				}
				echoes = 0
				for _, row := range response.Records {
					var env struct {
						Origin string
						Raw    []byte
					}
					var frame struct {
						Type, UUID string
						IsReplay   bool `json:"isReplay"`
					}
					if json.Unmarshal(row.Body, &env) == nil && env.Origin == "claude_primary" && json.Unmarshal(env.Raw, &frame) == nil && frame.Type == "user" && frame.UUID == intent.NativeUserUUID && frame.IsReplay {
						echoes++
					}
				}
				if echoes > 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("actual native initial echo not observed")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if echoes != 1 {
				t.Fatal("actual native public launch replayed initial input")
			}
			id := protocol.NamespacedID(d.api.endpointID, meta.ID)
			settleDeadline := time.Now().Add(15 * time.Second)
			var receipt protocol.SemanticMessageReceipt
			for {
				receipt, err = client.SemanticOperation(id, record.OperationID)
				if err == nil && receipt.State == "completed" {
					break
				}
				if time.Now().After(settleDeadline) {
					t.Fatal("actual native MAIN result did not settle exact initial receipt", receipt, err)
				}
				time.Sleep(10 * time.Millisecond)
			}
			if receipt.Agent != "claude" || receipt.NativeMessageID != intent.NativeUserUUID || receipt.NativeSessionID != meta.ConversationID {
				t.Fatal("actual native receipt lost owner identity", receipt)
			}
			response, err := shim.ReadHookSpoolFileResponse(filepath.Join(root, meta.ID, shim.HookSpoolFile), 0)
			if err != nil {
				t.Fatal(err)
			}
			assistants := map[string]bool{}
			correlatedResult := false
			for _, row := range response.Records {
				var env struct {
					Origin string
					Raw    []byte
				}
				var frame struct {
					Type, UUID string
					Session    string   `json:"session_id"`
					User       string   `json:"user_message_uuid"`
					Users      []string `json:"user_message_uuids"`
					Subtype    string
					Error      bool `json:"is_error"`
				}
				if json.Unmarshal(row.Body, &env) != nil || env.Origin != "claude_primary" || json.Unmarshal(env.Raw, &frame) != nil || frame.Session != meta.ConversationID {
					continue
				}
				if frame.Type == "assistant" && frame.UUID != "" {
					assistants[frame.UUID] = true
				}
				if frame.Type == "result" && frame.Subtype == "success" && !frame.Error {
					correlated := frame.User == intent.NativeUserUUID
					for _, owner := range frame.Users {
						if owner == intent.NativeUserUUID {
							correlated = true
						}
					}
					correlatedResult = correlatedResult || correlated
				}
			}
			if len(assistants) == 0 || !correlatedResult {
				t.Fatal("native qualification lacks actual assistant and owner-correlated result")
			}
			var page schema.NativeHistoryPage
			for {
				page, err = client.NativeInteractionHistory(schema.InteractionHistoryReq{Native: true, Session: id, Limit: 200})
				if err == nil && page.Version == 2 && len(page.Items) >= 2 {
					break
				}
				if time.Now().After(settleDeadline) {
					t.Fatal("actual native history did not flush", page, err)
				}
				time.Sleep(10 * time.Millisecond)
			}
			foundUser, foundAssistant := false, false
			for _, raw := range page.Items {
				var row struct {
					Type, UUID string
					Message    struct{ Content json.RawMessage }
				}
				if json.Unmarshal(raw, &row) != nil {
					t.Fatal("invalid native history row")
				}
				if row.Type == "result" {
					t.Fatal("native history fabricated result row")
				}
				if row.Type == "user" && row.UUID == intent.NativeUserUUID {
					var content string
					if json.Unmarshal(row.Message.Content, &content) != nil || content != text {
						t.Fatal("native history lost exact initial text")
					}
					foundUser = true
				}
				if row.Type == "assistant" && assistants[row.UUID] {
					foundAssistant = true
				}
			}
			if page.Scope == "" || !foundUser || !foundAssistant {
				t.Fatal("native history lost captured owner/assistant identity")
			}
			rows, err := client.List()
			if err != nil {
				t.Fatal(err)
			}
			rosterProof := false
			for _, row := range rows {
				if row.ID == id && row.RequiredRuntime == "claude-chat" && row.Agent == "claude" && row.Capabilities != nil {
					cap := row.Capabilities
					rosterProof = cap.Validate() == nil && cap.NativeInteractionVersion == 2 && cap.ProviderVersion == "2.1.310" && cap.SessionInstance != "" && !cap.StructuredChat && !cap.TerminalFallback && !cap.TerminalControl && !cap.Interrupt
				}
			}
			if !rosterProof || !d.executionCustodyUnsafe(meta.ID) {
				t.Fatal("actual owner roster omitted exact native proof or granted auto custody")
			}
			if err = client.Kill(id); err != nil {
				t.Fatal("authenticated owner Stop did not confirm", err)
			}
			stopDeadline := time.Now().Add(5 * time.Second)
			for logins.VerifyClaudeNativeWritersStopped(root, meta.ID) != nil {
				if time.Now().After(stopDeadline) {
					t.Fatal("native caller qualification lacks independent positive stopped proof")
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}
