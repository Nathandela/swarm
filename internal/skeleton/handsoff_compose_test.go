package skeleton

// Composition of a hands-off handoff (ADR-010 Amendment 4). These tests are the
// security half of the feature: EVERY refusal below must also LAUNCH NOTHING, because
// E7 names a context-free agent loose in the owner's checkout as the worst outcome
// available -- worse than no handoff at all, since the owner would believe the work
// was carried over. So each refusal case asserts the roster did not grow, not merely
// that an error came back.

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

// handsOffSpec is the launch a hands-off form submits: the target CLI, the chosen
// model, a cwd, and the NAMESPACED source id. Everything else is the daemon's to
// compose (E5), which is what these tests check.
func handsOffSpec(endpoint, sourceID, cwd string) daemon.LaunchSpec {
	return daemon.LaunchSpec{
		AgentType: "claude",
		Cwd:       cwd,
		// A deterministic missing PATH would stop composition at argv resolution; these
		// tests must fail EARLIER than that, so a refusal here is never mistaken for the
		// launch path simply running out of binaries.
		ClientEnv: []string{"PATH=/definitely/no-provider-binaries-here"},
		Options: map[string]string{
			protocol.OptionHandoffFrom: protocol.NamespacedID(endpoint, sourceID),
			"model":                    "opus",
		},
	}
}

// handsOffSource is a claude session that is STILL RUNNING. That is deliberate and is
// the whole point of the feature: a rate-limited session is byte-identical on the wire
// to a healthy idle one (E2), so the primary case hands off from a running source.
// validateResumeSource refuses exactly this; hands-off must not.
func handsOffSource(local, cwd, convID string) persist.Meta {
	return persist.Meta{
		ID:             local,
		AgentType:      "claude",
		ConversationID: convID,
		Cwd:            cwd,
		CreatedAt:      legacyCreatedAt,
		LastActivity:   legacyCreatedAt,
		Status:         status.Status{Process: status.ProcessRunning},
	}
}

// requireRefusedAndLaunchedNothing is the assertion that matters: a named refusal AND
// an unchanged roster.
func requireRefusedAndLaunchedNothing(t *testing.T, rig *resumeAPIRig, before int, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("hands-off handoff unexpectedly succeeded; want refusal containing %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("refusal = %q, want it to contain %q", err.Error(), want)
	}
	if got := len(rig.core.List()); got != before {
		t.Fatalf("roster grew from %d to %d on a refused hands-off handoff; no refusal may degrade to a bare launch", before, got)
	}
}

// TestHandsOff_RefusesUnusableConversationIDsAndLaunchesNothing is the traversal test,
// and it is a SECURITY test rather than an edge case. "./cmd/swarm/" is the literal junk
// two of the owner's real sessions latched off the rendered grid; filepath.Join CLEANS,
// so a stored id of "../../../../etc/passwd" would resolve OUTSIDE the projects root if
// any code path ever joined it. None may. The empty id is the fifth-of-seven case: no id
// was ever captured at all.
func TestHandsOff_RefusesUnusableConversationIDsAndLaunchesNothing(t *testing.T) {
	for _, tc := range []struct{ name, convID string }{
		{"the junk two real sessions latched", "./cmd/swarm/"},
		{"traversal to an absolute target", "../../../../etc/passwd"},
		{"bare parent", ".."},
		{"never captured at all", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			convID := tc.convID
			home := t.TempDir()
			var mu sync.Mutex
			var opened []string
			resolver := newFilesystemResumeHistoryResolver(home, generousResumeHistoryLimits())
			resolver.beforeOpen = func(p string) {
				mu.Lock()
				opened = append(opened, p)
				mu.Unlock()
			}
			rig := newResumeAPIRigWithProcess(t, "claude", convID, status.ProcessRunning, resolver)
			// A REAL provider tree, so the recovery scan actually walks and opens files and
			// the path assertions below have something to bite on. Its one transcript is
			// stamped far outside the resume window, so recovery legitimately finds no match
			// and the handoff is refused -- which is the point: the refusal must arrive
			// without any unusable id having steered a single read.
			writeClaudeHistory(t, home, legacyClaudeOtherID, rig.meta(t).Cwd, legacyCreatedAt.Add(72*time.Hour), "", "")
			before := len(rig.core.List())

			_, err := rig.api.Launch(handsOffSpec(testEndpoint, rig.sourceID, filepath.Join(rig.stateDir, "new-work")))

			requireRefusedAndLaunchedNothing(t, rig, before, err, "handoff:")
			mu.Lock()
			defer mu.Unlock()
			if len(opened) == 0 {
				t.Fatal("the resolver opened nothing, so this test proves nothing about where it opens")
			}
			// The load-bearing half: whatever the refusal SAID, no unusable id may ever have
			// steered a filesystem read. Every path the daemon opened must lie under the
			// anchored provider root, and none may carry the junk.
			root := filepath.Join(home, ".claude")
			needle := strings.Trim(convID, "./")
			for _, p := range opened {
				if p != root && !strings.HasPrefix(p, root+string(filepath.Separator)) {
					t.Fatalf("opened %q, which is outside the anchored provider root %q", p, root)
				}
				if needle != "" && strings.Contains(p, needle) {
					t.Fatalf("opened %q, which was steered by the unusable conversation id %q", p, convID)
				}
			}
		})
	}
}

// TestHandsOff_RefusesAForeignEndpointAndLaunchesNothing: a source id namespaced to
// another daemon names a session this daemon cannot see and must not guess at.
func TestHandsOff_RefusesAForeignEndpointAndLaunchesNothing(t *testing.T) {
	home := t.TempDir()
	rig := newResumeAPIRigWithProcess(t, "claude", legacyClaudeID, status.ProcessRunning,
		newFilesystemResumeHistoryResolver(home, generousResumeHistoryLimits()))
	before := len(rig.core.List())

	_, err := rig.api.Launch(handsOffSpec("ep-someoneelse", rig.sourceID, filepath.Join(rig.stateDir, "new-work")))

	requireRefusedAndLaunchedNothing(t, rig, before, err, "belongs to another daemon endpoint")
}

// TestHandsOff_RefusesAMissingSourceAndLaunchesNothing.
func TestHandsOff_RefusesAMissingSourceAndLaunchesNothing(t *testing.T) {
	home := t.TempDir()
	rig := newResumeAPIRigWithProcess(t, "claude", legacyClaudeID, status.ProcessRunning,
		newFilesystemResumeHistoryResolver(home, generousResumeHistoryLimits()))
	before := len(rig.core.List())

	_, err := rig.api.Launch(handsOffSpec(testEndpoint, "no-such-session", filepath.Join(rig.stateDir, "new-work")))

	requireRefusedAndLaunchedNothing(t, rig, before, err, "not found")
}

// TestHandsOff_RefusesBeingCombinedWithAResume guards the one pairing that would
// degrade silently rather than loudly: composeLaunchSpec's resume branch replaces argv
// wholesale, so a spec that composed a hands-off prompt and then resumed would drop the
// prompt and launch a successor with no idea what it was continuing. The protocol layer
// refuses the pairing too; this is the layer where the damage would happen.
func TestHandsOff_RefusesBeingCombinedWithAResume(t *testing.T) {
	for _, key := range []string{protocol.OptionResumeFrom, protocol.OptionResumeConversationID} {
		t.Run(key, func(t *testing.T) {
			home := t.TempDir()
			rig := newResumeAPIRigWithProcess(t, "claude", legacyClaudeID, status.ProcessRunning,
				newFilesystemResumeHistoryResolver(home, generousResumeHistoryLimits()))
			writeClaudeHistory(t, home, legacyClaudeID, rig.meta(t).Cwd, legacyCreatedAt.Add(time.Second), "", "")
			spec := handsOffSpec(testEndpoint, rig.sourceID, filepath.Join(rig.stateDir, "new-work"))
			spec.Options[key] = protocol.NamespacedID(testEndpoint, rig.sourceID)
			if key == protocol.OptionResumeConversationID {
				spec.Options[key] = legacyClaudeID
			}
			before := len(rig.core.List())

			_, err := rig.api.Launch(spec)

			requireRefusedAndLaunchedNothing(t, rig, before, err, "cannot be combined with")
		})
	}
}

// TestHandsOff_RefusesAnUnsupportedSourceAgentByName pins E7: claude sources only in
// this sweep, and the other three are refused BY NAME rather than served a launch with
// a transcript path this daemon cannot compute.
func TestHandsOff_RefusesAnUnsupportedSourceAgentByName(t *testing.T) {
	for _, agent := range []string{"codex", "agy", "opencode"} {
		t.Run(agent, func(t *testing.T) {
			home := t.TempDir()
			rig := newResumeAPIRigWithProcess(t, agent, legacyClaudeID, status.ProcessRunning,
				newFilesystemResumeHistoryResolver(home, generousResumeHistoryLimits()))
			before := len(rig.core.List())

			_, err := rig.api.Launch(handsOffSpec(testEndpoint, rig.sourceID, filepath.Join(rig.stateDir, "new-work")))

			requireRefusedAndLaunchedNothing(t, rig, before, err, agent)
		})
	}
}

// TestHandsOff_RefusesWhenTheTranscriptIsMissingAndLaunchesNothing: a perfectly valid
// conversation id whose transcript is not on disk. Pointing a successor at a file that
// is not there is the bare-launch failure wearing a prompt.
func TestHandsOff_RefusesWhenTheTranscriptIsMissingAndLaunchesNothing(t *testing.T) {
	home := t.TempDir()
	rig := newResumeAPIRigWithProcess(t, "claude", legacyClaudeID, status.ProcessRunning,
		newFilesystemResumeHistoryResolver(home, generousResumeHistoryLimits()))
	before := len(rig.core.List())

	_, err := rig.api.Launch(handsOffSpec(testEndpoint, rig.sourceID, filepath.Join(rig.stateDir, "new-work")))

	requireRefusedAndLaunchedNothing(t, rig, before, err, "was not found")
}

// TestHandsOff_RefusesATranscriptDeletedBetweenTheStatAndTheOpen is the TOCTOU window
// the anchored open actually closes. It is NOT a claim that the file still exists when
// the successor opens it minutes later -- nothing can promise that -- it is the claim
// that the DAEMON's own read is verified between its stat and its open.
func TestHandsOff_RefusesATranscriptDeletedBetweenTheStatAndTheOpen(t *testing.T) {
	home := t.TempDir()
	resolver := newFilesystemResumeHistoryResolver(home, generousResumeHistoryLimits())
	rig := newResumeAPIRigWithProcess(t, "claude", legacyClaudeID, status.ProcessRunning, resolver)
	sourceCwd := rig.meta(t).Cwd
	transcript := writeClaudeHistory(t, home, legacyClaudeID, sourceCwd, legacyCreatedAt.Add(time.Second), "", "")
	// The private deterministic race seam: fire after the candidate has been lstat'd and
	// before it is opened.
	resolver.beforeOpen = func(p string) {
		if p == transcript {
			if err := os.Remove(transcript); err != nil {
				t.Errorf("delete transcript mid-open: %v", err)
			}
		}
	}
	before := len(rig.core.List())

	_, err := rig.api.Launch(handsOffSpec(testEndpoint, rig.sourceID, filepath.Join(rig.stateDir, "new-work")))

	requireRefusedAndLaunchedNothing(t, rig, before, err, "handoff:")
}

// TestHandsOff_ComposesFivePointersLocalLineageAndTheChosenModel is the happy path, and
// it checks the four things four reviewers independently flagged: the prompt carries all
// five pointers, spawned_from carries the LOCAL id (a namespaced one silently breaks the
// TUI parent badge and child count), supervision is left EMPTY rather than "none" (E3),
// and the form's chosen model survives into the composed argv.
func TestHandsOff_ComposesFivePointersLocalLineageAndTheChosenModel(t *testing.T) {
	home := t.TempDir()
	const local = "srclocal"
	sourceCwd := filepath.Join(home, "work")
	src := handsOffSource(local, sourceCwd, legacyClaudeID)
	// Fixture completeness: a source that RAN in this directory implies it exists on
	// disk, and the composer now stats it before launching the successor there.
	if err := os.MkdirAll(sourceCwd, 0o700); err != nil {
		t.Fatal(err)
	}
	transcript := writeClaudeHistory(t, home, legacyClaudeID, sourceCwd, legacyCreatedAt.Add(time.Second), "", "")
	resolver := newFilesystemResumeHistoryResolver(home, generousResumeHistoryLimits())

	got, err := composeHandsOffLaunch(handsOffSpec(testEndpoint, local, "/new-work"), testEndpoint, srcGetter(local, src), resolver)
	if err != nil {
		t.Fatalf("hands-off composition of a RUNNING claude source was refused: %v", err)
	}

	for _, want := range []string{legacyClaudeID, transcript, sourceCwd, "claude", local} {
		if !strings.Contains(got.InitialPrompt, want) {
			t.Errorf("composed prompt is missing the pointer %q:\n%s", want, got.InitialPrompt)
		}
	}
	if got.SpawnedFrom != local {
		t.Errorf("SpawnedFrom = %q, want the LOCAL id %q", got.SpawnedFrom, local)
	}
	if got.SpawnIntent != protocol.SpawnIntentHandoff {
		t.Errorf("SpawnIntent = %q, want %q", got.SpawnIntent, protocol.SpawnIntentHandoff)
	}
	if got.Supervision != "" {
		t.Errorf("Supervision = %q, want EMPTY: no supervisor exists by construction (E3)", got.Supervision)
	}
	if got.Options["model"] != "opus" {
		t.Errorf("Options[model] = %q, want the form's chosen %q", got.Options["model"], "opus")
	}

	// The launch itself goes through the target adapter's ORDINARY Command(), so the
	// model becomes a flag and the composed prompt becomes the positional first prompt.
	resolved, err := composeLaunchSpec(got, testEndpoint, "", srcGetter(local, src), stubLookPath)
	if err != nil {
		t.Fatalf("composed hands-off spec did not compose argv: %v", err)
	}
	argv := strings.Join(resolved.Argv, "\x00")
	if !strings.Contains(argv, "--model\x00opus") {
		t.Errorf("argv %v does not carry the chosen model", resolved.Argv)
	}
	if n := strings.Count(argv, got.InitialPrompt); n != 1 {
		t.Errorf("argv carries the composed prompt %d times, want exactly once", n)
	}
	if resolved.ResumedFrom != "" {
		t.Errorf("ResumedFrom = %q, want empty: a hands-off handoff is not a resume", resolved.ResumedFrom)
	}
}

// TestHandsOff_UsesTheProviderCwdOfAWorktreeSource: for a worktree session Meta.Cwd is
// the repo root while the agent actually ran in <repo>/.swarm/worktrees/<slug>, and the
// provider filed its transcript under the latter. Reading Cwd here would search a
// directory the provider never wrote to.
func TestHandsOff_UsesTheProviderCwdOfAWorktreeSource(t *testing.T) {
	home := t.TempDir()
	const local = "srclocal"
	repo := filepath.Join(home, "repo")
	worktree := filepath.Join(repo, ".swarm", "worktrees", "wt1")
	src := handsOffSource(local, repo, legacyClaudeID)
	src.AgentCwd = worktree
	// Fixture completeness: a source that RAN in this directory implies it exists on
	// disk, and the composer now stats it before launching the successor there.
	if err := os.MkdirAll(worktree, 0o700); err != nil {
		t.Fatal(err)
	}
	transcript := writeClaudeHistory(t, home, legacyClaudeID, worktree, legacyCreatedAt.Add(time.Second), "", "")
	resolver := newFilesystemResumeHistoryResolver(home, generousResumeHistoryLimits())

	got, err := composeHandsOffLaunch(handsOffSpec(testEndpoint, local, "/new-work"), testEndpoint, srcGetter(local, src), resolver)
	if err != nil {
		t.Fatalf("worktree source refused: %v", err)
	}
	if !strings.Contains(got.InitialPrompt, transcript) {
		t.Errorf("composed prompt does not point at the worktree transcript %q:\n%s", transcript, got.InitialPrompt)
	}
	if !strings.Contains(got.InitialPrompt, worktree) {
		t.Errorf("composed prompt does not name the worktree cwd %q:\n%s", worktree, got.InitialPrompt)
	}
}

// TestHandsOff_LaunchesTheSuccessorWhereTheSourceWasWorking closes a mismatch between
// the two halves of the composition.
//
// The TUI has only a protocol.SessionView, which deliberately carries no AgentCwd -- four
// reviewers rejected widening that frozen wire type -- so the client can only send
// SessionView.Cwd, the LAUNCH cwd. For a worktree-isolated source that is the REPO ROOT,
// while the agent ran in <repo>/.swarm/worktrees/<slug>. The prompt already names the
// worktree, because the daemon composes it from ProviderCwd.
//
// Left alone, the successor's process would start in the repo root while its prompt told
// it to run git status somewhere else -- and a git worktree is a SEPARATE CHECKOUT, so
// those are different files, potentially on different branches. The transcript it is about
// to read describes the worktree. "Continue this work" would begin in the wrong tree.
//
// The daemon holds the value the client is missing, which is the whole reason composition
// lives here, so it corrects the cwd rather than inheriting the client's approximation.
// The hands-off form offers no cwd field, so this overrides no human choice.
//
// It also makes worktree and non-worktree sources behave the SAME way -- the successor
// starts where the source was working -- rather than making the worktree case the odd one.
// Two agents in one tree is the already-accepted E6 hazard, mitigated by the prompt's
// warning, and it is no worse here than for an ordinary source.
func TestHandsOff_LaunchesTheSuccessorWhereTheSourceWasWorking(t *testing.T) {
	home := t.TempDir()
	const local = "srclocal"

	t.Run("worktree source launches in the worktree, not the repo root", func(t *testing.T) {
		repo := filepath.Join(home, "repo")
		worktree := filepath.Join(repo, ".swarm", "worktrees", "wt1")
		if err := os.MkdirAll(worktree, 0o700); err != nil {
			t.Fatal(err)
		}
		src := handsOffSource(local, repo, legacyClaudeID)
		src.AgentCwd = worktree
		writeClaudeHistory(t, home, legacyClaudeID, worktree, legacyCreatedAt.Add(time.Second), "", "")
		resolver := newFilesystemResumeHistoryResolver(home, generousResumeHistoryLimits())

		// The client sends the repo root, which is all a SessionView can tell it.
		got, err := composeHandsOffLaunch(handsOffSpec(testEndpoint, local, repo), testEndpoint, srcGetter(local, src), resolver)
		if err != nil {
			t.Fatalf("worktree source refused: %v", err)
		}
		if got.Cwd != worktree {
			t.Fatalf("successor launches in %q; want the worktree %q the source actually ran in", got.Cwd, worktree)
		}
	})

	t.Run("plain source is unchanged", func(t *testing.T) {
		plain := filepath.Join(home, "plain")
		if err := os.MkdirAll(plain, 0o700); err != nil {
			t.Fatal(err)
		}
		src := handsOffSource(local, plain, legacyClaudeID)
		writeClaudeHistory(t, home, legacyClaudeID, plain, legacyCreatedAt.Add(time.Second), "", "")
		resolver := newFilesystemResumeHistoryResolver(home, generousResumeHistoryLimits())

		got, err := composeHandsOffLaunch(handsOffSpec(testEndpoint, local, plain), testEndpoint, srcGetter(local, src), resolver)
		if err != nil {
			t.Fatalf("plain source refused: %v", err)
		}
		if got.Cwd != plain {
			t.Fatalf("successor launches in %q; want %q", got.Cwd, plain)
		}
	})

	t.Run("a provider cwd that no longer exists is refused by name, not launched", func(t *testing.T) {
		repo := filepath.Join(home, "repo2")
		gone := filepath.Join(repo, ".swarm", "worktrees", "torn-down")
		src := handsOffSource(local, repo, legacyClaudeID)
		src.AgentCwd = gone
		writeClaudeHistory(t, home, legacyClaudeID, gone, legacyCreatedAt.Add(time.Second), "", "")
		resolver := newFilesystemResumeHistoryResolver(home, generousResumeHistoryLimits())

		// The protocol layer os.Stats the CLIENT's cwd, never this override, so the
		// override owes its own check -- otherwise a torn-down worktree reaches the
		// spawn as an obscure exec failure instead of a named refusal.
		_, err := composeHandsOffLaunch(handsOffSpec(testEndpoint, local, repo), testEndpoint, srcGetter(local, src), resolver)
		if err == nil {
			t.Fatal("a missing provider cwd composed a launch; want a named refusal")
		}
		if !strings.Contains(err.Error(), "handoff:") {
			t.Fatalf("refusal %q is not named as a handoff refusal", err)
		}
	})
}

// TestHandsOff_RecoversAMissingConversationIDFromProviderHistory: capture is
// hook-driven and a wedged agent fires no hooks, so an empty id is the COMMON case.
// Recovery at handoff time is what makes the feature usable at all -- and unlike the
// resume path's recovery it must tolerate a source that is still running.
func TestHandsOff_RecoversAMissingConversationIDFromProviderHistory(t *testing.T) {
	home := t.TempDir()
	const local = "srclocal"
	sourceCwd := filepath.Join(home, "work")
	src := handsOffSource(local, sourceCwd, "")
	// Fixture completeness: a source that RAN in this directory implies it exists on
	// disk, and the composer now stats it before launching the successor there.
	if err := os.MkdirAll(sourceCwd, 0o700); err != nil {
		t.Fatal(err)
	}
	transcript := writeClaudeHistory(t, home, legacyClaudeID, sourceCwd, legacyCreatedAt.Add(time.Second), "", "")
	resolver := newFilesystemResumeHistoryResolver(home, generousResumeHistoryLimits())

	got, err := composeHandsOffLaunch(handsOffSpec(testEndpoint, local, "/new-work"), testEndpoint, srcGetter(local, src), resolver)
	if err != nil {
		t.Fatalf("recoverable source refused: %v", err)
	}
	if !strings.Contains(got.InitialPrompt, legacyClaudeID) || !strings.Contains(got.InitialPrompt, transcript) {
		t.Errorf("composed prompt does not carry the recovered identity:\n%s", got.InitialPrompt)
	}
}

// TestHandsOff_LocateTranscriptRefusesANonCanonicalID is the belt-and-braces half of
// the traversal guard: even if a caller ever reached the locator with junk, the locator
// itself refuses before it names a file.
func TestHandsOff_LocateTranscriptRefusesANonCanonicalID(t *testing.T) {
	home := t.TempDir()
	resolver := newFilesystemResumeHistoryResolver(home, generousResumeHistoryLimits())
	m := handsOffSource("srclocal", filepath.Join(home, "work"), "")
	for _, convID := range []string{"./cmd/swarm/", "../../../../etc/passwd", "..", ""} {
		path, outcome := resolver.LocateTranscript(m, convID)
		if outcome == resumeHistoryFound || path != "" {
			t.Fatalf("LocateTranscript(%q) = (%q, %v), want a refusal and no path", convID, path, outcome)
		}
	}
}

// TestHandsOff_RefusesATranscriptThatDoesNotNameItsConversation closes a finding from
// adversarial review: confinement, inode identity and regular-file status prove the daemon
// reached the file it MEANT to, and prove nothing about whether that file holds the
// conversation.
//
// Before this, a zero-byte file, a file truncated by a crash, or a file holding unrelated
// bytes was reported as a SUCCESSFUL handoff. The successor would open it, find nothing,
// and be exactly the context-free agent E7 forbids -- reached by a route none of the named
// refusals covered, and reported to the owner as success.
//
// The check reads for IDENTITY only, never for content, so "pointers only" is intact:
// nothing it reads reaches the prompt.
func TestHandsOff_RefusesATranscriptThatDoesNotNameItsConversation(t *testing.T) {
	write := func(t *testing.T, home, cwd, body string) {
		t.Helper()
		dir := claudeProjectDir(home, cwd)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, legacyClaudeID+".jsonl"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	for _, tc := range []struct {
		name    string
		body    string
		refused bool
	}{
		{"empty file", "", true},
		{"whitespace only", "\n\n", true},
		{"not JSON at all", "this is not a transcript\n", true},
		{
			name:    "names a DIFFERENT conversation",
			body:    `{"sessionId":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","cwd":"/x"}` + "\n",
			refused: true,
		},
		{
			name:    "names its own conversation",
			body:    `{"sessionId":"` + legacyClaudeID + `","cwd":"/x"}` + "\n",
			refused: false,
		},
		{
			// The source may still be RUNNING and appending -- the primary case for this
			// feature -- so a half-written final line is the normal state of a live file,
			// not corruption. A complete first record must still carry the day.
			name:    "live file with a half-written trailing record",
			body:    `{"sessionId":"` + legacyClaudeID + `","cwd":"/x"}` + "\n" + `{"type":"assis`,
			refused: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			const local = "srclocal"
			cwd := filepath.Join(home, "work")
			if err := os.MkdirAll(cwd, 0o700); err != nil {
				t.Fatal(err)
			}
			write(t, home, cwd, tc.body)
			src := handsOffSource(local, cwd, legacyClaudeID)
			resolver := newFilesystemResumeHistoryResolver(home, generousResumeHistoryLimits())

			got, err := composeHandsOffLaunch(handsOffSpec(testEndpoint, local, cwd), testEndpoint, srcGetter(local, src), resolver)
			if tc.refused {
				if err == nil {
					t.Fatalf("a transcript that does not name its conversation composed a launch:\n%s", got.InitialPrompt)
				}
				if !strings.Contains(err.Error(), "handoff:") {
					t.Fatalf("refusal %q is not named as a handoff refusal", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("a valid transcript was refused: %v", err)
			}
			if !strings.Contains(got.InitialPrompt, legacyClaudeID) {
				t.Fatalf("composed prompt does not carry the conversation id:\n%s", got.InitialPrompt)
			}
		})
	}
}
