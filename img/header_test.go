package img

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"runtime"
	"sync"
	"testing"
	"time"

	"golang.org/x/image/tiff"
)

// jpegWithComments is a small JPEG image with size bytes of comment segments
// between the start of the file and everything else: the decoder reads
// through all of them before it gets to the frame header, which is where the
// dimensions are.
func jpegWithComments(t *testing.T, size int) []byte {
	t.Helper()
	var plain bytes.Buffer
	if err := jpeg.Encode(&plain, image.NewRGBA(image.Rect(0, 0, 64, 48)), nil); err != nil {
		t.Fatal(err)
	}

	var b bytes.Buffer
	b.Write(plain.Bytes()[:2]) // start of image
	segment := append([]byte{0xff, 0xfe, 0xff, 0xff}, make([]byte, 65533)...)
	for written := 0; written < size; written += len(segment) {
		b.Write(segment)
	}
	b.Write(plain.Bytes()[2:])
	return b.Bytes()
}

// pngWithText is a small paletted PNG image with size bytes of text chunks
// between its header and its palette, which the decoder reads before it can
// say what the image is.
func pngWithText(t *testing.T, size int) []byte {
	t.Helper()
	var plain bytes.Buffer
	paletted := image.NewPaletted(image.Rect(0, 0, 64, 48), color.Palette{color.Black, color.White})
	if err := png.Encode(&plain, paletted); err != nil {
		t.Fatal(err)
	}

	// The signature and the header chunk: 8 + (4 + 4 + 13 + 4) bytes.
	const headerEnd = 33
	text := append([]byte("tEXtComment\x00"), make([]byte, 1<<20)...)
	var chunk bytes.Buffer
	_ = binary.Write(&chunk, binary.BigEndian, uint32(len(text)-4))
	chunk.Write(text)
	_ = binary.Write(&chunk, binary.BigEndian, crc32.ChecksumIEEE(text))

	var b bytes.Buffer
	b.Write(plain.Bytes()[:headerEnd])
	for written := 0; written < size; written += chunk.Len() {
		b.Write(chunk.Bytes())
	}
	b.Write(plain.Bytes()[headerEnd:])
	return b.Bytes()
}

// plainTIFF is a TIFF image as the usual writers make them: the pixels first,
// and the directory that says what they are at the end of the file.
func plainTIFF(t *testing.T, width, height int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := tiff.Encode(&b, image.NewGray(image.Rect(0, 0, width, height)), nil); err != nil {
		t.Fatal(err)
	}
	if offset := binary.LittleEndian.Uint32(b.Bytes()[4:8]); int(offset) < 8<<20 {
		t.Fatalf("the directory of the TIFF is %d bytes in: too close to the start for this test", offset)
	}
	return b.Bytes()
}

// What kind of image a file is, and how large, is read from its header
// before the image waits for a worker and for its share of the memory. All
// that was read on the way was kept, to be read again when the image is
// decoded. A header can be as long as the file: comments before the frame
// header of a JPEG, text before the palette of a PNG, and in a TIFF the
// directory is at the end as a matter of course. Every request for such a
// file held all of it, twice over, for as long as it waited, with nothing
// bounding how many of them did.
func TestImageHeadersAreNotHeldWhileWaiting(t *testing.T) {
	const requests = 8

	cases := map[string]struct {
		picture func(t *testing.T) []byte
		wantErr error
	}{
		"a JPEG with 32 MiB of comments before its frame header": {
			picture: func(t *testing.T) []byte { return jpegWithComments(t, 32<<20) },
			wantErr: ErrUnsupportedFormat,
		},
		"a JPEG with 2 MiB of comments before its frame header": {
			picture: func(t *testing.T) []byte { return jpegWithComments(t, 2<<20) },
		},
		"a PNG with 32 MiB of text before its palette": {
			picture: func(t *testing.T) []byte { return pngWithText(t, 32<<20) },
			wantErr: ErrUnsupportedFormat,
		},
		"a PNG with 2 MiB of text before its palette": {
			picture: func(t *testing.T) []byte { return pngWithText(t, 2<<20) },
		},
		"a TIFF, with its directory at the end": {
			picture: func(t *testing.T) []byte { return plainTIFF(t, 4096, 2304) },
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			picture := tc.picture(t)
			s := New(1)

			// The only worker is busy.
			if err := s.sem.Acquire(context.Background(), 1); err != nil {
				t.Fatal(err)
			}
			released := false
			release := func() {
				if !released {
					released = true
					s.sem.Release(1)
				}
			}
			defer release()

			runtime.GC()
			var before runtime.MemStats
			runtime.ReadMemStats(&before)

			var (
				wg       sync.WaitGroup
				mu       sync.Mutex
				returned int
				failures []error
				sizes    []image.Point
			)
			for i := 0; i < requests; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					var out bytes.Buffer
					err := s.Resize(context.Background(), bytes.NewReader(picture), 32, 32, &out, WithQuality(QualityMedium))
					mu.Lock()
					defer mu.Unlock()
					returned++
					if err != nil {
						failures = append(failures, err)
						return
					}
					if cfg, _, err := image.DecodeConfig(&out); err == nil {
						sizes = append(sizes, image.Pt(cfg.Width, cfg.Height))
					}
				}()
			}

			// Until every request has either been refused or is waiting for
			// the worker, having taken its share of the memory.
			share := s.memoryOf(picture)
			deadline := time.Now().Add(20 * time.Second)
			for {
				mu.Lock()
				done := returned
				mu.Unlock()
				s.memory.mu.Lock()
				used := s.memory.used
				s.memory.mu.Unlock()
				if done == requests || (share > 0 && used == share*int64(requests-done)) {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("%d of %d requests returned, the others are not waiting for the worker", done, requests)
				}
				time.Sleep(5 * time.Millisecond)
			}

			runtime.GC()
			var held runtime.MemStats
			runtime.ReadMemStats(&held)
			if grown := int64(held.HeapAlloc) - int64(before.HeapAlloc); grown > 4<<20 {
				t.Errorf("%d requests for a %d MiB file hold %d MiB between them while they wait for the worker", requests, len(picture)>>20, grown>>20)
			}

			release()
			wg.Wait()

			if tc.wantErr != nil {
				if len(failures) != requests {
					t.Fatalf("%d of %d requests were refused, want all", len(failures), requests)
				}
				for _, err := range failures {
					if !errors.Is(err, tc.wantErr) {
						t.Errorf("Resize = %v, want %v", err, tc.wantErr)
					}
				}
				return
			}
			if len(failures) != 0 {
				t.Fatalf("%d of %d requests failed: %v", len(failures), requests, failures[0])
			}
			for _, size := range sizes {
				if size.X != 32 && size.Y != 32 {
					t.Errorf("the result is %v, want it to fit 32x32", size)
				}
			}
			if len(sizes) != requests {
				t.Errorf("%d of %d results are images", len(sizes), requests)
			}
		})
	}
}

// memoryOf returns what one image takes of the memory budget while it waits
// for a worker, or 0 for an image that is refused before that.
func (s *Service) memoryOf(picture []byte) int64 {
	_, cost, _, err := s.detectFormat(bytes.NewReader(picture))
	if err != nil {
		return 0
	}
	return max(1, min(cost, s.memory.size))
}

// sequential hides everything about a reader but that it can be read.
type sequential struct{ io.Reader }

// An image can come from something that can only be read once, or that is
// not at its start: then what was read of it is kept, or gone back to.
func TestResizeReadsFromAnyReader(t *testing.T) {
	var jpg bytes.Buffer
	if err := jpeg.Encode(&jpg, image.NewRGBA(image.Rect(0, 0, 64, 48)), nil); err != nil {
		t.Fatal(err)
	}
	var tif bytes.Buffer
	if err := tiff.Encode(&tif, image.NewGray(image.Rect(0, 0, 64, 48)), nil); err != nil {
		t.Fatal(err)
	}

	for name, picture := range map[string][]byte{"jpeg": jpg.Bytes(), "tiff": tif.Bytes()} {
		embedded := append([]byte("0123456789"), picture...)
		readers := map[string]func() io.Reader{
			"a reader that can be read at any place": func() io.Reader { return bytes.NewReader(picture) },
			"a reader that can only be read once":    func() io.Reader { return sequential{bytes.NewReader(picture)} },
			"a reader that is not at its start": func() io.Reader {
				r := bytes.NewReader(embedded)
				if _, err := r.Seek(10, io.SeekStart); err != nil {
					t.Fatal(err)
				}
				return r
			},
		}
		for how, reader := range readers {
			t.Run(name+" from "+how, func(t *testing.T) {
				var out bytes.Buffer
				if err := New(1).Resize(context.Background(), reader(), 32, 32, &out, WithQuality(QualityMedium)); err != nil {
					t.Fatal(err)
				}
				cfg, format, err := image.DecodeConfig(&out)
				if err != nil || format != name || cfg.Width != 32 || cfg.Height != 24 {
					t.Errorf("the result is a %dx%d %s image (%v), want 32x24 %s", cfg.Width, cfg.Height, format, err, name)
				}
			})
		}
	}
}
