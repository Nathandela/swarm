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
	return serve(ctx, deviceManager{client: client, grant: grant, alias: alias}, false, ready)
}

type deviceManager struct {
	client *voiceclient.DeviceClient
	grant  voicewire.DeviceCredential
	alias  string
}

func (m deviceManager) Status(ctx context.Context) (voice.ManagerStatus, error) {
	s, err := m.client.Status(ctx, m.grant)
	return voice.ManagerStatus{
		Ready: s.Ready, SchedulerPaused: s.SchedulerPaused, CustodyLost: s.CustodyLost, Binding: voice.ManagerBinding{ConversationID: s.ConversationID, Provider: s.Provider, Generation: s.Generation},
		Attention: "SSH · " + m.alias,
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

func (m deviceManager) PreviewPublication(ctx context.Context, id, publication string) ([]byte, error) {
	pcm, err := m.client.PreviewPublication(ctx, m.grant, id, publication)
	var status *voiceclient.DeviceHTTPError
	if errors.As(err, &status) && status.StatusCode == http.StatusFailedDependency {
		return nil, voice.ErrPreviewUnavailable
	}
	return pcm, deviceError(err)
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
	case http.StatusTooManyRequests:
		return voice.ErrCaptureBusy
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

func (m deviceManager) EndPTT(ctx context.Context, id string) error {
	return deviceError(m.client.EndPTT(ctx, m.grant, id))
}
