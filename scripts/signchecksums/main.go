// signchecksums signs a goreleaser checksums.txt with the release's ed25519
// key (lifecycle plan R2): stdin-free, two flags, zero dependencies beyond the
// standard library -- the verifying half is internal/upgrade.VerifyChecksums,
// and the two share nothing but the key format, which is the point (a
// self-contained verifier must not import a signing stack).
//
// The private key arrives ONLY via the environment variable named by --key-env
// (the release workflow's SWARM_RELEASE_SIGNING_KEY secret), base64 of the
// 32-byte ed25519 seed. The signature file is base64 of the raw 64-byte
// signature, published beside checksums.txt as checksums.txt.sig.
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
)

func main() {
	keyEnv := flag.String("key-env", "SWARM_RELEASE_SIGNING_KEY", "environment variable holding the base64 ed25519 seed")
	in := flag.String("in", "", "checksums file to sign")
	out := flag.String("out", "", "signature file to write (default <in>.sig)")
	flag.Parse()
	if *in == "" {
		fatal("signchecksums: --in is required")
	}
	if *out == "" {
		*out = *in + ".sig"
	}
	seedB64 := os.Getenv(*keyEnv)
	if seedB64 == "" {
		fatal("signchecksums: %s is empty; refusing to publish an unsigned release", *keyEnv)
	}
	seed, err := base64.StdEncoding.DecodeString(seedB64)
	if err != nil || len(seed) != ed25519.SeedSize {
		fatal("signchecksums: %s is not a base64 %d-byte ed25519 seed", *keyEnv, ed25519.SeedSize)
	}
	data, err := os.ReadFile(*in)
	if err != nil {
		fatal("signchecksums: %v", err)
	}
	sig := ed25519.Sign(ed25519.NewKeyFromSeed(seed), data)
	if err := os.WriteFile(*out, []byte(base64.StdEncoding.EncodeToString(sig)+"\n"), 0o644); err != nil {
		fatal("signchecksums: %v", err)
	}
}

func fatal(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
