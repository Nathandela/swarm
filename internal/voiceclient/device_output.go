package voiceclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/Nathandela/swarm/internal/voicewire"
)

// PreviewPublication renders one explicitly selected publication as bounded
// mono 24 kHz PCM16LE. Cancel ctx to stop; callers must flush their local sink.
func (c *DeviceClient) PreviewPublication(ctx context.Context, credential voicewire.DeviceCredential, utteranceID, publicationID string) ([]byte, error) {
	if !validSSHField(utteranceID, 128) || !validSSHField(publicationID, 128) {
		return nil, errors.New("voice: invalid device preview target")
	}
	ctx, cancel := context.WithTimeout(ctx, 28*time.Second)
	defer cancel()
	body, err := json.Marshal(voicewire.DevicePreviewRequest{UtteranceID: utteranceID, PublicationID: publicationID})
	if err != nil {
		return nil, err
	}
	select {
	case c.gate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	proof, err := c.makeProof(ctx, credential, "preview", body)
	<-c.gate
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(voicewire.DeviceRequest{Proof: proof, Body: body})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://voice-device/v1/device/preview", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, &DeviceHTTPError{StatusCode: response.StatusCode}
	}
	if response.Header.Get("Content-Type") != "application/octet-stream" || response.ContentLength <= 0 ||
		response.ContentLength%2 != 0 || response.ContentLength > voicewire.MaxPreviewPCMBytes {
		return nil, errors.New("voice: invalid device preview response")
	}
	pcm, err := io.ReadAll(io.LimitReader(response.Body, voicewire.MaxPreviewPCMBytes+1))
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if int64(len(pcm)) != response.ContentLength {
		return nil, errors.New("voice: incomplete device preview response")
	}
	return pcm, nil
}
