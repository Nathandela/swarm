package skeleton

// The fact that replaces the lease as a SIGNAL, without pretending to be the presence
// protocol nobody has built (conversation surface, Wave G; agents-tracker-tbpm.9).
//
// WHAT THE LEASE WAS DOING, AND WHY IT CANNOT KEEP DOING IT. Three things read a phone's
// take_control lease: the board's "phone control" marker, a grid-tap optimisation, and
// ADR-010 Amendment 3 C3's gate -- "the supervisor never types into a session someone is
// driving". R1 removes take-control from the product, and composer_send never needed a
// lease in the first place, so after that deletion no phone ever holds one and C3's gate
// answers false for every phone there is. The supervisor would then type its notification
// into a session somebody is actively chatting in.
//
// WHAT THIS IS INSTEAD, STATED AT ITS WEAKEST. The daemon observes messages arriving. It
// does not observe a phone being present: the watch channel is fallback-only, a chat phone
// reads a machine-wide journal, and `foreground_only` is a push-transport class rather than
// a presence fact. An honest presence signal would need begin, renew and end, an expiry,
// binding to a session incarnation, cleanup on transport loss and aggregation across
// devices -- a protocol, not a timestamp. So this records exactly what was seen: THIS PHONE
// SENT SOMETHING, AT THIS INSTANT.
//
// The terminal renders it in those words ("phone sent 09:41"), never as "phone is here",
// because the second names something nobody measured. When the presence protocol is built,
// this is what it replaces.

import "time"

// phoneActiveHorizon is how long a message counts as somebody driving the session.
//
// IT IS A CONVERSATION'S IDLE GAP, not a session's. The gate exists so the supervisor does
// not type into a live exchange; two minutes covers the pause between a question and its
// answer, and expires long before it could mute the supervisor for the life of a session
// that was touched once. A fact with a time on it must age out, or the marker beside it
// ("phone sent 09:41") is claiming something the gate no longer believes.
const phoneActiveHorizon = 2 * time.Minute

// notePhoneActivity records that a remote message reached this session. Called on the
// delivery paths, never on a refusal: a send the machine turned away is not somebody
// driving anything.
func (d *Daemon) notePhoneActivity(local string) {
	d.itemMu.Lock()
	defer d.itemMu.Unlock()
	if d.phoneActivity == nil {
		d.phoneActivity = map[string]time.Time{}
	}
	d.phoneActivity[local] = time.Now()
}

// phoneActivityAt reports when this session last received a remote message, or the zero
// time if it never has. It is what the terminal draws, so it is an INSTANT rather than a
// boolean: "phone sent 09:41" says what happened; "phone here" would not.
func (d *Daemon) phoneActivityAt(local string) time.Time {
	d.itemMu.Lock()
	defer d.itemMu.Unlock()
	return d.phoneActivity[local]
}

// phoneRecentlyActive is C3's half of the same record: within the horizon, somebody is
// driving this session from a phone.
func (d *Daemon) phoneRecentlyActive(local string) bool {
	at := d.phoneActivityAt(local)
	return !at.IsZero() && time.Since(at) < phoneActiveHorizon
}

// forgetPhoneActivity drops a session's record when the session ends, so the map does not
// outlive what it describes.
func (d *Daemon) forgetPhoneActivity(local string) {
	d.itemMu.Lock()
	defer d.itemMu.Unlock()
	delete(d.phoneActivity, local)
}

// setPhoneActivityForTest winds the record to an arbitrary instant. It exists so the
// horizon can be tested without sleeping through it, and it is the only writer besides
// notePhoneActivity.
func (d *Daemon) setPhoneActivityForTest(local string, at time.Time) {
	d.itemMu.Lock()
	defer d.itemMu.Unlock()
	if d.phoneActivity == nil {
		d.phoneActivity = map[string]time.Time{}
	}
	d.phoneActivity[local] = at
}
