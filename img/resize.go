package img

import (
	"context"
	"image"
	"image/color"
	"math"
)

// This file resizes images. It replaces a library that did the work in
// goroutines of its own: a panic there cannot be recovered by the caller, and
// ends the process. Everything here runs in the goroutine that calls it, so
// Service.Resize can recover from whatever a malformed image makes it do.

// kernel is a resampling filter: the weight of a source sample at a distance
// from the centre of a destination pixel, and how far the filter reaches.
// A kernel without support stands for nearest-neighbour sampling.
type kernel struct {
	support float64
	at      func(x float64) float64
}

var (
	nearestKernel = kernel{}

	// boxKernel averages the source pixels a destination pixel covers.
	boxKernel = kernel{support: 0.5, at: func(x float64) float64 {
		if math.Abs(x) <= 0.5 {
			return 1
		}
		return 0
	}}

	// lanczosKernel is the three-lobe Lanczos filter.
	lanczosKernel = kernel{support: 3, at: func(x float64) float64 {
		x = math.Abs(x)
		if x >= 3 {
			return 0
		}
		return sinc(x) * sinc(x/3)
	}}
)

func sinc(x float64) float64 {
	if x == 0 {
		return 1
	}
	return math.Sin(math.Pi*x) / (math.Pi * x)
}

// fitSize returns the size of an image of srcW x srcH scaled down to fit
// within maxW x maxH with its aspect ratio kept. An image that already fits
// keeps its size: Fit never enlarges.
func fitSize(srcW, srcH, maxW, maxH int) (w, h int) {
	if srcW <= maxW && srcH <= maxH {
		return srcW, srcH
	}

	// The side that is not pinned to the box is rounded down, so that the
	// result never sticks out of it.
	srcAspect := float64(srcW) / float64(srcH)
	maxAspect := float64(maxW) / float64(maxH)
	if srcAspect > maxAspect {
		w = maxW
		h = int(float64(w) / srcAspect)
	} else {
		h = maxH
		w = int(float64(h) * srcAspect)
	}

	return max(w, 1), max(h, 1)
}

// fillCrop returns the part of an image of srcW x srcH, around its centre,
// that has the aspect ratio of dstW x dstH: what remains when the image is
// scaled to cover that size and the overhang is cut off.
//
// Cropping before scaling, rather than after, means nothing larger than the
// result is ever built. Scaling a 1 x 10000 image to cover 256 x 256 first
// would make it 256 x 2560000.
func fillCrop(srcW, srcH, dstW, dstH int) image.Rectangle {
	srcAspect := float64(srcW) / float64(srcH)
	dstAspect := float64(dstW) / float64(dstH)

	cropW, cropH := srcW, srcH
	if srcAspect > dstAspect {
		cropW = int(math.Round(float64(srcH) * dstAspect))
	} else {
		cropH = int(math.Round(float64(srcW) / dstAspect))
	}
	cropW = min(max(cropW, 1), srcW)
	cropH = min(max(cropH, 1), srcH)

	x0 := (srcW - cropW) / 2
	y0 := (srcH - cropH) / 2
	return image.Rect(x0, y0, x0+cropW, y0+cropH)
}

// contribution is one source sample's share in a destination sample.
type contribution struct {
	index  int
	weight float64
}

// contributions returns, for every destination sample along one axis, the
// source samples it is made of and their weights, which add up to one.
func contributions(dstSize, srcSize int, k kernel) [][]contribution {
	ratio := float64(srcSize) / float64(dstSize)
	// When shrinking, the filter is widened to cover every source sample that
	// falls into a destination sample.
	scale := math.Max(ratio, 1)
	reach := math.Ceil(scale * k.support)

	out := make([][]contribution, dstSize)
	for v := range out {
		centre := (float64(v)+0.5)*ratio - 0.5
		begin := max(int(math.Ceil(centre-reach)), 0)
		end := min(int(math.Floor(centre+reach)), srcSize-1)

		var sum float64
		list := make([]contribution, 0, end-begin+1)
		for u := begin; u <= end; u++ {
			if w := k.at((float64(u) - centre) / scale); w != 0 {
				sum += w
				list = append(list, contribution{index: u, weight: w})
			}
		}
		if sum != 0 {
			for i := range list {
				list[i].weight /= sum
			}
		}
		out[v] = list
	}

	return out
}

func clampToByte(x float64) uint8 {
	v := int64(x + 0.5)
	if v > 255 {
		return 255
	}
	if v > 0 {
		return uint8(v)
	}
	return 0
}

// resample scales the part of src within crop, which is given relative to the
// top left corner of the image, to dstW x dstH.
func resample(ctx context.Context, src image.Image, crop image.Rectangle, dstW, dstH int, k kernel) (*image.NRGBA, error) {
	if crop.Empty() || dstW <= 0 || dstH <= 0 {
		return image.NewNRGBA(image.Rectangle{}), nil
	}

	rows := newRowReader(src)

	if k.support <= 0 {
		return resampleNearest(ctx, rows, crop, dstW, dstH)
	}

	// Horizontally first, row by row, into an image that is as wide as the
	// result and as tall as the source; then vertically.
	across := contributions(dstW, crop.Dx(), k)
	narrow := image.NewNRGBA(image.Rect(0, 0, dstW, crop.Dy()))
	row := make([]uint8, crop.Dx()*4)
	for y := 0; y < crop.Dy(); y++ {
		if y&63 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		rows.read(crop.Min.X, crop.Max.X, crop.Min.Y+y, row)
		blend(narrow.Pix[y*narrow.Stride:], 4, row, 4, across)
	}

	if dstH == crop.Dy() {
		return narrow, nil
	}

	down := contributions(dstH, crop.Dy(), k)
	dst := image.NewNRGBA(image.Rect(0, 0, dstW, dstH))
	for x := 0; x < dstW; x++ {
		if x&63 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		blend(dst.Pix[x*4:], dst.Stride, narrow.Pix[x*4:], narrow.Stride, down)
	}

	return dst, nil
}

// blend computes one line of destination pixels from one line of source
// pixels. The lines are in NRGBA order; their pixels are dstStep and srcStep
// bytes apart, so a line can be a row or a column. Colours are weighted by
// their alpha, so that transparent pixels do not bleed their colour into
// their neighbours.
func blend(dst []uint8, dstStep int, src []uint8, srcStep int, weights [][]contribution) {
	for i, list := range weights {
		var r, g, b, a float64
		for _, c := range list {
			s := src[c.index*srcStep : c.index*srcStep+4 : c.index*srcStep+4]
			aw := float64(s[3]) * c.weight
			r += float64(s[0]) * aw
			g += float64(s[1]) * aw
			b += float64(s[2]) * aw
			a += aw
		}
		if a != 0 {
			inverse := 1 / a
			d := dst[i*dstStep : i*dstStep+4 : i*dstStep+4]
			d[0] = clampToByte(r * inverse)
			d[1] = clampToByte(g * inverse)
			d[2] = clampToByte(b * inverse)
			d[3] = clampToByte(a)
		}
	}
}

// resampleNearest scales by taking, for every destination pixel, the source
// pixel under its centre.
func resampleNearest(ctx context.Context, rows *rowReader, crop image.Rectangle, dstW, dstH int) (*image.NRGBA, error) {
	dst := image.NewNRGBA(image.Rect(0, 0, dstW, dstH))
	dx := float64(crop.Dx()) / float64(dstW)
	dy := float64(crop.Dy()) / float64(dstH)

	columns := make([]int, dstW)
	for x := range columns {
		columns[x] = min(int((float64(x)+0.5)*dx), crop.Dx()-1)
	}

	row := make([]uint8, crop.Dx()*4)
	for y := 0; y < dstH; y++ {
		if y&63 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		srcY := crop.Min.Y + min(int((float64(y)+0.5)*dy), crop.Dy()-1)
		rows.read(crop.Min.X, crop.Max.X, srcY, row)

		line := dst.Pix[y*dst.Stride:]
		for x, srcX := range columns {
			copy(line[x*4:x*4+4], row[srcX*4:srcX*4+4])
		}
	}

	return dst, nil
}

// rowReader reads rows of an image as 8-bit, non-premultiplied RGBA.
type rowReader struct {
	img     image.Image
	origin  image.Point
	palette []color.NRGBA
}

func newRowReader(img image.Image) *rowReader {
	r := &rowReader{img: img, origin: img.Bounds().Min}
	if p, ok := img.(*image.Paletted); ok {
		r.palette = make([]color.NRGBA, len(p.Palette))
		for i, c := range p.Palette {
			r.palette[i] = color.NRGBAModel.Convert(c).(color.NRGBA)
		}
	}
	return r
}

// read fills dst with the pixels x0 to x1 (exclusive) of row y. Coordinates
// are relative to the top left corner of the image.
//
// The image types the decoders produce for photographs and screenshots are
// read directly from their buffers; everything else goes through the image's
// own colour conversion.
func (r *rowReader) read(x0, x1, y int, dst []uint8) {
	switch img := r.img.(type) {
	case *image.NRGBA:
		i := y*img.Stride + x0*4
		copy(dst, img.Pix[i:i+(x1-x0)*4])

	case *image.RGBA:
		i := y*img.Stride + x0*4
		for j := 0; j < (x1-x0)*4; j, i = j+4, i+4 {
			s := img.Pix[i : i+4 : i+4]
			d := dst[j : j+4 : j+4]
			switch a := s[3]; a {
			case 0:
				d[0], d[1], d[2], d[3] = 0, 0, 0, 0
			case 0xff:
				d[0], d[1], d[2], d[3] = s[0], s[1], s[2], 0xff
			default:
				// Undo the premultiplication, as color.NRGBAModel does.
				a16 := uint32(a) * 0x101
				d[0] = uint8((uint32(s[0]) * 0x101 * 0xffff / a16) >> 8)
				d[1] = uint8((uint32(s[1]) * 0x101 * 0xffff / a16) >> 8)
				d[2] = uint8((uint32(s[2]) * 0x101 * 0xffff / a16) >> 8)
				d[3] = a
			}
		}

	case *image.NRGBA64:
		i := y*img.Stride + x0*8
		for j := 0; j < (x1-x0)*4; j, i = j+4, i+8 {
			s := img.Pix[i : i+8 : i+8]
			dst[j], dst[j+1], dst[j+2], dst[j+3] = s[0], s[2], s[4], s[6]
		}

	case *image.Gray:
		i := y*img.Stride + x0
		for j := 0; j < (x1-x0)*4; j, i = j+4, i+1 {
			v := img.Pix[i]
			dst[j], dst[j+1], dst[j+2], dst[j+3] = v, v, v, 0xff
		}

	case *image.Gray16:
		i := y*img.Stride + x0*2
		for j := 0; j < (x1-x0)*4; j, i = j+4, i+2 {
			v := img.Pix[i]
			dst[j], dst[j+1], dst[j+2], dst[j+3] = v, v, v, 0xff
		}

	case *image.YCbCr:
		ax, ay := x0+img.Rect.Min.X, y+img.Rect.Min.Y
		for j := 0; j < (x1-x0)*4; j, ax = j+4, ax+1 {
			yi := img.YOffset(ax, ay)
			ci := img.COffset(ax, ay)
			red, green, blue := color.YCbCrToRGB(img.Y[yi], img.Cb[ci], img.Cr[ci])
			dst[j], dst[j+1], dst[j+2], dst[j+3] = red, green, blue, 0xff
		}

	case *image.Paletted:
		i := y*img.Stride + x0
		for j := 0; j < (x1-x0)*4; j, i = j+4, i+1 {
			c := r.palette[img.Pix[i]]
			dst[j], dst[j+1], dst[j+2], dst[j+3] = c.R, c.G, c.B, c.A
		}

	default:
		ay := y + r.origin.Y
		for j, ax := 0, x0+r.origin.X; j < (x1-x0)*4; j, ax = j+4, ax+1 {
			c := color.NRGBAModel.Convert(r.img.At(ax, ay)).(color.NRGBA)
			dst[j], dst[j+1], dst[j+2], dst[j+3] = c.R, c.G, c.B, c.A
		}
	}
}

// orient returns img turned and mirrored the way the EXIF orientation o says a
// viewer has to, to show it upright. Orientation 1, and anything unknown,
// leaves it as it is.
func orient(img *image.NRGBA, o int) *image.NRGBA {
	if o < 2 || o > 8 {
		return img
	}

	w, h := img.Rect.Dx(), img.Rect.Dy()
	dstW, dstH := w, h
	if o >= 5 {
		dstW, dstH = h, w
	}
	dst := image.NewNRGBA(image.Rect(0, 0, dstW, dstH))

	for y := 0; y < dstH; y++ {
		for x := 0; x < dstW; x++ {
			var sx, sy int
			switch o {
			case 2: // mirrored left to right
				sx, sy = w-1-x, y
			case 3: // upside down
				sx, sy = w-1-x, h-1-y
			case 4: // mirrored top to bottom
				sx, sy = x, h-1-y
			case 5: // mirrored along the top left to bottom right diagonal
				sx, sy = y, x
			case 6: // lying on its left side: turn it a quarter clockwise
				sx, sy = y, h-1-x
			case 7: // mirrored along the other diagonal
				sx, sy = w-1-y, h-1-x
			case 8: // lying on its right side: turn it a quarter anticlockwise
				sx, sy = w-1-y, x
			}
			s := img.Pix[sy*img.Stride+sx*4:]
			d := dst.Pix[y*dst.Stride+x*4:]
			d[0], d[1], d[2], d[3] = s[0], s[1], s[2], s[3]
		}
	}

	return dst
}

// jpegOrientation returns the EXIF orientation (1 to 8) recorded in the
// leading bytes of a JPEG file, or 0 when there is none.
func jpegOrientation(data []byte) int {
	if len(data) < 4 || data[0] != 0xff || data[1] != 0xd8 {
		return 0
	}

	for i := 2; i+4 <= len(data); {
		if data[i] != 0xff {
			return 0
		}
		marker := data[i+1]
		if marker == 0xff {
			i++
			continue
		}
		// The image data starts here; the metadata comes before it.
		if marker == 0xda || marker == 0xd9 {
			return 0
		}

		length := int(data[i+2])<<8 | int(data[i+3])
		if length < 2 {
			return 0
		}
		segment := data[i+4:]
		if length-2 < len(segment) {
			segment = segment[:length-2]
		}
		if marker == 0xe1 && len(segment) >= 6 && string(segment[:6]) == "Exif\x00\x00" {
			return tiffOrientation(segment[6:])
		}

		i += 2 + length
	}

	return 0
}

// tiffOrientation reads the Orientation tag from the first image directory of
// a TIFF structure, which is what an EXIF block is.
func tiffOrientation(tiff []byte) int {
	if len(tiff) < 8 {
		return 0
	}

	var u16 func([]byte) int
	var u32 func([]byte) int
	switch string(tiff[:2]) {
	case "II":
		u16 = func(b []byte) int { return int(b[0]) | int(b[1])<<8 }
		u32 = func(b []byte) int { return int(b[0]) | int(b[1])<<8 | int(b[2])<<16 | int(b[3])<<24 }
	case "MM":
		u16 = func(b []byte) int { return int(b[1]) | int(b[0])<<8 }
		u32 = func(b []byte) int { return int(b[3]) | int(b[2])<<8 | int(b[1])<<16 | int(b[0])<<24 }
	default:
		return 0
	}
	if u16(tiff[2:]) != 42 {
		return 0
	}

	dir := u32(tiff[4:])
	if dir < 8 || dir+2 > len(tiff) {
		return 0
	}
	count := u16(tiff[dir:])
	for n := 0; n < count; n++ {
		entry := dir + 2 + n*12
		if entry+12 > len(tiff) {
			return 0
		}
		if u16(tiff[entry:]) != 0x0112 {
			continue
		}
		// A single SHORT, stored in the entry itself.
		if u16(tiff[entry+2:]) != 3 || u32(tiff[entry+4:]) != 1 {
			return 0
		}
		if o := u16(tiff[entry+8:]); o >= 1 && o <= 8 {
			return o
		}
		return 0
	}

	return 0
}
