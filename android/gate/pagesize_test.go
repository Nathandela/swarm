package gate

// 16 KB PAGE SIZE (agents-tracker-m01a).
//
// Google Play REJECTS AT UPLOAD an app targeting API 35 or higher whose native libraries are
// not linked to support 16 KB pages -- in force since 2025-11-01
// (developer.android.com/guide/practices/page-sizes). The gate is "35 and higher", so this
// app has been subject to it since before the targetSdk 36 bump. android:pageSizeCompat is a
// device-side fallback with a user-visible warning dialog, not an upload exemption.
//
// CameraX's shipped .so files were already compliant. libgojni.so -- the one object in the
// APK that is ours, and the one that holds the session keys -- was not: llvm-readelf reported
// LOAD align 0x1000 for both arm64-v8a and x86_64. The cause is the NDK pin. NDK r28 links
// 16 KB aligned by default; android/toolchain.env pins r27, where the alignment has to be
// asked for, and android/build-aar.sh asked for nothing.
//
// TWO DIFFERENT PROPERTIES CARRY THE WORD "aligned" AND ONLY ONE OF THEM IS THIS ONE.
// `zipalign -c -P 16 4` measures the OFFSET at which the .so is stored inside the APK's zip.
// This measures the page size the shared object was LINKED for, which lives in its ELF
// program headers. Both are required, they are checked with different tools, and neither
// implies the other -- the zip offsets were already correct on the artifact whose segments
// were not, and a check that read only the first reported the app compliant.
//
// THE ASSERTION IS THEREFORE ON THE ARTIFACT, NOT ON THE BUILD COMMAND. A flag can sit in the
// command and never reach lld: -extldflags has to survive gomobile's forwarding, go build's
// splitting of -ldflags, and clang's -Wl translation, and getting any of that wrong produces
// an unchanged binary rather than an error. Only the linked bytes settle it.

import (
	"bytes"
	"debug/elf"
	"fmt"
	"regexp"
	"testing"
)

// androidPageSize is the page size Play requires native libraries to support. Segments may be
// aligned more coarsely -- NDK r28 emits 0x4000, and a 64 KB alignment also complies -- so the
// assertion is a floor rather than an equality.
const androidPageSize = 16384

// TestPageSize_TheBuiltAARsNativeLibrariesAreLinkedFor16KBPages is the gate: it reads the ELF
// program headers of every libgojni.so the artifact carries and requires each loadable segment
// to be aligned to at least 16 KB.
//
// It needs no NDK. llvm-readelf is how a person checks this by hand, but debug/elf reads the
// same p_align field, and every other assertion in this package runs on a plain runner with no
// Android SDK.
//
// IT SKIPS WHEN THERE IS NO AAR, for the reason PB-BIND-7's artifact half skips: the AAR is a
// gitignored build output, and a gate that went red for everyone who had not run build-aar.sh
// would be switched off within a week. TestPageSize_TheBuildCommandAsksTheLinkerFor16KBPages
// below is what those runners still get.
func TestPageSize_TheBuiltAARsNativeLibrariesAreLinkedFor16KBPages(t *testing.T) {
	path := builtAARPath(t)
	if !exists(path) {
		t.Skipf("16 KB PAGES: no built AAR at %s, so there are no linked segments to measure. "+
			"It is a build output and is gitignored; run android/build-aar.sh to give this "+
			"gate something to measure.", mustRel(t, path))
	}

	abis := declaredABIs(t, sourcePin(t, repoRoot(t)))
	if len(abis) == 0 {
		t.Fatalf("16 KB PAGES: SWARM_AAR_ABIS is empty, so this gate would measure no library " +
			"at all and pass vacuously")
	}

	for _, abi := range abis {
		entry := fmt.Sprintf("jni/%s/libgojni.so", abi)
		raw := zipEntry(t, path, entry, "16 KB PAGES")
		obj, err := elf.NewFile(bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("16 KB PAGES: %s in %s is not a readable ELF object: %v",
				entry, mustRel(t, path), err)
		}

		var loads int
		for _, seg := range obj.Progs {
			if seg.Type != elf.PT_LOAD {
				continue
			}
			loads++
			if seg.Align < androidPageSize {
				t.Errorf("16 KB PAGES: %s in %s has a LOAD segment aligned to 0x%x, below the "+
					"0x%x Play requires of an app targeting API 35 or higher. The upload is "+
					"REJECTED, not warned. android/build-aar.sh must pass "+
					"-extldflags=-Wl,-z,max-page-size=%d through gomobile's -ldflags; if it "+
					"already does, the flag is not reaching lld and the NDK pin has to move to "+
					"r28 or later, where 16 KB is the default.",
					entry, mustRel(t, path), seg.Align, androidPageSize, androidPageSize)
			}
		}
		if loads == 0 {
			t.Errorf("16 KB PAGES: %s in %s declares no PT_LOAD segments, so the check above "+
				"measured nothing", entry, mustRel(t, path))
		}
	}
}

// TestPageSize_TheBuildCommandAsksTheLinkerFor16KBPages runs everywhere, including on the
// runners where the gate above has no artifact and skips. It cannot tell whether the flag
// reaches the linker -- nothing that reads the script can -- but it does catch the flag being
// dropped, which on those machines nothing else would see until Play refused the upload.
func TestPageSize_TheBuildCommandAsksTheLinkerFor16KBPages(t *testing.T) {
	src := readFileOrFail(t, buildAARScript(t), "16 KB PAGES")

	want := regexp.MustCompile(`-extldflags=[^\s"]*max-page-size=(\d+)`)
	m := want.FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("16 KB PAGES: the AAR build command asks the external linker for no page "+
			"size, so libgojni.so is linked for the NDK's default. The pinned NDK (r27) "+
			"defaults to 4 KB, which Play rejects at upload for an app targeting API 35 or "+
			"higher. Pass -extldflags=-Wl,-z,max-page-size=%d inside gomobile's -ldflags.",
			androidPageSize)
	}
	if m[1] != fmt.Sprint(androidPageSize) {
		t.Errorf("16 KB PAGES: the AAR build command asks for max-page-size=%s; Play requires "+
			"support for %d-byte pages", m[1], androidPageSize)
	}
}
