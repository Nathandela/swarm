package skeleton

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/idempotency"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/procstart"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/workermail"
)

type CheckoutInventory struct {
	Complete         bool                 `json:"complete"`
	Supervisors      []string             `json:"supervisors"`
	Paths            []CheckoutPath       `json:"paths"`
	Discussions      []CheckoutDiscussion `json:"discussions,omitempty"`
	UnlinkedReceipts []CheckoutReceipt    `json:"unlinked_receipts,omitempty"`
	Unknowns         []string             `json:"unknowns,omitempty"`
}

type CheckoutPath struct {
	Path                   string            `json:"path"`
	PathState              string            `json:"path_state"`
	Repo                   string            `json:"repo,omitempty"`
	GitCommonDir           string            `json:"git_common_dir,omitempty"`
	Worktree               string            `json:"worktree,omitempty"`
	GitKind                string            `json:"git_kind,omitempty"`
	Branch                 string            `json:"branch,omitempty"`
	Head                   string            `json:"head,omitempty"`
	Locked                 bool              `json:"locked,omitempty"`
	Prunable               bool              `json:"prunable,omitempty"`
	References             []CheckoutSession `json:"linked_sessions"`
	Discussions            []string          `json:"discussions,omitempty"`
	Unknowns               []string          `json:"unknowns,omitempty"`
	registrationUnverified bool
}

type CheckoutSession struct {
	ID           string            `json:"id"`
	Name         string            `json:"name,omitempty"`
	Agent        string            `json:"agent"`
	LaunchPath   string            `json:"launch_path"`
	ProviderPath string            `json:"provider_path"`
	Links        []string          `json:"links"`
	State        string            `json:"state"`
	Resume       string            `json:"resume"`
	Hidden       bool              `json:"hidden,omitempty"`
	SpawnedFrom  string            `json:"spawned_from,omitempty"`
	ResumedFrom  string            `json:"resumed_from,omitempty"`
	Discussion   string            `json:"discussion,omitempty"`
	Receipts     []CheckoutReceipt `json:"unresolved_receipts,omitempty"`
	Unknowns     []string          `json:"unknowns,omitempty"`
}

type CheckoutDiscussion struct {
	ID              string            `json:"id"`
	ManagerSession  string            `json:"manager_session,omitempty"`
	ManagerInstance string            `json:"manager_instance,omitempty"`
	State           string            `json:"state"`
	Project         string            `json:"project,omitempty"`
	WorkerSessions  []string          `json:"worker_sessions,omitempty"`
	WorkerProjects  []string          `json:"worker_projects,omitempty"`
	Receipts        []CheckoutReceipt `json:"unresolved_receipts,omitempty"`
	Unknowns        []string          `json:"unknowns,omitempty"`
}

type CheckoutReceipt struct {
	SessionInstance string `json:"session_instance,omitempty"`
	ID              string `json:"id"`
	Kind            string `json:"kind"`
	State           string `json:"state"`
	Supervisor      string `json:"supervisor,omitempty"`
}

type checkoutMeta struct {
	persist.Meta
	launch, provider, legacy string
	view                     CheckoutSession
}

// InspectCheckouts reads the requested local state roots and Git's worktree
// registrations. Empty paths discover all checkouts mentioned by those roots.
func InspectCheckouts(ctx context.Context, roots, paths []string, discussions []CheckoutDiscussion) CheckoutInventory {
	out := CheckoutInventory{Complete: true, Supervisors: []string{}, Paths: []CheckoutPath{}, Discussions: discussions}
	byID := make(map[string]*checkoutMeta)
	children := make(map[string][]*checkoutMeta)
	nextAttempt := make(map[string][]*checkoutMeta)
	var rootsSeen, mailboxPaths []string
	for _, root := range roots {
		canonical, err := canonicalCleanupPath(root)
		if err != nil {
			out.Unknowns = append(out.Unknowns, fmt.Sprintf("cannot resolve supervisor %q: %v", root, err))
			continue
		}
		seen := false
		for _, prior := range rootsSeen {
			seen = seen || checkoutSamePath(prior, canonical)
		}
		if seen {
			continue
		}
		rootsSeen = append(rootsSeen, canonical)
		out.Supervisors = append(out.Supervisors, canonical)
		endpoint := EndpointIDForStateDir(root)
		metas, errs := persist.InspectSessions(ctx, canonical)
		for _, err := range errs {
			out.Unknowns = append(out.Unknowns, fmt.Sprintf("supervisor %q: %v", canonical, err))
		}
		for _, m := range metas {
			entry := newCheckoutMeta(m, canonical, endpoint)
			byID[entry.view.ID] = entry
			if entry.view.SpawnedFrom != "" {
				children[entry.view.SpawnedFrom] = append(children[entry.view.SpawnedFrom], entry)
			}
			if entry.view.ResumedFrom != "" {
				nextAttempt[entry.view.ResumedFrom] = append(nextAttempt[entry.view.ResumedFrom], entry)
			}
			if entry.launch == "" || entry.provider == "" {
				out.Unknowns = append(out.Unknowns, fmt.Sprintf("session %q has an unresolved checkout path", entry.view.ID))
			}
		}

		receipts, err := idempotency.InspectUnresolved(ctx, filepath.Join(canonical, "idempotency"))
		if err != nil {
			out.Unknowns = append(out.Unknowns, fmt.Sprintf("supervisor %q: receipt evidence unavailable: %v", canonical, err))
		}
		for _, row := range receipts {
			receipt := CheckoutReceipt{ID: row.OperationID, Kind: row.Action, State: string(row.Phase), Supervisor: endpoint, SessionInstance: row.SessionInstance}
			if entry := byID[protocol.NamespacedID(endpoint, row.SessionID)]; entry != nil {
				entry.view.Receipts = append(entry.view.Receipts, receipt)
			} else {
				out.UnlinkedReceipts = append(out.UnlinkedReceipts, receipt)
			}
		}
		notes, err := workermail.InspectPending(ctx, canonical)
		if err != nil {
			out.Unknowns = append(out.Unknowns, fmt.Sprintf("supervisor %q: pending worker mailbox unavailable: %v", canonical, err))
		}
		for _, note := range notes {
			receipt := CheckoutReceipt{ID: note.ID, Kind: "worker_mailbox_" + note.Kind, State: note.State, Supervisor: endpoint, SessionInstance: note.Instance}
			if entry := byID[note.Session]; entry != nil && entry.WorkerInstance == note.Instance && entry.WorkerAttempt == note.Attempt {
				entry.view.Receipts = append(entry.view.Receipts, receipt)
			} else {
				out.UnlinkedReceipts = append(out.UnlinkedReceipts, receipt)
			}
			if filepath.IsAbs(note.Project) {
				mailboxPaths = append(mailboxPaths, note.Project)
			}
		}
	}
	for _, entry := range byID {
		if (entry.view.Resume == "candidate" || entry.view.Resume == "parked_candidate") && len(nextAttempt[entry.view.ID]) > 0 {
			entry.view.Resume = "has_successor"
		}
		if entry.ManagerResumeOperationID != "" && len(nextAttempt[entry.view.ID]) == 0 {
			entry.view.Resume = "unresolved"
			entry.view.Receipts = append(entry.view.Receipts, CheckoutReceipt{ID: entry.ManagerResumeOperationID, Kind: "manager_resume_claim", State: "successor_unverified"})
		}
		for _, source := range []string{entry.view.SpawnedFrom, entry.view.ResumedFrom} {
			if source != "" && byID[source] == nil {
				entry.view.Unknowns = append(entry.view.Unknowns, "lineage predecessor is absent: "+source)
			}
		}
	}
	for _, discussion := range discussions {
		if len(discussion.Unknowns) != 0 {
			out.Complete = false
		}
		if entry := byID[discussion.ManagerSession]; entry != nil && discussion.State == "parked" {
			if entry.view.State == "parked_resume_source" {
				entry.view.State = "parked"
			} else {
				entry.view.Unknowns = append(entry.view.Unknowns, "connector reports a parked manager without matching physical exit evidence")
			}
		}
	}
	if len(paths) == 0 {
		seen := make(map[string]bool)
		for _, entry := range byID {
			for _, path := range []string{entry.launch, entry.provider, entry.legacy} {
				if path != "" && !seen[path] {
					paths = append(paths, path)
					seen[path] = true
				}
			}
		}
		for _, path := range mailboxPaths {
			if !seen[path] {
				paths = append(paths, path)
				seen[path] = true
			}
		}
		for _, discussion := range discussions {
			for _, path := range append([]string{discussion.Project}, discussion.WorkerProjects...) {
				if path != "" && !seen[path] {
					seen[path] = true
					paths = append(paths, path)
				}
			}
		}
		if cwd, err := os.Getwd(); err == nil && !seen[cwd] {
			paths = append(paths, cwd)
		}
		sort.Strings(paths)
	}
	out.Paths, out.Unknowns = inventoryGitPaths(ctx, paths, out.Unknowns)
	entries := make([]*checkoutMeta, 0, len(byID))
	for _, entry := range byID {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].view.ID < entries[j].view.ID })
	for i := range out.Paths {
		item := &out.Paths[i]
		linkCheckoutSessions(item, entries, byID, children, nextAttempt)
		linkCheckoutDiscussions(item, discussions)
		if ctx.Err() != nil {
			out.Paths = out.Paths[:i+1]
			out.Unknowns = append(out.Unknowns, "checkout association deadline reached")
			break
		}
		if len(item.Unknowns) != 0 {
			out.Complete = false
		}
		for _, linked := range item.References {
			if len(linked.Unknowns) != 0 {
				out.Complete = false
			}
		}
	}
	out.Complete = out.Complete && len(out.Unknowns) == 0 && len(out.UnlinkedReceipts) == 0
	sort.Strings(out.Unknowns)
	return out
}

func newCheckoutMeta(m persist.Meta, root, endpoint string) *checkoutMeta {
	view := CheckoutSession{
		ID: protocol.NamespacedID(endpoint, m.ID), Name: m.Name, Agent: m.AgentType,
		LaunchPath: m.Cwd, ProviderPath: m.ProviderCwd(), State: string(m.Status.Process),
		Resume: "none_recorded", Hidden: m.RosterHidden, Discussion: m.ManagerConversationID,
	}
	if view.Discussion == "" {
		view.Discussion = m.WorkerDiscussion
	}
	if m.SpawnedFrom != "" {
		view.SpawnedFrom = protocol.NamespacedID(endpoint, m.SpawnedFrom)
	}
	if m.ResumedFrom != "" {
		view.ResumedFrom = protocol.NamespacedID(endpoint, m.ResumedFrom)
	}
	canonical := func(s string) string {
		if !filepath.IsAbs(s) {
			return ""
		}
		path, _ := canonicalCleanupPath(s)
		return path
	}
	entry := &checkoutMeta{Meta: m, launch: canonical(m.Cwd), provider: canonical(m.ProviderCwd())}
	if m.LaunchOptions[protocol.OptionWorktree] == "true" && m.AgentCwd == "" {
		entry.legacy = canonical(filepath.Join(m.Cwd, ".swarm", "worktrees", m.ID))
		view.Unknowns = append(view.Unknowns, "legacy worktree association has no recorded provider checkout")
	}
	shimLive := false
	if m.ShimPID > 0 && m.ShimStartTime > 0 {
		start, err := procstart.StartTime(m.ShimPID)
		shimLive = err == nil && start == m.ShimStartTime
	}
	switch {
	case m.Status.NativeWorkUnknown:
		view.State = "unknown"
		view.Unknowns = append(view.Unknowns, "native work outcome is unknown")
	case m.Status.Process == status.ProcessRunning && shimLive:
		view.State = "live"
	case m.Status.Process == status.ProcessExited && !shimLive:
		if validProviderConversationID(m.AgentType, m.ConversationID) {
			view.Resume = "candidate"
		}
		if m.ManagerRetirement.OperationID != "" && m.ManagerRetirement.Replacement == "" {
			var exit shim.ExitInfo
			proven := readNativeProof(root, m.ID, shim.ExitFile, &exit) == nil &&
				daemon.ManagerRetirementProven(m, m.ManagerRetirement, exit, true, loginProcessAlive(m.ShimPID, m.ShimStartTime))
			if proven {
				view.State, view.Resume = "parked_resume_source", "parked_candidate"
			} else {
				view.Unknowns = append(view.Unknowns, "manager retirement has no verified exit")
				view.Resume = "unresolved"
			}
		}
	default:
		view.State = "unknown"
		view.Unknowns = append(view.Unknowns, "persisted process state and shim identity do not prove current liveness")
	}
	if m.ResumeError != "" || m.InputEmbargo != "" {
		view.Resume = "unresolved"
		view.Unknowns = append(view.Unknowns, "a resume or input recovery issue remains")
	}
	if m.ManagerRetirement.OperationID != "" && view.State != "parked_resume_source" {
		view.Receipts = append(view.Receipts, CheckoutReceipt{ID: m.ManagerRetirement.OperationID, Kind: "manager_retirement", State: "requires_reconciliation", Supervisor: endpoint})
	}
	if _, err := os.Lstat(filepath.Join(root, m.ID, directInputUnresolvedFile)); err == nil {
		view.Receipts = append(view.Receipts, CheckoutReceipt{Kind: "direct_input", State: "unresolved", Supervisor: endpoint})
	} else if !os.IsNotExist(err) {
		view.Unknowns = append(view.Unknowns, "cannot inspect direct-input recovery marker")
	}
	entry.view = view
	return entry
}

func checkoutSamePath(a, b string) bool {
	return a != "" && b != "" && cleanupContains(a, b) && cleanupContains(b, a)
}

func linkCheckoutSessions(item *CheckoutPath, entries []*checkoutMeta, byID map[string]*checkoutMeta, children, nextAttempt map[string][]*checkoutMeta) {
	item.References = []CheckoutSession{}
	links := make(map[string][]string)
	queue := make([]*checkoutMeta, 0)
	add := func(entry *checkoutMeta, relation string) {
		id := entry.view.ID
		if len(links[id]) == 0 {
			queue = append(queue, entry)
		}
		for _, current := range links[id] {
			if current == relation {
				return
			}
		}
		links[id] = append(links[id], relation)
	}
	for _, entry := range entries {
		if checkoutPathContains(item, entry.launch) || item.registrationUnverified && checkoutLexicalContains(item.Path, entry.Cwd) {
			add(entry, "launch_path")
		}
		if checkoutPathContains(item, entry.provider) || item.registrationUnverified && checkoutLexicalContains(item.Path, entry.ProviderCwd()) {
			add(entry, "provider_path")
			if !item.registrationUnverified && item.Worktree != "" && checkoutSamePath(item.Worktree, entry.provider) &&
				entry.AgentCwd != "" && entry.LaunchOptions[protocol.OptionWorktree] == "true" {
				add(entry, "swarm_worktree_record")
			}
		}
		if checkoutPathContains(item, entry.legacy) {
			add(entry, "possible_legacy_worktree")
		}
		if item.Worktree == "" && entry.provider != "" && cleanupContains(entry.provider, item.Path) {
			add(entry, "provider_path_ancestor")
		}
	}
	for i := 0; i < len(queue); i++ {
		entry := queue[i]
		for _, child := range children[entry.view.ID] {
			add(child, "descendant")
		}
		for _, child := range nextAttempt[entry.view.ID] {
			add(child, "resume_successor")
		}
		if source := byID[entry.view.ResumedFrom]; source != nil {
			add(source, "resume_predecessor")
		}
	}
	for _, entry := range queue {
		view := entry.view
		view.Links = links[entry.view.ID]
		sort.Strings(view.Links)
		item.References = append(item.References, view)
	}
	sort.Slice(item.References, func(i, j int) bool { return item.References[i].ID < item.References[j].ID })
}

func checkoutPathContains(item *CheckoutPath, path string) bool {
	if path == "" || item.Path == "" {
		return false
	}
	if item.registrationUnverified {
		return checkoutLexicalContains(item.Path, path)
	}
	return cleanupContains(item.Path, path) ||
		(item.Worktree != "" && cleanupContains(item.Worktree, path))
}

func linkCheckoutDiscussions(item *CheckoutPath, discussions []CheckoutDiscussion) {
	for _, discussion := range discussions {
		matches := false
		for _, session := range item.References {
			matches = matches || session.ID == discussion.ManagerSession
			for _, worker := range discussion.WorkerSessions {
				matches = matches || session.ID == worker
			}
		}
		for _, path := range append([]string{discussion.Project}, discussion.WorkerProjects...) {
			if filepath.IsAbs(path) {
				if canonical, err := canonicalCleanupPath(path); err == nil {
					matches = matches || checkoutPathContains(item, canonical)
				}
			}
		}
		if matches {
			item.Discussions = append(item.Discussions, discussion.ID)
		}
	}
}

func checkoutLexicalContains(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && filepath.IsLocal(relative)
}
