package swarmmobile

// The value types and collection handles the bound surface carries (PB-BIND-4): every
// one of them is declared HERE, so nothing crossing JNI is a cross-package type. That is
// both a bind constraint (crypto.KeyStore, protocol.Control, status.Group and time.Time
// are all unbindable) and a custody constraint -- a cross-package type is how key
// material reaches the boundary by accident.

import (
	"errors"
	"fmt"
)

// errNoReceiver is what every entry point returns for a receiver the app cannot legally
// have obtained from this package: a nil proxy whose Go object is gone, or a zero value
// that never went through NewApp. It is an ERROR rather than a panic because a panic
// crossing JNI aborts the app process.
var errNoReceiver = classed(ErrClassInternal,
	errors.New("swarmmobile: unusable receiver (nil or never constructed by this package)"))

// Config assembles an App. StateDir is the phone's private state directory (device keys
// plus the one durable state blob); RelayURL is the relay to dial; MachineID is the
// machine endpoint id whose durable coordinates this app resumes ("" adopts whatever the
// blob describes).
type Config struct {
	StateDir  string
	RelayURL  string
	MachineID string
}

// EventListener receives asynchronous events from the Go core. OnEvent runs on a Go
// goroutine, never on the Android main looper, and MUST NOT block: see the package doc
// for the queue bound and the drop-oldest overflow contract.
type EventListener interface {
	OnEvent(e *Event)
}

// Event is one asynchronous notification. Kind names the family ("journal", "terminal",
// "outcome", "connection", "overflow"); Stream names the per-stream staleness plane the
// event belongs to, so the UI can decide whether a view is live. Dropped is non-zero
// only on an "overflow" event, where it counts the events discarded since the previous
// overflow.
type Event struct {
	Kind      string
	Stream    string
	SessionID string
	State     string
	Message   string
	Cursor    int64
	Dropped   int
}

// Session is one row of the roster. Group and Agent are VERBATIM from the wire (the phone
// never derives a status group or an agent on-device); Need is the verbatim journal record
// type that last touched the session; Title is the display name derived from the
// namespaced id.
type Session struct {
	ID    string
	Title string
	Group string
	// Agent is the agent identity the machine reported for this session, verbatim from
	// the wire. Unlike Title it is never derived on-device: an empty Agent means the
	// session's records carried none.
	Agent   string
	Need    string
	Present bool
}

// SessionList is a roster HANDLE. gomobile has no bound list type, so a collection
// crosses as an opaque object with Count/At rather than as a slice.
type SessionList struct {
	items []Session
	stale bool
}

// Count is the number of sessions in the list.
func (l *SessionList) Count() (n int, err error) {
	defer barrier(&err)
	if l == nil {
		return 0, errNoReceiver
	}
	return len(l.items), nil
}

// At returns the session at index i.
func (l *SessionList) At(i int) (s *Session, err error) {
	defer barrier(&err)
	if l == nil {
		return nil, errNoReceiver
	}
	if i < 0 || i >= len(l.items) {
		return nil, classed(ErrClassNotFound,
			fmt.Errorf("swarmmobile: session index %d out of range [0,%d)", i, len(l.items)))
	}
	item := l.items[i]
	return &item, nil
}

// Stale reports that the JOURNAL stream this roster was rendered from has an unrepaired
// hole, so the view may be missing a session, an exit or a needs_input (PB-APP-8).
//
// It rides ON THE HANDLE rather than being left to the caller, and that is the requirement
// rather than a convenience. A screen that has to remember to call StreamState("journal")
// beside every Roster() is a screen that will forget once, and the failure is silent and
// looks exactly like a working app. Snapshot.Stale set the precedent for the terminal model;
// PB-APP-2's triage inbox is the FIRST screen the user opens and the one they act on, so it
// is the last place a known hole may be presented as live.
func (l *SessionList) Stale() (stale bool, err error) {
	defer barrier(&err)
	if l == nil {
		return false, errNoReceiver
	}
	return l.stale, nil
}

// UndeliveredInput is one unit of input the phone took from the user, acknowledged on
// screen, and could not deliver (PB-INPUT-1). Input is live-only, so the resolution is never
// a retry: it is "delivery unknown / not sent", and the whole point of the record is that
// the user is TOLD rather than left believing they typed it. Bytes is what was lost (0 for a
// resize); AtMillis is the unix-millisecond instant it was resolved.
type UndeliveredInput struct {
	SessionID string
	Bytes     int
	Reason    string
	AtMillis  int64
}

// UndeliveredList is an undelivered-input HANDLE, for the same reason as SessionList:
// gomobile has no bound list type, so a collection crosses as an opaque object.
type UndeliveredList struct {
	items   []UndeliveredInput
	dropped int
}

// Count is the number of undelivered entries.
func (l *UndeliveredList) Count() (n int, err error) {
	defer barrier(&err)
	if l == nil {
		return 0, errNoReceiver
	}
	return len(l.items), nil
}

// At returns the undelivered entry at index i.
func (l *UndeliveredList) At(i int) (u *UndeliveredInput, err error) {
	defer barrier(&err)
	if l == nil {
		return nil, errNoReceiver
	}
	if i < 0 || i >= len(l.items) {
		return nil, classed(ErrClassNotFound,
			fmt.Errorf("swarmmobile: undelivered index %d out of range [0,%d)", i, len(l.items)))
	}
	item := l.items[i]
	return &item, nil
}

// Dropped is how many OLDER entries the ledger's bound discarded (PB-INPUT-1).
//
// A bound that discarded silently would be a second defect wearing the first one's clothes:
// the user is told about the last N keystrokes they lost and never told there were thousands,
// which understates the failure at exactly the moment it is worst. Event.Dropped is the same
// contract one plane over -- the callback queue counts what its overflow discarded rather
// than dropping quietly.
func (l *UndeliveredList) Dropped() (n int, err error) {
	defer barrier(&err)
	if l == nil {
		return 0, errNoReceiver
	}
	return l.dropped, nil
}

// JournalEntry is one journal record as the app sees it. Group and Type are verbatim
// from the wire.
type JournalEntry struct {
	Cursor    int64
	SessionID string
	Type      string
	Group     string
}

// JournalPage is a journal page HANDLE, for the same reason as SessionList.
type JournalPage struct {
	items []JournalEntry
	next  int64
	stale bool
}

// Stale reports that the journal stream this page was read from has an unrepaired hole
// (PB-APP-8), so the page is not a complete history of what the agent did.
//
// ReadJournal serves PB-APP-3's session detail AND PB-APP-5's activity log, and both render
// as a chronology -- a shape that reads as complete unless it says otherwise.
func (p *JournalPage) Stale() (stale bool, err error) {
	defer barrier(&err)
	if p == nil {
		return false, errNoReceiver
	}
	return p.stale, nil
}

// Count is the number of entries in the page.
func (p *JournalPage) Count() (n int, err error) {
	defer barrier(&err)
	if p == nil {
		return 0, errNoReceiver
	}
	return len(p.items), nil
}

// At returns the entry at index i.
func (p *JournalPage) At(i int) (e *JournalEntry, err error) {
	defer barrier(&err)
	if p == nil {
		return nil, errNoReceiver
	}
	if i < 0 || i >= len(p.items) {
		return nil, classed(ErrClassNotFound,
			fmt.Errorf("swarmmobile: journal index %d out of range [0,%d)", i, len(p.items)))
	}
	item := p.items[i]
	return &item, nil
}

// NextCursor is the cursor the next ReadJournal should resume from.
func (p *JournalPage) NextCursor() (c int64, err error) {
	defer barrier(&err)
	if p == nil {
		return 0, errNoReceiver
	}
	return p.next, nil
}

// Snapshot is one session's server-rendered terminal grid. Text is the daemon-sanitized
// plain text joined by newlines: there is no VT emulator on the device (ADR-007 D2), and
// a []string is not bindable. Stale reports that the stream the grid arrived on has an
// unrepaired hole, so the view must not be presented as live.
type Snapshot struct {
	SessionID string
	Text      string
	Cols      int
	Rows      int
	Stale     bool
}

// LaunchSpec is a remote launch request. Options is a comma-separated list of key=value
// pairs ("model=opus,thinking=on"); an empty string means no options. It is a string
// rather than a map because gomobile binds no map type.
//
// Cols/Rows are the terminal grid the new session's PTY is opened at. They are REQUIRED by
// the machine -- the daemon refuses a launch with fewer than one column -- and they are the
// phone's to supply because only the phone knows the size of the view the user will watch
// the session in. Left at zero they default (defaultLaunchCols x defaultLaunchRows): the
// Android launch sheet has no terminal view to measure before the session exists, and a
// refused launch is a worse answer than a conventional grid the user can resize.
type LaunchSpec struct {
	Agent   string
	Cwd     string
	Prompt  string
	Options string
	Cols    int
	Rows    int
}

// Op identifies one mutating operation the app authored. OperationID is the durable
// idempotency key its outcome is attributed by (PB-SYNC-2); nothing is resolved by
// proximity.
type Op struct {
	Action      string
	SessionID   string
	OperationID string
}

// Outcome is the verdict on one operation, claimed BY OPERATION ID. Resolved is false
// while the machine has not answered; Code is the machine-readable refusal taxonomy
// (policy / kill_switch / rate_limit / ...) or the reply op when there is no refusal.
type Outcome struct {
	OperationID string
	Code        string
	Message     string
	Resolved    bool
}

// PushPreference is PB-APP-7's pair of coarse notification toggles.
type PushPreference struct {
	Alerts   bool
	Mentions bool
}

// StateSummary is what a restart actually restored. It exists so a test -- and the debug
// screen -- can observe that the phone resumed its durable coordinates rather than
// silently starting from zero.
type StateSummary struct {
	Machine     string
	EpochID     int64
	SendSeq     int64
	RelayCursor int64
	PendingOps  int
	Restored    bool
	Reconciled  bool
}

// Freshness is PB-APP-11's verdict: how recently the MACHINE last spoke, and whether what
// the phone is holding may still be presented as live.
//
// IT IS A SEPARATE FACT FROM StreamState, not a third value of it. A stream is stale because
// it has a hole; the phone is silent because nothing has arrived at all -- and the second
// carries a TIME the screen has to render ("not heard from your machine since 14:32"),
// which an enum cannot. Both are true at once often enough that collapsing them would lose
// one, and it is the silence half that a withholding relay produces while every other
// signal on the handset reads healthy.
//
// LastHeardUnixMs is the machine's OWN authenticated stamp, not the phone's arrival time:
// it is what the machine said about itself, and the one clock in this system the relay
// cannot move forward. Zero means this phone has never heard from its machine -- a first
// launch, or a restore that has not yet taken a frame -- which is Silent, and honestly so.
type Freshness struct {
	Silent          bool
	LastHeardUnixMs int64
}

// QRPayload is the DISPLAYABLE content of a scanned pairing QR: enough to show the user
// where the phone is about to connect (PB-PAIR-6), and deliberately not the pairing
// secret, which never leaves the Go core.
type QRPayload struct {
	RelayURL     string
	RendezvousID string
	HasStaticPub bool
}
