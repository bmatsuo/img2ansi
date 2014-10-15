package main

import (
	"bufio"
	"flag"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log"
	"math"
	"os"

	"github.com/nfnt/resize"
)

const ANSIClear = "\033[0m"

func main() {
	pad := flag.Bool("pad", false, "pad output on the left with whitespace")
	paletteName := flag.String("color", "256", "color palette (8, 256, gray, ...)")
	fontAspect := flag.Float64("fontaspect", 0.5, "aspect ratio (width/height)")
	flag.Parse()

	palette := ansiPalettes[*paletteName]
	if palette == nil {
		log.Fatalf("color palette not one of %q", ANSIPalettes())
	}

	if flag.NArg() < 1 {
		log.Fatal("missing filename")
	}
	if flag.NArg() > 1 {
		log.Fatal("unexpected arguments")
	}
	filename := flag.Arg(0)

	img, _, err := readImage(filename)
	if err != nil {
		log.Fatalf("image: %v", err)
	}
	sizenorm := normalSize(img.Bounds().Size(), *fontAspect)
	imgnorm := resize.Resize(uint(sizenorm.X), uint(sizenorm.Y), img, 0)
	err = writePixelsANSI(os.Stdout, imgnorm, palette, *pad)
	if err != nil {
		log.Fatalf("write: %v", err)
	}
}

func writePixelsANSI(w io.Writer, img image.Image, p ANSIPalette, pad bool) error {
	wbuf := bufio.NewWriter(w)
	rect := img.Bounds()
	size := rect.Size()
	for y := 0; y < size.Y; y++ {
		if pad {
			fmt.Fprint(wbuf, "  ")
		}
		for x := 0; x < size.X; x++ {
			color := img.At(rect.Min.X+x, rect.Min.Y+y)
			fmt.Fprint(wbuf, p.ANSI(color)+" ")
		}
		fmt.Fprintln(wbuf, ANSIClear)
	}
	return wbuf.Flush()
}

// readImage reads an image.Image from a specified file.
func readImage(filename string) (image.Image, string, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()

	img, format, err := image.Decode(f)
	if err != nil {
		return nil, "", err
	}
	return img, format, nil
}

// normalSize scales size according to aspect ratio fontAspect and returns the
// new size.
func normalSize(size image.Point, fontAspect float64) image.Point {
	aspect := float64(size.X) / float64(size.Y)
	norm := size
	norm.Y = size.Y
	w := float64(norm.Y) * aspect / fontAspect
	norm.X = int(round(w))
	return norm
}

// round x to the nearest integer biased toward +Inf.
func round(x float64) float64 {
	return math.Ceil(x - 0.5)
}

type ANSIPalette interface {
	ANSI(color.Color) string
}

var ansiPalettes = map[string]ANSIPalette{
	"256":       new(Palette256),
	"256-color": new(Palette256),
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
	_, _, _, a := c.RGBA()
	if a == 0 {
		return ANSIClear
	}
	gray := color.GrayModel.Convert(c).(color.Gray).Y
	scaled := int(round(ratio * float64(gray)))
	return fmt.Sprintf("\033[48;5;%dm", scaled+begin)
}

// Palette8 is an ANSIPalette that maps color.Color values to one of 8 color
// indexes by minimizing euclidean RGB distance.
type Palette8 [8][3]uint8

var DefaultPalette8 = &Palette8{
	{0, 0, 0},       // black
	{191, 25, 25},   // red
	{25, 184, 25},   // green
	{188, 110, 25},  // orange/brown/yellow
	{25, 25, 184},   // blue
	{186, 25, 186},  // magenta
	{25, 187, 187},  // cyan
	{178, 178, 178}, // gray
}

func (p *Palette8) ANSI(c color.Color) string {
	_, _, _, a := c.RGBA()
	if a == 0 {
		return ANSIClear
	}
	min := math.Inf(1) // minimum distance from c
	var imin int       // minimizing index
	for i, rgb := range *p {
		other := color.RGBA{rgb[0], rgb[1], rgb[2], 0}
		dist := Distance(other, c)
		if dist < min {
			min = dist
			imin = i
		}
	}
	return fmt.Sprintf("\033[4%dm", imin)
}

// Palette256 is an ANSIPalette that maps color.Color to one of 256 RGB colors.
type Palette256 struct {
}

func (p *Palette256) ANSI(c color.Color) string {
	const begin = 16
	const ratio = 5.0 / (1<<16 - 1)
	rf, gf, bf, af := c.RGBA()
	if af == 0 {
		return ANSIClear
	}
	r := int(round(ratio * float64(rf)))
	g := int(round(ratio * float64(gf)))
	b := int(round(ratio * float64(bf)))
	return fmt.Sprintf("\033[48;5;%dm", r*6*6+g*6+b+begin)
}

func Distance(c1, c2 color.Color) float64 {
	r1, g1, b1, _ := c1.RGBA()
	r2, g2, b2, _ := c2.RGBA()
	rdiff := float64(int(r1) - int(r2))
	gdiff := float64(int(g1) - int(g2))
	bdiff := float64(int(b1) - int(b2))
	return math.Sqrt(rdiff*rdiff + gdiff*gdiff + bdiff*bdiff)
}
