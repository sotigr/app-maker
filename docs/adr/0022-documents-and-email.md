# 0022: PDF documents, email attachments, and SMTP

Status: accepted (2026-11-06)

## Context
Apps need invoices, job cards, and receipts as files, and Greek invoices
carry a QR code for myDATA. Python nodes cannot write and return JSON
only; `email.send` had no attachments, one template per email, and only
a log sender. The user chose a pure-Go layout from JSON templates, with no
browser in the image (2026-11-06).

## Decision
- **Templates** `templates/documents/<name>.json`: `vars` (a JSON Schema
  object; declared properties are closed, so typos are errors), `page`
  (`size` A3/A4/A5/Letter/Legal, `orientation`, `margin` mm), `locale`
  (number and date formats), `timezone`, and `header`, `footer`, `body`
  lists of blocks: `heading` (level 1-3), `text` (`align`, `size`),
  `fields` (label/value grid, `columns`), `table` (`rows` expression,
  `columns` with `label`, `value`, `width`, `align`, optional `footer`
  rows), `image` (a blob value or a `templates/...` PNG/JPEG/GIF),
  `qr`, `spacer`, `pageBreak`; any block may have `when`.
  Text is literal with `{{ expr }}` interpolations over `vars` (and `row`,
  `index` in table cells; `page`, `pages` in headers and footers);
  `**bold**` spans. Functions: `now()`, `formatNumber(n, digits)`,
  `formatDate(t, 'date' | 'datetime' | 'time' | <Go layout>)`.
  `organon validate` type-checks every expression and checks that
  `document.render` nodes name existing templates.
- **Layout and PDF** (`platform/pdf`): `github.com/go-pdf/fpdf` writes the
  PDF with the embedded Noto Sans regular and bold (SIL OFL 1.1; Latin,
  Greek, Cyrillic), subset per file; tables wrap cells and repeat their
  header on new pages; `rsc.io/qr` draws QR codes. The evaluated document
  is a `kernel.Document`, so the flow domain and the PDF writer share it
  without depending on each other. Both binaries embed `time/tzdata`.
- **`document.render`** (External): `{template, vars, type, field}` →
  `{id, size, contentType}`. The PDF is stored as an uploaded blob for
  that Blob field and principal, under the field's `x-maxBytes` and
  `x-contentTypes`, so a following `feature.update` in the run sets it
  (unused blobs are cleaned up after 24 hours as for uploads).
- **`email.send`** gains `attachments` (blob values or `{blob, name}`, at
  most 10 MiB in all, read from blob storage at delivery) and `lang`
  (`templates/email/<name>.<lang>.txt`, then the base language, then
  `<name>.txt`).
- **SMTP**: server setting `email: {driver: smtp, addr, from, username,
  password: ${secret:...}, tls}` (env `ORGANON_EMAIL_*`); STARTTLS when
  offered, or implicit TLS; the delivery key is the Message-ID. The log
  sender stays the default.

## Consequences
- Layout is deliberately simple (no HTML/CSS, no custom fonts yet); apps
  needing more can render HTML in a Python node and return it as data, or
  wait for custom fonts in `templates/fonts/`.
- Document images are limited to 4 MiB each.
