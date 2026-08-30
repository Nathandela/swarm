package shim

// HookSpool is the shim-owned durable log playbook §6.1 requires: "the shim durably
// sequences and spools each accepted raw event before acknowledging it". It is a
// standalone, socket-independent primitive -- hooksocket.go is the live listener that
// Appends to it on a POST and answers a DRAIN from it; this file owns only the
// on-disk log itself: open, append (fsync before returning), replay in order,
// compact, and the bounded-refusal / gap-detection edges.
//
// ON-DISK FORMAT: a flat sequence of records, each a fixed 12-byte header (an 8-byte
// big-endian sequence, a 4-byte big-endian body length) followed by the body's raw
// bytes verbatim. There is no trailer and no per-record checksum: the header's
// declared body length is itself the gap detector -- a record whose header or body
// does not fully fit before EOF is a torn write (a crash mid-Append), and ReadFrom
// stops there rather than skip past or silently lose it.
//
// GAP HONESTY ACROSS COMPACTION AND RESTART (R6 review fix-pack): two invariants make
// a proven gap durable rather than a one-time observation that a later operation can
// erase:
//
//  1. Compact NEVER re-serializes what it keeps. It finds the exact byte offset
//     immediately after the record at the fold cursor (scanning from the start and
//     stopping the instant it hits anything unparseable) and copies everything from
//     there to true EOF VERBATIM into the replacement file -- clean records above the
//     cursor, and any torn tail beyond them, byte for byte. A tear can therefore never
//     be compacted away: the same bytes that proved it are still there afterward, at a
//     new offset, and a fresh scan finds the identical tear.
//  2. Append PERMANENTLY refuses once a tear is discovered (ErrHookSpoolTorn), rather
//     than writing a new record past it. A torn header/body followed by a later,
//     unrelated write is exactly how a prior version of this file fabricated a merged
//     record (a stale header's declared body length silently consuming the next
//     record's bytes) -- refusing outright removes the possibility rather than
//     papering over it. The refusal is not a session-ending failure: hookclient.
//     PostSmart already falls back to posting straight to the daemon whenever the
//     shim's hook socket does not durably accept a post, which is exactly this
//     spool's own refusal path, so "the agent continues locally" (playbook 6.1) holds
//     without this file doing anything more.
//
//     STATED PLAINLY (R6 review fix-pack round 1, HIGH 4): the latch is PERMANENT for
//     the session, and POST is unauthenticated by design, so a same-user process that
//     can write into this file can end the structured channel for the session's whole
//     life -- the daemon fallback carries the events, but the shim-side survival
//     boundary is gone. The control that prevents that is the 0700 session dir and
//     this file's 0600 mode, i.e. the ADR-004 same-user threat model, and nothing
//     else. That is a deliberate boundary, not an oversight; it is written down here
//     so no later reader mistakes the DRAIN token (hooksocket.go) for a mitigation of
//     it. The token gates a different verb against a different harm.
//
// SURVIVING A LOST HIGH-WATER MARK (R6 review fix-pack, BLOCKER 3): sequences are
// assigned 1, 2, 3, ... with no persisted identity beyond the file's own content. A
// spool fully compacted to empty (every record durably folded) carries no bytes at
// all -- nothing for a restarted process to recover its true last-appended sequence
// from. A tiny sidecar file (path+".floor", written only inside Compact, holding the
// decimal fold cursor) closes that hole: OpenHookSpool seeds its in-memory high-water
// mark from the floor before scanning the spool's own content, so a caller's cursor
// from before a full compaction is still recognized as stale (rather than silently
// treated as "nothing has ever happened, start over at sequence 1") even after a
// restart. ReadFrom uses that same high-water mark to detect a caller whose cursor
// names a sequence this spool instance has never produced (the file was destroyed and
// recreated from scratch under it) and a caller whose cursor sits behind the currently
// retained window (something folded past what it last observed) -- both report an
// honest gap rather than silently returning nothing or skipping over the hole.
import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// HookSpoolFile is the spool's side-file name, sibling of SnapshotFile/ExitFile/
// TranscriptFile inside a session's SessionDir.
const HookSpoolFile = "hooks.spool"

// hookSpoolDefaultMaxBytes bounds a HookSpool opened with maxBytes<=0.
const hookSpoolDefaultMaxBytes = 8 << 20

// hookRecordHeaderLen is the fixed prefix before every record's body.
const hookRecordHeaderLen = 8 + 4

// hookFloorSuffix names the fold-cursor sidecar, sibling of the spool file itself.
const hookFloorSuffix = ".floor"

// hookIncarnationSuffix names the sequence-space identity sidecar. The spool's
// numeric sequence starts at one again when the file is deleted and recreated, so
// a boundary is identified by this value plus its sequence, never by the sequence
// alone.
const hookIncarnationSuffix = ".incarnation"

// hookOpenLockSuffix names the persistent advisory-lock inode that serializes
// sequence-space discovery and creation. It is deliberately never unlinked:
// replacing a lock path while another opener waits on its old inode would split
// the critical section.
const hookOpenLockSuffix = ".open.lock"

// LegacyHookSpoolIncarnation is the explicit namespace for a disk-only spool that
// predates the incarnation sidecar and has not been opened by this build.
const LegacyHookSpoolIncarnation = "legacy"

type hookSpoolIdentity struct {
	ID            string `json:"id"`
	AdoptedLegacy bool   `json:"adopted_legacy,omitempty"`
}

// HookRecord is one durably-spooled hook event.
type HookRecord struct {
	Seq  uint64          `json:"seq"`
	Body json.RawMessage `json:"body"`
}

// ErrHookSpoolFull is returned by Append once accepting a record would grow the
// spool past its size bound. The write is refused outright: nothing already
// accepted is ever evicted to make room (playbook 6.1's "loses an accepted item"
// prohibition, applied to the spool's own bound as well as to daemon downtime).
var ErrHookSpoolFull = errors.New("shim: hook spool at its size bound")

// ErrHookSpoolTorn is returned by Append, permanently, once this spool instance has
// discovered a torn (partially-written) record anywhere in its file -- whether from a
// crash mid-write on a prior incarnation (found at Open) or external corruption of the
// live file (found at Append). Refusing every further write is what keeps a later,
// unrelated Append from ever landing past the tear and being misparsed as part of it
// (see the file's own header comment); the caller (hooksocket.go's servePost) already
// treats any Append error as "not durably accepted" and closes without acking, which
// is what lets hookclient.PostSmart fall back to posting straight to the daemon.
var ErrHookSpoolTorn = errors.New("shim: hook spool has a proven tear and permanently refuses further writes")

// testHookAfterSpoolFsync, when non-nil, runs synchronously inside Append, after the
// fsync commits and before Append returns -- the exact "accepted, not yet acked"
// window playbook 6.1 names. Nil in production.
var testHookAfterSpoolFsync func()

// testHookAfterHookSpoolCreate, when non-nil, runs after a missing spool path
// has been created but before OpenHookSpool continues initialization. Tests use
// a panic here to model process death at the creation durability boundary.
var testHookAfterHookSpoolCreate func()

// hookSpoolSync is the fsync Append commits a record with, behind a seam so the CALL
// itself is assertable (R6 review fix-pack round 1, MEDIUM 5). It was previously an
// inline s.f.Sync(): deleting that line left every durability test in this package
// green, because a second in-process handle reads the record straight back through the
// page cache. Ordering was never the defect -- unobservability was.
var hookSpoolSync = func(f *os.File) error { return f.Sync() }

// HookSpool is one session's durable hook-event log: unexported fields, opened over
// one file. Every method is safe for concurrent use.
type HookSpool struct {
	mu       sync.Mutex
	path     string
	f        *os.File
	maxBytes int
	// size is the byte offset through which the file is known-clean. Append always
	// writes starting here (never at the file's raw physical EOF), so a torn tail --
	// however it got there -- is never written past.
	size int64
	// lastSeq is the highest sequence this spool instance has EVER durably produced,
	// seeded from the persisted floor at Open and bumped by a clean scan or a
	// successful Append. It survives Compact, which only ever narrows what is
	// physically retained, never this.
	lastSeq uint64
	// gapAt is 0 while no tear is proven; else the earliest provable torn sequence,
	// latched for this instance's lifetime once set.
	gapAt uint64
	// incarnation is stable for this file's sequence space, including across
	// compaction/reopen. adoptedLegacy says it was minted for a pre-existing
	// pre-identity spool, so a decimal legacy gap checkpoint can be migrated once.
	incarnation   string
	adoptedLegacy bool
}

// OpenHookSpool opens (or creates, at 0600) the spool file at path -- and its parent
// dir, which callers already own at 0700. maxBytes<=0 means hookSpoolDefaultMaxBytes.
// Reopening an existing file (what a restarted shim does) resumes sequencing from the
// highest sequence known EITHER from the persisted fold floor or from a clean scan of
// the file's own content, whichever is higher, so Append after a restart is still
// monotonic and a caller's pre-restart cursor is still recognized, never silently
// treated as belonging to a brand new sequence space.
func OpenHookSpool(path string, maxBytes int) (*HookSpool, error) {
	if maxBytes <= 0 {
		maxBytes = hookSpoolDefaultMaxBytes
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("shim: hook spool dir: %w", err)
	}
	openLock, err := acquireHookSpoolOpenLock(path)
	if err != nil {
		return nil, err
	}
	defer releaseHookSpoolOpenLock(openLock)

	// Open without O_CREATE first. The old stat-then-O_CREATE sequence had two
	// correctness holes: process death after creation made restart misclassify the
	// new empty file as the old generation, and a concurrent creator could change
	// the answer between Stat and OpenFile. Under the per-path flock there is one
	// decision. A missing path commits the new floor/identity first, then publishes
	// the empty spool with O_EXCL.
	f, err := os.OpenFile(path, os.O_RDWR, 0o600)
	created := false
	var identity hookSpoolIdentity
	if errors.Is(err, os.ErrNotExist) {
		fresh, ferr := mintHookSpoolIncarnation()
		if ferr != nil {
			return nil, fmt.Errorf("shim: mint hook spool incarnation: %w", ferr)
		}
		identity = hookSpoolIdentity{ID: fresh}
		if rerr := os.Remove(hookFloorPath(path)); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			return nil, fmt.Errorf("shim: reset hook spool floor for new incarnation: %w", rerr)
		}
		// The identity writer's directory fsync commits both the floor removal and
		// the new identity before the spool path can become visible.
		if ierr := writeHookSpoolIdentity(path, identity); ierr != nil {
			return nil, fmt.Errorf("shim: persist hook spool incarnation: %w", ierr)
		}
		f, err = os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			// Every cooperating opener holds openLock. A path appearing here was
			// created by an older/uncoordinated writer. Refuse rather than bind our
			// newly committed identity to bytes we did not create.
			return nil, errors.New("shim: hook spool appeared while its generation was being initialized")
		}
		if err != nil {
			return nil, fmt.Errorf("shim: create hook spool: %w", err)
		}
		created = true
		if testHookAfterHookSpoolCreate != nil {
			testHookAfterHookSpoolCreate()
		}
	} else if err != nil {
		return nil, fmt.Errorf("shim: open hook spool: %w", err)
	} else {
		identity, err = readHookSpoolIdentity(path)
		if errors.Is(err, os.ErrNotExist) {
			fresh, ferr := mintHookSpoolIncarnation()
			if ferr != nil {
				_ = f.Close()
				return nil, fmt.Errorf("shim: mint hook spool incarnation: %w", ferr)
			}
			identity = hookSpoolIdentity{ID: fresh, AdoptedLegacy: true}
			if ierr := writeHookSpoolIdentity(path, identity); ierr != nil {
				_ = f.Close()
				return nil, fmt.Errorf("shim: persist hook spool incarnation: %w", ierr)
			}
		} else if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("shim: read hook spool incarnation: %w", err)
		}
	}
	keepOpen := false
	defer func() {
		if !keepOpen {
			_ = f.Close()
		}
	}()
	if err := f.Chmod(0o600); err != nil { // re-tighten regardless of umask or a pre-existing mode
		return nil, fmt.Errorf("shim: chmod hook spool: %w", err)
	}
	if created {
		// R6 REVIEW FIX-PACK ROUND 1 (MEDIUM 6): Append's fsync commits a record's
		// BYTES but not the directory entry that names the file they live in. On a
		// brand-new spool that leaves exactly one window in which an ACKED item can
		// still be lost -- power fails after the first post is acked, and the whole
		// file is gone with its directory entry. Compact already fsyncs the dir after
		// its rename (below); creation is the other half of the same obligation.
		// Failing here is correct rather than best-effort: an un-committed spool file
		// cannot honour the ack contract, and Run degrades cleanly to no hook socket.
		if err := fsyncDir(dir); err != nil {
			return nil, fmt.Errorf("shim: fsync hook spool dir after create: %w", err)
		}
	}
	s := &HookSpool{
		path: path, f: f, maxBytes: maxBytes, lastSeq: readHookFloor(path),
		incarnation: identity.ID, adoptedLegacy: identity.AdoptedLegacy,
	}
	recs, cleanEnd, tornSeq, torn, err := s.parseLocked()
	if err != nil {
		return nil, err
	}
	if n := len(recs); n > 0 && recs[n-1].Seq > s.lastSeq {
		s.lastSeq = recs[n-1].Seq
	}
	s.size = cleanEnd
	if torn {
		if tornSeq <= s.lastSeq {
			tornSeq = s.lastSeq + 1
		}
		s.gapAt = tornSeq
	}
	keepOpen = true
	return s, nil
}

// IncarnationID is this spool's stable sequence-space identity.
func (s *HookSpool) IncarnationID() string { return s.incarnation }

// AdoptedLegacySequence reports that this identity was minted for a pre-existing
// spool, allowing a decimal pre-identity gap checkpoint to migrate without a
// duplicate boundary.
func (s *HookSpool) AdoptedLegacySequence() bool { return s.adoptedLegacy }

// ReadHookSpoolIncarnation reads a spool's identity without opening or mutating it.
// An absent sidecar is the explicit legacy namespace, used by final drain over a
// disk-only spool written by an older shim.
func ReadHookSpoolIncarnation(path string) (id string, adoptedLegacy bool, err error) {
	identity, err := readHookSpoolIdentity(path)
	if errors.Is(err, os.ErrNotExist) {
		return LegacyHookSpoolIncarnation, true, nil
	}
	if err != nil {
		return "", false, err
	}
	return identity.ID, identity.AdoptedLegacy, nil
}

// ReadHookSpoolFile is ReadFrom against a spool FILE, with no live HookSpool and no
// listener -- the socket-independent reader (R6 review fix-pack round 2, BLOCKER 2).
//
// WHY IT EXISTS. The spool's whole promise is that an event is durable the moment it is
// acked, but until now its ONLY reader was hookServer, which is shut down the instant
// the agent is reaped (shim.go). Every hook acked inside the daemon's last drain
// interval was therefore unreachable forever while its bytes sat on disk -- an item the
// shim durably ACKED, lost at ordinary session end, which is exactly what playbook 6.1
// ("neither fails a provider hook nor loses an accepted item") and the ack contract
// forbid. A shim CRASH has the identical shape. The file is in the session's own 0700
// dir and the format is self-describing, so the durability stops being hostage to a
// live socket: the daemon reads the retired session's spool itself.
//
// It is READ-ONLY on purpose. Compact is the destructive half of a drain and belongs
// only to the live, token-gated DRAIN verb (hooksocket.go); a reader of a spool whose
// owner is gone folds nothing and leaves every byte exactly where it found it.
//
// Every rule ReadFrom's own doc states holds here verbatim, because this shares
// readFromLocked: records are always strictly below a reported gapAt, a cursor naming
// a sequence the file never produced is an honest gap, and so is a cursor sitting
// behind the retained window. A missing file is an error, never a silent empty read --
// "the spool is gone" and "the spool is clean and empty" must not look the same.
func ReadHookSpoolFile(path string, after uint64) (recs []HookRecord, gapAt uint64, hasGap bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, false, fmt.Errorf("shim: open hook spool for reading: %w", err)
	}
	defer func() { _ = f.Close() }()

	// The same high-water seeding OpenHookSpool performs: the persisted fold floor
	// first (so a cursor from before a full compaction is still recognized as stale
	// rather than read as a fresh sequence space), raised by whatever the file's own
	// content proves.
	s := &HookSpool{path: path, f: f, maxBytes: hookSpoolDefaultMaxBytes, lastSeq: readHookFloor(path)}
	all, cleanEnd, _, _, err := s.parseLocked()
	if err != nil {
		return nil, 0, false, err
	}
	if n := len(all); n > 0 && all[n-1].Seq > s.lastSeq {
		s.lastSeq = all[n-1].Seq
	}
	s.size = cleanEnd
	return s.readFromLocked(after)
}

// Append durably appends body -- fsyncing before it returns -- and reports its
// assigned sequence (monotonic, starting at 1). It refuses outright (ErrHookSpoolTorn)
// once this spool has ever discovered a tear, and (ErrHookSpoolFull) once accepting
// the record would grow the file past its size bound.
func (s *HookSpool) Append(body []byte) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.gapAt != 0 {
		return 0, ErrHookSpoolTorn
	}
	// Defend against the file being damaged out from under a LIVE instance -- no
	// crash, no reopen, just an external actor shrinking the file while this
	// HookSpool keeps running. A size below what was last known-clean can only mean
	// that, and it is caught here rather than assumed away: a live tear is latched
	// exactly like one discovered at Open, never silently written past.
	if info, err := s.f.Stat(); err == nil && info.Size() < s.size {
		s.gapAt = s.lastSeq + 1
		return 0, ErrHookSpoolTorn
	}

	recLen := int64(hookRecordHeaderLen + len(body))
	if s.size+recLen > int64(s.maxBytes) {
		return 0, ErrHookSpoolFull
	}
	seq := s.lastSeq + 1
	var hdr [hookRecordHeaderLen]byte
	binary.BigEndian.PutUint64(hdr[0:8], seq)
	binary.BigEndian.PutUint32(hdr[8:12], uint32(len(body)))

	// Truncate to the known-clean end BEFORE writing: belt-and-braces so a write can
	// never land after stray bytes, whatever put them there.
	if err := s.f.Truncate(s.size); err != nil {
		return 0, fmt.Errorf("shim: truncate hook spool before append: %w", err)
	}
	if _, err := s.f.Seek(s.size, io.SeekStart); err != nil {
		return 0, fmt.Errorf("shim: seek hook spool: %w", err)
	}
	if _, err := s.f.Write(hdr[:]); err != nil {
		return 0, fmt.Errorf("shim: write hook record header: %w", err)
	}
	if _, err := s.f.Write(body); err != nil {
		return 0, fmt.Errorf("shim: write hook record body: %w", err)
	}
	if err := hookSpoolSync(s.f); err != nil {
		return 0, fmt.Errorf("shim: fsync hook spool: %w", err)
	}
	if testHookAfterSpoolFsync != nil {
		testHookAfterSpoolFsync()
	}
	s.lastSeq = seq
	s.size += recLen
	return seq, nil
}

// ReadFrom returns every record with Seq>after, in order, stopping at the first
// corrupt/truncated one. hasGap names the boundary (gapAt is the first sequence that
// could not be read). A clean tail is hasGap=false, gapAt=0.
//
// ONE RULE BINDS EVERY RETURN (R6 review fix-pack round 1, MEDIUM 7): every record in
// recs has Seq < gapAt whenever hasGap is true. Nothing at or past a reported boundary
// is ever handed back -- not the torn record, not anything after it, and not the
// still-retained records that sit above a hole an out-of-band fold opened below them.
// ADR-017's survival row says a proven gap "is never silently bridged"; returning the
// far side of one to a caller that has no way to know which side it is looking at is
// how that gets bridged by accident.
//
// after>0 additionally proves two things a plain content scan cannot (R6 review
// fix-pack, BLOCKER 3): that the caller has not already seen sequences this spool
// instance never produced (the file was destroyed and recreated under it), and that
// nothing was folded away between what the caller last observed and what is now the
// oldest record retained. Both report an honest gap rather than silently returning
// nothing. after==0 ("I have never read anything") is exempt from both: a brand new
// reader has no basis to expect content some earlier, already-acknowledged fold
// legitimately retired before it ever connected.
func (s *HookSpool) ReadFrom(after uint64) (recs []HookRecord, gapAt uint64, hasGap bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readFromLocked(after)
}

func (s *HookSpool) readFromLocked(after uint64) ([]HookRecord, uint64, bool, error) {
	if after > 0 && after > s.lastSeq {
		// The caller claims to have already seen a sequence this spool instance has
		// never produced -- only possible if the underlying file was destroyed and
		// recreated (or otherwise reset) out from under a cursor still naming the old
		// sequence space.
		return nil, after + 1, true, nil
	}

	all, _, tornSeq, torn, err := s.parseLocked()
	if err != nil {
		return nil, 0, false, err
	}

	var head uint64 // the oldest sequence currently retained; 0 = "nothing retired yet"
	if len(all) > 0 {
		head = all[0].Seq
	} else if s.lastSeq > 0 {
		head = s.lastSeq + 1 // everything through lastSeq has been folded away
	}

	if after > 0 && head > 0 && after+1 < head {
		// Something folded away records between what the caller last observed and
		// what is now retained: EVERY record still retained sits ABOVE that hole.
		//
		// R6 REVIEW FIX-PACK ROUND 1 (MEDIUM 7): this branch used to hand those
		// records back alongside the gap, with a comment inviting the caller to
		// apply them. That is the exact inverse of the torn-tail branch below, where
		// every returned record sits strictly BELOW the boundary and IS applicable,
		// and it is what ADR-017's "never silently bridged" forbids: applying them
		// splices the post-hole stream onto whatever the reader last saw. One rule
		// now holds for both branches and for every future consumer of this exported
		// method -- recs are ALWAYS strictly below gapAt -- rather than a foot-gun
		// disarmed only by DrainOnce happening to break at the boundary.
		return nil, after + 1, true, nil
	}

	// Everything the caller has not seen. Only the torn-tail case can report a gap
	// from here, and every one of these records sits strictly below its boundary.
	var recs []HookRecord
	for _, r := range all {
		if r.Seq > after {
			recs = append(recs, r)
		}
	}
	if torn {
		return recs, tornSeq, true, nil
	}
	return recs, 0, false, nil
}

// parseLocked walks the file from the start and returns every cleanly-readable record
// in order, the byte offset immediately after the last one (cleanEnd -- where Append
// or Compact would cut), and, if the walk stopped at a torn record, that record's
// sequence and torn=true. tornSeq is exact when the header survived intact (read
// straight off its own bytes); when even the header did not, it falls back to the
// higher of what THIS scan has seen so far and the spool's own persisted high-water
// mark (s.lastSeq) -- correct whenever that mark reflects real history, which floor
// persistence (Compact) keeps true across a restart in every case this package's own
// tests exercise. The one residual: a torn HEADER (fewer than 12 bytes ever landed)
// discovered at the very first Open after BOTH a full compaction AND a restart, with
// no Append in between to have re-seeded s.lastSeq, can only fall back to the
// persisted floor -- exact for every torn-BODY case (the far more likely tear, since
// it requires nothing more than a body write to be interrupted) and for any torn
// record found while the file has clean history before it.
func (s *HookSpool) parseLocked() (recs []HookRecord, cleanEnd int64, tornSeq uint64, torn bool, err error) {
	if _, err := s.f.Seek(0, io.SeekStart); err != nil {
		return nil, 0, 0, false, fmt.Errorf("shim: seek hook spool: %w", err)
	}
	data, err := io.ReadAll(s.f)
	if err != nil {
		return nil, 0, 0, false, fmt.Errorf("shim: read hook spool: %w", err)
	}
	recs, cleanEnd, tornSeq, torn = scanHookRecords(data)
	if torn && tornSeq == 0 {
		// scanHookRecords' sentinel for "even the header did not land": fall back to
		// the higher of what this scan itself saw and the spool's own persisted
		// high-water mark (see this function's own doc for the one residual case).
		best := s.lastSeq
		if n := len(recs); n > 0 && recs[n-1].Seq > best {
			best = recs[n-1].Seq
		}
		tornSeq = best + 1
	}
	return recs, cleanEnd, tornSeq, torn, nil
}

// scanHookRecords walks data from its start and returns every cleanly-readable
// record in order, the byte offset immediately after the last one (cleanEnd -- data
// through this offset is known-clean, and Append or Compact would cut there), and,
// if the walk stopped at an unparseable (torn) record, that record's declared
// sequence and torn=true. tornSeq is exact when the header survived (read straight
// off its own bytes); it is 0 -- never a real record's sequence, which starts at 1
// -- when even the header did not land, a sentinel the two callers each resolve
// against a different "last known-good sequence" fallback (parseLocked's is
// s.lastSeq; Compact's is the fold cursor it was just handed). Shared by both so a
// torn record is defined identically everywhere this package looks for one.
func scanHookRecords(data []byte) (recs []HookRecord, cleanEnd int64, tornSeq uint64, torn bool) {
	off := 0
	for off < len(data) {
		if len(data)-off < hookRecordHeaderLen {
			return recs, int64(off), 0, true
		}
		seq := binary.BigEndian.Uint64(data[off : off+8])
		bodyLen := int(binary.BigEndian.Uint32(data[off+8 : off+hookRecordHeaderLen]))
		bodyOff := off + hookRecordHeaderLen
		if len(data)-bodyOff < bodyLen {
			return recs, int64(off), seq, true // the header survived the tear, the body did not
		}
		body := append(json.RawMessage(nil), data[bodyOff:bodyOff+bodyLen]...)
		recs = append(recs, HookRecord{Seq: seq, Body: body})
		off = bodyOff + bodyLen
	}
	return recs, int64(off), 0, false
}

// Compact durably drops every record with Seq<=foldSeq via a temp+fsync+rename+
// fsyncDir replace of the spool file (the same pattern writeFileAtomic/
// persistSideFiles use elsewhere in this package), so a crash mid-compaction leaves
// the OLD file intact -- rename is atomic -- never a half-written one. foldSeq must
// name a clean record boundary at or below the highest sequence ever appended:
// Compact's argument IS the daemon's proof of what it durably folded, so a cursor
// from the future, or one that lands inside or beyond an unparseable region, is
// refused rather than silently honored.
//
// Everything from the fold point onward -- every clean record above foldSeq, and any
// torn tail beyond them -- is copied into the replacement file VERBATIM, never
// re-serialized. This is deliberate (see the file's own header comment): compaction
// removes only a verified-clean PREFIX and must never be able to erase a tear it
// cannot even see past.
func (s *HookSpool) Compact(foldSeq uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if foldSeq > s.lastSeq {
		return fmt.Errorf("shim: hook spool compact fold cursor %d exceeds the highest appended sequence %d", foldSeq, s.lastSeq)
	}
	if foldSeq == 0 {
		return nil // nothing folded yet: a no-op, not an error
	}

	if _, err := s.f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("shim: seek hook spool for compact: %w", err)
	}
	data, err := io.ReadAll(s.f)
	if err != nil {
		return fmt.Errorf("shim: read hook spool for compact: %w", err)
	}

	cut := -1
	off := 0
	for off < len(data) {
		if len(data)-off < hookRecordHeaderLen {
			break
		}
		seq := binary.BigEndian.Uint64(data[off : off+8])
		bodyLen := int(binary.BigEndian.Uint32(data[off+8 : off+hookRecordHeaderLen]))
		bodyOff := off + hookRecordHeaderLen
		if len(data)-bodyOff < bodyLen {
			break
		}
		off = bodyOff + bodyLen
		if seq == foldSeq {
			cut = off
			break
		}
	}
	if cut < 0 {
		return fmt.Errorf("shim: hook spool compact fold cursor %d is not a clean record boundary in the spool", foldSeq)
	}
	tail := data[cut:]

	// Persist the fold floor BEFORE the replacement file is committed (R6 review
	// fix-pack, MEDIUM: the ORIGINAL write-ordering wrote this AFTER the rename,
	// best-effort, swallowing its error). A crash (or a full disk) between that
	// rename and that floor write left the floor stale -- or, on this spool's very
	// first compaction, at its zero default -- while the file a restart would
	// otherwise have rescanned had ALREADY been replaced, possibly by an entirely
	// empty one. OpenHookSpool then seeds lastSeq too LOW, and a perfectly
	// legitimate caller cursor above that false high-water mark reads as naming a
	// sequence this spool never produced -- a FALSE gap and a one-way capability
	// degrade for no real loss. Failing HERE, before anything durable has changed,
	// aborts the whole compaction cleanly instead: the OLD file and OLD floor are
	// both left exactly as they were, a safe, simply-retried state (the caller,
	// hooksocket.go's serveDrain, already treats every Compact error as exactly
	// that: "a refused compact leaves the spool untouched"). A floor written too
	// HIGH only ever makes an already-stale cursor look stale; only one written too
	// LOW is unrecoverable, which is why this must happen first, not merely be
	// logged after the fact.
	if err := writeHookFloor(s.path, foldSeq); err != nil {
		return fmt.Errorf("shim: persist hook spool fold floor: %w", err)
	}

	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, filepath.Base(s.path)+".tmp*")
	if err != nil {
		return fmt.Errorf("shim: create hook spool compact temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once renamed
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("shim: chmod hook spool compact temp: %w", err)
	}
	if _, err := tmp.Write(tail); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("shim: write hook spool compact temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("shim: fsync hook spool compact temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("shim: close hook spool compact temp: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("shim: rename hook spool compact temp: %w", err)
	}
	if err := fsyncDir(dir); err != nil {
		return fmt.Errorf("shim: fsync hook spool dir: %w", err)
	}

	// The pre-rename handle now refers to an unlinked inode: reopen at path so the
	// NEXT Append lands in the replacement file, not the one just retired.
	if err := s.f.Close(); err != nil {
		return fmt.Errorf("shim: close old hook spool handle: %w", err)
	}
	f, err := os.OpenFile(s.path, os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("shim: reopen hook spool after compact: %w", err)
	}
	s.f = f
	// s.size is the offset through which the REPLACEMENT file is known-clean, not
	// simply its length (R6 review fix-pack, MEDIUM): tail was copied verbatim and
	// can itself hold a torn tail beyond its own clean records (this instance's
	// gapAt may not be latched yet -- e.g. external corruption discovered only NOW,
	// by this scan, rather than at an earlier Open or Append). Re-scanning tail with
	// the SAME record definition parseLocked uses finds its true clean end; treating
	// the whole (possibly garbage-suffixed) tail as clean would let a later Append
	// land past undetected garbage -- precisely the fabricated-merged-record hole
	// this file's own header comment says was removed.
	tRecs, tClean, tTornSeq, tTorn := scanHookRecords(tail)
	s.size = tClean
	if tTorn {
		if tTornSeq == 0 {
			best := s.lastSeq
			if n := len(tRecs); n > 0 && tRecs[n-1].Seq > best {
				best = tRecs[n-1].Seq
			}
			tTornSeq = best + 1
		}
		if s.gapAt == 0 {
			s.gapAt = tTornSeq
		}
	}
	// s.lastSeq is untouched: Compact only ever removes an already-durably-folded
	// prefix and must never invent or erase what happened above it.

	return nil
}

// Close closes the spool's file handle.
func (s *HookSpool) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.f.Close()
}

// hookFloorPath is the fold-floor sidecar's path, sibling of the spool file.
func hookFloorPath(spoolPath string) string { return spoolPath + hookFloorSuffix }

// readHookFloor reads the persisted fold floor for spoolPath, or 0 if absent/unreadable
// (no compaction has ever happened, or its sidecar predates this mechanism).
func readHookFloor(spoolPath string) uint64 {
	data, err := os.ReadFile(hookFloorPath(spoolPath))
	if err != nil {
		return 0
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// writeHookFloor durably persists seq as spoolPath's fold floor via the same
// temp+fsync+rename+fsyncDir pattern used elsewhere in this package.
func writeHookFloor(spoolPath string, seq uint64) error {
	dir := filepath.Dir(spoolPath)
	name := filepath.Base(hookFloorPath(spoolPath))
	if err := writeFileAtomic(dir, name, []byte(strconv.FormatUint(seq, 10))); err != nil {
		return err
	}
	return fsyncDir(dir)
}

func hookSpoolIdentityPath(spoolPath string) string { return spoolPath + hookIncarnationSuffix }

func hookSpoolOpenLockPath(spoolPath string) string { return spoolPath + hookOpenLockSuffix }

func acquireHookSpoolOpenLock(spoolPath string) (*os.File, error) {
	path := hookSpoolOpenLockPath(spoolPath)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("shim: open hook spool generation lock: %w", err)
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("shim: chmod hook spool generation lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("shim: lock hook spool generation: %w", err)
	}
	return f, nil
}

func releaseHookSpoolOpenLock(f *os.File) {
	if f == nil {
		return
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	_ = f.Close()
}

func mintHookSpoolIncarnation() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func readHookSpoolIdentity(spoolPath string) (hookSpoolIdentity, error) {
	data, err := os.ReadFile(hookSpoolIdentityPath(spoolPath))
	if err != nil {
		return hookSpoolIdentity{}, err
	}
	var identity hookSpoolIdentity
	if err := json.Unmarshal(data, &identity); err != nil {
		return hookSpoolIdentity{}, err
	}
	if strings.TrimSpace(identity.ID) == "" {
		return hookSpoolIdentity{}, errors.New("shim: empty hook spool incarnation")
	}
	return identity, nil
}

func writeHookSpoolIdentity(spoolPath string, identity hookSpoolIdentity) error {
	data, err := json.Marshal(identity)
	if err != nil {
		return err
	}
	dir := filepath.Dir(spoolPath)
	if err := writeFileAtomic(dir, filepath.Base(hookSpoolIdentityPath(spoolPath)), data); err != nil {
		return err
	}
	return fsyncDir(dir)
}
