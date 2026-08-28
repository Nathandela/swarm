package claude

import (
	"encoding/json"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
)

// Claude Code keeps one file per RUNNING process at ~/.claude/sessions/<pid>.json.
// Measured on 2.1.250 (2026-08-28): it carries sessionId, cwd, status, the session's
// current display name (`name`), where it came from (`nameSource`: user, peer, auto,
// derived, collision or hook) and `nameSince`, the epoch-millisecond the name was set.
// The name is what the prompt box shows and what `/rename` changes; `--name` at launch
// sets it too, which is how a swarm-launched session starts out named like its swarm
// row. Every source is treated alike here: whatever Claude shows IS the name.
//
// The registry is keyed by pid, not session, so the assembly reads every file and
// this parser answers whether one of them is ours. Files are a few hundred bytes;
// anything past maxLiveNameFileBytes is not a registry file.
const (
	liveNameDir          = ".claude/sessions"
	maxLiveNameFileBytes = 64 << 10
)

// LiveNameDir makes the claude adapter an adapter.LiveNameSource.
func (claudeAdapter) LiveNameDir() string { return liveNameDir }

// LiveNameFromFile parses one registry file; ok iff it names conversationID's session
// and carries a non-empty name with a positive nameSince.
func (claudeAdapter) LiveNameFromFile(raw []byte, conversationID string) (adapter.LiveName, bool) {
	if len(raw) == 0 || len(raw) > maxLiveNameFileBytes || conversationID == "" {
		return adapter.LiveName{}, false
	}
	var rec struct {
		SessionID string `json:"sessionId"`
		Name      string `json:"name"`
		NameSince int64  `json:"nameSince"`
	}
	if json.Unmarshal(raw, &rec) != nil || rec.SessionID != conversationID || rec.Name == "" || rec.NameSince <= 0 {
		return adapter.LiveName{}, false
	}
	return adapter.LiveName{Name: rec.Name, Since: time.UnixMilli(rec.NameSince)}, true
}
