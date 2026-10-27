package tea

import (
	"io"
	"testing"
)

func TestRendererStopJoinsBeforeRestart(t *testing.T) {
	p := NewProgram(nil, WithOutput(io.Discard))
	p.renderer = nilRenderer{}
	p.stopRenderer(true)
	for i := 0; i < 40; i++ {
		p.startRenderer()
		stopped := p.rendererStopped
		p.stopRenderer(false)
		select {
		case <-stopped:
		default:
			t.Fatalf("renderer stop returned before goroutine exit, cycle %d", i)
		}
		p.stopRenderer(true)
	}
}
