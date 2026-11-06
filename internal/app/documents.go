package app

import (
	"context"
	"fmt"
	"net/http"
	"path"
	"strings"

	accessapi "organon/internal/access/api"
	flowdomain "organon/internal/flow/domain"
	integrationapp "organon/internal/integration/application"
	"organon/internal/kernel"
	manifestdomain "organon/internal/manifest/domain"
	"organon/internal/runtime/domain/expression"
	"organon/platform/pdf"
)

// Images in documents are at most this large.
const maxDocumentImage = 4 << 20

// compileDocuments compiles templates/documents/*.json (ADR 0022).
func compileDocuments(m *manifestdomain.Manifest) (*flowdomain.DocumentSet, []Problem) {
	var srcs []flowdomain.Source
	for _, f := range m.Templates {
		if isDocument(f.Path) {
			srcs = append(srcs, flowdomain.Source{File: f.Path, Data: f.Data})
		}
	}
	set, probs := flowdomain.CompileDocuments(expression.NewCompiler(), srcs)
	out := make([]Problem, len(probs))
	for i, p := range probs {
		out[i] = Problem{File: p.File, Pointer: p.Pointer, Message: p.Message}
	}
	return set, out
}

func isDocument(p string) bool {
	return strings.HasPrefix(p, "templates/documents/") && path.Ext(p) == ".json" && path.Dir(p) == "templates/documents"
}

// documentRefs checks that document.render nodes name existing templates
// when the name is a literal.
func documentRefs(flows *flowdomain.FlowSet, docs *flowdomain.DocumentSet) []Problem {
	var out []Problem
	if flows == nil {
		return nil
	}
	for _, f := range flows.All() {
		for name, n := range f.Def.Nodes {
			if n.Node != "document.render" {
				continue
			}
			src, _ := n.In["template"].(string)
			lit := strings.Trim(src, "'")
			if len(src) > 2 && src[0] == '\'' && src[len(src)-1] == '\'' && !strings.Contains(lit, "'") {
				if _, ok := docs.Get(lit); !ok {
					out = append(out, Problem{File: f.File, Pointer: "/nodes/" + name + "/in/template",
						Message: fmt.Sprintf("no document template templates/documents/%s.json (have: %s)", lit, strings.Join(docs.Names(), ", "))})
				}
			}
		}
	}
	return out
}

// renderDocument serves document.render: evaluate the template, lay it
// out as PDF, and store it as a blob for the field.
func renderDocument(c *Compiled, clock kernel.Clock, blobs func() *integrationapp.Blobs) func(ctx context.Context, p accessapi.Principal, name string, vars map[string]any, typeName, field string) (kernel.Blob, error) {
	files := map[string][]byte{}
	for _, f := range c.Manifest.Templates {
		files[f.Path] = f.Data
	}
	return func(ctx context.Context, p accessapi.Principal, name string, vars map[string]any, typeName, field string) (kernel.Blob, error) {
		t, ok := c.Documents.Get(name)
		if !ok {
			return kernel.Blob{}, kernel.NewError(kernel.CodeNotFound, "no document template %q", name)
		}
		b := blobs()
		image := func(v any) ([]byte, string, error) {
			var data []byte
			switch x := v.(type) {
			case string:
				d, ok := files[x]
				if !ok {
					return nil, "", fmt.Errorf("no file %s in the manifest", x)
				}
				data = d
			case map[string]any:
				id, _ := x["id"].(string)
				d, _, err := b.Bytes(ctx, id, maxDocumentImage)
				if err != nil {
					return nil, "", fmt.Errorf("image %s: %w", id, err)
				}
				data = d
			case kernel.Blob:
				d, _, err := b.Bytes(ctx, x.ID, maxDocumentImage)
				if err != nil {
					return nil, "", fmt.Errorf("image %s: %w", x.ID, err)
				}
				data = d
			default:
				return nil, "", fmt.Errorf("an image is a file value or a templates/ path")
			}
			switch http.DetectContentType(data) {
			case "image/png":
				return data, "png", nil
			case "image/jpeg":
				return data, "jpg", nil
			case "image/gif":
				return data, "gif", nil
			}
			return nil, "", fmt.Errorf("document images must be PNG, JPEG, or GIF")
		}
		doc, err := t.Evaluate(vars, clock.Now(), image)
		if err != nil {
			return kernel.Blob{}, kernel.NewError(kernel.CodeValidationFailed, "document %s: %v", name, err)
		}
		data, err := pdf.Render(doc)
		if err != nil {
			return kernel.Blob{}, fmt.Errorf("document %s: %w", name, err)
		}
		return b.StoreGenerated(ctx, p, typeName, field, "application/pdf", data)
	}
}
