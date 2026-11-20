package teammatemanager

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/discord"
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
	socket := filepath.Join(state, "daemon.sock")
	cmd := exec.Command(bin, "daemon")
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
	t.Cleanup(func() { _ = cmd.Process.Signal(syscall.SIGTERM); _ = cmd.Wait() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)
	var owner *protocol.Client
	for end := time.Now().Add(20 * time.Second); time.Now().Before(end); {
		owner, err = protocol.DialContext(ctx, socket, nil)
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
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
	open := func() *Runtime {
		r, err := New(ctx, connector, owner.EndpointID(), "", cfg)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r := open()
	t.Cleanup(func() { _ = r.Close() })
	thread := discord.TeammateDiscussion{ID: "fixture-discussion", InstallationID: cfg.InstallationID, Route: discord.Route{GuildID: "101", ChannelID: "202", ParentID: "201"}}
	reader := func(_ context.Context, _ discord.TeammateHistoryQuery) (discord.TeammateHistoryPage, error) {
		return discord.TeammateHistoryPage{Messages: []discord.TeammateHistoryMessage{{ContextMessage: discord.ContextMessage{ID: "300", Author: discord.Actor{ID: "401", Name: "Alice"}, Text: "Our synthetic check is called violet orchard."}}}}, nil
	}
	send := func(id, actor, body string) {
		t.Helper()
		a := discord.TeammateAdmission{Discussion: thread, OperationID: "input-" + id, Revision: "rev-" + id, Text: body, Route: discord.Route{GuildID: "101", ChannelID: "202", MessageID: id}, Author: discord.Actor{ID: actor, Name: actor}, InvokerID: actor, Authority: discord.AuthorityExternal}
		if err := r.Admit(ctx, a, reader); err != nil {
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
	send("301", "402", "What did Alice call our synthetic check? Reply with its name.")
	wait("cold context answer", 90*time.Second, func() bool {
		return len(outputs) > 0 && strings.Contains(strings.ToLower(outputs[0].Body), "violet orchard")
	})
	if os.Getenv("SWARM_TEAMMATE_TEST_WORKER_MESSAGE") == "1" {
		reply := "manager-reply-" + hex.EncodeToString(marker[:])
		workerCommand := fmt.Sprintf("%q teammate message --text 'What exact string should I write to reply.txt?'", bin)
		send("302", "401", "Launch exactly one Codex worker in project fixture. Its initial assignment is to run this exact shell command: "+workerCommand+
			". Tell the worker to finish its native turn after queuing the question and wait for your separate follow-up. Do not tell the worker the answer during creation. "+
			"When you receive its actual message as a new turn, reply to that exact worker with swarm_send. Ask it to write this exact value to reply.txt: "+reply+
			". Do not write the file yourself. Publish a brief acknowledgment now, and report the completed worker result after verifying it.")
		wait("native worker received a reply to its scoped message", 180*time.Second, func() bool {
			raw, err := os.ReadFile(filepath.Join(project, "reply.txt"))
			return err == nil && strings.TrimSpace(string(raw)) == reply
		})
		if rows := roster(); len(rows) != 2 {
			t.Fatalf("wanted one manager and one worker, observed %d sessions", len(rows))
		}
		return
	}
	send("302", "401", "Start one Codex worker in project fixture. Have it wait 15 seconds, read proof.txt, and return its exact text. Publish a short acknowledgement and end this turn. When the worker returns, publish its exact result here. Use only the scoped Swarm tools and leave the file to the worker.")
	wait("worker launch", 90*time.Second, func() bool { return len(roster()) == 2 })
	send("303", "402", "Keep the original proof.txt task. Please send the same worker one follow-up: read second.txt and return its exact text. Report both verified results in this thread. Use scoped Swarm tools; do not read either file yourself or start another worker.")
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	r = open()
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
	wait("idle retirement", voice.DiscussionIdleGrace+90*time.Second, func() bool {
		for _, row := range roster() {
			if row.Role == "manager" && row.Status.Process == status.ProcessRunning {
				return false
			}
		}
		return true
	})
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
