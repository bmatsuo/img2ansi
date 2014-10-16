package main

import (
	"bufio"
	"flag"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log"
	"math"
	"os"
	"runtime"
	"runtime/pprof"

	"github.com/nfnt/resize"
)

const ANSIClear = "\033[0m"

var AlphaThreshold = uint32(0xffff)

func IsTransparent(c color.Color, threshold uint32) bool {
	_, _, _, a := c.RGBA()
	return a < threshold
}

func init() {
	runtime.GOMAXPROCS(runtime.NumCPU())
	log.SetFlags(0)
}

func main() {
	cpuprofile := flag.String("cpuprofile", "", "path of pprof CPU profile output")
	width := flag.Int("width", 0, "desired width in terminal columns")
	pad := flag.Bool("pad", false, "pad output on the left with whitespace")
	paletteName := flag.String("color", "256", "color palette (8, 256, gray, ...)")
	fontAspect := flag.Float64("fontaspect", 0.5, "aspect ratio (width/height)")
	alphaThreshold := flag.Float64("alphamin", 1.0, "transparency threshold")
	useStdin := flag.Bool("stdin", false, "read image data from stdin")
	flag.Parse()

	AlphaThreshold = uint32(*alphaThreshold * float64(0xffff))

	if *cpuprofile != "" {
		f, err := os.Create(*cpuprofile)
		if err != nil {
			log.Fatal(err)
		}
		pprof.StartCPUProfile(f)
		defer pprof.StopCPUProfile()
	}

	palette := ansiPalettes[*paletteName]
	if palette == nil {
		log.Fatalf("color palette not one of %q", ANSIPalettes())
	}

	var img image.Image
	var err error
	if *useStdin {
		img, _, err = image.Decode(os.Stdin)
	} else {
		if flag.NArg() < 1 {
			log.Fatal("missing filename")
		}
		if flag.NArg() > 1 {
			log.Fatal("unexpected arguments")
		}
		filename := flag.Arg(0)
		img, _, err = readImage(filename)
	}
	if err != nil {
		log.Fatalf("image: %v", err)
	}

	// resize img to the proper width and aspect ratio
	size := img.Bounds().Size()
	if *width > 0 {
		size = sizeWidth(size, *width, *fontAspect)
	} else {
		size = sizeNormal(size, *fontAspect)
	}
	if size != img.Bounds().Size() {
		img = resize.Resize(uint(size.X), uint(size.Y), img, 0)
	}

	err = writePixelsANSI(os.Stdout, img, palette, *pad)
	if err != nil {
		log.Fatalf("write: %v", err)
	}
}

var lineBytes = []byte{'\n'}
var spaceBytes = []byte{' '}

func writePixelsANSI(w io.Writer, img image.Image, p ANSIPalette, pad bool) error {
	wbuf := bufio.NewWriter(w)
	rect := img.Bounds()
	size := rect.Size()
	for y := 0; y < size.Y; y++ {
		if pad {
			wbuf.Write(spaceBytes)
		}
		for x := 0; x < size.X; x++ {
			color := img.At(rect.Min.X+x, rect.Min.Y+y)
			wbuf.WriteString(p.ANSI(color))
			wbuf.Write(spaceBytes)
		}
		if pad {
			wbuf.Write(spaceBytes)
		}
		wbuf.WriteString(ANSIClear)
		wbuf.Write(lineBytes)
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

// sizeWidth returns a point with X equal to width and the same normalized
// aspect ratio as size.
func sizeWidth(size image.Point, width int, fontAspect float64) image.Point {
	size = sizeNormal(size, fontAspect)
	aspect := float64(size.X) / float64(size.Y)
	size.X = width
	size.Y = int(round(float64(width) / aspect))
	return size
}

// sizeNormal scales size according to aspect ratio fontAspect and returns the
// new size.
func sizeNormal(size image.Point, fontAspect float64) image.Point {
	aspect := float64(size.X) / float64(size.Y)
	norm := size
	norm.Y = size.Y
	w := float64(norm.Y) * aspect / fontAspect
	norm.X = int(round(w))
	return norm
}

// round x to the nearest integer biased toward +Inf.
func round(x float64) float64 {
	return math.Floor(x + 0.5)
}
