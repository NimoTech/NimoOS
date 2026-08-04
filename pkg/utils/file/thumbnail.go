package file

import (
	"bytes"

	"github.com/disintegration/imaging"
)

// Thumbnail generation parameters for BF23 ("actual thumbnailing"). A long-edge cap of
// 400px at JPEG quality ~70 targets ~20-50KB output for typical photos,
// versus the multi-hundred-KB-to-multi-MB originals that were previously
// served unchanged.
const (
	// ThumbnailMaxEdge is the maximum length, in pixels, of the longer side
	// of a generated thumbnail. Images already smaller than this are not
	// upscaled (see imaging.Fit).
	ThumbnailMaxEdge = 400
	// ThumbnailJPEGQuality is the JPEG encode quality used for thumbnails.
	ThumbnailJPEGQuality = 70
)

// GenerateThumbnail decodes the image at path — applying EXIF auto-rotation
// so the output is right-side-up regardless of how the camera stored it —
// downsizes it so neither dimension exceeds ThumbnailMaxEdge (preserving
// aspect ratio, never upscaling), and re-encodes the result as JPEG at
// ThumbnailJPEGQuality.
//
// It returns an error for anything imaging cannot decode (e.g. HEIC, or a
// corrupt/non-image file); callers should fall back to serving the original
// file or a generic placeholder icon in that case.
func GenerateThumbnail(path string) ([]byte, error) {
	src, err := imaging.Open(path, imaging.AutoOrientation(true))
	if err != nil {
		return nil, err
	}

	thumb := imaging.Fit(src, ThumbnailMaxEdge, ThumbnailMaxEdge, imaging.CatmullRom)

	buf := &bytes.Buffer{}
	if err := imaging.Encode(buf, thumb, imaging.JPEG, imaging.JPEGQuality(ThumbnailJPEGQuality)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
