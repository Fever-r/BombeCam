package main

import (
	"io/fs"
	"regexp"
	"testing"

	"github.com/Fever-r/BombeCam/internal/ui"
)

func TestCameraModelKey(t *testing.T) {
	for model, want := range map[string]string{
		"WS03":        "WS03",
		"K1PRO":       "K1PRO",
		"K1 Pro":      "K1PRO",
		"GC3_A3S11A3": "GC3",    // hardware revision after "_"
		"GP5":         "P5",     // longest key inside
		"P10":         "P10",    // not P1
		"GT1PRO-1W":   "GT1PRO", // not T1PRO or GT1
		"WS04PRO":     "WS04",
		"GL1C":        "GL1",
		"GC2E":        "GC2",
		"GK1PRO":      "GK1PRO",
		"C1_P1":       "C1", // the revision never picks the model
		"XP1C1":       "C1", // a tie goes to the first alphabetically
		"XYZ9":        "",
		"":            "",
	} {
		if got := cameraModelKey(model); got != want {
			t.Errorf("cameraModelKey(%q) = %q, want %q", model, got, want)
		}
	}
	for _, c := range []struct {
		model          string
		ptz, spotlight bool
	}{
		{"WS03", true, false}, {"WS01", false, false}, {"WS02", false, false}, {"WS04", true, true},
		{"GW30", true, true}, {"GT1PRO", false, false}, {"GC3_A3S11A3", false, false},
		{"unknown", true, false}, {"", true, false},
	} {
		if ptz, spot := cameraCaps(c.model); ptz != c.ptz || spot != c.spotlight {
			t.Errorf("cameraCaps(%q) = %v, %v; want %v, %v", c.model, ptz, spot, c.ptz, c.spotlight)
		}
	}
}

// The web page and Home Assistant must agree on what each model has.
func TestCameraModelsMatchWebPage(t *testing.T) {
	b, err := fs.ReadFile(ui.FS(), "app.js")
	if err != nil {
		t.Fatal(err)
	}
	table := regexp.MustCompile(`(?s)const CAMERA_CAPABILITY_MATRIX = \{(.*?)\n\};`).FindSubmatch(b)
	if table == nil {
		t.Fatal("CAMERA_CAPABILITY_MATRIX not found in app.js")
	}
	rows := regexp.MustCompile(`'([A-Z0-9]+)':\s*\{\s*ptz:\s*(true|false),\s*spotlight:\s*(true|false)`).FindAllSubmatch(table[1], -1)
	if len(rows) != len(cameraModels) {
		t.Errorf("app.js has %d models, cameraModels has %d", len(rows), len(cameraModels))
	}
	for _, r := range rows {
		key := string(r[1])
		c, ok := cameraModels[key]
		if !ok {
			t.Errorf("%s is in app.js but not in cameraModels", key)
			continue
		}
		if ptz, spot := string(r[2]) == "true", string(r[3]) == "true"; ptz != c.ptz || spot != c.spotlight {
			t.Errorf("%s: app.js ptz=%v spotlight=%v, cameraModels ptz=%v spotlight=%v", key, ptz, spot, c.ptz, c.spotlight)
		}
	}
}
