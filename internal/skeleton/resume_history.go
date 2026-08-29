package skeleton

import (
	"bufio"
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
	// resumeHistoryCompressed is a usable provider history whose storage format
	// cannot safely be handed to an arbitrary successor CLI as a readable path.
	// It is distinct from Unreadable: retrying permissions will not make a zstd
	// rollout into the plaintext JSONL the hands-off prompt promises.
	resumeHistoryCompressed
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
// confinement below the opened root, equality between each inspected inode and the
// inode subsequently opened, IsRegular, and O_RDONLY|O_NONBLOCK against a planted
// FIFO -- are properties of a FILE DESCRIPTOR, and this function returns a STRING.
// None of them survive serialization into a name that another
// process opens minutes later. Concretely, the returned path does NOT promise that the
// file still exists at open time, that it is the same inode, that no component became a
// symlink in between, that it is still a regular file, or that the successor's process
// resolves the same string to the same file. What it does promise is confinement BY
// CONSTRUCTION: swarm did not invent this path, every direct symlink observed by its
// pre-open checks is refused, and os.Root cannot resolve outside the trusted root. A
// symlink introduced by a concurrent rename may still be resolved when it stays inside
// that root and reaches the same inspected inode, so the contract does not claim a
// portable categorical no-follow guarantee. Every segment is provably a single
// separator-free component -- homeAbs is Clean'd and IsAbs-checked; provider and fixed
// subdirectory names are literals; providerAliasTarget proves the one supported root
// alias a clean, ".."-free strict descendant of HOME; Claude's ProjectDirName emits only
// [A-Za-z0-9-]; Codex date components are fixed-width decimal values; and conversation
// IDs and rollout names pass strict parsers. There is no input under which the assembled
// string leaves the selected provider root. (The argument is the COMPONENTS', not filepath.Join's: Join
// cleans, and cleaning is what would turn "../.." into an escape rather than stop it.)
// The machinery's real job here is stopping a malformed or hostile input from steering
// the DAEMON's own reads, and that job is done in full.
//
// Claude's pure naming layout and Codex's dated-tree search deliberately meet only
// at this anchored resolver. An adapter seam cannot describe Codex's one-to-many
// thread/revert layout without performing the filesystem search that belongs here.
func (r *filesystemResumeHistoryResolver) LocateTranscript(m persist.Meta, convID string) (string, resumeHistoryOutcome) {
	if !adapter.IsCanonicalConversationID(convID) {
		return "", resumeHistoryUnsafe
	}
	if m.AgentType != "claude" && m.AgentType != "codex" {
		return "", resumeHistoryUnsupported
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
	if m.AgentType == "codex" {
		return r.locateCodexTranscript(home, budget, convID)
	}
	// Absence is the signal (ADR-010 section 5): an adapter without a characterized
	// layout is not asserted into the interface, and a stub returning "" would look like
	// an answer and send an anchored open at a directory named "".
	ad, _ := registry.New(m.AgentType)
	layout, ok := adapter.AsTranscriptLayout(ad)
	if !ok {
		return "", resumeHistoryUnsupported
	}
	cwd := m.ProviderCwd() // the directory the AGENT ran in; see Resolve
	if !filepath.IsAbs(cwd) {
		return "", resumeHistoryNoMatch
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
	id        string
	parent    string
	when      time.Time
	rolloutID string
	reverted  bool
}

type codexRolloutName struct {
	when       time.Time
	threadID   string
	rolloutID  string
	compressed bool
	reverted   bool
}

// codexTranscriptCandidate is one path-shaped match for a KNOWN Codex thread.
// threadID stays in the parsed name only long enough to match the caller; rolloutID
// is the distinct UUID Codex adds after '_' when thread/revert creates a new
// immutable rollout while keeping the thread identity stable.
type codexTranscriptCandidate struct {
	year       string
	month      string
	day        string
	name       string
	when       time.Time
	rolloutID  string
	compressed bool
}

// locateCodexTranscript performs Codex's filesystem fallback for a KNOWN thread
// identity. That is intentionally a different search from resolveCodex: recovery
// correlates an unknown thread to one swarm launch in a narrow time/cwd window,
// whereas a known thread can acquire newer rollout files days later through
// thread/revert.
//
// The traversal has exactly three variable directory levels (YYYY/MM/DD), counts
// every directory entry against the shared budget, and opens no rollout until the
// newest matching name has been selected. Codex filenames have only second
// precision, so the rollout UUID is the deterministic same-second tie breaker --
// the same ordering used by Codex's own filesystem fallback.
func (r *filesystemResumeHistoryResolver) locateCodexTranscript(home *os.Root, budget *historyBudget, threadID string) (string, resumeHistoryOutcome) {
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

	var best codexTranscriptCandidate
	found := false
	ambiguous := false
	years, outcome := readDirBounded(sessions, budget)
	if outcome != resumeHistoryFound {
		return "", outcome
	}
	for _, yearEntry := range years {
		year := yearEntry.Name()
		if !decimalComponent(year, 4, 0, 9999) {
			continue
		}
		yearRoot, closeYear, outcome, opened := r.openDirPath(sessions, sessionsAbs, year)
		if !opened {
			return "", outcome
		}
		months, outcome := readDirBounded(yearRoot, budget)
		if outcome != resumeHistoryFound {
			closeYear()
			return "", outcome
		}
		for _, monthEntry := range months {
			month := monthEntry.Name()
			if !decimalComponent(month, 2, 1, 12) {
				continue
			}
			monthRoot, closeMonth, outcome, opened := r.openDirPath(yearRoot, filepath.Join(sessionsAbs, year), month)
			if !opened {
				closeYear()
				return "", outcome
			}
			days, outcome := readDirBounded(monthRoot, budget)
			if outcome != resumeHistoryFound {
				closeMonth()
				closeYear()
				return "", outcome
			}
			for _, dayEntry := range days {
				day := dayEntry.Name()
				dateText := year + "-" + month + "-" + day
				if !decimalComponent(day, 2, 1, 31) {
					continue
				}
				if _, err := time.Parse("2006-01-02", dateText); err != nil {
					continue
				}
				monthAbs := filepath.Join(sessionsAbs, year, month)
				dayRoot, closeDay, outcome, opened := r.openDirPath(monthRoot, monthAbs, day)
				if !opened {
					closeMonth()
					closeYear()
					return "", outcome
				}
				entries, outcome := readDirBounded(dayRoot, budget)
				if outcome != resumeHistoryFound {
					closeDay()
					closeMonth()
					closeYear()
					return "", outcome
				}
				for _, entry := range entries {
					parsed, valid := parseCodexTranscriptName(entry.Name())
					if !valid {
						// A malformed name for ANOTHER thread is irrelevant noise,
						// but a malformed current/revert name for the requested
						// thread could otherwise hide a newer rollout and make us
						// silently hand off stale history.
						if codexRolloutNameTargetsThread(entry.Name(), threadID) {
							closeDay()
							closeMonth()
							closeYear()
							return "", resumeHistoryUnsafe
						}
						continue
					}
					if parsed.threadID != threadID {
						continue
					}
					// A matching name in the wrong dated directory is not a layout
					// swarm has characterized. Refuse instead of returning a path whose
					// components contradict the identity encoded by its basename.
					if parsed.when.UTC().Format("2006-01-02") != dateText {
						closeDay()
						closeMonth()
						closeYear()
						return "", resumeHistoryUnsafe
					}
					candidate := codexTranscriptCandidate{
						year: year, month: month, day: day, name: entry.Name(),
						when: parsed.when, rolloutID: parsed.rolloutID, compressed: parsed.compressed,
					}
					if !found {
						best, found = candidate, true
						continue
					}
					switch compareCodexTranscriptCandidate(candidate, best) {
					case 1:
						best, found, ambiguous = candidate, true, false
					case 0:
						if found && candidate.name != best.name {
							// Codex compression keeps the logical rollout basename
							// and adds only .zst. During an interrupted cleanup both
							// siblings can exist; Codex itself prefers the plaintext
							// file, and so do we. Any other equal-key pair has no
							// characterized winner.
							if strings.TrimSuffix(candidate.name, ".zst") == strings.TrimSuffix(best.name, ".zst") {
								if best.compressed && !candidate.compressed {
									best = candidate
								}
							} else {
								ambiguous = true
							}
						}
					}
				}
				closeDay()
			}
			closeMonth()
		}
		closeYear()
	}
	if !found {
		return "", resumeHistoryNoMatch
	}
	if ambiguous {
		return "", resumeHistoryAmbiguous
	}

	// Re-open only the selected path through the anchor. Holding every day root
	// across a full-tree scan would turn the entry budget into an fd-exhaustion bug.
	dayRoot, closeDay, outcome, ok := r.openDirPath(sessions, sessionsAbs, best.year, best.month, best.day)
	if !ok {
		return "", outcome
	}
	defer closeDay()
	absDir := filepath.Join(sessionsAbs, best.year, best.month, best.day)
	f, outcome := r.openCandidate(dayRoot, absDir, best.name, budget)
	if outcome != resumeHistoryFound {
		return "", outcome
	}
	defer func() { _ = f.Close() }()
	if best.compressed {
		// Do not point an arbitrary successor CLI at a zstd stream and call it a
		// readable transcript, and do not silently choose an older plaintext
		// rollout: that would hand off stale state.
		return "", resumeHistoryCompressed
	}
	line, outcome := readCompleteLine(f, budget)
	if outcome != resumeHistoryFound {
		return "", outcome
	}
	if outcome := codexTranscriptNamesItsConversation(line, threadID); outcome != resumeHistoryFound {
		return "", outcome
	}
	return filepath.Join(absDir, best.name), resumeHistoryFound
}

func decimalComponent(value string, width, min, max int) bool {
	if len(value) != width {
		return false
	}
	n := 0
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
		n = n*10 + int(value[i]-'0')
	}
	return n >= min && n <= max
}

// parseCodexTranscriptName accepts both ordinary and reverted rollout names, and
// recognizes Codex's cold-history suffix without attempting to decode it.
func parseCodexTranscriptName(name string) (codexRolloutName, bool) {
	compressed := strings.HasSuffix(name, ".jsonl.zst")
	plainName := strings.TrimSuffix(name, ".zst")
	if !strings.HasPrefix(plainName, "rollout-") || !strings.HasSuffix(plainName, ".jsonl") {
		return codexRolloutName{}, false
	}
	core := strings.TrimSuffix(strings.TrimPrefix(plainName, "rollout-"), ".jsonl")
	const stampLen = len("2006-01-02T15-04-05")
	if len(core) < stampLen+1 || core[stampLen] != '-' {
		return codexRolloutName{}, false
	}
	when, err := time.ParseInLocation("2006-01-02T15-04-05", core[:stampLen], time.UTC)
	if err != nil {
		return codexRolloutName{}, false
	}
	ids := core[stampLen+1:]
	threadID, rolloutID := ids, ids
	reverted := false
	if strings.Count(ids, "_") == 1 {
		threadID, rolloutID, _ = strings.Cut(ids, "_")
		reverted = true
	} else if strings.Contains(ids, "_") {
		return codexRolloutName{}, false
	}
	if !adapter.IsCanonicalConversationID(threadID) || !adapter.IsCanonicalConversationID(rolloutID) {
		return codexRolloutName{}, false
	}
	return codexRolloutName{
		when: when, threadID: threadID, rolloutID: rolloutID,
		compressed: compressed, reverted: reverted,
	}, true
}

// codexRolloutNameTargetsThread recognizes the fixed-position, exact thread token
// in a rollout-shaped basename without accepting the remainder as valid. It exists
// for the failure side of parsing: once the canonical token is followed by its only
// legal boundaries ('_' for a revert, '.' for an extension, or end-of-name), a bad
// rollout UUID/remainder/extension belongs to the requested thread and must block a
// stale fallback. A different UUID, a longer token, or prose merely containing the
// target does not gain that power.
func codexRolloutNameTargetsThread(name, threadID string) bool {
	const stampLen = len("2006-01-02T15-04-05")
	const prefix = "rollout-"
	threadStart := len(prefix) + stampLen + 1
	threadEnd := threadStart + len(threadID)
	if !strings.HasPrefix(name, prefix) || len(name) < threadEnd || name[len(prefix)+stampLen] != '-' {
		return false
	}
	if name[threadStart:threadEnd] != threadID {
		return false
	}
	if len(name) == threadEnd {
		return true
	}
	return name[threadEnd] == '_' || name[threadEnd] == '.'
}

// compareCodexTranscriptCandidate returns the ordering of a against b. Canonical
// UUID spellings preserve byte order under lexical comparison, so no UUID package
// or second parser is needed for Codex's same-second tie breaker.
func compareCodexTranscriptCandidate(a, b codexTranscriptCandidate) int {
	if a.when.After(b.when) {
		return 1
	}
	if a.when.Before(b.when) {
		return -1
	}
	return strings.Compare(a.rolloutID, b.rolloutID)
}

func codexTranscriptNamesItsConversation(line []byte, want string) resumeHistoryOutcome {
	top, ok := decodeStrictObject([]byte(strings.TrimSpace(string(line))))
	if !ok {
		return resumeHistoryUnsafe
	}
	typ, ok := strictJSONString(top["type"])
	if !ok || typ != "session_meta" {
		return resumeHistoryNoMatch
	}
	payload, ok := decodeStrictObject(top["payload"])
	if !ok {
		return resumeHistoryUnsafe
	}
	id, ok := strictJSONString(payload["id"])
	if !ok || !adapter.IsCanonicalConversationID(id) {
		return resumeHistoryUnsafe
	}
	if id != want {
		return resumeHistoryNoMatch
	}
	return resumeHistoryFound
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
			parsed, valid := parseCodexTranscriptName(entry.Name())
			candidate := codexRolloutCandidateName(entry.Name())
			if !candidate {
				continue
			}
			if !valid {
				closeDay()
				return resumeHistoryResult{Outcome: resumeHistoryUnsafe}
			}
			if parsed.when.UTC().Format("2006-01-02") != day.Format("2006-01-02") {
				closeDay()
				return resumeHistoryResult{Outcome: resumeHistoryUnsafe}
			}
			if parsed.compressed {
				// Recovery has no captured thread identity, so a compressed
				// basename cannot be correlated to this source safely: cwd and the
				// metadata identity are inside the zstd stream. Ignore it exactly as
				// an unavailable candidate; the known-ID locator reports Compressed.
				continue
			}
			f, openOutcome := r.openCandidate(dayRoot, absDir, entry.Name(), budget)
			if openOutcome != resumeHistoryFound {
				closeDay()
				return resumeHistoryResult{Outcome: openOutcome}
			}
			line, readOutcome := readCompleteLine(f, budget)
			_ = f.Close()
			if readOutcome != resumeHistoryFound {
				closeDay()
				return resumeHistoryResult{Outcome: readOutcome}
			}
			candidateValue, matched, parseOutcome := parseCodexSessionMeta(line, parsed.threadID, cleanCWD, m.CreatedAt)
			if parseOutcome != resumeHistoryFound {
				closeDay()
				return resumeHistoryResult{Outcome: parseOutcome}
			}
			if matched {
				candidateValue.when = parsed.when
				candidateValue.rolloutID = parsed.rolloutID
				candidateValue.reverted = parsed.reverted
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

func codexRolloutCandidateName(name string) bool {
	plain := strings.TrimSuffix(name, ".zst")
	return strings.HasPrefix(plain, "rollout-") && strings.HasSuffix(plain, ".jsonl")
}

func readCompleteLine(f *os.File, budget *historyBudget) ([]byte, resumeHistoryOutcome) {
	if budget.limits.MaxRecordBytes > int64(int(^uint(0)>>1)-1) {
		return nil, resumeHistoryUnsafe
	}
	reader := bufio.NewReaderSize(f, int(budget.limits.MaxRecordBytes)+1)
	line, err := reader.ReadSlice('\n')
	if err != nil {
		return nil, resumeHistoryUnsafe
	}
	if int64(len(line)) > budget.limits.MaxRecordBytes || !budget.addBytes(len(line)) {
		return nil, resumeHistoryUnsafe
	}
	return append([]byte(nil), line[:len(line)-1]...), resumeHistoryFound
}

func parseCodexSessionMeta(line []byte, fileID, cleanCWD string, created time.Time) (codexHistoryCandidate, bool, resumeHistoryOutcome) {
	top, ok := decodeStrictObject([]byte(strings.TrimSpace(string(line))))
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
	// Codex 0.150.1 renders the basename from OffsetDateTime::now_local but
	// serializes this payload timestamp after converting that instant to UTC. The
	// basename drops its offset, and Codex's own parser assumes UTC only to recover
	// a sortable wall-clock key. It therefore cannot be compared to this absolute
	// timestamp as an instant. The filename and dated directory were validated
	// independently above; provenance here comes from the canonical ID, cwd and the
	// payload's RFC3339 timestamp relative to the swarm launch window.
	when, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
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
	ordinarySeen := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		if !candidate.reverted {
			if ordinarySeen[candidate.id] {
				// The revert winner must not erase this evidence: two ordinary
				// basenames claiming one stable ID are copied/moved histories in
				// every input order, even when a legitimate revert is also present.
				return resumeHistoryResult{Outcome: resumeHistoryAmbiguous}
			}
			ordinarySeen[candidate.id] = true
		}
		if prior, duplicate := byID[candidate.id]; duplicate {
			// A real thread/revert is distinguishable by `_ROLLOUT`; its
			// strictly newer (timestamp, rollout UUID) key is Codex's
			// characterized winner. Ordinary duplication was fenced above.
			order := compareCodexHistoryCandidate(candidate, prior)
			if order == 0 {
				return resumeHistoryResult{Outcome: resumeHistoryAmbiguous}
			}
			if order > 0 {
				byID[candidate.id] = candidate
			}
			continue
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

func compareCodexHistoryCandidate(a, b codexHistoryCandidate) int {
	if a.when.After(b.when) {
		return 1
	}
	if a.when.Before(b.when) {
		return -1
	}
	return strings.Compare(a.rolloutID, b.rolloutID)
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
