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
	"bufio"
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
	flag.StringVar(&fopts.Pad, "pad", " ", "specify text to pad output lines on the left")
	flag.BoolVar(&fopts.Animate, "animate", false, "animate images")
	flag.IntVar(&fopts.Repeat, "repeat", -1, "number of animated loops")
	flag.IntVar(&fopts.Delay, "delay", 200, "for animate, delay in milliseconds before the next frame")
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

		// correct for wrap/overflow due to newlines and padding.
		*width -= len(fopts.Pad)
		*height -= 1
	}
	scaledFrames := ResizeFrames(*width, *height, *fontAspect, frames, stop)

	// delay images to avoid them having too high a framerate at small sizes.
	ticker := time.NewTicker(time.Second / 100)
	defer ticker.Stop()
	delayedFrames := DelayFrames(scaledFrames, stop, ticker.C, 30*time.Millisecond)

	err = writeANSIFrames(os.Stdout, delayedFrames, stop, palette, fopts)
	if err != nil {
		log.Fatal(err)
	}
}

type Frame struct {
	Image image.Image
	Delay time.Duration
}

func DelayFrames(frames <-chan *Frame, stop <-chan struct{}, tick <-chan time.Time, delayDefault time.Duration) <-chan *Frame {
	delayed := make(chan *Frame)
	go func() {
		defer close(delayed)
		var zeroTime time.Time // zeroTime is set on the first frame
		var currTime time.Time // currTime is updated with tick
		var currFrame *Frame
		var currFrameTime time.Time
		var ok bool
		_frames := frames
		var _delayed chan<- *Frame
		for {
			select {
			case currFrame, ok = <-_frames:
				if !ok {
					return
				}
				_frames = nil

				if zeroTime.IsZero() {
					zeroTime = time.Now()
					currFrameTime = zeroTime
				}
				currFrameTime = currFrameTime.Add(currFrame.Delay)

				if currTime.Add(1).After(currFrameTime) {
					_delayed = delayed
				}
			case currTime = <-tick:
				if _delayed == nil && currFrame != nil && currTime.Add(1).After(currFrameTime) {
					_delayed = delayed
				}
			case _delayed <- currFrame:
				_delayed = nil
				_frames = frames
			case <-stop:
				return
			}
		}
	}()
	return delayed
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
	var rect image.Rectangle
	animate := opts != nil && opts.Animate

	for {
		select {
		case <-stop:
			return nil
		case f, ok := <-frames:
			if !ok {
				return nil
			}
			if animate {
				up := rect.Size().Y
				rect = f.Image.Bounds()
				if up > 0 {
					fmt.Fprintf(w, "\033[%dA", up)
				}
				time.Sleep(time.Duration(opts.Delay) * time.Millisecond)
			}
			err := writeANSIPixels(w, f.Image, p, opts.Pad)
			if err != nil {
				return err
			}
		}
	}
}

func writeANSIFramePixels(w io.Writer, imgs []image.Image, p ANSIPalette, opts *FrameOptions) error {
	var rect image.Rectangle
	animate := opts != nil && opts.Animate

	loopn := 1
	if opts != nil {
		loopn += opts.Repeat
	}

	for loop := 0; loopn <= 0 || loop < loopn; loop++ {
		for _, img := range imgs {
			if animate {
				up := rect.Size().Y
				rect = img.Bounds()
				if up > 0 {
					fmt.Fprintf(w, "\033[%dA", up)
				}
			}
			err := writeANSIPixels(w, img, p, opts.Pad)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func writeANSIPixels(w io.Writer, img image.Image, p ANSIPalette, pad string) error {
	wbuf := bufio.NewWriter(w)
	writeansii := func() func(color string) {
		var lastcolor string
		return func(color string) {
			if color != lastcolor {
				lastcolor = color
				wbuf.WriteString(color)
			}
		}
	}()
	rect := img.Bounds()
	size := rect.Size()
	for y := 0; y < size.Y; y++ {
		wbuf.WriteString(pad)
		for x := 0; x < size.X; x++ {
			color := img.At(rect.Min.X+x, rect.Min.Y+y)
			writeansii(p.ANSI(color))
			wbuf.WriteString(" ")
		}
		wbuf.WriteString(pad)
		writeansii(ANSIClear)
		wbuf.WriteString("\n")
	}
	return wbuf.Flush()
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

func readFramesURL(urlstr string, stop <-chan struct{}, fopts *FrameOptions) ([]image.Image, error) {
	u, err := url.Parse(urlstr)
	if err != nil {
		return nil, err
	}
	if u.Scheme == "" {
		return readFramesFile(urlstr, stop, fopts)
	}
	if u.Scheme == "file" {
		return readFramesFile(u.Path, stop, fopts)
	}
	if u.Scheme == "http" || u.Scheme == "https" {
		return readFramesHTTP(urlstr, stop, fopts)
	}
	return nil, fmt.Errorf("unrecognized url: %v", urlstr)
}

func decodeFramesHTTP(u string, stop <-chan struct{}, fopts *FrameOptions) (<-chan *Frame, error) {
	client := http.Client{
		Timeout: 10 * time.Second,
	}
	resp, err := client.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
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

func readFramesHTTP(u string, stop <-chan struct{}, fopts *FrameOptions) ([]image.Image, error) {
	client := http.Client{
		Timeout: 10 * time.Second,
	}
	resp, err := client.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
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
		return readFrames(resp.Body, stop, fopts)
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

func readFramesFile(filename string, stop <-chan struct{}, fopts *FrameOptions) ([]image.Image, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readFrames(f, stop, fopts)
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

func readFrames(r io.Reader, stop <-chan struct{}, fopts *FrameOptions) ([]image.Image, error) {
	var confbuf bytes.Buffer
	_, format, err := image.DecodeConfig(io.TeeReader(r, &confbuf))
	if err != nil {
		return nil, err
	}
	r = io.MultiReader(&confbuf, r)
	if format == "gif" {
		return readFramesGIF(r, stop, fopts)
	}
	img, _, err := image.Decode(r)
	if err != nil {
		return nil, err
	}
	return []image.Image{img}, nil
}

func decodeFramesGIF(r io.Reader, stop <-chan struct{}, fopts *FrameOptions) (<-chan *Frame, error) {
	img, err := gif.DecodeAll(r)
	if err != nil {
		return nil, err
	}

	const timeUnit = time.Second / 100
	c := make(chan *Frame, len(img.Image))
	go func() {
		defer close(c)

		renderer := newGIFRenderer(img, func(b image.Rectangle) draw.Image { return image.NewRGBA64(b) })
		for renderer.RenderNext() {
			select {
			case <-stop:
				return
			default:
			}
		}

		numloop := img.LoopCount
		if fopts.Repeat >= 0 {
			numloop = fopts.Repeat + 1
		} else if fopts.Repeat < 0 {
			numloop = -1
		}
		log.Printf("loop count: %d", numloop)
		log.Printf("delays: %v", img.Delay)
		for n := 0; n != numloop; n++ {
			for i, fimg := range renderer.Frames {
				delay := img.Delay[i]
				if i == 0 && n == 0 {
					delay = 0
				}
				select {
				case <-stop:
					return
				case c <- &Frame{
					Image: fimg,
					Delay: time.Duration(delay) * timeUnit,
				}:
				}
			}
		}
	}()
	return c, nil
}

func readFramesGIF(r io.Reader, stop <-chan struct{}, fopts *FrameOptions) ([]image.Image, error) {
	img, err := gif.DecodeAll(r)
	if err != nil {
		return nil, err
	}
	var imgs []image.Image
	for _, img := range img.Image {
		imgs = append(imgs, img)
	}
	return imgs, nil
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
//		sizeRect(size, 0, 0, fontAspect) == sizeNormal(size, fontAspect)
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
