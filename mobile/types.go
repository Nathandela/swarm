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
// blob describes); PushGatewayURL is the ADR-015 push gateway this installation registers
// with, empty on a build with none configured -- a phone that is then honestly
// foreground-only rather than one that reports a push path it does not have.
type Config struct {
	StateDir       string
	RelayURL       string
	MachineID      string
	PushGatewayURL string
}

// EventListener receives asynchronous events from the Go core. OnEvent runs on a Go
// goroutine, never on the Android main looper, and MUST NOT block: see the package doc
// for the queue bound and the drop-oldest overflow contract.
type EventListener interface {
	OnEvent(e *Event)
}

// Event is one asynchronous notification. Kind names the family ("journal", "interaction",
// "terminal", "outcome", "connection", "overflow"); Stream names the per-stream staleness plane
// the event belongs to, so the UI can decide whether a view is live. Dropped is non-zero
// only on an "overflow" event, where it counts the events discarded since the previous
// overflow.
//
// An "interaction" event is one transcript item appended or updated (ADR-009). It rides the
// "journal" stream because that is the channel it arrived on and is repaired by (IS-LAYER-4),
// and it carries the item's KIND on Message and its status on State -- a wake that could not
// say whether prose or an approval card landed would force every screen to re-read the whole
// transcript to find out. It is a WAKE and not a delivery: the body is read back through
// App.ReadTranscript, which is the only surface holding the folded item.
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
	// ---------------------------------------------------------------------------
	// WAVE R8 (ADR-017 T1/T2): the routed destination, and the honest header's facts.
	// ---------------------------------------------------------------------------
	//
	// THE DESTINATION IS THE MACHINE'S CHOICE, RESOLVED ON THE PHONE BY READING ONLY.
	// [Destination] is phonecore.RouteSession's answer over the daemon-authored capability
	// record AND the machine's remote profile: "chat", "terminal_fallback" or
	// "status_card" -- three destinations, nothing in between. A session with no record,
	// an INCONSISTENT record (structured_chat and terminal_fallback both true), a record
	// binding no session instance, or a machine whose profile declares no TerminalView
	// version all resolve to the status card, which is the fail-closed answer for every
	// one of them.
	//
	// A string rather than an enum because gomobile binds no Go enum type; the three
	// values are the wire vocabulary and Kotlin switches on them.
	Destination string
	// Provider, ProviderVersion and MissingCapability are playbook:280's honest header:
	// "the provider, the detected version, and the missing capability that cost it
	// structured chat". Without all three the screen says "this is a terminal" and not
	// "this is a terminal BECAUSE this build of this provider does not do X" -- and the
	// second sentence is the whole of what makes three destinations honest rather than
	// arbitrary. An empty ProviderVersion is UNKNOWN, never a guess.
	Provider          string
	ProviderVersion   string
	MissingCapability string
	// SessionInstance is the incarnation every generation and every snapshot binds to
	// (T8-a). An empty instance means the machine bound none, and the router already
	// refused such a record.
	SessionInstance string
	// StructuredChat is whether this session offers the structured COMPOSER --
	// phonecore.ComposerAvailable, which is RouteSession's chat arm and therefore already
	// carries every fail-closed rule: an untrusted profile, an inconsistent record and a
	// record binding no instance all answer false. It is deliberately NOT the record's raw
	// boolean, so no screen can offer a composer over a record the router refused.
	//
	// TerminalControl is read straight off the record and never inferred from the
	// destination: a session degraded INTO the fallback by a proven structured gap may
	// watch and may not control (T6-b).
	StructuredChat  bool
	TerminalControl bool
	// StateSinceUnixMs is the MACHINE's stamp of when the session entered its current state
	// (persist.Meta.EffectiveGroupEnteredAt(), carried on the roster and the journalworthy
	// transitions), in Unix milliseconds so Kotlin can age a row from it: the age is time IN
	// the state the row shows, not time since launch. 0 is ABSENT: no record has carried a
	// stamp for this session, and Kotlin draws no age for 0 -- never the epoch
	// (phone-refit-playbook W7.1).
	StateSinceUnixMs int64
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
	// TSUnixMs is the daemon's own stamp of when the record was appended, in Unix
	// milliseconds; 0 is absent (a daemon predating the wire field), and Kotlin draws no
	// time for 0 -- the cursor beside it is a sequence number and is never shown as one
	// (phone-refit-playbook W7.4).
	TSUnixMs int64
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

// TranscriptItem is one interaction item as the app sees it: one durable, replayable unit of
// the conversation -- a message, a tool run, a file change, an approval, a plan revision, a
// status marker (ADR-009, docs/specifications/interaction-schema.md §3).
//
// Text is the RECONSTRUCTED body, folded by the Go core: a streamed agent_message arrives as
// increments and the core concatenates them in cursor order (IS-DELTA-1), so a client that
// read `text` out of Body would render the last increment as the whole message.
//
// Body is the item object VERBATIM as JSON, and the per-kind fields of §3 are read from it by
// the client. That is a gomobile constraint before it is a design choice -- there is no bound
// map and no variant type, the same limit that makes Snapshot.Text a joined string rather than
// a []string -- and it is what makes an unknown kind or an unknown field free on this boundary
// (IS-COMPAT-1/-2): a build that has never heard of a field still carries it across intact.
//
// Cursor is the item's position in the transcript: the journal cursor of its FIRST record
// (IS-LAYER-3). Degraded marks an item from a NEWER item schema than the Go core understands,
// rendered as far as it does understand it rather than dropped (IS-COMPAT-4). Resolved is set
// on an approval_request whose approval_resolved has landed (IS-LIFE-2), which is what
// dismisses a stale card -- including one answered at the machine.
type TranscriptItem struct {
	SessionID string
	ItemID    string
	Cursor    int64
	Kind      string
	Status    string
	TurnID    string
	TSUnixMs  int64
	Text      string
	Body      string
	Truncated bool
	Detail    bool
	Degraded  bool
	Resolved  bool
	// ToolKind is a tool_run's flat glyph vocabulary (Mirror M2.2, interaction-schema.md
	// §3.3 `tool_kind`): the Kotlin ToolCard picks its glyph from this ONE field and never
	// parses Body (IS-TOOL-1's posture at this boundary). Empty where the wire carried none.
	ToolKind string
	// Source is a user_message's honest phone-vs-terminal attribution (Mirror M2.4):
	// "phone" only when the daemon watched its own injection, "owner" for a prompt typed at
	// the machine, empty where the wire carried none -- never invented.
	Source string
	// OperationID names WHICH of this phone's sends the agent echoed back (owner ruling
	// R6). A message drawn on the phone stays PENDING until its own id comes back on an
	// item, because a send is acknowledged when the daemon wrote bytes into a PTY and not
	// when the CLI accepted them -- so the echo, and only the echo, is the delivery.
	// Empty on every item nobody claimed.
	OperationID string
}

// TranscriptPage is a transcript HANDLE, for the same reason as SessionList: gomobile has no
// bound list type, so a collection crosses as an opaque object with Count/At.
type TranscriptPage struct {
	items []TranscriptItem
	next  int64
	stale bool
}

// Count is the number of items in the page.
func (p *TranscriptPage) Count() (n int, err error) {
	defer barrier(&err)
	if p == nil {
		return 0, errNoReceiver
	}
	return len(p.items), nil
}

// At returns the item at index i.
func (p *TranscriptPage) At(i int) (it *TranscriptItem, err error) {
	defer barrier(&err)
	if p == nil {
		return nil, errNoReceiver
	}
	if i < 0 || i >= len(p.items) {
		return nil, classed(ErrClassNotFound,
			fmt.Errorf("swarmmobile: transcript index %d out of range [0,%d)", i, len(p.items)))
	}
	item := p.items[i]
	return &item, nil
}

// NextCursor is the cursor the next ReadTranscript should resume from.
func (p *TranscriptPage) NextCursor() (c int64, err error) {
	defer barrier(&err)
	if p == nil {
		return 0, errNoReceiver
	}
	return p.next, nil
}

// Stale reports that the journal stream this transcript was folded from has an unrepaired
// hole (PB-APP-8), so the conversation is not a complete record of what the agent did.
//
// It rides ON THE HANDLE for SessionList.Stale's reason, and the case is stronger here: a
// transcript reads as a complete conversation, and a missing tool run or approval in the
// middle of one is invisible.
func (p *TranscriptPage) Stale() (stale bool, err error) {
	defer barrier(&err)
	if p == nil {
		return false, errNoReceiver
	}
	return p.stale, nil
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

	// SessionInstance is WHICH INCARNATION this screen belongs to (ADR-017 T8-a), and
	// RenderedAtMillis is the MACHINE's own render time as Unix milliseconds UTC.
	//
	// THEY EXIST SO A SCREEN CAN SAY IT IS STALE. `TerminalGrid.ageMs` was hardcoded to zero
	// because no machine-authored render time was on the wire, so the fallback presented an
	// arbitrarily old grid as current -- and the compensating flag, `Stale`, is a
	// sequence-gap signal that by construction does not fire when a machine simply STOPS
	// SENDING. A screen that silently freezes is the worst failure mode this surface has,
	// because the user acts on what they see.
	//
	// Milliseconds and not a time: gomobile binds no time.Time. Zero means the machine sent
	// none -- a build that predates the closing round -- and a zero must be rendered as
	// "unknown", never as "rendered just now".
	SessionInstance  string
	RenderedAtMillis int64
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
//
// Paired is the one field here that is a PRODUCT FACT rather than a restored coordinate, and it
// is here because this is the type the screens already read. Every other field describes what the
// pairing pinned; Paired says whether that pairing is still one. See App.StateSummary for what it
// is derived from and why the machine NAME was the wrong thing to infer it from.
type StateSummary struct {
	Machine        string
	EpochID        int64
	SendSeq        int64
	RelayCursor    int64
	RosterRevision int64
	PendingOps     int
	Restored       bool
	Reconciled     bool
	Paired         bool
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

// MachinePresence is the relay's last reported reachability for the machine, and when this
// phone took that reading (PB-APP-5, bead agents-tracker-xtj).
//
// State is the relay's own vocabulary -- online, offline, unknown -- with unknown covering
// both "the relay has no live record" and "this phone has no connection through which to
// ask". A screen renders one word either way, because in both cases nobody can currently
// vouch for the machine.
//
// ObservedUnixMs is what makes the reading judgeable rather than merely available: this is a
// CACHED opinion, so its age is part of the answer, and 0 means nothing has been observed yet
// rather than that the reading is from the epoch.
type MachinePresence struct {
	State          string
	ObservedUnixMs int64
}

// QRPayload is the DISPLAYABLE content of a scanned pairing QR: enough to show the user
// where the phone is about to connect (PB-PAIR-6), and deliberately not the pairing
// secret, which never leaves the Go core.
type QRPayload struct {
	RelayURL     string
	RendezvousID string
	HasStaticPub bool
}
