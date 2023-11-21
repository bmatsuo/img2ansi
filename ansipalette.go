package main

import (
	"image/color"
	"strconv"
)

type ANSIPalette interface {
	ANSI(color.Color) string
}

var ansiPalettes = map[string]ANSIPalette{
	"256":       new(Palette256Precise),
	"256-color": new(Palette256Precise),
	"256-fast":  new(Palette256),
	"8":         DefaultPalette8,
	"8-color":   DefaultPalette8,
	"gray":      new(PaletteGray),
	"grayscale": new(PaletteGray),
	"grey":      new(PaletteGray),
	"greyscale": new(PaletteGray),
}

func ANSIPalettes() []string {
	var names []string
	for name := range ansiPalettes {
		names = append(names, name)
	}
	return names
}

// PaletteGray is an ANSIPalette that maps color.Color values to one of twenty
// four grayscale values.
type PaletteGray struct {
}

func (p *PaletteGray) ANSI(c color.Color) string {
	const begin = 0xe8
	const ratio = 24.0 / 255.0
	if IsTransparent(c, AlphaThreshold) {
		return ANSIClear
	}
	gray := color.GrayModel.Convert(c).(color.Gray).Y
	scaled := int(round(ratio * float64(gray)))
	value := scaled + begin
	return "\033[48;5;" + strconv.Itoa(value) + "m"
}

// Color8 represents the set of colors in an 8-color palette.
type Color8 uint

const (
	Black Color8 = iota
	Red
	Green
	Orange // or brown or yellow
	Blue
	Magenta
	Cyan
	Gray
)

// Palette8 is an ANSIPalette that maps color.Color values to one of 8 color
// indexes by minimizing euclidean RGB distance.
type Palette8 [8]color.Color

var DefaultPalette8 = &Palette8{
	Black:   &color.RGBA{R: 0, G: 0, B: 0},
	Red:     &color.RGBA{R: 191, G: 25, B: 25},
	Green:   &color.RGBA{R: 25, G: 184, B: 25},
	Orange:  &color.RGBA{R: 188, G: 110, B: 25},
	Blue:    &color.RGBA{R: 25, G: 25, B: 184},
	Magenta: &color.RGBA{R: 186, G: 25, B: 186},
	Cyan:    &color.RGBA{R: 25, G: 187, B: 187},
	Gray:    &color.RGBA{R: 178, G: 178, B: 178},
}

func (p *Palette8) ANSI(c color.Color) string {
	if IsTransparent(c, AlphaThreshold) {
		return ANSIClear
	}
	var imin int // minimizing index
	cpalette := color.Palette((*p)[:]).Convert(c)
	for i, c2 := range *p {
		if c2 == cpalette {
			imin = i
		}
	}
	return "\033[4" + strconv.Itoa(imin) + "m"
}

// Palette256 is an ANSIPalette that maps color.Color to one of 256 RGB colors.
type Palette256 struct {
}

func (p *Palette256) ANSI(c color.Color) string {
	val, opaque := colorFindRGB(c)
	if !opaque {
		return ANSIClear
	}
	return "\033[48;5;" + strconv.Itoa(val) + "m"
}

var q2c = [6]int{0x00, 0x5f, 0x87, 0xaf, 0xd7, 0xff}

// colorFindRGB is ported from tmux's color matching function
//
//	https://github.com/tmux/tmux/blob/dae2868d1227b95fd076fb4a5efa6256c7245943/colour.c#L57
func colorFindRGB(c color.Color) (int, bool) {
	r, g, b, a := c.RGBA()

	if a < AlphaThreshold {
		return 0, false
	}

	r, g, b = colorScale256(r), colorScale256(g), colorScale256(b)

	qr := colorTo6Cube(r)
	cr := q2c[qr]

	qg := colorTo6Cube(g)
	cg := q2c[qg]

	qb := colorTo6Cube(b)
	cb := q2c[qb]

	if uint32(cr) == r && uint32(cg) == g && uint32(cb) == b {
		return (16 + int(36*qr) + int(6*qg) + int(qb)), true
	}

	greyAvg := int(r+g+b) / 3
	greyIdx := 23
	if greyAvg <= 238 {
		greyIdx = (greyAvg - 3) / 10
	}
	grey := 8 + (10 * greyIdx)

	cDist := colorDistSq(cr, cg, cb, int(r), int(g), int(b))
	if colorDistSq(grey, grey, grey, int(r), int(g), int(b)) < cDist {
		return (232 + greyIdx), true
	}

	return (16 + int(36*qr) + int(6*qg) + int(qb)), true
}

func colorScale256(v uint32) uint32 {
	return v * 256 / (1<<16 - 1)
}

func colorTo6Cube(v uint32) uint32 {
	if v < 48 {
		return 0
	}
	if v < 114 {
		return 1
	}
	return ((v - 35) / 40)
}

func colorDistSq(r1, g1, b1, r2, g2, b2 int) int {
	return (r1-r2)*(r1-r2) + (g1-g2)*(g1-g2) + (b1-b2)*(b1-b2)
}

type Palette256Precise struct{}

func (p *Palette256Precise) ANSI(c color.Color) string {
	if IsTransparent(c, AlphaThreshold) {
		return ANSIClear
	}
	val := palette256.Index(c)
	return "\033[48;5;" + strconv.Itoa(val) + "m"
}
