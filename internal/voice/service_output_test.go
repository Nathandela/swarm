package voice

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/voice/openailive"
)

type previewFixture struct {
	spoken       chan<- string
	events       []openailive.Event
	clock        chan time.Time
	waitForClock bool
}

func (p *previewFixture) Greet(ctx context.Context, text string) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case p.spoken <- text:
		return "fixture-greeting", nil
	}
}
func (p *previewFixture) SendAudio(ctx context.Context, pcm []byte) error {
	if len(pcm) != previewClockFrameBytes || previewHasSignal(pcm) {
		return errors.New("invalid preview silence")
	}
	if p.clock != nil {
		select {
		case p.clock <- time.Now():
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return ctx.Err()
}

func (p *previewFixture) Next(ctx context.Context) (openailive.Event, error) {
	if len(p.events) == 0 {
		<-ctx.Done()
		return openailive.Event{}, ctx.Err()
	}
	event := p.events[0]
	if p.waitForClock && event.Type == "session.output_audio.delta" {
		for range 2 {
			select {
			case <-p.clock:
			case <-ctx.Done():
				return openailive.Event{}, ctx.Err()
			}
		}
		p.waitForClock = false
	}
	p.events = p.events[1:]
	return event, nil
}
func (*previewFixture) Close() error { return nil }

func TestOwnerPreviewSupersedesPriorGeneration(t *testing.T) {
	var preview ownerPreview
	first, finishFirst := preview.begin(context.Background())
	defer finishFirst()
	second, finishSecond := preview.begin(context.Background())
	defer finishSecond()
	if !errors.Is(first.Err(), context.Canceled) || second.Err() != nil {
		t.Fatal("new preview did not cancel precisely the prior generation")
	}
	finishFirst()
	third, finishThird := preview.begin(context.Background())
	defer finishThird()
	if !errors.Is(second.Err(), context.Canceled) || third.Err() != nil {
		t.Fatal("stale cleanup cleared the active generation")
	}
	finishThird()
	if !errors.Is(third.Err(), context.Canceled) {
		t.Fatal("ending a preview did not cancel its provider context")
	}
}

func TestOwnerPreviewAuthorizesPreviouslySilentPublication(t *testing.T) {
	controller, store, runtime, binding := newControllerFixture(t)
	ctx := context.Background()
	const id, turn = "owner-preview", "native-turn"
	if _, err := controller.SubmitText(Utterance{ID: id, DeviceID: "fixture", VoiceSessionID: "fixture", MediaEpoch: 1}, "go"); err != nil {
		t.Fatal(err)
	}
	if err := controller.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	nonce := runtime.sent[0].ManagerAdmission.Nonce
	runtime.status.ReadyForInput = false
	runtime.status.Turn = protocol.ManagerNativeTurn{State: "running", NativeInputID: nonce, NativeTurnID: turn}
	runtime.proof = protocol.ManagerCallProof{Main: true, NativeInputID: nonce, NativeTurnID: turn, NativeCallID: "native-call"}
	if err := store.RevokeSpeech(binding, turn); err != nil {
		t.Fatal(err)
	}
	result, err := controller.ManagerCall(ctx, ManagerToolCall{Name: "publish", Arguments: json.RawMessage(`{"output_id":"silent","sequence":1,"text":"Intentional preview only.","final":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	var publication Publication
	if err := json.Unmarshal(result, &publication); err != nil || publication.Speak || publication.SpeechEpoch != 0 {
		t.Fatalf("publication should be text-only: %+v, %v", publication, err)
	}
	runtime.status.Turn.State = "completed"
	if err := controller.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	control := &managerControl{controller: controller, store: store, binding: binding}
	if got, err := control.previewPublication(previewRequest{UtteranceID: id, PublicationID: publication.ID}); err != nil || got != publication {
		t.Fatalf("deliberate owner preview could not authorize silent publication: %+v, %v", got, err)
	}
	if err := store.noteControllerCustodyLost(binding); err != nil {
		t.Fatal(err)
	}
	if _, err := control.previewPublication(previewRequest{UtteranceID: id, PublicationID: publication.ID}); !errors.Is(err, ErrStale) {
		t.Fatalf("preview after custody loss = %v", err)
	}
}

func TestPreviewPCMRequiresClockAndAudibleAudio(t *testing.T) {
	for _, tc := range []struct {
		name      string
		output    []byte
		wantAudio bool
	}{
		{"clocked speech", []byte{0, 4, 0, 9}, true},
		{"silence and transcript alone", []byte{0, 0, 1, 0}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := make(chan time.Time, 10)
			fixture := &previewFixture{spoken: make(chan string, 1), clock: clock, waitForClock: true, events: []openailive.Event{
				{Type: "session.commentary.appended", ClientEventID: "fixture-greeting"},
				{Type: "session.output_transcript.delta", Text: "Even a caption does not prove audible output."},
				{Type: "session.output_audio.delta", Audio: tc.output},
				{Type: "session.closed"},
			}}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			start := time.Now()
			audio, err := previewPCM(ctx, fixture, "Canonical manager commentary.", nil)
			if time.Since(start) < previewClockInterval/2 {
				t.Fatal("fixture spoke before the paced input advanced")
			}
			if tc.wantAudio {
				if err != nil || len(audio) != len(tc.output) {
					t.Fatalf("clocked preview = %v, %v", audio, err)
				}
			} else if !errors.Is(err, ErrPreviewUnavailable) || len(audio) != 0 {
				t.Fatalf("silence returned as confirmed preview: %v, %v", audio, err)
			}
		})
	}
}

func TestPreviewPCMDoesNotReturnAudioAfterCancellation(t *testing.T) {
	fixture := &previewFixture{spoken: make(chan string, 1), clock: make(chan time.Time, 10), events: []openailive.Event{
		{Type: "session.commentary.appended", ClientEventID: "fixture-greeting"},
		{Type: "session.output_audio.delta", Audio: []byte{0, 4}},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		audio, err := previewPCM(ctx, fixture, "Canonical manager commentary.", nil)
		if len(audio) != 0 {
			err = errors.New("canceled preview returned PCM")
		}
		result <- err
	}()
	select {
	case <-fixture.clock:
		cancel()
	case <-time.After(time.Second):
		t.Fatal("preview clock never started")
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrPreviewUnavailable) {
			t.Fatalf("canceled preview returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("preview clock failed to stop")
	}
}
