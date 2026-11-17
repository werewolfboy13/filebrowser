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
	"runtime/debug"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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

	progressive := progressiveJPEGHeader(16, 16)
	if samples, isProgressive := jpegSampling(progressive); samples != 1.5 || !isProgressive {
		t.Fatalf("jpegSampling of a 4:2:0 progressive image = %v, %v", samples, isProgressive)
	}
	unknown := color.ModelFunc(func(c color.Color) color.Color { return c })

	cases := map[string]struct {
		cfg    image.Config
		format string
		header []byte
		within bool
	}{
		"50 megapixel phone photo (4:2:0 JPEG)":      {image.Config{Width: 8160, Height: 6120, ColorModel: color.YCbCrModel}, "jpeg", header420, true},
		"JPEG with unknown sampling counts as 4:4:4": {image.Config{Width: 8160, Height: 6120, ColorModel: color.YCbCrModel}, "jpeg", nil, true},
		"grayscale scan at the side limit":           {image.Config{Width: 10000, Height: 10000, ColorModel: color.GrayModel}, "png", nil, true},
		"4K RGBA screenshot":                         {image.Config{Width: 3840, Height: 2160, ColorModel: color.NRGBAModel}, "png", nil, true},

		// The photographs the budget of 128 MiB an image used to turn away.
		"24 megapixel progressive JPEG": {image.Config{Width: 6000, Height: 4000, ColorModel: color.YCbCrModel}, "jpeg", progressive, true},
		"24 megapixel 8-bit RGB TIFF":   {image.Config{Width: 6000, Height: 4000, ColorModel: color.RGBAModel}, "tiff", nil, true},
		"20 megapixel 16-bit RGB PNG":   {image.Config{Width: 5472, Height: 3648, ColorModel: color.RGBA64Model}, "png", nil, true},
		"36 megapixel 8-bit RGBA PNG":   {image.Config{Width: 7360, Height: 4912, ColorModel: color.NRGBAModel}, "png", nil, true},

		// What is still too much for one image.
		"16-bit RGBA at the side limit":             {image.Config{Width: 10000, Height: 10000, ColorModel: color.NRGBA64Model}, "png", nil, false},
		"8-bit RGBA at the side limit":              {image.Config{Width: 10000, Height: 10000, ColorModel: color.NRGBAModel}, "png", nil, false},
		"50 megapixel progressive JPEG":             {image.Config{Width: 8160, Height: 6120, ColorModel: color.YCbCrModel}, "jpeg", progressive, false},
		"TIFF is held twice while decoding":         {image.Config{Width: 6000, Height: 6000, ColorModel: color.RGBAModel}, "tiff", nil, false},
		"unknown colour model counts as the widest": {image.Config{Width: 6000, Height: 6000, ColorModel: unknown}, "png", nil, false},
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

// progressiveJPEGHeader returns the start of a progressive (SOF2) 4:2:0 JPEG
// of the given size: everything the decoder needs to report its dimensions,
// and nothing to decode.
func progressiveJPEGHeader(w, h int) []byte {
	return []byte{
		0xff, 0xd8, // SOI
		0xff, 0xe0, 0x00, 0x10, 0x4a, 0x46, 0x49, 0x46, 0x00, 0x01, 0x01, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, // APP0 JFIF
		0xff, 0xc2, 0x00, 0x11, 0x08, // SOF2, length 17, 8-bit
		byte(h >> 8), byte(h), byte(w >> 8), byte(w),
		0x03,             // three components, 4:2:0
		0x01, 0x22, 0x00, // Y
		0x02, 0x11, 0x01, // Cb
		0x03, 0x11, 0x01, // Cr
	}
}

// The budget used to be 128 MiB an image, and an image estimated above it was
// refused: ordinary large photographs (a 24 megapixel progressive JPEG is
// estimated at 171 MiB) got neither a thumbnail nor a preview. Such an image
// is accepted now and waits for its share of the memory.
func TestResizeAcceptsLargePhotos(t *testing.T) {
	err := New(1).Resize(context.Background(), bytes.NewReader(progressiveJPEGHeader(6000, 4000)), 256, 256, io.Discard)
	if errors.Is(err, ErrImageTooLarge) {
		t.Errorf("a 24 megapixel progressive JPEG is refused as too large: %v", err)
	}
	// The file ends after its header, so decoding it fails; what matters is
	// that it got that far.
	if err == nil {
		t.Error("decoding a file that ends after its header reported success")
	}

	// The budget of every service has room for the largest image it accepts.
	for workers := 0; workers <= 8; workers++ {
		s := New(max(1, workers))
		if s.memory.size < s.maxImage || s.memory.size < int64(workers)*DecodeMemoryPerWorker {
			t.Errorf("%d workers: budget %d MiB, largest image %d MiB", workers, s.memory.size>>20, s.maxImage>>20)
		}
	}
}

// trackDecodes replaces the decoder with one that reports how many images
// are being decoded at one time, and holds each for a moment so that they
// overlap when nothing keeps them apart.
func trackDecodes(t *testing.T) (highest func() int) {
	t.Helper()

	var (
		mu     sync.Mutex
		active int
		most   int
	)
	original := decodeImage
	t.Cleanup(func() { decodeImage = original })
	decodeImage = func(r io.Reader) (image.Image, string, error) {
		mu.Lock()
		active++
		most = max(most, active)
		mu.Unlock()

		time.Sleep(30 * time.Millisecond)
		img, format, err := original(r)

		mu.Lock()
		active--
		mu.Unlock()
		return img, format, err
	}

	return func() int {
		mu.Lock()
		defer mu.Unlock()
		return most
	}
}

// Images take their estimated cost out of one budget before they are decoded:
// together they never hold more than it, however many workers there are.
func TestResizesShareOneMemoryBudget(t *testing.T) {
	picture := zeroPNG(t, 1000, 1000, 8, 6, 4) // 4 MB decoded
	const cost = 4_000_000

	resizeAll := func(s *Service, n int) {
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := s.Resize(context.Background(), bytes.NewReader(picture), 64, 64, io.Discard); err != nil {
					t.Errorf("Resize: %v", err)
				}
			}()
		}
		wg.Wait()
	}

	t.Run("a budget for one image at a time", func(t *testing.T) {
		highest := trackDecodes(t)
		s := New(4)
		s.memory = newWeighted(cost + cost/2)
		resizeAll(s, 6)
		if got := highest(); got != 1 {
			t.Errorf("%d images were decoded at one time under a budget for one", got)
		}
		if s.memory.used != 0 {
			t.Errorf("%d bytes of the budget were not given back", s.memory.used)
		}
	})

	t.Run("a budget for two", func(t *testing.T) {
		highest := trackDecodes(t)
		s := New(4)
		s.memory = newWeighted(2*cost + cost/2)
		resizeAll(s, 6)
		if got := highest(); got != 2 {
			t.Errorf("%d images were decoded at one time under a budget for two", got)
		}
	})

	t.Run("room for all: the workers are the limit", func(t *testing.T) {
		highest := trackDecodes(t)
		s := New(3)
		resizeAll(s, 6)
		if got := highest(); got != 3 {
			t.Errorf("%d images were decoded at one time by 3 workers", got)
		}
	})

	t.Run("an image over the ceiling is refused, whatever is free", func(t *testing.T) {
		s := New(4)
		s.maxImage = cost - 1
		if err := s.Resize(context.Background(), bytes.NewReader(picture), 64, 64, io.Discard); !errors.Is(err, ErrImageTooLarge) {
			t.Errorf("Resize of an image over the ceiling returned %v", err)
		}
	})
}

// A request that waits for memory gives up when it is cancelled, and takes
// neither memory nor a worker with it.
func TestResizeWaitingForMemoryStopsWithTheRequest(t *testing.T) {
	picture := zeroPNG(t, 1000, 1000, 8, 6, 4)
	s := New(1)
	s.memory = newWeighted(5_000_000)

	// Someone else holds the budget.
	if err := s.memory.acquire(context.Background(), 5_000_000); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := s.Resize(ctx, bytes.NewReader(picture), 64, 64, io.Discard)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Resize waiting for memory returned %v, want context.DeadlineExceeded", err)
	}
	if waited := time.Since(start); waited > 5*time.Second {
		t.Errorf("Resize waited %s past its deadline", waited)
	}

	// A small image is not kept waiting by it: the worker is free.
	if !s.sem.TryAcquire(1) {
		t.Fatal("the request that gave up kept a worker")
	}
	s.sem.Release(1)

	s.memory.release(5_000_000)
	if err := s.Resize(context.Background(), bytes.NewReader(picture), 64, 64, io.Discard); err != nil {
		t.Errorf("Resize once the memory was free: %v", err)
	}
	if s.memory.used != 0 {
		t.Errorf("%d bytes of the budget were not given back", s.memory.used)
	}
}

// What it is for: four requests at once for images that are each a tenth of
// a megabyte on disk and 34 MiB decoded hold what the budget allows, not four
// times an image. Before there was a budget, each worker held one.
func TestConcurrentResizesKeepTheHeapWithinTheBudget(t *testing.T) {
	const side = 3000 // 36 MB decoded as 8-bit RGBA
	picture := zeroPNG(t, side, side, 8, 6, 4)
	cost := int64(side) * side * 4

	peakDuring := func(s *Service) uint64 {
		defer debug.SetGCPercent(debug.SetGCPercent(20))
		runtime.GC()
		var base runtime.MemStats
		runtime.ReadMemStats(&base)

		var peak atomic.Uint64
		stop := make(chan struct{})
		sampled := make(chan struct{})
		go func() {
			defer close(sampled)
			for {
				select {
				case <-stop:
					return
				case <-time.After(time.Millisecond):
					var m runtime.MemStats
					runtime.ReadMemStats(&m)
					if m.HeapInuse > peak.Load() {
						peak.Store(m.HeapInuse)
					}
				}
			}
		}()

		var wg sync.WaitGroup
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := s.Resize(context.Background(), bytes.NewReader(picture), 256, 256, io.Discard); err != nil {
					t.Errorf("Resize: %v", err)
				}
			}()
		}
		wg.Wait()
		close(stop)
		<-sampled

		if peak.Load() < base.HeapInuse {
			return 0
		}
		return peak.Load() - base.HeapInuse
	}

	// A budget for one of these images at a time, with four workers.
	bounded := New(4)
	bounded.memory = newWeighted(cost + cost/4)
	within := peakDuring(bounded)

	// The default budget of four workers has room for all four at once.
	unbounded := peakDuring(New(4))

	t.Logf("peak heap above the start with 4 requests at once: %d MiB under a budget of %d MiB, %d MiB with room for all four",
		within>>20, bounded.memory.size>>20, unbounded>>20)

	// One image, the garbage of the one before it, and the resizing.
	if limit := uint64(3 * cost); within > limit {
		t.Errorf("4 concurrent resizes held %d MiB under a budget of %d MiB (limit %d MiB)", within>>20, bounded.memory.size>>20, limit>>20)
	}
}
