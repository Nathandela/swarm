package daemon

import (
	"encoding/json"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
	"unicode"

	"github.com/Nathandela/swarm/internal/persist"
)

func (d *Daemon) JournalSourceID() string { return d.journal.SourceID() }

func validJournalInstance(value string) bool {
	return value != "" && len(value) <= 256 && !strings.ContainsFunc(value, unicode.IsSpace) && !strings.ContainsFunc(value, unicode.IsControl)
}

// The side-file's incarnation tuple must match the meta being published. Bare
// legacy instances carry no such proof and remain unknown in ordered history.
func (d *Daemon) recordedJournalInstance(meta persist.Meta) string {
	if meta.ShimPID <= 0 || meta.ShimStartTime <= 0 {
		return ""
	}
	root, err := os.OpenRoot(d.sessionDir(meta.ID))
	if err != nil {
		return ""
	}
	defer func() { _ = root.Close() }()
	f, err := root.OpenFile("session-instance", os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 1024 {
		return ""
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Getuid()) || owner.Nlink != 1 {
		return ""
	}
	raw, err := io.ReadAll(io.LimitReader(f, 1025))
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(raw))
	if len(fields) != 3 || !validJournalInstance(fields[0]) {
		return ""
	}
	pid, pe := strconv.Atoi(fields[1])
	start, se := strconv.ParseInt(fields[2], 10, 64)
	if pe != nil || se != nil || pid != meta.ShimPID || start != meta.ShimStartTime {
		return ""
	}
	return fields[0]
}
func payloadJournalInstance(payload []byte) string {
	var rec struct {
		SessionInstance string `json:"session_instance"`
	}
	if json.Unmarshal(payload, &rec) != nil || !validJournalInstance(rec.SessionInstance) {
		return ""
	}
	return rec.SessionInstance
}

func (d *Daemon) verifiedJournalPayloadIdentity(session, instance string) (string, string) {
	if !validJournalInstance(instance) {
		return "", ""
	}
	meta, ok := d.Get(session)
	if !ok || d.recordedJournalInstance(meta) != instance {
		return "", ""
	}
	return instance, meta.Role
}
