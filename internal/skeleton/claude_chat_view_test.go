package skeleton

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/procstart"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/shimwire"
	tea "github.com/Nathandela/swarm/internal/thirdparty/bubbletea"
	"github.com/Nathandela/swarm/internal/tui"
)

func driveClaudeView(t *testing.T, model tea.Model, cmd tea.Cmd, want func(string) bool) tea.Model {
	t.Helper()
	queue := []tea.Cmd{cmd}
	deadline := time.Now().Add(5 * time.Second)
	for !want(model.View().Content) && time.Now().Before(deadline) {
		if len(queue) == 0 {
			break
		}
		current := queue[0]
		queue = queue[1:]
		if current == nil {
			continue
		}
		message := current()
		if batch, ok := message.(tea.BatchMsg); ok {
			queue = append(queue, batch...)
			continue
		}
		var next tea.Cmd
		model, next = model.Update(message)
		if next != nil {
			queue = append(queue, next)
		}
	}
	if !want(model.View().Content) {
		text := model.View().Content
		if len(text) > 4096 {
			text = text[:4096]
		}
		t.Fatalf("connected chat view did not reach expected state: %q", text)
	}
	return model
}

func TestClaudeChatViewConnectedOneTurnAndOwnedStop(t *testing.T) {
	d := assemble(t)
	meta := launchClaudeMessageFixture(t, d)
	board := dialClient(t, d, "subscribe", protocol.CapJournal, protocol.CapSemanticSend)
	attaches := 0
	model := tui.New(board, nil, tui.WithClaudeChatDialer(func() (tui.Client, error) {
		return protocol.Dial(d.SocketPath(), []string{protocol.CapJournal, protocol.CapSemanticSend})
	}), tui.WithAttachRunner(func(protocol.SessionView, bool, bool) error { attaches++; return nil }))
	defer func() { _ = model.(interface{ Close() error }).Close() }()
	model, _ = model.Update(tea.WindowSizeMsg{Width: 120, Height: 25})
	var cmd tea.Cmd
	model, cmd = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = driveClaudeView(t, model, cmd, func(s string) bool { return strings.Contains(s, "Dev fixture;") })
	const text = "view-owned Unicode 🐝\nsecond line"
	model, _ = model.Update(tea.PasteMsg{Content: text})
	model, cmd = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = driveClaudeView(t, model, cmd, func(s string) bool {
		return strings.Contains(s, "completed") && strings.Contains(s, "fixture reply:") && strings.Contains(s, "second line")
	})
	if attaches != 0 {
		t.Fatal("chat fell back to raw attach")
	}
	spool, err := shim.ReadHookSpoolFileResponse(filepath.Join(d.stateDir, meta.ID, shim.HookSpoolFile), 0)
	if err != nil || len(spool.Records) != 4 {
		t.Fatal("message did not retain exactly four primary frames", err)
	}
	var init struct {
		Primary shimwire.ClaudePrimaryIdentity `json:"primary"`
	}
	if json.Unmarshal(spool.Records[0].Body, &init) != nil {
		t.Fatal("missing primary target")
	}
	operations := d.core.SemanticRequestOperations(meta.ID)
	if len(operations) != 1 {
		t.Fatal("view did not retain exactly one owner operation", operations)
	}
	receipt, err := board.SemanticOperation(protocol.NamespacedID(d.api.endpointID, meta.ID), operations[0])
	if err != nil || receipt.State != "completed" || receipt.OutcomeSource != "claude_primary_fixture" {
		t.Fatal("view result did not match durable receipt", receipt, err)
	}
	execution, err := d.executionStore.LoadExecution(meta.ID)
	if err != nil || execution.OwnerRevision != 1 || !execution.UnknownActors || execution.Incomplete != "dev_fake_chat_subset" {
		t.Fatal("view invented complete workflow or lost durable revision", execution, err)
	}
	model, cmd = model.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	model = driveClaudeView(t, model, cmd, func(s string) bool { return strings.Contains(s, "Stop requested") })
	gone := func(pid int, birth int64) bool {
		current, e := procstart.StartTime(pid)
		if e == nil && current > 0 && current != birth {
			return true
		}
		return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && time.Now().Before(meta.CreatedAt.Add(11*time.Second)) {
		if gone(init.Primary.PrimaryPID, init.Primary.PrimaryStartTime) && gone(meta.ShimPID, meta.ShimStartTime) {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("view Stop did not end exact child and shim before finite fixture autoexit")
}
