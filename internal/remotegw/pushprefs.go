package remotegw

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/Nathandela/swarm/internal/protocol"
)

// PushPrefs is the machine-authoritative push preference (PB-PUSH-8). It is the wire type
// verbatim, not a copy: the phone seals exactly this record into a push_prefs command and
// the gateway stores exactly what it was sent, so a converter between two identical shapes
// would only be somewhere for the two to drift apart.
type PushPrefs = protocol.PushPrefs

// PushPrefsSource is the gateway's custody of that record: the notifier READS it before
// every wake, the command bridge WRITES it when the daemon authorizes a change. Both hold
// the SAME object in a live gateway, so a preference the user just set takes effect on the
// next transition rather than at the next restart.
type PushPrefsSource interface {
	// LoadPrefs returns the current preference. A never-configured machine yields the
	// bootstrap default (both categories enabled, Version 0) and a nil error. An
	// unreadable record yields the ZERO value -- both categories OFF -- alongside its
	// error, so a caller that logs and continues suppresses rather than resumes.
	LoadPrefs() (PushPrefs, error)
	// SavePrefs stores p, refusing any version that does not strictly exceed the stored
	// one.
	SavePrefs(p PushPrefs) error
}

// bootstrapPrefs is the preference of a machine on which the user has never expressed one.
//
// It is ENABLED, and that is deliberately the OPPOSITE direction from the corrupt-record
// case below. The two are different objects: a machine with no record has nothing to
// contradict, and push is the sole background wake path (ADR-007 B16), so shipping it off
// by default would make the product inert out of the box for every new install. Version 0
// is reserved for it so the phone's first real update always wins.
var bootstrapPrefs = PushPrefs{Version: 0, NeedsInput: true, Finished: true}

// errStalePrefsVersion refuses a preference update that does not advance the version.
var errStalePrefsVersion = errors.New("remotegw: push preference version is not newer than the stored one")

// filePushPrefs is the durable custody: one small JSON record beside the gateway's other
// state, rewritten atomically.
type filePushPrefs struct {
	mu   sync.Mutex
	path string
}

var _ PushPrefsSource = (*filePushPrefs)(nil)

// OpenPushPrefs opens the durable push-preference record at path. It does NOT read or
// validate the file here: LoadPrefs re-reads on every call, because the command bridge
// rewrites this record while the notifier is reading it and a value cached at open would
// leave the sender acting on a preference the user has already changed.
func OpenPushPrefs(path string) (PushPrefsSource, error) {
	if path == "" {
		return nil, errors.New("remotegw: push preference custody requires a path")
	}
	return &filePushPrefs{path: path}, nil
}

// LoadPrefs reads the record.
//
// A MISSING file is the bootstrap default (enabled). A file that EXISTS but cannot be read
// or parsed is a different thing entirely and is answered the other way: the zero value
// (both categories off) plus the error. That record may well have said "off", and resuming
// pushes against a settings screen that says otherwise would leak token, timing and size
// while the user believes they are silenced -- and would do it with nothing failing
// anywhere. Returning the enabled default alongside an error would be the same defect with
// an error nobody has to act on.
func (f *filePushPrefs) LoadPrefs() (PushPrefs, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.loadLocked()
}

func (f *filePushPrefs) loadLocked() (PushPrefs, error) {
	raw, err := os.ReadFile(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return bootstrapPrefs, nil
	}
	if err != nil {
		return PushPrefs{}, fmt.Errorf("read push preference %s: %w", f.path, err)
	}
	var p PushPrefs
	if err := json.Unmarshal(raw, &p); err != nil {
		return PushPrefs{}, fmt.Errorf("parse push preference %s: %w", f.path, err)
	}
	return p, nil
}

// SavePrefs stores p, refusing anything that does not STRICTLY advance the version.
//
// The relay is the declared adversary and may reorder or replay what it retains, so a
// preference frame captured before the user turned pushes off must not turn them back on.
// Equality is refused too: a replay of the current record is exactly what an attacker
// would send to pin the preference in place.
//
// A stored record that cannot be READ blocks every write, which is the fail-closed
// direction: an unreadable file whose version is unknown could be newer than p, so
// accepting p might be the rollback this guard exists to refuse.
func (f *filePushPrefs) SavePrefs(p PushPrefs) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	current, err := f.loadLocked()
	if err != nil {
		return err
	}
	if p.Version <= current.Version {
		return fmt.Errorf("%w: %d is not above %d", errStalePrefsVersion, p.Version, current.Version)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return writeFileAtomic(f.path, ".push-prefs-*", raw)
}
