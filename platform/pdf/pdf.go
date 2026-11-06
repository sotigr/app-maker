// Package pdf lays out a kernel.Document and writes it as PDF (ADR 0022).
// Text uses the embedded Noto Sans (Latin, Greek, Cyrillic), subset into
// each file.
package pdf

import (
	"bytes"
	_ "embed"
	"fmt"
	"strconv"
	"strings"

	"github.com/go-pdf/fpdf"
	"rsc.io/qr"

	"organon/internal/kernel"
)

var (
	//go:embed fonts/NotoSans-Regular.ttf
	regular []byte
	//go:embed fonts/NotoSans-Bold.ttf
	bold []byte
)

const (
	family   = "noto"
	baseSize = 10.0
	lineH    = 1.45 // line height, as a multiple of the font size in mm
	ptToMM   = 0.3528
)

var sizes = map[string]bool{"A4": true, "A5": true, "A3": true, "Letter": true, "Legal": true}

// Render writes d as a PDF.
func Render(d kernel.Document) ([]byte, error) {
	size := d.Size
	if size == "" {
		size = "A4"
	}
	if !sizes[size] {
		return nil, fmt.Errorf("unknown page size %q", size)
	}
	orient := "P"
	if d.Landscape {
		orient = "L"
	}
	margin := d.Margin
	if margin <= 0 {
		margin = 15
	}
	p := fpdf.New(orient, "mm", size, "")
	p.SetCreator("Organon", true)
	p.AddUTF8FontFromBytes(family, "", regular)
	p.AddUTF8FontFromBytes(family, "B", bold)
	p.SetMargins(margin, margin, margin)
	p.AliasNbPages("{nb}")
	r := &renderer{p: p, margin: margin, images: map[string]bool{}}
	footerH := r.measure(d.Footer)
	p.SetAutoPageBreak(true, margin+footerH)
	if len(d.Header) > 0 {
		p.SetHeaderFunc(func() {
			r.blocks(d.Header)
			p.Ln(3)
		})
	}
	if len(d.Footer) > 0 {
		p.SetFooterFunc(func() {
			_, h := p.GetPageSize()
			p.SetY(h - margin - footerH)
			r.blocks(d.Footer)
		})
	}
	p.AddPage()
	r.blocks(d.Body)
	if err := p.Error(); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := p.Output(&out); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

type renderer struct {
	p      *fpdf.Fpdf
	margin float64
	images map[string]bool
	n      int
}

func (r *renderer) width() float64 {
	w, _ := r.p.GetPageSize()
	return w - 2*r.margin
}

// measure estimates the height of footer blocks (one line per text block).
func (r *renderer) measure(bs []kernel.DocBlock) float64 {
	h := 0.0
	for _, b := range bs {
		switch b.Kind {
		case kernel.BlockSpacer:
			h += b.Space
		case kernel.BlockQR, kernel.BlockImage:
			h += max(b.Width, 20)
		default:
			h += textSize(b) * ptToMM * lineH * float64(max(1, strings.Count(plain(b.Runs), "\n")+1))
		}
	}
	return h
}

func textSize(b kernel.DocBlock) float64 {
	if b.Kind == kernel.BlockHeading {
		switch b.Level {
		case 1:
			return 18
		case 2:
			return 14
		default:
			return 12
		}
	}
	if b.Size > 0 {
		return b.Size
	}
	return baseSize
}

func plain(runs []kernel.DocRun) string {
	var s strings.Builder
	for _, r := range runs {
		s.WriteString(r.Text)
	}
	return s.String()
}

// pageText fills the page placeholders of headers and footers.
func (r *renderer) pageText(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "{page}", strconv.Itoa(r.p.PageNo())), "{pages}", "{nb}")
}

func (r *renderer) blocks(bs []kernel.DocBlock) {
	for _, b := range bs {
		r.block(b)
	}
}

func (r *renderer) block(b kernel.DocBlock) {
	p := r.p
	switch b.Kind {
	case kernel.BlockHeading, kernel.BlockText:
		size := textSize(b)
		h := size * ptToMM * lineH
		bold := b.Kind == kernel.BlockHeading
		if b.Kind == kernel.BlockHeading {
			p.Ln(1)
		}
		if b.Align == "center" || b.Align == "right" {
			// Aligned text is one weight: bold when any run is.
			for _, run := range b.Runs {
				bold = bold || run.Bold
			}
			r.font(bold, size)
			p.MultiCell(0, h, r.pageText(plain(b.Runs)), "", map[string]string{"center": "C", "right": "R"}[b.Align], false)
		} else {
			for _, run := range b.Runs {
				r.font(bold || run.Bold, size)
				p.Write(h, r.pageText(run.Text))
			}
			p.Ln(h)
		}
		if b.Kind == kernel.BlockHeading {
			p.Ln(1)
		}
	case kernel.BlockFields:
		cols := max(b.Columns, 1)
		colW := r.width() / float64(cols)
		labelW := colW * 0.38
		h := baseSize * ptToMM * lineH
		for i := 0; i < len(b.Fields); i += cols {
			x0, y0 := p.GetX(), p.GetY()
			bottom := y0
			for c := 0; c < cols && i+c < len(b.Fields); c++ {
				f := b.Fields[i+c]
				x := r.margin + float64(c)*colW
				p.SetXY(x, y0)
				r.font(true, baseSize-1)
				p.SetTextColor(90, 96, 110)
				p.MultiCell(labelW, h, f[0], "", "L", false)
				p.SetTextColor(0, 0, 0)
				p.SetXY(x+labelW, y0)
				r.font(false, baseSize)
				p.MultiCell(colW-labelW-2, h, f[1], "", "L", false)
				bottom = max(bottom, p.GetY())
			}
			p.SetXY(x0, bottom)
		}
		p.Ln(2)
	case kernel.BlockTable:
		r.table(b.Table)
	case kernel.BlockImage, kernel.BlockQR:
		data, typ := b.Image, b.ImageType
		if b.Kind == kernel.BlockQR {
			code, err := qr.Encode(b.QR, qr.M)
			if err != nil {
				p.SetError(fmt.Errorf("qr: %w", err))
				return
			}
			data, typ = code.PNG(), "png"
		}
		r.n++
		name := "img" + strconv.Itoa(r.n)
		info := p.RegisterImageOptionsReader(name, fpdf.ImageOptions{ImageType: typ, ReadDpi: true}, bytes.NewReader(data))
		if info == nil || p.Err() {
			return
		}
		w := b.Width
		if w <= 0 {
			w = min(info.Width(), r.width())
			if b.Kind == kernel.BlockQR {
				w = 30
			}
		}
		x := r.margin
		switch b.Align {
		case "center":
			x = r.margin + (r.width()-w)/2
		case "right":
			x = r.margin + r.width() - w
		}
		p.ImageOptions(name, x, p.GetY(), min(w, r.width()), 0, true, fpdf.ImageOptions{ImageType: typ}, 0, "")
		p.Ln(2)
	case kernel.BlockSpacer:
		p.Ln(b.Space)
	case kernel.BlockPageBreak:
		p.AddPage()
	}
}

func (r *renderer) font(bold bool, size float64) {
	style := ""
	if bold {
		style = "B"
	}
	r.p.SetFont(family, style, size)
}

// table draws a header row, wrapped rows (repeating the header after a
// page break), and bold footer rows.
func (r *renderer) table(t *kernel.DocTable) {
	if t == nil || len(t.Columns) == 0 {
		return
	}
	p := r.p
	total := 0.0
	for _, c := range t.Columns {
		total += colWeight(c)
	}
	widths := make([]float64, len(t.Columns))
	for i, c := range t.Columns {
		widths[i] = r.width() * colWeight(c) / total
	}
	h := baseSize * ptToMM * lineH
	header := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		header[i] = c.Label
	}
	_, pageH := p.GetPageSize()
	_, _, _, bottomMargin := p.GetMargins()
	row := func(cells []string, bold, fill bool) {
		r.font(bold, baseSize)
		lines := 1
		for i, c := range cells {
			if i < len(widths) {
				lines = max(lines, len(p.SplitText(c, widths[i]-2)))
			}
		}
		rowH := float64(lines)*h + 1.5
		if p.GetY()+rowH > pageH-bottomMargin {
			p.AddPage()
			if !fill {
				r.row(header, widths, h, h+1.5, true, true, t.Columns)
			}
		}
		r.row(cells, widths, h, rowH, bold, fill, t.Columns)
	}
	row(header, true, true)
	for _, cells := range t.Rows {
		row(cells, false, false)
	}
	for _, cells := range t.Footer {
		row(cells, true, false)
	}
	p.Ln(3)
}

func (r *renderer) row(cells []string, widths []float64, lineH, rowH float64, bold, fill bool, cols []kernel.DocColumn) {
	p := r.p
	r.font(bold, baseSize)
	x, y := r.margin, p.GetY()
	if fill {
		p.SetFillColor(238, 240, 245)
		p.Rect(x, y, r.width(), rowH, "F")
	}
	for i, w := range widths {
		text := ""
		if i < len(cells) {
			text = cells[i]
		}
		align := map[string]string{"center": "C", "right": "R"}[cols[i].Align]
		if align == "" {
			align = "L"
		}
		p.SetXY(x+1, y+0.75)
		p.MultiCell(w-2, lineH, text, "", align, false)
		x += w
	}
	p.SetDrawColor(220, 223, 230)
	p.Line(r.margin, y+rowH, r.margin+r.width(), y+rowH)
	p.SetXY(r.margin, y+rowH)
}

func colWeight(c kernel.DocColumn) float64 {
	if c.Width > 0 {
		return c.Width
	}
	return 1
}
