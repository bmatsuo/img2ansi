package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"log"
	"math"
	"os"

	"github.com/nfnt/resize"
)

const ANSIClear = "\033[0m"

func main() {
	fontAspect := flag.Float64("fontaspect", 0.5, "aspect ratio (width/height)")
	flag.Parse()
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
	rectnorm := imgnorm.Bounds()

	var palette ANSIPalette
	palette = new(Palette256)
	for y := 0; y < sizenorm.Y; y++ {
		fmt.Print("  ")
		for x := 0; x < sizenorm.X; x++ {
			color := imgnorm.At(rectnorm.Min.X+x, rectnorm.Min.Y+y)
			fmt.Print(palette.ANSI(color) + " ")
		}
		fmt.Println(ANSIClear)
	}
}

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

func normalSize(size image.Point, fontAspect float64) image.Point {
	aspect := float64(size.X) / float64(size.Y)
	var norm image.Point
	norm.Y = size.Y
	w := float64(size.Y) * aspect / fontAspect
	norm.X = int(round(w))
	return norm
}

func round(x float64) float64 {
	return math.Ceil(x - 0.5)
}

type ANSIPalette interface {
	ANSI(color.Color) string
}

type Palette256 struct {
}

func (p *Palette256) ANSI(c color.Color) string {
	const begin = 16
	const ratio = 5.0 / float64(1<<16-1)
	rf, gf, bf, af := c.RGBA()
	if af < 1 {
		return ANSIClear
	}
	r := int(round(ratio * float64(rf)))
	g := int(round(ratio * float64(gf)))
	b := int(round(ratio * float64(bf)))
	return fmt.Sprintf("\033[48;5;%dm", r*6*6+g*6+b+begin)
}
