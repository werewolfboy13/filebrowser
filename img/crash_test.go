package img

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"runtime"
	"testing"
)

// palettedBMP returns an 8-bit paletted BMP, w x h pixels, whose palette has a
// single entry and whose pixels all carry the given index. With an index
// other than 0 the file refers to a colour it does not define. The decoder
// accepts it as it is: it produces an image whose pixels point past the end
// of its palette.
func palettedBMP(w, h int, index byte) []byte {
	const (
		fileHeader = 14
		infoHeader = 40
		paletteLen = 4 // one BGRA entry
	)
	stride := (w + 3) &^ 3
	offset := fileHeader + infoHeader + paletteLen
	size := offset + stride*h

	b := make([]byte, size)
	b[0], b[1] = 'B', 'M'
	binary.LittleEndian.PutUint32(b[2:], uint32(size))
	binary.LittleEndian.PutUint32(b[10:], uint32(offset))
	binary.LittleEndian.PutUint32(b[14:], infoHeader)
	binary.LittleEndian.PutUint32(b[18:], uint32(w))
	binary.LittleEndian.PutUint32(b[22:], uint32(h))
	binary.LittleEndian.PutUint16(b[26:], 1) // planes
	binary.LittleEndian.PutUint16(b[28:], 8) // bits per pixel
	binary.LittleEndian.PutUint32(b[46:], 1) // colours used
	for i := offset; i < size; i++ {
		b[i] = index
	}
	return b
}

// palettedTIFF is the same idea as a TIFF: an 8-bit palette image, w x h, with
// a colour map of two entries and pixels that all carry the given index.
func palettedTIFF(w, h int, index byte) []byte {
	const (
		tShort = 3
		tLong  = 4
	)
	type entry struct {
		tag, typ uint16
		count    uint32
		value    uint32
	}

	pixels := bytes.Repeat([]byte{index}, w*h)
	colorMap := make([]byte, 6*2) // two colours, three channels, 16 bits each

	entries := []entry{
		{256, tLong, 1, uint32(w)},                  // ImageWidth
		{257, tLong, 1, uint32(h)},                  // ImageLength
		{258, tShort, 1, 8},                         // BitsPerSample
		{259, tShort, 1, 1},                         // Compression: none
		{262, tShort, 1, 3},                         // PhotometricInterpretation: palette
		{273, tLong, 1, 0},                          // StripOffsets, filled in below
		{278, tLong, 1, uint32(h)},                  // RowsPerStrip
		{279, tLong, 1, uint32(len(pixels))},        // StripByteCounts
		{320, tShort, uint32(len(colorMap)) / 2, 0}, // ColorMap, filled in below
	}

	ifdOffset := 8
	dataOffset := ifdOffset + 2 + len(entries)*12 + 4
	entries[5].value = uint32(dataOffset)
	entries[8].value = uint32(dataOffset + len(pixels))

	var out bytes.Buffer
	out.WriteString("II*\x00")
	_ = binary.Write(&out, binary.LittleEndian, uint32(ifdOffset))
	_ = binary.Write(&out, binary.LittleEndian, uint16(len(entries)))
	for _, e := range entries {
		_ = binary.Write(&out, binary.LittleEndian, e.tag)
		_ = binary.Write(&out, binary.LittleEndian, e.typ)
		_ = binary.Write(&out, binary.LittleEndian, e.count)
		if e.typ == tShort && e.count == 1 {
			_ = binary.Write(&out, binary.LittleEndian, uint16(e.value))
			_ = binary.Write(&out, binary.LittleEndian, uint16(0))
		} else {
			_ = binary.Write(&out, binary.LittleEndian, e.value)
		}
	}
	_ = binary.Write(&out, binary.LittleEndian, uint32(0)) // no further IFD
	out.Write(pixels)
	out.Write(colorMap)
	return out.Bytes()
}

// crashers are image files that are valid enough to be decoded, and whose
// decoded form then trips up code that trusts it.
func crashers() map[string][]byte {
	return map[string][]byte{
		"BMP with a pixel outside its palette, small":  palettedBMP(8, 8, 200),
		"BMP with a pixel outside its palette, large":  palettedBMP(1200, 900, 200),
		"TIFF with a pixel outside its palette, small": palettedTIFF(8, 8, 200),
		"TIFF with a pixel outside its palette, large": palettedTIFF(1200, 900, 200),
	}
}

// Image files that refer to colours they do not define are an error like any
// other unusable image. The decoders reject them today; an image like that
// which reached the resizing code used to end the process (see
// TestResizeSurvivesPanics).
func TestCraftedImagesAreAnError(t *testing.T) {
	for name, data := range crashers() {
		t.Run(name, func(t *testing.T) {
			for _, options := range [][]Option{
				{WithMode(ResizeModeFit), WithQuality(QualityMedium)},                       // the "big" preview
				{WithMode(ResizeModeFill), WithQuality(QualityLow), WithFormat(FormatJpeg)}, // the thumbnail
			} {
				err := New(1).Resize(context.Background(), bytes.NewReader(data), 256, 256, io.Discard, options...)
				if err == nil {
					t.Error("an image with pixels outside its palette was resized without an error")
				}
			}
		})
	}
}

// The library that used to do the resizing ran it in goroutines of its own. A
// panic there cannot be recovered by the caller, nor by the HTTP server, which
// only guards the goroutine of the request: it ends the process. One image
// that made the resizing code stumble would have taken the server down, and
// again after every restart, for as long as someone listed its directory.
//
// Decoding and resizing now run in the calling goroutine, and Resize turns a
// panic in either into an error.
func TestResizeSurvivesPanics(t *testing.T) {
	valid := func() []byte {
		var buf bytes.Buffer
		if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, 300, 200))); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}()

	// An image as no decoder should produce it: every pixel points past the
	// end of the palette. Reading any of them panics.
	broken := image.NewPaletted(image.Rect(0, 0, 300, 200), color.Palette{color.Black})
	for i := range broken.Pix {
		broken.Pix[i] = 200
	}

	decoders := map[string]func(io.Reader) (image.Image, string, error){
		"the decoder panics": func(io.Reader) (image.Image, string, error) {
			panic("decoder bug")
		},
		"the decoder returns an inconsistent image": func(io.Reader) (image.Image, string, error) {
			return broken, "png", nil
		},
		"the decoder returns an image that panics when read": func(io.Reader) (image.Image, string, error) {
			return panickyImage{}, "png", nil
		},
	}

	original := decodeImage
	defer func() { decodeImage = original }()

	svc := New(1)
	for name, decoder := range decoders {
		t.Run(name, func(t *testing.T) {
			for _, options := range [][]Option{
				{WithMode(ResizeModeFit), WithQuality(QualityMedium)},
				{WithMode(ResizeModeFit), WithQuality(QualityHigh)},
				{WithMode(ResizeModeFill), WithQuality(QualityLow), WithFormat(FormatJpeg)},
			} {
				decodeImage = decoder
				err := svc.Resize(context.Background(), bytes.NewReader(valid), 100, 100, io.Discard, options...)
				if !errors.Is(err, ErrInvalidImage) {
					t.Errorf("Resize returned %v, want ErrInvalidImage", err)
				}

				// The worker is free again, and the next image is resized.
				decodeImage = original
				var out bytes.Buffer
				if err := svc.Resize(context.Background(), bytes.NewReader(valid), 100, 100, &out, options...); err != nil {
					t.Fatalf("Resize of a good image after a bad one: %v", err)
				}
				if out.Len() == 0 {
					t.Error("no output for a good image after a bad one")
				}
			}
		})
	}
}

// panickyImage is an image whose pixels cannot be read.
type panickyImage struct{}

func (panickyImage) ColorModel() color.Model { return color.NRGBAModel }
func (panickyImage) Bounds() image.Rectangle { return image.Rect(0, 0, 300, 200) }
func (panickyImage) At(int, int) color.Color { panic("cannot read this pixel") }

// A thumbnail is made by scaling the image until it covers the thumbnail and
// cutting off the overhang. Done in that order, a 1 x 10000 image (a hundred
// bytes as a PNG) became 256 x 2560000 on the way: 2.5 GiB for one request,
// enough to get a container with a memory limit killed. The overhang is cut
// off first now.
func TestThumbnailOfAThinImageIsCheap(t *testing.T) {
	for name, size := range map[string]image.Point{"tall": {1, 10000}, "wide": {10000, 1}, "tiny": {1, 1}} {
		t.Run(name, func(t *testing.T) {
			var file bytes.Buffer
			if err := png.Encode(&file, image.NewGray(image.Rect(0, 0, size.X, size.Y))); err != nil {
				t.Fatal(err)
			}

			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)

			var out bytes.Buffer
			err := New(1).Resize(context.Background(), bytes.NewReader(file.Bytes()), 256, 256, &out,
				WithMode(ResizeModeFill), WithQuality(QualityLow), WithFormat(FormatJpeg))

			runtime.ReadMemStats(&after)
			if err != nil {
				t.Fatal(err)
			}
			if allocated := (after.TotalAlloc - before.TotalAlloc) >> 20; allocated > 32 {
				t.Errorf("the thumbnail of a %dx%d image allocated %d MiB", size.X, size.Y, allocated)
			}

			cfg, _, err := image.DecodeConfig(&out)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Width != 256 || cfg.Height != 256 {
				t.Errorf("thumbnail is %dx%d, want 256x256", cfg.Width, cfg.Height)
			}
		})
	}
}
