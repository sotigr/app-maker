package e2e

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

// ADR 0022: document.render produces a PDF blob that a flow sets on a
// field, and email.send attaches it, choosing the template of the
// caller's language.
func TestDocumentsAndAttachments(t *testing.T) {
	eachDriver(t, func(t *testing.T, driver string) {
		w := setup(t, driver)
		me := mustOK(t, w.editor.do("GET", "/v1/features/user/"+w.editor.id, nil), 200)
		mustOK(t, w.editor.do("PATCH", "/v1/features/user/"+w.editor.id, map[string]any{"locale": "el"}, "If-Match", me.Header.Get("ETag")), 200)
		mustOK(t, w.editor.do("POST", "/v1/actions/task/"+w.task+"/report", map[string]any{}), 200)
		task := mustOK(t, w.editor.do("GET", "/v1/features/task/"+w.task, nil), 200)
		att := task.Body["data"].(map[string]any)["attachment"].(map[string]any)
		if att["contentType"] != "application/pdf" {
			t.Fatalf("attachment: %v", att)
		}
		res, err := http.Get(w.s.url + att["url"].(string))
		if err != nil {
			t.Fatal(err)
		}
		pdf, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		if res.StatusCode != 200 || !bytes.HasPrefix(pdf, []byte("%PDF-")) || res.Header.Get("Content-Disposition") != "inline" {
			t.Fatalf("download: %d %v", res.StatusCode, res.Header)
		}
		if out := os.Getenv("ORGANON_TEST_PDF"); out != "" && driver == "memory" {
			_ = os.WriteFile(out, pdf, 0o644)
		}
		eventually(t, "the report email", func() bool {
			for _, m := range w.s.emails.Sent() {
				if strings.HasPrefix(m.Subject, "Αναφορά εργασίας") && len(m.Attachments) == 1 &&
					strings.HasSuffix(m.Attachments[0].Name, ".pdf") && bytes.Equal(m.Attachments[0].Data, pdf) {
					return true
				}
			}
			return false
		})
	})
}
