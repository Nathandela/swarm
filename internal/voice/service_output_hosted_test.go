package voice

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/Nathandela/swarm/internal/voice/openailive"
)

// Run only with an explicitly approved private voice settings directory.
// Exactly one short Live session is opened and no log contains the credential,
// provider transcript, WAV data, native manager or worker input.
func TestHostedOpenAIPreview(t *testing.T) {
	dir := os.Getenv("SWARM_TEST_HOSTED_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set SWARM_TEST_HOSTED_PREVIEW_DIR for an approved single-session provider probe")
	}
	key, err := LoadOpenAIKey(dir)
	if err != nil {
		t.Fatal("approved private OpenAI credential unavailable")
	}
	const phrase = "The teal notebook is beside the silver lamp."
	ctx, stop := context.WithTimeout(context.Background(), previewTimeout)
	defer stop()
	greeting := previewGreeting(phrase, "en")
	start := time.Now()
	session, err := openailive.OpenPreview(ctx, key, "marin", greeting)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	var stats previewStats
	pcm, previewErr := previewPCM(ctx, session, greeting, &stats)
	normalize := func(s string) string {
		return strings.Join(strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) }), " ")
	}
	transcribed := normalize(stats.Transcript.String())
	match := strings.Contains(transcribed, normalize(phrase))
	matchedWords := 0
	for _, word := range []string{"teal", "notebook", "silver", "lamp"} {
		if strings.Contains(" "+transcribed+" ", " "+word+" ") {
			matchedWords++
		}
	}
	t.Logf("duration_ms=%d commentary_ack=%t audio_frames=%d signal_frames=%d pcm_bytes=%d transcript_fragments=%d synthetic_keyword_matches=%d/4 synthetic_phrase_match=%t",
		time.Since(start).Milliseconds(), stats.Acknowledged, stats.AudioFrames, stats.SignalFrames,
		len(pcm), stats.TranscriptFragments, matchedWords, match)
	if previewErr != nil || !stats.Acknowledged || stats.SignalFrames == 0 || matchedWords < 2 {
		t.Fatalf("synthetic preview did not provide correlated speech evidence: %v", previewErr)
	}
}
