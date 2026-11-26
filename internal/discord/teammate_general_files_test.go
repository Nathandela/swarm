package discord

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/bwmarrin/discordgo"
)

func TestTeammateFilePublicationCarriesGeneralFileTypes(t *testing.T) {
	ctx := context.Background()
	files := []struct{ name, kind string }{
		{"Quarterly deck.pptx", "application/vnd.openxmlformats-officedocument.presentationml.presentation"},
		{"memo.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
		{"model.xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"},
		{"bundle.zip", "application/zip"},
		{"notes.txt", "text/plain; charset=utf-8"},
		{"blob.bin", "application/octet-stream"},
		{"table.csv", "text/csv"},
	}
	p := TeammatePublication{ID: "general", AdmissionID: "input", Body: "Synthetic files"}
	var data [][]byte
	for i, f := range files {
		raw := []byte(fmt.Sprintf("PK\x03\x04synthetic-%d\x00\xff", i))
		data = append(data, raw)
		p.Files = append(p.Files, outboxTestFile(fmt.Sprintf("file%d", i), f.name, f.kind, raw))
	}
	posts := 0
	c, _ := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
		if outboxTestScope(w, r, true) {
			return
		}
		posts++
		mr, err := r.MultipartReader()
		if err != nil {
			t.Error(err)
			return
		}
		var payload struct {
			Content string
			Nonce   string
		}
		if part, err := mr.NextPart(); err != nil || json.NewDecoder(part).Decode(&payload) != nil {
			t.Errorf("payload_json: %v", err)
			return
		}
		message := discordgo.Message{ID: "301", ChannelID: "201", Author: &discordgo.User{ID: "99"}, Content: payload.Content}
		for i, f := range p.Files {
			part, err := mr.NextPart()
			if err != nil {
				t.Error(err)
				return
			}
			got, _ := io.ReadAll(part)
			sum := sha256.Sum256(got)
			if part.FileName() != f.Name || part.Header.Get("Content-Type") != f.ContentType || hex.EncodeToString(sum[:]) != f.SHA256 || !bytes.Equal(got, data[i]) {
				t.Errorf("part %d: name=%q type=%q", i, part.FileName(), part.Header.Get("Content-Type"))
			}
			message.Attachments = append(message.Attachments, &discordgo.MessageAttachment{ID: fmt.Sprint(401 + i), Filename: part.FileName(), Size: len(got)})
		}
		_ = json.NewEncoder(w).Encode(message)
	})
	d := bindFileOutboxTest(t, c)
	c.manager = &fileOutboxTestManager{data: data}
	if err := c.queuePublication(ctx, d, p); err != nil {
		t.Fatal(err)
	}
	if err := c.sendPending(ctx, d); err != nil || posts != 1 {
		t.Fatalf("posts=%d err=%v", posts, err)
	}
	var ids []string
	for i := range p.Files {
		ids = append(ids, fmt.Sprint(401+i))
	}
	assertOutboxFileEvidence(t, c.service.Store, d, p, StateSent, []string{"301"}, ids)
}

func TestTeammateFilePublicationRejectsUnsafeFileSets(t *testing.T) {
	ctx := context.Background()
	one := []byte("synthetic")
	big := make([]byte, schema.MaxPublicationFileBytes+1)
	many := make([]schema.PublicationFile, schema.MaxPublicationFiles+1)
	for i := range many {
		many[i] = outboxTestFile(fmt.Sprint("f", i), fmt.Sprint("f", i, ".bin"), "application/octet-stream", one)
	}
	for name, tc := range map[string]struct {
		files []schema.PublicationFile
		data  [][]byte
		want  error
	}{
		"content type with markup": {[]schema.PublicationFile{outboxTestFile("a", "a.bin", "text/html", one)}, [][]byte{one}, ErrInvalid},
		"malformed content type":   {[]schema.PublicationFile{outboxTestFile("a", "a.bin", "not a type", one)}, [][]byte{one}, ErrInvalid},
		"path in name":             {[]schema.PublicationFile{outboxTestFile("a", "../a.bin", "application/octet-stream", one)}, [][]byte{one}, ErrInvalid},
		"too many files":           {many, nil, ErrCapacity},
		"too large":                {[]schema.PublicationFile{outboxTestFile("a", "a.bin", "application/octet-stream", big)}, [][]byte{big}, ErrCapacity},
		"tampered bytes":           {[]schema.PublicationFile{outboxTestFile("a", "a.bin", "application/octet-stream", one)}, [][]byte{[]byte("swapped!!")}, errPublicationFilesChanged},
	} {
		t.Run(name, func(t *testing.T) {
			c, _ := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
				if !outboxTestScope(w, r, true) {
					t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
				}
			})
			d := bindFileOutboxTest(t, c)
			c.manager = &fileOutboxTestManager{data: tc.data}
			err := c.queuePublication(ctx, d, TeammatePublication{ID: "p", AdmissionID: "input", Body: "x", Files: tc.files})
			if !errors.Is(err, tc.want) && !strings.Contains(fmt.Sprint(err), fmt.Sprint(tc.want)) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}
