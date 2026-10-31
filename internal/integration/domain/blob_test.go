package domain

import (
	"bytes"
	"image"
	"image/png"
	"strconv"
	"testing"
	"time"
)

func TestBlobSigner(t *testing.T) {
	s := BlobSigner{Key: []byte("k")}
	now := time.Unix(1_000_000, 0)
	exp := now.Add(time.Minute)
	sig := s.Sign("get", "b1", exp)
	e := strconv.FormatInt(exp.Unix(), 10)
	if !s.Valid("get", "b1", e, sig, now) {
		t.Fatal("valid signature rejected")
	}
	for name, ok := range map[string]bool{
		"other op":   s.Valid("put", "b1", e, sig, now),
		"other blob": s.Valid("get", "b2", e, sig, now),
		"expired":    s.Valid("get", "b1", e, sig, now.Add(2*time.Minute)),
		"other key":  BlobSigner{Key: []byte("x")}.Valid("get", "b1", e, sig, now),
		"bad exp":    s.Valid("get", "b1", "soon", sig, now),
	} {
		if ok {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestThumbnail(t *testing.T) {
	var src bytes.Buffer
	_ = png.Encode(&src, image.NewRGBA(image.Rect(0, 0, 300, 120)))
	out, ct, err := Thumbnail(src.Bytes(), "image/png", 100)
	if err != nil || ct != "image/png" {
		t.Fatal(err, ct)
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil || img.Bounds().Dx() != 100 || img.Bounds().Dy() != 40 {
		t.Fatalf("thumbnail %v %v", err, img.Bounds())
	}
	// Small images are not enlarged.
	out, _, _ = Thumbnail(src.Bytes(), "image/png", 1000)
	if img, _ := png.Decode(bytes.NewReader(out)); img.Bounds().Dx() != 300 {
		t.Fatalf("enlarged to %v", img.Bounds())
	}
	if _, _, err := Thumbnail([]byte("x"), "text/plain", 64); err != ErrNotImage {
		t.Fatalf("text: %v", err)
	}
	if _, _, err := Thumbnail([]byte("not a png"), "image/png", 64); err == nil {
		t.Fatal("garbage decoded")
	}
}
