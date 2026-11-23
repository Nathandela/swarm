package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/discord"
	"github.com/Nathandela/swarm/internal/skeleton"
	"github.com/Nathandela/swarm/internal/teammatemanager"
	"github.com/Nathandela/swarm/internal/voice"
)

type inventoryPaths []string

func (p *inventoryPaths) String() string { return strings.Join(*p, ", ") }
func (p *inventoryPaths) Set(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("state directory must be an absolute path")
	}
	if len(*p) == 16 {
		return errors.New("too many state directories")
	}
	*p = append(*p, path)
	return nil
}

func runCheckout(args []string, stdout, stderr io.Writer) int {
	const usage = "usage: swarm checkout inventory [--json] [--supervisor-state DIR] [--discord-state DIR] [PATH ...]"
	if len(args) == 0 || args[0] != "inventory" {
		if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
			_, _ = fmt.Fprintln(stdout, usage)
			return 0
		}
		_, _ = fmt.Fprintln(stderr, usage)
		return misuseExit
	}
	fs := flag.NewFlagSet("checkout inventory", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { _, _ = fmt.Fprintln(stderr, usage); fs.PrintDefaults() }
	asJSON := fs.Bool("json", false, "print machine-readable inventory")
	var supervisors, connectors inventoryPaths
	fs.Var(&supervisors, "supervisor-state", "also inspect this local supervisor state directory (repeatable)")
	fs.Var(&connectors, "discord-state", "also inspect this local Discord connector state directory (repeatable)")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return misuseExit
	}
	cc, err := clientConfig()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "checkout inventory: %v\n", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	roots, discussions, unknowns := checkoutInventoryScope(ctx, cc, supervisors, connectors)
	report := skeleton.InspectCheckouts(ctx, roots, fs.Args(), discussions)
	report.Unknowns = append(report.Unknowns, unknowns...)
	sort.Strings(report.Unknowns)
	report.Complete = report.Complete && len(unknowns) == 0
	if *asJSON {
		err = writeJSON(stdout, report)
	} else {
		err = printCheckoutInventory(stdout, report)
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "checkout inventory: write output: %v\n", err)
		return 1
	}
	if !report.Complete {
		return 1
	}
	return 0
}

func checkoutInventoryScope(ctx context.Context, cc daemon.ClientConfig, extraRoots, extraConnectors []string) ([]string, []skeleton.CheckoutDiscussion, []string) {
	roots := append([]string{cc.StateDir}, extraRoots...)
	dirs := append([]string{}, extraConnectors...)
	var unknowns []string
	if cc.SocketPath != filepath.Join(cc.StateDir, "daemon.sock") {
		unknowns = append(unknowns, "configured supervisor socket uses a nondefault path; inventory follows explicit local state roots")
	}
	if slot, err := installedDiscordDirectory(); err == nil {
		if dir, err := discord.InspectSelectedState(slot); err == nil {
			dirs = append(dirs, dir)
		} else if !os.IsNotExist(err) {
			unknowns = append(unknowns, "installed Discord selection is unreadable: "+err.Error())
		}
	} else {
		unknowns = append(unknowns, "installed Discord selection cannot be resolved: "+err.Error())
	}
	if dir, err := discord.InspectSelectedState(cc.StateDir); err == nil {
		dirs = append(dirs, dir)
	} else if os.IsNotExist(err) {
		dir := filepath.Join(cc.StateDir, "discord")
		if _, err = os.Lstat(discord.ConfigPath(dir)); err == nil {
			dirs = append(dirs, dir)
		} else if !os.IsNotExist(err) {
			unknowns = append(unknowns, "default Discord configuration is unreadable: "+err.Error())
		}
	} else {
		unknowns = append(unknowns, "active Discord selection is unreadable: "+err.Error())
	}
	var discussions []skeleton.CheckoutDiscussion
	seen := make(map[string]bool)
	for _, dir := range dirs {
		if canonical, err := filepath.EvalSymlinks(dir); err == nil {
			dir = canonical
		}
		if seen[dir] {
			continue
		}
		seen[dir] = true
		if ctx.Err() != nil {
			unknowns = append(unknowns, "Discord scope discovery timed out")
			break
		}
		cfg, err := discord.InspectConfig(dir)
		if err != nil {
			unknowns = append(unknowns, fmt.Sprintf("Discord configuration at %q is unreadable: %v", dir, err))
			continue
		}
		if !cfg.TeammateMode() {
			unknowns = append(unknowns, fmt.Sprintf("Discord at %q uses a legacy connector; see swarm discord status for its receipts", dir))
			continue
		}
		endpoints := make(map[string]string)
		if cfg.Supervisor != nil {
			roots = append(roots, cfg.Supervisor.StateDir)
			endpoints[cfg.DaemonSocket] = skeleton.EndpointIDForStateDir(cfg.Supervisor.StateDir)
		} else if cfg.DaemonSocket == cc.SocketPath {
			endpoints[cfg.DaemonSocket] = skeleton.EndpointIDForStateDir(cc.StateDir)
		} else {
			unknowns = append(unknowns, fmt.Sprintf("Discord at %q has no recorded state root for its supervisor socket %q", dir, cfg.DaemonSocket))
		}
		endpoints[""] = endpoints[cfg.DaemonSocket]
		if previous := cfg.PreviousSupervisor; previous != nil {
			roots = append(roots, previous.StateDir)
			endpoints[previous.Socket], endpoints[""] = previous.Endpoint, previous.Endpoint
		}
		rows, issues := inspectCheckoutConnector(ctx, dir, cfg.InstallationID, endpoints)
		discussions = append(discussions, rows...)
		unknowns = append(unknowns, issues...)
	}
	sort.Slice(discussions, func(i, j int) bool { return discussions[i].ID < discussions[j].ID })
	return roots, discussions, unknowns
}

func inspectCheckoutConnector(ctx context.Context, dir, installation string, endpoints map[string]string) ([]skeleton.CheckoutDiscussion, []string) {
	var status discord.TeammateStatus
	dialCtx, cancel := context.WithTimeout(ctx, time.Second)
	err := discord.CallLocal(dialCtx, dir, "checkout-status", nil, &status)
	cancel()
	if err != nil {
		status, err = discord.InspectCheckoutStatus(dir, installation)
	}
	var issues []string
	if err != nil {
		issues = append(issues, fmt.Sprintf("Discord receipts at %q unavailable: %v (inspect the connector on its owning VM)", dir, err))
	}
	if status.IngressIssue != "" || status.WorkerMailIssue != "" {
		issues = append(issues, fmt.Sprintf("Discord at %q reports unresolved ingress or mailbox work", dir))
	}
	if status.PendingNotices != 0 {
		issues = append(issues, fmt.Sprintf("Discord at %q has %d connector notices without a linked checkout", dir, status.PendingNotices))
	}
	var rows []skeleton.CheckoutDiscussion
	shownIngress := 0
	for _, row := range status.Discussions {
		view := skeleton.CheckoutDiscussion{ID: row.ID, State: "unknown"}
		if row.Native != nil && row.Native.CheckoutEvidence != "" {
			view = checkoutDiscussionView(row.ID, *row.Native)
		}
		for _, input := range row.Pending {
			shownIngress++
			id := input.OperationID
			if id == "" {
				id = input.MessageID
			}
			view.Receipts = append(view.Receipts, skeleton.CheckoutReceipt{ID: id, Kind: "discord_ingress", State: input.State})
		}
		for _, output := range row.Outbox {
			state := "pending"
			if output.Error != "" {
				state = "unconfirmed"
			}
			view.Receipts = append(view.Receipts, skeleton.CheckoutReceipt{ID: output.ID, Kind: "discord_publication", State: state})
		}
		rows = append(rows, view)
	}
	if unlinked := status.Pending - shownIngress; unlinked > 0 {
		issues = append(issues, fmt.Sprintf("Discord at %q has %d queued messages without an inspected checkout binding", dir, unlinked))
	}
	// Dormant discussions need not be present in the live connector index. Read
	// their existing voice ledgers directly without opening a controller.
	discussionsDir := filepath.Join(dir, "discussions")
	entries, readErr := os.ReadDir(discussionsDir)
	if readErr != nil && !os.IsNotExist(readErr) {
		issues = append(issues, fmt.Sprintf("Discord discussions at %q unavailable: %v", discussionsDir, readErr))
	}
	diskLinked := make(map[string]int)
	for i, row := range status.Discussions {
		if endpoint := endpoints[row.SupervisorSocket]; endpoint != "" {
			ledgerID := teammatemanager.CheckoutDiscussionLedgerID(discussionsDir, endpoint, installation, row.Thread.GuildID, row.Thread.ChannelID, row.ID)
			diskLinked[ledgerID] = i
		} else {
			rows[i].Unknowns = append(rows[i].Unknowns, "Discord discussion owning supervisor is unverified")
		}
	}
	for i, entry := range entries {
		if i == 256 || ctx.Err() != nil {
			issues = append(issues, "Discord discussion discovery limit reached")
			break
		}
		ledgerID := entry.Name()
		index, linked := diskLinked[ledgerID]
		if linked && rows[index].State != "unknown" && len(rows[index].Unknowns) == 0 {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			issues = append(issues, fmt.Sprintf("Discord discussion %q is a symlink", ledgerID))
			continue
		}
		if !entry.IsDir() {
			continue
		}
		view, err := voice.InspectDiscussion(filepath.Join(discussionsDir, ledgerID))
		if errors.Is(err, os.ErrNotExist) && !linked {
			continue
		}
		if err != nil {
			if linked {
				rows[index].Unknowns = append(rows[index].Unknowns, "native discussion ledger is unavailable")
			} else {
				issues = append(issues, fmt.Sprintf("Discord discussion %q is unavailable: %v", ledgerID, err))
			}
			continue
		}
		if linked {
			recovered := checkoutDiscussionView(rows[index].ID, teammatemanager.CheckoutStatusProjection(view))
			recovered.Receipts = append(recovered.Receipts, rows[index].Receipts...)
			rows[index] = recovered
		} else {
			rows = append(rows, checkoutDiscussionView(voice.DiscussionConversationPrefix+ledgerID, teammatemanager.CheckoutStatusProjection(view)))
		}
	}
	for i := range rows {
		if rows[i].State != "unknown" {
			continue
		}
		if i < len(status.Discussions) {
			if cached := status.Discussions[i].Native; cached != nil && cached.CheckoutEvidence == "" && cached.ManagerSession != "" {
				hint := checkoutDiscussionView(rows[i].ID, *cached)
				hint.State = "unknown"
				hint.Receipts = append(hint.Receipts, rows[i].Receipts...)
				hint.Unknowns = append(rows[i].Unknowns, "last recorded manager and checkout could not be verified against the native ledger")
				rows[i] = hint
			}
		}
		if len(rows[i].Unknowns) == 0 {
			rows[i].Unknowns = append(rows[i].Unknowns, "native discussion binding is unverified")
		}
	}
	return rows, issues
}

func checkoutDiscussionView(id string, status discord.TeammateNativeStatus) skeleton.CheckoutDiscussion {
	view := skeleton.CheckoutDiscussion{ID: id, State: status.State, Project: status.Project, ManagerSession: status.ManagerSession, ManagerInstance: status.ManagerInstance}
	if view.State == "" {
		view.State = "unknown"
	}
	if view.State == "unknown" {
		view.Unknowns = append(view.Unknowns, "native discussion status is unavailable")
	}
	for _, job := range status.Jobs {
		if job.Session != "" {
			view.WorkerSessions = append(view.WorkerSessions, job.Session)
		}
		if job.Project != "" {
			view.WorkerProjects = append(view.WorkerProjects, job.Project)
		}
		if job.RecoveryRequired || job.State == "pending_launch" || job.State == "exited_unconfirmed" || job.State == "evidence_limited" {
			view.Receipts = append(view.Receipts, skeleton.CheckoutReceipt{ID: job.AttemptID, Kind: "worker_job", State: job.State, SessionInstance: job.Instance})
		}
	}
	if status.RetirementOperation != "" && status.State != "parked" {
		view.Receipts = append(view.Receipts, skeleton.CheckoutReceipt{ID: status.RetirementOperation, Kind: "manager_retirement", State: status.State})
	}
	for _, note := range status.Notes {
		if note.State != "settled" && note.State != "complete" && note.State != "rejected" {
			view.Receipts = append(view.Receipts, skeleton.CheckoutReceipt{ID: note.ID, Kind: "worker_note", State: note.State, SessionInstance: note.Instance})
		}
	}
	for _, pending := range []struct {
		kind string
		n    int
	}{
		{"native_admissions", status.PendingAdmissions},
		{"native_actions", status.PendingActions},
		{"pending_replies", status.PendingReplies},
		{"unconfirmed_replies", status.UnconfirmedReplies},
	} {
		if pending.n > 0 {
			view.Receipts = append(view.Receipts, skeleton.CheckoutReceipt{Kind: pending.kind, State: fmt.Sprintf("%d unresolved", pending.n)})
		}
	}
	return view
}

func printCheckoutInventory(out io.Writer, report skeleton.CheckoutInventory) error {
	var text strings.Builder
	fmt.Fprintf(&text, "Checkout inventory (complete=%t for selected local state roots)\n", report.Complete)
	for _, root := range report.Supervisors {
		fmt.Fprintf(&text, "Supervisor %q\n", root)
	}
	for _, path := range report.Paths {
		fmt.Fprintf(&text, "%q  %s", path.Path, path.PathState)
		if path.GitKind != "" {
			fmt.Fprintf(&text, "  %s", path.GitKind)
		}
		if path.Repo != "" {
			fmt.Fprintf(&text, "  repo=%q worktree=%q", path.Repo, path.Worktree)
		}
		if path.Branch != "" {
			fmt.Fprintf(&text, "  branch=%q", path.Branch)
		}
		fmt.Fprintln(&text)
		if len(path.References) == 0 && len(path.Discussions) == 0 {
			fmt.Fprintln(&text, "  No linked session or discussion found in this snapshot.")
		}
		for _, session := range path.References {
			fmt.Fprintf(&text, "  %q %q  %s  resume=%s  %s\n", session.ID, session.Name, session.State, session.Resume, strings.Join(session.Links, ", "))
			for _, receipt := range session.Receipts {
				fmt.Fprintf(&text, "    receipt %q %q %q\n", receipt.Kind, receipt.ID, receipt.State)
			}
			for _, issue := range session.Unknowns {
				fmt.Fprintf(&text, "    unknown: %q\n", issue)
			}
		}
		for _, discussion := range path.Discussions {
			fmt.Fprintf(&text, "  Discussion %q\n", discussion)
		}
		for _, issue := range path.Unknowns {
			fmt.Fprintf(&text, "  unknown: %q\n", issue)
		}
	}
	for _, discussion := range report.Discussions {
		fmt.Fprintf(&text, "Discussion %q  %q  manager=%q project=%q\n", discussion.ID, discussion.State, discussion.ManagerSession, discussion.Project)
		for _, receipt := range discussion.Receipts {
			fmt.Fprintf(&text, "  receipt %q %q %q\n", receipt.Kind, receipt.ID, receipt.State)
		}
		for _, issue := range discussion.Unknowns {
			fmt.Fprintf(&text, "  unknown: %q\n", issue)
		}
	}
	for _, receipt := range report.UnlinkedReceipts {
		fmt.Fprintf(&text, "Unlinked receipt %q %q %q\n", receipt.Kind, receipt.ID, receipt.State)
	}
	for _, issue := range report.Unknowns {
		fmt.Fprintf(&text, "Unknown: %q\n", issue)
	}
	fmt.Fprintln(&text, "Use the evidence to decide what to retain. Verify Git changes and any other consumers separately.")
	_, err := io.WriteString(out, text.String())
	return err
}
