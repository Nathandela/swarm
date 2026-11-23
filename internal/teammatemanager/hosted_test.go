package teammatemanager

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/discord"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/voice"
)

// This opt-in rehearsal uses a real native account. Discord messages and authors are synthetic.
func TestHostedTeammateLifecycle(t *testing.T) {
	bin, workdir := os.Getenv("SWARM_TEAMMATE_TEST_BINARY"), os.Getenv("SWARM_TEAMMATE_TEST_MANAGER_DIR")
	if bin == "" && workdir == "" {
		t.Skip("set SWARM_TEAMMATE_TEST_BINARY and SWARM_TEAMMATE_TEST_MANAGER_DIR for the native rehearsal")
	}
	if !filepath.IsAbs(bin) || !filepath.IsAbs(workdir) {
		t.Fatal("provide an absolute Swarm binary path and a native trusted manager workspace")
	}
	root, err := os.MkdirTemp("/tmp", "dt-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("private rehearsal evidence retained in %s", root)
			return
		}
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	state, project, connector := filepath.Join(root, "s"), filepath.Join(root, "w"), filepath.Join(root, "d")
	for _, path := range []string{state, project, connector} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	var marker [8]byte
	if _, err := rand.Read(marker[:]); err != nil {
		t.Fatal(err)
	}
	proof := "silver compass " + hex.EncodeToString(marker[:])
	if err := os.WriteFile(filepath.Join(project, "proof.txt"), []byte(proof+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	steeringProof := "cobalt lantern " + hex.EncodeToString(marker[3:])
	if err := os.WriteFile(filepath.Join(project, "second.txt"), []byte(steeringProof+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	armResumeFailures := prepareHostedResumeFailures(t, root)
	socket := filepath.Join(state, "daemon.sock")
	startDaemon := func(binary string) *exec.Cmd {
		cmd := exec.Command(binary, "daemon")
		cmd.Dir = project
		cmd.Env = []string{"SWARM_DAEMON_STATE=" + state, "SWARM_DAEMON_SOCK=" + socket, "SWARM_DAEMON_LOCK=" + filepath.Join(state, "daemon.lock"), "SWARM_DAEMON_LOG=" + filepath.Join(state, "daemon.log")}
		for _, key := range []string{"HOME", "PATH", "USER", "LOGNAME", "LANG", "SHELL"} {
			if value, ok := os.LookupEnv(key); ok {
				cmd.Env = append(cmd.Env, key+"="+value)
			}
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		return cmd
	}
	upgradeFrom := os.Getenv("SWARM_TEAMMATE_TEST_UPGRADE_FROM")
	interruption := os.Getenv("SWARM_TEAMMATE_TEST_INTERRUPTION")
	interruptionFrom := os.Getenv("SWARM_TEAMMATE_TEST_INTERRUPTION_FROM")
	initial := bin
	if interruption == "upgrade" || interruption == "delete-parked" && interruptionFrom != "" {
		initial = interruptionFrom
		if !filepath.IsAbs(initial) {
			t.Fatal("set an absolute installed release binary path for the interruption")
		}
	}
	if upgradeFrom != "" {
		initial = upgradeFrom
	}
	cmd := startDaemon(initial)
	t.Cleanup(func() { _ = cmd.Process.Signal(syscall.SIGTERM); _ = cmd.Wait() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)
	dialOwner := func() *protocol.Client {
		var owner *protocol.Client
		for end := time.Now().Add(20 * time.Second); time.Now().Before(end); {
			owner, err = protocol.DialContext(ctx, socket, []string{protocol.CapManagerProvenance, protocol.CapManagerRetire})
			if err == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if err != nil {
			t.Fatal(err)
		}
		return owner
	}
	owner := dialOwner()
	t.Cleanup(func() {
		_ = owner.Close()
		end, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		cleanup, err := protocol.DialContext(end, socket, nil)
		if err != nil {
			t.Error("rehearsal cleanup connection", err)
			return
		}
		defer func() { _ = cleanup.Close() }()
		rows, err := cleanup.List()
		if err != nil {
			t.Error("rehearsal cleanup roster", err)
			return
		}
		for _, row := range rows {
			if row.Status.Process == status.ProcessRunning {
				if err := cleanup.Kill(row.ID); err != nil {
					t.Errorf("stop rehearsal %s: %v", row.ID, err)
				}
			}
		}
		for end.Err() == nil {
			rows, err = cleanup.List()
			if err != nil {
				t.Error(err)
				return
			}
			active := false
			for _, row := range rows {
				if row.Status.Process == status.ProcessRunning {
					active = true
					break
				}
			}
			if !active {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Error("rehearsal processes did not finish before cleanup deadline")
	})
	cfg := discord.Config{Mode: "teammate", Name: "Teammate rehearsal", InstallationID: "synthetic-place", ManagerCLI: "codex", WorkDir: workdir, DaemonSocket: socket, Projects: map[string]string{"fixture": project}}
	startup := os.Getenv("SWARM_TEAMMATE_TEST_STARTUP")
	if startup != "" {
		if startup != "codex" && startup != "claude" {
			t.Fatal("private startup rehearsal requires codex or claude")
		}
		cfg.ManagerCLI = startup
	}
	vmMode := os.Getenv("SWARM_TEAMMATE_TEST_VM") == "1"
	if vmMode {
		cfg.ProjectAccess = discord.ProjectAccessVM
		cfg.Projects = nil // The real job below uses an unregistered directory.
	}
	personal := os.Getenv("SWARM_TEAMMATE_TEST_PERSONAL") == "1"
	if personal {
		cfg.Mode, cfg.DMChannelID, cfg.OwnerID = "personal", "202", "401"
	}
	open := func() *Runtime {
		r, err := New(ctx, connector, owner.EndpointID(), state, cfg)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r := open()
	t.Cleanup(func() { _ = r.Close() })
	thread := discord.TeammateDiscussion{ID: "fixture-discussion", InstallationID: cfg.InstallationID, Route: discord.Route{GuildID: "101", ChannelID: "202", ParentID: "201"}}
	if personal {
		thread.Route = discord.Route{ChannelID: cfg.DMChannelID}
	}
	history := []discord.TeammateHistoryMessage{{ContextMessage: discord.ContextMessage{ID: "300", Author: discord.Actor{ID: "401", Name: "Alice"}, Text: "Our synthetic check is called violet orchard."}}}
	var historyMu sync.Mutex
	delivered := map[string]discord.TeammatePublication{}
	durable := os.Getenv("SWARM_TEAMMATE_TEST_DURABLE")
	multiRestart := os.Getenv("SWARM_TEAMMATE_TEST_MULTI_RESTART") == "1"
	reader := func(_ context.Context, q discord.TeammateHistoryQuery) (discord.TeammateHistoryPage, error) {
		historyMu.Lock()
		defer historyMu.Unlock()
		if durable == "" {
			return discord.TeammateHistoryPage{Messages: slices.Clone(history)}, nil
		}
		switch q.Source {
		case "", "thread":
			return discord.TeammateHistoryPage{Messages: slices.Clone(history)}, nil
		case "publication":
			if multiRestart && q.Publication != nil {
				if p, ok := delivered[q.Publication.ID]; ok && p.BodyHash == q.Publication.BodyHash && p.AdmissionID == q.Publication.AdmissionID {
					return discord.TeammateHistoryPage{ConfirmedMessageIDs: []string{fmt.Sprint(10000 + p.Sequence)}}, nil
				}
			}
			return discord.TeammateHistoryPage{}, nil
		case "audience":
			// This private rehearsal has a fixed audience.
			return discord.TeammateHistoryPage{}, nil
		case "admission", "grant":
			for _, m := range history {
				if m.ID == q.MessageID && m.Author.ID == q.AuthorID && q.AdmissionID == "input-"+m.ID && q.Revision == "rev-"+m.ID {
					return discord.TeammateHistoryPage{}, nil
				}
			}
			return discord.TeammateHistoryPage{}, discord.ErrTeammateSourceChanged
		default:
			return discord.TeammateHistoryPage{}, discord.ErrNotAuthorized
		}
	}
	admit := func(id, actor, body string) error {
		t.Helper()
		a := discord.TeammateAdmission{ExpectReply: multiRestart, Discussion: thread, OperationID: "input-" + id, Revision: "rev-" + id, Text: body, Route: discord.Route{GuildID: "101", ChannelID: "202", MessageID: id}, Author: discord.Actor{ID: actor, Name: actor}, InvokerID: actor, Authority: discord.AuthorityExternal}
		if personal {
			a.Authority = discord.AuthorityOwnerDirect
			a.Route.GuildID = ""
		}
		if durable != "" {
			historyMu.Lock()
			history = append(history, discord.TeammateHistoryMessage{ContextMessage: discord.ContextMessage{ID: id, Author: a.Author, Text: body, CreatedAt: time.Now().UTC()}})
			historyMu.Unlock()
		}
		return r.Admit(ctx, a, reader)
	}
	send := func(id, actor, body string) {
		t.Helper()
		if err := admit(id, actor, body); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	var outputs []discord.TeammatePublication
	wait := func(label string, limit time.Duration, match func() bool) {
		t.Helper()
		start := time.Now()
		var last error
		for time.Since(start) < limit && ctx.Err() == nil {
			last = r.Sync(ctx, thread, reader, func(p discord.TeammatePublication) error {
				if seen[p.ID] {
					return fmt.Errorf("repeated publication %s", p.ID)
				}
				seen[p.ID] = true
				historyMu.Lock()
				delivered[p.ID] = p
				historyMu.Unlock()
				outputs = append(outputs, p)
				return nil
			})
			if last == nil && match() {
				t.Logf("%s in %s", label, time.Since(start).Round(time.Second))
				return
			}
			time.Sleep(300 * time.Millisecond)
		}
		t.Fatalf("%s: deadline; outputs=%d last error=%v", label, len(outputs), last)
	}
	roster := func() []protocol.SessionView {
		rows, err := owner.List()
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}
	if n := len(roster()); n != 0 {
		t.Fatalf("native sessions before first contact: %d", n)
	}
	if startup != "" {
		if !personal {
			t.Fatal("private startup rehearsal requires personal mode")
		}
		want := "private-startup-" + hex.EncodeToString(marker[:])
		// The connector retries a saved source message when the manager is still
		// starting. Keep its operation ID and content identical on each attempt.
		body := "Reply in this discussion with exactly " + want + "."
		for attempt := 1; ; attempt++ {
			err := admit("301", cfg.OwnerID, body)
			if err == nil {
				break
			}
			if attempt >= 3 || !errors.Is(err, voice.ErrDiscussionActivationUnavailable) || !errors.Is(err, voice.ErrControllerNativeUnavailable) {
				t.Fatalf("startup admission %d: %v", attempt, err)
			}
			t.Logf("source input-301 retained while native manager starts: %v", err)
			time.Sleep(time.Second)
		}
		wait("native manager published the requested reply and finished its turn", 135*time.Second, func() bool {
			var matches []string
			for _, publication := range outputs {
				if publication.AdmissionID == "input-301" && strings.Contains(publication.Body, want) {
					matches = append(matches, publication.ID)
				}
			}
			if len(matches) > 1 {
				t.Fatalf("input-301 produced duplicate replies: %v", matches)
			}
			if len(matches) == 0 {
				return false
			}
			view, err := r.Status(thread)
			if err != nil || view.PendingAdmissions != 0 {
				return false
			}
			if view.State != "parked" {
				native, err := owner.ManagerRuntime(view.ManagerSession, view.ManagerInstance)
				if err != nil || !native.ReadyForInput || native.Turn.State != "completed" && native.Turn.State != "idle" {
					return false
				}
			}
			t.Logf("input-301 produced publication %s", matches[0])
			return true
		})
		return
	}
	if multiRestart {
		if !personal || !vmMode || durable != "codex" {
			t.Fatal("multi-worker restart requires a personal VM rehearsal with Codex workers")
		}
		expected := map[string]string{"north": proof, "south": steeringProof}
		for label, result := range expected {
			if err := os.WriteFile(filepath.Join(project, label+".txt"), []byte(result), 0600); err != nil {
				t.Fatal(err)
			}
		}
		instruction := "Dispatch exactly two ordinary Codex workers in " + project + " now. One is north and one is south. Give each worker this assignment, replacing LABEL with its own label: touch ready-LABEL, then wait until the local file release exists. Read only LABEL.txt, then run swarm teammate message --kind complete --text with exactly that file's contents, redirect the CLI receipt to sent-LABEL, and include the same contents in the native final answer. Only the workers may touch or read these files. For each incoming completion, publish its exact result as final=true in its own authenticated completion wake. Summarize both only once both have reported."
		send("301", cfg.OwnerID, instruction)
		held := map[string]protocol.SessionView{}
		procs := map[string]persist.Meta{}
		store, err := persist.NewStore(state)
		if err != nil {
			t.Fatal(err)
		}

		wait("both ordinary workers are executing their distinct assignments", 180*time.Second, func() bool {
			for label := range expected {
				if _, err := os.Stat(filepath.Join(project, "ready-"+label)); err != nil {
					return false
				}
			}
			for _, row := range roster() {
				if row.WorkerAttempt != "" && row.WorkerInstance != "" && row.Status.Process == status.ProcessRunning {
					_, local, ok := protocol.ParseID(row.ID)
					if !ok {
						t.Fatal(row.ID)
					}
					meta, err := store.Load(local)
					if err != nil || meta.ShimPID <= 0 || meta.ShimStartTime <= 0 {
						return false
					}
					procs[row.ID] = meta
					held[row.ID] = row
				}
			}
			return len(held) == 2
		})
		before, err := r.Status(thread)
		if err != nil {
			t.Fatal(err)
		}
		for _, note := range before.Notes {
			if note.Kind == "complete" {
				t.Fatalf("premature completion note %s", note.ID)
			}
		}
		for label, marker := range expected {
			if _, err := os.Stat(filepath.Join(project, "sent-"+label)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("%s completion receipt exists before release or cannot be checked: %v", label, err)
			}
			for _, output := range outputs {
				if strings.Contains(output.Body, marker) {
					t.Fatalf("premature %s result", label)
				}
			}
		}
		if err := r.Close(); err != nil {
			t.Fatal(err)
		}
		if err := owner.Close(); err != nil {
			t.Fatal(err)
		}
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		_ = cmd.Wait()
		cmd = startDaemon(bin)
		owner = dialOwner()
		for id, before := range held {
			found := false
			for _, after := range roster() {
				if after.ID == id && after.WorkerInstance == before.WorkerInstance && after.WorkerAttempt == before.WorkerAttempt && after.Status.Process == status.ProcessRunning {
					_, local, _ := protocol.ParseID(id)
					meta, err := store.Load(local)
					found = err == nil && meta.ShimPID == procs[id].ShimPID && meta.ShimStartTime == procs[id].ShimStartTime
				}
			}
			if !found {
				t.Fatalf("restart did not retain exact worker %s", id)
			}
		}
		if err := os.WriteFile(filepath.Join(project, "release"), nil, 0600); err != nil {
			t.Fatal(err)
		}
		receipts := make(map[string]string)
		for label := range expected {
			end := time.Now().Add(150 * time.Second)
			for {
				raw, err := os.ReadFile(filepath.Join(project, "sent-"+label))
				var receipt struct{ ID, State string }
				if err == nil && json.Unmarshal(raw, &receipt) == nil && receipt.State == "queued" && receipt.ID != "" {
					receipts[label] = receipt.ID
					break
				}
				if time.Now().After(end) {
					t.Fatalf("no queued completion for %s during connector outage", label)
				}
				time.Sleep(200 * time.Millisecond)
			}
		}
		r = open()
		wait("both worker results returned from their original turns after the restart", 180*time.Second, func() bool {
			results := make(map[string]string)
			for _, output := range outputs {
				if !strings.HasPrefix(output.AdmissionID, "worker-message-") || output.AttemptID == "" || output.JobID == "" {
					continue
				}
				for label, marker := range expected {
					if strings.Contains(output.Body, marker) {
						if _, ok := results[label]; ok {
							t.Fatalf("duplicate %s result publication", label)
						}
						results[label] = output.AttemptID
					}
				}
			}
			if len(results) != 2 || results["north"] == results["south"] {
				return false
			}
			view, err := r.Status(thread)
			if err != nil || len(view.Jobs) != 2 || view.PendingAdmissions != 0 || view.PendingReplies != 0 || view.UnconfirmedReplies != 0 {
				return false
			}
			attemptByReceipt := map[string]string{}
			for _, n := range view.Notes {
				if n.Kind == "complete" && n.AdmissionPhase == "completed" {
					original, ok := held[n.Session]
					if !ok || original.WorkerAttempt != n.AttemptID || original.WorkerInstance != n.Instance {
						t.Fatalf("completion %s came from a changed attempt or worker incarnation", n.ID)
					}
					if _, duplicate := attemptByReceipt[n.ID]; duplicate {
						t.Fatalf("duplicate completion note %s", n.ID)
					}
					attemptByReceipt[n.ID] = n.AttemptID
				}
			}
			for label, attempt := range results {
				if attemptByReceipt[receipts[label]] != attempt || len(attemptByReceipt) != 2 {
					return false
				}
				t.Logf("%s original completion attempt %s", label, attempt)
			}
			return true
		})
		return
	}
	if fixture := os.Getenv("SWARM_TEAMMATE_TEST_ATTACHMENT_FIXTURE"); fixture != "" {
		if !personal || !vmMode {
			t.Fatal("attachment rehearsal requires personal VM mode")
		}
		runHostedAttachment(t, ctx, r, thread, reader, project, state, fixture, wait, &outputs, roster)
		return
	}
	if !vmMode && durable == "codex" && interruption == "restart" {
		question := "Which tree " + hex.EncodeToString(marker[:4]) + "?"
		answer := "larch-" + hex.EncodeToString(marker[4:])
		if err := os.WriteFile(filepath.Join(project, "question.txt"), []byte(question), 0600); err != nil {
			t.Fatal(err)
		}
		send("301", "401", "Use one Codex worker in the fixture project. Its original turn must write the contents of question.txt to waiting.txt, then wait up to eight minutes for the file release. "+
			"After release it must ask the exact question from question.txt using swarm teammate message --kind needs-input --text, get your answer with swarm teammate reply --id and --wait 90s, and write the returned answer text in answer.txt. "+
			"Then it must send exactly the text from proof.txt using swarm teammate message --kind complete, save that queued receipt to sent.txt, and return exactly the same proof text in its native final. "+
			"Only the worker may read the project files. When you receive its question answer privately with exactly "+answer+" using swarm_reply. Do not reveal the answer to the worker in advance. Publish only one final result consisting of the exact proof text.")
		wait("enrolled worker waiting before restart", 150*time.Second, func() bool {
			raw, err := os.ReadFile(filepath.Join(project, "waiting.txt"))
			return err == nil && strings.TrimSpace(string(raw)) == question
		})
		before, err := r.Status(thread)
		if err != nil || len(before.Jobs) != 1 || before.Jobs[0].Instance == "" {
			t.Fatal("missing enrolled worker", before, err)
		}
		job := before.Jobs[0]
		_, local, ok := protocol.ParseID(job.Session)
		if !ok {
			t.Fatal("bad enrolled worker session", job.Session)
		}
		store, err := persist.NewStore(state)
		if err != nil {
			t.Fatal(err)
		}
		original, err := store.Load(local)
		if err != nil || original.ShimPID <= 0 || original.ShimStartTime <= 0 || original.Status.Process != status.ProcessRunning {
			t.Fatal("original worker must be running", err)
		}
		if err := owner.Close(); err != nil {
			t.Fatal(err)
		}
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		_ = cmd.Wait()
		cmd = startDaemon(bin)
		owner = dialOwner()
		challenge := "wake-" + hex.EncodeToString(marker[:])
		send("302", "401", "While the worker continues, publish one short reply containing exactly "+challenge+".")
		wait("Discord-only challenge after enrolled worker restart", 120*time.Second, func() bool {
			return slices.ContainsFunc(outputs, func(p discord.TeammatePublication) bool {
				return p.AdmissionID == "input-302" && strings.TrimSpace(p.Body) == challenge
			})
		})
		current, err := store.Load(local)
		if err != nil || current.ShimPID != original.ShimPID || current.ShimStartTime != original.ShimStartTime || current.Status.Process != status.ProcessRunning {
			t.Fatal("enrolled worker did not survive", err)
		}
		if err := os.WriteFile(filepath.Join(project, "release"), nil, 0600); err != nil {
			t.Fatal(err)
		}
		wait("recovered question returned exact private answer", 150*time.Second, func() bool {
			raw, err := os.ReadFile(filepath.Join(project, "answer.txt"))
			return err == nil && strings.TrimSpace(string(raw)) == answer
		})
		wait("enrolled completion and native evidence settled", 180*time.Second, func() bool {
			sent, readErr := os.ReadFile(filepath.Join(project, "sent.txt"))
			view, err := r.Status(thread)
			if readErr != nil || !strings.Contains(string(sent), "queued") || err != nil || view.PendingAdmissions != 0 || len(view.Jobs) != 1 || view.Jobs[0].State != "turn_completed" {
				return false
			}
			questions, completions := 0, 0
			for _, n := range view.Notes {
				if n.Session != job.Session || n.Instance != job.Instance || n.JobID != job.ID || n.AttemptID != job.AttemptID || n.Admission == 0 || n.AdmissionPhase != "completed" {
					continue
				}
				if n.Kind == "needs-input" && n.Answered {
					questions++
				}
				if n.Kind == "complete" {
					completions++
				}
			}
			return questions == 1 && completions == 1
		})
		wakes, reports := 0, 0
		for _, p := range outputs {
			if p.AdmissionID == "input-302" && strings.Contains(p.Body, challenge) {
				wakes++
			}
			if p.JobID == job.ID && p.AttemptID == job.AttemptID && strings.Contains(p.Body, proof) {
				reports++
			}
		}
		if wakes != 1 || reports != 1 {
			t.Fatalf("enrolled attempt %s produced %d challenge answers and %d result publications", job.AttemptID, wakes, reports)
		}
		t.Logf("one challenge and one result for enrolled %s/%s, attempt %s, original shim %d", job.Session, job.Instance, job.AttemptID, original.ShimPID)
		return
	}
	if provider := durable; provider != "" {
		if !vmMode || (provider != "codex" && provider != "claude") {
			t.Fatal("durable rehearsal requires VM mode and a codex or claude worker")
		}
		activeUpgrade := upgradeFrom != "" && os.Getenv("SWARM_TEAMMATE_TEST_ACTIVE_UPGRADE") == "1"
		var previousNative protocol.ManagerRuntimeBinding
		if upgradeFrom != "" {
			send("299", "401", "Reply here with exactly ready-to-upgrade.")
			wait("pre-upgrade manager published and settled the native turn", 90*time.Second, func() bool {
				st, err := r.Status(thread)
				if err != nil || st.State != "active" || st.PendingAdmissions != 0 {
					return false
				}
				native, err := owner.ManagerRuntime(st.ManagerSession, st.ManagerInstance)
				if err != nil || !native.ReadyForInput || (native.Turn.State != "idle" && native.Turn.State != "completed") {
					return false
				}
				for _, p := range outputs {
					if strings.Contains(p.Body, "ready-to-upgrade") {
						previousNative = native.Binding
						return true
					}
				}
				return false
			})
			before := roster()
			if len(before) != 1 || before[0].Role != "manager" {
				t.Fatal("expected one original manager")
			}
			expectedProcess := status.ProcessRunning
			if !activeUpgrade {
				expectedProcess = status.ProcessExited
				wait("outgoing supervisor parked the original manager", voice.DiscussionIdleGrace+45*time.Second, func() bool {
					st, err := r.Status(thread)
					if err != nil || st.State != "parked" || st.ManagerSession != before[0].ID || st.RetirementOperation == "" {
						return false
					}
					rows := roster()
					return len(rows) == 1 && rows[0].ID == before[0].ID && rows[0].Status.Process == status.ProcessExited
				})
			}
			if err := r.Close(); err != nil {
				t.Fatal(err)
			}
			_ = owner.Close()
			if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Wait(); err != nil {
				t.Fatal(err)
			}
			cmd = startDaemon(bin)
			owner = dialOwner()
			after := roster()
			if len(after) != 1 || after[0].ID != before[0].ID || after[0].Status.Process != expectedProcess {
				t.Fatal("upgrade lost the original manager")
			}
			r = open()
			st, err := r.Status(thread)
			if err != nil || !activeUpgrade && st.State != "parked" || st.ManagerSession != after[0].ID {
				t.Fatalf("retained parked binding: %+v, %v", st, err)
			}
			t.Logf("retained pre-upgrade dispatcher %s (%s)", after[0].ID, st.State)
		}
		question := "question-" + hex.EncodeToString(marker[:4])
		answer := "answer-" + hex.EncodeToString(marker[4:])
		if err := os.WriteFile(filepath.Join(project, "question.txt"), []byte(question), 0600); err != nil {
			t.Fatal(err)
		}
		a := "Use one " + provider + " worker in " + project + ". Ask it to send its first question with the marker from question.txt immediately, using a needs-input note. " +
			"Have it retrieve your reply with the question ID, write your exact answer into answer.txt, and stay in that same turn until the file release appears (allow up to ten minutes). " +
			"It should then send the contents of proof.txt with a complete note, save its queued receipt to sent.txt, and include the result in its native final answer. " +
			"Only the worker may open these files. When the question arrives, answer it with exactly " + answer + " and publish its question marker. Publish the verified final result when it arrives. Do not give the worker the answer in advance."
		send("301", "401", a)
		var job, attempt string
		wait("manager received early explicit question", 150*time.Second, func() bool {
			for _, p := range outputs {
				if strings.HasPrefix(p.AdmissionID, "worker-message-") && p.JobID != "" && p.AttemptID != "" && strings.Contains(p.Body, question) {
					job, attempt = p.JobID, p.AttemptID
					return true
				}
			}
			return false
		})
		if upgradeFrom != "" {
			st, err := r.Status(thread)
			if err != nil {
				t.Fatal(err)
			}
			native, err := owner.ManagerRuntime(st.ManagerSession, st.ManagerInstance)
			if err != nil || native.Binding.ConversationID != previousNative.ConversationID || native.Binding.Session == previousNative.Session ||
				(native.Binding.NativeSessionID == previousNative.NativeSessionID) == activeUpgrade {
				t.Fatalf("unexpected upgrade binding: %+v, %v", native.Binding, err)
			}
			var gaps int
			for _, p := range outputs {
				if p.SystemCause == discord.TeammateSystemContinuityGap {
					gaps++
				}
			}
			if activeUpgrade && gaps != 1 || !activeUpgrade && gaps != 0 {
				t.Fatalf("unexpected continuity notices: %d", gaps)
			}
			t.Logf("native history %s -> %s, logical discussion %s; %d continuity notices", previousNative.NativeSessionID, native.Binding.NativeSessionID, native.Binding.ConversationID, gaps)
		}
		wait("held worker received the exact answer", 90*time.Second, func() bool {
			raw, err := os.ReadFile(filepath.Join(project, "answer.txt"))
			return err == nil && strings.TrimSpace(string(raw)) == answer
		})
		beforeGap, err := r.Status(thread)
		if err != nil {
			t.Fatal(err)
		}
		deletedParked := interruption == "delete-parked"
		switch interruption {
		case "", "delete-parked":
			if deletedParked && !personal {
				t.Fatal("deleted dispatcher rehearsal requires personal mode")
			}
			// A native worker status transition can legitimately restart the idle grace.
			wait("manager parked with the same worker running", 2*voice.DiscussionIdleGrace+90*time.Second, func() bool {
				st, err := r.Status(thread)
				if err != nil || st.State != "parked" {
					return false
				}
				for _, row := range roster() {
					if row.SpawnedFrom != "" && strings.HasPrefix(row.Name, "DSP-") && row.Status.Process == status.ProcessRunning {
						return true
					}
				}
				return false
			})
			if deletedParked {
				beforeGap, err = r.Status(thread)
				if err != nil || beforeGap.State != "parked" || beforeGap.RetirementOperation == "" {
					t.Fatal("missing exact parked manager", beforeGap, err)
				}
			}
		case "connector", "restart", "upgrade", "death", "quota", "replay-refusal":
			if (!personal && interruption != "restart") || beforeGap.State != "active" || beforeGap.ManagerSession == "" {
				t.Fatal("interruption drill needs an active manager in the selected mode", beforeGap, err)
			}
		default:
			t.Fatal("unknown interruption", interruption)
		}
		var heldChild, heldInstance string
		if interruption == "restart" || deletedParked {
			for _, row := range roster() {
				if row.WorkerAttempt == attempt && row.WorkerInstance != "" && row.Status.Process == status.ProcessRunning {
					if heldChild != "" {
						t.Fatal("multiple live workers before restart")
					}
					heldChild, heldInstance = row.ID, row.WorkerInstance
				}
			}
			if heldChild == "" {
				t.Fatal("worker must be running at restart")
			}
		}
		var quotaTurn string
		if interruption == "quota" {
			quotaTurn = armHostedManagerQuota(t, state, project, beforeGap, owner, send, wait)
		}
		if err := r.Close(); err != nil {
			t.Fatal(err)
		}
		switch interruption {
		case "delete-parked", "restart", "upgrade", "replay-refusal":
			var retirement *protocol.ManagerRetireReq
			if deletedParked {
				id := r.mailDiscussionID(thread, owner.EndpointID())
				ledger, err := voice.OpenStore(filepath.Join(connector, "discussions", id))
				if err != nil {
					t.Fatal(err)
				}
				snapshot, err := ledger.Snapshot()
				if err := errors.Join(err, ledger.Close()); err != nil {
					t.Fatal(err)
				}
				retirement = snapshot.Conversations[voice.DiscussionConversationPrefix+id].Retirement
				if retirement == nil || protocol.ManagerRetireOperationID(*retirement) != beforeGap.RetirementOperation {
					t.Fatal("parked manager's retirement receipt changed")
				}
				if err := owner.Delete(beforeGap.ManagerSession); err != nil {
					t.Fatal("delete the exact parked dispatcher", err)
				}
			}
			if err := owner.Close(); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			_ = cmd.Wait()
			if interruption == "replay-refusal" {
				_, local, ok := protocol.ParseID(beforeGap.ManagerSession)
				if !ok {
					t.Fatal("invalid original manager identity", beforeGap)
				}
				store, err := persist.NewStore(state)
				if err != nil {
					t.Fatal(err)
				}
				m, err := store.Load(local)
				if err != nil || m.Role != "manager" || m.Status.Process != status.ProcessRunning || m.ShimPID <= 0 || syscall.Kill(m.ShimPID, 0) != nil {
					t.Fatal("original manager must still be running during supervisor outage", err)
				}
				m.ResumeError = "isolated rehearsal: previous native login unavailable"
				if err := store.Save(m); err != nil {
					t.Fatal(err)
				}
			}
			cmd = startDaemon(bin)
			owner = dialOwner()
			if deletedParked {
				if slices.ContainsFunc(roster(), func(row protocol.SessionView) bool { return row.ID == beforeGap.ManagerSession }) {
					t.Fatal("deleted dispatcher did not stay absent across daemon restart")
				}
				want := "deleted"
				if interruptionFrom != "" {
					want = "deleted_parked"
				}
				receipt, err := owner.ManagerRetire(ctx, *retirement, true)
				if err != nil || receipt.State != want || receipt.OperationID != beforeGap.RetirementOperation {
					t.Fatal("incorrect deletion evidence after daemon restart", receipt, err)
				}
				t.Logf("deletion receipt %s matched the exact parked retirement after restart", want)
			}
		case "quota":
			finishHostedManagerQuota(t, state, project, beforeGap, owner, quotaTurn)
		case "death":
			if err := owner.Kill(beforeGap.ManagerSession); err != nil {
				t.Fatal(err)
			}
		}
		challenge := "wake-" + hex.EncodeToString(marker[:])
		if interruption == "restart" {
			r = open()
			send("302", "401", "Without contacting the worker, publish one reply containing exactly "+challenge+".")
			wait("Discord input alone answered while the original worker was active", 90*time.Second, func() bool {
				for _, p := range outputs {
					if p.AdmissionID == "input-302" && strings.TrimSpace(p.Body) == challenge {
						return true
					}
				}
				return false
			})
			var liveWorker bool
			for _, row := range roster() {
				if row.ID == heldChild && row.WorkerAttempt == attempt && row.WorkerInstance == heldInstance && row.Status.Process == status.ProcessRunning {
					liveWorker = true
				}
			}
			if !liveWorker {
				t.Fatal("the original worker must still be running when the challenge is answered")
			}
			if err := r.Close(); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(project, "release"), nil, 0600); err != nil {
			t.Fatal(err)
		}
		var completionID string
		for end := time.Now().Add(3 * time.Minute); ; time.Sleep(200 * time.Millisecond) {
			raw, err := os.ReadFile(filepath.Join(project, "sent.txt"))
			if err == nil && strings.Contains(string(raw), "queued") {
				if deletedParked {
					var receipt struct{ ID, State string }
					if json.Unmarshal(raw, &receipt) != nil || receipt.State != "queued" || receipt.ID == "" {
						t.Fatal("worker did not save its exact queued completion receipt")
					}
					completionID = receipt.ID
				}
				break
			}
			if time.Now().After(end) {
				t.Fatal("worker did not record queued completion during connector downtime")
			}
		}
		r = open()
		if deletedParked {
			send("302", "401", "Also reply with exactly "+challenge+". The original worker result is still needed. Do not spawn another worker.")
		}
		wait("same attempt completion published after reconnect", 150*time.Second, func() bool {
			for _, p := range outputs {
				if strings.HasPrefix(p.AdmissionID, "worker-message-") && p.JobID == job && p.AttemptID == attempt && strings.Contains(p.Body, proof) {
					t.Logf("job %s attempt %s publication %s", job, attempt, p.ID)
					return true
				}
			}
			return false
		})
		if deletedParked {
			wait("concurrent owner message answered in the same discussion", 90*time.Second, func() bool {
				return slices.ContainsFunc(outputs, func(p discord.TeammatePublication) bool {
					return p.AdmissionID == "input-302" && strings.TrimSpace(p.Body) == challenge
				})
			})
		}
		if interruption == "restart" || deletedParked {
			wait("restarted manager settled after publication", 90*time.Second, func() bool {
				st, err := r.Status(thread)
				if err != nil || st.PendingAdmissions != 0 {
					return false
				}
				if st.State == "parked" {
					return true
				}
				native, err := owner.ManagerRuntime(st.ManagerSession, st.ManagerInstance)
				return err == nil && native.ReadyForInput && (native.Turn.State == "idle" || native.Turn.State == "completed")
			})
			var reports int
			for _, p := range outputs {
				if strings.Contains(p.Body, proof) {
					reports++
				}
			}
			if reports != 1 {
				t.Fatalf("expected one substantive result across restart, observed %d", reports)
			}
		}
		got, err := r.Status(thread)
		if err != nil {
			t.Fatal(err)
		}
		kinds := make(map[string]bool)
		for _, note := range got.Notes {
			if note.JobID != job || note.AttemptID != attempt || note.Admission == 0 {
				continue
			}
			if note.Kind == "needs-input" && !note.Answered {
				t.Fatal("missing durable reply export")
			}
			if deletedParked && (note.Session != heldChild || note.Instance != heldInstance ||
				note.Kind == "complete" && note.ID != completionID) {
				t.Fatal("replacement did not admit the original worker's exact CLI receipt", note)
			}
			if kinds[note.Kind] {
				t.Fatalf("duplicate admitted %s note", note.Kind)
			}
			kinds[note.Kind] = true
			t.Logf("%s note %s admitted as %d on %s", note.Kind, note.ID, note.Admission, note.Instance)
		}
		if interruption != "" {
			var activeManagers, children int
			for _, row := range roster() {
				if row.Role == "manager" && row.Status.Process == status.ProcessRunning {
					activeManagers++
				}
				if strings.HasPrefix(row.Name, "DSP-") {
					children++
					if heldChild != "" && row.ID != heldChild {
						t.Fatalf("worker changed across restart: %s -> %s", heldChild, row.ID)
					}
				}
			}
			if (interruption == "quota" || interruption == "replay-refusal" || deletedParked) && (got.ManagerSession == beforeGap.ManagerSession || got.Generation <= beforeGap.Generation || activeManagers != 1) {
				t.Fatal(interruption, "did not replace the original dispatcher exactly once", beforeGap, got)
			}
			if interruption == "replay-refusal" {
				var oldExited bool
				for _, row := range roster() {
					if row.ID == beforeGap.ManagerSession && row.Status.Process == status.ProcessExited {
						oldExited = true
					}
				}
				var reports int
				for _, p := range outputs {
					if p.JobID == job && p.AttemptID == attempt && strings.Contains(p.Body, proof) {
						reports++
					}
				}
				if !oldExited || reports != 1 {
					t.Fatalf("original manager exited=%v, correlated completion publications=%d", oldExited, reports)
				}
			}
			if deletedParked {
				for _, row := range roster() {
					if row.ID == beforeGap.ManagerSession {
						t.Fatal("deleted dispatcher unexpectedly reappeared")
					}
					if row.ID == heldChild && row.WorkerInstance != heldInstance {
						t.Fatal("original worker instance changed")
					}
				}
			}
			if activeManagers > 1 || children != 1 {
				t.Fatalf("after %s: %d active managers, %d worker launches", interruption, activeManagers, children)
			}
			t.Logf("%s recovered on same channel %s; old manager %s, current %s; one child, %d active managers", interruption, thread.Route.ChannelID, beforeGap.ManagerSession, got.ManagerSession, activeManagers)
		}
		if interruption == "restart" || deletedParked {
			replies := 0
			for _, p := range outputs {
				if p.AdmissionID == "input-302" && strings.Contains(p.Body, challenge) {
					replies++
				}
			}
			if replies != 1 {
				t.Fatalf("received %d challenge replies across connector reopen and worker completion", replies)
			}
		}
		if !kinds["needs-input"] || !kinds["complete"] {
			t.Fatalf("explicit admissions: %v", kinds)
		}
		return
	}
	if personal {
		for i, prefix := range []string{"birch", "cedar"} {
			code := prefix + "-" + hex.EncodeToString(marker[:])
			if err := voice.SaveManagerStartingPrompt(state, "When the owner asks for the check word, reply with "+code); err != nil {
				t.Fatal(err)
			}
			send(fmt.Sprint(301+i), cfg.OwnerID, "What is the current check word?")
			wait("personal owner received current standing instruction", 90*time.Second, func() bool {
				return len(outputs) > i && strings.Contains(outputs[len(outputs)-1].Body, code)
			})
		}
		if rows := roster(); len(rows) != 1 || rows[0].Role != "manager" {
			t.Fatalf("personal check expected a single manager, observed %d sessions", len(rows))
		}
		return
	}
	send("301", "402", "What did Alice call our synthetic check? Reply with its name.")
	wait("cold context answer", 90*time.Second, func() bool {
		for _, p := range outputs {
			if p.AdmissionID == "input-301" && strings.Contains(strings.ToLower(p.Body), "violet orchard") {
				return true
			}
		}
		return false
	})
	if messageMode := os.Getenv("SWARM_TEAMMATE_TEST_WORKER_MESSAGE"); messageMode == "1" || messageMode == "claude" {
		reply := "manager-reply-" + hex.EncodeToString(marker[:])
		var workerNonce [8]byte
		if _, err := rand.Read(workerNonce[:]); err != nil {
			t.Fatal(err)
		}
		workerMarker := "worker-question-" + hex.EncodeToString(workerNonce[:])
		if err := os.WriteFile(filepath.Join(project, "question.txt"), []byte(workerMarker+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		workerCommand := fmt.Sprintf(`%q teammate message --text "My marker is $(cat question.txt). What exact string should I write to reply.txt?"`, bin)
		provider := "Codex"
		workerFollowup := "Tell the worker to finish its native turn after queuing the question and wait for your separate follow-up."
		managerFollowup := "When the worker message wakes you, use swarm reply with the question ID to ask that same worker to write this exact value to reply.txt: "
		if messageMode == "claude" {
			provider = "Claude"
			workerFollowup = fmt.Sprintf("Tell the worker to take the queued message ID, run %q teammate reply --id MESSAGE_ID --wait 90s with that ID, then write the exact response text into reply.txt.", bin)
			managerFollowup = "When the worker message wakes you, use swarm reply with the question ID and this exact text: "
		}
		send("302", "401", "Launch exactly one "+provider+" worker in project fixture. Its initial assignment is to run this exact shell command: "+workerCommand+
			". "+workerFollowup+" Do not tell the worker the answer during creation. "+managerFollowup+reply+
			". After you receive the worker's actual message, also publish its exact marker in Discord. Do not access the files yourself.")
		wait("scoped worker message reached the native manager", 180*time.Second, func() bool {
			for _, p := range outputs {
				if p.JobID != "" && p.AttemptID != "" && strings.Contains(p.Body, workerMarker) {
					return true
				}
			}
			return false
		})
		wait("native worker received a reply to its scoped message", 90*time.Second, func() bool {
			raw, err := os.ReadFile(filepath.Join(project, "reply.txt"))
			return err == nil && strings.TrimSpace(string(raw)) == reply
		})
		if rows := roster(); len(rows) != 2 {
			t.Fatalf("wanted one manager and one worker, observed %d sessions", len(rows))
		}
		return
	}
	location := "project fixture"
	if vmMode {
		location = project
	}
	send("302", "401", "Start exactly one Codex worker in "+location+". Have it wait 15 seconds, read proof.txt, and return its exact text. Publish its result here. Leave the file to the worker.")
	wait("manager acknowledged the human separately from the worker result", 90*time.Second, func() bool {
		for _, p := range outputs {
			if p.AdmissionID == "input-302" && p.JobID == "" && p.QuestionID == "" && strings.TrimSpace(p.Body) != "" {
				return true
			}
		}
		return false
	})
	var dispatchedID string
	wait("visible dispatcher and one DSP worker", 90*time.Second, func() bool {
		rows := roster()
		if len(rows) != 2 {
			return false
		}
		for _, row := range rows {
			if row.Role != "manager" && strings.HasPrefix(row.Name, "DSP-") && row.SpawnedFrom != "" {
				dispatchedID = row.ID
				return true
			}
		}
		return false
	})
	send("303", "402", "Keep the original proof.txt task. Please send the same worker one follow-up: read second.txt and return its exact text. Report both verified results in this thread. Do not read either file yourself or start another worker.")
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	r = open()
	wait("manager acknowledged the second participant's steering", 90*time.Second, func() bool {
		for _, p := range outputs {
			if p.AdmissionID == "input-303" && p.QuestionID == "" && p.SystemCause == "" && strings.TrimSpace(p.Body) != "" && !strings.Contains(p.Body, steeringProof) {
				return true
			}
		}
		return false
	})
	wait("verified asynchronous job result after restart", 150*time.Second, func() bool {
		for _, p := range outputs {
			if p.JobID != "" && p.AttemptID != "" && strings.Contains(p.Body, proof) {
				return true
			}
		}
		return false
	})
	wait("second participant steered the same worker", 150*time.Second, func() bool {
		for _, p := range outputs {
			if p.JobID != "" && p.AttemptID != "" && strings.Contains(p.Body, steeringProof) {
				return true
			}
		}
		return false
	})
	if rows := roster(); len(rows) != 2 || (rows[0].ID != dispatchedID && rows[1].ID != dispatchedID) {
		t.Fatal("follow-up did not retain the original worker")
	}
	wait("idle retirement", voice.DiscussionIdleGrace+90*time.Second, func() bool {
		for _, row := range roster() {
			if row.Role == "manager" && row.Status.Process == status.ProcessRunning {
				return false
			}
		}
		return true
	})
	migrating := os.Getenv("SWARM_TEAMMATE_TEST_MIGRATE") == "1"
	if migrating {
		if err := r.Close(); err != nil {
			t.Fatal(err)
		}
		cfg.ProjectAccess = discord.ProjectAccessVM
		r = open()
	}
	if armResumeFailures != nil || migrating {
		old, err := r.Status(thread)
		if err != nil || len(old.Jobs) != 1 || old.State != "parked" {
			t.Fatalf("parked fixture: %+v %v", old, err)
		}
		verify := func(string) {}
		if armResumeFailures != nil {
			verify = armResumeFailures(state, old.ManagerSession)
		}
		resumeProof := "pine cone " + hex.EncodeToString(marker[:])
		if err := os.WriteFile(filepath.Join(project, "third.txt"), []byte(resumeProof), 0600); err != nil {
			t.Fatal(err)
		}
		instruction := "Use the same worker as before. Ask it to append one x to count.txt exactly once, read third.txt, and report its exact value here. Leave both files to that worker."
		admitDeadline := time.Now().Add(90 * time.Second)
		reopened := false
		for {
			err := admit("304", "402", instruction)
			if err == nil {
				break
			}
			if ctx.Err() != nil || time.Now().After(admitDeadline) {
				t.Fatal("parked follow-up admission failed", err)
			}
			if !reopened {
				if err := r.Close(); err != nil {
					t.Fatal(err)
				}
				r = open()
				reopened = true
			}
			time.Sleep(300 * time.Millisecond)
		}
		send("304", "402", instruction)
		wait("manager acknowledged the recovered follow-up", 90*time.Second, func() bool {
			for _, p := range outputs {
				if p.AdmissionID == "input-304" && p.QuestionID == "" && p.SystemCause == "" && strings.TrimSpace(p.Body) != "" && !strings.Contains(p.Body, resumeProof) {
					return true
				}
			}
			return false
		})
		wait("same worker delivered the recovered follow-up", 150*time.Second, func() bool {
			for _, p := range outputs {
				if p.JobID == old.Jobs[0].ID && p.AttemptID != "" && strings.Contains(p.Body, resumeProof) {
					return true
				}
			}
			return false
		})
		after, err := r.Status(thread)
		if err != nil || len(after.Jobs) != 1 || old.Jobs[0].Session != after.Jobs[0].Session || old.Jobs[0].Instance != after.Jobs[0].Instance {
			t.Fatal("recovery lost worker identity", err)
		}
		count, err := os.ReadFile(filepath.Join(project, "count.txt"))
		if err != nil || strings.TrimSpace(string(count)) != "x" {
			t.Fatalf("expected one worker effect, got %q: %v", count, err)
		}
		verify(after.ManagerSession)
		return
	}
	before := len(outputs)
	send("304", "402", "What exact value did the worker find in proof.txt earlier? Please quote it.")
	wait("continued conversation", 90*time.Second, func() bool {
		for _, p := range outputs[before:] {
			if strings.Contains(p.Body, proof) {
				return true
			}
		}
		return false
	})
	workers := 0
	for _, row := range roster() {
		if row.Role != "manager" {
			workers++
		}
	}
	if workers != 1 {
		t.Fatalf("wanted exactly one worker, observed %d", workers)
	}
}
