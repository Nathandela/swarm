package teammatemanager

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/discord"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
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
	callCanary := os.Getenv("SWARM_TEAMMATE_TEST_CALL") == "1"
	nativeHome := os.Getenv("SWARM_TEAMMATE_TEST_NATIVE_HOME")
	if callCanary {
		resolvedHome, resolveErr := filepath.EvalSymlinks(nativeHome)
		ownerHome, ownerErr := filepath.EvalSymlinks(os.Getenv("HOME"))
		if !filepath.IsAbs(nativeHome) || resolveErr != nil || ownerErr != nil || resolvedHome == ownerHome {
			t.Fatal("call canary requires an isolated SWARM_TEAMMATE_TEST_NATIVE_HOME")
		}
		nativeHome = resolvedHome
	}
	startDaemon := func(binary string) *exec.Cmd {
		cmd := exec.Command(binary, "daemon")
		cmd.Dir = project
		cmd.Env = []string{"SWARM_DAEMON_STATE=" + state, "SWARM_DAEMON_SOCK=" + socket, "SWARM_DAEMON_LOCK=" + filepath.Join(state, "daemon.lock"), "SWARM_DAEMON_LOG=" + filepath.Join(state, "daemon.log")}
		for _, key := range []string{"HOME", "PATH", "USER", "LOGNAME", "LANG", "SHELL"} {
			if key == "HOME" && callCanary {
				cmd.Env = append(cmd.Env, key+"="+nativeHome)
				continue
			}
			if value, ok := os.LookupEnv(key); ok {
				cmd.Env = append(cmd.Env, key+"="+value)
			}
		}
		if callCanary {
			startup, err := os.OpenFile(filepath.Join(root, "supervisor-startup.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = startup.Close() }()
			cmd.Stderr = startup
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		return cmd
	}
	upgradeFrom := os.Getenv("SWARM_TEAMMATE_TEST_UPGRADE_FROM")
	interruption := os.Getenv("SWARM_TEAMMATE_TEST_INTERRUPTION")
	initial := bin
	if interruption == "upgrade" {
		initial = os.Getenv("SWARM_TEAMMATE_TEST_INTERRUPTION_FROM")
		if !filepath.IsAbs(initial) {
			t.Fatal("set the installed release binary for the in-flight upgrade")
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
			owner, err = protocol.DialContext(ctx, socket, []string{protocol.CapManagerProvenance})
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
	reader := func(_ context.Context, _ discord.TeammateHistoryQuery) (discord.TeammateHistoryPage, error) {
		return discord.TeammateHistoryPage{Messages: []discord.TeammateHistoryMessage{{ContextMessage: discord.ContextMessage{ID: "300", Author: discord.Actor{ID: "401", Name: "Alice"}, Text: "Our synthetic check is called violet orchard."}}}}, nil
	}
	admit := func(id, actor, body string) error {
		t.Helper()
		a := discord.TeammateAdmission{Discussion: thread, OperationID: "input-" + id, Revision: "rev-" + id, Text: body, Route: discord.Route{GuildID: "101", ChannelID: "202", MessageID: id}, Author: discord.Actor{ID: actor, Name: actor}, InvokerID: actor, Authority: discord.AuthorityExternal}
		if personal {
			a.Authority = discord.AuthorityOwnerDirect
			a.Route.GuildID = ""
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
	if callCanary {
		if upgradeFrom != "" {
			t.Fatal("call canary requires a single candidate supervisor")
		}
		send("301", "401", "Remember this exact marker in our discussion: "+proof+". Publish only seeded to acknowledge it. No shell, file writes, or delegated work.")
		var manager, instance string
		wait("written seed settled in one native discussion", 90*time.Second, func() bool {
			st, statusErr := r.Status(thread)
			if statusErr != nil || st.State != "active" || st.PendingAdmissions != 0 {
				return false
			}
			for _, p := range outputs {
				if p.AdmissionID == "input-301" && strings.TrimSpace(p.Body) == "seeded" {
					manager, instance = st.ManagerSession, st.ManagerInstance
					return true
				}
			}
			return false
		})
		const canaryText = "For the call continuity canary, publish only the exact marker you remember from our previous written instruction. No shell, file reads, network, or delegated work."
		const callOperation = "dv_hosted_canary"
		authority := discord.AuthorityExternal
		if personal {
			authority = discord.AuthorityOwnerDirect
		}
		hash := discord.PublicationBodyHash(canaryText)
		coord := schema.VoiceOrigin{CallID: "hosted-canary-call", TurnID: "hosted-canary-turn", SpeakerID: "401", SupervisorSocket: socket, BodyHash: hash, Generation: 1}
		admission := discord.TeammateAdmission{Discussion: thread, OperationID: callOperation, Revision: hash, Text: canaryText,
			Route: thread.Route, Author: discord.Actor{ID: coord.SpeakerID, Name: "Synthetic Alice"}, InvokerID: coord.SpeakerID, Authority: authority, Voice: coord}
		textReader := reader
		admissionChecks := 0
		reader = func(callCtx context.Context, q discord.TeammateHistoryQuery) (discord.TeammateHistoryPage, error) {
			if q.Source != "admission" && q.Source != "grant" {
				return textReader(callCtx, q)
			}
			if q.Voice != coord || q.AdmissionID != callOperation || q.AuthorID != coord.SpeakerID || q.Revision != hash ||
				q.Authority != authority || q.MessageID != "" || q.Anchor != "" || q.Cursor != "" {
				return discord.TeammateHistoryPage{}, discord.ErrTeammateSourceChanged
			}
			if q.Source == "admission" {
				admissionChecks++
			}
			return discord.TeammateHistoryPage{}, nil
		}
		if err := r.Admit(ctx, admission, reader); err != nil {
			t.Fatal(err)
		}
		if err := r.Admit(ctx, admission, reader); err != nil {
			t.Fatal("queued replay", err)
		}
		wait("verified call marker returned by the same native discussion", 90*time.Second, func() bool {
			st, statusErr := r.Status(thread)
			if statusErr != nil || st.State != "active" || st.PendingAdmissions != 0 {
				return false
			}
			if st.ManagerSession != manager || st.ManagerInstance != instance {
				t.Fatal("call selected different native manager")
			}
			for _, p := range outputs {
				if p.AdmissionID == callOperation && strings.TrimSpace(p.Body) == proof {
					p.BodyHash = discord.PublicationBodyHash(p.Body)
					origin, originErr := r.PublicationCallAdmission(ctx, thread, p)
					if originErr != nil || origin != callOperation {
						t.Fatalf("canonical call publication origin: %q %v", origin, originErr)
					}
					return true
				}
			}
			return false
		})
		if err := r.Admit(ctx, admission, reader); err != nil {
			t.Fatal("cutover replay", err)
		}
		if phase, err := r.AdmissionState(ctx, admission); phase != discord.TeammateAdmissionCutover || err != nil || admissionChecks < 2 {
			t.Fatalf("voice admission receipt: %q, %v; live checks %d", phase, err, admissionChecks)
		}
		if err := r.Sync(ctx, thread, reader, func(p discord.TeammatePublication) error {
			return fmt.Errorf("cutover replay produced another publication %s", p.ID)
		}); err != nil {
			t.Fatal(err)
		}
		native, err := owner.ManagerRuntime(manager, instance)
		if err != nil || !adapter.IsCanonicalConversationID(native.Binding.NativeSessionID) {
			t.Fatalf("unconfirmed canary native context: %v", err)
		}
		const voiceInputMarker = "For the call continuity canary"
		nativeInputs := codexCanaryInputs(t, nativeHome, native.Binding.NativeSessionID, voiceInputMarker)
		spokenOutputs := 0
		for _, p := range outputs {
			if p.AdmissionID == callOperation {
				spokenOutputs++
			}
		}
		if nativeInputs != 1 || spokenOutputs != 1 || len(roster()) != 1 {
			t.Fatalf("canary custody counts: native inputs=%d canonical outputs=%d", nativeInputs, spokenOutputs)
		}
		t.Logf("native canary observed %d input, %d canonical output and %d live grant checks; no Discord or speech traffic", nativeInputs, spokenOutputs, admissionChecks)
		return
	}
	if fixture := os.Getenv("SWARM_TEAMMATE_TEST_ATTACHMENT_FIXTURE"); fixture != "" {
		if !personal || !vmMode {
			t.Fatal("attachment rehearsal requires personal VM mode")
		}
		runHostedAttachment(t, ctx, r, thread, reader, project, state, fixture, wait, &outputs, roster)
		return
	}
	if provider := os.Getenv("SWARM_TEAMMATE_TEST_DURABLE"); provider != "" {
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
		switch interruption {
		case "":
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
		case "connector", "restart", "upgrade", "death", "quota":
			if !personal || beforeGap.State != "active" || beforeGap.ManagerSession == "" {
				t.Fatal("interruption drill needs an active personal manager", beforeGap, err)
			}
		default:
			t.Fatal("unknown interruption", interruption)
		}
		var quotaTurn string
		if interruption == "quota" {
			quotaTurn = armHostedManagerQuota(t, state, project, beforeGap, owner, send, wait)
		}
		if err := r.Close(); err != nil {
			t.Fatal(err)
		}
		switch interruption {
		case "restart", "upgrade":
			if err := owner.Close(); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			_ = cmd.Wait()
			cmd = startDaemon(bin)
			owner = dialOwner()
		case "quota":
			finishHostedManagerQuota(t, state, project, beforeGap, owner, quotaTurn)
		case "death":
			if err := owner.Kill(beforeGap.ManagerSession); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(project, "release"), nil, 0600); err != nil {
			t.Fatal(err)
		}
		for end := time.Now().Add(3 * time.Minute); ; time.Sleep(200 * time.Millisecond) {
			raw, err := os.ReadFile(filepath.Join(project, "sent.txt"))
			if err == nil && strings.Contains(string(raw), "queued") {
				break
			}
			if time.Now().After(end) {
				t.Fatal("worker did not record queued completion during connector downtime")
			}
		}
		r = open()
		wait("same attempt completion published after reconnect", 150*time.Second, func() bool {
			for _, p := range outputs {
				if strings.HasPrefix(p.AdmissionID, "worker-message-") && p.JobID == job && p.AttemptID == attempt && strings.Contains(p.Body, proof) {
					t.Logf("job %s attempt %s publication %s", job, attempt, p.ID)
					return true
				}
			}
			return false
		})
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
				}
			}
			if interruption == "quota" && (got.ManagerSession == beforeGap.ManagerSession || got.Generation <= beforeGap.Generation || activeManagers != 1) {
				t.Fatal("quota did not replace the original dispatcher exactly once", beforeGap, got)
			}
			if activeManagers > 1 || children != 1 {
				t.Fatalf("after %s: %d active managers, %d worker launches", interruption, activeManagers, children)
			}
			t.Logf("%s recovered on same DM %s; old manager %s, current %s; one child, %d active managers", interruption, thread.Route.ChannelID, beforeGap.ManagerSession, got.ManagerSession, activeManagers)
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

func codexCanaryInputs(t *testing.T, home, session, marker string) int {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(home, ".codex", "sessions", "*", "*", "*", "rollout-*-"+session+".jsonl"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("isolated Codex rollout unavailable for bound native context: %v", err)
	}
	file, err := os.Open(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 16<<20 {
		t.Fatalf("invalid canary Codex rollout: %v", err)
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	matches := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		if !bytes.Contains(line, []byte(marker)) {
			continue
		}
		var row struct {
			Type    string
			Payload struct{ Type, Role string }
		}
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatal("malformed canary rollout row")
		}
		// Codex also records an item_completed telemetry row for the same input.
		if row.Type == "response_item" && row.Payload.Type == "message" && row.Payload.Role == "user" {
			matches++
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return matches
}
