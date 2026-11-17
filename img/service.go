//go:generate go-enum --sql --marshal --file $GOFILE
package img

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"strings"

	"github.com/dsoprea/go-exif/v3"
	"github.com/marusama/semaphore/v2"
	"golang.org/x/image/bmp"
	"golang.org/x/image/tiff"

	exifcommon "github.com/dsoprea/go-exif/v3/common"
)

// ErrUnsupportedFormat means the given image format is not supported.
var ErrUnsupportedFormat = errors.New("unsupported image format")

// ErrImageTooLarge means the image is too large to create a thumbnail.
var ErrImageTooLarge = errors.New("image too large for thumbnail generation")

// Maximum dimensions for thumbnail generation to prevent server crashes. An
// image within them can still be refused for the memory it needs: see
// MaxDecodeMemory.
const (
	MaxImageWidth  = 10000
	MaxImageHeight = 10000
)

// Service
type Service struct {
	sem semaphore.Semaphore
}

func New(workers int) *Service {
	return &Service{
		sem: semaphore.New(workers),
	}
}

// Format is an image file format.
/*
ENUM(
jpeg
png
gif
tiff
bmp
)
*/
type Format int

// jpegQuality is the quality resized images are encoded with as JPEG.
const jpegQuality = 95

// encode writes img in the format.
func (x Format) encode(w io.Writer, img *image.NRGBA) error {
	switch x {
	case FormatPng:
		return png.Encode(w, img)
	case FormatGif:
		return gif.Encode(w, img, &gif.Options{NumColors: 256})
	case FormatTiff:
		return tiff.Encode(w, img, &tiff.Options{Compression: tiff.Deflate, Predictor: true})
	case FormatBmp:
		return bmp.Encode(w, img)
	default:
		// JPEG has no transparency. An opaque image is handed over as
		// premultiplied RGBA, which is the same bytes and what the encoder
		// is fastest with.
		if img.Opaque() {
			return jpeg.Encode(w, &image.RGBA{Pix: img.Pix, Stride: img.Stride, Rect: img.Rect}, &jpeg.Options{Quality: jpegQuality})
		}
		return jpeg.Encode(w, img, &jpeg.Options{Quality: jpegQuality})
	}
}

/*
ENUM(
high
medium
low
)
*/
type Quality int

func (x Quality) kernel() kernel {
	switch x {
	case QualityHigh:
		return lanczosKernel
	case QualityMedium:
		return boxKernel
	case QualityLow:
		return nearestKernel
	default:
		return boxKernel
	}
}

/*
ENUM(
fit
fill
)
*/
type ResizeMode int

func (s *Service) FormatFromExtension(ext string) (Format, error) {
	switch strings.ToLower(strings.TrimPrefix(ext, ".")) {
	case "jpg", "jpeg":
		return FormatJpeg, nil
	case "png":
		return FormatPng, nil
	case "gif":
		return FormatGif, nil
	case "tif", "tiff":
		return FormatTiff, nil
	case "bmp":
		return FormatBmp, nil
	}
	return -1, ErrUnsupportedFormat
}

type resizeConfig struct {
	format     Format
	resizeMode ResizeMode
	quality    Quality
}

type Option func(*resizeConfig)

func WithFormat(format Format) Option {
	return func(config *resizeConfig) {
		config.format = format
	}
}

func WithMode(mode ResizeMode) Option {
	return func(config *resizeConfig) {
		config.resizeMode = mode
	}
}

func WithQuality(quality Quality) Option {
	return func(config *resizeConfig) {
		config.quality = quality
	}
}

// ErrInvalidImage means that the image could not be processed although its
// header looked fine: decoding or resizing it failed in a way the code doing
// it did not expect.
var ErrInvalidImage = errors.New("invalid image")

// Resize reads an image from in, scales it to width x height according to the
// options, and writes the result to out.
//
// Whatever goes wrong with an image is returned as an error, including a
// panic in the decoder or in the resizing: both run in the calling goroutine,
// so a file that trips one of them up fails its own request and nothing else.
func (s *Service) Resize(ctx context.Context, in io.Reader, width, height int, out io.Writer, options ...Option) (err error) {
	if err := s.sem.Acquire(ctx, 1); err != nil {
		return err
	}
	defer s.sem.Release(1)

	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%w: %v", ErrInvalidImage, r)
		}
	}()

	// The wait for a worker can outlast the request.
	if err := ctx.Err(); err != nil {
		return err
	}

	format, wrappedReader, err := s.detectFormat(in)
	if err != nil {
		return err
	}

	config := resizeConfig{
		format:     format,
		resizeMode: ResizeModeFit,
		quality:    QualityMedium,
	}
	for _, option := range options {
		option(&config)
	}

	if config.quality == QualityLow && format == FormatJpeg {
		thm, newWrappedReader, errThm := getEmbeddedThumbnail(wrappedReader)
		wrappedReader = newWrappedReader
		if errThm == nil {
			_, err = out.Write(thm)
			if err == nil {
				return nil
			}
		}
	}

	// A JPEG can say that it has to be turned to be shown upright. That is in
	// its header, which is kept aside while the decoder reads through it.
	var header *headBuffer
	if format == FormatJpeg {
		header = &headBuffer{limit: orientationHeaderSize}
		wrappedReader = io.TeeReader(wrappedReader, header)
	}

	img, _, err := decodeImage(wrappedReader)
	if err != nil {
		return err
	}

	orientation := 0
	if header != nil {
		orientation = jpegOrientation(header.buf)
	}

	return s.scale(ctx, img, orientation, width, height, out, config)
}

// decodeImage decodes an image in one of the registered formats. It is a
// variable so that tests can stand in for a decoder that misbehaves.
var decodeImage = image.Decode

// orientationHeaderSize is how much of a JPEG file is kept to look for its
// orientation in. The EXIF block comes first and is at most 64 KiB.
const orientationHeaderSize = 128 << 10

// headBuffer keeps the first limit bytes written to it.
type headBuffer struct {
	buf   []byte
	limit int
}

func (h *headBuffer) Write(p []byte) (int, error) {
	if room := h.limit - len(h.buf); room > 0 {
		h.buf = append(h.buf, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

// scale resizes the decoded image and writes it out.
func (s *Service) scale(ctx context.Context, img image.Image, orientation, width, height int, out io.Writer, config resizeConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if width <= 0 || height <= 0 {
		return fmt.Errorf("%w: cannot resize to %dx%d", ErrInvalidImage, width, height)
	}

	bounds := img.Bounds()
	srcW, srcH := bounds.Dx(), bounds.Dy()
	if srcW <= 0 || srcH <= 0 {
		return fmt.Errorf("%w: the image is empty", ErrInvalidImage)
	}

	// The image is scaled as it is stored and turned afterwards, when it is
	// small. For an image that lies on its side that means the target is
	// given on its side too.
	if orientation >= 5 {
		width, height = height, width
	}

	var (
		crop       = image.Rect(0, 0, srcW, srcH)
		dstW, dstH int
	)
	switch config.resizeMode {
	case ResizeModeFill:
		crop = fillCrop(srcW, srcH, width, height)
		dstW, dstH = width, height
	case ResizeModeFit:
		fallthrough
	default:
		dstW, dstH = fitSize(srcW, srcH, width, height)
	}

	resized, err := resample(ctx, img, crop, dstW, dstH, config.quality.kernel())
	if err != nil {
		return err
	}

	return config.format.encode(out, orient(resized, orientation))
}

func (s *Service) detectFormat(in io.Reader) (Format, io.Reader, error) {
	buf := &bytes.Buffer{}
	r := io.TeeReader(in, buf)

	imgConfig, imgFormat, err := image.DecodeConfig(r)
	if err != nil {
		return 0, nil, fmt.Errorf("%s: %w", err.Error(), ErrUnsupportedFormat)
	}

	// Check if image dimensions exceed maximum allowed size
	if imgConfig.Width > MaxImageWidth || imgConfig.Height > MaxImageHeight {
		return 0, nil, fmt.Errorf("image dimensions %dx%d exceed maximum %dx%d: %w",
			imgConfig.Width, imgConfig.Height, MaxImageWidth, MaxImageHeight, ErrImageTooLarge)
	}

	// The dimensions alone do not bound the memory decoding needs: the pixel
	// size and the format matter as much.
	if cost := decodeCost(imgConfig, imgFormat, buf.Bytes()); cost > MaxDecodeMemory {
		return 0, nil, fmt.Errorf("decoding a %dx%d %s image needs about %d MiB, more than the %d MiB allowed: %w",
			imgConfig.Width, imgConfig.Height, imgFormat, cost>>20, MaxDecodeMemory>>20, ErrImageTooLarge)
	}

	format, err := ParseFormat(imgFormat)
	if err != nil {
		return 0, nil, ErrUnsupportedFormat
	}

	return format, io.MultiReader(buf, in), nil
}

func getEmbeddedThumbnail(in io.Reader) ([]byte, io.Reader, error) {
	buf := &bytes.Buffer{}
	r := io.TeeReader(in, buf)
	wrappedReader := io.MultiReader(buf, in)

	offset := 0
	offsets := []int{12, 30}
	head := make([]byte, 0xffff)

	_, err := r.Read(head)
	if err != nil {
		return nil, wrappedReader, err
	}

	for _, offset = range offsets {
		if _, err = exif.ParseExifHeader(head[offset:]); err == nil {
			break
		}
	}

	if err != nil {
		return nil, wrappedReader, err
	}

	im, err := exifcommon.NewIfdMappingWithStandard()
	if err != nil {
		return nil, wrappedReader, err
	}

	_, index, err := exif.Collect(im, exif.NewTagIndex(), head[offset:])
	if err != nil {
		return nil, wrappedReader, err
	}

	ifd := index.RootIfd.NextIfd()
	if ifd == nil {
		return nil, wrappedReader, exif.ErrNoThumbnail
	}

	thm, err := ifd.Thumbnail()
	return thm, wrappedReader, err
}
