// Command swarm-publish uploads an Android App Bundle to Google Play and promotes it to a
// track, replacing the manual Console upload. It fails closed: every target -- the bundle,
// the credential, the applicationId, the track -- must be stated on the command line, since
// publishing to the wrong app or the wrong track is not undoable from this side.
//
// It uses only the standard library, by way of internal/play; the rationale for not taking
// gradle-play-publisher or google.golang.org/api is recorded in that package.
//
// NEVER RUN AGAINST A CREDENTIAL YOU DO NOT INTEND TO PUBLISH WITH. --dry-run performs
// every step except the commit, which is the safe way to prove the credential and the
// bundle before doing the irreversible part.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/Nathandela/swarm/internal/play"
	"github.com/Nathandela/swarm/internal/remote/push"
)

// tracks is the set Play accepts. Validated locally because Google's own rejection for a
// mistyped track arrives four API calls later, after an edit has been opened and a bundle
// uploaded.
var tracks = []string{"internal", "alpha", "beta", "production"}

// run parses argv and performs one publish.
func run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("swarm-publish", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	aab := fs.String("aab", "", "path to the .aab to upload (required)")
	key := fs.String("key", "", "path to the service-account JSON credential (required)")
	pkg := fs.String("package", "", "applicationId to publish, e.g. dev.swarm.phone (required)")
	pushGatewayURL := fs.String("push-gateway-url", "", "expected bare HTTPS Cloud Run push origin (required)")
	track := fs.String("track", "internal", "Play track: "+strings.Join(tracks, ", "))
	dryRun := fs.Bool("dry-run", false, "do everything except commit the edit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	for _, required := range []struct{ flag, value string }{
		{"--aab", *aab},
		{"--key", *key},
		{"--package", *pkg},
		{"--push-gateway-url", *pushGatewayURL},
	} {
		if required.value == "" {
			return fmt.Errorf("swarm-publish: %s is required", required.flag)
		}
	}
	if !validTrack(*track) {
		return fmt.Errorf("swarm-publish: unknown track %q; want one of %s", *track, strings.Join(tracks, ", "))
	}
	bundle, err := openVerifiedProductionFirebaseBundle(*aab, *pkg, *pushGatewayURL)
	if err != nil {
		return fmt.Errorf("swarm-publish: %w", err)
	}
	defer func() { _ = bundle.Close() }()

	// Read and validate the credential before touching the network. os.ReadFile and
	// LoadServiceAccount both report the PATH and the SHAPE of the problem, never the
	// file's contents -- a credential quoted into an error lands in the terminal
	// transcript and in CI logs.
	doc, err := os.ReadFile(*key)
	if err != nil {
		return fmt.Errorf("swarm-publish: read credential: %w", err)
	}
	acct, err := push.LoadServiceAccount(doc)
	if err != nil {
		return fmt.Errorf("swarm-publish: %w", err)
	}

	res, err := play.Publish(ctx, play.Config{
		Account: acct,
		Package: *pkg,
		Track:   *track,
		Bundle:  bundle,
		DryRun:  *dryRun,
	})
	if err != nil {
		return err
	}
	if res.Committed {
		fmt.Printf("swarm-publish: published version code %d of %s to the %s track (edit %s)\n",
			res.VersionCode, *pkg, *track, res.EditID)
		return nil
	}
	fmt.Printf("swarm-publish: DRY RUN -- uploaded version code %d of %s and staged the %s track, "+
		"then stopped before committing. Edit %s was never applied and expires on its own; "+
		"nothing was published. Re-run without --dry-run to publish.\n",
		res.VersionCode, *pkg, *track, res.EditID)
	return nil
}

func validTrack(track string) bool {
	for _, t := range tracks {
		if t == track {
			return true
		}
	}
	return false
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		// A duplicate version code is not a failure of this tool: the earlier upload
		// landed. Say so on the way out, where an operator staring at a red exit reads.
		if errors.Is(err, play.ErrDuplicateVersionCode) {
			fmt.Fprintln(os.Stderr, "swarm-publish: nothing was lost -- that bundle is already on Play.")
		}
		os.Exit(1)
	}
}
