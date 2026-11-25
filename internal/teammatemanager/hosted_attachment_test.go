package teammatemanager

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/discord"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/voice"
	"github.com/Nathandela/swarm/internal/workermail"
)

func runHostedAttachment(t *testing.T, ctx context.Context, r *Runtime, thread discord.TeammateDiscussion, reader discord.TeammateHistoryReader, project, state, fixture, cli string, wait func(string, time.Duration, func() bool), outputs *[]discord.TeammatePublication, roster func() []protocol.SessionView) {
	t.Helper()
	var f struct{ Manifest, Document, Pixel string }
	raw, err := os.ReadFile(filepath.Join(fixture, "expected.json"))
	if err != nil || json.Unmarshal(raw, &f) != nil || !filepath.IsAbs(f.Manifest) || len(f.Document) != 16 || len(f.Pixel) != 6 {
		t.Fatal("generate a private fixture with TestPersonalAttachmentOnlyHandoff or TestGuildAttachmentIngressUsesVisibleForwardAndReply")
	}
	body := "Delegate to a single worker with swarm spawn --cli " + cli + " in " + project + ". Only the worker may open the attached document and image. Ask it to read the document's complete marker and decode the RGB of the PNG top-left pixel as six lowercase hex digits. It must send a complete note with both results from the same child; publish both results from that note. Inspect only the manifest yourself; never open the files or provide a guess."
	twoWay := os.Getenv("SWARM_TEAMMATE_TEST_TWO_WAY") == "1"
	if twoWay {
		body += " Ask the worker to copy the input PNG bytes to screenshot.png in its working directory and write a summary.csv with header marker,pixel and one data row containing the two values. Use a final newline. Send one swarm teammate message --kind complete --text with both values and --file screenshot.png --file summary.csv. Once that command succeeds, overwrite the worker's local summary.csv with the exact text replaced to prove the note's files are already pinned. Use swarm_read_note to get the file IDs from that completion, then use publish with those file_ids, the values in the caption, and final=true."
		if thread.Route.GuildID != "" {
			body += " Address the final reply to the verified participant 401, message 301, using recipient_id and recipient_message_id."
		}
	}
	authority := discord.AuthorityOwnerDirect
	if thread.Route.GuildID != "" {
		authority = discord.AuthorityExternal
	}
	a := discord.TeammateAdmission{ExpectReply: true, Discussion: thread, OperationID: "input-301", Revision: "rev-301", Text: body, AttachmentManifest: f.Manifest, Route: discord.Route{GuildID: thread.Route.GuildID, ChannelID: thread.Route.ChannelID, MessageID: "301"}, Author: discord.Actor{ID: "401", Name: "fixture"}, InvokerID: "401", Authority: authority}
	for attempt := 1; ; attempt++ {
		err = r.Admit(ctx, a, reader)
		if err == nil {
			break
		}
		if attempt >= 3 || !errors.Is(err, voice.ErrDiscussionActivationUnavailable) || !errors.Is(err, voice.ErrControllerNativeUnavailable) {
			t.Fatal(err)
		}
		t.Logf("source input-301 retained while %s starts: %v", cli, err)
		time.Sleep(time.Second)
	}
	wait("same worker reported document marker and decoded image pixel", 4*time.Minute, func() bool {
		for _, p := range *outputs {
			if !strings.HasPrefix(p.AdmissionID, "worker-message-") || p.JobID == "" || p.AttemptID == "" || !strings.Contains(p.Body, f.Document) || !strings.Contains(strings.ToLower(p.Body), f.Pixel) {
				continue
			}
			if twoWay && len(p.Files) != 2 {
				continue
			}
			st, err := r.Status(thread)
			if err != nil {
				return false
			}
			for _, n := range st.Notes {
				if n.Kind != "complete" || n.JobID != p.JobID || n.AttemptID != p.AttemptID {
					continue
				}
				for _, row := range roster() {
					if row.ID == n.Session && row.SpawnedFrom != "" {
						stored, err := workermail.Lookup(state, workermail.Binding{Discussion: row.WorkerDiscussion, Session: n.Session, Instance: n.Instance, Attempt: row.WorkerAttempt}, n.ID)
						if err != nil || !strings.Contains(stored.Text, f.Document) || !strings.Contains(strings.ToLower(stored.Text), f.Pixel) {
							continue
						}
						if twoWay {
							mutated, err := os.ReadFile(filepath.Join(row.Cwd, "summary.csv"))
							if err != nil || strings.TrimSpace(string(mutated)) != "replaced" {
								continue
							}
							verifyHostedPublishedFiles(t, ctx, r, thread, p, f.Manifest, f.Document, f.Pixel)
						}
						t.Logf("attachment worker %s instance %s attempt %s note %s publication %s", n.Session, n.Instance, n.AttemptID, n.ID, p.ID)
						return true
					}
				}
			}
		}
		return false
	})
}

func verifyHostedPublishedFiles(t *testing.T, ctx context.Context, r *Runtime, discussion discord.TeammateDiscussion, p discord.TeammatePublication, manifest, document, pixel string) {
	t.Helper()
	var entries []struct{ File, ContentType string }
	raw, err := os.ReadFile(manifest)
	if err != nil || json.Unmarshal(raw, &entries) != nil {
		t.Fatal("cannot read the isolated connector manifest", err)
	}
	expected := map[string][]byte{"summary.csv": []byte("marker,pixel\n" + document + "," + pixel + "\n")}
	for _, entry := range entries {
		if entry.ContentType == "image/png" {
			expected["screenshot.png"], err = os.ReadFile(filepath.Join(filepath.Dir(manifest), entry.File))
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	recipient := ""
	if discussion.Route.GuildID != "" {
		recipient = "401"
	}
	hash := schema.PublicationContentHash(p.Body, p.Files, recipient)
	if p.RecipientID != recipient || p.BodyHash != "" && p.BodyHash != hash {
		t.Fatalf("wrong publication identity or verified recipient %q", p.RecipientID)
	}
	p.BodyHash = hash
	if err := r.ValidatePublication(ctx, discussion, p); err != nil {
		t.Fatalf("exported publication receipt did not validate: %v", err)
	}
	contents, err := r.ReadPublicationFiles(ctx, discussion, p)
	if err != nil || len(contents) != len(expected) || len(expected) != 2 {
		t.Fatalf("publication file receipt failed: %v (files=%d)", err, len(contents))
	}
	for i, f := range p.Files {
		want, ok := expected[f.Name]
		if !ok || !bytes.Equal(contents[i], want) || f.Size != int64(len(want)) || f.SHA256 != fmt.Sprintf("%x", sha256.Sum256(want)) {
			t.Fatalf("file %s did not match its receipt or immutable source", f.Name)
		}
		delete(expected, f.Name)
	}
	t.Logf("file receipt %s hash %s recipient %q reconfirmed original PNG and CSV after worker source edit", p.ID, p.BodyHash, p.RecipientID)
}

// The opt-in fixture uses real Claude terminals and Swarm shims. A loopback model
// scripts native tools and checks their results without using account credentials.
type hostedScopedClaude struct {
	mu       sync.Mutex
	steps    map[string][]hostedClaudeTool
	results  map[string]map[string]json.RawMessage
	seen     map[string]bool
	requests int
}

type hostedClaudeTool struct {
	name  string
	input map[string]string
}

func hostedShellWord(arg string) string {
	return "'" + strings.ReplaceAll(arg, "'", "'\"'\"'") + "'"
}

func prepareHostedScopedClaude(t *testing.T, root, project, swarm string) *hostedScopedClaude {
	t.Helper()
	claude, err := exec.LookPath("claude")
	if err != nil || !filepath.IsAbs(claude) {
		t.Fatal("install a native Claude CLI", err)
	}
	version, err := exec.Command(claude, "--version").Output()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("native Claude version %s", strings.TrimSpace(string(version)))
	git := exec.Command("git", "init", "-q", project)
	if out, err := git.CombinedOutput(); err != nil {
		t.Fatalf("initialize trusted scratch project: %v: %s", err, out)
	}
	fixture := &hostedScopedClaude{steps: map[string][]hostedClaudeTool{}, results: map[string]map[string]json.RawMessage{}, seen: map[string]bool{}}
	server := httptest.NewServer(http.HandlerFunc(fixture.serve))
	t.Cleanup(server.Close)
	profile, path := filepath.Join(root, "claude-profile"), filepath.Join(root, "bin")
	for _, dir := range []string{profile, path} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	profileSettings := `{"permissions":{"blockReadsOutsideWorkingDirectories":true,"allow":["Bash(swarm spawn *)"]}}`
	trust := fmt.Sprintf(`{"hasCompletedOnboarding":true,"projects":{%q:{"hasTrustDialogAccepted":true}}}`, project)
	for name, raw := range map[string]string{"settings.json": profileSettings, ".claude.json": trust} {
		if err := os.WriteFile(filepath.Join(profile, name), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	wrapper := "#!/bin/sh\nunset ANTHROPIC_API_KEY\nexport CLAUDE_CONFIG_DIR=" + hostedShellWord(profile) + " ANTHROPIC_BASE_URL=" + hostedShellWord(server.URL) +
		" ANTHROPIC_AUTH_TOKEN=local-synthetic-fixture CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 DISABLE_AUTOUPDATER=1\nexec " + hostedShellWord(claude) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(path, "claude"), []byte(wrapper), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(swarm, filepath.Join(path, "swarm")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", path+string(os.PathListSeparator)+os.Getenv("PATH"))
	return fixture
}

func (h *hostedScopedClaude) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/messages" {
		_, _ = fmt.Fprint(w, `{"input_tokens":20}`)
		return
	}
	var request struct {
		Model    string
		Messages []struct {
			Content json.RawMessage
		}
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, "cannot read synthetic request", http.StatusBadRequest)
		return
	}
	role := ""
	results := map[string]json.RawMessage{}
	for _, message := range request.Messages {
		var blocks []struct {
			Type, Text string
			ID         string `json:"tool_use_id"`
		}
		if json.Unmarshal(message.Content, &blocks) != nil {
			var text string
			if json.Unmarshal(message.Content, &text) == nil {
				blocks = append(blocks, struct {
					Type, Text string
					ID         string `json:"tool_use_id"`
				}{Type: "text", Text: text})
			}
		}
		var rawBlocks []json.RawMessage
		_ = json.Unmarshal(message.Content, &rawBlocks)
		for i, block := range blocks {
			if block.Type == "tool_result" {
				results[block.ID] = rawBlocks[i]
			}
			if block.Type == "text" {
				for _, label := range []string{"warmup", "dispatcher", "worker"} {
					if strings.Contains(block.Text, "hosted-scope-"+label) {
						role = label
					}
				}
			}
		}
	}
	h.mu.Lock()
	h.requests++
	h.seen[role] = true
	if len(results) > len(h.results[role]) {
		h.results[role] = results
	}
	steps := h.steps[role]
	h.mu.Unlock()
	step := 0
	for ; step < len(steps); step++ {
		if results[fmt.Sprintf("toolu_%s_%d", role, step)] == nil {
			break
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	emit := func(kind string, fields map[string]any) {
		fields["type"] = kind
		raw, _ := json.Marshal(fields)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, raw)
	}
	message := map[string]any{"id": fmt.Sprintf("msg_fixture_%s_%d", role, step), "type": "message", "role": "assistant", "content": []any{}, "model": request.Model,
		"stop_reason": nil, "stop_sequence": nil, "usage": map[string]int{"input_tokens": 20, "output_tokens": 1}}
	emit("message_start", map[string]any{"message": message})
	reason := "end_turn"
	if step < len(steps) {
		tool := steps[step]
		raw, _ := json.Marshal(tool.input)
		emit("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "tool_use", "id": fmt.Sprintf("toolu_%s_%d", role, step), "name": tool.name, "input": struct{}{}}})
		emit("content_block_delta", map[string]any{"index": 0, "delta": map[string]string{"type": "input_json_delta", "partial_json": string(raw)}})
		reason = "tool_use"
	} else {
		emit("content_block_start", map[string]any{"index": 0, "content_block": map[string]string{"type": "text", "text": ""}})
		emit("content_block_delta", map[string]any{"index": 0, "delta": map[string]string{"type": "text_delta", "text": "Fixture done."}})
	}
	emit("content_block_stop", map[string]any{"index": 0})
	emit("message_delta", map[string]any{"delta": map[string]any{"stop_reason": reason, "stop_sequence": nil}, "usage": map[string]int{"output_tokens": 15}})
	emit("message_stop", map[string]any{})
}

func (h *hostedScopedClaude) run(t *testing.T, ctx context.Context, r *Runtime, thread discord.TeammateDiscussion, reader discord.TeammateHistoryReader, project string, wait func(string, time.Duration, func() bool), roster func() []protocol.SessionView) {
	t.Helper()
	admit := func(id, text, manifest string) {
		t.Helper()
		authority := discord.AuthorityOwnerDirect
		if thread.Route.GuildID != "" {
			authority = discord.AuthorityExternal
		}
		a := discord.TeammateAdmission{Discussion: thread, OperationID: "input-" + id, Revision: "rev-" + id, Text: text, AttachmentManifest: manifest,
			Route:  discord.Route{GuildID: thread.Route.GuildID, ChannelID: thread.Route.ChannelID, MessageID: id},
			Author: discord.Actor{ID: "401", Name: "fixture"}, InvokerID: "401", Authority: authority}
		if err := r.Admit(ctx, a, reader); err != nil {
			t.Fatal(err)
		}
	}
	dir, err := r.AttachmentDirectory(thread)
	if err != nil {
		t.Fatal(err)
	}
	admit("301", "hosted-scope-warmup", "")
	var manager string
	wait("native dispatcher completed its warmup before the file existed", 45*time.Second, func() bool {
		h.mu.Lock()
		warm := h.seen["warmup"]
		h.mu.Unlock()
		view, err := r.Status(thread)
		if !warm || err != nil || view.ManagerSession == "" || view.PendingAdmissions != 0 {
			return false
		}
		manager = view.ManagerSession
		return true
	})
	var marker [12]byte
	if _, err := rand.Read(marker[:]); err != nil {
		t.Fatal(err)
	}
	document := fmt.Sprintf("scope-proof,%x", marker[:])
	picture := image.NewRGBA(image.Rect(0, 0, 48, 48))
	for y := 0; y < 48; y++ {
		for x := 0; x < 48; x++ {
			picture.SetRGBA(x, y, color.RGBA{marker[x%len(marker)], marker[y%len(marker)], marker[(x+y)%len(marker)], 255})
		}
	}
	var jpg bytes.Buffer
	if err := jpeg.Encode(&jpg, picture, nil); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(dir, "late-input")
	if err := os.Mkdir(bundle, 0700); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(bundle, "files.json")
	for name, raw := range map[string][]byte{
		"files.json": []byte(`[{"file":"report.csv","content_type":"text/csv"},{"file":"image.jpg","content_type":"image/jpeg"}]`),
		"report.csv": []byte(document + "\n"), "image.jpg": jpg.Bytes(),
	} {
		if err := os.WriteFile(filepath.Join(bundle, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	reads := []hostedClaudeTool{
		{"Read", map[string]string{"file_path": manifest}},
		{"Read", map[string]string{"file_path": filepath.Join(bundle, "report.csv")}},
		{"Read", map[string]string{"file_path": filepath.Join(bundle, "image.jpg")}},
	}
	h.mu.Lock()
	h.steps["worker"] = reads
	h.steps["dispatcher"] = append(append([]hostedClaudeTool(nil), reads...), hostedClaudeTool{"Bash", map[string]string{
		"command": "swarm spawn --cli claude --dir " + hostedShellWord(project) + " --prompt hosted-scope-worker", "description": "Spawn one Claude worker for the staged fixture",
	}})
	h.mu.Unlock()
	admit("302", "hosted-scope-dispatcher", manifest)
	wait("both original dispatcher and ordinary Claude child read the late manifest, CSV and exact JPEG without a prompt", 90*time.Second, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		jpegPayload := []byte(base64.StdEncoding.EncodeToString(jpg.Bytes()))
		for _, role := range []string{"dispatcher", "worker"} {
			for step, proof := range [][]byte{[]byte("report.csv"), []byte(document), jpegPayload} {
				raw := h.results[role][fmt.Sprintf("toolu_%s_%d", role, step)]
				if raw == nil {
					return false
				}
				if !bytes.Contains(raw, proof) {
					t.Fatalf("%s native Read at step %d did not contain fixture proof: %.240s", role, step, raw)
				}
			}
		}
		view, err := r.Status(thread)
		if err != nil || view.ManagerSession != manager {
			return false
		}
		_, local, ok := protocol.ParseID(manager)
		if !ok {
			t.Fatalf("invalid dispatcher session ID %q", manager)
		}
		var child string
		for _, row := range roster() {
			if row.SpawnedFrom != local {
				continue
			}
			if child != "" || row.Agent != "claude" || row.WorkerInstance == "" || row.WorkerAttempt == "" {
				t.Fatalf("expected one ordinary Claude child with a bound worker incarnation; got %q and %+v", child, row)
			}
			child = row.ID
		}
		if child == "" {
			return false
		}
		t.Logf("same dispatcher %s, ordinary child %s, native backend requests %d, JPEG %d bytes", manager, child, h.requests, jpg.Len())
		return true
	})
}
