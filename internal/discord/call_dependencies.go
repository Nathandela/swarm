package discord

import (
	"context"

	"github.com/Nathandela/swarm/internal/voice/callspeech"
)

type dispatcherCallSpeech interface {
	Ready() <-chan struct{}
	Done() <-chan struct{}
	Close()
	Acknowledge() error
	Push(string, []byte) error
	PublishForTurn(string, string) error
}

type dispatcherCallDependencies struct {
	newSpeech func(context.Context, callspeech.Config) dispatcherCallSpeech
	runMedia  func(context.Context, string, string, string, string, []string, <-chan any, func(callMediaEvent) error) error
}

func defaultDispatcherCallDependencies() dispatcherCallDependencies {
	return dispatcherCallDependencies{
		newSpeech: func(ctx context.Context, cfg callspeech.Config) dispatcherCallSpeech {
			return callspeech.New(ctx, cfg)
		},
		runMedia: runCallMedia,
	}
}
