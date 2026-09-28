package cmd

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/config"
	"github.com/nlink-jp/gem-agent/internal/termimg"
	"github.com/nlink-jp/mermaid-render/raster"
)

// The diagram font is read only where pictures can be drawn, a setting
// that does not load is a warning and the default font, and without the
// default font diagrams stay text (ADR-0092 §6).
func TestDiagramPicture(t *testing.T) {
	home, _ := os.UserHomeDir()
	type calls struct {
		spec []raster.FontSpec
		def  int
	}
	loaders := func(c *calls, specErr, defErr error) fontLoaders {
		return fontLoaders{
			spec: func(s raster.FontSpec) (*raster.Font, error) {
				c.spec = append(c.spec, s)
				return &raster.Font{}, specErr
			},
			def: func() (*raster.Font, error) {
				c.def++
				return &raster.Font{}, defErr
			},
		}
	}
	bad := errors.New("font /x.ttc: no face named \"Nope\"")
	for _, tc := range []struct {
		name         string
		images       termimg.Protocol
		cfg          config.DiagramConfig
		specErr      error
		defErr       error
		wantPic      bool
		wantNote     string
		wantSpec     int
		wantDef      int
		wantSpecPath string
	}{
		{name: "no protocol reads no font", images: termimg.None},
		{name: "the default", images: termimg.ITerm2, wantPic: true, wantDef: 1},
		{name: "a font that loads", images: termimg.Kitty, cfg: config.DiagramConfig{Font: "~/f.ttc", FontName: "F-W3"},
			wantPic: true, wantSpec: 1, wantSpecPath: home + "/f.ttc"},
		{name: "a font that does not load", images: termimg.ITerm2, cfg: config.DiagramConfig{Font: "/x.ttc", FontName: "Nope"}, specErr: bad,
			wantPic: true, wantNote: `[tui.diagram] font /x.ttc: no face named "Nope"; diagrams use the default font`, wantSpec: 1, wantDef: 1},
		{name: "a name without a font", images: termimg.ITerm2, cfg: config.DiagramConfig{FontName: "F-W3"},
			wantPic: true, wantNote: "need font", wantDef: 1},
		{name: "a bold name without a bold font", images: termimg.ITerm2, cfg: config.DiagramConfig{Font: "/f.ttc", BoldFontName: "F-W6"},
			wantPic: true, wantNote: "bold_font_name needs bold_font", wantDef: 1},
		{name: "no default font either", images: termimg.ITerm2, defErr: errors.New("no Hiragino"),
			wantNote: "drawn as text", wantDef: 1},
	} {
		var c calls
		pic, notes := diagramPicture(tc.images, tc.cfg, loaders(&c, tc.specErr, tc.defErr))
		if (pic != nil) != tc.wantPic {
			t.Errorf("%s: picture %v, want %v", tc.name, pic != nil, tc.wantPic)
		}
		note := strings.Join(notes, "\n")
		if tc.wantNote == "" && note != "" || !strings.Contains(note, tc.wantNote) {
			t.Errorf("%s: notes %q, want %q", tc.name, note, tc.wantNote)
		}
		if len(c.spec) != tc.wantSpec || c.def != tc.wantDef {
			t.Errorf("%s: %d spec loads, %d default loads; want %d, %d", tc.name, len(c.spec), c.def, tc.wantSpec, tc.wantDef)
		}
		if tc.wantSpecPath != "" && (len(c.spec) == 0 || c.spec[0].Path != tc.wantSpecPath) {
			t.Errorf("%s: loaded %+v, want path %s", tc.name, c.spec, tc.wantSpecPath)
		}
	}
}
