package main

import (
	"image"
	"image/color"
	"image/draw"

	"github.com/bmatsuo/img2ansi/gif"
)

type gifrenderer struct {
	GIF    *gif.GIF
	Draw   func(bounds image.Rectangle) draw.Image
	Frames []image.Image
	frame  draw.Image
	index  int
}

func newGIFRenderer(g *gif.GIF, draw func(image.Rectangle) draw.Image) *gifrenderer {
	return &gifrenderer{
		GIF:   g,
		Draw:  draw,
		index: -1,
	}
}

func (r *gifrenderer) Frame() image.Image {
	return r.Frames[r.index]
}

func (r *gifrenderer) RenderAll() {
	for r.RenderNext() {
	}
}

func (r *gifrenderer) RenderNext() (ok bool) {
	i := (r.index + 1) % len(r.GIF.Image)
	r.index = i
	if len(r.Frames) <= i {
		r.renderFrame(i)
		return true
	}
	return false
}

func (r *gifrenderer) renderFrame(i int) {
	m := r.GIF.Image[i]
	bounds := image.Rect(0, 0, r.GIF.Config.Width, r.GIF.Config.Height)
	if i == 0 {
		disposal := r.GIF.Disposal[len(r.GIF.Image)-1]
		r.frame = r.Draw(bounds)
		var fill image.Image
		if disposal == gif.DisposalBackground {
			fill = image.NewUniform(r.GIF.Config.ColorModel.(color.Palette)[r.GIF.BackgroundIndex])
		} else {
			fill = image.NewUniform(color.Transparent)
		}
		draw.Draw(r.frame, bounds, fill, bounds.Min, draw.Src)
	} else {
		disposal := r.GIF.Disposal[i-1]
		// disposal unspecified and DisposalNone are handled the same way, leave the frame as it is
		if disposal == gif.DisposalBackground {
			c := r.GIF.Config.ColorModel.(color.Palette)[r.GIF.BackgroundIndex]
			img := image.NewUniform(c)
			draw.Draw(r.frame, bounds, img, bounds.Min, draw.Src)
		} else if disposal == gif.DisposalPrevious {
			fill := image.NewUniform(color.Transparent)
			draw.Draw(r.frame, bounds, fill, bounds.Min, draw.Src)
		}
	}

	// draw the image over the virtual screen.  transparency is checked
	// directly instead of blending because GIF89a has binary
	// transparency (no alpha channel).
	for x := m.Rect.Min.X; x < m.Rect.Max.X; x++ {
		for y := m.Rect.Min.Y; y < m.Rect.Max.Y; y++ {
			color := m.ColorIndexAt(x, y)
			if r.GIF.HasTransparent[i] && color == r.GIF.Transparent[i] {
				continue
			}
			r.frame.Set(x, y, m.Palette[color])
		}
	}

	framecp := r.Draw(bounds)
	draw.Draw(framecp, bounds, r.frame, bounds.Min, draw.Over)
	r.Frames = append(r.Frames, framecp)
}

func (r *gifrenderer) RenderFrames() {
	for i := range r.GIF.Image {
		r.renderFrame(i)
	}
}
