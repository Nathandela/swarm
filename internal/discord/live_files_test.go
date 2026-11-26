//go:build livediscord

package discord

// Live Discord check for worker file publication. It never runs in CI:
//
//	SWARM_LIVE_DISCORD_STATE=~/.local/state/swarm/discord-personal \
//	  go test -tags livediscord ./internal/discord -run TestLiveWorkerFilesReachDiscordIntact -v
//
// It posts one clearly labeled message with synthetic files to the configured
// personal DM, then downloads every attachment and compares name, size and SHA-256.

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/workermail"
)

func liveZip(t *testing.T, entries ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, name := range entries {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.Write([]byte("<synthetic/>"))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestLiveWorkerFilesReachDiscordIntact(t *testing.T) {
	state := os.Getenv("SWARM_LIVE_DISCORD_STATE")
	if state == "" {
		t.Skip("set SWARM_LIVE_DISCORD_STATE to a personal connector state dir")
	}
	cfg, err := LoadConfig(state)
	if err != nil || cfg.Mode != "personal" || !snowflake(cfg.DMChannelID) {
		t.Fatalf("personal config: %v", err)
	}
	token, err := LoadToken(cfg.TokenFile)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewDiscordClient(token)
	if err != nil {
		t.Fatal(err)
	}

	big := make([]byte, 3<<20)
	_, _ = rand.Read(big)
	png, _ := hex.DecodeString("89504e470d0a1a0a0000000d49484452000000010000000108060000001f15c4890000000d49444154789c6360000002000154a24f5d0000000049454e44ae426082")
	files := map[string][]byte{
		"Synthétique deck v2.pptx": liveZip(t, "[Content_Types].xml", "ppt/presentation.xml"),
		"memo.docx":                liveZip(t, "[Content_Types].xml", "word/document.xml"),
		"model.xlsx":               liveZip(t, "[Content_Types].xml", "xl/workbook.xml"),
		"bundle.zip":               liveZip(t, "a.txt"),
		"notes.txt":                []byte("Synthetic Swarm attachment test.\n"),
		"data.json":                []byte(`{"synthetic":true}`),
		"table.csv":                []byte("column,value\nsynthetic,1\n"),
		"random 3MiB.bin":          big,
		"pixel.png":                png,
		"report.pdf":               []byte("%PDF-1.4\n%synthetic\n"),
	}
	checkout, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for name, data := range files {
		path := filepath.Join(checkout, name)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	uploads, err := workermail.CaptureFiles(checkout, paths)
	if err != nil {
		t.Fatal(err)
	}
	mail := t.TempDir()
	b := workermail.Binding{Discussion: "live", Parent: "parent", Session: "worker", Instance: "instance", Attempt: "attempt"}
	note, err := workermail.Submit(mail, b, "live", "complete", "", true, uploads...)
	if err != nil {
		t.Fatal(err)
	}
	var data [][]byte
	for _, f := range note.Files {
		raw, err := workermail.ReadFile(mail, b, note.ID, f.ID)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, raw)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	nonce := fmt.Sprint(time.Now().UnixNano())
	text := "Swarm attachment test with synthetic files only. Safe to ignore or delete."
	msg, err := client.postMessageWithFiles(ctx, Outbound{Route: Route{ChannelID: cfg.DMChannelID}, Text: text, Nonce: nonce}, "", note.Files, data)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("posted message %s with %d attachments", msg.ID, len(msg.Attachments))
	if _, ok := publicationAttachmentIDs(note.Files, msg.Attachments); !ok {
		for i, a := range msg.Attachments {
			t.Logf("sent %q (%d) got %q (%d)", note.Files[i].Name, note.Files[i].Size, a.Filename, a.Size)
		}
		t.Fatal("Discord receipt does not match the sent names and sizes")
	}
	for i, a := range msg.Attachments {
		resp, err := http.Get(a.URL)
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("download %s: %d %v", a.Filename, resp.StatusCode, err)
		}
		sum := sha256.Sum256(got)
		f := note.Files[i]
		ok := hex.EncodeToString(sum[:]) == f.SHA256 && int64(len(got)) == f.Size
		t.Logf("%-28s %-75s %8d bytes sha256 %s match=%v", a.Filename, f.ContentType, len(got), f.SHA256[:16], ok)
		if !ok {
			t.Errorf("%s: downloaded bytes differ", a.Filename)
		}
	}
}
