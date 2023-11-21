package main

import (
	"bytes"
	"io"
)

type frameBuffer struct {
	w io.Writer
	b []byte
}

func newFrameBuffer(w io.Writer) *frameBuffer {
	return &frameBuffer{w: w}
}

func (b *frameBuffer) Write(p []byte) (int, error) {
	b.b = append(b.b, p...)
	return len(p), nil
}

func (b *frameBuffer) WriteString(s string) (int, error) {
	m, ok := b.tryGrowByReslice(len(s))
	if ok {
		return copy(b.b[m:], s), nil
	}
	b.b = append(b.b, []byte(s)...)
	return len(s), nil
}

func (b *frameBuffer) Flush() error {
	_, err := io.Copy(b.w, bytes.NewReader(b.b))
	if err != nil {
		return err
	}
	b.b = b.b[:0]
	return nil
}

// tryGrowByReslice is an inlineable version of grow for the fast-case where the
// internal buffer only needs to be resliced.
// It returns the index where bytes should be written and whether it succeeded.
func (b *frameBuffer) tryGrowByReslice(n int) (int, bool) {
	if l := len(b.b); n <= cap(b.b)-l {
		b.b = b.b[:l+n]
		return l, true
	}
	return 0, false
}
