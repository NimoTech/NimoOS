package file

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"testing"

	"github.com/disintegration/imaging"
)

// makeLargeJPEG writes a w×h synthetic JPEG (quality 90, so it round-trips at
// a nontrivial size) to a temp file and returns its path and byte size.
func makeLargeJPEG(t *testing.T, w, h int) (path string, size int64) {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	// Fill with pseudo-random-ish gradient noise so JPEG can't trivially
	// compress it away to near-zero bytes (a flat color would defeat the
	// "shrinks meaningfully" assertion for reasons unrelated to resizing).
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.NRGBA{
				R: uint8((x * 7) % 256),
				G: uint8((y * 13) % 256),
				B: uint8((x + y) % 256),
				A: 255,
			})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("jpeg encode: %v", err)
	}
	f, err := os.CreateTemp(t.TempDir(), "large_*.jpg")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	f.Close()
	return f.Name(), int64(buf.Len())
}

// TestGenerateThumbnail_ShrinksLongEdgeAndBytes is the reproduction case from
// task BF23: a large (2000x1500-ish) photo must come back with its long edge
// at or under ThumbnailMaxEdge (400) and a byte size dramatically smaller
// than the source — not "roughly the same size" as the current buggy
// handler produces.
func TestGenerateThumbnail_ShrinksLongEdgeAndBytes(t *testing.T) {
	path, srcSize := makeLargeJPEG(t, 2000, 1500)

	data, err := GenerateThumbnail(path)
	if err != nil {
		t.Fatalf("GenerateThumbnail: %v", err)
	}

	decoded, err := imaging.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode thumbnail: %v", err)
	}
	b := decoded.Bounds()
	w, h := b.Dx(), b.Dy()
	longEdge := w
	if h > longEdge {
		longEdge = h
	}
	if longEdge > ThumbnailMaxEdge {
		t.Errorf("long edge %d exceeds ThumbnailMaxEdge %d (got %dx%d)", longEdge, ThumbnailMaxEdge, w, h)
	}

	if int64(len(data)) >= srcSize {
		t.Errorf("thumbnail (%d bytes) is not smaller than source (%d bytes)", len(data), srcSize)
	}
	// Should be a real thumbnail, not just marginally smaller: expect at
	// least a 5x reduction for a 2000x1500 source shrunk to <=400 long edge.
	if int64(len(data))*5 >= srcSize {
		t.Errorf("thumbnail (%d bytes) is not meaningfully smaller than source (%d bytes); expected >=5x reduction", len(data), srcSize)
	}
}

// TestGenerateThumbnail_DoesNotUpscaleSmallImages ensures a source already
// smaller than the target box is not enlarged.
func TestGenerateThumbnail_DoesNotUpscaleSmallImages(t *testing.T) {
	path, _ := makeLargeJPEG(t, 120, 80)

	data, err := GenerateThumbnail(path)
	if err != nil {
		t.Fatalf("GenerateThumbnail: %v", err)
	}
	decoded, err := imaging.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode thumbnail: %v", err)
	}
	b := decoded.Bounds()
	if b.Dx() != 120 || b.Dy() != 80 {
		t.Errorf("expected unchanged 120x80 for a source smaller than the target box, got %dx%d", b.Dx(), b.Dy())
	}
}

// TestGenerateThumbnail_UnsupportedFormatErrors ensures decode failures
// (e.g. a HEIC file, which imaging cannot decode) surface as an error so the
// caller can fall back to serving the original file or a generic icon.
func TestGenerateThumbnail_UnsupportedFormatErrors(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "not_an_image_*.heic")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	f.Write([]byte("this is not image data"))
	f.Close()

	if _, err := GenerateThumbnail(f.Name()); err == nil {
		t.Error("expected an error for an undecodable file, got nil")
	}
}
