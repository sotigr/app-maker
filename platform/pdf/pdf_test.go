package pdf

import (
	"bytes"
	"os"
	"regexp"
	"testing"

	"organon/internal/kernel"
)

// ADR 0022: a document with Greek text, fields, a table, a QR code, and a
// page break renders to a PDF with the embedded font and one page per
// break.
func TestRender(t *testing.T) {
	rows := [][]string{}
	for i := 0; i < 60; i++ {
		rows = append(rows, []string{"Αλλαγή λαδιών και φίλτρου", "1", "45,00 €"})
	}
	d := kernel.Document{
		Header: []kernel.DocBlock{{Kind: kernel.BlockText, Runs: []kernel.DocRun{{Text: "Μοτο Συνεργείο", Bold: true}}, Align: "right"}},
		Footer: []kernel.DocBlock{{Kind: kernel.BlockText, Runs: []kernel.DocRun{{Text: "Σελίδα {page} από {pages}"}}, Align: "center", Size: 8}},
		Body: []kernel.DocBlock{
			{Kind: kernel.BlockHeading, Level: 1, Runs: []kernel.DocRun{{Text: "Τιμολόγιο INV-2026-000042"}}},
			{Kind: kernel.BlockText, Runs: []kernel.DocRun{{Text: "Πελάτης: "}, {Text: "Γιώργος Π.", Bold: true}}},
			{Kind: kernel.BlockFields, Columns: 2, Fields: [][2]string{{"Ημερομηνία", "06/11/2026"}, {"ΑΦΜ", "123456789"}}},
			{Kind: kernel.BlockTable, Table: &kernel.DocTable{
				Columns: []kernel.DocColumn{{Label: "Περιγραφή", Width: 4}, {Label: "Ποσ.", Align: "right"}, {Label: "Ποσό", Align: "right"}},
				Rows:    rows, Footer: [][]string{{"Σύνολο", "", "2.700,00 €"}}}},
			{Kind: kernel.BlockQR, QR: "https://www1.aade.gr/tameiakes/myweb/q1.php?SIG=EXAMPLE", Width: 25},
			{Kind: kernel.BlockPageBreak},
			{Kind: kernel.BlockText, Runs: []kernel.DocRun{{Text: "Όροι εγγύησης."}}},
		},
	}
	out, err := Render(d)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(os.Getenv("PDF_OUT"), out, 0o644)
	if !bytes.HasPrefix(out, []byte("%PDF-")) || !bytes.Contains(out, []byte("FontFile2")) {
		t.Fatalf("not a PDF with the embedded font (%d bytes)", len(out))
	}
	if n := len(regexp.MustCompile(`/Type /Page\b`).FindAll(out, -1)); n < 3 {
		t.Fatalf("want at least 3 pages (long table + break), got %d", n)
	}
	if _, err := Render(kernel.Document{Size: "B9"}); err == nil {
		t.Fatal("unknown page sizes fail")
	}
}
