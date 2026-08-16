package skeleton

// HookDrainer is the daemon-side half of playbook §6.1's structured-capture survival
// boundary: it dials a session's shim-owned hook socket (internal/shim's HookDrainTag
// wire), drains everything past its persisted fold cursor, applies each record through
// ingestHookBytes IN ORDER, and durably persists the advanced cursor after each one --
// so a crash mid-batch loses at most the redelivery of the record in flight, which
// ingestHookBytes's own per-session cb.Sequence dedup (hookSeqDuplicate, below --
// NOT engine.HandleCallback's dimension-scoped replay guard, which an unmapped
// event bypasses entirely) then makes a safe no-op, never a duplicate journal item.
//
// On a reported gap it applies every record BELOW the boundary exactly as above and
// never a record at or past it (never silently bridging the hole, ADR-017), then
// emits a structured_gap boundary (internal/daemon) and degrades the session's
// stored capability record (ADR-017 T2 rule 2) one-way -- durably, at most once per
// PROVEN boundary rather than once per daemon incarnation -- and returns without
// ever advancing the cursor past the boundary or retrying on its own: a caller
// decides what, if anything, comes next.
//
// WHY IT LIVES HERE: exactly interaction.go's own reason (its header comment). skeleton
// is the one package that already imports internal/daemon, internal/adapter,
// internal/protocol AND internal/shim, so the drain is assembled here, beside the
// producer (serveHookInteractions) it feeds.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/hookclient"
	"github.com/Nathandela/swarm/internal/shim"
)

// ErrHookDrainGap is returned by DrainOnce when the shim's spool reports an
// unrecoverable gap.
var ErrHookDrainGap = errors.New("skeleton: hook drain observed a spool gap")

// hookDrainTimeout bounds one drain round trip against a session's hook socket.
const hookDrainTimeout = 5 * time.Second

// ingestHookBytes is serveHook's (conn.go) shared second half, factored out so the
// live hook-socket path and HookDrainer's replay path can never diverge in behavior:
// decode the raw callback, authenticate it through the engine (S6/G5) -- logging a
// rejection (agents-tracker-sskl: a session's signal going silently dead is worth a
// daemon-log line) -- and, only once that succeeds, offer its captured body to the
// interaction producer.
//
// R6 REVIEW FIX-PACK (BLOCKER 1): engine.HandleCallback's OWN cb.Sequence replay
// guard (applyTyped) only runs for a callback that derives at least one status
// dimension -- an "unmapped event" (deriveDims returns nothing, engine.go's
// len(dims)==0 branch) is accepted as a benign no-op BEFORE that check ever runs.
// capture=raw events (PostToolUse, etc: exactly the ones that shape interaction
// items) are commonly unmapped, so a record redelivered either by a crash between
// apply and cursor-persist (DrainOnce) or by PostToShim's own ack-less retry (same
// cb.Sequence, a fresh spool Seq) would shape a SECOND journal item for the same
// event. hookSeqDuplicate closes that gap independent of the engine's own
// dimension-scoped tracking: at most one ingest per (session, cb.Sequence) pair.
func (d *Daemon) ingestHookBytes(raw []byte) error {
	cb, err := hookclient.Decode(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	if d.hookSeqDuplicate(cb.SessionID, cb.Sequence) {
		// LOGGED, not silent (R6 review fix-pack round 2): both callers discard this
		// error -- conn.go's serveHook by design, and DrainOnce by folding it into its
		// skipped count, which runHookDrain in turn discards -- so a suppressed event
		// left no trace anywhere. A duplicate is rare by construction (it takes a crash
		// mid-batch or PostSmart's ack-less retry), so one line costs nothing and is the
		// only observable an operator gets when the guard fires.
		log.Printf("skeleton: hook callback sequence %d for session %s was already ingested; dropped as a redelivery", cb.Sequence, cb.SessionID)
		return fmt.Errorf("skeleton: hook callback sequence %d already ingested for session %s", cb.Sequence, cb.SessionID)
	}
	if err := d.eng.HandleCallback(cb); err != nil {
		log.Printf("skeleton: hook callback rejected for session %s event %s: %v", cb.SessionID, cb.Event, err)
		return err
	}
	d.serveHookInteractions(cb)
	d.markHookSeqIngested(cb.SessionID, cb.Sequence)
	return nil
}

// hookSeenAboveMax bounds how many NON-CONTIGUOUS above-floor sequences one session's
// seen-set retains. See hookSeen.add for what happens at the bound and why the bound is
// this generous: the out-of-order window is set by how many `swarm hook` processes race
// each other around one instant, which is a handful, not hundreds.
const hookSeenAboveMax = 256

// hookSeen is one session's record of WHICH callback sequences have been fully ingested:
// a contiguous floor (everything at or below it is seen) plus the bounded set of seen
// sequences above it.
//
// IT IS A MEMBERSHIP TEST, NOT A HIGH-WATER GATE (R6 review fix-pack round 2, BLOCKER 1).
// Round 1 made this a monotone `seq <= highWater` comparison, which contradicts the
// premise internal/engine states in writing (agents-tracker-707, a real observed bug):
// "a sequence carries no causal order: it is handed out by racing `swarm hook`
// processes contending on a flock", and hookclient releases that flock BEFORE the POST,
// so arrival order and sequence order are independent. That is exactly why the engine's
// own anti-replay guard is DIMENSION-scoped rather than session-scoped. A monotone gate
// therefore dropped, whole, any legitimately distinct event that arrived after a
// higher-sequenced sibling -- and a capture=raw body is the ONLY place tool_input /
// tool_response ever exist. Two probes proved it on both the drain path and the live
// path. The question this guard must answer is "has THIS sequence been ingested", and
// nothing weaker answers it.
type hookSeen struct {
	floor uint64
	above map[uint64]struct{}
}

// hookSeenRecord is hookSeen's on-disk form.
type hookSeenRecord struct {
	Floor uint64   `json:"floor"`
	Above []uint64 `json:"above,omitempty"`
}

func (s *hookSeen) has(seq uint64) bool {
	if seq <= s.floor {
		return true
	}
	_, ok := s.above[seq]
	return ok
}

// clone copies the set, so a mark can be built and DURABLY PERSISTED before this
// process commits to believing it (markHookSeqIngested).
func (s *hookSeen) clone() *hookSeen {
	c := &hookSeen{floor: s.floor, above: make(map[uint64]struct{}, len(s.above)+1)}
	for k := range s.above {
		c.above[k] = struct{}{}
	}
	return c
}

// add records seq as seen, folds any newly-contiguous run into the floor, and enforces
// the bound.
//
// AT THE BOUND the floor advances to the lowest retained above-floor sequence, which
// assumes everything below it was seen -- the one place this structure degrades back to
// the round-1 high-water behavior, and the one place it can therefore drop a genuinely
// distinct straggler. Reaching it takes hookSeenAboveMax sequences ingested with a
// permanent hole below every one of them; the ordinary case folds into the floor
// immediately and keeps `above` at or near empty. The alternative -- an unbounded set --
// is an unbounded on-disk record written on every hook, which is a worse failure with a
// wider blast radius. The trade is stated rather than hidden.
func (s *hookSeen) add(seq uint64) {
	if s.has(seq) {
		return
	}
	if s.above == nil {
		s.above = map[uint64]struct{}{}
	}
	s.above[seq] = struct{}{}
	for {
		next := s.floor + 1
		if _, ok := s.above[next]; !ok {
			break
		}
		delete(s.above, next)
		s.floor = next
	}
	if len(s.above) <= hookSeenAboveMax {
		return
	}
	lowest := uint64(0)
	for k := range s.above {
		if lowest == 0 || k < lowest {
			lowest = k
		}
	}
	s.floor = lowest
	for k := range s.above {
		if k <= s.floor {
			delete(s.above, k)
		}
	}
}

// record renders the set for persistence, with `above` sorted so the file is stable.
func (s *hookSeen) record() hookSeenRecord {
	r := hookSeenRecord{Floor: s.floor, Above: make([]uint64, 0, len(s.above))}
	for k := range s.above {
		r.Above = append(r.Above, k)
	}
	sort.Slice(r.Above, func(i, j int) bool { return r.Above[i] < r.Above[j] })
	return r
}

// hookSeqDuplicate reports whether seq has already been fully ingested (engine applied +
// interactions offered) for sessionID. seq==0 (a caller, typically a test, that never
// set a sequence) carries no identity to dedupe on and is never treated as a duplicate,
// matching hookclient's own contract that production sequences start at 1.
//
// THE SET IS DURABLE (R6 review fix-pack round 1, BLOCKER 2). It was a plain in-memory
// map, which cannot cover the window ingestHookBytes's own doc names -- "a crash
// between ingestHookBytes applying it and DrainOnce persisting the advanced cursor" --
// because that crash takes the map with it. A probe drained one unmapped capture=raw
// event (no status dimension, so engine.HandleCallback's replay guard is bypassed
// exactly as documented), rewound the cursor, opened a genuinely new Daemon and
// re-drained: applied=1, and the journal held TWO interaction items for one logical
// event. The set now lives beside the session's fold cursor, in the session's own dir,
// read back on the first lookup of each incarnation.
func (d *Daemon) hookSeqDuplicate(sessionID string, seq uint64) bool {
	if seq == 0 {
		return false
	}
	d.itemMu.Lock()
	defer d.itemMu.Unlock()
	s := d.hookSeenLocked(sessionID)
	return s != nil && s.has(seq)
}

// hookSeenLocked resolves sessionID's seen-set from memory, else from its durable
// side-file, else nil ("nothing has ever been ingested for this id"). Caller holds
// itemMu.
//
// IT NEVER CACHES A MISS (R6 review fix-pack round 2, LOW 4). This runs on a body
// decoded off an explicitly UNAUTHENTICATED POST, ahead of engine.HandleCallback, so a
// crafted session id must not be able to grow the map without bound. Only an id that
// actually HAS durable state on disk, or one that reaches markHookSeqIngested (which is
// downstream of a successful apply, i.e. authenticated), ever takes a map slot.
func (d *Daemon) hookSeenLocked(sessionID string) *hookSeen {
	if s, ok := d.hookSeq[sessionID]; ok {
		return s
	}
	path := d.hookSeqPath(sessionID)
	if path == "" {
		return nil
	}
	s := readHookSeen(path)
	if s == nil {
		return nil
	}
	if d.hookSeq == nil {
		d.hookSeq = map[string]*hookSeen{}
	}
	d.hookSeq[sessionID] = s
	return s
}

// hookSeqPath is sessionID's durable ingest seen-set, beside the daemon-side fold cursor
// in the session's own 0700 dir -- so it is retired with the session, and a restarted
// daemon finds it exactly where the incarnation that crashed left it. "" when this
// Daemon has no state dir (a bare literal in a test), which degrades to memory-only
// behavior rather than failing.
//
// THE ID IS VALIDATED AS A SINGLE PATH SEGMENT FIRST (R6 review fix-pack round 2,
// LOW 4). filepath.Join CLEANS "..", so an unauthenticated POST carrying
// session_id="../../../etc/passwd" resolved this to "/etc/passwd/hook.applied" -- a
// pre-auth read of an arbitrary path outside the state dir. Bounded harm (read-only,
// parsed and discarded), but a hole with no reason to exist: a session id names one
// directory under the state dir and nothing else, so anything that is not a plain
// segment gets no path at all.
func (d *Daemon) hookSeqPath(sessionID string) string {
	if d.stateDir == "" || !safeSessionSegment(sessionID) {
		return ""
	}
	return filepath.Join(d.stateDir, sessionID, "hook.applied")
}

// safeSessionSegment reports whether id is usable verbatim as ONE path segment under the
// state dir: non-empty, not a traversal, no separator, no NUL, and bounded.
func safeSessionSegment(id string) bool {
	if id == "" || len(id) > 128 || id == "." || id == ".." {
		return false
	}
	return !strings.ContainsAny(id, `/\`+"\x00")
}

// markHookSeqIngested records seq as fully ingested for sessionID, once ingestHookBytes's
// own apply (engine + interaction producer) has actually succeeded -- a callback the
// engine rejected for its own reasons (bad token, a genuinely stale reorder) is never
// marked, so retrying it is not silently swallowed by this guard.
//
// The durable write happens BEFORE this process commits to the new set (the mark is
// built on a clone): a mark this process believes but disk does not is precisely the
// state that produced the duplicate above, so it is never claimed. Failing the other way
// -- marked on disk, then a crash before the cursor advances -- costs at most one
// legitimately skipped redelivery, which DrainOnce already counts and ingestHookBytes
// logs.
func (d *Daemon) markHookSeqIngested(sessionID string, seq uint64) {
	if seq == 0 {
		return
	}
	d.itemMu.Lock()
	defer d.itemMu.Unlock()
	cur := d.hookSeenLocked(sessionID)
	if cur == nil {
		cur = &hookSeen{}
	} else if cur.has(seq) {
		return
	}
	next := cur.clone()
	next.add(seq)
	if path := d.hookSeqPath(sessionID); path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			log.Printf("skeleton: persist hook ingest seen-set for session %s: %v", sessionID, err)
			return
		}
		if err := persistHookSeen(path, next.record()); err != nil {
			log.Printf("skeleton: persist hook ingest seen-set for session %s: %v", sessionID, err)
			return
		}
	}
	if d.hookSeq == nil {
		d.hookSeq = map[string]*hookSeen{}
	}
	d.hookSeq[sessionID] = next
}

// readHookSeen reads the seen-set persisted at path, or nil when there is none (or it is
// unreadable/unparseable -- an unreadable mark must read as "nothing ingested", never as
// "everything ingested").
func readHookSeen(path string) *hookSeen {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var rec hookSeenRecord
	if json.Unmarshal(data, &rec) != nil {
		// A bare decimal is the ROUND-1 shape of this file (a single high-water mark).
		// Reading it as the floor of an otherwise-empty set is exactly what it meant,
		// so a daemon that wrote one and then upgraded does not silently forget it and
		// re-apply everything it already ingested.
		v, perr := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
		if perr != nil {
			return nil
		}
		return &hookSeen{floor: v}
	}
	s := &hookSeen{floor: rec.Floor}
	if len(rec.Above) > 0 {
		s.above = make(map[uint64]struct{}, len(rec.Above))
		for _, v := range rec.Above {
			s.above[v] = struct{}{}
		}
	}
	return s
}

// persistHookSeen durably writes rec to path (temp+fsync+rename+fsyncDir), the same
// writer persistHookCursor uses, so a crash mid-write leaves the OLD set intact rather
// than a half-written one.
func persistHookSeen(path string, rec hookSeenRecord) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return persistHookBytes(path, data)
}

// HookDrainer drains one session's shim-owned hook spool into the daemon.
type HookDrainer struct {
	d              *Daemon
	sessionID      string
	hookSocketPath string
	cursorPath     string

	// drainMu serializes whole drain cycles against each other, so the poll loop and
	// the final drain stopHookDrain performs at session end can never be mid-apply at
	// the same time over the same cursor file.
	drainMu sync.Mutex

	mu        sync.Mutex
	token     string // "" = no drain-auth token configured (see SetToken)
	spoolPath string // "" = no disk fallback configured (see SetSpoolPath)
}

// NewHookDrainer builds a drainer for sessionID against hookSocketPath, persisting
// its fold cursor at cursorPath -- reopened by a later daemon incarnation to resume
// exactly where the prior one left off.
func NewHookDrainer(d *Daemon, sessionID, hookSocketPath, cursorPath string) *HookDrainer {
	return &HookDrainer{d: d, sessionID: sessionID, hookSocketPath: hookSocketPath, cursorPath: cursorPath}
}

// SetToken configures the drain-auth token this drainer presents on every DRAIN
// request (shim.HookDrainRequest.Token, checked against the shim's own configured
// Config.HookToken). The zero value (never called) means "no token" -- matching the
// shim side's own compat default, so a caller that predates the shim's token gate
// keeps working unchanged against a shim whose HookToken is equally unset.
func (hd *HookDrainer) SetToken(token string) {
	hd.mu.Lock()
	hd.token = token
	hd.mu.Unlock()
}

// SetSpoolPath configures the session's hooks.spool file, which drainFromSpoolFile reads
// directly when no live shim is left to serve a DRAIN. The zero value (never called)
// disables the disk path entirely -- the same "unset means disabled" convention the
// hook socket itself follows.
func (hd *HookDrainer) SetSpoolPath(path string) {
	hd.mu.Lock()
	hd.spoolPath = path
	hd.mu.Unlock()
}

// cursor returns the persisted, applied-and-folded cursor (0 if nothing has drained
// yet).
func (hd *HookDrainer) cursor() uint64 {
	return readHookCursor(hd.cursorPath)
}

// DrainOnce performs one dial+drain+apply+persist cycle: it requests every record
// past the persisted cursor, folding at the shim everything already durably applied
// through that SAME cursor -- never past it, so the cursor this call is about to
// advance to can never be compacted out from under a reader that has not yet observed
// it -- applies each returned record in order, and persists the advanced cursor after
// each one. skipped counts a record whose apply was rejected (engine.HandleCallback
// refused it, most commonly a genuinely stale/replayed redelivery from the
// crash-mid-batch case this design tolerates -- already logged by ingestHookBytes) so
// that outcome is OBSERVABLE rather than folded silently into applied's count; the
// cursor still advances past a skipped record either way, since it was durably read
// from the spool and must never be requested again.
func (hd *HookDrainer) DrainOnce() (applied, skipped int, err error) {
	hd.drainMu.Lock()
	defer hd.drainMu.Unlock()
	return hd.drainOnceLocked()
}

func (hd *HookDrainer) drainOnceLocked() (applied, skipped int, err error) {
	cursor := readHookCursor(hd.cursorPath)

	conn, err := net.DialTimeout("unix", hd.hookSocketPath, hookDrainTimeout)
	if err != nil {
		return 0, 0, fmt.Errorf("skeleton: dial hook socket: %w", err)
	}
	defer func() { _ = conn.Close() }()

	hd.mu.Lock()
	token := hd.token
	hd.mu.Unlock()
	req, err := json.Marshal(shim.HookDrainRequest{FromSeq: cursor, FoldSeq: cursor, Token: token})
	if err != nil {
		return 0, 0, fmt.Errorf("skeleton: encode hook drain request: %w", err)
	}
	_ = conn.SetDeadline(time.Now().Add(hookDrainTimeout))
	if _, err := conn.Write(append([]byte{shim.HookDrainTag}, req...)); err != nil {
		return 0, 0, fmt.Errorf("skeleton: write hook drain request: %w", err)
	}
	var resp shim.HookDrainResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return 0, 0, fmt.Errorf("skeleton: decode hook drain response: %w", err)
	}

	return hd.applyLocked(resp)
}

// drainFromSpoolFile is DrainOnce's socket-independent twin (R6 review fix-pack round 2,
// BLOCKER 2): it reads the session's hooks.spool FILE directly, applies everything past
// the persisted cursor with the identical rules, and persists the advanced cursor after
// each record.
//
// WHY IT EXISTS. The shim's hookServer is the only thing that ever served a DRAIN, and
// it is shut down the moment the agent is reaped -- so an event acked inside the last
// drain interval of a session became permanently unreachable while its bytes sat on
// disk. Five real-launch trials lost it 5/5. A shim CRASH has the same shape. Reading
// the spool from disk removes the live socket from the durability story entirely: the
// file is in the session's own 0700 dir, its format is self-describing, and this side
// only ever reads it (never Compacts -- folding is the live, token-gated DRAIN's job).
//
// It is a no-op when no spool path was configured (SetSpoolPath), and reports the disk
// error verbatim when the file is gone: "the spool is gone" and "the spool is clean and
// empty" must never look the same to a caller.
func (hd *HookDrainer) drainFromSpoolFile() (applied, skipped int, err error) {
	hd.drainMu.Lock()
	defer hd.drainMu.Unlock()
	return hd.drainFromSpoolFileLocked()
}

func (hd *HookDrainer) drainFromSpoolFileLocked() (applied, skipped int, err error) {
	hd.mu.Lock()
	spoolPath := hd.spoolPath
	hd.mu.Unlock()
	if spoolPath == "" {
		return 0, 0, errNoHookSpoolPath
	}
	cursor := readHookCursor(hd.cursorPath)
	recs, gapAt, hasGap, rerr := shim.ReadHookSpoolFile(spoolPath, cursor)
	if rerr != nil {
		return 0, 0, fmt.Errorf("skeleton: read hook spool file: %w", rerr)
	}
	return hd.applyLocked(shim.HookDrainResponse{Records: recs, Gap: hasGap, GapBoundary: gapAt})
}

// errNoHookSpoolPath is drainFromSpoolFile's refusal when no spool file was configured.
var errNoHookSpoolPath = errors.New("skeleton: no hook spool path configured for this drainer")

// FinalDrain is the LAST drain of a session's life, run when its loop is stopped
// (hookdrainloop.go's stopHookDrain, on the OnSessionEnd path, BEFORE the engine retires
// the session so an applied callback still authenticates). It tries the live socket
// first -- a session ending for a reason that left the shim up still has one -- and then
// always reads the spool file, which is the only path left once the agent has been
// reaped and hookServer has shut down with it.
//
// Errors are the ordinary case here, not a fault: a dead socket and an absent spool file
// are both what "this session had no structured channel, or it is already fully drained"
// looks like. Only the gap is worth surfacing, and DrainOnce already emitted and degraded
// on it.
func (hd *HookDrainer) FinalDrain() {
	hd.drainMu.Lock()
	defer hd.drainMu.Unlock()
	if _, _, err := hd.drainOnceLocked(); errors.Is(err, ErrHookDrainGap) {
		return // proven, unrecoverable, already emitted and degraded: the disk holds no more
	}
	if applied, _, err := hd.drainFromSpoolFileLocked(); err != nil {
		if !errors.Is(err, errNoHookSpoolPath) && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, ErrHookDrainGap) {
			log.Printf("skeleton: final hook drain for session %s: %v", hd.sessionID, err)
		}
	} else if applied > 0 {
		log.Printf("skeleton: final hook drain for session %s recovered %d record(s) from its spool file", hd.sessionID, applied)
	}
}

// applyLocked is the one apply loop both drain paths share, so the socket path and the
// disk path can never diverge in what they apply, what they refuse, or what they
// persist. Caller holds drainMu.
func (hd *HookDrainer) applyLocked(resp shim.HookDrainResponse) (applied, skipped int, err error) {
	for _, rec := range resp.Records {
		// ADR-017 "never silently bridged": a record at or past a reported boundary is
		// on the FAR side of unrecoverable content. Applying one would splice the
		// post-gap stream onto whatever the reader last saw, and advancing the cursor
		// past it would make the hole undetectable on every later poll.
		//
		// DEFENCE IN DEPTH as of the R6 review fix-pack round 1 (MEDIUM 7): the shim's
		// ReadFrom no longer returns such a record at all -- it used to, for the
		// "hole below the retained window" case, which is what made this break the one
		// thing standing between an exported API and a silent bridge. The rule is now
		// enforced at the source AND honored here, because a consumer that trusts a
		// boundary it was handed should not also have to trust that nothing above it
		// came along for the ride.
		if resp.Gap && rec.Seq >= resp.GapBoundary {
			break
		}
		if ierr := hd.d.ingestHookBytes(rec.Body); ierr != nil {
			skipped++
		} else {
			applied++
		}
		if perr := persistHookCursor(hd.cursorPath, rec.Seq); perr != nil {
			return applied, skipped, fmt.Errorf("skeleton: persist hook drain cursor: %w", perr)
		}
	}

	if resp.Gap {
		// R6 REVIEW FIX-PACK ROUND 1 (BLOCKER 1): the degrade runs UNCONDITIONALLY on
		// every proven gap, and is never gated by the emit-dedupe below. The two
		// answer different questions. "Has this exact boundary already been WRITTEN
		// to the journal?" is about not appending the same record twice. "Is this
		// session still allowed to claim structured_chat?" is about ADR-017 T2 rule
		// 2, and its answer, once a gap is proven, is no -- for the session
		// instance, on every incarnation, whether or not this one is the incarnation
		// that happened to append the record. Wrapping the two together is what let a
		// restarted daemon rediscover the identical still-present gap, skip the whole
		// block as already-reported, and leave the session advertising structured
		// chat. degradeCapability is idempotent and one-way, so calling it on every
		// gap costs nothing and can never re-upgrade anything.
		hd.degradeCapability()

		// The emit-dedupe, and ONLY the emit-dedupe. gapCursorPath persists exactly
		// which boundary was already appended, sibling of the fold cursor, so a
		// restart recognizes it: ADR-017 / playbook 6.1 name "an exact structured_gap
		// boundary", singular, per PROVEN TEAR -- durably, not merely once per process.
		//
		// R6 REVIEW FIX-PACK ROUND 1 (MEDIUM 8): this was ANDed with a process-local
		// "have I ever reported one" latch, which strictly lost information. A shim
		// that restarts over a wiped spool produces a fresh sequence space and hence a
		// genuinely DIFFERENT boundary; the persisted-boundary check recognizes that
		// and re-reports, while the latch swallowed every boundary after the first for
		// the life of the drainer. The persisted check is correct and sufficient
		// alone, so the latch is gone.
		gapPath := hookGapCursorPath(hd.cursorPath)
		if readHookCursor(gapPath) != resp.GapBoundary {
			reason := fmt.Sprintf("hook spool gap at seq %d", resp.GapBoundary)
			if gerr := hd.d.Core().EmitStructuredGap(hd.sessionID, reason); gerr != nil {
				log.Printf("skeleton: EmitStructuredGap for session %s: %v", hd.sessionID, gerr)
			}
			if perr := persistHookCursor(gapPath, resp.GapBoundary); perr != nil {
				log.Printf("skeleton: persist hook drain gap boundary for session %s: %v", hd.sessionID, perr)
			}
		}
		return applied, skipped, ErrHookDrainGap
	}
	return applied, skipped, nil
}

// degradeCapability applies ADR-017 T2 rule 2's one-way degrade to the session, DURABLY
// (capability.go): the fact of the proven gap is recorded whether or not a capability
// record has been authored yet, and any record that exists -- now or later -- carries
// structured_chat=false, terminal_fallback=true from it. No record is ever invented
// here; records are authored at launch.
//
// Idempotent by construction, which is what lets DrainOnce call it on EVERY proven gap
// rather than only on the incarnation that appends the journal record.
func (hd *HookDrainer) degradeCapability() {
	hd.d.markSessionDegraded(hd.sessionID)
}

// hookGapCursorPath is the sidecar recording the exact gap boundary already
// reported for a session's fold cursor, sibling of it -- both files, and the
// mechanism they name, private to this one HookDrainer/session.
func hookGapCursorPath(cursorPath string) string { return cursorPath + ".gap" }

// readHookCursor reads the persisted fold cursor at path, or 0 if it does not exist
// yet (nothing drained so far) or is unreadable.
func readHookCursor(path string) uint64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// persistHookCursor durably writes seq to path via temp+fsync+rename+fsyncDir, so a
// crash mid-write leaves the OLD cursor intact rather than a half-written one.
func persistHookCursor(path string, seq uint64) error {
	return persistHookBytes(path, []byte(strconv.FormatUint(seq, 10)))
}

// persistHookBytes is that writer, shared with persistHookSeen.
func persistHookBytes(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once renamed
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}
