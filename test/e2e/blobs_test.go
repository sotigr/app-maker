package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	integrationapp "organon/internal/integration/application"
	"organon/internal/kernel"
)

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		img.Set(x, x%h, color.RGBA{R: 200, A: 255})
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// raw sends a request without the client's credentials (signed URLs).
func (s *server) raw(method, path, contentType string, body []byte) (int, http.Header, []byte) {
	s.t.Helper()
	req, _ := http.NewRequest(method, s.url+path, bytes.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header, b
}

func (w *world) uploadBlob(t *testing.T, c *client, task, contentType string, data []byte) map[string]any {
	t.Helper()
	r := mustOK(t, c.do("POST", "/v1/uploads", map[string]any{"type": "task", "feature": task, "field": "attachment",
		"size": len(data), "contentType": contentType}), 201)
	if status, _, body := w.s.raw("PUT", r.str("uploadUrl"), contentType, data); status != 204 {
		t.Fatalf("upload: %d %s", status, body)
	}
	return r.Body["blob"].(map[string]any)
}

// Spec 14.4: signed uploads checked against policy and schema limits,
// blobs committed through normal writes, signed downloads for readers,
// thumbnails, and cleanup of unreferenced blobs.
func TestBlobUploadsAndDownloads(t *testing.T) {
	eachDriver(t, func(t *testing.T, driver string) {
		w := setup(t, driver)
		img := pngBytes(t, 200, 100)

		// Policy and schema limits.
		req := map[string]any{"type": "task", "feature": w.task, "field": "attachment", "size": len(img), "contentType": "image/png"}
		if r := w.viewer.do("POST", "/v1/uploads", req); r.Status != 403 {
			t.Fatalf("viewer upload: %s", r.Raw)
		}
		if r := w.stranger.do("POST", "/v1/uploads", req); r.Status != 404 {
			t.Fatalf("stranger upload: %s", r.Raw)
		}
		for _, bad := range []map[string]any{
			{"type": "task", "feature": w.task, "field": "attachment", "size": 32 << 20, "contentType": "image/png"},
			{"type": "task", "feature": w.task, "field": "attachment", "size": 10, "contentType": "application/zip"},
			{"type": "task", "feature": w.task, "field": "title", "size": 10, "contentType": "text/plain"},
		} {
			if r := w.editor.do("POST", "/v1/uploads", bad); r.Status != 400 {
				t.Fatalf("invalid upload %v: %s", bad, r.Raw)
			}
		}

		// Upload: the signature, size, and content type are enforced.
		ticket := mustOK(t, w.editor.do("POST", "/v1/uploads", req), 201)
		url := ticket.str("uploadUrl")
		if status, _, _ := w.s.raw("PUT", strings.Replace(url, "sig=", "sig=0", 1), "image/png", img); status != 403 {
			t.Fatalf("tampered signature: %d", status)
		}
		if status, _, _ := w.s.raw("PUT", url, "image/gif", img); status != 400 {
			t.Fatalf("wrong content type: %d", status)
		}
		if status, _, _ := w.s.raw("PUT", url, "image/png", img[:10]); status != 400 {
			t.Fatalf("short body: %d", status)
		}
		blob := ticket.Body["blob"].(map[string]any)
		// Not uploaded yet: the write is rejected.
		if r := w.editor.do("PATCH", "/v1/features/task/"+w.task, map[string]any{"attachment": blob}, "If-Match", w.taskVersion(t, w.task)); r.Status != 400 {
			t.Fatalf("commit before upload: %s", r.Raw)
		}
		if status, _, body := w.s.raw("PUT", url, "image/png", img); status != 204 {
			t.Fatalf("upload: %d %s", status, body)
		}
		if status, _, _ := w.s.raw("PUT", url, "image/png", img); status != 409 {
			t.Fatalf("second upload: %d", status)
		}
		// Another principal cannot commit someone else's upload.
		if r := w.admin.do("PATCH", "/v1/features/task/"+w.task, map[string]any{"attachment": blob}, "If-Match", w.taskVersion(t, w.task)); r.Status != 400 {
			t.Fatalf("foreign blob: %s", r.Raw)
		}
		r := mustOK(t, w.editor.do("PATCH", "/v1/features/task/"+w.task, map[string]any{"attachment": blob}, "If-Match", w.taskVersion(t, w.task)), 200)
		att := r.Body["data"].(map[string]any)["attachment"].(map[string]any)
		if att["id"] != blob["id"] || att["url"] == nil {
			t.Fatalf("committed attachment: %s", r.Raw)
		}

		// Readers get a signed URL; the bytes come back unchanged.
		got := mustOK(t, w.viewer.do("GET", "/v1/features/task/"+w.task, nil), 200)
		att = got.Body["data"].(map[string]any)["attachment"].(map[string]any)
		status, hdr, body := w.s.raw("GET", att["url"].(string), "", nil)
		if status != 200 || !bytes.Equal(body, img) || hdr.Get("Content-Type") != "image/png" || hdr.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("download: %d %v", status, hdr)
		}
		if status, _, _ := w.s.raw("GET", strings.Replace(att["url"].(string), "sig=", "sig=0", 1), "", nil); status != 403 {
			t.Fatalf("tampered download: %d", status)
		}
		// The thumbnail is rendered after commit; until then its URL serves
		// the original, uncached.
		thumbURL := att["thumbnails"].(map[string]any)["64"].(string)
		var thumb []byte
		eventually(t, "thumbnail", func() bool {
			s, h, b := w.s.raw("GET", thumbURL, "", nil)
			thumb = b
			return s == 200 && h.Get("Cache-Control") != "no-store"
		})
		ti, err := png.Decode(bytes.NewReader(thumb))
		if err != nil || ti.Bounds().Dx() != 64 || ti.Bounds().Dy() != 32 {
			t.Fatalf("thumbnail: %v %v", err, ti)
		}
		// A committed blob cannot be reused on another feature.
		other := w.newTask(t, map[string]any{"title": "Other"})
		if r := w.admin.do("PATCH", "/v1/features/task/"+other, map[string]any{"attachment": blob}, "If-Match", w.taskVersion(t, other)); r.Status != 400 {
			t.Fatalf("reused blob: %s", r.Raw)
		}

		// Replacing the attachment releases the old blob; cleanup deletes
		// it (and a never-used upload) 24 hours later.
		next := w.uploadBlob(t, w.editor, w.task, "text/plain", []byte("notes"))
		mustOK(t, w.editor.do("PATCH", "/v1/features/task/"+w.task, map[string]any{"attachment": next}, "If-Match", w.taskVersion(t, w.task)), 200)
		stale := w.uploadBlob(t, w.editor, w.task, "text/plain", []byte("never used"))
		admin := w.admin
		blobState := func(id string) string {
			return admin.do("GET", "/v1/features/blob/"+id, nil).str("data", "state")
		}
		eventually(t, "release", func() bool { return blobState(blob["id"].(string)) == "released" })
		w.s.clock.Advance(25 * time.Hour)
		admin = w.s.admin() // sessions expired with the clock
		if err := w.s.rt.Enqueuer.Enqueue(context.Background(), []kernel.JobRequest{{Kind: integrationapp.JobCleanupBlobs,
			Queue: kernel.QueueMaintenance, Args: json.RawMessage("{}"), MaxAttempts: 1}}); err != nil {
			t.Fatal(err)
		}
		eventually(t, "cleanup", func() bool {
			return blobState(blob["id"].(string)) == "deleted" && blobState(stale["id"].(string)) == "deleted"
		})
		if s := blobState(next["id"].(string)); s != "committed" {
			t.Fatalf("committed blob state %q", s)
		}
		if status, _, _ := w.s.raw("GET", att["url"].(string), "", nil); status != 404 && status != 403 {
			t.Fatalf("deleted blob still served: %d", status)
		}
	})
}

// Spec 14.4 and ADR 0016: downloads the browser can show (images, PDF,
// video, audio) are served inline and frameable by the app, with Range
// support; thumbnail URLs exist only for thumbnailable types and fall back
// to the original until the thumbnail exists.
func TestBlobPreviews(t *testing.T) {
	eachDriver(t, func(t *testing.T, driver string) {
		w := setup(t, driver)
		commit := func(blob map[string]any) map[string]any {
			t.Helper()
			r := mustOK(t, w.editor.do("PATCH", "/v1/features/task/"+w.task, map[string]any{"attachment": blob}, "If-Match", w.taskVersion(t, w.task)), 200)
			return r.Body["data"].(map[string]any)["attachment"].(map[string]any)
		}

		// No thumbnails are offered for types that never get them.
		att := commit(w.uploadBlob(t, w.editor, w.task, "image/webp", []byte("RIFF....WEBPVP8 ")))
		if _, ok := att["thumbnails"]; ok {
			t.Fatalf("webp thumbnails: %v", att)
		}

		// A thumbnail that cannot be rendered serves the original, uncached.
		broken := []byte("not really a png")
		att = commit(w.uploadBlob(t, w.editor, w.task, "image/png", broken))
		thumbURL := att["thumbnails"].(map[string]any)["64"].(string)
		status, hdr, body := w.s.raw("GET", thumbURL, "", nil)
		if status != 200 || !bytes.Equal(body, broken) || hdr.Get("Cache-Control") != "no-store" {
			t.Fatalf("thumbnail fallback: %d %v %q", status, hdr, body)
		}
		if status, _, _ := w.s.raw("GET", strings.Replace(thumbURL, "sig=", "sig=0", 1), "", nil); status != 403 {
			t.Fatalf("tampered thumbnail: %d", status)
		}

		// PDF: inline, frameable by the app only, still sandboxed; ranges work.
		pdf := []byte("%PDF-1.4 a small document body")
		att = commit(w.uploadBlob(t, w.editor, w.task, "application/pdf", pdf))
		status, hdr, _ = w.s.raw("GET", att["url"].(string), "", nil)
		csp := hdr.Get("Content-Security-Policy")
		if status != 200 || hdr.Get("Content-Disposition") != "inline" || !strings.Contains(csp, "sandbox") || !strings.Contains(csp, "frame-ancestors 'self'") {
			t.Fatalf("pdf download: %d %v", status, hdr)
		}
		req, _ := http.NewRequest("GET", w.s.url+att["url"].(string), nil)
		req.Header.Set("Range", "bytes=0-7")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		part, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		if res.StatusCode != 206 || string(part) != "%PDF-1.4" || res.Header.Get("Content-Range") != "bytes 0-7/"+strconv.Itoa(len(pdf)) {
			t.Fatalf("range: %d %v %q", res.StatusCode, res.Header, part)
		}

		// Other types stay downloads.
		att = commit(w.uploadBlob(t, w.editor, w.task, "text/plain", []byte("notes")))
		if _, hdr, _ := w.s.raw("GET", att["url"].(string), "", nil); hdr.Get("Content-Disposition") != "attachment" {
			t.Fatalf("text download: %v", hdr)
		}
	})
}
