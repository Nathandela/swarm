package gradium

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// Live calls are opt-in and send only this neutral synthetic phrase, never a microphone.
func TestLiveSyntheticRoundtrip(t *testing.T) {
	if os.Getenv("SWARM_GRADIUM_LIVE") != "1" {
		t.Skip("set SWARM_GRADIUM_LIVE=1 with SWARM_GRADIUM_KEY_FILE for an authorized synthetic provider probe")
	}
	path := os.Getenv("SWARM_GRADIUM_KEY_FILE")
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		t.Fatal("provider key requires a private regular file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("provider key could not be read")
	}
	key := strings.TrimSpace(string(raw))
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	speech, err := OpenTTS(ctx, key, Config{Region: "eu", Voice: "vjFyp2BcCyTlUmYb", Format: "pcm_24000"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = speech.Close() }()
	started := time.Now()
	if err := speech.SendPhrase(ctx, "Hello Swarm.", true); err != nil {
		t.Fatal(err)
	}
	if err := speech.End(ctx); err != nil {
		t.Fatal(err)
	}
	var audio bytes.Buffer
	chunks := 0
	var first time.Duration
	for e := range speech.Events() {
		if e.Type == "audio" {
			if first == 0 {
				first = time.Since(started)
			}
			chunks++
			if audio.Len()+len(e.Audio) > 480000 {
				t.Fatal("synthetic speech exceeded ten-second bound")
			}
			audio.Write(e.Audio)
		}
	}
	if err := speech.Err(); err != nil {
		t.Fatal(err)
	}
	if audio.Len() == 0 {
		t.Fatal("provider returned no audio")
	}
	listen, err := OpenSTT(ctx, key, Config{Region: "eu", Language: "en", Format: speech.Ready().OutputFormat})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listen.Close() }()
	written := make(chan error, 1)
	go func() {
		data := audio.Bytes()
		rate := speech.Ready().SampleRate
		for at := 0; at < len(data); at += 3840 {
			end := at + 3840
			if end > len(data) {
				end = len(data)
			}
			if err := listen.SendAudio(ctx, data[at:end]); err != nil {
				written <- err
				return
			}
			timer := time.NewTimer(time.Duration(end-at) * time.Second / time.Duration(rate*2))
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				written <- ctx.Err()
				return
			}
		}
		written <- listen.Flush(ctx, 11)
	}()
	var words, order []string
	flushed, late := false, false
	for e := range listen.Events() {
		if len(order) < 256 {
			order = append(order, e.Type)
		}
		if e.Type == "text" {
			if e.Late {
				late = true
			} else {
				words = append(words, e.Text)
			}
		}
		if e.Type == "flushed" {
			if e.FlushID != 11 {
				t.Fatal("uncorrelated live flush")
			}
			flushed = true
			if err := <-written; err != nil {
				t.Fatal(err)
			}
			if err := listen.End(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := listen.Err(); err != nil {
		t.Fatal(err)
	}
	if !flushed || len(words) == 0 {
		t.Fatal("correlated transcript absent")
	}
	result := map[string]any{"date": "2026-11-08", "kind": "own Go client authenticated synthetic roundtrip", "microphone_used": false, "tts_ready": speech.Ready(), "stt_ready": listen.Ready(), "tts_chunks": chunks, "tts_pcm_bytes": audio.Len(), "tts_first_network_audio_ms": float64(first.Microseconds()) / 1000, "transcript": strings.Join(words, " "), "flushed": flushed, "late_text_observed": late, "event_order": order, "scope": "one neutral synthetic phrase; no acoustic, phone, billing-refund or latency qualification"}
	data, _ := json.MarshalIndent(result, "", "  ")
	t.Log(string(data))
	if destination := os.Getenv("SWARM_GRADIUM_LIVE_EVIDENCE"); destination != "" {
		if err := os.WriteFile(destination, append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
