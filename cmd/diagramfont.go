package cmd

import (
	"fmt"

	"github.com/nlink-jp/gem-agent/internal/config"
	"github.com/nlink-jp/gem-agent/internal/diagram"
	"github.com/nlink-jp/gem-agent/internal/termimg"
	"github.com/nlink-jp/mermaid-render/raster"
)

// fontLoaders are the two ways a diagram font is read, injected for tests.
type fontLoaders struct {
	spec func(raster.FontSpec) (*raster.Font, error)
	def  func() (*raster.Font, error)
}

var systemFonts = fontLoaders{spec: raster.LoadFont, def: raster.DefaultFont}

// diagramPicture loads the font mermaid diagrams are drawn in — once, here
// in the cmd layer, because the view layer opens no file (ADR-0089 §5) —
// and only for a session that draws images: one that cannot show a
// picture reads no font (ADR-0092 §6). A setting that does not load is a
// banner warning and the default font, never a refusal to start (the
// operator's decision); without the default font either, diagrams stay on
// the box-art lane (nil).
func diagramPicture(images termimg.Protocol, c config.DiagramConfig, load fontLoaders) (diagram.Picture, []string) {
	if images == termimg.None {
		return nil, nil
	}
	var notes []string
	font, bold := c.Files()
	switch {
	case font == "" && (c.FontName != "" || bold != "" || c.BoldFontName != ""):
		notes = append(notes, "[tui.diagram] font_name, bold_font and bold_font_name need font; diagrams use the default font")
	case bold == "" && c.BoldFontName != "":
		notes = append(notes, "[tui.diagram] bold_font_name needs bold_font; diagrams use the default font")
	case font != "":
		f, err := load.spec(raster.FontSpec{Path: font, Name: c.FontName, BoldPath: bold, BoldName: c.BoldFontName})
		if err == nil {
			return diagram.NewPicture(f, termimg.MaxBytes), nil
		}
		notes = append(notes, fmt.Sprintf("[tui.diagram] %v; diagrams use the default font", err))
	}
	f, err := load.def()
	if err != nil {
		return nil, append(notes, fmt.Sprintf("diagrams: the default font did not load (%v); they are drawn as text", err))
	}
	return diagram.NewPicture(f, termimg.MaxBytes), notes
}
