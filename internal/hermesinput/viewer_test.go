package hermesinput

import (
	"reflect"
	"strings"
	"testing"
)

func TestViewerUsesQualifiedNativeTUIAndExactSession(t *testing.T) {
	got, err := ViewerArgv("hermes", "local-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"hermes", "chat", "--tui", "--resume", "local-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("viewer argv = %v", got)
	}
	for _, id := range []string{"", "local-short", "--resume", "20260829_140000_abcdef", "local-" + strings.Repeat("A", 64), "local-" + strings.Repeat("0", 63) + "/"} {
		if _, err := ViewerArgv("hermes", id); err == nil {
			t.Fatalf("native viewer accepted %q", id)
		}
	}
}
