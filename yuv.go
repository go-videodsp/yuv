// Copyright (c) 2026, go-videodsp
// SPDX-License-Identifier: BSD-3-Clause

// Package yuv converts the planar pictures a video decoder produces into RGBA,
// in pure Go with no libswscale or ffmpeg linkage.
//
// Every decoder ends here. A decoder hands back three planes of luma and chroma
// at whatever subsampling the stream used, and anything that shows or saves a
// picture wants interleaved RGBA -- so this is the one step no codec can avoid
// and none of them should each write again.
//
// The coefficients are DERIVED from the two numbers that define a colour space
// rather than kept as a table. A table has to be trusted; a derivation can be
// checked against the definition, and the tests do exactly that: they compute the
// conversion in floating point from the same two numbers and require the integer
// arithmetic here to land within one of it.
package yuv

import (
	"errors"
	"fmt"
)

// Errors a picture can be refused with.
var (
	// ErrGeometry means a plane is too small for the size claimed, or a size is
	// not one the subsampling can express.
	ErrGeometry = errors.New("yuv: plane geometry does not match the picture")
	// ErrFormat means the subsampling, colour space or range is not one of those
	// named here.
	ErrFormat = errors.New("yuv: unknown format")
)

// Subsampling is how much smaller the chroma planes are than the luma plane.
type Subsampling uint8

// The subsamplings a decoder produces.
const (
	// YUV420 halves the chroma planes in both directions, which is what almost
	// every stream uses.
	YUV420 Subsampling = iota
	// YUV422 halves them horizontally only.
	YUV422
	// YUV444 keeps them full size.
	YUV444
)

// Space is the colour space the luma was formed with.
type Space uint8

// The colour spaces, named by the recommendation that defines them.
const (
	// BT601 is standard definition.
	BT601 Space = iota
	// BT709 is high definition, and what almost every stream above 576 lines
	// states or is assumed to use.
	BT709
	// BT2020 is ultra high definition, non-constant luminance.
	BT2020
)

// Range says which values the samples actually use.
type Range uint8

// The two ranges.
const (
	// Limited leaves headroom and footroom: luma runs 16 to 235 and chroma 16 to
	// 240. It is what a stream uses unless it says otherwise, and treating it as
	// full washes out the blacks.
	Limited Range = iota
	// Full uses every value from 0 to 255.
	Full
)

// Picture is a planar picture as a decoder hands it back.
//
// Strides are in bytes and may exceed the width: a decoder allocates rows wide
// enough for its own alignment, and copying a picture to tighten them is the cost
// this avoids.
type Picture struct {
	Y, Cb, Cr     []byte
	YStride       int
	CStride       int
	Width, Height int
	Subsampling   Subsampling
	Space         Space
	Range         Range
}

// chromaSize is how large the chroma planes are for this subsampling.
//
// ⛔ The rounding is UP in both directions. An odd width in 4:2:0 still needs a
// chroma column for its last pixel, and rounding down leaves the rightmost column
// of every odd-width picture reading chroma from the row below -- a green or
// magenta edge, which is what this arithmetic exists to prevent.
func (p Picture) chromaSize() (w, h int) {
	switch p.Subsampling {
	case YUV420:
		return (p.Width + 1) / 2, (p.Height + 1) / 2
	case YUV422:
		return (p.Width + 1) / 2, p.Height
	default:
		return p.Width, p.Height
	}
}

// check says whether the planes can hold the picture claimed.
func (p Picture) check() error {
	if p.Width <= 0 || p.Height <= 0 {
		return fmt.Errorf("%w: %dx%d", ErrGeometry, p.Width, p.Height)
	}
	if p.Subsampling > YUV444 {
		return fmt.Errorf("%w: subsampling %d", ErrFormat, p.Subsampling)
	}
	if p.Space > BT2020 {
		return fmt.Errorf("%w: colour space %d", ErrFormat, p.Space)
	}
	if p.Range > Full {
		return fmt.Errorf("%w: range %d", ErrFormat, p.Range)
	}
	if p.YStride < p.Width {
		return fmt.Errorf("%w: luma stride %d is narrower than %d", ErrGeometry, p.YStride, p.Width)
	}
	cw, ch := p.chromaSize()
	if p.CStride < cw {
		return fmt.Errorf("%w: chroma stride %d is narrower than %d", ErrGeometry, p.CStride, cw)
	}
	// The last row needs only as many bytes as the picture is wide, not a whole
	// stride: a decoder may hand back a plane that stops at the end of its last
	// row, and demanding the padding would refuse a picture that is all there.
	if need := p.YStride*(p.Height-1) + p.Width; len(p.Y) < need {
		return fmt.Errorf("%w: luma plane holds %d bytes, needs %d", ErrGeometry, len(p.Y), need)
	}
	need := p.CStride*(ch-1) + cw
	if len(p.Cb) < need || len(p.Cr) < need {
		return fmt.Errorf("%w: chroma planes hold %d and %d bytes, need %d",
			ErrGeometry, len(p.Cb), len(p.Cr), need)
	}
	return nil
}
