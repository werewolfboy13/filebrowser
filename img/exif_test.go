package img

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
)

// jpegWithOrientation returns img encoded as a JPEG that carries the EXIF
// orientation o, in the byte order given.
func jpegWithOrientation(t testing.TB, img image.Image, o int, order binary.ByteOrder) []byte {
	t.Helper()

	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, img, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}

	// A TIFF structure with one directory holding one tag: Orientation.
	var tiff bytes.Buffer
	if order == binary.LittleEndian {
		tiff.WriteString("II")
	} else {
		tiff.WriteString("MM")
	}
	_ = binary.Write(&tiff, order, uint16(42))
	_ = binary.Write(&tiff, order, uint32(8))      // offset of the directory
	_ = binary.Write(&tiff, order, uint16(1))      // one entry
	_ = binary.Write(&tiff, order, uint16(0x0112)) // Orientation
	_ = binary.Write(&tiff, order, uint16(3))      // SHORT
	_ = binary.Write(&tiff, order, uint32(1))      // one value
	_ = binary.Write(&tiff, order, uint16(o))
	_ = binary.Write(&tiff, order, uint16(0))
	_ = binary.Write(&tiff, order, uint32(0)) // no further directory

	exif := append([]byte("Exif\x00\x00"), tiff.Bytes()...)

	var out bytes.Buffer
	out.Write([]byte{0xff, 0xd8}) // start of image
	out.Write([]byte{0xff, 0xe1}) // APP1
	_ = binary.Write(&out, binary.BigEndian, uint16(len(exif)+2))
	out.Write(exif)
	out.Write(encoded.Bytes()[2:]) // the encoded image without its own start marker
	return out.Bytes()
}

// quadrants returns an image of w x h whose four quarters have different
// colours: red top left, green top right, blue bottom left, white bottom
// right. Turning or mirroring it moves the colours to other corners.
func quadrants(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{A: 0xff}
			switch {
			case x < w/2 && y < h/2:
				c.R = 0xff
			case y < h/2:
				c.G = 0xff
			case x < w/2:
				c.B = 0xff
			default:
				c.R, c.G, c.B = 0xff, 0xff, 0xff
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

// A camera stores a picture as its sensor saw it and notes, in the EXIF
// orientation, how it has to be turned to be upright. Previews have to honour
// that, or portrait photographs lie on their side.
func TestResizeHonoursTheJPEGOrientation(t *testing.T) {
	red := color.NRGBA{R: 0xff, A: 0xff}
	green := color.NRGBA{G: 0xff, A: 0xff}
	blue := color.NRGBA{B: 0xff, A: 0xff}
	white := color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}

	// The stored image: red top left, green top right, blue bottom left,
	// white bottom right. For each orientation, the corners of the upright
	// picture in the same order.
	corners := map[int][4]color.NRGBA{
		0: {red, green, blue, white}, // no orientation recorded
		1: {red, green, blue, white},
		2: {green, red, white, blue}, // mirrored left to right
		3: {white, blue, green, red}, // upside down
		4: {blue, white, red, green}, // mirrored top to bottom
		5: {red, blue, green, white}, // mirrored along the main diagonal
		6: {blue, red, white, green}, // quarter turn clockwise
		7: {white, green, blue, red}, // mirrored along the other diagonal
		8: {green, white, red, blue}, // quarter turn anticlockwise
	}

	near := func(a, b color.NRGBA) bool {
		d := func(x, y uint8) bool { return int(x)-int(y) < 40 && int(y)-int(x) < 40 }
		return d(a.R, b.R) && d(a.G, b.G) && d(a.B, b.B)
	}

	const w, h = 400, 200
	stored := quadrants(w, h)
	for o, want := range corners {
		for _, order := range []binary.ByteOrder{binary.BigEndian, binary.LittleEndian} {
			var file []byte
			if o == 0 {
				var buf bytes.Buffer
				if err := jpeg.Encode(&buf, stored, &jpeg.Options{Quality: 100}); err != nil {
					t.Fatal(err)
				}
				file = buf.Bytes()
			} else {
				file = jpegWithOrientation(t, stored, o, order)
			}
			if got := jpegOrientation(file); got != o {
				t.Fatalf("orientation %d (%v): read back as %d", o, order, got)
			}

			wantW, wantH := w/2, h/2
			if o >= 5 {
				wantW, wantH = h/2, w/2
			}

			// Both ways of resizing, at the quality the previews use.
			for mode, size := range map[ResizeMode]image.Point{ResizeModeFit: {wantW, wantH}, ResizeModeFill: {64, 64}} {
				var out bytes.Buffer
				box := image.Pt(200, 200)
				if mode == ResizeModeFill {
					box = size
				}
				err := New(1).Resize(context.Background(), bytes.NewReader(file), box.X, box.Y, &out,
					WithMode(mode), WithQuality(QualityMedium), WithFormat(FormatPng))
				if err != nil {
					t.Fatal(err)
				}
				decoded, _, err := image.Decode(&out)
				if err != nil {
					t.Fatal(err)
				}
				b := decoded.Bounds()
				if b.Dx() != size.X || b.Dy() != size.Y {
					t.Errorf("orientation %d, mode %v: result is %dx%d, want %dx%d", o, mode, b.Dx(), b.Dy(), size.X, size.Y)
					continue
				}

				at := func(x, y int) color.NRGBA {
					return color.NRGBAModel.Convert(decoded.At(b.Min.X+x, b.Min.Y+y)).(color.NRGBA)
				}
				got := [4]color.NRGBA{at(2, 2), at(b.Dx()-3, 2), at(2, b.Dy()-3), at(b.Dx()-3, b.Dy()-3)}
				for i := range got {
					if !near(got[i], want[i]) {
						t.Errorf("orientation %d, mode %v: corner %d is %v, want %v", o, mode, i, got[i], want[i])
					}
				}
			}
		}
	}
}

// Whatever stands where the orientation should be, reading it never fails.
func TestJPEGOrientationOfMalformedHeaders(t *testing.T) {
	valid := jpegWithOrientation(t, quadrants(16, 16), 6, binary.BigEndian)
	if jpegOrientation(valid) != 6 {
		t.Fatal("the test file has no orientation")
	}

	// Every truncation of the header, and every single corrupted byte in it.
	for n := 0; n < 64; n++ {
		_ = jpegOrientation(valid[:n])
	}
	for i := 0; i < 64; i++ {
		for _, v := range []byte{0x00, 0xff, 0x7f} {
			corrupted := append([]byte(nil), valid[:128]...)
			corrupted[i] = v
			if o := jpegOrientation(corrupted); o < 0 || o > 8 {
				t.Errorf("byte %d set to %#x: orientation %d", i, v, o)
			}
		}
	}

	for name, data := range map[string][]byte{
		"empty":           nil,
		"not a JPEG":      []byte("\x89PNG\r\n\x1a\n"),
		"only the marker": {0xff, 0xd8},
		"other metadata":  append([]byte{0xff, 0xd8, 0xff, 0xe1, 0x00, 0x08}, []byte("http:/")...),
	} {
		if o := jpegOrientation(data); o != 0 {
			t.Errorf("%s: orientation %d, want none", name, o)
		}
	}
}
