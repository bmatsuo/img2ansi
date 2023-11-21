package main

import (
	"image"
	"math"
)

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
