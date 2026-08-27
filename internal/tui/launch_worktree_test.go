package tui

import "testing"

// ADR-010 Amendment 1 / A5 Phase 0, bead agents-tracker-v4ir — submitLaunch must
// carry the worktree toggle into the fired LaunchReq. Before the fix,
// launchModel.worktree is collected by the form but never copied onto req.Worktree
// (nor injected into Options), so the daemon's launchOptions (protocol/server.go,
// which reads req.Worktree to add OptionWorktree) never sees the toggle regardless
// of what the user selected in the form.

// TestLaunch_SubmitCarriesWorktreeToggle turns the worktree checkbox ON and
// submits: the LaunchReq the client fires must carry Worktree=true.
func TestLaunch_SubmitCarriesWorktreeToggle(t *testing.T) {
	f := newFakeClient()
	m := openLaunch(t, f)

	// Field order (detectMixed, one option "Model"): directory, name, agent, Model,
	// prompt, worktree. Five Down presses from directory land on worktree.
	m = send(m, keyDown) // directory -> name
	m = send(m, keyDown) // name -> agent
	m = send(m, keyDown) // agent -> Model
	m = send(m, keyDown) // Model -> prompt
	m = send(m, keyDown) // prompt -> worktree
	if !launchOf(m).isWorktree() {
		t.Fatalf("expected focus on the worktree field after 5 tabs, got focus %d", launchOf(m).focus)
	}
	m = send(m, keyRune(' ')) // toggle worktree on
	if !launchOf(m).worktree {
		t.Fatalf("space on the worktree field should toggle it on")
	}

	_, cmd := m.Update(keyEnter)
	execCmd(cmd)

	reqs := f.launchReqs()
	if len(reqs) != 1 {
		t.Fatalf("expected exactly one launch, got %d", len(reqs))
	}
	if !reqs[0].Worktree {
		t.Fatalf("LaunchReq.Worktree = false, want true: the toggle was on but submitLaunch dropped it")
	}
}

// TestLaunch_SubmitWorktreeOffByDefault is the companion case: leaving the
// checkbox untouched (off) must submit Worktree=false, so a fix cannot pass by
// hardcoding true or otherwise decoupling the field from form state.
func TestLaunch_SubmitWorktreeOffByDefault(t *testing.T) {
	f := newFakeClient()
	m := openLaunch(t, f)

	_, cmd := m.Update(keyEnter) // submit with the worktree toggle left off
	execCmd(cmd)

	reqs := f.launchReqs()
	if len(reqs) != 1 {
		t.Fatalf("expected exactly one launch, got %d", len(reqs))
	}
	if reqs[0].Worktree {
		t.Fatalf("LaunchReq.Worktree = true, want false: the toggle was never turned on")
	}
}
