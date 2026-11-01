package infrastructure

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"organon/internal/integration/domain"
)

// Spec 14.4 / 15.7: the BlobStore contract on the filesystem adapter.
func TestBlobFS(t *testing.T) {
	ctx := context.Background()
	s := BlobFS{Dir: t.TempDir()}
	if err := s.Put(ctx, "ab-1", strings.NewReader("hello"), 5, "text/plain"); err != nil {
		t.Fatal(err)
	}
	r, info, err := s.Get(ctx, "ab-1")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r)
	_ = r.Close()
	if string(b) != "hello" || info.Size != 5 || info.ContentType != "text/plain" {
		t.Fatalf("got %q %+v", b, info)
	}
	// Wrong lengths leave nothing behind.
	for _, body := range []string{"hell", "hello!"} {
		if err := s.Put(ctx, "ab-2", strings.NewReader(body), 5, "text/plain"); err == nil {
			t.Fatalf("%q accepted for size 5", body)
		}
	}
	if _, _, err := s.Get(ctx, "ab-2"); !errors.Is(err, domain.ErrBlobNotFound) {
		t.Fatalf("partial blob: %v", err)
	}
	if err := s.Put(ctx, "../x", strings.NewReader(""), 0, "text/plain"); err == nil {
		t.Fatal("path traversal accepted")
	}
	if err := s.Delete(ctx, "ab-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "ab-1"); err != nil {
		t.Fatalf("second delete: %v", err)
	}
	if _, _, err := s.Get(ctx, "ab-1"); !errors.Is(err, domain.ErrBlobNotFound) {
		t.Fatalf("deleted blob: %v", err)
	}
}
