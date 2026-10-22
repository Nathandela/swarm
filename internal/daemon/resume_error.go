package daemon

import (
	"fmt"
	"github.com/Nathandela/swarm/internal/persist"
)

// RecordResumeError keeps a failed exact continuation visible after its provider
// switch journal completes. It uses the same serialized metadata writer as status.
func (d *Daemon) RecordResumeError(id, reason string) error {
	if len(reason) > 4096 {
		reason = reason[:4096]
	}
	d.writeMu.Lock()
	d.mu.Lock()
	sess, ok := d.sessions[id]
	var meta persist.Meta
	if ok {
		meta = sess.meta
	}
	d.mu.Unlock()
	if !ok {
		d.writeMu.Unlock()
		return fmt.Errorf("daemon: unknown session %q", id)
	}
	if meta.ResumeError == reason {
		d.writeMu.Unlock()
		return nil
	}
	meta.ResumeError = reason
	written, err := d.saveMetaLocked(&meta)
	d.writeMu.Unlock()
	if err != nil || !written {
		return err
	}
	if d.cfg.onMetaSave != nil {
		d.cfg.onMetaSave(meta)
	}
	return nil
}
