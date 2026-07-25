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
var errNoReceiver = errors.New("swarmmobile: unusable receiver (nil or never constructed by this package)")

// barrier is the panic barrier every exported entry point installs as its FIRST
// statement (PB-BIND-5). A Go panic that reaches the JNI frame kills the app process --
// there is no Java frame to catch it -- so it is converted into the entry point's error
// result, which is why every entry point has one.
func barrier(err *error) {
	if r := recover(); r != nil {
		*err = fmt.Errorf("swarmmobile: recovered panic at the JNI boundary: %v", r)
	}
}

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

// Session is one row of the roster. Group is VERBATIM from the wire (the phone never
// derives a status group on-device); Need is the verbatim journal record type that last
// touched the session; Title is the display name derived from the namespaced id.
type Session struct {
	ID      string
	Title   string
	Group   string
	Need    string
	Present bool
}

// SessionList is a roster HANDLE. gomobile has no bound list type, so a collection
// crosses as an opaque object with Count/At rather than as a slice.
type SessionList struct {
	items []Session
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
		return nil, fmt.Errorf("swarmmobile: session index %d out of range [0,%d)", i, len(l.items))
	}
	item := l.items[i]
	return &item, nil
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
		return nil, fmt.Errorf("swarmmobile: journal index %d out of range [0,%d)", i, len(p.items))
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
type LaunchSpec struct {
	Agent   string
	Cwd     string
	Prompt  string
	Options string
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

// QRPayload is the DISPLAYABLE content of a scanned pairing QR: enough to show the user
// where the phone is about to connect (PB-PAIR-6), and deliberately not the pairing
// secret, which never leaves the Go core.
type QRPayload struct {
	RelayURL     string
	RendezvousID string
	HasStaticPub bool
}
