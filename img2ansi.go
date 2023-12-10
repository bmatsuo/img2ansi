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
	"context"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log"
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

var debugProcStartTime = time.Now()

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

	ctx, done := signal.NotifyContext(context.Background(), os.Interrupt)
	// TODO: Should done be called in a smarter way?
	defer done()
	defer func() {
		if ctx.Err() != nil {
			io.WriteString(os.Stdout, ANSIClear)
			log.Fatal(ctx.Err())
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
		frames, err = decodeFrames(ctx, os.Stdin, fopts)
		if err != nil {
			log.Fatal(err)
		}
	} else if flag.NArg() == 1 {
		frames, err = decodeFramesURL(ctx, flag.Arg(0), fopts)
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
			frames, err := decodeFramesURL(ctx, filename, fopts)
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
	scaledFrames := ResizeFrames(ctx, *width, *height, *fontAspect, frames)

	loopedFrames := LoopFrames(ctx, scaledFrames, fopts)

	ansiFrames := writeANSIFrames(ctx, loopedFrames, palette, fopts)

	err = drawANSIFrames(ctx, os.Stdout, ansiFrames, fopts)
	if err != nil {
		log.Fatal(err)
	}
}

type Frame struct {
	Image     image.Image
	Delay     time.Duration
	LoopCount int
}

type ANSIFrame struct {
	Buffer    *frameBuffer
	Delay     time.Duration
	LoopCount int
}

func LoopFrames(ctx context.Context, frames <-chan *Frame, fopts *FrameOptions) <-chan *Frame {
	var allFrames []*Frame
	looped := make(chan *Frame)
	go func() {
		defer close(looped)

	collectFrames:
		for {
			select {
			case <-ctx.Done():
				return
			case f, ok := <-frames:
				if !ok {
					break collectFrames
				}
				allFrames = append(allFrames, f)
				select {
				case <-ctx.Done():
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
				case <-ctx.Done():
					return
				case looped <- f:
				}
			}
		}
	}()
	return looped
}

func ResizeFrames(ctx context.Context, width, height int, fontAspect float64, frames <-chan *Frame) <-chan *Frame {
	if width == 0 && height == 0 {
		return frames
	}
	scaled := make(chan *Frame)
	go func() {
		defer close(scaled)
		// resize the images to the proper size and aspect ratio
		for {
			select {
			case <-ctx.Done():
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

func writeANSIFrames(ctx context.Context, frames <-chan *Frame, p ANSIPalette, opts *FrameOptions) <-chan *ANSIFrame {
	draw := make(chan *ANSIFrame)

	go func() {
		defer close(draw)

		// Keep two buffers so one can be filled while the other is being drawn.
		buffers := nbuffer(2)
		nframe := 0
		lastRect := image.Rectangle{}
		animate := opts != nil && opts.Animate

		for {
			select {
			case <-ctx.Done():
				return
			case f, ok := <-frames:
				if !ok {
					return
				}

				buf := buffers[nframe%2]

				if animate {
					// Reset the cursor to the top of the image
					up := lastRect.Size().Y
					lastRect = f.Image.Bounds()
					if up > 0 {
						fmt.Fprintf(buf, "\033[%dA", up)
					}
				}

				writeANSIPixels(buf, f.Image, p, opts.Pad)

				b := &ANSIFrame{
					Buffer:    buf,
					Delay:     f.Delay,
					LoopCount: f.LoopCount,
				}

				select {
				case <-ctx.Done():
					return
				case draw <- b:
				}

				nframe++
			}
		}
	}()
	return draw
}

// drawANSIFrames encodes images received over frames as ANSI escape sequences
// using p and writes them to w.  drawANSIFrames does not use opts.Repeat.
func drawANSIFrames(ctx context.Context, w io.Writer, frames <-chan *ANSIFrame, opts *FrameOptions) error {
	animate := opts != nil && opts.Animate

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
	frameStart := time.Time{}
	_ = frameStart

	for {
		select {
		case <-ctx.Done():
			return nil
		case f, ok := <-frames:
			if !ok {
				return nil
			}

			if Debug && nframe == 0 {
				log.Printf("time to first frame: %s", time.Since(debugProcStartTime))
			}

			// Delay this animation frame before rendering by setting frameGate
			if animate && nframe > 0 {
				delay := time.Duration(opts.Delay) * time.Millisecond
				if delay == 0 {
					delay = f.Delay
				}
				if delay == 0 {
					delay = DelayDefault
				}
				delay -= time.Since(frameStart)
				frameGate = time.After(delay)
			}

			<-frameGate
			frameStart = time.Now()

			err := f.Buffer.FlushTo(w)
			if err != nil {
				return err
			}
		}
		nframe++
	}
}

func writeANSIPixels(w *frameBuffer, img image.Image, p ANSIPalette, pad string) {
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
}

func decodeFramesURL(ctx context.Context, urlstr string, fopts *FrameOptions) (<-chan *Frame, error) {
	u, err := url.Parse(urlstr)
	if err != nil {
		return nil, err
	}
	if u.Scheme == "" {
		return decodeFramesFile(ctx, urlstr, fopts)
	}
	if u.Scheme == "file" {
		return decodeFramesFile(ctx, u.Path, fopts)
	}
	if u.Scheme == "http" || u.Scheme == "https" {
		return decodeFramesHTTP(ctx, urlstr, fopts)
	}
	return nil, fmt.Errorf("unrecognized url: %v", urlstr)
}

func decodeFramesHTTP(ctx context.Context, u string, fopts *FrameOptions) (<-chan *Frame, error) {
	client := http.Client{
		Timeout: 10 * time.Second,
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
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
		return decodeFrames(ctx, resp.Body, fopts)
	default:
		return nil, fmt.Errorf("mime: %v %v", resp.Header.Get("Content-Type"), u)
	}
}

func decodeFramesFile(ctx context.Context, filename string, fopts *FrameOptions) (<-chan *Frame, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return decodeFrames(ctx, f, fopts)
}

func decodeFrames(ctx context.Context, r io.Reader, fopts *FrameOptions) (<-chan *Frame, error) {
	var confbuf bytes.Buffer
	_, format, err := image.DecodeConfig(io.TeeReader(r, &confbuf))
	if err != nil {
		return nil, err
	}
	r = io.MultiReader(&confbuf, r)
	if format == "gif" {
		return decodeFramesGIF(ctx, r, fopts)
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

func decodeFramesGIF(ctx context.Context, r io.Reader, fopts *FrameOptions) (<-chan *Frame, error) {
	img, err := gif.DecodeAll(r)
	if err != nil {
		return nil, err
	}

	renderer := newGIFRenderer(img, func(b image.Rectangle) draw.Image { return image.NewRGBA64(b) })
	for renderer.RenderNext() {
		select {
		case <-ctx.Done():
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
			case <-ctx.Done():
				return
			case c <- f:
			}
		}
	}()
	return c, nil
}
