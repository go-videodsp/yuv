// Copyright (c) 2026, go-videodsp
// SPDX-License-Identifier: BSD-3-Clause

package yuv

import "image"

// luma is the pair of numbers that defines a colour space: how much of the luma
// comes from red and how much from blue. Everything else about the conversion
// follows from them, which is why they are all that is stored.
var luma = map[Space]struct{ Kr, Kb float64 }{
	BT601:  {0.299, 0.114},
	BT709:  {0.2126, 0.0722},
	BT2020: {0.2627, 0.0593},
}

// fracBits is how many fractional bits the fixed-point coefficients carry.
//
// Sixteen is chosen so that a coefficient times a sample cannot overflow an
// int32: the largest coefficient is under 2.1, the largest sample swing is 255,
// and 2.1 * 255 * 65536 is comfortably inside the range. A test asserts the
// arithmetic lands within one of the definition, which is what this buys.
const fracBits = 16

const one = 1 << fracBits

// coeffs is the conversion, as integers.
type coeffs struct {
	yMul, yOff int32 // luma scale and the value that is black
	rCr        int32
	gCb, gCr   int32
	bCb        int32
}

// coeffsFor derives the conversion from the colour space and the range.
//
// ⛔ Derived, not tabulated. A table of magic numbers has to be trusted and
// cannot be checked against anything; this can be read against the definition it
// comes from, and the tests do read it against a floating-point computation of
// the same definition.
func coeffsFor(space Space, rng Range) coeffs {
	k := luma[space]
	kg := 1 - k.Kr - k.Kb

	// The conversion in its defined form, on normalised values:
	//   R = Y + 2(1-Kr)·Cr
	//   B = Y + 2(1-Kb)·Cb
	//   G = Y - 2Kb(1-Kb)/Kg·Cb - 2Kr(1-Kr)/Kg·Cr
	rCr := 2 * (1 - k.Kr)
	bCb := 2 * (1 - k.Kb)
	gCb := 2 * k.Kb * (1 - k.Kb) / kg
	gCr := 2 * k.Kr * (1 - k.Kr) / kg

	// Limited range leaves headroom and footroom, so the samples have to be
	// stretched before the matrix is applied: luma spans 219 of the 255 values
	// and chroma 224 of them.
	yMul, cMul, yOff := 1.0, 1.0, 0.0
	if rng == Limited {
		yMul, cMul, yOff = 255.0/219.0, 255.0/224.0, 16
	}
	// Every coefficient here is positive, so the rounding needs no sign: for any
	// Kr and Kb the recommendations state -- both above zero and summing below
	// one -- all four chroma multipliers and the luma scale come out above zero.
	// A signed rounding would be a branch no colour space of this form can reach.
	round := func(f float64) int32 { return int32(f*one + 0.5) }
	return coeffs{
		yMul: round(yMul),
		yOff: int32(yOff),
		rCr:  round(rCr * cMul),
		gCb:  round(gCb * cMul),
		gCr:  round(gCr * cMul),
		bCb:  round(bCb * cMul),
	}
}

// clamp8 holds a value to what a byte can carry.
//
// ⛔ Clamping is not optional and not a nicety. A legal picture can state chroma
// that puts a channel outside the range -- the colour spaces are larger than the
// cube they are stored in -- so a conversion that let it wrap would turn a bright
// red into a dark cyan. Every channel goes through here.
func clamp8(v int32) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

// RGBA converts p into dst, which must be at least as large as the picture.
//
// ⛔ Chroma is REPLICATED, not filtered: each chroma sample colours the whole
// block of luma pixels it covers. That is a choice, and naming it here is the
// point -- a filtered upsampling is a different picture, not a better-rounded one.
//
// Measured against ffmpeg on a real 4:2:0 frame, the two differ by at most 3 with
// the difference sitting at 2, and the cause is this and not the arithmetic: on
// synthetic pictures with known answers, both this and ffmpeg land within half a
// level of the definition, with neutral chroma and with chroma pushed to the
// corners of the cube. Asking ffmpeg to stop filtering produced a byte-identical
// file, so its filtering could not be ruled out directly -- which is why this says
// "by elimination" rather than claiming to have proved it.
//
// Replication is what a decoder's preview path wants: it costs nothing and cannot
// invent an edge that was not coded. A filtered upsampling belongs beside it as
// another function, when something needs one.
//
// A nil dst is allocated at the picture's size, which is what a caller converting
// a decoded frame wants; a dst given is written into, which is what a caller
// converting a stream of frames wants so it allocates once.
func RGBA(p Picture, dst *image.RGBA) (*image.RGBA, error) {
	if err := p.check(); err != nil {
		return nil, err
	}
	if dst == nil {
		dst = image.NewRGBA(image.Rect(0, 0, p.Width, p.Height))
	} else if b := dst.Bounds(); b.Dx() < p.Width || b.Dy() < p.Height {
		return nil, ErrGeometry
	}
	c := coeffsFor(p.Space, p.Range)

	// How far down and across to step in the chroma planes for each luma pixel.
	shiftX, shiftY := 1, 1
	switch p.Subsampling {
	case YUV422:
		shiftY = 0
	case YUV444:
		shiftX, shiftY = 0, 0
	}

	for y := 0; y < p.Height; y++ {
		yRow := p.Y[y*p.YStride:]
		cRow := (y >> uint(shiftY)) * p.CStride
		cb := p.Cb[cRow:]
		cr := p.Cr[cRow:]
		out := dst.Pix[y*dst.Stride:]
		for x := 0; x < p.Width; x++ {
			cx := x >> uint(shiftX)
			// The luma is stretched from its stored range; the chroma is taken
			// as a signed offset from the middle of its.
			yv := (int32(yRow[x]) - c.yOff) * c.yMul
			u := int32(cb[cx]) - 128
			v := int32(cr[cx]) - 128

			i := x * 4
			out[i+0] = clamp8((yv + c.rCr*v + one/2) >> fracBits)
			out[i+1] = clamp8((yv - c.gCb*u - c.gCr*v + one/2) >> fracBits)
			out[i+2] = clamp8((yv + c.bCb*u + one/2) >> fracBits)
			out[i+3] = 0xFF
		}
	}
	return dst, nil
}
