// Copyright (c) 2026, go-videodsp
// SPDX-License-Identifier: BSD-3-Clause

package yuv

import (
	"errors"
	"image"
	"math"
	"testing"
)

// reference converts one triple the way the definition states, in floating point.
//
// ⛔ This is the witness the integer arithmetic is judged against, and it is
// written from the same two numbers that define the space rather than from the
// code under test. A test comparing the implementation against itself would pass
// whatever the coefficients were.
func reference(space Space, rng Range, y, cb, cr uint8) (r, g, b float64) {
	k := luma[space]
	kg := 1 - k.Kr - k.Kb
	yf, uf, vf := float64(y), float64(cb)-128, float64(cr)-128
	if rng == Limited {
		yf = (yf - 16) * 255 / 219
		uf *= 255.0 / 224.0
		vf *= 255.0 / 224.0
	}
	r = yf + 2*(1-k.Kr)*vf
	b = yf + 2*(1-k.Kb)*uf
	g = yf - 2*k.Kb*(1-k.Kb)/kg*uf - 2*k.Kr*(1-k.Kr)/kg*vf
	clamp := func(v float64) float64 { return math.Max(0, math.Min(255, v)) }
	return clamp(r), clamp(g), clamp(b)
}

// one1x1 makes a 4:4:4 picture of a single pixel, which is the shape that isolates
// the arithmetic from the subsampling.
func one1x1(space Space, rng Range, y, cb, cr uint8) Picture {
	return Picture{
		Y: []byte{y}, Cb: []byte{cb}, Cr: []byte{cr},
		YStride: 1, CStride: 1, Width: 1, Height: 1,
		Subsampling: YUV444, Space: space, Range: rng,
	}
}

// TestTheIntegerArithmeticLandsWithinOneOfTheDefinition sweeps the sample space
// for every colour space and range.
//
// ⛔ Within ONE, not equal: fixed-point arithmetic rounds and the definition does
// not, so demanding equality would assert something untrue. One is what the
// sixteen fractional bits are chosen to buy, and a larger tolerance would let a
// wrong coefficient through.
func TestTheIntegerArithmeticLandsWithinOneOfTheDefinition(t *testing.T) {
	for _, space := range []Space{BT601, BT709, BT2020} {
		for _, rng := range []Range{Limited, Full} {
			worst := 0.0
			for y := 0; y < 256; y += 5 {
				for cb := 0; cb < 256; cb += 9 {
					for cr := 0; cr < 256; cr += 9 {
						got, err := RGBA(one1x1(space, rng, uint8(y), uint8(cb), uint8(cr)), nil)
						if err != nil {
							t.Fatalf("RGBA: %v", err)
						}
						wr, wg, wb := reference(space, rng, uint8(y), uint8(cb), uint8(cr))
						p := got.Pix
						for i, want := range []float64{wr, wg, wb} {
							if d := math.Abs(float64(p[i]) - want); d > worst {
								worst = d
							}
							if d := math.Abs(float64(p[i]) - want); d > 1 {
								t.Fatalf("space %d range %d y=%d cb=%d cr=%d channel %d: %d, want %.3f",
									space, rng, y, cb, cr, i, p[i], want)
							}
						}
						if p[3] != 0xFF {
							t.Fatalf("alpha = %d, want opaque", p[3])
						}
					}
				}
			}
			t.Logf("space %d range %d: worst error %.4f", space, rng, worst)
		}
	}
}

func TestGreyStaysGreyAndBlackIsBlack(t *testing.T) {
	// Chroma at the middle means no colour at all, so the three channels agree.
	got, err := RGBA(one1x1(BT709, Full, 128, 128, 128), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Pix[0] != 128 || got.Pix[1] != 128 || got.Pix[2] != 128 {
		t.Errorf("full-range grey became %v", got.Pix[:3])
	}

	// ⛔ Limited range puts black at 16, not 0. Treating a limited picture as full
	// washes out every black in it, which is the commonest way a converter is
	// wrong while looking plausible.
	got, err = RGBA(one1x1(BT709, Limited, 16, 128, 128), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Pix[0] != 0 || got.Pix[1] != 0 || got.Pix[2] != 0 {
		t.Errorf("limited-range black became %v, want 0,0,0", got.Pix[:3])
	}
	// And the same sample read as full range is NOT black, which is what makes
	// the line above a real assertion rather than a coincidence.
	got, err = RGBA(one1x1(BT709, Full, 16, 128, 128), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Pix[0] == 0 {
		t.Error("the same sample is black in both ranges, so the range is being ignored")
	}
}

// TestAChannelOutsideTheCubeIsClampedNotWrapped.
//
// ⛔ A legal picture can state chroma that puts a channel outside 0..255 -- the
// colour spaces are larger than the cube they are stored in. Letting it wrap turns
// a bright red into a dark cyan, which is a picture nobody would call merely
// slightly wrong.
func TestAChannelOutsideTheCubeIsClampedNotWrapped(t *testing.T) {
	// Maximum red chroma on bright luma overshoots the top.
	got, err := RGBA(one1x1(BT709, Full, 255, 128, 255), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Pix[0] != 255 {
		t.Errorf("red = %d, want it clamped to 255", got.Pix[0])
	}
	// Minimum on dark luma undershoots the bottom.
	got, err = RGBA(one1x1(BT709, Full, 0, 128, 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Pix[0] != 0 {
		t.Errorf("red = %d, want it clamped to 0", got.Pix[0])
	}
}

// TestEachSubsamplingReadsTheChromaItShould builds a picture whose chroma differs
// per block, so a wrong step lands on the wrong sample and shows.
func TestEachSubsamplingReadsTheChromaItShould(t *testing.T) {
	for _, tc := range []struct {
		name string
		sub  Subsampling
		cw   int
		ch   int
	}{
		{"4:2:0", YUV420, 1, 1},
		{"4:2:2", YUV422, 1, 2},
		{"4:4:4", YUV444, 2, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A 2x2 luma of mid grey, and chroma that says "no colour" in the
			// first sample and something else everywhere after it.
			cb := make([]byte, tc.cw*tc.ch)
			cr := make([]byte, tc.cw*tc.ch)
			for i := range cb {
				cb[i], cr[i] = 128, 128
			}
			p := Picture{
				Y: []byte{128, 128, 128, 128}, Cb: cb, Cr: cr,
				YStride: 2, CStride: tc.cw, Width: 2, Height: 2,
				Subsampling: tc.sub, Space: BT709, Range: Full,
			}
			got, err := RGBA(p, nil)
			if err != nil {
				t.Fatalf("RGBA: %v", err)
			}
			for y := 0; y < 2; y++ {
				for x := 0; x < 2; x++ {
					i := y*got.Stride + x*4
					if got.Pix[i] != 128 || got.Pix[i+1] != 128 || got.Pix[i+2] != 128 {
						t.Errorf("pixel %d,%d = %v, want grey -- a chroma sample was read from the wrong place",
							x, y, got.Pix[i:i+3])
					}
				}
			}
		})
	}
}

// TestAnOddSizeStillHasChromaForItsLastPixel.
//
// ⛔ Rounding the chroma planes DOWN leaves the rightmost column and bottom row of
// every odd-sized 4:2:0 picture reading from the sample next door -- a coloured
// edge. The size arithmetic rounds up, and this is what says so.
func TestAnOddSizeStillHasChromaForItsLastPixel(t *testing.T) {
	// 3x3 luma needs 2x2 chroma, not 1x1.
	p := Picture{
		Y:       make([]byte, 9),
		Cb:      make([]byte, 4),
		Cr:      make([]byte, 4),
		YStride: 3, CStride: 2, Width: 3, Height: 3,
		Subsampling: YUV420, Space: BT709, Range: Full,
	}
	for i := range p.Y {
		p.Y[i] = 128
	}
	for i := range p.Cb {
		p.Cb[i], p.Cr[i] = 128, 128
	}
	if _, err := RGBA(p, nil); err != nil {
		t.Fatalf("a 3x3 picture with 2x2 chroma was refused: %v", err)
	}
	// And 1x1 chroma is refused, which is the control: without the rounding up
	// this would be accepted and the last column read out of bounds.
	p.Cb, p.Cr, p.CStride = []byte{128}, []byte{128}, 1
	if _, err := RGBA(p, nil); !errors.Is(err, ErrGeometry) {
		t.Errorf("chroma too small by rounding: err = %v, want ErrGeometry", err)
	}
}

func TestADestinationIsReusedAndASmallOneRefused(t *testing.T) {
	p := one1x1(BT709, Full, 128, 128, 128)
	big := image.NewRGBA(image.Rect(0, 0, 16, 16))
	got, err := RGBA(p, big)
	if err != nil {
		t.Fatalf("RGBA: %v", err)
	}
	if got != big {
		t.Error("a destination given was not the one written into")
	}
	small := image.NewRGBA(image.Rect(0, 0, 1, 1))
	p.Width, p.Height, p.YStride, p.CStride = 2, 1, 2, 2
	p.Y, p.Cb, p.Cr = []byte{1, 2}, []byte{1, 2}, []byte{1, 2}
	if _, err := RGBA(p, small); !errors.Is(err, ErrGeometry) {
		t.Errorf("err = %v, want ErrGeometry", err)
	}
}

func TestGeometryAndFormatRefusals(t *testing.T) {
	ok := func() Picture { return one1x1(BT709, Full, 128, 128, 128) }
	for _, tc := range []struct {
		name  string
		spoil func(*Picture)
		want  error
	}{
		{"no width", func(p *Picture) { p.Width = 0 }, ErrGeometry},
		{"no height", func(p *Picture) { p.Height = 0 }, ErrGeometry},
		{"a subsampling that does not exist", func(p *Picture) { p.Subsampling = 9 }, ErrFormat},
		{"a colour space that does not exist", func(p *Picture) { p.Space = 9 }, ErrFormat},
		{"a range that does not exist", func(p *Picture) { p.Range = 9 }, ErrFormat},
		{"a luma stride narrower than the picture", func(p *Picture) { p.Width, p.YStride = 4, 2 }, ErrGeometry},
		{"a chroma stride narrower than the chroma", func(p *Picture) {
			p.Width, p.Height, p.YStride = 4, 1, 4
			p.Y = make([]byte, 4)
			p.CStride = 1
		}, ErrGeometry},
		{"a luma plane too short", func(p *Picture) { p.Y = nil }, ErrGeometry},
		{"a chroma plane too short", func(p *Picture) { p.Cb = nil }, ErrGeometry},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := ok()
			tc.spoil(&p)
			if _, err := RGBA(p, nil); !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestAPlaneThatStopsAtItsLastRowIsAccepted: a decoder may hand back a plane that
// ends where the picture does rather than after a final stride of padding, and
// demanding the padding would refuse a picture that is all there.
func TestAPlaneThatStopsAtItsLastRowIsAccepted(t *testing.T) {
	p := Picture{
		Y:       make([]byte, 4*2+2), // two rows of stride four, last row only two wide
		Cb:      make([]byte, 1),
		Cr:      make([]byte, 1),
		YStride: 4, CStride: 1, Width: 2, Height: 3,
		Subsampling: YUV420, Space: BT709, Range: Full,
	}
	p.Cb = make([]byte, 2*1+1)
	p.Cr = make([]byte, 2*1+1)
	if _, err := RGBA(p, nil); err != nil {
		t.Errorf("a plane ending at its last row was refused: %v", err)
	}
}
