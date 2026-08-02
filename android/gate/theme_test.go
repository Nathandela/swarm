package gate

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// PB-TOK-4 -- "The Android app does not follow the system uiMode. Owned by S13:
// a test asserts the app theme does not inherit a DayNight/system-mode parent."
//
// This requirement exists because it was previously homeless: PB-TOK-2's second
// criterion was an assertion about the APP while S5 owns only the token source,
// so a DayNight parent could ship with no test failing anywhere. §5 defers light
// mode to Phase C, which means a system-light handset must not render the app
// unstyled or low-contrast.
//
// There are three independent ways the system mode gets in, and blocking one
// leaves the others open:
//
//  1. a DayNight theme parent, which resolves to Light under notnight;
//  2. a values-night/ (or any -night qualified) resource directory;
//  3. AppCompatDelegate's default night mode, which is MODE_NIGHT_FOLLOW_SYSTEM
//     unless the app sets otherwise -- this one leaves no trace in the theme at
//     all, and is asserted behaviourally in ThemeNightModeTest.kt.
//
// (1) and (2) are structural and are asserted here. Neither this file nor the
// Kotlin test is sufficient alone: an app that overrides every colour attribute
// would keep a DayNight parent and still resolve identically under both
// qualifiers, so the structural check catches what the behavioural one cannot,
// and vice versa.

func resDir(t *testing.T) string {
	return filepath.Join(appModule(t), "src", "main", "res")
}

var dayNightParent = regexp.MustCompile(`parent\s*=\s*"[^"]*DayNight[^"]*"`)

// TestPBTOK4_NoThemeInheritsADayNightParent walks every values*/ XML rather than
// one known filename: a DayNight parent in styles.xml is the same defect as one
// in themes.xml.
func TestPBTOK4_NoThemeInheritsADayNightParent(t *testing.T) {
	root := resDir(t)
	files := xmlFilesUnder(root)
	if len(files) == 0 {
		t.Fatalf("PB-TOK-4: no resource XML under %s. The check would pass vacuously; "+
			"the Android module does not exist yet", mustRel(t, root))
	}

	sawTheme := false
	for _, f := range files {
		body, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		s := string(body)
		if strings.Contains(s, "<style") {
			sawTheme = true
		}
		if m := dayNightParent.FindString(s); m != "" {
			t.Errorf("PB-TOK-4: %s declares %s. A DayNight parent resolves to the LIGHT "+
				"variant on a system-light handset, and §5 defers light mode -- so the app "+
				"would render unstyled or low-contrast on exactly the configuration nobody "+
				"tests on", mustRel(t, f), m)
		}
	}
	if !sawTheme {
		t.Fatalf("PB-TOK-4: no <style> element found anywhere under %s; there is no theme "+
			"to assert about", mustRel(t, root))
	}
}

// TestPBTOK4_NoNightQualifiedResources. A -night resource directory is the
// system uiMode by another name, and it works even with a non-DayNight parent.
func TestPBTOK4_NoNightQualifiedResources(t *testing.T) {
	root := resDir(t)
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("PB-TOK-4: cannot read %s: %v", mustRel(t, root), err)
	}
	if len(entries) == 0 {
		t.Fatalf("PB-TOK-4: %s is empty", mustRel(t, root))
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		for _, part := range strings.Split(e.Name(), "-") {
			if part == "night" {
				t.Errorf("PB-TOK-4: %s is a -night qualified resource directory. The app "+
					"must not vary with the system uiMode at all", filepath.Join("res", e.Name()))
			}
		}
	}
}

// TestPBTOK4_ManifestDoesNotOptIntoSystemDarkening. android:forceDarkAllowed
// hands the platform permission to re-tint a theme it was never given, which is
// the same failure with a different mechanism.
func TestPBTOK4_ManifestDoesNotOptIntoSystemDarkening(t *testing.T) {
	manifest := filepath.Join(appModule(t), "src", "main", "AndroidManifest.xml")
	body := readFileOrFail(t, manifest, "PB-TOK-4")
	if regexp.MustCompile(`forceDarkAllowed\s*=\s*"true"`).MatchString(body) {
		t.Errorf("PB-TOK-4: %s sets android:forceDarkAllowed=\"true\"", mustRel(t, manifest))
	}
	if !strings.Contains(body, "android:theme") {
		t.Errorf("PB-TOK-4: %s sets no android:theme, so the application inherits the "+
			"platform default -- which does follow the system uiMode", mustRel(t, manifest))
	}
}

func xmlFilesUnder(root string) []string {
	var out []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() && strings.HasSuffix(path, ".xml") {
			out = append(out, path)
		}
		return nil
	})
	return out
}
