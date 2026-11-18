package openailive

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTranscribeSendsWAVAndLanguage(t *testing.T) {
	pcm := []byte{0, 0, 1, 255}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("request method or authorization mismatch")
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("invalid multipart request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.FormValue("model") != "gpt-transcribe" || r.FormValue("language") != "" {
			t.Errorf("model or language field mismatch")
		}
		wantLanguage := r.URL.Query().Get("want-language")
		if r.FormValue("languages[]") != wantLanguage || len(r.Form["languages[]"]) > 1 {
			t.Errorf("unexpected language hint %q", r.Form["languages[]"])
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Errorf("missing WAV: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		defer func() { _ = file.Close() }()
		wav, err := io.ReadAll(file)
		le := binary.LittleEndian
		if err != nil || header.Filename != "utterance.wav" || len(wav) != 44+len(pcm) ||
			string(wav[:4]) != "RIFF" || le.Uint32(wav[4:8]) != uint32(36+len(pcm)) ||
			string(wav[8:16]) != "WAVEfmt " || le.Uint32(wav[16:20]) != 16 ||
			le.Uint16(wav[20:22]) != 1 || le.Uint16(wav[22:24]) != 1 ||
			le.Uint32(wav[24:28]) != 24000 || le.Uint32(wav[28:32]) != 48000 ||
			le.Uint16(wav[32:34]) != 2 || le.Uint16(wav[34:36]) != 16 ||
			string(wav[36:40]) != "data" || le.Uint32(wav[40:44]) != uint32(len(pcm)) ||
			!bytes.Equal(wav[44:], pcm) {
			t.Errorf("incorrect mono PCM16 WAV: %x, %v", wav, err)
		}
		_, _ = fmt.Fprint(w, `{"text":"Final transcript.","languages":[{"code":"en"}]}`)
	}))
	defer server.Close()
	for _, input := range []struct{ language, want string }{{"en", "en"}, {"zh-tw", "zh-tw"}, {"any", ""}, {"", ""}} {
		got, err := transcribe(t.Context(), "test-token", pcm, input.language, server.URL+"?want-language="+input.want)
		if err != nil || got != "Final transcript." {
			t.Fatalf("language %q: got %q, %v", input.language, got, err)
		}
	}
}

func TestTranscribeRejectsInvalidInput(t *testing.T) {
	for _, test := range []struct {
		key, language string
		pcm           []byte
	}{
		{"test-token", "en", nil},
		{"test-token", "en", []byte{0}},
		{"test-token", "en", make([]byte, MaxUtteranceBytes+2)},
		{"test-token", "en\n", []byte{0, 0}},
		{"", "en", []byte{0, 0}},
		{"test-token\n", "en", []byte{0, 0}},
		{"test token", "en", []byte{0, 0}},
		{"test-tokén", "en", []byte{0, 0}},
	} {
		if _, err := transcribe(t.Context(), test.key, test.pcm, test.language, "invalid endpoint"); err == nil || !strings.Contains(err.Error(), "invalid transcription input") {
			t.Fatalf("invalid input reached HTTP request: %v", err)
		}
	}
}

func TestTranscribeBoundsAndRedactsResponses(t *testing.T) {
	const secret = "private-response-secret"
	for _, test := range []struct {
		name   string
		status int
		body   string
		ok     bool
	}{
		{"redirect", 302, secret, false},
		{"preserving-redirect", 307, secret, false},
		{"auth", 401, secret, false},
		{"throttle", 429, secret, false},
		{"invalid-json", 200, secret, false},
		{"missing-text", 200, `{"languages":[]}`, false},
		{"null-text", 200, `{"text":null}`, false},
		{"unpaired-surrogate", 200, `{"text":"\uD800"}`, false},
		{"invalid-utf8", 200, "{\"text\":\"\xff\"}", false},
		{"oversize-text", 200, `{"text":"` + strings.Repeat("z", (64<<10)+1) + `"}`, false},
		{"trailing-data", 200, `{"text":"safe"}{}`, false},
		{"oversize", 200, `{"text":"` + strings.Repeat("z", 128<<10) + `"}`, false},
		{"silence", 200, `{"text":""}`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/" {
					t.Error("client followed redirect")
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				w.Header().Set("Location", "/must-not-follow")
				w.WriteHeader(test.status)
				_, _ = fmt.Fprint(w, test.body)
			}))
			defer server.Close()
			got, err := transcribe(t.Context(), "test-token", []byte{0, 0}, "", server.URL)
			if got != "" || (err == nil) != test.ok || err != nil && (strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "test-token")) {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}
}

func TestTranscribeCancellationDuringResponse(t *testing.T) {
	for _, useDeadline := range []bool{false, true} {
		t.Run(fmt.Sprint(useDeadline), func(t *testing.T) {
			reached := make(chan struct{})
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				close(reached)
				<-release
			}))
			defer server.Close()
			defer close(release)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			want := context.Canceled
			if useDeadline {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, time.Second)
				defer stop()
				want = context.DeadlineExceeded
			}
			result := make(chan error, 1)
			go func() {
				_, err := transcribe(ctx, "test-token", []byte{0, 0}, "", server.URL)
				result <- err
			}()
			select {
			case <-reached:
			case <-time.After(3 * time.Second):
				t.Fatal("transcription response headers were not reached")
			}
			if !useDeadline {
				cancel()
			}
			select {
			case err := <-result:
				if !errors.Is(err, want) {
					t.Fatalf("lost request context: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("transcription did not return")
			}
		})
	}
}
