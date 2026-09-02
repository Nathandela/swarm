package skeleton

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/registry"
	"github.com/Nathandela/swarm/internal/persist"
)

type resumeHistoryOutcome uint8

const (
	resumeHistoryUnsupported resumeHistoryOutcome = iota
	resumeHistoryNoMatch
	resumeHistoryFound
	resumeHistoryAmbiguous
	resumeHistoryUnsafe
	resumeHistoryUnreadable
)

type resumeHistoryResult struct {
	Outcome        resumeHistoryOutcome
	ConversationID string
}

// resumeHistoryResolver is the component that knows where a provider keeps its
// conversation history on disk. It answers two questions about one session, and both
// belong to it for the same reason -- the answer requires the provider's layout AND the
// anchored traversal, and neither caller may hold either. Resolve says WHICH
// conversation a session was; LocateTranscript says WHERE that conversation lives.
type resumeHistoryResolver interface {
	Resolve(persist.Meta) resumeHistoryResult
	// LocateTranscript returns the absolute path of the transcript holding convID,
	// having opened it under the anchor. Read its implementation comment before
	// trusting the returned string: what the anchor buys is narrower than it looks.
	LocateTranscript(m persist.Meta, convID string) (string, resumeHistoryOutcome)
}

type resumeHistoryLimits struct {
	MaxEntries     int
	MaxOpenFiles   int
	MaxRecordBytes int64
	MaxTotalBytes  int64
}

var defaultResumeHistoryLimits = resumeHistoryLimits{
	MaxEntries:     4096,
	MaxOpenFiles:   512,
	MaxRecordBytes: 64 << 10,
	MaxTotalBytes:  8 << 20,
}

// providerAliasMaxDepth bounds the number of anchored directory descriptors a
// provider-root alias traversal can retain during one recovery. Exact depth is
// inclusive; max+1 fails before any target component is opened.
const providerAliasMaxDepth = 64

type filesystemResumeHistoryResolver struct {
	home                  string
	limits                resumeHistoryLimits
	beforeAliasReadlink   func(string) // private deterministic alias race seams; nil in production
	beforeAliasTargetOpen func(string)
	beforeOpen            func(string) // private deterministic TOCTOU test seam; nil in production
}

func newFilesystemResumeHistoryResolver(home string, limits resumeHistoryLimits) *filesystemResumeHistoryResolver {
	return &filesystemResumeHistoryResolver{home: home, limits: limits}
}

type historyBudget struct {
	limits  resumeHistoryLimits
	entries int
	opens   int
	bytes   int64
}

func (b *historyBudget) valid() bool {
	return b.limits.MaxEntries > 0 && b.limits.MaxOpenFiles > 0 &&
		b.limits.MaxRecordBytes > 0 && b.limits.MaxTotalBytes > 0
}

func (b *historyBudget) addEntries(n int) bool {
	b.entries += n
	return b.entries <= b.limits.MaxEntries
}

func (b *historyBudget) openFile() bool {
	b.opens++
	return b.opens <= b.limits.MaxOpenFiles
}

func (b *historyBudget) addBytes(n int) bool {
	b.bytes += int64(n)
	return b.bytes <= b.limits.MaxTotalBytes
}

func (r *filesystemResumeHistoryResolver) Resolve(m persist.Meta) resumeHistoryResult {
	switch m.AgentType {
	case "codex", "claude":
	default:
		return resumeHistoryResult{Outcome: resumeHistoryUnsupported}
	}
	// ProviderCwd, not Cwd: a provider files its history under the directory the AGENT
	// ran in, which for a worktree-isolated session is <repo>/.swarm/worktrees/<slug> and
	// not the repo the launch was requested in. Reading Cwd here searched a directory
	// the provider never wrote to, so recovery could not work for those sessions at all.
	// The absoluteness gate applies to the value actually used, for the same reason.
	if !filepath.IsAbs(m.ProviderCwd()) {
		return resumeHistoryResult{Outcome: resumeHistoryNoMatch}
	}
	if !filepath.IsAbs(r.home) {
		return resumeHistoryResult{Outcome: resumeHistoryUnreadable}
	}
	budget := &historyBudget{limits: r.limits}
	if !budget.valid() {
		return resumeHistoryResult{Outcome: resumeHistoryUnsafe}
	}
	home, outcome := r.openHome()
	if outcome != resumeHistoryFound {
		return resumeHistoryResult{Outcome: outcome}
	}
	defer func() { _ = home.Close() }()
	if m.AgentType == "codex" {
		return r.resolveCodex(home, budget, m)
	}
	return r.resolveClaude(home, budget, m)
}

// LocateTranscript answers the second question this resolver is the right place to
// answer: not "which conversation was this session" but "where does that conversation
// live". It walks to the file through the SAME anchored, budgeted os.Root traversal
// Resolve uses, opens it, and returns its absolute path.
//
// WHAT THE ANCHORED WALK BUYS, AND WHAT IT DOES NOT. The traversal's guarantees --
// os.SameFile inode identity, no symlink component, IsRegular, O_RDONLY|O_NONBLOCK
// against a planted FIFO -- are properties of a FILE DESCRIPTOR, and this function
// returns a STRING. None of them survive serialization into a name that another
// process opens minutes later. Concretely, the returned path does NOT promise that the
// file still exists at open time, that it is the same inode, that no component became a
// symlink in between, that it is still a regular file, or that the successor's process
// resolves the same string to the same file. What it does promise is confinement BY
// CONSTRUCTION: swarm did not invent this path and did not follow a symlink to produce
// it. Every segment is provably a single separator-free component -- homeAbs is Clean'd
// and IsAbs-checked; ".claude" and "projects" are compile-time literals, and where
// ".claude" is instead a stable alias, providerAliasTarget has already proved its
// target a clean, ".."-free, strict descendant of that same home; ProjectDirName emits
// only [A-Za-z0-9-] (one dash per non-alphanumeric rune, so no separator can survive
// it); and a convID that passed IsCanonicalConversationID is 36 characters of lowercase
// hex with dashes at fixed offsets. There is no input under which the assembled string
// leaves the projects root. (The argument is the COMPONENTS', not filepath.Join's: Join
// cleans, and cleaning is what would turn "../.." into an escape rather than stop it.)
// The machinery's real job here is stopping a malformed or hostile input from steering
// the DAEMON's own reads, and that job is done in full.
//
// Two layouts, one anchored walk. Claude files by CWD, so the file is NAMED from the
// cwd and the id and opened exactly (adapter.TranscriptLayout). Codex files by DAY, so
// the day is read out of the id (adapter.DatedTranscriptLayout) and LISTED, bounded by
// the budget, for the one entry naming the id. Absence of both is the signal (ADR-010
// section 5): an adapter that characterizes neither is refused by name, and a stub
// returning "" would instead look like an answer and send an anchored open at a
// directory named "".
func (r *filesystemResumeHistoryResolver) LocateTranscript(m persist.Meta, convID string) (string, resumeHistoryOutcome) {
	if !adapter.IsCanonicalConversationID(convID) {
		return "", resumeHistoryUnsafe
	}
	ad, _ := registry.New(m.AgentType)
	layout, byCwd := adapter.AsTranscriptLayout(ad)
	dated, byDay := adapter.AsDatedTranscriptLayout(ad)
	if !byCwd && !byDay {
		return "", resumeHistoryUnsupported
	}
	cwd := m.ProviderCwd() // the directory the AGENT ran in; see Resolve
	if !filepath.IsAbs(cwd) {
		return "", resumeHistoryNoMatch
	}
	if !filepath.IsAbs(r.home) {
		return "", resumeHistoryUnreadable
	}
	budget := &historyBudget{limits: r.limits}
	if !budget.valid() {
		return "", resumeHistoryUnsafe
	}
	home, outcome := r.openHome()
	if outcome != resumeHistoryFound {
		return "", outcome
	}
	defer func() { _ = home.Close() }()
	if byDay {
		day, ok := dated.TranscriptDay(convID)
		if !ok {
			return "", resumeHistoryNoMatch // a canonical id that names no day names no file
		}
		return r.locateCodexTranscript(home, budget, day, convID)
	}
	provider, providerAbs, closeProvider, outcome, ok := r.openProviderRoot(home, ".claude")
	if !ok {
		return "", outcome
	}
	defer closeProvider()
	project, absDir, closeProject, outcome, ok := r.openProviderChild(provider, providerAbs, "projects", layout.ProjectDirName(filepath.Clean(cwd)))
	if !ok {
		return "", outcome
	}
	defer closeProject()
	name := layout.TranscriptFileName(convID)
	// One extra Lstat, for the MESSAGE and not for the safety: openCandidate answers
	// Unreadable for both a missing file and an unreadable one, and "the transcript is
	// not there" is the failure an owner will actually hit -- capture is hook-driven, so
	// a session can hold an id whose file was since cleaned up. The safety decision is
	// still openCandidate's, below.
	if _, err := project.Lstat(name); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", resumeHistoryNoMatch
		}
		return "", resumeHistoryUnreadable
	}
	f, outcome := r.openCandidate(project, absDir, name, budget)
	if outcome != resumeHistoryFound {
		return "", outcome
	}
	defer func() { _ = f.Close() }()
	// A REGULAR FILE WITH THE RIGHT NAME IS NOT YET A TRANSCRIPT. Confinement, inode
	// identity and regular-file status say the daemon reached the file it meant to; they
	// say nothing about whether the file holds the conversation. A zero-byte file, one
	// truncated by a crash, or one holding unrelated bytes would otherwise be reported as
	// a successful handoff, and the successor would open it and find nothing -- which is
	// the context-free launch E7 forbids, arrived at by a route the refusals did not
	// cover. Found by adversarial review.
	//
	// The check stays inside "pointers only": it reads for IDENTITY, never for content,
	// and nothing it reads reaches the prompt.
	if outcome := claudeTranscriptNamesItsConversation(f, budget, convID); outcome != resumeHistoryFound {
		return "", outcome
	}
	return filepath.Join(absDir, name), resumeHistoryFound
}

// locateIdentityMaxRecords bounds how far into a transcript the identity check reads.
// Claude writes sessionId in its very first record, so one is the expected cost; the
// allowance exists only so a leading record without the field cannot defeat the check.
// It matters because a hands-off source's transcript can be tens of megabytes and this
// is a naming check, not a scan.
const locateIdentityMaxRecords = 16

// claudeTranscriptNamesItsConversation reports whether the open transcript actually
// claims the conversation whose name it carries.
//
// It is deliberately NOT parseClaudeHistory. That function additionally matches the cwd
// and the creation window, which is right when SEARCHING for an unknown id and wrong
// here: the id is already known, and the window would reject a legitimate transcript
// simply for being older than the swarm session that is handing off.
//
// A TRAILING PARTIAL RECORD IS BENIGN, and that is load-bearing rather than lenient: the
// source may still be RUNNING and appending -- that is the primary case this feature
// exists for -- so a half-written final line is the normal state of a live file, not
// evidence of corruption. Only a complete record can satisfy the check; an incomplete one
// simply ends the read.
func claudeTranscriptNamesItsConversation(f *os.File, budget *historyBudget, want string) resumeHistoryOutcome {
	if budget.limits.MaxRecordBytes > int64(int(^uint(0)>>1)-1) {
		return resumeHistoryUnsafe
	}
	reader := bufio.NewReaderSize(f, int(budget.limits.MaxRecordBytes)+1)
	for records := 0; records < locateIdentityMaxRecords; records++ {
		line, err := reader.ReadSlice('\n')
		if err != nil {
			// EOF, whole or partial: no complete record named the conversation.
			return resumeHistoryNoMatch
		}
		if int64(len(line)) > budget.limits.MaxRecordBytes || !budget.addBytes(len(line)) {
			return resumeHistoryUnsafe
		}
		top, ok := decodeStrictObject([]byte(strings.TrimSpace(string(line[:len(line)-1]))))
		if !ok {
			continue // an unparseable record is not an identity; keep looking, bounded
		}
		raw, bearing := top["sessionId"]
		if !bearing {
			continue
		}
		if id, idOK := strictJSONString(raw); idOK && id == want {
			return resumeHistoryFound
		}
		// A complete record naming a DIFFERENT conversation means this file is not the
		// one the name promises. Refuse rather than keep hunting.
		return resumeHistoryNoMatch
	}
	return resumeHistoryNoMatch
}

// locateCodexTranscript is the dated-layout locator, codex's exactly as resolveCodex
// is: the provider root is .codex and the tree beneath it is sessions/YYYY/MM/DD. The
// day is the id's own (adapter.DatedTranscriptLayout), tried FIRST so a busy neighbour
// cannot spend the entry budget before the right day is read; then the day after,
// because the id carries the millisecond the thread was minted while the file carries
// the second codex wrote it, and across midnight those differ (measured: of 1888 real
// rollouts, 28 are stamped later than their id, none earlier); then the day before,
// which only a machine filing rollouts by a local time behind UTC could ever need.
// Within a day the entries are LISTED, bounded by the budget, and only the one whose
// parsed id is convID is opened; every other entry is ignored rather than judged,
// since a stray file in a day directory is not this locator's business. A NAME HIT ENDS
// THE SEARCH: a file the name claims that holds no complete record naming the id is
// reported not found, and the other days are not tried for it, so a same-id file in a
// neighbouring day cannot answer for the one the id's own day already named.
func (r *filesystemResumeHistoryResolver) locateCodexTranscript(home *os.Root, budget *historyBudget, day time.Time, convID string) (string, resumeHistoryOutcome) {
	provider, providerAbs, closeProvider, outcome, ok := r.openProviderRoot(home, ".codex")
	if !ok {
		return "", outcome
	}
	defer closeProvider()
	sessions, sessionsAbs, closeSessions, outcome, ok := r.openProviderChild(provider, providerAbs, "sessions")
	if !ok {
		return "", outcome
	}
	defer closeSessions()
	for _, delta := range []int{0, 1, -1} {
		d := day.AddDate(0, 0, delta)
		parts := []string{d.Format("2006"), d.Format("01"), d.Format("02")}
		dayRoot, closeDay, dayOutcome, present := r.openDirPath(sessions, sessionsAbs, parts...)
		if !present {
			if dayOutcome == resumeHistoryNoMatch {
				continue
			}
			return "", dayOutcome
		}
		path, outcome, named := r.locateCodexInDay(dayRoot, filepath.Join(sessionsAbs, filepath.Join(parts...)), budget, convID)
		closeDay()
		if named || outcome != resumeHistoryNoMatch {
			return path, outcome
		}
	}
	return "", resumeHistoryNoMatch
}

// locateCodexInDay's third result says whether an entry in this day NAMED the id at all,
// so the caller can stop looking once one did, whatever it held.
func (r *filesystemResumeHistoryResolver) locateCodexInDay(dayRoot *os.Root, absDir string, budget *historyBudget, convID string) (string, resumeHistoryOutcome, bool) {
	entries, outcome := readDirBounded(dayRoot, budget)
	if outcome != resumeHistoryFound {
		return "", outcome, false
	}
	// Names first, so two files claiming one id (a same-user decoy, D7) fail closed
	// instead of whichever the directory lists first winning.
	name := ""
	for _, entry := range entries {
		if _, fileID, candidate, valid := parseCodexHistoryName(entry.Name()); candidate && valid && fileID == convID {
			if name != "" {
				return "", resumeHistoryAmbiguous, true
			}
			name = entry.Name()
		}
	}
	if name == "" {
		return "", resumeHistoryNoMatch, false
	}
	f, outcome := r.openCandidate(dayRoot, absDir, name, budget)
	if outcome != resumeHistoryFound {
		return "", outcome, true
	}
	line, outcome := readCompleteLine(f, budget)
	_ = f.Close()
	if outcome != resumeHistoryFound {
		return "", outcome, true
	}
	if !codexTranscriptNamesItsConversation(line, convID) {
		return "", resumeHistoryNoMatch, true
	}
	return filepath.Join(absDir, name), resumeHistoryFound, true
}

// codexTranscriptNamesItsConversation is the codex half of "a regular file with the
// right name is not yet a transcript" (claudeTranscriptNamesItsConversation): the first
// record must be a session_meta whose payload names convID. THERE IS NO CWD CLAUSE, and
// its absence is measured rather than lazy: codex records the app-server's working
// directory as the thread's, and under swarm the app-server runs in the session's state
// dir (internal/shim/backend.go), so every swarm-launched rollout names
// <stateDir>/sessions/<creating session> -- a resumed thread names an OLDER session's --
// and a clause requiring the source's checkout would refuse every real session (found
// by adversarial review against 1888 real rollouts). The identity the name promises is
// the id, and that is what is checked. It is deliberately NOT parseCodexSessionMeta,
// which also matches cwd and the creation window: right when searching for an unknown
// id, wrong when the id is known and the thread may be older than the session.
func codexTranscriptNamesItsConversation(line []byte, want string) bool {
	top, ok := decodeStrictObject([]byte(strings.TrimSpace(string(line))))
	if !ok {
		return false
	}
	if typ, ok := strictJSONString(top["type"]); !ok || typ != "session_meta" {
		return false
	}
	payload, ok := decodeStrictObject(top["payload"])
	if !ok {
		return false
	}
	id, ok := strictJSONString(payload["id"])
	return ok && id == want
}

func (r *filesystemResumeHistoryResolver) openHome() (*os.Root, resumeHistoryOutcome) {
	clean := filepath.Clean(r.home)
	before, err := os.Lstat(clean)
	if err != nil {
		return nil, resumeHistoryUnreadable
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.IsDir() {
		return nil, resumeHistoryUnsafe
	}
	root, err := os.OpenRoot(clean)
	if err != nil {
		return nil, resumeHistoryUnreadable
	}
	f, err := root.Open(".")
	if err != nil {
		_ = root.Close()
		return nil, resumeHistoryUnreadable
	}
	after, statErr := f.Stat()
	_ = f.Close()
	if statErr != nil || !os.SameFile(before, after) {
		_ = root.Close()
		return nil, resumeHistoryUnsafe
	}
	return root, resumeHistoryFound
}

// openProviderRoot applies the resolver's sole symlink exception. The stable
// ~/.codex and ~/.claude aliases used on this VM may be absolute links to strict,
// clean descendants of trusted HOME. Their captured targets are traversed one
// component at a time through the already anchored home root, and the alias inode
// and target text are revalidated after traversal. Every link below this first
// component remains forbidden by openDirPath.
func (r *filesystemResumeHistoryResolver) openProviderRoot(home *os.Root, provider string) (*os.Root, string, func(), resumeHistoryOutcome, bool) {
	homeAbs := filepath.Clean(r.home)
	aliasAbs := filepath.Join(homeAbs, provider)
	before, err := home.Lstat(provider)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, "", func() {}, resumeHistoryNoMatch, false
		}
		return nil, "", func() {}, resumeHistoryUnreadable, false
	}
	if before.Mode()&os.ModeSymlink == 0 {
		root, closeRoot, outcome, ok := r.openDirPath(home, homeAbs, provider)
		return root, aliasAbs, closeRoot, outcome, ok
	}
	if provider != ".codex" && provider != ".claude" {
		return nil, "", func() {}, resumeHistoryUnsafe, false
	}
	if r.beforeAliasReadlink != nil {
		r.beforeAliasReadlink(aliasAbs)
	}
	target, err := home.Readlink(provider)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, "", func() {}, resumeHistoryUnreadable, false
		}
		return nil, "", func() {}, resumeHistoryUnsafe, false
	}
	// Readlink is not an opening operation, so verify the inspected link was not
	// replaced between Lstat and Readlink before trusting the returned text.
	readInfo, err := home.Lstat(provider)
	if err != nil || readInfo.Mode()&os.ModeSymlink == 0 || !os.SameFile(before, readInfo) {
		return nil, "", func() {}, resumeHistoryUnsafe, false
	}
	components, ok := providerAliasTarget(homeAbs, target)
	if !ok {
		return nil, "", func() {}, resumeHistoryUnsafe, false
	}
	if r.beforeAliasTargetOpen != nil {
		r.beforeAliasTargetOpen(aliasAbs)
	}
	targetRoot, closeTarget, outcome, opened := r.openDirPath(home, homeAbs, components...)
	if !opened {
		if outcome == resumeHistoryNoMatch {
			outcome = resumeHistoryUnreadable
		}
		return nil, "", func() {}, outcome, false
	}
	// The opened target is independent of the alias now, but the compatibility
	// contract is explicitly for a stable alias. A replacement or retarget at any
	// point in target traversal therefore fails closed.
	if !stableProviderAlias(home, provider, before, target) {
		closeTarget()
		return nil, "", func() {}, resumeHistoryUnsafe, false
	}
	return targetRoot, filepath.Join(append([]string{homeAbs}, components...)...), closeTarget, resumeHistoryFound, true
}

func stableProviderAlias(home *os.Root, provider string, original os.FileInfo, target string) bool {
	beforeRead, err := home.Lstat(provider)
	if err != nil || beforeRead.Mode()&os.ModeSymlink == 0 || !os.SameFile(original, beforeRead) {
		return false
	}
	currentTarget, err := home.Readlink(provider)
	if err != nil || currentTarget != target {
		return false
	}
	afterRead, err := home.Lstat(provider)
	return err == nil && afterRead.Mode()&os.ModeSymlink != 0 &&
		os.SameFile(original, afterRead) && os.SameFile(beforeRead, afterRead)
}

func providerAliasTarget(home, target string) ([]string, bool) {
	if !filepath.IsAbs(target) || filepath.Clean(target) != target {
		return nil, false
	}
	for _, component := range strings.Split(target, string(os.PathSeparator)) {
		if component == ".." {
			return nil, false
		}
	}
	rel, err := filepath.Rel(home, target)
	if err != nil || rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return nil, false
	}
	components := strings.Split(rel, string(os.PathSeparator))
	if len(components) > providerAliasMaxDepth {
		return nil, false
	}
	for _, component := range components {
		if component == "" || component == "." || component == ".." {
			return nil, false
		}
	}
	return components, true
}

// openDirPath traverses one component at a time through os.Root. Each component
// is lstat'd, exposed to the deterministic race seam, opened relative to the
// already anchored parent, then verified with os.SameFile.
func (r *filesystemResumeHistoryResolver) openDirPath(base *os.Root, baseAbs string, components ...string) (*os.Root, func(), resumeHistoryOutcome, bool) {
	current := base
	abs := baseAbs
	var opened []*os.Root
	cleanup := func() {
		for i := len(opened) - 1; i >= 0; i-- {
			_ = opened[i].Close()
		}
	}
	for _, component := range components {
		if component == "" || component == "." || component == ".." || filepath.Base(component) != component {
			cleanup()
			return nil, func() {}, resumeHistoryUnsafe, false
		}
		before, err := current.Lstat(component)
		if err != nil {
			cleanup()
			if errors.Is(err, os.ErrNotExist) {
				return nil, func() {}, resumeHistoryNoMatch, false
			}
			return nil, func() {}, resumeHistoryUnreadable, false
		}
		if before.Mode()&os.ModeSymlink != 0 || !before.IsDir() {
			cleanup()
			return nil, func() {}, resumeHistoryUnsafe, false
		}
		abs = filepath.Join(abs, component)
		if r.beforeOpen != nil {
			r.beforeOpen(abs)
		}
		child, err := current.OpenRoot(component)
		if err != nil {
			cleanup()
			if errors.Is(err, os.ErrNotExist) {
				return nil, func() {}, resumeHistoryUnreadable, false
			}
			return nil, func() {}, resumeHistoryUnsafe, false
		}
		f, err := child.Open(".")
		if err != nil {
			_ = child.Close()
			cleanup()
			if errors.Is(err, os.ErrNotExist) {
				return nil, func() {}, resumeHistoryUnreadable, false
			}
			return nil, func() {}, resumeHistoryUnsafe, false
		}
		after, statErr := f.Stat()
		_ = f.Close()
		if statErr != nil || !os.SameFile(before, after) {
			_ = child.Close()
			cleanup()
			return nil, func() {}, resumeHistoryUnsafe, false
		}
		opened = append(opened, child)
		current = child
	}
	return current, cleanup, resumeHistoryFound, true
}

func readDirBounded(root *os.Root, budget *historyBudget) ([]os.DirEntry, resumeHistoryOutcome) {
	remaining := budget.limits.MaxEntries - budget.entries
	if remaining < 0 {
		return nil, resumeHistoryUnsafe
	}
	f, err := root.Open(".")
	if err != nil {
		return nil, resumeHistoryUnreadable
	}
	defer func() { _ = f.Close() }()
	entries, err := f.ReadDir(remaining + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, resumeHistoryUnreadable
	}
	if !budget.addEntries(len(entries)) {
		return nil, resumeHistoryUnsafe
	}
	return entries, resumeHistoryFound
}

func (r *filesystemResumeHistoryResolver) openCandidate(root *os.Root, absDir, name string, budget *historyBudget) (*os.File, resumeHistoryOutcome) {
	before, err := root.Lstat(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, resumeHistoryUnreadable
		}
		return nil, resumeHistoryUnreadable
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return nil, resumeHistoryUnsafe
	}
	if !budget.openFile() {
		return nil, resumeHistoryUnsafe
	}
	if r.beforeOpen != nil {
		r.beforeOpen(filepath.Join(absDir, name))
	}
	// O_NONBLOCK prevents a regular file replaced after Lstat with a FIFO from
	// hanging the daemon before the opened inode can be verified below.
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, resumeHistoryUnreadable
		}
		return nil, resumeHistoryUnsafe
	}
	after, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, resumeHistoryUnreadable
	}
	if !os.SameFile(before, after) || !after.Mode().IsRegular() {
		_ = f.Close()
		return nil, resumeHistoryUnsafe
	}
	return f, resumeHistoryFound
}

type codexHistoryCandidate struct {
	id     string
	parent string
}

func (r *filesystemResumeHistoryResolver) resolveCodex(home *os.Root, budget *historyBudget, m persist.Meta) resumeHistoryResult {
	provider, providerAbs, closeProvider, outcome, ok := r.openProviderRoot(home, ".codex")
	if !ok {
		return resumeHistoryResult{Outcome: outcome}
	}
	defer closeProvider()
	sessions, sessionsAbs, closeSessions, outcome, ok := r.openProviderChild(provider, providerAbs, "sessions")
	if !ok {
		return resumeHistoryResult{Outcome: outcome}
	}
	defer closeSessions()
	cleanCWD := filepath.Clean(m.ProviderCwd())
	var candidates []codexHistoryCandidate
	for delta := -1; delta <= 1; delta++ {
		day := m.CreatedAt.UTC().AddDate(0, 0, delta)
		parts := []string{day.Format("2006"), day.Format("01"), day.Format("02")}
		dayRoot, closeDay, dayOutcome, present := r.openDirPath(sessions, sessionsAbs, parts...)
		if !present {
			if dayOutcome == resumeHistoryNoMatch {
				continue
			}
			return resumeHistoryResult{Outcome: dayOutcome}
		}
		entries, listOutcome := readDirBounded(dayRoot, budget)
		if listOutcome != resumeHistoryFound {
			closeDay()
			return resumeHistoryResult{Outcome: listOutcome}
		}
		absDir := filepath.Join(sessionsAbs, filepath.Join(parts...))
		for _, entry := range entries {
			fileTime, fileID, candidate, valid := parseCodexHistoryName(entry.Name())
			if !candidate {
				continue
			}
			if !valid {
				closeDay()
				return resumeHistoryResult{Outcome: resumeHistoryUnsafe}
			}
			f, openOutcome := r.openCandidate(dayRoot, absDir, entry.Name(), budget)
			if openOutcome != resumeHistoryFound {
				closeDay()
				return resumeHistoryResult{Outcome: openOutcome}
			}
			line, readOutcome := readCompleteLine(f, budget)
			_ = f.Close()
			if readOutcome == resumeHistoryNoMatch {
				continue // no complete first line: not a record (ADR-010 Amendment 7 H2)
			}
			if readOutcome != resumeHistoryFound {
				closeDay()
				return resumeHistoryResult{Outcome: readOutcome}
			}
			candidateValue, matched, parseOutcome := parseCodexSessionMeta(line, fileID, fileTime, cleanCWD, m.CreatedAt)
			if parseOutcome == resumeHistoryNoMatch {
				continue // a torn first record: not a record (ADR-010 Amendment 7 H2)
			}
			if parseOutcome != resumeHistoryFound {
				closeDay()
				return resumeHistoryResult{Outcome: parseOutcome}
			}
			if matched {
				candidates = append(candidates, candidateValue)
			}
		}
		closeDay()
	}
	return resolveCodexCandidateGraph(candidates)
}

func (r *filesystemResumeHistoryResolver) openProviderChild(provider *os.Root, providerAbs string, components ...string) (*os.Root, string, func(), resumeHistoryOutcome, bool) {
	root, cleanup, outcome, ok := r.openDirPath(provider, providerAbs, components...)
	return root, filepath.Join(append([]string{providerAbs}, components...)...), cleanup, outcome, ok
}

func parseCodexHistoryName(name string) (time.Time, string, bool, bool) {
	if !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") {
		return time.Time{}, "", false, false
	}
	const stampLen = len("2006-01-02T15-04-05")
	body := strings.TrimSuffix(strings.TrimPrefix(name, "rollout-"), ".jsonl")
	if len(body) != stampLen+1+36 || body[stampLen] != '-' {
		return time.Time{}, "", true, false
	}
	stamp, id := body[:stampLen], body[stampLen+1:]
	when, err := time.ParseInLocation("2006-01-02T15-04-05", stamp, time.UTC)
	return when, id, true, err == nil && adapter.IsCanonicalConversationID(id)
}

func readCompleteLine(f *os.File, budget *historyBudget) ([]byte, resumeHistoryOutcome) {
	if budget.limits.MaxRecordBytes > int64(int(^uint(0)>>1)-1) {
		return nil, resumeHistoryUnsafe
	}
	reader := bufio.NewReaderSize(f, int(budget.limits.MaxRecordBytes)+1)
	line, err := reader.ReadSlice('\n')
	if errors.Is(err, io.EOF) {
		// Empty, or still being written: no complete first line to judge (ADR-010
		// Amendment 7 H2). The partial bytes were still read, so they are still charged.
		// An over-long line is bufio.ErrBufferFull and stays unsafe.
		if !budget.addBytes(len(line)) {
			return nil, resumeHistoryUnsafe
		}
		return nil, resumeHistoryNoMatch
	}
	if err != nil {
		return nil, resumeHistoryUnsafe
	}
	if int64(len(line)) > budget.limits.MaxRecordBytes || !budget.addBytes(len(line)) {
		return nil, resumeHistoryUnsafe
	}
	return append([]byte(nil), line[:len(line)-1]...), resumeHistoryFound
}

// tornRecord reports a line that is not one syntactically complete JSON value: a torn
// write, which codex 0.151 produces while writing sub-agent headers (measured), and not
// a record anyone can be refused on (ADR-010 Amendment 7 H2). A value that parses but
// violates the strict schema is still decodeStrictObject's to refuse.
func tornRecord(line []byte) bool {
	var syntaxErr *json.SyntaxError
	err := json.NewDecoder(bytes.NewReader(line)).Decode(new(json.RawMessage))
	return errors.As(err, &syntaxErr) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)
}

func parseCodexSessionMeta(line []byte, fileID string, fileTime time.Time, cleanCWD string, created time.Time) (codexHistoryCandidate, bool, resumeHistoryOutcome) {
	trimmed := []byte(strings.TrimSpace(string(line)))
	if tornRecord(trimmed) {
		return codexHistoryCandidate{}, false, resumeHistoryNoMatch
	}
	top, ok := decodeStrictObject(trimmed)
	if !ok {
		return codexHistoryCandidate{}, false, resumeHistoryUnsafe
	}
	typ, ok := strictJSONString(top["type"])
	if !ok || typ != "session_meta" {
		return codexHistoryCandidate{}, false, resumeHistoryUnsafe
	}
	payload, ok := decodeStrictObject(top["payload"])
	if !ok {
		return codexHistoryCandidate{}, false, resumeHistoryUnsafe
	}
	id, idOK := strictJSONString(payload["id"])
	stamp, stampOK := strictJSONString(payload["timestamp"])
	cwd, cwdOK := strictJSONString(payload["cwd"])
	if !idOK || !stampOK || !cwdOK || !adapter.IsCanonicalConversationID(id) || id != fileID {
		return codexHistoryCandidate{}, false, resumeHistoryUnsafe
	}
	when, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil || when.UTC().Format("2006-01-02T15-04-05") != fileTime.UTC().Format("2006-01-02T15-04-05") {
		return codexHistoryCandidate{}, false, resumeHistoryUnsafe
	}
	parent := ""
	if raw, exists := payload["parent_thread_id"]; exists {
		if string(raw) != "null" {
			parent, ok = strictJSONString(raw)
			if !ok || !adapter.IsCanonicalConversationID(parent) {
				return codexHistoryCandidate{}, false, resumeHistoryUnsafe
			}
		}
	}
	if !filepath.IsAbs(cwd) || filepath.Clean(cwd) != cleanCWD || !withinResumeWindow(created, when) {
		return codexHistoryCandidate{}, false, resumeHistoryFound
	}
	return codexHistoryCandidate{id: id, parent: parent}, true, resumeHistoryFound
}

func withinResumeWindow(created, candidate time.Time) bool {
	delta := candidate.Sub(created)
	return delta >= -2*time.Second && delta <= 30*time.Second
}

func resolveCodexCandidateGraph(candidates []codexHistoryCandidate) resumeHistoryResult {
	if len(candidates) == 0 {
		return resumeHistoryResult{Outcome: resumeHistoryNoMatch}
	}
	byID := make(map[string]codexHistoryCandidate, len(candidates))
	for _, candidate := range candidates {
		if _, duplicate := byID[candidate.id]; duplicate {
			return resumeHistoryResult{Outcome: resumeHistoryAmbiguous}
		}
		byID[candidate.id] = candidate
	}
	rootFor := func(start string) (string, bool) {
		seen := make(map[string]struct{})
		current := start
		for {
			if _, cycle := seen[current]; cycle {
				return "", false
			}
			seen[current] = struct{}{}
			candidate := byID[current]
			if candidate.parent == "" {
				return current, true
			}
			if _, local := byID[candidate.parent]; !local {
				return current, true
			}
			current = candidate.parent
		}
	}
	root := ""
	for id := range byID {
		candidateRoot, ok := rootFor(id)
		if !ok || root != "" && root != candidateRoot {
			return resumeHistoryResult{Outcome: resumeHistoryAmbiguous}
		}
		root = candidateRoot
	}
	return resumeHistoryResult{Outcome: resumeHistoryFound, ConversationID: root}
}

func (r *filesystemResumeHistoryResolver) resolveClaude(home *os.Root, budget *historyBudget, m persist.Meta) resumeHistoryResult {
	cleanCWD := filepath.Clean(m.ProviderCwd())
	// ONE DEFINITION, ASKED OF THE PROVIDER. The project-directory encoding is Claude's
	// own knowledge, so it comes from the adapter's optional TranscriptLayout seam rather
	// than from a private copy here: the hands-off composer needs the very same directory,
	// and two copies of an encoder are two chances to disagree. The seam only NAMES the
	// directory -- it opens nothing -- so every traversal below is still this resolver's
	// anchored, budgeted os.Root walk.
	//
	// An unregistered agent type yields a nil Adapter, whose type assertion is false, so
	// the one branch covers both "no such adapter" and "no characterized layout". Absence
	// is the signal (ADR-010 section 5).
	ad, _ := registry.New(m.AgentType)
	layout, ok := adapter.AsTranscriptLayout(ad)
	if !ok {
		return resumeHistoryResult{Outcome: resumeHistoryUnsupported}
	}
	projectName := layout.ProjectDirName(cleanCWD)
	provider, providerAbs, closeProvider, outcome, ok := r.openProviderRoot(home, ".claude")
	if !ok {
		return resumeHistoryResult{Outcome: outcome}
	}
	defer closeProvider()
	project, absDir, closeProject, outcome, ok := r.openProviderChild(provider, providerAbs, "projects", projectName)
	if !ok {
		return resumeHistoryResult{Outcome: outcome}
	}
	defer closeProject()
	entries, outcome := readDirBounded(project, budget)
	if outcome != resumeHistoryFound {
		return resumeHistoryResult{Outcome: outcome}
	}
	var matches []string
	for _, entry := range entries {
		id, candidate, valid := parseClaudeHistoryName(entry.Name())
		if !candidate {
			continue
		}
		if !valid {
			return resumeHistoryResult{Outcome: resumeHistoryUnsafe}
		}
		f, openOutcome := r.openCandidate(project, absDir, entry.Name(), budget)
		if openOutcome != resumeHistoryFound {
			return resumeHistoryResult{Outcome: openOutcome}
		}
		matched, parseOutcome := parseClaudeHistory(f, budget, id, cleanCWD, m.CreatedAt)
		_ = f.Close()
		if parseOutcome != resumeHistoryFound && parseOutcome != resumeHistoryNoMatch {
			return resumeHistoryResult{Outcome: parseOutcome}
		}
		if matched {
			matches = append(matches, id)
		}
	}
	switch len(matches) {
	case 0:
		return resumeHistoryResult{Outcome: resumeHistoryNoMatch}
	case 1:
		return resumeHistoryResult{Outcome: resumeHistoryFound, ConversationID: matches[0]}
	default:
		return resumeHistoryResult{Outcome: resumeHistoryAmbiguous}
	}
}

func parseClaudeHistoryName(name string) (string, bool, bool) {
	if !strings.HasSuffix(name, ".jsonl") {
		return "", false, false
	}
	id := strings.TrimSuffix(name, ".jsonl")
	if len(id) != 36 {
		return "", false, false
	}
	return id, true, adapter.IsCanonicalConversationID(id)
}

func parseClaudeHistory(f *os.File, budget *historyBudget, fileID, cleanCWD string, created time.Time) (bool, resumeHistoryOutcome) {
	if budget.limits.MaxRecordBytes > int64(int(^uint(0)>>1)-1) {
		return false, resumeHistoryUnsafe
	}
	reader := bufio.NewReaderSize(f, int(budget.limits.MaxRecordBytes)+1)
	for {
		line, err := reader.ReadSlice('\n')
		if errors.Is(err, io.EOF) && len(line) == 0 {
			return false, resumeHistoryNoMatch
		}
		if err != nil || int64(len(line)) > budget.limits.MaxRecordBytes || !budget.addBytes(len(line)) {
			return false, resumeHistoryUnsafe
		}
		top, ok := decodeStrictObject([]byte(strings.TrimSpace(string(line[:len(line)-1]))))
		if !ok {
			return false, resumeHistoryUnsafe
		}
		rawID, bearing := top["sessionId"]
		if !bearing {
			continue
		}
		id, idOK := strictJSONString(rawID)
		if !idOK || !adapter.IsCanonicalConversationID(id) || id != fileID {
			return false, resumeHistoryUnsafe
		}
		rawCWD, hasCWD := top["cwd"]
		rawStamp, hasStamp := top["timestamp"]
		if !hasCWD && !hasStamp {
			continue // canonical identity-only prefix record; wait for evidence
		}
		if !hasCWD || !hasStamp {
			return false, resumeHistoryUnsafe
		}
		cwd, cwdOK := strictJSONString(rawCWD)
		stamp, stampOK := strictJSONString(rawStamp)
		if !cwdOK || !stampOK {
			return false, resumeHistoryUnsafe
		}
		when, parseErr := time.Parse(time.RFC3339Nano, stamp)
		if parseErr != nil {
			return false, resumeHistoryUnsafe
		}
		return filepath.IsAbs(cwd) && filepath.Clean(cwd) == cleanCWD && withinResumeWindow(created, when), resumeHistoryFound
	}
}
