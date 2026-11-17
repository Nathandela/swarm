package voice

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/Nathandela/swarm/internal/voice/openailive"
	"github.com/Nathandela/swarm/internal/voicewire"
)

type devicePreviewSlot struct {
	ctx    context.Context
	cancel context.CancelFunc
}

func (r *deviceRegistry) invalidatePreviews(next deviceRegistryState) {
	for id, preview := range r.previews {
		previous, current := r.state.Devices[id], next.Devices[id]
		if current.TokenHash == "" || current.TokenHash != previous.TokenHash || current.Generation != previous.Generation {
			preview.cancel()
			delete(r.previews, id)
		}
	}
}

func (r *deviceRegistry) startPreview(ctx context.Context, request voicewire.DeviceRequest, verify func(string) error) (*devicePreviewSlot, func(), error) {
	var slot *devicePreviewSlot
	id := request.Proof.Credential.DeviceID
	err := r.authorize(request.Proof, "preview", request.Body, func(device string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := verify(device); err != nil {
			return err
		}
		if old := r.previews[device]; old != nil {
			old.cancel()
		}
		if r.previews == nil {
			r.previews = make(map[string]*devicePreviewSlot)
		}
		lifetime, cancel := context.WithTimeout(ctx, previewTimeout)
		slot = &devicePreviewSlot{ctx: lifetime, cancel: cancel}
		r.previews[device] = slot
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return slot, func() {
		slot.cancel()
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.previews[id] == slot {
			delete(r.previews, id)
		}
	}, nil
}

func (c *managerControl) serveDevicePreview(w http.ResponseWriter, r *http.Request) {
	sendError := func(err error) {
		switch {
		case errors.Is(err, errDeviceDenied):
			w.WriteHeader(http.StatusForbidden)
		case errors.Is(err, ErrPreviewUnavailable):
			w.WriteHeader(http.StatusFailedDependency)
		default:
			w.WriteHeader(managerControlErrorStatus(err))
		}
	}
	var request voicewire.DeviceRequest
	var req voicewire.DevicePreviewRequest
	if readDeviceJSON(r, &request, "proof", "body") != nil ||
		validateObjectMembers(request.Body, "utterance_id", "publication_id") != nil ||
		json.Unmarshal(request.Body, &req) != nil || !configText(req.UtteranceID, 128) || !configText(req.PublicationID, 128) {
		sendError(ErrInvalid)
		return
	}
	if c.preview.open == nil || c.controller == nil {
		sendError(ErrPreviewUnavailable)
		return
	}
	credential := request.Proof.Credential
	load := func(device string) (Publication, error) {
		receipt, state, err := c.deviceReceipt(device, req.UtteranceID)
		if err != nil {
			return Publication{}, err
		}
		return c.verifyPublication(state, receipt, req.PublicationID)
	}
	var selected Publication
	var greeting string
	slot, done, err := c.devices.startPreview(r.Context(), request, func(device string) error {
		var err error
		selected, err = load(device)
		if err != nil {
			return err
		}
		greeting = previewGreeting(selected.Text, c.preview.language)
		if len(greeting) > openailive.MaxCommentaryBytes {
			return ErrCapacity
		}
		return nil
	})
	if err != nil {
		sendError(err)
		return
	}
	defer done()
	verify := func() (Publication, error) {
		var current Publication
		err := c.devices.commitIfGranted(credential, func() error {
			if slot.ctx.Err() != nil || c.devices.previews[credential.DeviceID] != slot {
				return ErrStale
			}
			var err error
			current, err = load(credential.DeviceID)
			return err
		})
		return current, err
	}
	pcm, err := c.collectPreview(slot.ctx, selected, greeting, verify)
	// Recheck even when the provider returns an error so revoked/superseded
	// generations cannot be mistaken for a provider-side failure.
	current, fenceErr := verify()
	if fenceErr != nil {
		sendError(fenceErr)
		return
	}
	if current != selected || slot.ctx.Err() != nil {
		sendError(ErrStale)
		return
	}
	if err != nil {
		sendError(err)
		return
	}
	writer := http.NewResponseController(w)
	stopWrite := context.AfterFunc(slot.ctx, func() { _ = writer.SetWriteDeadline(time.Now()) })
	defer stopWrite()
	if slot.ctx.Err() != nil {
		sendError(ErrStale)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(pcm)))
	_, _ = w.Write(pcm)
}
