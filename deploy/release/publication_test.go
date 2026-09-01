package release_test

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type step struct {
	ID   string         `yaml:"id"`
	Uses string         `yaml:"uses"`
	Run  string         `yaml:"run"`
	With map[string]any `yaml:"with"`
}

type job struct {
	Permissions map[string]string `yaml:"permissions"`
	Steps       []step            `yaml:"steps"`
}

type workflow struct {
	Permissions map[string]string `yaml:"permissions"`
	Jobs        map[string]job    `yaml:"jobs"`
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func parseWorkflow(t *testing.T, path string) workflow {
	t.Helper()
	var got workflow
	if err := yaml.Unmarshal([]byte(read(t, path)), &got); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return got
}

func input(s step, name string) string {
	return fmt.Sprint(s.With[name])
}

func TestReleasePublishesOnlyImmutableTagImagesWithProvenance(t *testing.T) {
	const path = "../../.github/workflows/release.yml"
	raw := read(t, path)
	wf := parseWorkflow(t, path)

	if strings.Contains(strings.ToLower(raw), ":latest") {
		t.Fatal("release workflow must never publish a mutable latest tag")
	}
	if strings.Contains(raw, "pull_request:") || !strings.Contains(raw, "tags:") {
		t.Fatal("release publication must be reachable only from a tag-triggered workflow")
	}
	if wf.Permissions["packages"] == "write" {
		t.Fatal("packages: write must not be granted workflow-wide")
	}
	if len(wf.Permissions) != 1 || wf.Permissions["contents"] != "read" {
		t.Fatalf("release workflow default permissions = %#v, want only contents:read", wf.Permissions)
	}

	publish, ok := wf.Jobs["publish_containers"]
	if !ok {
		t.Fatal("release workflow has no publish_containers job")
	}
	for permission, want := range map[string]string{
		"contents":     "read",
		"packages":     "write",
		"id-token":     "write",
		"attestations": "write",
	} {
		if got := publish.Permissions[permission]; got != want {
			t.Errorf("publish_containers permission %s = %q, want %q", permission, got, want)
		}
	}
	for name, j := range wf.Jobs {
		if name != "publish_containers" && j.Permissions["packages"] == "write" {
			t.Errorf("job %s has packages: write; only publish_containers may push images", name)
		}
	}

	wantBuilds := map[string]string{
		"relay_image":  "deploy/relay/Dockerfile",
		"pushgw_image": "deploy/pushgw/Dockerfile",
	}
	builds := make(map[string]step)
	attestations := make(map[string]bool)
	hasLogin, hasExactTagGuard, hasManifest := false, false, false
	for _, s := range publish.Steps {
		switch {
		case strings.HasPrefix(s.Uses, "docker/login-action"):
			hasLogin = true
		case strings.HasPrefix(s.Uses, "docker/build-push-action"):
			builds[s.ID] = s
		case strings.HasPrefix(s.Uses, "actions/attest-build-provenance"):
			attestations[input(s, "subject-name")] = strings.Contains(input(s, "subject-digest"), ".outputs.digest") && input(s, "push-to-registry") == "true"
		}
		if strings.Contains(s.Run, `^v[0-9]+\.[0-9]+\.[0-9]+$`) {
			hasExactTagGuard = true
		}
		if strings.Contains(s.Run, "container-images.json") && strings.Contains(s.Run, "@${{") {
			hasManifest = true
		}
	}
	if !hasLogin || !hasExactTagGuard || !hasManifest {
		t.Errorf("release publication wiring: login=%v exact-tag-guard=%v digest-manifest=%v", hasLogin, hasExactTagGuard, hasManifest)
	}
	for id, dockerfile := range wantBuilds {
		build, ok := builds[id]
		if !ok {
			t.Errorf("missing build-push step id %q", id)
			continue
		}
		if got := input(build, "file"); got != dockerfile {
			t.Errorf("%s file = %q, want %q", id, got, dockerfile)
		}
		if input(build, "push") != "true" {
			t.Errorf("%s does not push its release image", id)
		}
		if got := input(build, "platforms"); got != "linux/amd64,linux/arm64" {
			t.Errorf("%s platforms = %q", id, got)
		}
		tags := input(build, "tags")
		if !strings.Contains(tags, "${{ github.ref_name }}") || strings.Contains(strings.ToLower(tags), "latest") || strings.Contains(tags, "\n") {
			t.Errorf("%s tags = %q; want exactly the release ref and no aliases", id, tags)
		}
		image := "ghcr.io/nathandela/swarm-" + strings.TrimSuffix(strings.TrimPrefix(id, "relay_"), "_image")
		if id == "relay_image" {
			image = "ghcr.io/nathandela/swarm-relay"
		}
		if !attestations[image] {
			t.Errorf("%s has no registry-pushed build provenance bound to its digest", image)
		}
	}
}

func TestPRContainerGatesCannotPublish(t *testing.T) {
	for _, path := range []string{
		"../../.github/workflows/relay-container.yml",
		"../../.github/workflows/pushgw-container.yml",
	} {
		raw := read(t, path)
		wf := parseWorkflow(t, path)
		if !strings.Contains(raw, "pull_request:") {
			t.Errorf("%s no longer runs on pull requests", path)
		}
		if strings.Contains(raw, "docker/login-action") || strings.Contains(raw, "push: true") {
			t.Errorf("%s may authenticate or push from an untrusted build/scan lane", path)
		}
		if wf.Permissions["packages"] == "write" {
			t.Errorf("%s grants packages: write at workflow scope", path)
		}
		for name, j := range wf.Jobs {
			if j.Permissions["packages"] == "write" {
				t.Errorf("%s job %s grants packages: write", path, name)
			}
		}
	}
}

func TestDigestManifestIsAttachedToTheDurableGitHubRelease(t *testing.T) {
	wf := parseWorkflow(t, "../../.github/workflows/release.yml")
	publish, ok := wf.Jobs["publish"]
	if !ok {
		t.Fatal("release workflow has no publish job")
	}

	download, goreleaser, upload := -1, -1, -1
	for i, s := range publish.Steps {
		switch {
		case strings.HasPrefix(s.Uses, "actions/download-artifact") && input(s, "name") == "container-images-${{ github.ref_name }}":
			download = i
		case strings.Contains(s.Run, "goreleaser release"):
			goreleaser = i
		case strings.Contains(s.Run, "gh release upload") && strings.Contains(s.Run, "container-images.json"):
			upload = i
		}
	}
	if download < 0 || goreleaser < 0 || upload < 0 {
		t.Fatalf("durable manifest wiring: download=%d goreleaser=%d release-upload=%d", download, goreleaser, upload)
	}
	if !(download < goreleaser && goreleaser < upload) {
		t.Fatalf("manifest ordering must be download -> create release -> attach manifest; got %d -> %d -> %d", download, goreleaser, upload)
	}
}
