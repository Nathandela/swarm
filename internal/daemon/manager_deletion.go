package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/Nathandela/swarm/internal/journal"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

const managerDeletionDirectory = ".manager-deletions"

var managerDeletionJournalMarker = json.RawMessage(`{"exact_manager_deletion":1}`)

type managerDeletionWitness struct {
	Version          int    `json:"version"`
	Session          string `json:"session"`
	SessionInstance  string `json:"session_instance"`
	ConversationID   string `json:"conversation_id"`
	Provider         string `json:"provider"`
	NativeSessionID  string `json:"native_session_id"`
	NativeGeneration string `json:"native_generation"`
}

func managerDeletionFor(b schema.ManagerRuntimeBinding) managerDeletionWitness {
	return managerDeletionWitness{1, b.Session, b.SessionInstance, b.ConversationID, b.Provider, b.NativeSessionID, b.NativeGeneration}
}

func (d *Daemon) recordManagerDeletion(m persist.Meta, instance string) error {
	generation := ""
	if d.cfg.ManagerDeletionGeneration != nil && m.ConversationID != "" && validJournalInstance(instance) {
		observed := d.cfg.ManagerDeletionGeneration(m)
		if decoded, err := hex.DecodeString(observed); err == nil && len(decoded) == 16 {
			generation = observed
		}
	}
	// Retain a negative witness, too. A later deletion of a reused local ID
	// must not leave an earlier incarnation's positive witness authoritative.
	rec := managerDeletionWitness{1, m.ID, instance, m.ManagerConversationID, m.AgentType, m.ConversationID, generation}
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	dir := filepath.Join(d.cfg.StateDir, managerDeletionDirectory)
	if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	root, err := openManagerDeletionDirectory(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	tmp := ".manager-deletion-" + rand.Text()
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = root.Remove(tmp) }()
	_, werr := f.Write(raw)
	if err = errors.Join(werr, f.Sync(), f.Close()); err != nil {
		return err
	}
	if err = root.Rename(tmp, m.ID+".json"); err != nil {
		return err
	}
	return syncLaunchDirectories(dir, d.cfg.StateDir)
}

func openManagerDeletionDirectory(dir string) (*os.Root, error) {
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || !isPrivateDeletionFile(info, 0700) {
		return nil, errors.New("invalid manager deletion receipt directory")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		_ = root.Close()
		return nil, errors.New("manager deletion receipt directory changed")
	}
	return root, nil
}

func isPrivateDeletionFile(info os.FileInfo, mode os.FileMode) bool {
	owner, ok := info.Sys().(*syscall.Stat_t)
	return ok && owner.Uid == uint32(os.Getuid()) && info.Mode().Perm() == mode &&
		(info.IsDir() || owner.Nlink == 1)
}

// ManagerDeletionStatus proves only explicit removal of the requested owner.
// Legacy retained history is admissible only for a previously parked manager.
// Neither receipt settles native input, worker effects, or operation outcomes.
func (d *Daemon) ManagerDeletionStatus(binding schema.ManagerRuntimeBinding, allowParkedLegacy bool) string {
	if binding.Validate() != nil || !persist.ValidID(binding.Session) || !validJournalInstance(binding.SessionInstance) ||
		!schema.IsDiscordManager("manager", binding.ConversationID) {
		return ""
	}
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	if _, found := d.Get(binding.Session); found {
		return ""
	}
	if _, err := os.Lstat(d.sessionDir(binding.Session)); !errors.Is(err, os.ErrNotExist) {
		return ""
	}

	witness, err := d.readManagerDeletion(binding.Session)
	if err == nil {
		if witness == managerDeletionFor(binding) {
			return schema.ManagerRetireDeleted
		}
		return ""
	}
	if !allowParkedLegacy || !errors.Is(err, os.ErrNotExist) || d.journal == nil {
		return ""
	}
	retained, err := d.journal.ReadFrom(0)
	if err == nil && legacyManagerDeletion(retained.Events, binding) {
		return schema.ManagerRetireDeletedParked
	}
	return ""
}

func (d *Daemon) readManagerDeletion(id string) (managerDeletionWitness, error) {
	var witness managerDeletionWitness
	root, err := openManagerDeletionDirectory(filepath.Join(d.cfg.StateDir, managerDeletionDirectory))
	if err != nil {
		return witness, err
	}
	defer func() { _ = root.Close() }()
	f, err := root.OpenFile(id+".json", os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return witness, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !isPrivateDeletionFile(info, 0600) || info.Size() > 4096 {
		return witness, errors.New("invalid manager deletion receipt")
	}
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(data) > 4096 {
		return witness, errors.New("unreadable manager deletion receipt")
	}
	err = json.Unmarshal(data, &witness)
	return witness, err
}

// shortcut: pre-existing deletion events have no native metadata. Require both
// exact retained exit and explicit removal until those older installations age out.
func legacyManagerDeletion(events []journal.Record, b schema.ManagerRuntimeBinding) bool {
	deleted := false
	for i := len(events) - 1; i >= 0; i-- {
		r := events[i]
		if r.SessionID != b.Session {
			continue
		}
		switch r.Type {
		case journal.TypeDeleted:
			if len(r.Payload) != 0 || r.Role != "manager" || r.SessionInstance != b.SessionInstance {
				return false
			}
			deleted = true
		case journal.TypeLaunched, journal.TypeLost:
			// Reuse, ambiguity, or a later surviving row cannot prove deletion.
			return false
		case journal.TypeExited, journal.TypeSessionState, journal.TypeGroupTransition, journal.TypeCapabilityTransition:
			if !deleted || (r.SessionInstance != "" && r.SessionInstance != b.SessionInstance) ||
				(r.Role != "" && r.Role != "manager") || (r.Agent != "" && r.Agent != b.Provider) {
				return false
			}
			if r.Type == journal.TypeExited {
				return r.SessionInstance == b.SessionInstance && r.Role == "manager" && r.Agent == b.Provider
			}
		}
	}
	return false
}
