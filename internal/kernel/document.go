package kernel

// Document is a rendered document template (ADR 0022): text is final,
// images are bytes. The flow context produces it; platform/pdf lays it out.
type Document struct {
	Size      string // A4 (default), A5, Letter
	Landscape bool
	Margin    float64 // millimetres (default 15)
	Header    []DocBlock
	Footer    []DocBlock // "{page}" and "{pages}" are replaced per page
	Body      []DocBlock
}

// Document block kinds.
const (
	BlockHeading   = "heading"
	BlockText      = "text"
	BlockFields    = "fields"
	BlockTable     = "table"
	BlockImage     = "image"
	BlockQR        = "qr"
	BlockSpacer    = "spacer"
	BlockPageBreak = "pageBreak"
)

// DocBlock is one block of a document.
type DocBlock struct {
	Kind  string
	Runs  []DocRun // heading, text
	Level int      // heading 1-3
	Align string   // left (default), center, right
	Size  float64  // text size in points; 0 = default
	// Fields are label/value pairs in Columns columns.
	Fields  [][2]string
	Columns int
	Table   *DocTable
	// Image is PNG, JPEG, or GIF bytes; Width in millimetres (0 = natural,
	// at most the text width).
	Image     []byte
	ImageType string // png, jpg, gif
	Width     float64
	// QR is the text encoded by a qr block (Width is its size).
	QR    string
	Space float64 // spacer height in millimetres
}

// DocRun is text with an optional bold weight.
type DocRun struct {
	Text string
	Bold bool
}

// DocTable is a table with a header row and optional footer rows.
type DocTable struct {
	Columns []DocColumn
	Rows    [][]string
	Footer  [][]string
}

// DocColumn is a table column; Width is a relative weight (default 1).
type DocColumn struct {
	Label string
	Width float64
	Align string
}
