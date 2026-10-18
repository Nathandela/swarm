package skeleton

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/opencodebridge"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

const openCodeHistoryMaxBytes = 8 << 20
const openCodeHistoryMaxMessages = 4096

// Native rows stay intact; provenance describes this root-only rolling export.
type openCodeHistoryExport struct {
	Info       json.RawMessage           `json:"info"`
	Messages   []json.RawMessage         `json:"messages"`
	Provenance openCodeHistoryProvenance `json:"swarm_provenance"`
}
type openCodeHistoryProvenance struct {
	Identity          opencodebridge.Identity `json:"identity"`
	Source            string                  `json:"source"`
	NativeVersion     string                  `json:"native_version"`
	Scope             string                  `json:"scope"`
	CapturedAt        time.Time               `json:"captured_at"`
	Truncated         bool                    `json:"truncated"`
	MutableDuringRead bool                    `json:"mutable_during_read"`
	Gaps              string                  `json:"gaps"`
}

func nativeHistoryIdentity(m persist.Meta) opencodebridge.Identity {
	b := m.OpenCodeBinding
	return opencodebridge.Identity{Version: 1, WorkerID: m.ID, RootID: m.ConversationID, Directory: m.ProviderCwd(), SessionInstance: b.Generation, BackendGeneration: b.Generation, NativeCreatedAtMS: b.NativeCreatedAtMS}
}
func validateNativeHistory(result *openCodeHistoryExport, id opencodebridge.Identity) error {
	var info struct {
		ID        string `json:"id"`
		Directory string `json:"directory"`
		Version   string `json:"version"`
		Time      struct {
			Created int64 `json:"created"`
		} `json:"time"`
	}
	if json.Unmarshal(result.Info, &info) != nil || info.ID != id.RootID || info.Directory != id.Directory || info.Time.Created != id.NativeCreatedAtMS || info.Version != "1.19.3" || result.Messages == nil {
		return errors.New("native history identity or version unconfirmed")
	}
	if len(result.Messages) > openCodeHistoryMaxMessages {
		return errors.New("native history exceeds message bound")
	}
	seen := map[string]bool{}
	parts := map[string]bool{}
	size := len(result.Info)
	for _, raw := range result.Messages {
		size += len(raw)
		if size > openCodeHistoryMaxBytes {
			return errors.New("native history exceeds byte bound")
		}
		var row struct {
			Info struct {
				ID        string `json:"id"`
				SessionID string `json:"sessionID"`
				Role      string `json:"role"`
			} `json:"info"`
			Parts []struct {
				ID        string `json:"id"`
				SessionID string `json:"sessionID"`
				MessageID string `json:"messageID"`
				Type      string `json:"type"`
			} `json:"parts"`
		}
		if json.Unmarshal(raw, &row) != nil || row.Info.SessionID != id.RootID || !strings.HasPrefix(row.Info.ID, "msg_") || !adapter.IsOpenCodeConversationID("ses_"+strings.TrimPrefix(row.Info.ID, "msg_")) || seen[row.Info.ID] || (row.Info.Role != "user" && row.Info.Role != "assistant") || row.Parts == nil {
			return errors.New("native history message identity unconfirmed")
		}
		seen[row.Info.ID] = true
		for _, part := range row.Parts {
			if part.ID == "" || parts[part.ID] || part.SessionID != id.RootID || part.MessageID != row.Info.ID || part.Type == "" {
				return errors.New("native history part identity unconfirmed")
			}
			parts[part.ID] = true
		}
	}
	return nil
}
func collectOpenCodeHistory(ctx context.Context, id opencodebridge.Identity, read func(context.Context, int, string) (opencodebridge.Snapshot, error)) (openCodeHistoryExport, error) {
	result := openCodeHistoryExport{Messages: make([]json.RawMessage, 0)}
	cursors := map[string]bool{}
	cursor := ""
	for page := 0; page < 64; page++ {
		snapshot, err := read(ctx, 100, cursor)
		if err != nil {
			return result, err
		}
		if err := validateOpenCodeSnapshot(snapshot, id); err != nil {
			return result, err
		}
		if len(snapshot.Messages) > 100 {
			return result, errors.New("native history page exceeds bound")
		}
		if page == 0 {
			result.Info = append(json.RawMessage(nil), snapshot.Session...)
		}
		result.Messages = append(result.Messages, snapshot.Messages...)
		if err := validateNativeHistory(&result, id); err != nil {
			return result, err
		}
		if snapshot.NextCursor == "" {
			sortNativeHistory(result.Messages)
			return result, nil
		}
		if len(snapshot.NextCursor) > 4096 || cursors[snapshot.NextCursor] {
			return result, errors.New("native history pagination repeated")
		}
		cursors[snapshot.NextCursor] = true
		cursor = snapshot.NextCursor
	}
	return result, errors.New("native history exceeds page bound")
}
func sortNativeHistory(rows []json.RawMessage) {
	type ordered struct {
		raw     json.RawMessage
		created int64
		id      string
	}
	values := make([]ordered, len(rows))
	for i, raw := range rows {
		var row struct {
			Info struct {
				ID   string `json:"id"`
				Time struct {
					Created int64 `json:"created"`
				} `json:"time"`
			} `json:"info"`
		}
		_ = json.Unmarshal(raw, &row)
		values[i] = ordered{raw, row.Info.Time.Created, row.Info.ID}
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].created != values[j].created {
			return values[i].created < values[j].created
		}
		return values[i].id < values[j].id
	})
	for i, value := range values {
		rows[i] = value.raw
	}
}

type openCodeHistoryResolver struct {
	core     *daemon.Daemon
	fallback resumeHistoryResolver
}

func (r *openCodeHistoryResolver) Resolve(m persist.Meta) resumeHistoryResult {
	if m.AgentType != "opencode" {
		if r.fallback == nil {
			return resumeHistoryResult{Outcome: resumeHistoryUnsupported}
		}
		return r.fallback.Resolve(m)
	}
	if m.RequiredRuntime != persist.RequiredRuntimeOpenCode || m.OpenCodeBinding == nil || m.OpenCodeBinding.NativeCreatedAtMS <= 0 || !adapter.IsOpenCodeConversationID(m.ConversationID) {
		return resumeHistoryResult{Outcome: resumeHistoryUnsafe}
	}
	return resumeHistoryResult{Outcome: resumeHistoryFound, ConversationID: m.ConversationID}
}
func (r *openCodeHistoryResolver) LocateTranscript(m persist.Meta, id string) (string, resumeHistoryOutcome) {
	if m.AgentType != "opencode" {
		if r.fallback == nil {
			return "", resumeHistoryUnsupported
		}
		return r.fallback.LocateTranscript(m, id)
	}
	if r.core == nil || r.Resolve(m).Outcome != resumeHistoryFound || id != m.ConversationID {
		return "", resumeHistoryUnsafe
	}
	current, ok := r.core.Get(m.ID)
	if !ok || current.ConversationID != id || current.OpenCodeBinding == nil || current.OpenCodeBinding.Generation != m.OpenCodeBinding.Generation {
		return "", resumeHistoryUnsafe
	}
	b, err := r.core.SessionOpenCodeBridge(m.ID)
	if err != nil {
		return "", resumeHistoryUnreadable
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	identity := nativeHistoryIdentity(current)
	var result openCodeHistoryExport
	source := "live_sdk"
	if current.Status.Process == status.ProcessRunning {
		c, err := opencodebridge.NewClient(b.SocketPath, b.Token)
		if err != nil {
			return "", resumeHistoryUnreadable
		}
		defer c.Close()
		ready, err := c.Ready(ctx)
		if err != nil || !ready.Bound || !ready.Acknowledged || !ready.Ready || ready.Identity != identity {
			return "", resumeHistoryUnreadable
		}
		result, err = collectOpenCodeHistory(ctx, identity, c.Snapshot)
	} else {
		source = "native_cli_export"
		result, err = r.exportEnded(ctx, current)
	}
	if err != nil {
		return "", resumeHistoryUnreadable
	}
	if err := validateNativeHistory(&result, identity); err != nil {
		return "", resumeHistoryUnsafe
	}
	result.Provenance = openCodeHistoryProvenance{Identity: identity, Source: source, NativeVersion: "1.19.3", Scope: "exact root; native child references remain in original parts", CapturedAt: time.Now().UTC(), MutableDuringRead: true, Gaps: "native compaction and omitted native data are not reconstructed"}
	raw, err := json.MarshalIndent(result, "", "  ")
	if err != nil || len(raw) > openCodeHistoryMaxBytes {
		return "", resumeHistoryUnsafe
	}
	// The public pointer is immutable and separate from the private binding credential.
	dir := filepath.Dir(b.SocketPath)
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", resumeHistoryUnreadable
	}
	defer root.Close()
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return "", resumeHistoryUnreadable
	}
	name := "native-history-" + hex.EncodeToString(nonce[:]) + ".json"
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return "", resumeHistoryUnreadable
	}
	_, err = file.Write(append(raw, '\n'))
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = root.Remove(name)
		return "", resumeHistoryUnreadable
	}
	parent, err := root.Open(".")
	if err != nil {
		_ = root.Remove(name)
		return "", resumeHistoryUnreadable
	}
	err = parent.Sync()
	_ = parent.Close()
	if err != nil {
		_ = root.Remove(name)
		return "", resumeHistoryUnreadable
	}
	return filepath.Join(dir, name), resumeHistoryFound
}

type boundedNativeExport struct {
	data   bytes.Buffer
	cancel context.CancelFunc
}

func (w *boundedNativeExport) Write(p []byte) (int, error) {
	if w.data.Len()+len(p) > openCodeHistoryMaxBytes {
		w.cancel()
		return 0, io.ErrShortBuffer
	}
	return w.data.Write(p)
}
func (r *openCodeHistoryResolver) exportEnded(ctx context.Context, m persist.Meta) (openCodeHistoryExport, error) {
	var result openCodeHistoryExport
	if m.CLIIdentity == nil || m.CLIIdentity.Version != "1.19.3" || !persist.MatchCLIFingerprint(m.CLIIdentity.Path, m.CLIIdentity.Fingerprint) {
		return result, errors.New("native history executable unconfirmed")
	}
	native, err := r.core.OpenCodeResumeEnvironment(m.ID)
	if err != nil {
		return result, err
	}
	env := mergeNativeOpenCodeEnvironment(m.Env, native)
	// Missing storage must refuse before the CLI can create a replacement empty profile.
	home, data := "", ""
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		if key == "HOME" {
			home = value
		}
		if key == "XDG_DATA_HOME" {
			data = value
		}
	}
	if data == "" {
		if home == "" {
			return result, errors.New("native history profile unconfirmed")
		}
		data = filepath.Join(home, ".local", "share")
	}
	if fi, err := os.Stat(filepath.Join(data, "opencode", "opencode.db")); err != nil || !fi.Mode().IsRegular() {
		return result, errors.New("native history profile unavailable")
	}
	observed, err := probeCLIIdentity("opencode", m.CLIIdentity.Path, env, m.ProviderCwd())
	if err != nil || observed == nil || *observed != *m.CLIIdentity {
		return result, errors.New("native history executable changed")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, m.CLIIdentity.Path, "export", m.ConversationID)
	cmd.Env, cmd.Dir = env, m.ProviderCwd()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 100 * time.Millisecond
	output := &boundedNativeExport{cancel: cancel}
	cmd.Stdout = output
	cmd.Stderr = &cappedCLIOutput{}
	if cmd.Run() != nil || !persist.MatchCLIFingerprint(m.CLIIdentity.Path, m.CLIIdentity.Fingerprint) {
		return result, errors.New("native history export unavailable")
	}
	if json.Unmarshal(output.data.Bytes(), &result) != nil {
		return result, errors.New("native history export unreadable")
	}
	if err := validateNativeHistory(&result, nativeHistoryIdentity(m)); err != nil {
		return result, err
	}
	sortNativeHistory(result.Messages)
	return result, nil
}
