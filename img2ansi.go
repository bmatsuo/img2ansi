/*
Command img2ansi renders raster images for a terminal using ANSI color
codes.  Supported image types are JPEG, PNG, and GIF (which may be
animated).

	img2ansi motd.png
	img2ansi -animate -repeat=5 -scale https://i.imgur.com/872FDBm.gif
	img2ansi -h

The command takes as arguments URLs referencing images to render.  If no
arguments are given img2ansi reads image data from standard input.  Image
URLs may be local files (simple paths or file:// urls) or HTTP(S) URLs.
*/
package main

import (
	"bytes"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"runtime"
	"runtime/pprof"
	"time"

	"github.com/bmatsuo/img2ansi/gif"
	"github.com/nfnt/resize"
)

const ANSIClear = "\033[0m"
const DelayDefault = 33 * time.Millisecond

var Debug = false
var HTTPUserAgent = ""
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
	fopts := new(FrameOptions)

	cpuprofile := flag.String("cpuprofile", "", "path of pprof CPU profile output")
	scaleToTerm := flag.Bool("scale", false, "scale to fit the current terminal (overrides -width and -height)")
	height := flag.Int("height", 0, "desired height in terminal lines")
	width := flag.Int("width", 0, "desired width in terminal columns")
	paletteName := flag.String("color", "256", "color palette (8, 256, gray, ...)")
	fontAspect := flag.Float64("fontaspect", 0.5, "aspect ratio (width/height)")
	alphaThreshold := flag.Float64("alphamin", 1.0, "transparency threshold")
	useStdin := flag.Bool("stdin", false, "read image data from stdin")
	flag.StringVar(&HTTPUserAgent, "useragent", "", "user-agent header override for images fetched over http")
	flag.StringVar(&fopts.Pad, "pad", " ", "specify text to pad output lines on the left")
	flag.BoolVar(&fopts.Animate, "animate", false, "animate images")
	flag.IntVar(&fopts.Repeat, "repeat", -1, "number of animated loops")
	flag.IntVar(&fopts.Delay, "delay", 0, "for -animate, force delay in milliseconds before the next frame")
	flag.BoolVar(&Debug, "debug", false, "print debug information")
	flag.Parse()
	if *useStdin && flag.NArg() > 0 {
		log.Fatal("no arguments are expected when -stdin provided")
	}

	stop := make(chan struct{})
	var gotsignal *os.Signal
	defer func() {
		if gotsignal != nil {
			io.WriteString(os.Stdout, ANSIClear)
			log.Fatal(*gotsignal)
		}
	}()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	go func() {
		for s := range sig {
			signal.Stop(sig)
			s := s
			gotsignal = &s
			close(stop)
		}
	}()

	AlphaThreshold = uint32(*alphaThreshold * float64(0xffff))

	palette := ansiPalettes[*paletteName]
	if palette == nil {
		log.Fatalf("color palette not one of %q", ANSIPalettes())
	}

	if *cpuprofile != "" {
		f, err := os.Create(*cpuprofile)
		if err != nil {
			log.Fatal(err)
		}
		pprof.StartCPUProfile(f)
		defer pprof.StopCPUProfile()
	}

	var frames <-chan *Frame
	var err error
	if *useStdin || flag.NArg() == 0 {
		frames, err = decodeFrames(os.Stdin, stop, fopts)
		if err != nil {
			log.Fatal(err)
		}
	} else {
		// decode all the images given as arguments and concatenate their
		// frames.
		_frames := make(chan *Frame)
		frames = _frames
		var frameChans []<-chan *Frame
		for _, filename := range flag.Args() {
			frames, err := decodeFramesURL(filename, stop, fopts)
			if err != nil {
				log.Fatal(err)
			}
			frameChans = append(frameChans, frames)
		}
		go func() {
			defer close(_frames)
			for _, frames := range frameChans {
				for frame := range frames {
					_frames <- frame
				}
			}
		}()
	}

	// scale frame images to the desired size
	if *scaleToTerm {
		*width, *height, err = getTermDim()
		if err != nil {
			log.Fatal(err)
		}
		if Debug {
			log.Printf("terminal dimensions: %d x %d", *width, *height)
		}

		// correct for wrap/overflow due to newlines and padding.
		*width -= len(fopts.Pad)
		*width -= 1
		*height -= 1
	}
	scaledFrames := ResizeFrames(*width, *height, *fontAspect, frames, stop)

	loopedFrames := LoopFrames(scaledFrames, stop, fopts)

	err = writeANSIFrames(os.Stdout, loopedFrames, stop, palette, fopts)
	if err != nil {
		log.Fatal(err)
	}
}

type Frame struct {
	Image     image.Image
	Delay     time.Duration
	LoopCount int
}

func LoopFrames(frames <-chan *Frame, stop <-chan struct{}, fopts *FrameOptions) <-chan *Frame {
	var allFrames []*Frame
	looped := make(chan *Frame)
	go func() {
		defer close(looped)

	collectFrames:
		for {
			select {
			case <-stop:
				return
			case f, ok := <-frames:
				if !ok {
					break collectFrames
				}
				allFrames = append(allFrames, f)
				select {
				case <-stop:
					return
				case looped <- f:
				}
			}
		}

		if len(allFrames) == 0 {
			return
		}

		numloop := allFrames[0].LoopCount
		if fopts.Repeat >= 0 {
			numloop = fopts.Repeat
		} else if fopts.Repeat < 0 {
			numloop = -1
		}

		for n := 0; n != numloop; n++ {
			for _, f := range allFrames {
				select {
				case <-stop:
					return
				case looped <- f:
				}
			}
		}
	}()
	return looped
}

func ResizeFrames(width, height int, fontAspect float64, frames <-chan *Frame, stop <-chan struct{}) <-chan *Frame {
	scaled := make(chan *Frame)
	go func() {
		defer close(scaled)
		// resize the images to the proper size and aspect ratio
		for {
			select {
			case <-stop:
				return
			case f, ok := <-frames:
				if !ok {
					return
				}
				img := f.Image
				sizeOrig := img.Bounds().Size()
				size := sizeRect(sizeOrig, width, height, fontAspect)
				if size != sizeOrig { // it is super unlikely for this to happen
					img = resize.Resize(uint(size.X), uint(size.Y), img, 0)
				}
				scaled <- &Frame{
					Image: img,
					Delay: f.Delay,
				}
			}
		}
	}()
	return scaled
}

type DecodeOptions struct {
	DefaultDelay time.Duration
	LoopCount    int
}

// FrameOptions describes how to render a sequence of frames in a terminal.
type FrameOptions struct {
	// Delay is the time to wait between animating frames.
	Delay int

	// Pad is a string prepended to each row of pixels.
	Pad string

	// Animate will animate the frames when true.  Animation is accomplished by
	// emitting a control sequence to reset the cursor before rendering each
	// frame.
	Animate bool

	// Repeat specifies the number of times to render the frame sequence.  If
	// Repeat is zero the frames are rendered just once.  If Repeat is less
	// than zero the frames are rendered indefinitely.
	Repeat int
}

// writeANSIFrames encodes images received over frames as ANSI escape sequences
// using p and writes them to w.  writeANSIFrames does not use opts.Repeat.
func writeANSIFrames(w io.Writer, frames <-chan *Frame, stop <-chan struct{}, p ANSIPalette, opts *FrameOptions) error {
	lastRect := image.Rectangle{}
	animate := opts != nil && opts.Animate

	// The frame buffer is filled completely before flushing to have the most
	// accurate and consistent fps.
	buf := newFrameBuffer(w)

	// frameGate receives a value when a frame is ready to be drawn. The value
	// received should not be interpreted
	frameGate := func() <-chan time.Time {
		c := make(chan time.Time)
		close(c)
		return c
	}()

	// frame counter and timing
	nframe := 0
	start := time.Now()
	defer func() {
		if Debug {
			dur := time.Since(start)
			secs := float64(dur) / float64(time.Second)
			fps := float64(nframe) / secs
			log.Printf("fps: %.2f", fps)
		}
	}()

	for {
		select {
		case <-stop:
			return nil
		case f, ok := <-frames:
			if !ok {
				return nil
			}

			if animate {
				// Delay this animation frame before rendering by setting frameGate
				if nframe > 0 {
					delay := time.Duration(opts.Delay) * time.Millisecond
					if delay == 0 {
						delay = f.Delay
					}
					if delay == 0 {
						delay = DelayDefault
					}
					frameGate = time.After(delay)
				}

				// Reset the cursor to the top of the image
				up := lastRect.Size().Y
				lastRect = f.Image.Bounds()
				if up > 0 {
					fmt.Fprintf(buf, "\033[%dA", up)
				}
			}

			err := writeANSIPixels(buf, f.Image, p, opts.Pad)
			if err != nil {
				return err
			}

			<-frameGate

			err = buf.Flush()
			if err != nil {
				return err
			}
		}
		nframe++
	}
}

func writeANSIPixels(w *frameBuffer, img image.Image, p ANSIPalette, pad string) error {
	writeansii := func() func(color string) {
		var lastcolor string
		return func(color string) {
			if color != lastcolor {
				lastcolor = color
				w.WriteString(color)
			}
		}
	}()
	rect := img.Bounds()
	size := rect.Size()
	for y := 0; y < size.Y; y++ {
		w.WriteString(pad)
		for x := 0; x < size.X; x++ {
			color := img.At(rect.Min.X+x, rect.Min.Y+y)
			writeansii(p.ANSI(color))
			w.WriteString(" ")
		}
		w.WriteString(pad)
		writeansii(ANSIClear)
		w.WriteString("\n")
	}
	return nil
}

func decodeFramesURL(urlstr string, stop <-chan struct{}, fopts *FrameOptions) (<-chan *Frame, error) {
	u, err := url.Parse(urlstr)
	if err != nil {
		return nil, err
	}
	if u.Scheme == "" {
		return decodeFramesFile(urlstr, stop, fopts)
	}
	if u.Scheme == "file" {
		return decodeFramesFile(u.Path, stop, fopts)
	}
	if u.Scheme == "http" || u.Scheme == "https" {
		return decodeFramesHTTP(urlstr, stop, fopts)
	}
	return nil, fmt.Errorf("unrecognized url: %v", urlstr)
}

func decodeFramesHTTP(u string, stop <-chan struct{}, fopts *FrameOptions) (<-chan *Frame, error) {
	client := http.Client{
		Timeout: 10 * time.Second,
	}
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	if HTTPUserAgent != "" {
		req.Header.Set("User-Agent", HTTPUserAgent)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		resp.Body = nil
		resp.Write(os.Stderr)
		return nil, fmt.Errorf("http: %v %v", resp.Status, u)
	}
	if resp.StatusCode >= 300 {
		// TODO:
		// Handle redirects better
		return nil, fmt.Errorf("http: %v %v", resp.Status, u)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("http: %v %v", resp.Status, u)
	}
	switch resp.Header.Get("Content-Type") {
	case "application/octet-stream", "image/png", "image/gif", "image/jpeg":
		return decodeFrames(resp.Body, stop, fopts)
	default:
		return nil, fmt.Errorf("mime: %v %v", resp.Header.Get("Content-Type"), u)
	}
}

func decodeFramesFile(filename string, stop <-chan struct{}, fopts *FrameOptions) (<-chan *Frame, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return decodeFrames(f, stop, fopts)
}

func decodeFrames(r io.Reader, stop <-chan struct{}, fopts *FrameOptions) (<-chan *Frame, error) {
	var confbuf bytes.Buffer
	_, format, err := image.DecodeConfig(io.TeeReader(r, &confbuf))
	if err != nil {
		return nil, err
	}
	r = io.MultiReader(&confbuf, r)
	if format == "gif" {
		return decodeFramesGIF(r, stop, fopts)
	}

	c := make(chan *Frame, 1)
	defer close(c)
	img, _, err := image.Decode(r)
	if err != nil {
		return nil, err
	}
	c <- &Frame{
		Image: img,
	}
	return c, nil
}

func decodeFramesGIF(r io.Reader, stop <-chan struct{}, fopts *FrameOptions) (<-chan *Frame, error) {
	img, err := gif.DecodeAll(r)
	if err != nil {
		return nil, err
	}

	renderer := newGIFRenderer(img, func(b image.Rectangle) draw.Image { return image.NewRGBA64(b) })
	for renderer.RenderNext() {
		select {
		case <-stop:
			return nil, fmt.Errorf("gif rendering interrupted")
		default:
		}
	}

	const timeUnit = time.Second / 100
	c := make(chan *Frame, len(img.Image))
	go func() {
		defer close(c)

		for i, fimg := range renderer.Frames {
			f := &Frame{
				Image:     fimg,
				Delay:     time.Duration(img.Delay[i]) * timeUnit,
				LoopCount: img.LoopCount,
			}

			select {
			case <-stop:
				return
			case c <- f:
			}
		}
	}()
	return c, nil
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

// sizeRect returns a point with dimensions less than or equal to the
// corresponding dimensions of size and having the same aspect ratio.  sizeRect
// always returns the largest such coordinates.  In particular this means the
// following expression evaluates true
//
//	sizeRect(size, 0, 0, fontAspect) == sizeNormal(size, fontAspect)
func sizeRect(size image.Point, width, height int, fontAspect float64) image.Point {
	size = sizeNormal(size, fontAspect)
	if width <= 0 && height <= 0 {
		return size
	}
	if width <= 0 {
		return _sizeHeight(size, height)
	}
	if height <= 0 {
		return _sizeWidth(size, width)
	}
	aspectSize := float64(size.X) / float64(size.Y)
	aspectRect := float64(width) / float64(height)
	if aspectSize > aspectRect {
		// the image aspect ratio is wider than the given dimensions.  the
		// image cannot fill the screen vertically.
		return _sizeWidth(size, width)
	}
	return _sizeHeight(size, height)
}

// _sizeWidth returns a point with X equal to width and the same aspect ratio
// as size.
func _sizeWidth(sizeNorm image.Point, width int) image.Point {
	aspect := float64(sizeNorm.X) / float64(sizeNorm.Y)
	sizeNorm.X = width
	sizeNorm.Y = int(round(float64(width) / aspect))
	return sizeNorm
}

// _sizeHeight returns a point with Y equal to height and the same aspect ratio
// as size.
func _sizeHeight(sizeNorm image.Point, height int) image.Point {
	aspect := float64(sizeNorm.X) / float64(sizeNorm.Y)
	sizeNorm.Y = height
	sizeNorm.X = int(round(float64(height) * aspect))
	return sizeNorm
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
