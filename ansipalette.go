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
	const begin = 16
	const ratio = 5.0 / (1<<16 - 1)
	rf, gf, bf, af := c.RGBA()
	if af < AlphaThreshold {
		return ANSIClear
	}
	r := int(round(ratio * float64(rf)))
	g := int(round(ratio * float64(gf)))
	b := int(round(ratio * float64(bf)))
	val := r*6*6 + g*6 + b + begin
	return "\033[48;5;" + strconv.Itoa(val) + "m"
}

type Palette256Precise struct{}

func (p *Palette256Precise) ANSI(c color.Color) string {
	if IsTransparent(c, AlphaThreshold) {
		return ANSIClear
	}
	val := palette256.Index(c)
	return "\033[48;5;" + strconv.Itoa(val) + "m"
}
