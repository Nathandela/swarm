package gradium

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestLiveWireCharacterization(t *testing.T) {
	if os.Getenv("SWARM_GRADIUM_CHARACTERIZE") != "1" {
		t.Skip("explicit synthetic characterization only")
	}
	key, err := os.ReadFile(os.Getenv("SWARM_GRADIUM_KEY_FILE"))
	if err != nil {
		t.Fatal("key unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "wss://eu.api.gradium.ai/api/speech/tts", &websocket.DialOptions{HTTPHeader: http.Header{"X-Api-Key": []string{strings.TrimSpace(string(key))}}, HTTPClient: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}})
	if err != nil {
		t.Fatal("dial failed")
	}
	defer func() { _ = c.CloseNow() }()
	c.SetReadLimit(MaxMessageBytes)
	if err := sendFrame(ctx, c, map[string]any{"type": "setup", "model_name": "default", "voice_id": "vjFyp2BcCyTlUmYb", "output_format": "pcm_24000"}); err != nil {
		t.Fatal("setup failed")
	}
	for i := 0; i < 128; i++ {
		frame := readFrame(ctx, c)
		if frame == nil {
			t.Fatal("early EOF")
		}
		b, _ := json.Marshal(characterizationFields(frame))
		t.Log(string(b))
		if frame["type"] == "error" {
			t.Fatal("provider returned a terminal error")
		}
		if frame["type"] == "ready" {
			_ = sendFrame(ctx, c, map[string]any{"type": "text", "text": "Hello Swarm. <flush>"})
			_ = sendFrame(ctx, c, map[string]any{"type": "end_of_stream"})
		}
		if frame["type"] == "end_of_stream" {
			return
		}
	}
	t.Fatal("unbounded synthetic frames")
}

// Diagnostic fixtures retain only known event names and numeric public metadata.
func characterizationFields(frame map[string]any) map[string]any {
	kind, _ := frame["type"].(string)
	switch kind {
	case "ready", "audio", "text", "flushed", "end_of_stream", "error":
	default:
		kind = "unknown"
	}
	public := map[string]any{"type": kind}
	if code, ok := frame["code"].(float64); ok {
		public["code"] = code
	}
	if kind == "error" || kind == "unknown" {
		return public
	}
	for _, field := range []string{"sample_rate", "frame_size", "delay_in_frames", "start_s", "stop_s", "stream_id", "flush_id"} {
		if number, ok := frame[field].(float64); ok {
			public[field] = number
		}
	}
	if audio, ok := frame["audio"].(string); ok {
		public["audio_base64_bytes"] = len(audio)
	}
	return public
}
