package swarmmobile

import (
	"context"
	"encoding/base64"
	"sync/atomic"
	"time"

	"github.com/Nathandela/swarm/internal/voiceclient"
	"github.com/Nathandela/swarm/internal/voicewire"
)

// VoicePreview is one explicit request for one publication's preview. It
// returns bounded Base64 mono 24 kHz PCM16LE, without a playback claim.
type VoicePreview struct {
	client                 *voiceclient.DeviceClient
	ctx                    context.Context
	cancel                 context.CancelFunc
	grant                  voicewire.DeviceCredential
	utterance, publication string
	started                atomic.Bool
	binding                atomic.Pointer[voicewire.DeviceStatus]
}

// PreviewPublication prepares an exact signed preview. ReadPCM performs the
// network request. Stop cancels it without ending the whole device connection.
func (d *VoiceDevice) PreviewPublication(grantJSON, utteranceID, publicationID string) (preview *VoicePreview, err error) {
	defer barrier(&err)
	if d == nil || d.client == nil || d.lifetime.Err() != nil {
		return nil, errVoiceEnded
	}
	grant, err := d.decodeGrant(grantJSON)
	if err != nil || !voiceIdentity(utteranceID) || !voiceIdentity(publicationID) {
		return nil, errVoiceInput
	}
	ctx, cancel := context.WithCancel(d.lifetime)
	return &VoicePreview{client: d.client, ctx: ctx, cancel: cancel, grant: grant, utterance: utteranceID, publication: publicationID}, nil
}

// ReadPCM fetches at most six seconds of audio once. Call it off the UI thread.
func (p *VoicePreview) ReadPCM() (pcmBase64 string, err error) {
	defer barrier(&err)
	if p == nil || p.client == nil || p.ctx.Err() != nil || !p.started.CompareAndSwap(false, true) {
		return "", errVoiceEnded
	}
	ctx, cancel := context.WithTimeout(p.ctx, 28*time.Second)
	defer cancel()
	pcm, err := p.client.PreviewPublication(ctx, p.grant, p.utterance, p.publication)
	defer clear(pcm)
	if ctx.Err() != nil {
		return "", errVoiceEnded
	}
	if err != nil || len(pcm) == 0 || len(pcm)%2 != 0 || len(pcm) > voicewire.MaxPreviewPCMBytes {
		return "", errVoiceUnavailable
	}
	text := base64.StdEncoding.EncodeToString(pcm)
	if ctx.Err() != nil {
		return "", errVoiceEnded
	}
	return text, nil
}

// Verify checks the exact signed device scope before and during local playback.
// The first successful check fences subsequent checks to the same place and conversation.
func (p *VoicePreview) Verify() (err error) {
	defer barrier(&err)
	if p == nil || p.client == nil || p.ctx.Err() != nil {
		return errVoiceEnded
	}
	ctx, cancel := context.WithTimeout(p.ctx, 900*time.Millisecond)
	defer cancel()
	current, err := p.client.Status(ctx, p.grant)
	if err != nil || current.CustodyLost || current.Generation == 0 {
		return errVoiceUnavailable
	}
	p.binding.CompareAndSwap(nil, &current)
	expected := p.binding.Load()
	if p.ctx.Err() != nil || current.PlaceID != expected.PlaceID ||
		current.ConversationID != expected.ConversationID || current.Provider != expected.Provider ||
		current.Generation != expected.Generation {
		return errVoiceUnavailable
	}
	return nil
}

// Stop cancels this preview, including one that has not started. The platform
// must separately stop and flush its own audio sink.
func (p *VoicePreview) Stop() (err error) {
	defer barrier(&err)
	if p == nil || p.cancel == nil {
		return errVoiceEnded
	}
	p.cancel()
	return nil
}
