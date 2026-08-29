package gate

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The launcher mark is generated artwork, not geometry to redraw in Android XML.
//
// docs/design/swarm-illustration-direction/swarm-atmospheric-mark.png is the approved transparent
// source. The Android resource is a byte-for-byte copy wrapped by a small drawable XML that scales
// it into the adaptive icon's mask-safe zone. This keeps the store icon, README identity and
// installed app on one artwork instead of approximating the soft trails with hard vector strokes.
const (
	approvedLauncherMarkRelPath = "docs/design/swarm-illustration-direction/swarm-atmospheric-mark.png"
	shippedLauncherMarkName     = "swarm_atmospheric_mark.png"
	launcherForegroundName      = "ic_launcher_foreground.xml"
	launcherInsetDP             = 24.0
	adaptiveCanvasDP            = 108.0
	guaranteedSafeRadiusDP      = 33.0
)

type launcherResourceElement struct {
	XMLName  xml.Name
	Attrs    []xml.Attr                `xml:",any,attr"`
	Children []launcherResourceElement `xml:",any"`
}

func (e launcherResourceElement) attr(name string) (string, bool) {
	for _, a := range e.Attrs {
		if a.Name.Local == name {
			return strings.TrimSpace(a.Value), true
		}
	}
	return "", false
}

func (e launcherResourceElement) childrenNamed(name string) []launcherResourceElement {
	var out []launcherResourceElement
	for _, child := range e.Children {
		if child.XMLName.Local == name {
			out = append(out, child)
		}
	}
	return out
}

func loadLauncherResource(t *testing.T, path string) launcherResourceElement {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read launcher resource %s: %v", mustRel(t, path), err)
	}
	var root launcherResourceElement
	if err := xml.Unmarshal(raw, &root); err != nil {
		t.Fatalf("%s is not well-formed XML: %v", mustRel(t, path), err)
	}
	return root
}

func launcherForegroundPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(appModule(t), "src", "main", "res", "drawable", launcherForegroundName)
}

func shippedLauncherMarkPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(appModule(t), "src", "main", "res", "drawable-nodpi", shippedLauncherMarkName)
}

func approvedLauncherMarkPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(repoRoot(t), filepath.FromSlash(approvedLauncherMarkRelPath))
}

func launcherMipmapDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(appModule(t), "src", "main", "res", "mipmap-anydpi-v26")
}

func requireLauncherAttr(t *testing.T, e launcherResourceElement, name, where string) string {
	t.Helper()
	value, ok := e.attr(name)
	if !ok {
		t.Fatalf("%s has no android:%s", where, name)
	}
	return value
}

func parseDP(t *testing.T, raw, where string) float64 {
	t.Helper()
	if !strings.HasSuffix(raw, "dp") {
		t.Fatalf("%s is %q, want an explicit dp value", where, raw)
	}
	value, err := strconv.ParseFloat(strings.TrimSuffix(raw, "dp"), 64)
	if err != nil {
		t.Fatalf("%s is %q, which is not a dp value: %v", where, raw, err)
	}
	return value
}

func decodeLauncherPNG(t *testing.T, path string) image.Image {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open launcher artwork %s: %v", mustRel(t, path), err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatalf("decode launcher artwork %s: %v", mustRel(t, path), err)
	}
	return img
}

// maxAlphaRadius returns the furthest non-transparent pixel as a fraction of the source width.
// Pixel centres are used so the calculation describes what the bitmap actually paints.
func maxAlphaRadius(t *testing.T, img image.Image) float64 {
	t.Helper()
	bounds := img.Bounds()
	if bounds.Dx() == 0 || bounds.Dy() == 0 || bounds.Dx() != bounds.Dy() {
		t.Fatalf("launcher mark is %dx%d; adaptive artwork must be a non-empty square",
			bounds.Dx(), bounds.Dy())
	}
	var maxRadius float64
	painted := 0
	transparent := 0
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			_, _, _, alpha := img.At(x, y).RGBA()
			if alpha == 0 {
				transparent++
				continue
			}
			painted++
			dx := (float64(x-bounds.Min.X)+0.5)/float64(bounds.Dx()) - 0.5
			dy := (float64(y-bounds.Min.Y)+0.5)/float64(bounds.Dy()) - 0.5
			maxRadius = math.Max(maxRadius, math.Hypot(dx, dy))
		}
	}
	if painted == 0 || transparent == 0 {
		t.Fatalf("launcher mark has painted=%d and transparent=%d pixels; it must be a transparent foreground",
			painted, transparent)
	}
	return maxRadius
}

func TestLauncherForegroundUsesApprovedAtmosphericMark(t *testing.T) {
	approved, err := os.ReadFile(approvedLauncherMarkPath(t))
	if err != nil {
		t.Fatalf("read approved launcher mark %s: %v", approvedLauncherMarkRelPath, err)
	}
	shipped, err := os.ReadFile(shippedLauncherMarkPath(t))
	if err != nil {
		t.Fatalf("read shipped launcher mark: %v", err)
	}
	if !bytes.Equal(shipped, approved) {
		t.Errorf("the Android launcher bitmap is not a byte-for-byte copy of %s; editing the copy "+
			"creates a second Atmospheric Swarm drawing that can drift from the store and README identity",
			approvedLauncherMarkRelPath)
	}

	root := loadLauncherResource(t, launcherForegroundPath(t))
	if root.XMLName.Local != "inset" {
		t.Fatalf("%s has root <%s>, want <inset>", launcherForegroundName, root.XMLName.Local)
	}
	inset := parseDP(t, requireLauncherAttr(t, root, "inset", launcherForegroundName),
		launcherForegroundName+"'s adaptive inset")
	if inset != launcherInsetDP {
		t.Errorf("%s uses a %.2fdp inset, want %.2fdp; that is the measured value that keeps every "+
			"non-transparent trail pixel inside Android's guaranteed mask-safe circle",
			launcherForegroundName, inset, launcherInsetDP)
	}

	bitmaps := root.childrenNamed("bitmap")
	if len(bitmaps) != 1 || len(root.Children) != 1 {
		t.Fatalf("%s declares %d <bitmap> child(ren) and %d total child(ren), want exactly one bitmap",
			launcherForegroundName, len(bitmaps), len(root.Children))
	}
	if got := requireLauncherAttr(t, bitmaps[0], "src", "<bitmap>"); got != "@drawable/swarm_atmospheric_mark" {
		t.Errorf("%s bitmap source is %q, want @drawable/swarm_atmospheric_mark",
			launcherForegroundName, got)
	}
	if got := requireLauncherAttr(t, bitmaps[0], "gravity", "<bitmap>"); got != "fill" {
		t.Errorf("%s bitmap gravity is %q, want fill so the measured square maps to the measured inset",
			launcherForegroundName, got)
	}
}

func TestLauncherAtmosphericMarkStaysInsideEveryAdaptiveMask(t *testing.T) {
	root := loadLauncherResource(t, launcherForegroundPath(t))
	inset := parseDP(t, requireLauncherAttr(t, root, "inset", launcherForegroundName),
		launcherForegroundName+"'s adaptive inset")
	if inset*2 >= adaptiveCanvasDP {
		t.Fatalf("%.2fdp inset leaves no drawable area on the %.0fdp adaptive canvas",
			inset, adaptiveCanvasDP)
	}

	maxRadius := maxAlphaRadius(t, decodeLauncherPNG(t, approvedLauncherMarkPath(t)))
	withoutInset := maxRadius * adaptiveCanvasDP
	withInset := maxRadius * (adaptiveCanvasDP - 2*inset)
	if withoutInset <= guaranteedSafeRadiusDP {
		t.Fatalf("negative control is vacuous: the raw mark already fits the %.0fdp safe radius "+
			"(measured %.2fdp), so this test cannot prove the inset matters",
			guaranteedSafeRadiusDP, withoutInset)
	}
	if withInset > guaranteedSafeRadiusDP {
		t.Errorf("the furthest non-transparent Atmospheric Swarm pixel lands at radius %.2fdp after "+
			"the %.2fdp inset, outside Android's guaranteed %.0fdp adaptive-icon safe radius",
			withInset, inset, guaranteedSafeRadiusDP)
	}
}

func TestLauncherAdaptiveIconDeclaresAtmosphericLayers(t *testing.T) {
	want := map[string]string{
		"background": "@color/swarm_background",
		"foreground": "@drawable/ic_launcher_foreground",
		"monochrome": "@drawable/ic_launcher_foreground",
	}
	for _, file := range []string{"ic_launcher.xml", "ic_launcher_round.xml"} {
		root := loadLauncherResource(t, filepath.Join(launcherMipmapDir(t), file))
		if root.XMLName.Local != "adaptive-icon" {
			t.Errorf("%s has root <%s>, want <adaptive-icon>", file, root.XMLName.Local)
			continue
		}
		for layer, wantRef := range want {
			children := root.childrenNamed(layer)
			if len(children) != 1 {
				t.Errorf("%s declares %d <%s> layers, want exactly one", file, len(children), layer)
				continue
			}
			if got := requireLauncherAttr(t, children[0], "drawable", fmt.Sprintf("%s <%s>", file, layer)); got != wantRef {
				t.Errorf("%s <%s> is %q, want %q", file, layer, got, wantRef)
			}
		}
	}
}
