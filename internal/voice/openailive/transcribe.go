package openailive

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/Nathandela/swarm/internal/jsontext"
)

const (
	transcriptionURL      = "https://api.openai.com/v1/audio/transcriptions"
	MaxUtteranceBytes     = sampleRate * 2 * 120
	transcriptionMaxReply = 128 << 10
	transcriptionMaxText  = 64 << 10
)

// Transcribe finalizes one utterance of mono, 24 kHz, signed 16-bit little-endian PCM.
// Language may be empty or "any" to let OpenAI detect the language.
func Transcribe(ctx context.Context, key string, pcm []byte, language string) (string, error) {
	return transcribe(ctx, key, pcm, language, transcriptionURL)
}

func transcribe(ctx context.Context, key string, pcm []byte, language, endpoint string) (string, error) {
	if len(pcm) == 0 || len(pcm)%2 != 0 || len(pcm) > MaxUtteranceBytes ||
		len(key) == 0 || len(key) > 4096 || strings.IndexFunc(key, func(r rune) bool { return r < 33 || r > 126 }) >= 0 || !validTranscriptionLanguage(language) {
		return "", errors.New("invalid transcription input")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return "", err
	}

	body := bytes.NewBuffer(make([]byte, 0, len(pcm)+1024))
	form := multipart.NewWriter(body)
	if err := form.WriteField("model", "gpt-transcribe"); err != nil {
		return "", errors.New("could not encode transcription request")
	}
	if language != "" && language != "any" {
		if err := form.WriteField("languages[]", language); err != nil {
			return "", errors.New("could not encode transcription language")
		}
	}
	file, err := form.CreateFormFile("file", "utterance.wav")
	if err != nil {
		return "", errors.New("could not encode transcription file")
	}
	if err := writeTranscriptionWAV(file, pcm); err != nil {
		return "", errors.New("could not encode transcription audio")
	}
	if err := form.Close(); err != nil {
		return "", errors.New("could not finish transcription request")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return "", errors.New("could not construct transcription request")
	}
	req.GetBody = nil
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", form.FormDataContentType())
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	res, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.New("transcription request failed")
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("transcription request returned HTTP %d", res.StatusCode)
	}

	raw, err := io.ReadAll(io.LimitReader(res.Body, transcriptionMaxReply+1))
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.New("transcription response could not be read")
	}
	var reply struct {
		Text *string `json:"text"`
	}
	if len(raw) > transcriptionMaxReply || jsontext.ValidateUnicode(raw) != nil || json.Unmarshal(raw, &reply) != nil || reply.Text == nil || len(*reply.Text) > transcriptionMaxText {
		return "", errors.New("invalid transcription response")
	}
	return *reply.Text, nil
}

func validTranscriptionLanguage(language string) bool {
	switch language {
	case "", "any", "zh-cn", "zh-tw", "zh-hk":
		return true
	}
	if len(language) != 2 && len(language) != 3 {
		return false
	}
	for _, letter := range language {
		if letter < 'a' || letter > 'z' {
			return false
		}
	}
	return true
}

func writeTranscriptionWAV(w io.Writer, pcm []byte) error {
	var header [44]byte
	le := binary.LittleEndian
	copy(header[:], "RIFF")
	le.PutUint32(header[4:], uint32(36+len(pcm)))
	copy(header[8:], "WAVEfmt ")
	le.PutUint32(header[16:], 16) // PCM format chunk size.
	le.PutUint16(header[20:], 1)  // PCM format.
	le.PutUint16(header[22:], 1)  // Mono.
	le.PutUint32(header[24:], sampleRate)
	le.PutUint32(header[28:], sampleRate*2)
	le.PutUint16(header[32:], 2)  // Bytes per sample frame.
	le.PutUint16(header[34:], 16) // Bits per sample.
	copy(header[36:], "data")
	le.PutUint32(header[40:], uint32(len(pcm)))
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	_, err := w.Write(pcm)
	return err
}
