package skeleton

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenCodeLiveHistoryNeverPublishesFailedPagination(t *testing.T) {
	f := newOpenCodeSendFixture(t)
	f.historyPagingFail.Store(true)
	meta, ok := f.d.core.Get(f.binding.WorkerID)
	if !ok {
		t.Fatal("native worker missing")
	}
	resolver := &openCodeHistoryResolver{core: f.d.core}
	path, outcome := resolver.LocateTranscript(meta, meta.ConversationID)
	if path != "" || outcome != resumeHistoryUnreadable {
		t.Errorf("failed pagination published partial history: path=%q outcome=%v", path, outcome)
	}
	files, err := os.ReadDir(filepath.Dir(f.binding.SocketPath))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasPrefix(file.Name(), "native-history-") {
			t.Error("partial history artifact survived")
		}
	}
}
