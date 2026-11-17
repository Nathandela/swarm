package voicepanel

import (
	"context"
	"errors"
	"net/http"

	"github.com/Nathandela/swarm/internal/voice"
	"github.com/Nathandela/swarm/internal/voiceclient"
	"github.com/Nathandela/swarm/internal/voicewire"
)

// ServeDevice uses the same loopback browser boundary as the local manager panel.
func ServeDevice(ctx context.Context, client *voiceclient.DeviceClient, grant voicewire.DeviceCredential, alias string, ready func(string) error) error {
	return serve(ctx, deviceManager{client: client, grant: grant, alias: alias}, true, ready)
}

type deviceManager struct {
	client *voiceclient.DeviceClient
	grant  voicewire.DeviceCredential
	alias  string
}

func (m deviceManager) Status(ctx context.Context) (voice.ManagerStatus, error) {
	s, err := m.client.Status(ctx, m.grant)
	return voice.ManagerStatus{
		Ready: s.Ready, Binding: voice.ManagerBinding{ConversationID: s.ConversationID, Provider: s.Provider},
		Attention: "SSH · " + m.alias + " · Audio preview unavailable",
	}, deviceError(err)
}

func (m deviceManager) SubmitText(ctx context.Context, id, text string) (voice.Admission, error) {
	r, err := m.client.SubmitText(ctx, m.grant, id, text)
	return deviceAdmission(r), deviceError(err)
}

func (m deviceManager) Receipt(ctx context.Context, id string) (voice.Admission, error) {
	r, err := m.client.Receipt(ctx, m.grant, id)
	return deviceAdmission(r), deviceError(err)
}

func (m deviceManager) BeginPTT(ctx context.Context, id string) (voice.PTTCapture, error) {
	r, err := m.client.BeginPTT(ctx, m.grant, id)
	return voice.PTTCapture{ID: r.ID, Phase: r.Phase, Admission: r.Admission}, deviceError(err)
}

func (m deviceManager) FinalizePTT(ctx context.Context, id string, pcm []byte) (voice.Admission, error) {
	r, err := m.client.FinalizePTT(ctx, m.grant, id, pcm)
	return deviceAdmission(r), deviceError(err)
}

func (m deviceManager) Publications(ctx context.Context, id string) ([]voice.Publication, error) {
	items, err := m.client.Publications(ctx, m.grant, id)
	if err != nil {
		return nil, deviceError(err)
	}
	results := make([]voice.Publication, len(items))
	for i, p := range items {
		results[i] = voice.Publication{ID: p.ID, Sequence: p.Sequence, Text: p.Text, Final: p.Final}
	}
	return results, nil
}

func (m deviceManager) PreviewPublication(context.Context, string, string) ([]byte, error) {
	return nil, voice.ErrPreviewUnavailable
}

func deviceAdmission(r voicewire.DeviceAdmission) voice.Admission {
	return voice.Admission{Sequence: r.Sequence, Phase: r.Phase}
}

func deviceError(err error) error {
	var status *voiceclient.DeviceHTTPError
	if !errors.As(err, &status) {
		return err
	}
	switch status.StatusCode {
	case http.StatusBadRequest:
		return voice.ErrInvalid
	case http.StatusNotFound:
		return voice.ErrNotFound
	case http.StatusConflict:
		return voice.ErrConflict
	case http.StatusGone:
		return voice.ErrExpired
	case http.StatusTooEarly:
		return voice.ErrPTTUnconfirmed
	case http.StatusFailedDependency:
		return voice.ErrPTTUnavailable
	default:
		return err
	}
}
