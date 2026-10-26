package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Nathandela/swarm/internal/agyinput"
)

func runNativeInput(args []string, stderr io.Writer) int {
	if len(args) != 2 || args[0] != "register-agy" || !filepath.IsAbs(args[1]) || filepath.Base(args[1]) != agyinput.DescriptorFile {
		_, _ = fmt.Fprintln(stderr, "usage: swarm native-input register-agy <session descriptor>")
		return 2
	}
	desc, err := agyinput.ReadDescriptor(filepath.Dir(args[1]))
	source := agyinput.SourceFromEnv(os.Getenv)
	source.WorkingDirectory, _ = os.Getwd()
	if err != nil || source.SwarmSessionID != desc.SessionID || source.ConversationID == "" || source.CSRFToken == "" || source.SourceMetadata == "" {
		_, _ = fmt.Fprintf(stderr, "native input registration must run inside its own agy tool context (descriptor=%t, session=%t, native_identity=%t, native_auth=%t, native_source=%t)\n", err == nil, source.SwarmSessionID == desc.SessionID, source.ConversationID != "", source.CSRFToken != "", source.SourceMetadata != "")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := agyinput.NewClient(desc).Register(ctx, source); err != nil {
		_, _ = fmt.Fprintf(stderr, "native input registration could not prove this session; ask its owner to refresh or register again: %v\n", err)
		return 1
	}
	return 0
}
