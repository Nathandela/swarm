// Command hermes_native_probe qualifies the public Swarm boundary against an
// already running, isolated native owner and a loopback-only scripted provider.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Nathandela/swarm/internal/hermesinput"
	"github.com/Nathandela/swarm/internal/processcontain"
	"github.com/Nathandela/swarm/internal/procstart"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/skeleton"
	"github.com/Nathandela/swarm/internal/status"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	root := flag.String("root", "", "isolated fixture root")
	swarm := flag.String("swarm", "", "private Swarm binary")
	statsURL := flag.String("stats", "", "loopback provider stats endpoint")
	flag.Parse()
	if *root == "" || *swarm == "" || *statsURL == "" {
		return errors.New("root, swarm and stats are required")
	}
	// Fail before any native execution if the outer harness forgot its OS guard.
	outside := filepath.Join(filepath.Dir(*root), "hermes-denied-"+strconv.Itoa(os.Getpid()))
	if file, err := os.OpenFile(outside, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600); err == nil {
		_ = file.Close()
		_ = os.Remove(outside)
		return errors.New("fixture is not OS write isolated")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	state := filepath.Join(*root, "sw")
	cfg := skeleton.Config{StateDir: state, SocketPath: filepath.Join(state, "d"), LockPath: filepath.Join(state, "l"), LogPath: filepath.Join(state, "log"), ShimBinary: *swarm, MaxSessions: 8, PollInterval: 500 * time.Millisecond, NativeInputEnabled: func() bool { return true }}
	serve, err := skeleton.Serve(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = serve.Close() }()
	dial := func() (*protocol.Client, error) {
		return protocol.DialContext(ctx, cfg.SocketPath, []string{protocol.CapSemanticSend, protocol.CapMessageOrigin})
	}
	client, err := dial()
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	var sessions []string
	defer func() {
		for _, id := range sessions {
			_ = client.Kill(id)
		}
	}()
	launch := func(name string) (string, hermesinput.Descriptor, error) {
		cwd := filepath.Join(*root, "workspace-"+name)
		if err := os.Mkdir(cwd, 0700); err != nil {
			return "", hermesinput.Descriptor{}, err
		}
		id, _, err := client.Launch(protocol.LaunchReq{Agent: "hermes", Name: "native-λ-" + name, Cwd: cwd, Env: os.Environ(), Options: map[string]string{"toolsets": "terminal,delegation"}, Cols: 160, Rows: 45})
		if err != nil {
			return id, hermesinput.Descriptor{}, err
		}
		sessions = append(sessions, id)
		_, local, ok := protocol.ParseID(id)
		if !ok {
			return id, hermesinput.Descriptor{}, errors.New("launch returned unbound session ID")
		}
		desc, err := hermesinput.ReadDescriptor(filepath.Join(state, local))
		return id, desc, err
	}
	a, ad, err := launch("a")
	if err != nil {
		return fmt.Errorf("launch A: %w", err)
	}
	b, bd, err := launch("b")
	if err != nil {
		return fmt.Errorf("launch B: %w", err)
	}
	if ad.Binding.OwnerPID != bd.Binding.OwnerPID || ad.Binding.OwnerStartTime != bd.Binding.OwnerStartTime || ad.Binding.InstanceID != bd.Binding.InstanceID || ad.Binding.SessionID == bd.Binding.SessionID {
		return errors.New("workers did not bind independent sessions to the same native owner")
	}
	view := func(id string) (protocol.SessionView, error) {
		rows, err := client.List()
		if err != nil {
			return protocol.SessionView{}, err
		}
		for _, row := range rows {
			if row.ID == id {
				return row, nil
			}
		}
		return protocol.SessionView{}, errors.New("worker missing from roster")
	}
	var receipts []protocol.SemanticMessageReceipt
	send := func(id, text string) (string, error) {
		row, err := view(id)
		if err != nil {
			return "", err
		}
		if row.Capabilities == nil {
			_, local, _ := protocol.ParseID(id)
			raw, e := os.ReadFile(filepath.Join(state, local, "capabilities.json"))
			if e != nil {
				return "", e
			}
			var capability protocol.SessionCapabilities
			if e = json.Unmarshal(raw, &capability); e != nil {
				return "", e
			}
			row.Capabilities = &capability
		}
		op, err := protocol.NewSemanticOperationID()
		if err != nil {
			return "", err
		}
		req := protocol.ComposerSendReq{Session: id, SessionInstance: row.Capabilities.SessionInstance, Text: text, Origin: &protocol.MessageOrigin{Kind: "external", InstallationID: "fixture", RequestID: op, ActorID: "synthetic-discord-user"}}
		if err = client.SemanticSend(op, req); err != nil {
			return op, err
		}
		if err = client.SemanticSend(op, req); err != nil {
			return op, err
		}
		r, err := client.SemanticOperation(id, op)
		if err != nil {
			return op, err
		}
		if r.State != "submitted" {
			return op, fmt.Errorf("native receipt is not submitted: %+v", r)
		}
		receipts = append(receipts, r)
		return op, nil
	}
	wait := func(label string, predicate func() bool) error {
		fmt.Fprintln(os.Stderr, "waiting:", label)
		deadline := time.Now().Add(35 * time.Second)
		for !predicate() {
			if time.Now().After(deadline) {
				return fmt.Errorf("deadline: %s", label)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
		return nil
	}
	readPID := func(name string) (int, int64, error) {
		raw, err := os.ReadFile(filepath.Join(*root, name))
		if err != nil {
			return 0, 0, err
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil {
			return 0, 0, err
		}
		start, err := procstart.StartTime(pid)
		return pid, start, err
	}
	if _, err = send(a, "SWARM_BACKGROUND_A"); err != nil {
		return err
	}
	if err = wait("A background process", func() bool { _, _, e := readPID("a.pid"); return e == nil }); err != nil {
		return err
	}
	if _, err = send(b, "SWARM_BACKGROUND_B"); err != nil {
		return err
	}
	if err = wait("B background process", func() bool { _, _, e := readPID("b.pid"); return e == nil }); err != nil {
		return err
	}
	apid, astart, err := readPID("a.pid")
	if err != nil {
		return err
	}
	bpid, bstart, err := readPID("b.pid")
	if err != nil {
		return err
	}
	if _, err = send(a, "SWARM_DELEGATE_A"); err != nil {
		return err
	}
	if err = wait("A native async child", func() bool { _, _, e := readPID("child-a.pid"); return e == nil }); err != nil {
		return err
	}
	childPID, childStart, err := readPID("child-a.pid")
	if err != nil {
		return err
	}
	if _, err = send(a, "SWARM_PERMISSION_A"); err != nil {
		return err
	}
	var permission string
	if err = wait("A native approval", func() bool {
		activity, e := hermesinput.Inspect(ctx, ad.Binding)
		if e != nil {
			return false
		}
		for _, prompt := range activity.Snapshot.Prompts {
			if prompt.Kind == "approval" {
				permission = prompt.ID
				return true
			}
		}
		return false
	}); err != nil {
		return err
	}
	if err = wait("native TUI permission rendering", func() bool {
		snap, e := client.TerminalSnapshot(a)
		return e == nil && strings.Contains(strings.Join(snap.Lines, "\n"), "rm -f")
	}); err != nil {
		return err
	}
	queued, err := send(a, "/approve all")
	if err != nil {
		return err
	}
	activity, err := hermesinput.Inspect(ctx, ad.Binding)
	if err != nil {
		return err
	}
	found, queuedFound := false, false
	for _, prompt := range activity.Snapshot.Prompts {
		found = found || prompt.ID == permission
	}
	for _, pending := range activity.Snapshot.Pending {
		queuedFound = queuedFound || pending.InputID == queued && pending.Status == "queued"
	}
	if !found || !queuedFound {
		return errors.New("external message changed native permission or missed its native queue")
	}
	if _, err = os.Stat(filepath.Join(*root, "protected")); err != nil {
		return errors.New("external message answered native tool approval")
	}
	stats := func() (int, error) {
		response, err := http.Get(*statsURL)
		if err != nil {
			return 0, err
		}
		defer func() { _ = response.Body.Close() }()
		var value struct {
			MainCalls int `json:"main_calls"`
		}
		err = json.NewDecoder(response.Body).Decode(&value)
		return value.MainCalls, err
	}
	before, err := stats()
	if err != nil {
		return err
	}
	if err = client.Close(); err != nil {
		return err
	}
	if err = serve.Close(); err != nil {
		return err
	}
	serve, err = skeleton.Serve(cfg)
	if err != nil {
		return err
	}
	client, err = dial()
	if err != nil {
		return err
	}
	time.Sleep(time.Second)
	after, err := stats()
	if err != nil || before != after {
		return fmt.Errorf("daemon restart created a model turn: %d -> %d (%v)", before, after, err)
	}
	if current, e := procstart.StartTime(ad.Binding.OwnerPID); e != nil || current != ad.Binding.OwnerStartTime {
		return errors.New("daemon restart replaced native owner")
	}
	if err = client.Kill(a); err == nil {
		return errors.New("native stop claimed joined completion")
	}
	alive := func(pid int, start int64) bool { return (processcontain.Identity{PID: pid, StartTime: start}).Alive() }
	if err = wait("A background and child process termination", func() bool { return !alive(apid, astart) && !alive(childPID, childStart) }); err != nil {
		return err
	}
	if !alive(bpid, bstart) || !alive(bd.Binding.OwnerPID, bd.Binding.OwnerStartTime) {
		return errors.New("a stop reached B or the shared native owner")
	}
	var stoppedRow protocol.SessionView
	var stoppedRowErr error
	if err = wait("A unknown-work status", func() bool {
		row, e := view(a)
		stoppedRow, stoppedRowErr = row, e
		return e == nil && row.Status.NativeWorkUnknown && row.Group == status.GroupNeedsInput
	}); err != nil {
		return fmt.Errorf("%w; row=%+v error=%v", err, stoppedRow, stoppedRowErr)
	}
	if err = client.Delete(a); err == nil {
		return errors.New("uncertain native workspace was deleted")
	}
	if _, err = os.Stat(ad.Binding.Directory); err != nil {
		return errors.New("a retained workspace disappeared")
	}
	if _, err = send(b, "SWARM_B_STILL_USABLE"); err != nil {
		return fmt.Errorf("b became unusable: %w", err)
	}
	result := map[string]any{"shared_owner_pid": ad.Binding.OwnerPID, "shared_owner_start": ad.Binding.OwnerStartTime, "shared_owner_instance": ad.Binding.InstanceID, "session_a": ad.Binding.SessionID, "session_b": bd.Binding.SessionID, "permission_id": permission, "literal_approval_text_queued": true, "native_tui_permission_rendered": true, "restart_no_model_turn": true, "a_background_stopped": true, "a_async_child_stopped": true, "b_background_survived": true, "b_still_usable": true, "workspace_retained_unknown": true, "receipts": receipts}
	raw, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(raw))
	return os.WriteFile(filepath.Join(*root, "result.json"), raw, 0600)
}
