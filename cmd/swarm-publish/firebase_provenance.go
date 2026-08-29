package main

// The Firebase provenance sidecar is the handoff between the operator-only
// Android build and this publisher. google-services.json stays gitignored and
// must not travel with the artifact; bundleRelease instead records the public
// Firebase identities and the exact AAB digest beside the signed bundle.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

const (
	firebaseProvenanceSuffix = ".swarm-firebase-provenance.json"
	firebaseProvenanceMax    = 4096

	productionFirebaseProjectID = "swarm-8404f"
	productionFirebasePackage   = "dev.swarm.phone"
	productionFirebaseAppID     = "1:733314021126:android:ff6e016cffe98782535087"
)

type firebaseProvenance struct {
	Schema         int    `json:"schema"`
	ProjectID      string `json:"project_id"`
	PackageName    string `json:"package_name"`
	MobileSDKAppID string `json:"mobilesdk_app_id"`
	AABSHA256      string `json:"aab_sha256"`
}

// openVerifiedProductionFirebaseBundle refuses any bundle that was not the
// exact output of a successful production-configured bundleRelease and returns
// the same open, rewound descriptor that internal/play uploads. It deliberately
// runs before credential parsing: a local artifact mistake must not read a Play
// key or open an edit on Google's side. Keeping the descriptor open also makes a
// later rename or replacement of aabPath unable to change the uploaded artifact.
func openVerifiedProductionFirebaseBundle(aabPath, packageName string) (_ *os.File, resultErr error) {
	if packageName != productionFirebasePackage {
		return nil, fmt.Errorf("Firebase provenance: --package=%q, want production package %q",
			packageName, productionFirebasePackage)
	}

	bundle, err := os.Open(aabPath)
	if err != nil {
		return nil, fmt.Errorf("Firebase provenance: open AAB: %w", err)
	}
	defer func() {
		if resultErr != nil {
			_ = bundle.Close()
		}
	}()
	info, err := bundle.Stat()
	if err != nil {
		return nil, fmt.Errorf("Firebase provenance: inspect AAB: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("Firebase provenance: AAB %q is not a regular file", aabPath)
	}

	provenancePath := aabPath + firebaseProvenanceSuffix
	f, err := os.Open(provenancePath)
	if err != nil {
		return nil, fmt.Errorf("Firebase provenance sidecar %q is missing or unreadable: %w; "+
			"rebuild with :app:bundleRelease and do not publish a stale AAB", provenancePath, err)
	}
	defer func() { _ = f.Close() }()

	raw, err := io.ReadAll(io.LimitReader(f, firebaseProvenanceMax+1))
	if err != nil {
		return nil, fmt.Errorf("Firebase provenance: read sidecar: %w", err)
	}
	if len(raw) > firebaseProvenanceMax {
		return nil, fmt.Errorf("Firebase provenance: sidecar exceeds %d bytes", firebaseProvenanceMax)
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var provenance firebaseProvenance
	if err := dec.Decode(&provenance); err != nil {
		return nil, fmt.Errorf("Firebase provenance: decode sidecar: %w", err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("Firebase provenance: sidecar contains multiple JSON documents")
		}
		return nil, fmt.Errorf("Firebase provenance: trailing sidecar data: %w", err)
	}

	for _, fact := range []struct {
		name, got, want string
	}{
		{"project_id", provenance.ProjectID, productionFirebaseProjectID},
		{"package_name", provenance.PackageName, productionFirebasePackage},
		{"mobilesdk_app_id", provenance.MobileSDKAppID, productionFirebaseAppID},
	} {
		if fact.got != fact.want {
			return nil, fmt.Errorf("Firebase provenance: %s=%q, want production value %q",
				fact.name, fact.got, fact.want)
		}
	}
	if provenance.Schema != 1 {
		return nil, fmt.Errorf("Firebase provenance: schema=%d, want 1", provenance.Schema)
	}

	digest, err := sha256AndRewind(bundle)
	if err != nil {
		return nil, fmt.Errorf("Firebase provenance: hash AAB: %w", err)
	}
	wantDigest := hex.EncodeToString(digest[:])
	if provenance.AABSHA256 != wantDigest {
		return nil, fmt.Errorf("Firebase provenance: AAB SHA-256 does not match sidecar; " +
			"the bundle is stale, replaced, or not the build the sidecar describes")
	}
	return bundle, nil
}

func sha256AndRewind(f *os.File) ([sha256.Size]byte, error) {
	var sum [sha256.Size]byte
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return sum, err
	}

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return sum, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return sum, err
	}
	copy(sum[:], h.Sum(nil))
	return sum, nil
}
