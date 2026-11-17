package img

import (
	"image"
	"image/color"
)

// MaxDecodeMemory is the most memory, in bytes, decoding one image may be
// expected to need. Images estimated above it are refused.
//
// It replaces a limit on the pixel count alone: what a decoded image costs
// depends on its colour model as much as on its dimensions (a 16-bit RGBA
// pixel is eight bytes, a grayscale one is one), and a file's size says nothing
// about either. The value leaves room for the largest photographs in common
// use (a 50 megapixel 4:2:0 JPEG decodes to about 75 MiB) while bounding what
// the configured number of image workers can hold between them.
const MaxDecodeMemory = 128 << 20

// decodeCost estimates the memory needed to decode an image, from its
// configuration, its format and the leading bytes of the file (which, for the
// formats that matter here, hold everything the estimate uses).
func decodeCost(cfg image.Config, format string, header []byte) int64 {
	pixels := int64(cfg.Width) * int64(cfg.Height)

	switch format {
	case "jpeg":
		// The decoder stores one byte per sample, and the number of samples
		// per pixel depends on the chroma subsampling. A progressive image is
		// decoded through a table of 32-bit coefficients, one per sample, that
		// is held as well.
		samples, progressive := jpegSampling(header)
		if samples == 0 {
			switch cfg.ColorModel {
			case color.GrayModel:
				samples = 1
			case color.CMYKModel:
				samples = 4
			default:
				samples = 3
			}
		}
		cost := float64(pixels) * samples
		if progressive {
			cost *= 5
		}
		return int64(cost)
	case "png":
		cost := pixels * bytesPerPixel(cfg.ColorModel)
		// An interlaced image is decoded pass by pass into temporary images,
		// the largest of which is half the size of the result.
		if len(header) > 28 && header[28] == 1 {
			cost += cost / 2
		}
		return cost
	case "tiff":
		// The decoder reads a whole strip into memory before it copies it into
		// the image, and an image can be a single strip.
		return 2 * pixels * bytesPerPixel(cfg.ColorModel)
	default:
		return pixels * bytesPerPixel(cfg.ColorModel)
	}
}

// bytesPerPixel returns the size of one pixel of the image type the standard
// decoders produce for a colour model. An unknown model counts as the widest.
func bytesPerPixel(model color.Model) int64 {
	switch model {
	case color.GrayModel, color.AlphaModel:
		return 1
	case color.Gray16Model, color.Alpha16Model:
		return 2
	case color.YCbCrModel:
		return 3
	case color.RGBAModel, color.NRGBAModel, color.CMYKModel, color.NYCbCrAModel:
		return 4
	case color.RGBA64Model, color.NRGBA64Model:
		return 8
	}

	if _, ok := model.(color.Palette); ok {
		return 1
	}

	return 8
}

// jpegSampling reads the frame header of a JPEG stream and returns the number
// of samples the image has per pixel (1.5 for the usual 4:2:0 subsampling, 3
// for none) and whether it is progressive. It returns 0 samples when no frame
// header is found.
func jpegSampling(data []byte) (samples float64, progressive bool) {
	if len(data) < 4 || data[0] != 0xff || data[1] != 0xd8 {
		return 0, false
	}

	for i := 2; i+4 <= len(data); {
		if data[i] != 0xff {
			return 0, false
		}
		marker := data[i+1]
		if marker == 0xff {
			// Fill byte before a marker.
			i++
			continue
		}

		length := int(data[i+2])<<8 | int(data[i+3])
		if length < 2 {
			return 0, false
		}

		// SOF0 to SOF2 are the frame types the standard decoder supports.
		if marker >= 0xc0 && marker <= 0xc2 {
			segment := data[i+4:]
			if len(segment) < 6 {
				return 0, false
			}
			components := int(segment[5])
			if components == 0 || len(segment) < 6+3*components {
				return 0, false
			}

			var total, maxH, maxV int
			for c := 0; c < components; c++ {
				h := int(segment[6+3*c+1] >> 4)
				v := int(segment[6+3*c+1] & 0x0f)
				if h == 0 || v == 0 {
					return 0, false
				}
				total += h * v
				maxH = max(maxH, h)
				maxV = max(maxV, v)
			}

			return float64(total) / float64(maxH*maxV), marker == 0xc2
		}

		// Start of scan: the frame header always comes before it.
		if marker == 0xda {
			return 0, false
		}

		i += 2 + length
	}

	return 0, false
}
