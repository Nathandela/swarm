//go:build linux

package skeleton

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/enrollment"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/procstart"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/coder/websocket"
)

const accountFixtureDeviceCode = "synthetic-private-device-code"

// The real swarm executable supplies the detached supervisor and runner. A copy
// of this test executable supplies only the local native WebSocket fixture.
func TestMain(m *testing.M) {
	if name := filepath.Base(os.Args[0]); name == "claude" || name == "2.1.289" || name == "2.1.290" {
		if len(os.Args) == 2 && os.Args[1] == "--version" {
			version := "2.1.289"
			executable, _ := os.Executable()
			if filepath.Base(executable) == "2.1.290" {
				version = "2.1.290"
			}
			fmt.Println(version + " (Claude Code)")
			os.Exit(0)
		}
		// Only an explicit private fixture marker admits a synthetic child. It
		// records process selection and idles; it never invokes a provider/model.
		record, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".retention-fixture-record"))
		if err != nil {
			os.Exit(91)
		}
		executable, _ := os.Executable()
		cwd, _ := os.Getwd()
		start, _ := procstart.StartTime(os.Getpid())
		raw, _ := json.Marshal(struct {
			Executable, Argv0, Cwd string
			PID                    int
			StartTime              int64
		}{executable, os.Args[0], cwd, os.Getpid(), start})
		if os.WriteFile(string(record), raw, 0o600) != nil {
			os.Exit(92)
		}
		fmt.Println("synthetic retained child ready")
		for {
			time.Sleep(time.Second)
		}
	}
	if filepath.Base(os.Args[0]) == "account-fixture-codex" {
		if len(os.Args) == 2 && os.Args[1] == "--version" {
			fmt.Println("codex-cli 0.160.0")
			os.Exit(0)
		}
		accountFixtureCodex()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func accountFixtureCodex() {
	var socket string
	for _, arg := range os.Args {
		if strings.HasPrefix(arg, "unix://") {
			socket = strings.TrimPrefix(arg, "unix://")
		}
	}
	// Publish owner-only permissions at bind time, as the native server does
	// before publishing its alias. A post-bind chmod alone has a visible race.
	mask := syscall.Umask(0o177)
	listener, err := net.Listen("unix", socket)
	syscall.Umask(mask)
	if err != nil {
		os.Exit(2)
	}
	// The pinned native server restricts its bound socket to the owner.
	if err := os.Chmod(socket, 0o600); err != nil {
		os.Exit(2)
	}
	mode, _ := os.ReadFile(filepath.Join(filepath.Dir(os.Args[0]), "native-fixture-mode"))
	_ = http.Serve(listener, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		conn, err := websocket.Accept(writer, request, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		authenticated := false
		write := func(value any) {
			raw, _ := json.Marshal(value)
			_ = conn.Write(context.Background(), websocket.MessageText, raw)
		}
		complete := func() {
			_ = os.WriteFile(filepath.Join(os.Getenv("CODEX_HOME"), "auth.json"), accountTestCodexRaw("native-fixture-account", "native@example.test"), 0o600)
			authenticated = true
			write(map[string]any{"method": "account/login/completed", "params": map[string]any{"success": true}})
		}
		for {
			_, raw, err := conn.Read(context.Background())
			if err != nil {
				return
			}
			var rpc struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if json.Unmarshal(raw, &rpc) != nil {
				return
			}
			var result any
			switch rpc.Method {
			case "initialized":
				continue
			case "initialize":
				result = map[string]any{"userAgent": "codex-cli 0.160.0"}
			case "account/read":
				result = map[string]any{"account": nil}
				if authenticated {
					result = map[string]any{"account": map[string]any{"type": "chatgpt", "email": "native@example.test", "planType": "plus"}}
				}
			case "account/login/start":
				_ = os.WriteFile(filepath.Join(filepath.Dir(os.Args[0]), "native-started"), []byte("1"), 0o600)
				if string(mode) == "ready" {
					complete() // Native completion may precede the start result.
				}
				result = map[string]any{"type": "chatgptDeviceCode", "loginId": "fixture-login", "verificationUrl": "https://auth.example.test/device", "userCode": accountFixtureDeviceCode}
			case "account/login/cancel":
				complete() // Exercise success racing a durable cancellation.
				result = map[string]any{}
			}
			write(map[string]any{"id": rpc.ID, "result": result})
		}
	}))
}

func accountTestNative(t *testing.T, m *accountManager, mode string) string {
	t.Helper()
	buildBinaries(t)
	m.executable = swarmBin
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	native := filepath.Join(m.stateRoot, "account-fixture-codex")
	target, err := os.OpenFile(native, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(target, source)
	closeErr := target.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatalf("copy offline native fixture: %v %v", copyErr, closeErr)
	}
	accountTestPut(t, filepath.Join(m.stateRoot, "native-fixture-mode"), []byte(mode))
	m.native["codex"] = &persist.CLIIdentity{Path: native, Version: "0.160.0"}
	return native
}

func accountTestJob(t *testing.T, m *accountManager, c accounts.Candidate, state, method, target string) accountJob {
	t.Helper()
	job := accountJob{ID: c.ID, Generation: 1, Provider: c.Provider, Method: method, State: state, Candidate: c, TargetAccountID: target, Deadline: time.Now().Add(15 * time.Minute).UTC()}
	if err := m.saveJob(job); err != nil {
		t.Fatal(err)
	}
	return job
}

func accountTestDeadProcess(t *testing.T) enrollment.ProcessIdentity {
	t.Helper()
	cmd := exec.Command("/bin/true")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	start, err := procstart.StartTime(cmd.Process.Pid)
	if err != nil {
		_ = cmd.Wait()
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	return enrollment.ProcessIdentity{PID: cmd.Process.Pid, StartTime: start}
}

func accountTestProgress(t *testing.T, m *accountManager, job accountJob, worker enrollment.ProcessIdentity) enrollment.Progress {
	t.Helper()
	p := enrollment.Progress{SchemaVersion: enrollment.SchemaVersion, JobID: job.ID, Generation: job.Generation, Phase: enrollment.PhaseReady, Worker: worker, WritersStopped: true, NativeWritersStopped: true, Email: "fixture@example.test", Plan: "plus", UpdatedAt: time.Now().UTC()}
	raw, _ := json.Marshal(p)
	accountTestPut(t, filepath.Join(m.stateRoot, "accounts", "jobs", job.ID, enrollment.ProgressFile), raw)
	return p
}

func TestAccountManagerRecoversAdmissionCommitBeforeJobSave(t *testing.T) {
	root := accountTestState(t)
	m := accountTestManager(t, root)
	c := accountTestCandidate(t, m, "codex", "crash-account")
	job := accountTestJob(t, m, c, "admitting", "import-native", "")
	account := accountTestAdmit(t, m, c)
	// Crash boundary: registry admission committed, canonical job is still
	// admitting. Reopening must discover that exact private generation.
	m.close()
	reopened := accountTestManager(t, root)
	out, err := reopened.Accounts(protocol.AccountsReq{Action: "status", JobID: job.ID})
	if err != nil || out.Job == nil || out.Job.State != "admitted" {
		t.Fatalf("admission recovery: err=%v job=%v", err, out.Job)
	}
	if reopened.jobs.Jobs[job.ID].AdmittedAccountID != account.ID || len(accountTestRegistry(t, reopened).Accounts) != 1 {
		t.Fatal("admission replay duplicated or lost committed account")
	}
}

func TestAccountManagerCancelAfterAdmissionCommitDoesNotEraseCandidate(t *testing.T) {
	root := accountTestState(t)
	m := accountTestManager(t, root)
	c := accountTestCandidate(t, m, "codex", "cancel-after-admit")
	job := accountTestJob(t, m, c, "admitting", "import-native", "")
	account := accountTestAdmit(t, m, c)
	before, err := os.ReadFile(filepath.Join(c.ProfilePath, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	m.close()
	m = accountTestManager(t, root)
	out, err := m.Accounts(protocol.AccountsReq{Action: "cancel", JobID: job.ID})
	if err != nil || out.Job == nil || out.Job.State != "admitted" {
		t.Fatalf("committed admission lost to cancel: err=%v", err)
	}
	after, err := os.ReadFile(filepath.Join(c.ProfilePath, "auth.json"))
	if err != nil || string(before) != string(after) {
		t.Fatal("cancel erased or rewrote admitted credentials")
	}
	if _, ok := accountTestRegistry(t, m).Accounts[account.ID]; !ok {
		t.Fatal("cancel removed committed account")
	}
}

func TestAccountManagerReauthenticationTargetSurvivesReopen(t *testing.T) {
	root := accountTestState(t)
	m := accountTestManager(t, root)
	original := accountTestCandidate(t, m, "codex", "reauth-account")
	account := accountTestAdmit(t, m, original)
	candidate := accountTestCandidate(t, m, "codex", "reauth-account")
	job := accountTestJob(t, m, candidate, "ready", "import-native", account.ID)
	m.close()
	m = accountTestManager(t, root)
	out, err := m.Accounts(protocol.AccountsReq{Action: "status", JobID: job.ID})
	if err != nil || out.Job == nil || out.Job.TargetAccountID != account.ID {
		t.Fatal("reauthentication target lost across reopen")
	}
	out, err = m.Accounts(protocol.AccountsReq{Action: "admit", JobID: job.ID, AccountID: account.ID, ExpectedRevision: accountTestRegistry(t, m).Revision})
	if err != nil || out.Job == nil || out.Job.State != "admitted" {
		t.Fatalf("reauthentication admission refused: %v", err)
	}
	registry := accountTestRegistry(t, m)
	if len(registry.Accounts) != 1 || registry.Accounts[account.ID].CurrentGeneration != 2 {
		t.Fatal("reauthentication created a different logical account")
	}
}

func TestAccountManagerWorkerDisplayPersistsAfterWriterExit(t *testing.T) {
	root := accountTestState(t)
	m := accountTestManager(t, root)
	c := accountTestCandidate(t, m, "codex", "ready-display")
	job := accountTestJob(t, m, c, "authenticating", "device-code", "")
	job.Worker = accountTestDeadProcess(t)
	if err := m.saveJob(job); err != nil {
		t.Fatal(err)
	}
	accountTestProgress(t, m, job, job.Worker)
	out, err := m.Accounts(protocol.AccountsReq{Action: "status", JobID: job.ID})
	if err != nil || out.Job == nil || out.Job.State != "ready" || out.Job.Email != "fixture@example.test" || out.Job.Plan != "plus" {
		t.Fatalf("post-exit review fields lost: err=%v job=%v", err, out.Job)
	}
	m.close()
	m = accountTestManager(t, root)
	out, err = m.Accounts(protocol.AccountsReq{Action: "status", JobID: job.ID})
	if err != nil || out.Job == nil || out.Job.Email != "fixture@example.test" || out.Job.Plan != "plus" {
		t.Fatal("ready review fields lost across daemon reopen")
	}
}

func TestAccountManagerImportDisplayAndCanonicalSecrecy(t *testing.T) {
	root := accountTestState(t)
	m := accountTestManager(t, root)
	source := filepath.Join(root, "owner-selected-import", "auth.json")
	accountTestPut(t, source, accountTestCodexRaw("import-display", "import@example.test"))
	candidate := accountTestCandidate(t, m, "codex", "import-display")
	job := accountTestJob(t, m, candidate, "ready", "import-native", "")
	job.Email = "import@example.test"
	if err := m.saveJob(job); err != nil {
		t.Fatal(err)
	}
	out, err := m.Accounts(protocol.AccountsReq{Action: "status", JobID: job.ID})
	if err != nil || out.Job == nil || out.Job.Email != "import@example.test" {
		t.Fatalf("import review identity unavailable: %v", err)
	}
	jobID := out.Job.ID
	m.close()
	m = accountTestManager(t, root)
	out, err = m.Accounts(protocol.AccountsReq{Action: "status", JobID: jobID})
	if err != nil || out.Job == nil || out.Job.Email != "import@example.test" {
		t.Fatal("import review identity lost across reopen")
	}
	raw, err := os.ReadFile(filepath.Join(m.stateRoot, "accounts", "enrollment.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{accountFixtureBearer, accountFixtureRefresh, source, `"access_token"`, `"refresh_token"`, `"id_token"`, `"profile_path"`, `"user_code"`, `"verification_url"`} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatal("native secret or execution path persisted in canonical enrollment")
		}
	}
	// Token-only execution is uncharacterized: refuse before creating a job,
	// candidate or token vault entry, and never persist the supplied secret.
	const token = "synthetic-private-claude-token"
	out, err = m.Accounts(protocol.AccountsReq{Action: "import", Provider: "claude", Method: "token-manual", Token: token})
	if !errors.Is(err, protocol.ErrAccountsUnavailable) || out.Job != nil {
		t.Fatal("uncharacterized token-only enrollment was advertised or admitted")
	}
	raw, _ = os.ReadFile(filepath.Join(m.stateRoot, "accounts", "enrollment.json"))
	if strings.Contains(string(raw), token) || strings.Contains(string(raw), `"token":`) {
		t.Fatal("opaque token persisted in canonical job")
	}
}

func TestAccountManagerStaleCASDoesNotBeginAdmission(t *testing.T) {
	m := accountTestManager(t, accountTestState(t))
	c := accountTestCandidate(t, m, "codex", "stale-cas")
	job := accountTestJob(t, m, c, "ready", "import-native", "")
	before := accountTestRegistry(t, m)
	if _, err := m.store.SetEnabled(before.Revision, "claude", true); err != nil {
		t.Fatal(err)
	}
	_, err := m.Accounts(protocol.AccountsReq{Action: "admit", JobID: job.ID, Label: "Fixture", ExpectedRevision: before.Revision})
	if !errors.Is(err, protocol.ErrAccountsStaleRevision) || m.jobs.Jobs[job.ID].State != "ready" || len(accountTestRegistry(t, m).Accounts) != 0 {
		t.Fatal("stale CAS crossed admission boundary")
	}
}

func TestAccountManagerOneActiveJobPerProvider(t *testing.T) {
	m := accountTestManager(t, accountTestState(t))
	c := accountTestCandidate(t, m, "codex", "active-job")
	accountTestJob(t, m, c, "ready", "import-native", "")
	accountTestNative(t, m, "ready")
	before, _ := os.ReadDir(filepath.Join(m.stateRoot, "accounts", "profiles"))
	_, err := m.Accounts(protocol.AccountsReq{Action: "start", Provider: "codex", Method: "device-code"})
	after, _ := os.ReadDir(filepath.Join(m.stateRoot, "accounts", "profiles"))
	if !errors.Is(err, protocol.ErrAccountsUnavailable) || len(m.jobs.Jobs) != 1 || len(before) != len(after) {
		t.Fatal("duplicate provider job created another candidate writer")
	}
}

func TestAccountManagerCancellingWithoutWorkerProofBlocksAnotherProviderJob(t *testing.T) {
	m := accountTestManager(t, accountTestState(t))
	c, err := m.store.CreateCandidate("codex", accounts.KindNative)
	if err != nil {
		t.Fatal(err)
	}
	job := accountTestJob(t, m, c, "pending", "device-code", "")
	jobDir := filepath.Join(m.stateRoot, "accounts", "jobs", job.ID)
	if err := os.Mkdir(jobDir, 0o700); err != nil {
		t.Fatal(err)
	}
	accountTestPut(t, filepath.Join(jobDir, enrollment.ConfigFile), []byte(`{"schema_version":1}`))
	out, err := m.Accounts(protocol.AccountsReq{Action: "cancel", JobID: job.ID})
	if err != nil || out.Job == nil || out.Job.State != "cancelling" {
		t.Fatal("unproved cancellation released its provider slot")
	}
	if _, err := os.Stat(filepath.Join(jobDir, enrollment.CancelFile)); err != nil {
		t.Fatal("missing worker identity prevented the durable cancellation fence")
	}
	if _, err := os.Stat(c.ProfilePath); err != nil {
		t.Fatal("unproved cancellation erased a possible native writer's profile")
	}
	accountTestNative(t, m, "ready")
	_, err = m.Accounts(protocol.AccountsReq{Action: "start", Provider: "codex", Method: "device-code"})
	if !errors.Is(err, protocol.ErrAccountsUnavailable) || len(m.jobs.Jobs) != 1 {
		t.Fatal("a cancelling native generation admitted another provider job")
	}
	// A late ready result cannot reverse cancellation intent once writer death
	// is known, even if no worker identity made it into the original job save.
	accountTestProgress(t, m, job, accountTestDeadProcess(t))
	out, err = m.Accounts(protocol.AccountsReq{Action: "status", JobID: job.ID})
	if err != nil || out.Job == nil || out.Job.State != "cancelled" || len(accountTestRegistry(t, m).Accounts) != 0 {
		t.Fatal("late ready result overcame canonical cancellation")
	}
}

func TestAccountManagerSameBinaryWorkerReadySurvivesReopen(t *testing.T) {
	root := accountTestState(t)
	m := accountTestManager(t, root)
	accountTestNative(t, m, "ready")
	out, err := m.Accounts(protocol.AccountsReq{Action: "start", Provider: "codex", Method: "device-code"})
	if err != nil || out.Job == nil {
		t.Fatalf("offline native worker startup failed: %v", err)
	}
	jobID := out.Job.ID
	ref := m.ref(m.jobs.Jobs[jobID])
	t.Cleanup(func() {
		if ref.Worker.Alive() {
			_ = enrollment.FenceCancel(root, jobID, ref.Generation)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, _ = enrollment.Cancel(ctx, ref)
		}
	})
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		out, err = m.Accounts(protocol.AccountsReq{Action: "status", JobID: jobID})
		if err == nil && out.Job != nil && out.Job.State == "ready" {
			break
		}
		if err == nil && out.Job != nil && out.Job.State == "failed" {
			t.Fatalf("offline native worker failed: %s", out.Job.ErrorCode)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil || out.Job == nil || out.Job.State != "ready" || out.Job.Email != "native@example.test" || out.Job.Plan != "plus" {
		t.Fatal("native worker never reached durable ready review")
	}
	progress, err := enrollment.ReadProgress(root, jobID)
	if err != nil || !progress.WritersStopped || !progress.NativeWritersStopped || progress.Worker.Alive() || progress.Runner.Alive() {
		t.Fatal("canonical ready crossed the native writer-death boundary")
	}
	if len(accountTestRegistry(t, m).Accounts) != 0 {
		t.Fatal("native worker admitted its own credentials")
	}
	for _, path := range []string{filepath.Join(root, "accounts", "enrollment.json"), filepath.Join(root, "accounts", "jobs", jobID, enrollment.ProgressFile)} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{accountFixtureBearer, accountFixtureRefresh, accountFixtureDeviceCode} {
			if strings.Contains(string(raw), secret) {
				t.Fatal("native secret escaped its credential profile or live IPC")
			}
		}
	}
	m.close()
	m = accountTestManager(t, root)
	out, err = m.Accounts(protocol.AccountsReq{Action: "status", JobID: jobID})
	if err != nil || out.Job == nil || out.Job.State != "ready" || out.Job.Email != "native@example.test" || out.Job.Plan != "plus" {
		t.Fatal("native ready review identity was lost after worker and daemon exit")
	}
}

func TestAccountManagerCancelRecoversWorkerStartedBeforeCanonicalPIDSave(t *testing.T) {
	root := accountTestState(t)
	m := accountTestManager(t, root)
	native := accountTestNative(t, m, "pending")
	c, err := m.store.CreateCandidate("codex", accounts.KindNative)
	if err != nil {
		t.Fatal(err)
	}
	job := accountTestJob(t, m, c, "pending", "device-code", "")
	ref, err := enrollment.Start(context.Background(), swarmBin, enrollment.Config{StateRoot: root, JobID: job.ID, CandidateID: c.ID, CandidateProfileGeneration: c.ProfileGeneration, Generation: job.Generation, Provider: job.Provider, Method: job.Method, NativePath: native, NativeVersion: "0.160.0", Deadline: time.Now().Add(15 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = enrollment.FenceCancel(root, ref.JobID, ref.Generation)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = enrollment.Cancel(ctx, ref)
	})
	deadline := time.Now().Add(8 * time.Second)
	var live enrollment.LiveStatus
	for time.Now().Before(deadline) {
		live, err = enrollment.Status(context.Background(), ref)
		if err == nil && live.Device != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil || live.Device == nil || m.jobs.Jobs[job.ID].Worker.PID != 0 {
		t.Fatal("fixture did not reach the Start-before-canonical-PID-save crash window")
	}
	m.close()
	m = accountTestManager(t, root)
	var out protocol.AccountsReply
	for deadline = time.Now().Add(8 * time.Second); time.Now().Before(deadline); {
		out, err = m.Accounts(protocol.AccountsReq{Action: "cancel", JobID: job.ID})
		if err == nil && out.Job != nil && out.Job.State == "cancelled" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil || out.Job == nil || out.Job.State != "cancelled" || m.jobs.Jobs[job.ID].Worker != ref.Worker || ref.Worker.Alive() {
		t.Fatal("daemon cancellation lost the already-started worker generation")
	}
	if len(accountTestRegistry(t, m).Accounts) != 0 {
		t.Fatal("late native success committed after explicit cancellation")
	}
	if _, err := os.Stat(c.ProfilePath); !os.IsNotExist(err) {
		t.Fatal("cancelled candidate was retained after writer death")
	}
}

func TestAccountManagerRejectsStaleWorkerProgressAndLiveWriterProof(t *testing.T) {
	for _, test := range []string{"different-worker", "missing-worker", "live-worker", "live-runner", "live-child", "no-native-stop-proof"} {
		t.Run(test, func(t *testing.T) {
			m := accountTestManager(t, accountTestState(t))
			c := accountTestCandidate(t, m, "codex", test)
			job := accountTestJob(t, m, c, "authenticating", "device-code", "")
			cmd := exec.Command("/bin/sleep", "30")
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
			start, err := procstart.StartTime(cmd.Process.Pid)
			if err != nil {
				t.Fatal(err)
			}
			job.Worker = enrollment.ProcessIdentity{PID: cmd.Process.Pid, StartTime: start}
			liveProcess := job.Worker
			if test != "different-worker" && test != "live-worker" {
				job.Worker = accountTestDeadProcess(t)
			}
			if err := m.saveJob(job); err != nil {
				t.Fatal(err)
			}
			progressWorker := job.Worker
			if test == "different-worker" {
				progressWorker = accountTestDeadProcess(t)
			}
			p := accountTestProgress(t, m, job, progressWorker)
			switch test {
			case "missing-worker":
				p.Worker = enrollment.ProcessIdentity{}
			case "live-runner":
				p.Runner = liveProcess
			case "live-child":
				p.Children = []enrollment.ProcessIdentity{liveProcess}
			case "no-native-stop-proof":
				p.NativeWritersStopped = false
			}
			raw, _ := json.Marshal(p)
			accountTestPut(t, filepath.Join(m.stateRoot, "accounts", "jobs", job.ID, enrollment.ProgressFile), raw)
			_, err = m.Accounts(protocol.AccountsReq{Action: "status", JobID: job.ID})
			if !errors.Is(err, protocol.ErrAccountsUnavailable) || m.jobs.Jobs[job.ID].State != "authenticating" {
				t.Fatal("stale progress or live writer admitted a ready candidate")
			}
		})
	}
}

func TestAccountManagerPrivateJobInventoryRejectsSpecialPaths(t *testing.T) {
	for _, special := range []string{"symlink", "fifo", "public-file", "jobs-symlink", "jobs-fifo"} {
		t.Run(special, func(t *testing.T) {
			root := accountTestState(t)
			m := accountTestManager(t, root)
			m.close()
			path := filepath.Join(root, "accounts", "enrollment.json")
			switch special {
			case "symlink":
				target := filepath.Join(root, "outside.json")
				accountTestPut(t, target, []byte(`{"schema_version":1,"jobs":{}}`))
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := syscall.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
			case "public-file":
				accountTestPut(t, path, []byte(`{"schema_version":1,"jobs":{}}`))
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
			default:
				path = filepath.Join(root, "accounts", "jobs")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if special == "jobs-fifo" {
					if err := syscall.Mkfifo(path, 0o600); err != nil {
						t.Fatal(err)
					}
				} else {
					target := filepath.Join(root, "outside-jobs")
					if err := os.Mkdir(target, 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(target, path); err != nil {
						t.Fatal(err)
					}
				}
			}
			started := time.Now()
			reopened := openAccountManager(root, "", nil)
			t.Cleanup(reopened.close)
			if !errors.Is(reopened.unavailable, protocol.ErrAccountsUnavailable) || time.Since(started) > time.Second {
				t.Fatal("unsafe canonical job inventory was accepted or blocked on a special file")
			}
		})
	}
}

func TestAccountManagerCancelBeforeStartSurvivesDaemonRestart(t *testing.T) {
	root := accountTestState(t)
	m := accountTestManager(t, root)
	c, err := m.store.CreateCandidate("codex", accounts.KindNative)
	if err != nil {
		t.Fatal(err)
	}
	job := accountTestJob(t, m, c, "pending", "device-code", "")
	m.close()
	reopened := accountTestManager(t, root)
	out, err := reopened.Accounts(protocol.AccountsReq{Action: "cancel", JobID: job.ID})
	if err != nil || out.Job == nil || out.Job.State != "cancelled" {
		t.Fatalf("never-started cancellation stayed blocked: %v", err)
	}
	if _, err = os.Stat(c.ProfilePath); !os.IsNotExist(err) {
		t.Fatal("never-started candidate was retained")
	}
	accountTestNative(t, reopened, "ready")
	out, err = reopened.Accounts(protocol.AccountsReq{Action: "start", Provider: "codex", Method: "device-code"})
	if err != nil || out.Job == nil || out.Job.State != "authenticating" {
		t.Fatal("never-started cancellation retained provider slot")
	}
	ref := reopened.ref(reopened.jobs.Jobs[out.Job.ID])
	defer func() {
		_ = enrollment.FenceCancel(root, ref.JobID, ref.Generation)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = enrollment.Cancel(ctx, ref)
	}()
}

func TestAccountManagerMissingNativeInstallationCreatesNoCandidate(t *testing.T) {
	m := accountTestManager(t, accountTestState(t))
	m.native["codex"] = &persist.CLIIdentity{Path: filepath.Join(m.stateRoot, "missing-native"), Version: "0.160.0"}
	before, err := os.ReadDir(filepath.Join(m.stateRoot, "accounts", "profiles"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Accounts(protocol.AccountsReq{Action: "start", Provider: "codex", Method: "device-code"})
	after, readErr := os.ReadDir(filepath.Join(m.stateRoot, "accounts", "profiles"))
	if !errors.Is(err, protocol.ErrAccountsUnavailable) || readErr != nil || len(before) != len(after) || len(m.jobs.Jobs) != 0 {
		t.Fatal("missing native installation left pending credentials or intent")
	}
}

func TestAccountManagerUnlaunchableNativeMethodCreatesNoCandidate(t *testing.T) {
	for _, mode := range []os.FileMode{0o775, 0o777, 0o644} {
		t.Run(fmt.Sprintf("%o", mode), func(t *testing.T) {
			m := accountTestManager(t, accountTestState(t))
			native := filepath.Join(m.stateRoot, "native-codex")
			accountTestPut(t, native, []byte("#!/bin/sh\nexit 1\n"))
			if err := os.Chmod(native, mode); err != nil {
				t.Fatal(err)
			}
			// Normal npm installation aliases must resolve to the same file
			// whose executable permissions the worker validates.
			alias := filepath.Join(m.stateRoot, "codex")
			if err := os.Symlink(native, alias); err != nil {
				t.Fatal(err)
			}
			m.native["codex"] = &persist.CLIIdentity{Path: alias, Version: "0.160.0"}
			method := m.methods()["codex"][0]
			if method.Available || method.Reason == "" {
				t.Error("unlaunchable Codex sign-in was advertised as available")
			}
			_, err := m.Accounts(protocol.AccountsReq{Action: "start", Provider: "codex", Method: "device-code"})
			profiles, readErr := os.ReadDir(filepath.Join(m.stateRoot, "accounts", "profiles"))
			jobs, jobsErr := os.ReadDir(filepath.Join(m.stateRoot, "accounts", "jobs"))
			if !errors.Is(err, protocol.ErrAccountsUnavailable) || readErr != nil || jobsErr != nil || len(profiles) != 0 || len(jobs) != 0 || len(m.jobs.Jobs) != 0 {
				t.Error("unlaunchable Codex created a failed enrollment or candidate")
			}
		})
	}
}

func TestAccountManagerNativeMethodTracksPermissionRepair(t *testing.T) {
	m := accountTestManager(t, accountTestState(t))
	native := filepath.Join(m.stateRoot, "native-codex")
	accountTestPut(t, native, []byte("#!/bin/sh\nexit 1\n"))
	m.native["codex"] = &persist.CLIIdentity{Path: native, Version: "0.160.0"}
	for _, mode := range []os.FileMode{0o775, 0o755, 0o775} {
		if err := os.Chmod(native, mode); err != nil {
			t.Fatal(err)
		}
		if got := m.methods()["codex"][0].Available; got != (mode == 0o755) {
			t.Errorf("mode %o: available=%v", mode, got)
		}
	}
}

func TestAccountManagerPrunesOnlyCompletedEnrollmentHistory(t *testing.T) {
	root := accountTestState(t)
	m := accountTestManager(t, root)
	c := accountTestCandidate(t, m, "codex", "history-retention")
	base := accountTestJob(t, m, c, "cancelled", "import-native", "")
	// A full historical authority must not permanently disable enrollment.
	for i := 1; i <= 255; i++ {
		job := base
		job.ID = fmt.Sprintf("%032x", i)
		job.Candidate.ID = job.ID
		job.Candidate.ProfileGeneration = job.ID
		job.Deadline = base.Deadline.Add(-time.Duration(i) * time.Second)
		m.jobs.Jobs[job.ID] = job
	}
	uncertain := m.jobs.Jobs[fmt.Sprintf("%032x", 1)]
	uncertain.State = "cancelling"
	uncertain.Provider = "claude"
	uncertain.Candidate.Provider = "claude"
	m.jobs.Jobs[uncertain.ID] = uncertain
	if err := m.pruneTerminalJobs(); err != nil {
		t.Fatal(err)
	}
	if len(m.jobs.Jobs) != 128 || m.jobs.Jobs[uncertain.ID].State != "cancelling" {
		t.Fatal("history pruning discarded an unresolved writer fence")
	}
	m.close()
	reopened := accountTestManager(t, root)
	if reopened.unavailable != nil || len(reopened.jobs.Jobs) != 128 || reopened.jobs.Jobs[uncertain.ID].State != "cancelling" {
		t.Fatal("bounded enrollment history was not durable")
	}
}

func TestAccountManagerCachedImportsUnavailableBeforeReadingCredentials(t *testing.T) {
	m := accountTestManager(t, accountTestState(t))
	for _, provider := range []string{"claude", "codex"} {
		for _, method := range m.methods()[provider] {
			if method.ID == "import-native" && (method.Available || method.Reason == "") {
				t.Fatal("uncoupled cached identity import was advertised as usable")
			}
		}
		before, _ := os.ReadDir(filepath.Join(m.stateRoot, "accounts", "profiles"))
		_, err := m.Accounts(protocol.AccountsReq{Action: "import", Provider: provider, Method: "import-native", SourcePath: "/a/path/that/must/not/be/read"})
		after, _ := os.ReadDir(filepath.Join(m.stateRoot, "accounts", "profiles"))
		if !errors.Is(err, protocol.ErrAccountsUnavailable) || len(m.jobs.Jobs) != 0 || len(before) != len(after) {
			t.Fatal("unverified import created a candidate or canonical intent")
		}
	}
}

func TestAccountManagerCancelAfterJobDirectoryBeforeConfig(t *testing.T) {
	root := accountTestState(t)
	m := accountTestManager(t, root)
	c, err := m.store.CreateCandidate("codex", accounts.KindNative)
	if err != nil {
		t.Fatal(err)
	}
	job := accountTestJob(t, m, c, "pending", "device-code", "")
	if err := os.Mkdir(filepath.Join(root, "accounts", "jobs", job.ID), 0700); err != nil {
		t.Fatal(err)
	}
	m.close()
	reopened := accountTestManager(t, root)
	out, err := reopened.Accounts(protocol.AccountsReq{Action: "cancel", JobID: job.ID})
	if err != nil || out.Job == nil || out.Job.State != "cancelled" {
		t.Fatal("pre-config crash permanently blocked cancellation")
	}
	if _, err := os.Stat(c.ProfilePath); !os.IsNotExist(err) {
		t.Fatal("provably unstarted profile retained credentials")
	}
}
