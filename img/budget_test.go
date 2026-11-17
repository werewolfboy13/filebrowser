package img

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"runtime"
	"testing"
)

// zeroPNG builds a w x h PNG of the given bit depth and colour type whose
// pixels are all zero, without ever holding the decoded image in memory. Such
// a file is tiny: its size says nothing about the memory decoding it needs.
func zeroPNG(t *testing.T, w, h int, depth, colorType byte, bytesPerPixel int) []byte {
	t.Helper()

	var out bytes.Buffer
	out.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	chunk := func(typ string, data []byte) {
		var l [4]byte
		binary.BigEndian.PutUint32(l[:], uint32(len(data)))
		out.Write(l[:])
		crc := crc32.NewIEEE()
		crc.Write([]byte(typ))
		crc.Write(data)
		out.WriteString(typ)
		out.Write(data)
		binary.BigEndian.PutUint32(l[:], crc.Sum32())
		out.Write(l[:])
	}

	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], uint32(w))
	binary.BigEndian.PutUint32(ihdr[4:], uint32(h))
	ihdr[8] = depth
	ihdr[9] = colorType
	chunk("IHDR", ihdr)

	var idat bytes.Buffer
	zw, err := zlib.NewWriterLevel(&idat, zlib.BestSpeed)
	if err != nil {
		t.Fatal(err)
	}
	row := make([]byte, 1+w*bytesPerPixel)
	for i := 0; i < h; i++ {
		if _, err := zw.Write(row); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	chunk("IDAT", idat.Bytes())
	chunk("IEND", nil)

	return out.Bytes()
}

// The size check used to be per side only (10000 x 10000) and ignored the bit
// depth, so a 760 KiB upload of a 16-bit RGBA PNG made one preview request
// decode 800 MiB. The check is now on the memory decoding needs.
func TestResizeRefusesImagesOverTheDecodeBudget(t *testing.T) {
	bomb := zeroPNG(t, 10000, 10000, 16, 6, 8)
	if len(bomb) > 8<<20 {
		t.Fatalf("test image is %d bytes, expected a small file", len(bomb))
	}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	err := New(1).Resize(context.Background(), bytes.NewReader(bomb), 256, 256, io.Discard)

	runtime.ReadMemStats(&after)
	if !errors.Is(err, ErrImageTooLarge) {
		t.Errorf("Resize of a 10000x10000 16-bit RGBA image returned %v, want ErrImageTooLarge", err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 64<<20 {
		t.Errorf("Resize allocated %d MiB before giving up", allocated>>20)
	}

	// The per-side limit still applies to images that are cheap per pixel.
	wide := zeroPNG(t, MaxImageWidth+1, 1, 8, 0, 1)
	if err := New(1).Resize(context.Background(), bytes.NewReader(wide), 256, 256, io.Discard); !errors.Is(err, ErrImageTooLarge) {
		t.Errorf("Resize of a %d pixel wide image returned %v, want ErrImageTooLarge", MaxImageWidth+1, err)
	}
}

func TestDecodeCost(t *testing.T) {
	// The standard encoder always writes 4:2:0 for colour images.
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, image.NewYCbCr(image.Rect(0, 0, 16, 16), image.YCbCrSubsampleRatio420), nil); err != nil {
		t.Fatal(err)
	}
	header420 := encoded.Bytes()
	if samples, progressive := jpegSampling(header420); samples != 1.5 || progressive {
		t.Fatalf("jpegSampling of a 4:2:0 baseline image = %v, %v", samples, progressive)
	}

	cases := map[string]struct {
		cfg    image.Config
		format string
		header []byte
		within bool
	}{
		"50 megapixel phone photo (4:2:0 JPEG)":      {image.Config{Width: 8160, Height: 6120, ColorModel: color.YCbCrModel}, "jpeg", header420, true},
		"JPEG with unknown sampling counts as 4:4:4": {image.Config{Width: 8160, Height: 6120, ColorModel: color.YCbCrModel}, "jpeg", nil, false},
		"24 megapixel JPEG, unknown sampling":        {image.Config{Width: 6000, Height: 4000, ColorModel: color.YCbCrModel}, "jpeg", nil, true},
		"grayscale scan at the side limit":           {image.Config{Width: 10000, Height: 10000, ColorModel: color.GrayModel}, "png", nil, true},
		"4K RGBA screenshot":                         {image.Config{Width: 3840, Height: 2160, ColorModel: color.NRGBAModel}, "png", nil, true},
		"16-bit RGBA at the side limit":              {image.Config{Width: 10000, Height: 10000, ColorModel: color.NRGBA64Model}, "png", nil, false},
		"8-bit RGBA at the side limit":               {image.Config{Width: 10000, Height: 10000, ColorModel: color.NRGBAModel}, "png", nil, false},
		"TIFF is held twice while decoding":          {image.Config{Width: 5000, Height: 5000, ColorModel: color.RGBAModel}, "tiff", nil, false},
		"unknown colour model counts as the widest":  {image.Config{Width: 5000, Height: 5000, ColorModel: color.ModelFunc(func(c color.Color) color.Color { return c })}, "png", nil, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cost := decodeCost(tc.cfg, tc.format, tc.header)
			if (cost <= MaxDecodeMemory) != tc.within {
				t.Errorf("decodeCost = %d MiB, budget %d MiB, want within=%v", cost>>20, MaxDecodeMemory>>20, tc.within)
			}
		})
	}
}

// A request that has gone away must not start decoding once a worker is free.
func TestResizeStopsWhenTheContextIsDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	small := zeroPNG(t, 64, 64, 8, 0, 1)
	err := New(1).Resize(ctx, bytes.NewReader(small), 16, 16, io.Discard)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Resize with a cancelled context returned %v, want context.Canceled", err)
	}
}
