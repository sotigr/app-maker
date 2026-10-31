package domain

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
)

// ErrNotImage is returned for content types without thumbnails.
var ErrNotImage = errors.New("not an image")

// maxPixels bounds decoded images (decompression bombs).
const maxPixels = 40_000_000

// Thumbnail scales an image to fit a size×size box (never enlarging) with
// box averaging. JPEG stays JPEG; PNG and GIF become PNG.
func Thumbnail(src []byte, contentType string, size int) ([]byte, string, error) {
	var decode func([]byte) (image.Image, error)
	var cfg func([]byte) (image.Config, error)
	switch contentType {
	case "image/jpeg":
		decode = func(b []byte) (image.Image, error) { return jpeg.Decode(bytes.NewReader(b)) }
		cfg = func(b []byte) (image.Config, error) { return jpeg.DecodeConfig(bytes.NewReader(b)) }
	case "image/png":
		decode = func(b []byte) (image.Image, error) { return png.Decode(bytes.NewReader(b)) }
		cfg = func(b []byte) (image.Config, error) { return png.DecodeConfig(bytes.NewReader(b)) }
	case "image/gif":
		decode = func(b []byte) (image.Image, error) { return gif.Decode(bytes.NewReader(b)) }
		cfg = func(b []byte) (image.Config, error) { return gif.DecodeConfig(bytes.NewReader(b)) }
	default:
		return nil, "", ErrNotImage
	}
	if size <= 0 {
		return nil, "", fmt.Errorf("invalid thumbnail size %d", size)
	}
	c, err := cfg(src)
	if err != nil {
		return nil, "", err
	}
	if c.Width <= 0 || c.Height <= 0 || c.Width*c.Height > maxPixels {
		return nil, "", fmt.Errorf("image of %dx%d is too large", c.Width, c.Height)
	}
	img, err := decode(src)
	if err != nil {
		return nil, "", err
	}
	out := scale(img, size)
	var buf bytes.Buffer
	if contentType == "image/jpeg" {
		err = jpeg.Encode(&buf, out, &jpeg.Options{Quality: 85})
		return buf.Bytes(), "image/jpeg", err
	}
	err = png.Encode(&buf, out)
	return buf.Bytes(), "image/png", err
}

func scale(img image.Image, size int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	nw, nh := w, h
	if w > size || h > size {
		if w >= h {
			nw, nh = size, max(1, h*size/w)
		} else {
			nw, nh = max(1, w*size/h), size
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		y0, y1 := b.Min.Y+y*h/nh, b.Min.Y+max((y+1)*h/nh, y*h/nh+1)
		for x := 0; x < nw; x++ {
			x0, x1 := b.Min.X+x*w/nw, b.Min.X+max((x+1)*w/nw, x*w/nw+1)
			var r, g, bl, a, n uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					cr, cg, cb, ca := img.At(sx, sy).RGBA()
					r, g, bl, a, n = r+uint64(cr), g+uint64(cg), bl+uint64(cb), a+uint64(ca), n+1
				}
			}
			dst.Set(x, y, color.RGBA64{uint16(r / n), uint16(g / n), uint16(bl / n), uint16(a / n)})
		}
	}
	return dst
}
