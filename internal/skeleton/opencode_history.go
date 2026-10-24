package skeleton

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
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
	if json.Unmarshal(result.Info, &info) != nil || info.ID != id.RootID || info.Directory != id.Directory || info.Time.Created != id.NativeCreatedAtMS || !opencodebridge.SupportedNativeVersion(info.Version) || result.Messages == nil {
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
	core         *daemon.Daemon
	fallback     resumeHistoryResolver
	afterPublish func() // private deterministic publication/launch interleaving seam
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

// Direct exports retain their result. Production hands-off launches hold this same
// publication fence through their launch outcome before releasing ownership.
var openCodeExportMu sync.Mutex

func (r *openCodeHistoryResolver) LocateTranscript(m persist.Meta, id string) (string, resumeHistoryOutcome) {
	if m.AgentType != "opencode" {
		if r.fallback == nil {
			return "", resumeHistoryUnsupported
		}
		return r.fallback.LocateTranscript(m, id)
	}
	openCodeExportMu.Lock()
	defer openCodeExportMu.Unlock()
	path, outcome, owned := r.locateTranscript(m, id)
	if owned != nil {
		_ = owned.root.Close()
	}
	return path, outcome
}

func (r *openCodeHistoryResolver) locateTranscript(m persist.Meta, id string) (string, resumeHistoryOutcome, *ownedHandoffExport) {
	if m.AgentType != "opencode" {
		if r.fallback == nil {
			return "", resumeHistoryUnsupported, nil
		}
		path, outcome := r.fallback.LocateTranscript(m, id)
		return path, outcome, nil
	}
	if r.core == nil || r.Resolve(m).Outcome != resumeHistoryFound || id != m.ConversationID {
		return "", resumeHistoryUnsafe, nil
	}
	current, ok := r.core.Get(m.ID)
	if !ok || current.ConversationID != id || current.OpenCodeBinding == nil || current.OpenCodeBinding.Generation != m.OpenCodeBinding.Generation {
		return "", resumeHistoryUnsafe, nil
	}
	b, err := r.core.SessionOpenCodeBridge(m.ID)
	if err != nil {
		return "", resumeHistoryUnreadable, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	identity := nativeHistoryIdentity(current)
	var result openCodeHistoryExport
	source := "live_sdk"
	nativeVersion := ""
	if current.Status.Process == status.ProcessRunning {
		c, clientErr := opencodebridge.NewClient(b.SocketPath, b.Token)
		if clientErr != nil {
			return "", resumeHistoryUnreadable, nil
		}
		defer c.Close()
		ready, readyErr := c.Ready(ctx)
		if readyErr != nil || !ready.Bound || !ready.Acknowledged || !ready.Ready || ready.Identity != identity || !opencodebridge.SupportedNativeVersion(ready.NativeVersion) {
			return "", resumeHistoryUnreadable, nil
		}
		nativeVersion = ready.NativeVersion
		result, err = collectOpenCodeHistory(ctx, identity, c.Snapshot)
	} else {
		source = "native_cli_export"
		result, err = r.exportEnded(ctx, current)
		if err == nil {
			nativeVersion = current.CLIIdentity.Version
		}
	}
	if err != nil {
		return "", resumeHistoryUnreadable, nil
	}
	if err := validateNativeHistory(&result, identity); err != nil {
		return "", resumeHistoryUnsafe, nil
	}
	result.Provenance = openCodeHistoryProvenance{Identity: identity, Source: source, NativeVersion: nativeVersion, Scope: "exact root; native child references remain in original parts", CapturedAt: time.Now().UTC(), MutableDuringRead: true, Gaps: "native compaction and omitted native data are not reconstructed"}
	stateDir := filepath.Dir(filepath.Dir(b.SocketPath))
	path, owned, err := publishHandoffExport(stateDir, result)
	if err != nil {
		log.Printf("skeleton: OpenCode handoff export at %q failed; retained staging may require manual inspection: %v", filepath.Join(stateDir, handoffExportDirectory), err)
		return "", resumeHistoryUnreadable, nil
	}
	if r.afterPublish != nil {
		r.afterPublish()
	}
	return path, resumeHistoryFound, owned
}

const handoffExportDirectory = "handoff-exports"

type ownedHandoffExport struct {
	root *os.Root
	name string
	info os.FileInfo
}

// Ownership comes only from successful exclusive publication, never a resolver path.
func (o *ownedHandoffExport) discard() error {
	info, err := o.root.Lstat(o.name)
	if err != nil {
		return err
	}
	if !os.SameFile(info, o.info) || !safeHandoffFile(info) {
		return errors.New("handoff: export changed; retained")
	}
	return o.root.Remove(o.name)
}

func safeHandoffFile(info os.FileInfo) bool { return safeHandoffLinks(info, 1) }
func safeHandoffLinks(info os.FileInfo, maxLinks uint64) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.Mode().IsRegular() && info.Mode().Perm() == 0600 && st.Uid == uint32(os.Getuid()) && st.Nlink >= 1 && uint64(st.Nlink) <= maxLinks && info.Size() <= openCodeHistoryMaxBytes
}

func readHandoffCandidate(root *os.Root, name string, canonical []byte, id opencodebridge.Identity, maxLinks uint64, probe func(string) error) (os.FileInfo, error) {
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !safeHandoffLinks(info, maxLinks) {
		return nil, errors.New("handoff: unsafe existing export")
	}
	raw, err := io.ReadAll(io.LimitReader(file, openCodeHistoryMaxBytes+1))
	if err != nil {
		return nil, err
	}
	var previous openCodeHistoryExport
	if json.Unmarshal(raw, &previous) != nil || validateNativeHistory(&previous, id) != nil {
		return nil, errors.New("handoff: invalid existing export")
	}
	previousCanonical, err := canonicalHandoffExport(previous)
	if err != nil || !bytes.Equal(canonical, previousCanonical) {
		return nil, errors.New("handoff: existing export content mismatch")
	}
	if probe != nil {
		if err := probe("file-sync"); err != nil {
			return nil, err
		}
	}
	if err := file.Sync(); err != nil {
		return nil, err
	}
	return info, nil
}

func removeHandoffStaging(root *os.Root, name string, expected os.FileInfo, maxLinks uint64) error {
	current, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !os.SameFile(expected, current) || !safeHandoffLinks(current, maxLinks) {
		return errors.New("handoff: staging entry changed; retained")
	}
	return root.Remove(name)
}

func canonicalHandoffExport(result openCodeHistoryExport) ([]byte, error) {
	result.Provenance.CapturedAt = time.Time{}
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func publishHandoffExport(stateDir string, result openCodeHistoryExport) (string, *ownedHandoffExport, error) {
	return publishHandoffExportWithProbe(stateDir, result, nil)
}

func publishHandoffExportWithProbe(stateDir string, result openCodeHistoryExport, probe func(string) error) (retPath string, retOwned *ownedHandoffExport, retErr error) {
	canonical, err := canonicalHandoffExport(result)
	if err != nil {
		return "", nil, err
	}
	digest := sha256.Sum256(canonical)
	name := "native-history-" + hex.EncodeToString(digest[:]) + ".json"
	defer func() {
		if retErr != nil {
			retErr = fmt.Errorf("handoff: export %q: %w", filepath.Join(stateDir, handoffExportDirectory, name), retErr)
		}
	}()
	raw, err := json.MarshalIndent(result, "", "  ")
	if err != nil || len(raw)+1 > openCodeHistoryMaxBytes {
		return "", nil, errors.New("handoff: export exceeds byte bound")
	}
	state, err := os.OpenRoot(stateDir)
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = state.Close() }()
	if err := state.Mkdir(handoffExportDirectory, 0700); err != nil {
		if !os.IsExist(err) {
			return "", nil, err
		}
	}
	parent, err := state.Open(".")
	if err != nil {
		return "", nil, err
	}
	err = parent.Sync()
	_ = parent.Close()
	if err != nil {
		return "", nil, err
	}
	directory, err := state.OpenFile(handoffExportDirectory, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_DIRECTORY, 0)
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = directory.Close() }()
	syncDirectory := func() error {
		if probe != nil {
			if err := probe("directory-sync"); err != nil {
				return err
			}
		}
		return directory.Sync()
	}
	info, err := directory.Stat()
	if err != nil {
		return "", nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm() != 0700 || st.Uid != uint32(os.Getuid()) {
		return "", nil, errors.New("handoff: unsafe export directory")
	}
	root, err := state.OpenRoot(handoffExportDirectory)
	if err != nil {
		return "", nil, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = root.Close()
		}
	}()
	opened, err := root.Open(".")
	if err != nil {
		return "", nil, err
	}
	actual, err := opened.Stat()
	_ = opened.Close()
	if err != nil || !os.SameFile(info, actual) {
		return "", nil, errors.New("handoff: export directory changed")
	}
	path := filepath.Join(stateDir, handoffExportDirectory, name)
	// One content-bound staging name makes restart recovery local to this export.
	temporary := ".pending-" + name
	staged := false
	var stagingInfo os.FileInfo
	if pending, err := readHandoffCandidate(root, temporary, canonical, result.Provenance.Identity, 2, probe); err == nil {
		stagingInfo = pending
		target, statErr := root.Lstat(name)
		if statErr == nil {
			st := pending.Sys().(*syscall.Stat_t)
			if st.Nlink == 2 && (!os.SameFile(pending, target) || !safeHandoffLinks(target, 2)) {
				return "", nil, errors.New("handoff: staging has an unrelated link; retained")
			}
			if err := removeHandoffStaging(root, temporary, pending, 2); err != nil {
				return "", nil, err
			}
			if err := syncDirectory(); err != nil {
				return "", nil, err
			}
		} else if os.IsNotExist(statErr) && safeHandoffFile(pending) {
			staged = true
		} else {
			return "", nil, errors.New("handoff: staging ownership is ambiguous; retained")
		}
	} else if !os.IsNotExist(err) {
		return "", nil, err
	}
	if _, err := readHandoffCandidate(root, name, canonical, result.Provenance.Identity, 1, probe); err == nil {
		if err := syncDirectory(); err != nil {
			return "", nil, err
		}
		return path, nil, nil
	} else if !os.IsNotExist(err) {
		return "", nil, err
	}
	linked := false
	if !staged {
		file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
		if err != nil {
			return "", nil, err
		}
		stagingInfo, err = file.Stat()
		if err != nil {
			_ = file.Close()
			return "", nil, err
		}
		defer func() {
			if !linked {
				if err := removeHandoffStaging(root, temporary, stagingInfo, 1); err != nil {
					retErr = errors.Join(retErr, errors.New("handoff: staging cleanup failed"), err)
				}
			}
		}()
		_, err = file.Write(append(raw, '\n'))
		if err == nil && probe != nil {
			err = probe("after-write")
		}
		if err == nil && probe != nil {
			err = probe("file-sync")
		}
		if err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return "", nil, err
		}
	}
	if probe != nil {
		if err := probe("before-link"); err != nil {
			return "", nil, err
		}
	}
	if err := root.Link(temporary, name); err != nil {
		return "", nil, err
	}
	linked = true
	if probe != nil {
		if err := probe("after-link"); err != nil {
			return "", nil, err
		}
	}
	if err := removeHandoffStaging(root, temporary, stagingInfo, 2); err != nil {
		return "", nil, err
	}
	if probe != nil {
		if err := probe("after-unlink"); err != nil {
			return "", nil, err
		}
	}
	if err := syncDirectory(); err != nil {
		return "", nil, err
	}
	info, err = root.Lstat(name)
	if err != nil || !safeHandoffFile(info) {
		return "", nil, errors.New("handoff: published export changed")
	}
	keep = true
	return path, &ownedHandoffExport{root: root, name: name, info: info}, nil
}

// This wrapper carries exclusive publication ownership back to the launch owner.
type handoffHistoryResolver struct {
	resumeHistoryResolver
	native       *openCodeHistoryResolver
	owned        *ownedHandoffExport
	preflight    func(persist.Meta) error
	preflightErr error
}

func (r *handoffHistoryResolver) LocateTranscript(m persist.Meta, id string) (string, resumeHistoryOutcome) {
	if m.AgentType == "opencode" && r.preflight != nil {
		if r.preflightErr = r.preflight(m); r.preflightErr != nil {
			return "", resumeHistoryUnsafe
		}
	}
	path, outcome, owned := r.native.locateTranscript(m, id)
	r.owned = owned
	return path, outcome
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
func (r *openCodeHistoryResolver) nativeExportCommand(ctx context.Context, m persist.Meta) (*exec.Cmd, error) {
	if m.CLIIdentity == nil || !opencodebridge.SupportedNativeVersion(m.CLIIdentity.Version) || !persist.MatchCLIFingerprint(m.CLIIdentity.Path, m.CLIIdentity.Fingerprint) {
		return nil, errors.New("native history executable unconfirmed")
	}
	native, err := r.core.OpenCodeResumeEnvironment(m.ID)
	if err != nil {
		return nil, err
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
			return nil, errors.New("native history profile unconfirmed")
		}
		data = filepath.Join(home, ".local", "share")
	}
	if fi, err := os.Stat(filepath.Join(data, "opencode", "opencode.db")); err != nil || !fi.Mode().IsRegular() {
		return nil, errors.New("native history profile unavailable")
	}
	observed, err := probeCLIIdentity("opencode", m.CLIIdentity.Path, env, m.ProviderCwd())
	if err != nil || observed == nil || *observed != *m.CLIIdentity {
		return nil, errors.New("native history executable changed")
	}
	cmd := exec.CommandContext(ctx, m.CLIIdentity.Path, "export", m.ConversationID)
	cmd.Env, cmd.Dir = env, m.ProviderCwd()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 100 * time.Millisecond
	cmd.Stderr = &cappedCLIOutput{}
	return cmd, nil
}
func (r *openCodeHistoryResolver) exportEnded(ctx context.Context, m persist.Meta) (openCodeHistoryExport, error) {
	var result openCodeHistoryExport
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd, err := r.nativeExportCommand(ctx, m)
	if err != nil {
		return result, err
	}
	output := &boundedNativeExport{cancel: cancel}
	cmd.Stdout = output
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
