package skeleton

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/Nathandela/swarm/internal/accountconfig"
	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

const accountInboxDirectory = "accounts/recovery-inbox"
const accountInboxMaxEntries = 256
const accountInboxMaxBytes = 64 << 10
const accountObservationHoldFile = "account-observation-hold.json"

var errAccountInboxRetry = errors.New("account recovery observation has not been durably accepted")

// This is accepted evidence, not a second recovery journal. Only the existing
// auth-watch writer applies these bounded facts to the registry and journal.
// Native error text, prompt text, raw frames and hook tokens never enter it.
type accountInboxRecord struct {
	SchemaVersion int                         `json:"schema_version"`
	ID            string                      `json:"id"`
	Kind          string                      `json:"kind"`
	Local         string                      `json:"local"`
	Binding       accounts.Binding            `json:"binding"`
	Conversation  string                      `json:"conversation"`
	ShimPID       int                         `json:"shim_pid"`
	ShimStartTime int64                       `json:"shim_start_time"`
	Instance      string                      `json:"instance,omitempty"`
	Feed          string                      `json:"feed,omitempty"`
	Sequence      uint64                      `json:"sequence,omitempty"`
	ReceivedAt    time.Time                   `json:"received_at"`
	Model         string                      `json:"model,omitempty"`
	Class         string                      `json:"class,omitempty"`
	TurnID        string                      `json:"turn_id,omitempty"`
	Started       bool                        `json:"started,omitempty"`
	Completed     bool                        `json:"completed,omitempty"`
	OwnerPrompt   bool                        `json:"owner_prompt,omitempty"`
	ClearModel    bool                        `json:"clear_model,omitempty"`
	QuotaFeed     uint64                      `json:"quota_feed,omitempty"`
	QuotaSequence uint64                      `json:"quota_sequence,omitempty"`
	Scopes        []accounts.ScopeObservation `json:"scopes,omitempty"`
}

type accountInboxQuotaStamp struct {
	Binding  accounts.Binding
	Physical string
	Feed     uint64
	Sequence uint64
}

func accountInboxID(rec accountInboxRecord) string {
	rec.ID, rec.ReceivedAt, rec.QuotaFeed, rec.QuotaSequence = "", time.Time{}, 0, 0
	raw, _ := json.Marshal(rec)
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

func (m *accountRotationManager) inboxRecord(local, kind, conversation string, sequence uint64) (accountInboxRecord, bool) {
	if m == nil || m.w == nil {
		return accountInboxRecord{}, false
	}
	meta, ok := m.w.get(local)
	if !ok || meta.AccountBinding == nil || meta.ConversationID != conversation || !validManagedBinding(*meta.AccountBinding) {
		return accountInboxRecord{}, false
	}
	rec := accountInboxRecord{SchemaVersion: 1, Kind: kind, Local: local, Binding: *meta.AccountBinding, Conversation: conversation, ShimPID: meta.ShimPID, ShimStartTime: meta.ShimStartTime, Sequence: sequence, ReceivedAt: time.Now().UTC()}
	if m.d != nil {
		rec.Instance, _ = m.d.sessionInstance(local)
	}
	return rec, true
}

func openAccountInbox(stateDir string, create bool) (*os.Root, error) {
	root, err := openAccountRecoveryRoot(stateDir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	if !create {
		if _, err := root.Lstat(accountInboxDirectory); errors.Is(err, os.ErrNotExist) {
			return nil, os.ErrNotExist
		}
	}
	inbox, err := historyOpenDir(root, accountInboxDirectory, create)
	if err != nil {
		return nil, err
	}
	info, err := inbox.Lstat(".")
	stat, ok := infoSysStat(info)
	if err != nil || !ok || stat.Uid != uint32(os.Getuid()) || info.Mode().Perm()&0o077 != 0 {
		_ = inbox.Close()
		return nil, accounts.ErrUnsafePath
	}
	return inbox, nil
}

func infoSysStat(info os.FileInfo) (*syscall.Stat_t, bool) {
	if info == nil {
		return nil, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return stat, ok
}

func readAccountInbox(root *os.Root, name string) (accountInboxRecord, error) {
	rec, err := readAccountEvidence(root, name)
	if err != nil {
		return rec, err
	}
	if rec.ID+".json" != name {
		return rec, accounts.ErrUnsafePath
	}
	return rec, nil
}

func readAccountEvidence(root *os.Root, name string) (accountInboxRecord, error) {
	var rec accountInboxRecord
	if name != accountObservationHoldFile && (!strings.HasSuffix(name, ".json") || !accountHex(strings.TrimSuffix(name, ".json"), 64)) {
		return rec, accounts.ErrUnsafePath
	}
	f, err := historyOpenFile(root, name)
	if err != nil {
		return rec, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || info.Mode().Perm()&0o077 != 0 {
		return rec, accounts.ErrUnsafePath
	}
	raw, err := io.ReadAll(io.LimitReader(f, accountInboxMaxBytes+1))
	if err != nil || len(raw) > accountInboxMaxBytes || rejectDuplicateJSONKeys(raw) != nil {
		return rec, accounts.ErrUnsafePath
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if dec.Decode(&rec) != nil || dec.Decode(&struct{}{}) != io.EOF || rec.SchemaVersion != 1 || accountInboxID(rec) != rec.ID || !persist.ValidID(rec.Local) || !validManagedBinding(rec.Binding) || len(rec.Conversation) > 256 || len(rec.Model) > 128 || len(rec.TurnID) > 256 || len(rec.Scopes) > 128 || rec.ReceivedAt.IsZero() || (rec.Class != "" && rec.Class != "quota" && rec.Class != "auth-invalid") {
		return rec, accounts.ErrUnsafePath
	}
	switch rec.Kind {
	case "model", "failure", "claude-turn", "native", "hold":
	default:
		return rec, accounts.ErrUnsafePath
	}
	return rec, nil
}

func inboxEntries(root *os.Root) ([]os.DirEntry, error) {
	f, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	entries, err := f.ReadDir(accountInboxMaxEntries + 1)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if len(entries) > accountInboxMaxEntries {
		return nil, accounts.ErrInUse
	}
	return entries, nil
}

// Only the writer's recognizable, private, uncommitted temporary inodes may
// be retired. They have never authorized hook acknowledgement or recovery.
func cleanupInboxTemps(root *os.Root) error {
	entries, err := inboxEntries(root)
	if err != nil {
		return err
	}
	changed := false
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".pending-") {
			continue
		}
		if suffix := strings.TrimPrefix(entry.Name(), ".pending-"); len(suffix) != 26 || !persist.ValidID(suffix) {
			return accounts.ErrUnsafePath
		}
		f, err := historyOpenFile(root, entry.Name())
		if err != nil {
			return err
		}
		info, err := f.Stat()
		_ = f.Close()
		if err != nil || info.Mode().Perm()&0o077 != 0 || info.Size() > accountInboxMaxBytes {
			return accounts.ErrUnsafePath
		}
		if err := root.Remove(entry.Name()); err != nil {
			return err
		}
		changed = true
	}
	if changed {
		return historySync(root)
	}
	return nil
}

func (m *accountRotationManager) acceptInbox(rec accountInboxRecord) error {
	rec.ID = accountInboxID(rec)
	m.inboxMu.Lock()
	defer m.inboxMu.Unlock()
	if m.d != nil && rec.Instance == "" {
		m.markInboxErrorLocked(rec.Local)
		return errAccountInboxRetry
	}
	root, err := openAccountInbox(m.w.stateDir, true)
	if err != nil {
		m.markInboxErrorLocked(rec.Local)
		return errAccountInboxRetry
	}
	defer func() { _ = root.Close() }()
	if err := cleanupInboxTemps(root); err != nil {
		m.markInboxErrorLocked(rec.Local)
		return errAccountInboxRetry
	}
	entries, err := inboxEntries(root)
	if err != nil {
		m.markInboxErrorLocked(rec.Local)
		return errAccountInboxRetry
	}
	name := rec.ID + ".json"
	if _, err := root.Lstat(name); err == nil {
		existing, readErr := readAccountInbox(root, name)
		if readErr != nil || historySync(root) != nil {
			m.markInboxErrorLocked(rec.Local)
			return errAccountInboxRetry
		}
		rec = existing
	} else {
		if !errors.Is(err, os.ErrNotExist) || len(entries) >= accountInboxMaxEntries {
			m.markInboxErrorLocked(rec.Local)
			return errAccountInboxRetry
		}
		if rec.Kind == "native" && rec.Feed != "" {
			registry, err := m.store.Snapshot()
			if err != nil {
				m.markInboxErrorLocked(rec.Local)
				return errAccountInboxRetry
			}
			quota := registry.Accounts[rec.Binding.AccountID].Quota
			stamp, exists := m.inboxQuotaFeeds[rec.Local]
			sameFeed := exists && stamp.Physical == rec.Feed && stamp.Binding == rec.Binding
			if sameFeed && stamp.Binding == rec.Binding {
				rec.QuotaFeed, rec.QuotaSequence = stamp.Feed, stamp.Sequence+1
				if quota.FeedGeneration == rec.QuotaFeed && rec.QuotaSequence <= quota.LastSequence {
					rec.QuotaSequence = quota.LastSequence + 1
				}
			} else {
				rec.QuotaFeed, rec.QuotaSequence = quota.FeedGeneration+1, 1
				for _, allocated := range m.inboxQuotaFeeds {
					if allocated.Binding.AccountID == rec.Binding.AccountID && allocated.Binding.CredentialGeneration == rec.Binding.CredentialGeneration && allocated.Feed >= rec.QuotaFeed {
						rec.QuotaFeed = allocated.Feed + 1
					}
				}
			}
			for _, entry := range entries {
				pending, err := readAccountInbox(root, entry.Name())
				if err != nil {
					m.markInboxErrorLocked(rec.Local)
					return errAccountInboxRetry
				}
				if pending.Binding.AccountID == rec.Binding.AccountID && pending.Binding.CredentialGeneration == rec.Binding.CredentialGeneration {
					if pending.Feed == rec.Feed && pending.QuotaFeed >= rec.QuotaFeed {
						rec.QuotaFeed = pending.QuotaFeed
						if pending.QuotaSequence >= rec.QuotaSequence {
							rec.QuotaSequence = pending.QuotaSequence + 1
						}
					}
					if pending.Feed != rec.Feed && !sameFeed && pending.QuotaFeed >= rec.QuotaFeed {
						rec.QuotaFeed = pending.QuotaFeed + 1
						rec.QuotaSequence = 1
					}
				}
			}
			if rec.QuotaFeed == 0 || rec.QuotaSequence == 0 {
				m.markInboxErrorLocked(rec.Local)
				return errAccountInboxRetry
			}
		}
		raw, err := json.Marshal(rec)
		if err != nil || len(raw) > accountInboxMaxBytes {
			m.markInboxErrorLocked(rec.Local)
			return errAccountInboxRetry
		}
		temporary := ".pending-" + newItemID()
		f, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
		if err == nil {
			_, err = f.Write(raw)
			if err == nil {
				err = f.Sync()
			}
			closeErr := f.Close()
			if err == nil {
				err = closeErr
			}
		}
		if err == nil {
			err = root.Rename(temporary, name)
		}
		_ = root.Remove(temporary)
		if err == nil {
			err = historySync(root)
		}
		if err != nil {
			m.markInboxErrorLocked(rec.Local)
			return errAccountInboxRetry
		}
	}
	if rec.Kind == "native" && rec.Feed != "" {
		if m.inboxQuotaFeeds == nil {
			m.inboxQuotaFeeds = make(map[string]accountInboxQuotaStamp)
		}
		m.inboxQuotaFeeds[rec.Local] = accountInboxQuotaStamp{Binding: rec.Binding, Physical: rec.Feed, Feed: rec.QuotaFeed, Sequence: rec.QuotaSequence}
	}
	delete(m.inboxErrors, rec.Local)
	if m.inboxModels == nil {
		m.inboxModels = make(map[string]string)
	}
	if inboxHoldsModel(rec) {
		m.inboxModels[rec.ID] = rec.Local
	}
	m.admitClaudePromptLocked(rec)
	m.wakeInboxLocked()
	return nil
}

func (m *accountRotationManager) markInboxErrorLocked(local string) {
	if m.inboxErrors == nil {
		m.inboxErrors = make(map[string]bool)
	}
	m.inboxErrors[local] = true
}

func (m *accountRotationManager) wakeInboxLocked() {
	select {
	case m.w.managedOps <- accountOwnerOperation{apply: func(*authWatcher) error { return m.drainInbox() }}:
	default: // The durable file remains visible to the writer's ordinary tick.
	}
}

func (m *accountRotationManager) modelInboxHeld(local string) bool {
	m.inboxMu.Lock()
	defer m.inboxMu.Unlock()
	if m.w.stateErr != nil || m.inboxFatal || m.inboxErrors[local] || m.inboxHolds[local] {
		return true
	}
	for id, pending := range m.inboxModels {
		if pending == local && id != m.inboxApplying {
			return true
		}
	}
	return false
}

func inboxHoldsModel(rec accountInboxRecord) bool {
	return rec.Kind == "model" || rec.ClearModel || rec.Class != ""
}

func (m *accountRotationManager) drainInbox() error {
	if m.w.stateErr != nil || m.w.stopping() {
		return protocol.ErrAccountsUnavailable
	}
	m.inboxMu.Lock()
	root, err := openAccountInbox(m.w.stateDir, false)
	if errors.Is(err, os.ErrNotExist) {
		m.inboxMu.Unlock()
		return nil
	}
	if err != nil {
		m.inboxMu.Unlock()
		m.w.stateErr = err
		return err
	}
	defer func() { _ = root.Close() }()
	if err := cleanupInboxTemps(root); err != nil {
		m.inboxMu.Unlock()
		m.w.stateErr = err
		return err
	}
	entries, err := inboxEntries(root)
	var records []accountInboxRecord
	models := make(map[string]string)
	if err == nil {
		// Complete a possibly uncertain previous unlink before accepting its absence.
		err = historySync(root)
	}
	if err == nil {
		for _, entry := range entries {
			var rec accountInboxRecord
			rec, err = readAccountInbox(root, entry.Name())
			if err != nil {
				break
			}
			records = append(records, rec)
			m.admitClaudePromptLocked(rec)
			if inboxHoldsModel(rec) {
				models[rec.ID] = rec.Local
			}
		}
	}
	m.inboxModels = models
	m.inboxMu.Unlock()
	if err != nil {
		m.w.stateErr = err
		return err
	}
	sort.Slice(records, func(i, j int) bool {
		if !records[i].ReceivedAt.Equal(records[j].ReceivedAt) {
			return records[i].ReceivedAt.Before(records[j].ReceivedAt)
		}
		return records[i].ID < records[j].ID
	})
	blocked := make(map[string]bool)
	var firstErr error
	for _, rec := range records {
		if blocked[rec.Local] {
			continue
		}
		m.inboxMu.Lock()
		m.inboxApplying = rec.ID
		m.inboxMu.Unlock()
		applyErr := m.applyInbox(rec)
		m.inboxMu.Lock()
		m.inboxApplying = ""
		m.inboxMu.Unlock()
		if err := applyErr; err != nil {
			blocked[rec.Local] = true
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		// A previous apply may have published a file before its parent fsync
		// failed. Idempotent replay can then do no new write; confirm that
		// serialized journal rename before retiring the durable evidence.
		parent, confirmErr := openAccountRecoveryRoot(m.w.stateDir)
		if confirmErr == nil {
			confirmErr = historySync(parent)
			_ = parent.Close()
		}
		if confirmErr != nil {
			blocked[rec.Local] = true
			if firstErr == nil {
				firstErr = confirmErr
			}
			continue
		}
		m.inboxMu.Lock()
		err := root.Remove(rec.ID + ".json")
		if err == nil {
			err = historySync(root)
		}
		if err == nil {
			delete(m.inboxModels, rec.ID)
		}
		m.inboxMu.Unlock()
		if err != nil {
			return err
		}
	}
	return firstErr
}

func (m *accountRotationManager) nativeInboxFailed(local, instance string) {
	log.Printf("account recovery: native observation for %s could not be durably accepted; holding automatic recovery", local)
	meta, ok := m.w.get(local)
	if ok && meta.AccountBinding != nil {
		rec, valid := m.inboxRecord(local, "hold", meta.ConversationID, 0)
		if valid && rec.Instance == instance {
			rec.ID = accountInboxID(rec)
			m.inboxMu.Lock()
			err := writeAccountObservationHold(m.w.stateDir, rec)
			if m.inboxHolds == nil {
				m.inboxHolds = make(map[string]bool)
			}
			m.inboxHolds[local] = true
			if err != nil {
				m.inboxFatal = true
			}
			m.inboxMu.Unlock()
		}
	}
	if m.w.restoreRecycle != nil {
		m.w.restoreRecycle(local)
	}
}

func writeAccountObservationHold(stateDir string, rec accountInboxRecord) error {
	root, err := openAccountRecoveryRoot(stateDir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	dir, err := historyOpenDir(root, rec.Local, true)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	if _, err := dir.Lstat(accountObservationHoldFile); err == nil {
		existing, err := readAccountEvidence(dir, accountObservationHoldFile)
		if err != nil || existing.ID != rec.ID {
			return accounts.ErrUnsafePath
		}
		return historySync(dir)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	temporary := ".account-hold-" + newItemID()
	f, err := dir.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Remove(temporary) }()
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = dir.Rename(temporary, accountObservationHoldFile)
	}
	if err != nil {
		return err
	}
	return historySync(dir)
}

func (m *accountRotationManager) drainObservationHolds() error {
	root, err := openAccountRecoveryRoot(m.w.stateDir)
	if err != nil {
		m.w.stateErr = err
		return err
	}
	defer func() { _ = root.Close() }()
	holds := make(map[string]bool)
	for _, meta := range m.w.list() {
		if meta.AccountBinding == nil {
			continue
		}
		if _, err := root.Lstat(meta.ID + "/" + accountObservationHoldFile); errors.Is(err, os.ErrNotExist) {
			continue
		}
		dir, err := historyOpenDir(root, meta.ID, false)
		if err != nil {
			m.w.stateErr = err
			return err
		}
		rec, err := readAccountEvidence(dir, accountObservationHoldFile)
		if err == nil && (rec.Kind != "hold" || rec.Local != meta.ID) {
			err = accounts.ErrUnsafePath
		}
		if err != nil {
			_ = dir.Close()
			m.w.stateErr = err
			return err
		}
		_, proofErr := m.inboxSource(rec)
		if proofErr == nil && (meta.Status.Process != status.ProcessRunning || meta.RosterHidden) {
			err = dir.Remove(accountObservationHoldFile)
			if err == nil {
				err = historySync(dir)
			}
			if err == nil {
				m.inboxMu.Lock()
				delete(m.inboxErrors, meta.ID)
				m.inboxMu.Unlock()
			}
		} else {
			holds[meta.ID] = true
		}
		_ = dir.Close()
		if err != nil {
			m.w.stateErr = err
			return err
		}
	}
	m.inboxMu.Lock()
	m.inboxHolds = holds
	m.inboxMu.Unlock()
	return nil
}

func (m *accountRotationManager) restoreInboxHolds() {
	if m.w.stateErr != nil {
		return
	}
	if err := m.drainObservationHolds(); err != nil {
		return
	}
	m.inboxMu.Lock()
	defer m.inboxMu.Unlock()
	root, err := openAccountInbox(m.w.stateDir, false)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		m.w.stateErr = err
		return
	}
	defer func() { _ = root.Close() }()
	if err = cleanupInboxTemps(root); err != nil {
		m.w.stateErr = err
		return
	}
	entries, err := inboxEntries(root)
	if err != nil {
		m.w.stateErr = err
		return
	}
	if m.inboxModels == nil {
		m.inboxModels = make(map[string]string)
	}
	for _, entry := range entries {
		rec, err := readAccountInbox(root, entry.Name())
		if err != nil {
			m.w.stateErr = err
			return
		}
		if inboxHoldsModel(rec) {
			m.inboxModels[rec.ID] = rec.Local
		}
	}
}

// Authenticated admission fixes the feed provenance. On daemon restart, the
// same persisted instance and shim incarnation can replay even after reconnect.
func (m *accountRotationManager) inboxSource(rec accountInboxRecord) (persist.Meta, error) {
	meta, ok := m.w.get(rec.Local)
	if !ok || meta.AccountBinding == nil || *meta.AccountBinding != rec.Binding || meta.ConversationID != rec.Conversation || meta.ShimPID != rec.ShimPID || meta.ShimStartTime != rec.ShimStartTime {
		return persist.Meta{}, accounts.ErrIneligible
	}
	if rec.Instance != "" {
		if m.d == nil {
			return persist.Meta{}, accounts.ErrIneligible
		}
		instance, ok := m.d.sessionInstance(rec.Local)
		if !ok || instance != rec.Instance {
			return persist.Meta{}, accounts.ErrIneligible
		}
	}
	return meta, nil
}

func (m *accountRotationManager) applyInbox(input accountInboxRecord) error {
	meta, err := m.inboxSource(input)
	if err != nil {
		return err
	}
	w := m.w
	switch input.Kind {
	case "failure":
		if meta.Status.Process != status.ProcessRunning || meta.RosterHidden {
			return nil
		}
		native, contextErr := accountconfig.HasNativeContext(w.stateDir, meta.AccountProjectionRef)
		if contextErr != nil {
			return contextErr
		}
		if native {
			if input.TurnID == "" {
				if err := m.noteModel(meta, "", input.Sequence); err != nil {
					return err
				}
				return m.reportFailure(w, input.Local, input.Class, "", input.ID, input)
			}
			if m.claudeFailureSuperseded(input) {
				return nil
			}
		}
		if native || input.TurnID != "" {
			model, stale, err := m.claudeFailedRequestModel(meta, input)
			if native && !m.claudeFailureAdmitted(input) {
				err = errClaudeFailureModelPending
			}
			if stale {
				return nil
			}
			if err != nil && time.Since(input.ReceivedAt) < 30*time.Second {
				return errClaudeFailureModelPending
			}
			if err != nil {
				model = ""
			}
			if err := m.noteModel(meta, model, input.Sequence); err != nil {
				return err
			}
		}
		if native {
			return m.reportFailure(w, input.Local, input.Class, "", input.ID, input)
		}
		return m.reportFailure(w, input.Local, input.Class, "", input.ID)
	case "model":
		if input.Model == "" && input.Started && meta.CLIIdentity != nil && meta.CLIIdentity.Version == accountconfig.CharacterizedClaudeVersion {
			for _, rec := range w.state.AccountRotations {
				if rec.CandidateID == input.Local && rec.State == accountLaunched && rec.Destination != nil && *meta.AccountBinding == *rec.Destination && rec.ConversationID == input.Conversation && meta.InputEmbargo == rec.Incident.ID && meta.LaunchOptions["model"] == rec.Incident.Model && exactAccountModel(rec.Incident.Model) {
					pinned := meta.AccountClaudeFallback != nil && meta.AccountClaudeFallback.RecoveryPinned
					if !pinned {
						break
					}
					if err := m.noteModel(meta, rec.Incident.Model, input.Sequence); err != nil {
						return err
					}
					rec.ConversationProven, rec.NativeHookSequence = true, input.Sequence
					return m.persist(rec)
				}
			}
		}
		if err := m.noteModel(meta, input.Model, input.Sequence); err != nil {
			return err
		}
		for _, rec := range w.state.AccountRotations {
			if rec.CandidateID == input.Local && rec.State == accountLaunched && rec.Destination != nil && *meta.AccountBinding == *rec.Destination && rec.ConversationID == input.Conversation && input.Model == rec.Incident.Model {
				rec.ConversationProven = true
				rec.NativeHookSequence = input.Sequence
				return m.persist(rec)
			}
		}
	case "claude-turn":
		if input.Started {
			if err := m.persistClaudePrompt(meta, input); err != nil {
				return err
			}
		}
		if input.ClearModel {
			if err := m.noteModel(meta, "", input.Sequence); err != nil {
				return err
			}
		}
		for _, rec := range w.state.AccountRotations {
			if rec.CandidateID != input.Local || !rec.InputReleased || (rec.State != accountCommitted && rec.State != accountUnknown) || input.Sequence <= rec.NativeHookSequence {
				continue
			}
			if input.Started {
				if !input.OwnerPrompt {
					rec.TrialTurnID, rec.TrialHookSequence = "", 0
				} else {
					rec.TrialHookSequence = input.Sequence
					rec.TrialTurnID = "claude:" + fmtUint(input.Sequence)
				}
				return m.persist(rec)
			}
			if input.Completed && input.Sequence > rec.TrialHookSequence && rec.TrialHookSequence > rec.NativeHookSequence {
				if meta.Status.Turn != status.TurnIdle || meta.Status.Interaction != status.InteractionNone || w.sessionUnsafe(meta.ID) {
					return accounts.ErrInUse
				}
				return m.completeTrial(meta.ID, rec.TrialTurnID)
			}
		}
	case "native":
		if input.QuotaFeed != 0 {
			registry, err := m.store.Snapshot()
			if err != nil {
				return err
			}
			scopes := input.Scopes
			if len(scopes) == 0 {
				scopes = []accounts.ScopeObservation{{Scope: accounts.ScopeGlobal, Authority: accounts.AuthorityUnknown}}
			}
			_, err = m.store.Observe(registry.Revision, input.Binding, accounts.Observation{FeedGeneration: input.QuotaFeed, Sequence: input.QuotaSequence, ReceivedAt: input.ReceivedAt, Scopes: scopes})
			if err != nil && !errors.Is(err, accounts.ErrIneligible) {
				return err
			}
		}
		if input.Class != "" {
			if meta.Status.Process != status.ProcessRunning || meta.RosterHidden {
				return nil
			}
			return m.reportFailure(w, input.Local, input.Class, "", input.ID)
		}
		if input.Started {
			return m.beginTrial(input.Local, input.TurnID)
		}
		if input.Completed {
			return m.completeTrial(input.Local, input.TurnID)
		}
	}
	return nil
}
