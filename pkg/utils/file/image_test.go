package file

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"testing"

	"github.com/disintegration/imaging"
)

// makeOrientedJPEG returns a 100×50 landscape JPEG file path whose EXIF
// Orientation tag is set to the given value.
func makeOrientedJPEG(t *testing.T, orientation uint16) string {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 100, 50))
	for y := 0; y < 50; y++ {
		for x := 0; x < 100; x++ {
			img.Set(x, y, color.NRGBA{200, 100, 50, 255})
		}
	}
	var jpegBuf bytes.Buffer
	if err := jpeg.Encode(&jpegBuf, img, nil); err != nil {
		t.Fatalf("jpeg encode: %v", err)
	}
	jpegBytes := jpegBuf.Bytes()

	var tiff bytes.Buffer
	tiff.Write([]byte{0x49, 0x49, 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00})
	binary.Write(&tiff, binary.LittleEndian, uint16(1))
	binary.Write(&tiff, binary.LittleEndian, uint16(0x0112))
	binary.Write(&tiff, binary.LittleEndian, uint16(3))
	binary.Write(&tiff, binary.LittleEndian, uint32(1))
	binary.Write(&tiff, binary.LittleEndian, orientation)
	binary.Write(&tiff, binary.LittleEndian, uint16(0))
	binary.Write(&tiff, binary.LittleEndian, uint32(0))

	payload := append([]byte("Exif\x00\x00"), tiff.Bytes()...)
	var app1 bytes.Buffer
	app1.Write([]byte{0xFF, 0xE1})
	binary.Write(&app1, binary.BigEndian, uint16(len(payload)+2))
	app1.Write(payload)

	combined := append(jpegBytes[:2:2], append(app1.Bytes(), jpegBytes[2:]...)...)

	f, err := os.CreateTemp(t.TempDir(), "oriented_*.jpg")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	f.Write(combined)
	f.Close()
	return f.Name()
}

// TestApplyOrientationToThumbnail_Orientation6 verifies that a landscape JPEG
// with EXIF orientation=6 (needs 90° CW rotation) is rotated to portrait.
func TestApplyOrientationToThumbnail_Orientation6(t *testing.T) {
	// Create a landscape image: 100 wide x 50 tall
	src := imaging.New(100, 50, color.NRGBA{200, 100, 50, 255})
	buf := bytes.Buffer{}
	if err := imaging.Encode(&buf, src, imaging.JPEG); err != nil {
		t.Fatalf("failed to encode test image: %v", err)
	}

	result := applyOrientationToThumbnail(buf.Bytes(), 6)

	decoded, err := imaging.Decode(bytes.NewReader(result))
	if err != nil {
		t.Fatalf("failed to decode result: %v", err)
	}
	b := decoded.Bounds()
	w, h := b.Max.X-b.Min.X, b.Max.Y-b.Min.Y

	// Orientation 6 = 90° CW: 100x50 → 50x100
	if w != 50 || h != 100 {
		t.Errorf("expected 50×100 after orientation-6 rotation, got %d×%d", w, h)
	}
}

// TestApplyOrientationToThumbnail_Orientation1 verifies normal orientation is a no-op.
func TestApplyOrientationToThumbnail_Orientation1(t *testing.T) {
	src := imaging.New(100, 50, color.NRGBA{200, 100, 50, 255})
	buf := bytes.Buffer{}
	if err := imaging.Encode(&buf, src, imaging.JPEG); err != nil {
		t.Fatalf("failed to encode test image: %v", err)
	}

	result := applyOrientationToThumbnail(buf.Bytes(), 1)

	decoded, err := imaging.Decode(bytes.NewReader(result))
	if err != nil {
		t.Fatalf("failed to decode result: %v", err)
	}
	b := decoded.Bounds()
	w, h := b.Max.X-b.Min.X, b.Max.Y-b.Min.Y

	if w != 100 || h != 50 {
		t.Errorf("expected 100×50 for orientation-1 (no-op), got %d×%d", w, h)
	}
}

// TestGetThumbnailByWebPhoto_AppliesExifOrientation verifies that the fallback
// thumbnail generator respects EXIF orientation (portrait photo stored landscape).
func TestGetThumbnailByWebPhoto_AppliesExifOrientation(t *testing.T) {
	path := makeOrientedJPEG(t, 6) // landscape pixels, orientation=6 means 90° CW needed
	data, err := GetThumbnailByWebPhoto(path, 100, 0)
	if err != nil {
		t.Fatalf("GetThumbnailByWebPhoto: %v", err)
	}
	decoded, err := imaging.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode result: %v", err)
	}
	b := decoded.Bounds()
	w, h := b.Max.X-b.Min.X, b.Max.Y-b.Min.Y
	// After orientation-6 correction a 100×50 image becomes 50×100;
	// resizing by width=100 → final size is 100×200.
	if w >= h {
		t.Errorf("expected portrait (height > width) after orientation-6, got %d×%d", w, h)
	}
}

// TestApplyOrientationToThumbnail_Orientation8 verifies 90° CCW rotation.
func TestApplyOrientationToThumbnail_Orientation8(t *testing.T) {
	src := imaging.New(100, 50, color.NRGBA{200, 100, 50, 255})
	buf := bytes.Buffer{}
	if err := imaging.Encode(&buf, src, imaging.JPEG); err != nil {
		t.Fatalf("failed to encode test image: %v", err)
	}

	result := applyOrientationToThumbnail(buf.Bytes(), 8)

	decoded, err := imaging.Decode(bytes.NewReader(result))
	if err != nil {
		t.Fatalf("failed to decode result: %v", err)
	}
	b := decoded.Bounds()
	w, h := b.Max.X-b.Min.X, b.Max.Y-b.Min.Y

	// Orientation 8 = 90° CCW: 100x50 → 50x100
	if w != 50 || h != 100 {
		t.Errorf("expected 50×100 after orientation-8 rotation, got %d×%d", w, h)
	}
}
