// Package teammatemanager connects verified Discord discussions to native managers.
package teammatemanager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"unicode"

	"github.com/Nathandela/swarm/internal/discord"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/voice"
	"github.com/Nathandela/swarm/internal/workermail"
	"golang.org/x/sys/unix"
)

type Runtime struct {
	ctx                       context.Context
	cancel                    context.CancelFunc
	dir, socketRoot, endpoint string
	managerSettingsDir        string
	previous                  *discord.PreviousSupervisor
	vmProjectAccess           bool
	cfg                       voice.Config
	dmChannel, dmOwner        string
	projects                  map[string]string
	mu                        sync.Mutex
	discussions               map[string]*discussion
	dormant                   map[string]bool
}

type discussion struct {
	voice  *voice.DiscussionController
	socket string
	mu     sync.RWMutex
	reader discord.TeammateHistoryReader
	id     string
	users  int
}

var _ discord.TeammateManager = (*Runtime)(nil)

func New(ctx context.Context, dir, endpoint, managerSettingsDir string, config discord.Config) (*Runtime, error) {
	if !filepath.IsAbs(dir) || config.InstallationID == "" {
		return nil, voice.ErrInvalid
	}
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	workdir, err := canonicalDirectory(config.WorkDir)
	if err != nil {
		return nil, fmt.Errorf("discord teammate: manager working directory: %w", err)
	}
	tmp, err := filepath.EvalSymlinks("/tmp")
	if err != nil {
		return nil, err
	}
	socketRoot := filepath.Join(tmp, "swarm-dm-"+strconv.Itoa(os.Getuid()))
	if err := privateRoot(socketRoot); err != nil {
		return nil, err
	}
	privateDir := filepath.Join(dir, "discussions")
	if err := privateRoot(privateDir); err != nil {
		return nil, err
	}
	projects := make(map[string]string, len(config.Projects))
	for name, path := range config.Projects {
		root, err := canonicalDirectory(path)
		if err != nil {
			return nil, fmt.Errorf("discord teammate: project %q: %w", name, err)
		}
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" || projects[name] != "" {
			return nil, errors.New("discord teammate: named projects must be unique")
		}
		projects[name] = root
	}
	cfg := voice.Config{Version: 2, PlaceID: config.InstallationID, Name: config.Name, Provider: config.ManagerCLI, WorkDir: workdir, DaemonSocket: config.DaemonSocket,
		ConversationID: voice.DiscussionConversationPrefix, Language: "any", VoiceID: "marin", Mode: "conversation"}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	var dmChannel, dmOwner string
	if config.Mode == "personal" {
		dmChannel, dmOwner = config.DMChannelID, config.OwnerID
		if dmChannel == "" || dmOwner == "" {
			cancel()
			return nil, voice.ErrInvalid
		}
	}
	return &Runtime{ctx: ctx, cancel: cancel, dir: privateDir, socketRoot: socketRoot, endpoint: endpoint, managerSettingsDir: managerSettingsDir, previous: config.PreviousSupervisor, vmProjectAccess: config.ProjectAccess == discord.ProjectAccessVM, cfg: cfg, dmChannel: dmChannel, dmOwner: dmOwner, projects: projects, discussions: map[string]*discussion{}, dormant: map[string]bool{}}, nil
}

func canonicalDirectory(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", voice.ErrInvalid
	}
	root, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", voice.ErrInvalid
	}
	return root, nil
}

func privateRoot(path string) error {
	if err := os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Getuid()) || !info.IsDir() || info.Mode().Perm() != 0700 {
		return fmt.Errorf("discord teammate: %s must be an owner-only directory", path)
	}
	return nil
}

func (r *Runtime) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cancel()
	var err error
	for _, d := range r.discussions {
		err = errors.Join(err, d.voice.Close())
	}
	r.discussions = nil
	return err
}

// Unmarked records predate migration and always belong to their original supervisor.
func (r *Runtime) supervisorForDiscussion(b discord.TeammateDiscussion) (endpoint, socket, settings string, err error) {
	if p := r.previous; p != nil && (b.SupervisorSocket == "" || b.SupervisorSocket == p.Socket) {
		return p.Endpoint, p.Socket, p.StateDir, nil
	}
	if b.SupervisorSocket != "" && b.SupervisorSocket != r.cfg.DaemonSocket {
		return "", "", "", voice.ErrStale
	}
	return r.endpoint, r.cfg.DaemonSocket, r.managerSettingsDir, nil
}

func (r *Runtime) discussion(bound discord.TeammateDiscussion, reader discord.TeammateHistoryReader, skipDormant bool) (*discussion, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ctx.Err(); err != nil {
		return nil, err
	}
	if bound.ID == "" || bound.InstallationID != r.cfg.PlaceID || bound.Route.ChannelID == "" {
		return nil, voice.ErrInvalid
	}
	if r.dmChannel != "" {
		if bound.Route.GuildID != "" || bound.Route.ParentID != "" || bound.Route.ChannelID != r.dmChannel {
			return nil, voice.ErrInvalid
		}
	} else if bound.Route.GuildID == "" {
		return nil, voice.ErrInvalid
	}
	endpoint, socket, settings, err := r.supervisorForDiscussion(bound)
	if err != nil {
		return nil, err
	}
	id := r.mailDiscussionID(bound, endpoint)
	if opened := r.discussions[id]; opened != nil {
		opened.users++
		if reader != nil {
			opened.mu.Lock()
			opened.reader = reader
			opened.mu.Unlock()
		}
		return opened, nil
	}
	if skipDormant && r.dormant[id] {
		if !r.vmProjectAccess || settings == "" {
			return nil, nil
		}
		notes, err := workermail.Pending(settings, voice.DiscussionConversationPrefix+id)
		if err != nil {
			return nil, err
		}
		if len(notes) == 0 {
			return nil, nil
		}
	}
	delete(r.dormant, id)
	cfg := r.cfg
	cfg.DaemonSocket = socket
	cfg.ConversationID += id
	socketDir := filepath.Join(r.socketRoot, id[:32])
	if err := privateRoot(socketDir); err != nil {
		return nil, err
	}
	entry := &discussion{socket: filepath.Join(socketDir, "bridge.sock"), reader: reader, id: id, users: 1}
	manager, err := voice.OpenDiscussion(r.ctx, filepath.Join(r.dir, id), socketDir, endpoint, cfg, voice.DiscussionScope{
		ManagerSettingsDir: settings, VMProjectAccess: r.vmProjectAccess,
		DiscussionID: bound.ID, InstallationID: bound.InstallationID, GuildID: bound.Route.GuildID, DMOwnerID: r.dmOwner, ThreadID: bound.Route.ChannelID,
		History: entry.read, VerifyAudience: entry.verifyAudience, VerifyOrigin: entry.verifyOrigin, VerifyGrant: entry.verifyGrant, ProjectEnrolled: r.projectEnrolled,
	})
	if err != nil {
		return nil, err
	}
	entry.voice = manager
	r.discussions[id] = entry
	return entry, nil
}

func (r *Runtime) release(entry *discussion) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry.users--
	if entry.users != 0 || r.discussions[entry.id] != entry {
		return
	}
	dormant, err := entry.voice.Dormant()
	if err == nil && dormant {
		_ = entry.voice.Close()
		delete(r.discussions, entry.id)
		r.dormant[entry.id] = true
	}
}

func (d *discussion) verifyAudience(ctx context.Context) error {
	d.mu.RLock()
	reader := d.reader
	d.mu.RUnlock()
	if reader == nil {
		return voice.ErrNotFound
	}
	_, err := reader(ctx, discord.TeammateHistoryQuery{Source: "audience"})
	return err
}

func (d *discussion) verifyOrigin(ctx context.Context, origin schema.MessageOrigin) error {
	return d.checkOrigin(ctx, origin, "admission")
}
func (d *discussion) verifyGrant(ctx context.Context, origin schema.MessageOrigin) error {
	return d.checkOrigin(ctx, origin, "grant")
}
func (d *discussion) checkOrigin(ctx context.Context, origin schema.MessageOrigin, stage string) error {
	d.mu.RLock()
	reader := d.reader
	d.mu.RUnlock()
	if reader == nil {
		return voice.ErrNotFound
	}
	_, err := reader(ctx, discord.TeammateHistoryQuery{Source: stage, AdmissionID: origin.RequestID,
		Revision: origin.Revision, MessageID: origin.MessageID, AuthorID: origin.ActorID})
	if errors.Is(err, discord.ErrTeammateSourceChanged) {
		return voice.ErrDiscussionSourceChanged
	}
	return err
}

func (d *discussion) read(ctx context.Context, q voice.DiscussionHistoryInput) (json.RawMessage, error) {
	d.mu.RLock()
	reader := d.reader
	d.mu.RUnlock()
	if reader == nil {
		return nil, voice.ErrNotFound
	}
	page, err := reader(ctx, discord.TeammateHistoryQuery{Source: q.Source, Cursor: q.Cursor, Anchor: q.Anchor})
	if err != nil {
		return nil, err
	}
	return json.Marshal(page)
}

func (r *Runtime) Admit(ctx context.Context, input discord.TeammateAdmission, reader discord.TeammateHistoryReader) error {
	entry, err := r.discussion(input.Discussion, reader, false)
	if err != nil {
		return err
	}
	defer r.release(entry)
	page, source, historyErr := initialHistory(ctx, input, reader)
	err = entry.voice.Admit(ctx, voice.DiscussionInput{
		Origin: discussionOrigin(input), Answer: input.Text, QuestionID: input.QuestionID, QuestionHash: input.QuestionHash, Supersedes: input.Supersedes,
		Project: func(selected string) (string, bool) {
			project := r.resolveProject(selected, input.Text, projectHistory(page, input.Route.MessageID))
			wording := projectWords(input.Text)
			explicit, count := r.namedProject(wording)
			return project, count == 1 && explicit == project && projectSwitch(wording)
		},
		Text: r.prompt(input, page, source, historyErr),
	})
	return activationNotice(err)
}

func activationNotice(err error) error {
	if errors.Is(err, voice.ErrDiscussionActivationUnavailable) {
		return errors.Join(discord.ErrTeammateManagerUnavailable, err)
	}
	return err
}

func (r *Runtime) ResumeManual(ctx context.Context, bound discord.TeammateDiscussion, session, instance string, externalInput uint64) error {
	entry, err := r.discussion(bound, nil, false)
	if err != nil {
		return err
	}
	defer r.release(entry)
	return entry.voice.ResumeManual(ctx, session, instance, externalInput)
}

func (r *Runtime) Status(bound discord.TeammateDiscussion) (discord.TeammateNativeStatus, error) {
	entry, err := r.discussion(bound, nil, false)
	if err != nil {
		return discord.TeammateNativeStatus{}, err
	}
	defer r.release(entry)
	status, err := entry.voice.Status()
	if err != nil {
		return discord.TeammateNativeStatus{}, err
	}
	result := discord.TeammateNativeStatus{
		Notes: status.Notes, RetirementOperation: status.RetirementOperation, OldestPendingNote: status.OldestPendingNote,
		State: status.State, ManagerSession: status.ManagerSession, ManagerInstance: status.ManagerInstance,
		Project: status.Project, Diagnostic: status.Diagnostic, Generation: status.Generation, ExternalInput: status.ExternalInput,
	}
	for _, job := range status.Jobs {
		result.Jobs = append(result.Jobs, discord.TeammateJobStatus{ID: job.ID, AttemptID: job.AttemptID, Session: job.Session,
			Instance: job.Instance, Project: job.Project, State: job.State, Diagnostic: job.Diagnostic})
	}
	return result, nil
}

func (r *Runtime) PublicationExported(bound discord.TeammateDiscussion, id string, question bool) (bool, error) {
	entry, err := r.discussion(bound, nil, false)
	if err != nil {
		return false, err
	}
	defer r.release(entry)
	return entry.voice.PublicationExported(id, question)
}

func (r *Runtime) ValidatePublication(ctx context.Context, bound discord.TeammateDiscussion, p discord.TeammatePublication) error {
	entry, err := r.discussion(bound, nil, false)
	if err != nil {
		return err
	}
	defer r.release(entry)
	source := voice.DiscussionOutputSource{AdmissionID: p.AdmissionID, JobID: p.JobID, AttemptID: p.AttemptID, SystemCause: p.SystemCause}
	output := voice.DiscussionOutput{DiscussionOutputSource: source, ID: p.ID, QuestionID: p.QuestionID, QuestionHash: p.QuestionHash, Sequence: p.Sequence}
	err = entry.voice.ValidatePublication(ctx, output, p.BodyHash)
	if errors.Is(err, voice.ErrExpired) {
		return discord.ErrNotAuthorized
	}
	return err
}

func discussionOrigin(input discord.TeammateAdmission) schema.MessageOrigin {
	return schema.MessageOrigin{
		Kind: string(input.Authority), InstallationID: input.Discussion.InstallationID, DiscussionID: input.Discussion.ID, RequestID: input.OperationID,
		ActorID: input.Author.ID, ActorName: input.Author.Name, RequesterID: input.InvokerID, GuildID: input.Discussion.Route.GuildID,
		ChannelID: input.Discussion.Route.ChannelID, MessageID: input.Route.MessageID, Revision: input.Revision,
	}
}

func (r *Runtime) AdmissionState(ctx context.Context, input discord.TeammateAdmission) (string, error) {
	if err := ctx.Err(); err != nil {
		return "missing", err
	}
	entry, err := r.discussion(input.Discussion, nil, false)
	if err != nil {
		return "missing", err
	}
	defer r.release(entry)
	return entry.voice.AdmissionState(discussionOrigin(input))
}

func (r *Runtime) CancelAdmission(ctx context.Context, input discord.TeammateAdmission) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entry, err := r.discussion(input.Discussion, nil, false)
	if err != nil {
		return err
	}
	defer r.release(entry)
	return entry.voice.CancelAdmission(discussionOrigin(input))
}

func (r *Runtime) AdmissionKnown(ctx context.Context, input discord.TeammateAdmission) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	entry, err := r.discussion(input.Discussion, nil, false)
	if err != nil {
		return false, err
	}
	defer r.release(entry)
	return entry.voice.AdmissionKnown(discussionOrigin(input))
}

func (r *Runtime) Sync(ctx context.Context, bound discord.TeammateDiscussion, reader discord.TeammateHistoryReader, emit func(discord.TeammatePublication) error) error {
	entry, err := r.discussion(bound, reader, true)
	if err != nil {
		return err
	}
	if entry == nil {
		return nil
	}
	defer r.release(entry)
	err = entry.voice.Sync(ctx, func(output voice.DiscussionOutput) error {
		return emit(discord.TeammatePublication{
			ID: output.ID, Body: output.Text, AdmissionID: output.AdmissionID, JobID: output.JobID, AttemptID: output.AttemptID,
			QuestionID: output.QuestionID, QuestionHash: output.QuestionHash, Sequence: output.Sequence, SystemCause: output.SystemCause,
		})
	})
	return activationNotice(err)
}

func projectWords(value string) string {
	return " " + strings.Join(strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r) && r != '-' && r != '_'
	}), " ") + " "
}

func (r *Runtime) projectEnrolled(path string) bool {
	if r.vmProjectAccess {
		root, err := canonicalDirectory(path)
		return err == nil && root == path && unix.Access(root, unix.X_OK) == nil
	}
	if len(r.projects) == 0 {
		return path == r.cfg.WorkDir
	}
	for _, candidate := range r.projects {
		if candidate == path {
			return true
		}
	}
	return false
}

func projectSwitch(wording string) bool {
	return strings.Contains(wording, " switch to ") || strings.Contains(wording, " switch project to ") ||
		strings.Contains(wording, " use project ") || strings.Contains(wording, " change project to ")
}

func (r *Runtime) resolveProject(selected, text, history string) string {
	if r.vmProjectAccess {
		wording := projectWords(text)
		if selected != "" && r.projectEnrolled(selected) && !projectSwitch(wording) {
			return selected
		}
		if root, count := r.namedProject(wording); count == 1 && r.projectEnrolled(root) {
			return root
		}
		if selected != "" && r.projectEnrolled(selected) {
			return selected
		}
		return r.cfg.WorkDir
	}
	if len(r.projects) == 0 {
		return r.cfg.WorkDir
	}
	if len(r.projects) == 1 {
		for _, path := range r.projects {
			return path
		}
	}
	enrolled := false
	for _, path := range r.projects {
		if path == selected {
			enrolled = true
			break
		}
	}
	if !enrolled {
		selected = ""
	}
	wording := projectWords(text)
	switchProject := projectSwitch(wording)
	if selected != "" && !switchProject {
		return selected
	}
	root, count := r.namedProject(wording)
	if count == 1 {
		return root
	}
	if count > 1 || switchProject {
		return selected
	}
	root, count = r.namedProject(projectWords(history))
	if count == 1 {
		return root
	}
	return selected
}

func (r *Runtime) namedProject(wording string) (string, int) {
	root := ""
	count := 0
	for name, path := range r.projects {
		if strings.Contains(wording, projectWords(name)) {
			root = path
			count++
		}
	}
	return root, count
}

func initialHistory(ctx context.Context, input discord.TeammateAdmission, reader discord.TeammateHistoryReader) (discord.TeammateHistoryPage, string, error) {
	source := "thread"
	if input.Discussion.ParentAnchor != "" && input.Discussion.ParentAnchor == input.Route.MessageID {
		source = "parent"
	}
	if reader == nil {
		return discord.TeammateHistoryPage{}, source, voice.ErrNotFound
	}
	page, err := reader(ctx, discord.TeammateHistoryQuery{Source: source})
	return page, source, err
}

func projectHistory(page discord.TeammateHistoryPage, invokingMessage string) string {
	var context strings.Builder
	for _, messages := range [][]discord.TeammateHistoryMessage{page.Messages, page.ReplyChain} {
		for _, m := range messages {
			if m.ID != invokingMessage {
				context.WriteString(m.Text)
				context.WriteByte('\n')
			}
		}
	}
	if page.Starter != nil && page.Starter.ID != invokingMessage {
		context.WriteString(page.Starter.Text)
	}
	return context.String()
}

func (r *Runtime) prompt(input discord.TeammateAdmission, page discord.TeammateHistoryPage, source string, historyErr error) string {
	var msg strings.Builder
	if r.vmProjectAccess {
		msg.WriteString("Send every Discord reply with the publish tool. A native final answer alone stays in the terminal. " + voice.DiscussionReplyInstructions + " Use your shell and ordinary VM commands. Start workers with swarm spawn, steer the same session with swarm message, and inspect output with swarm peek. Read swarm dispatcher-guide before delegating. Ask workers for explicit needs-input and completion notes, then return after dispatch.\n")
	} else {
		msg.WriteString(voice.DiscussionReplyInstructions + " Use the scoped manager tools and approved project. Verify results with swarm_read.\n")
	}
	if len(r.projects) > 1 && !r.vmProjectAccess {
		names := make([]string, 0, len(r.projects))
		for name := range r.projects {
			names = append(names, name)
		}
		sort.Strings(names)
		if len(strings.Join(names, ", ")) <= 512 {
			fmt.Fprintf(&msg, "Enrolled project choices %s. If unresolved and work requires one, ask using these names as choices.\n", strings.Join(names, ", "))
		}
	}
	if len(input.Text) < 1000 {
		if historyErr != nil {
			msg.WriteString("The initial history read was unavailable.\n")
		} else {
			var entries []discord.TeammateHistoryMessage
			seen := map[string]bool{input.Route.MessageID: true}
			n := 0
			add := func(entry discord.TeammateHistoryMessage) {
				if seen[entry.ID] {
					return
				}
				raw, err := json.Marshal(entry)
				if err != nil || n+len(raw) > 900 {
					return
				}
				n += len(raw)
				seen[entry.ID] = true
				entries = append(entries, entry)
			}
			for i := len(page.ReplyChain) - 1; i >= 0; i-- {
				add(page.ReplyChain[i])
			}
			if page.Starter != nil {
				add(*page.Starter)
			}
			for i := len(page.Messages) - 1; i >= 0; i-- {
				add(page.Messages[i])
			}
			sort.Slice(entries, func(i, j int) bool { return entries[i].CreatedAt.Before(entries[j].CreatedAt) })
			history, _ := json.Marshal(entries)
			fmt.Fprintf(&msg, "Recent Discord evidence (JSON) %s\n", history)
			fmt.Fprintf(&msg, "Evidence source %s, latest message %s. The history tool can retrieve remaining pages.\n", source, page.HighWater)
			if page.Notice != "" {
				fmt.Fprintf(&msg, "History: %s\n", page.Notice)
			}
		}
	}
	body, _ := json.Marshal(struct {
		Author                   discord.Actor
		MessageID, ReplyTo, Text string
	}{input.Author, input.Route.MessageID, input.ReplyTo, input.Text})
	fmt.Fprintf(&msg, "Current human instruction (JSON) %s", body)
	return msg.String()
}

func (r *Runtime) mailDiscussionID(b discord.TeammateDiscussion, endpoint string) string {
	hash := sha256.Sum256([]byte(strings.Join([]string{r.dir, endpoint, b.InstallationID, b.Route.GuildID, b.Route.ChannelID, b.ID}, "\x00")))
	return hex.EncodeToString(hash[:])
}

// PendingWorkerDiscussions snapshots the durable pending index once per bound supervisor.
func (r *Runtime) PendingWorkerDiscussions() (func(discord.TeammateDiscussion) bool, error) {
	if !r.vmProjectAccess {
		return nil, nil
	}
	dirs := []string{r.managerSettingsDir}
	if r.previous != nil && r.previous.StateDir != r.managerSettingsDir {
		dirs = append(dirs, r.previous.StateDir)
	}
	byDir := map[string]map[string]bool{}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		pending, err := workermail.Discussions(dir)
		if err != nil {
			return nil, err
		}
		if len(pending) > 0 {
			byDir[dir] = pending
		}
	}
	if len(byDir) == 0 {
		return nil, nil
	}
	return func(b discord.TeammateDiscussion) bool {
		ep, _, dir, err := r.supervisorForDiscussion(b)
		return err == nil && byDir[dir][voice.DiscussionConversationPrefix+r.mailDiscussionID(b, ep)]
	}, nil
}
