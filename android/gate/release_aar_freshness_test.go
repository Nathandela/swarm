package gate

import (
	"strings"
	"testing"
)

// A version bump cannot make an old libgojni.so current. The release file
// dependency owns an always-run producer; debug remains an explicit local-build
// workflow.
func TestPlayReleaseAarDependencyHasAnAlwaysRunProducer(t *testing.T) {
	build := readFileOrFail(t, moduleBuildFile(t), "release AAR freshness")
	script := readFileOrFail(t, buildAARScript(t), "release AAR freshness")

	for _, want := range []string{
		`case "$#" in`,
		`0) out="$here/app/libs/swarm.aar" ;;`,
		`/*) out=$1 ;;`,
		`requires an absolute output path`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("release AAR freshness: android/build-aar.sh is missing %q", want)
		}
	}
	for _, want := range []string{
		`val releaseSwarmAar = layout.buildDirectory.file("generated/release-swarm-aar/swarm.aar")`,
		`tasks.register<Exec>("rebuildReleaseSwarmAar")`,
		`workingDir(rootProject.projectDir.parentFile)`,
		`commandLine("./android/build-aar.sh", releaseSwarmAar.get().asFile.absolutePath)`,
		`outputs.file(releaseSwarmAar)`,
		`outputs.upToDateWhen { false }`,
		`tasks.matching { it.name == "preDebugBuild" }.configureEach { dependsOn(requireSwarmAar) }`,
		`debugImplementation(files(swarmAar))`,
		`releaseImplementation(files(releaseSwarmAar).builtBy(rebuildReleaseSwarmAar))`,
	} {
		if !strings.Contains(build, want) {
			t.Errorf("release AAR freshness: android/app/build.gradle.kts is missing %q", want)
		}
	}
	if strings.Contains(build, `implementation(files(swarmAar))`) ||
		strings.Contains(build, `releaseImplementation(files(swarmAar))`) {
		t.Error("release AAR freshness: generic implementation dependency can resolve the stale AAR before its release producer")
	}
	if strings.Contains(build, `tasks.named("preBuild") { dependsOn(requireSwarmAar) }`) {
		t.Error("release AAR freshness: global preBuild can reject a clean release before its AAR producer runs")
	}
}
