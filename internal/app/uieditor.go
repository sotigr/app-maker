package app

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	accessapi "organon/internal/access/api"
	"organon/internal/kernel"
	uidomain "organon/internal/ui/domain"
	"organon/platform/httpserver"
)

// uiEditor serves the visual UI builder's server side (ADR 0016): the raw
// ui/ files, validation of unsaved edits with the same compiler as organon
// validate, and, in development only, writing ui/ files back into the
// manifest directory. organon dev's hot reload then activates them.
type uiEditor struct {
	rt *Runtime
}

func (h *uiEditor) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/ui/source", h.source)
	mux.HandleFunc("POST /v1/ui/validate", h.validate)
	if h.rt.Cfg.Dev {
		mux.HandleFunc("PUT /v1/dev/manifest/ui/pages/{name}", h.savePage)
		mux.HandleFunc("DELETE /v1/dev/manifest/ui/pages/{name}", h.deletePage)
		mux.HandleFunc("PUT /v1/dev/manifest/ui/app", h.saveApp)
		mux.HandleFunc("PUT /v1/dev/manifest/ui/styles", h.saveStyles)
		mux.HandleFunc("PUT /v1/dev/manifest/ui/assets/{path...}", h.saveAsset)
		mux.HandleFunc("DELETE /v1/dev/manifest/ui/assets/{path...}", h.deleteAsset)
	}
}

// uiFiles returns the current ui/ files by path.
func (h *uiEditor) uiFiles() map[string][]byte {
	files := map[string][]byte{}
	for _, f := range h.rt.App.Manifest.UI {
		files[f.Path] = f.Data
	}
	return files
}

// check compiles the ui/ files after edit and returns every problem.
func (h *uiEditor) check(edit func(files map[string][]byte)) []Problem {
	files := h.uiFiles()
	edit(files)
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	srcs := make([]uidomain.Source, len(paths))
	for i, p := range paths {
		srcs[i] = uidomain.Source{File: p, Data: files[p]}
	}
	c := h.rt.App
	_, probs := uidomain.Compile(uiDeps(c.Types, c.Flows), srcs)
	out := []Problem{}
	for _, p := range probs {
		out = append(out, Problem{File: p.File, Pointer: p.Pointer, Message: p.Message, Warning: p.Warning})
	}
	return out
}

func hasErrors(probs []Problem) bool {
	for _, p := range probs {
		if !p.Warning {
			return true
		}
	}
	return false
}

type sourcePage struct {
	Name string          `json:"name"`
	File string          `json:"file"`
	Doc  json.RawMessage `json:"doc"`
}

// source returns the ui/ files as written (with x-layout), every type's
// field metadata, and whether this server can save (organon dev).
func (h *uiEditor) source(w http.ResponseWriter, r *http.Request) {
	if !adminOnly(w, r) {
		return
	}
	out := map[string]any{"dev": h.rt.Cfg.Dev, "app": nil, "styles": nil, "pages": []sourcePage{}, "assets": []string{},
		"types": h.rt.UI.AllTypes(accessapi.PrincipalFrom(r.Context()))}
	pages := []sourcePage{}
	assets := []string{}
	for _, f := range h.rt.App.Manifest.UI {
		rel := f.Path
		if strings.HasPrefix(rel, "modules/") {
			rel = strings.Join(strings.Split(rel, "/")[2:], "/")
		}
		switch {
		case rel == "ui/app.json":
			out["app"] = json.RawMessage(f.Data)
		case rel == "ui/styles.css":
			out["styles"] = string(f.Data)
		case strings.HasPrefix(rel, "ui/pages/"):
			var head struct {
				Page string `json:"page"`
			}
			if json.Unmarshal(f.Data, &head) == nil {
				pages = append(pages, sourcePage{Name: head.Page, File: f.Path, Doc: f.Data})
			}
		case strings.HasPrefix(rel, "ui/assets/"):
			assets = append(assets, strings.TrimPrefix(rel, "ui/assets/"))
		}
	}
	sort.Slice(pages, func(i, j int) bool { return pages[i].Name < pages[j].Name })
	out["pages"], out["assets"] = pages, assets
	httpserver.WriteJSON(w, http.StatusOK, out)
}

// validate checks unsaved edits: files maps ui/ paths to new contents,
// null removing a file.
func (h *uiEditor) validate(w http.ResponseWriter, r *http.Request) {
	if !adminOnly(w, r) {
		return
	}
	var b struct {
		Files map[string]*string `json:"files"`
	}
	if err := httpserver.DecodeJSON(r, &b); err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	probs := h.check(func(files map[string][]byte) {
		for p, c := range b.Files {
			if c == nil {
				delete(files, p)
			} else {
				files[p] = []byte(*c)
			}
		}
	})
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"valid": !hasErrors(probs), "problems": probs})
}

// pageFile is the file of page name: its current file, or ui/pages/<name>.json.
func (h *uiEditor) pageFile(name string) string {
	if ui := h.rt.App.UI; ui != nil {
		for _, p := range ui.Pages {
			if p.Name == name {
				return p.File
			}
		}
	}
	return "ui/pages/" + name + ".json"
}

func pretty(doc json.RawMessage) ([]byte, error) {
	var v any
	if err := json.Unmarshal(doc, &v); err != nil {
		return nil, kernel.NewError(kernel.CodeBadRequest, "document is not JSON: %v", err)
	}
	out, _ := json.MarshalIndent(v, "", "  ")
	return append(out, '\n'), nil
}

// apply validates the change and, without errors, writes and removes
// files; writes maps manifest-relative paths to contents.
func (h *uiEditor) apply(w http.ResponseWriter, r *http.Request, writes map[string][]byte, removes []string) {
	probs := h.check(func(files map[string][]byte) {
		for _, p := range removes {
			delete(files, p)
		}
		for p, d := range writes {
			files[p] = d
		}
	})
	if hasErrors(probs) {
		httpserver.WriteJSON(w, http.StatusUnprocessableEntity, map[string]any{"valid": false, "problems": probs})
		return
	}
	dir := h.rt.App.Manifest.Dir
	inside := func(rel string) (string, bool) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		rp, err := filepath.Rel(dir, p)
		return p, err == nil && filepath.ToSlash(rp) == rel && strings.Contains(rel, "ui/")
	}
	for rel, data := range writes {
		p, ok := inside(rel)
		if !ok {
			httpserver.WriteError(w, r, kernel.NewError(kernel.CodeBadRequest, "invalid path %q", rel))
			return
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			httpserver.WriteError(w, r, err)
			return
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			httpserver.WriteError(w, r, err)
			return
		}
	}
	for _, rel := range removes {
		if _, keep := writes[rel]; keep {
			continue
		}
		if p, ok := inside(rel); ok {
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				httpserver.WriteError(w, r, err)
				return
			}
		}
	}
	saved := make([]string, 0, len(writes))
	for rel := range writes {
		saved = append(saved, rel)
	}
	sort.Strings(saved)
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"saved": saved, "removed": removes, "problems": probs})
}

func (h *uiEditor) savePage(w http.ResponseWriter, r *http.Request) {
	if !adminOnly(w, r) {
		return
	}
	name := r.PathValue("name")
	var b struct {
		Document json.RawMessage `json:"document"`
		// From renames: the page's previous name, whose file is replaced.
		From string `json:"from,omitempty"`
	}
	if err := httpserver.DecodeJSON(r, &b); err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	var head struct {
		Page string `json:"page"`
	}
	if err := json.Unmarshal(b.Document, &head); err != nil || head.Page != name || !flowName.MatchString(name) {
		httpserver.WriteError(w, r, kernel.NewError(kernel.CodeValidationFailed, "the document's page name must be %q", name))
		return
	}
	data, err := pretty(b.Document)
	if err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	var removes []string
	file := h.pageFile(name)
	if b.From != "" && b.From != name {
		old := h.pageFile(b.From)
		removes = append(removes, old)
		if !h.exists(file) {
			file = path.Join(path.Dir(old), name+".json")
		}
	}
	h.apply(w, r, map[string][]byte{file: data}, removes)
}

func (h *uiEditor) exists(rel string) bool {
	_, ok := h.uiFiles()[rel]
	return ok
}

func (h *uiEditor) deletePage(w http.ResponseWriter, r *http.Request) {
	if !adminOnly(w, r) {
		return
	}
	file := h.pageFile(r.PathValue("name"))
	if !h.exists(file) {
		httpserver.WriteError(w, r, kernel.ErrNotFound)
		return
	}
	h.apply(w, r, nil, []string{file})
}

func (h *uiEditor) saveApp(w http.ResponseWriter, r *http.Request) {
	if !adminOnly(w, r) {
		return
	}
	var b struct {
		Document json.RawMessage `json:"document"`
	}
	if err := httpserver.DecodeJSON(r, &b); err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	data, err := pretty(b.Document)
	if err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	h.apply(w, r, map[string][]byte{"ui/app.json": data}, nil)
}

// saveStyles writes ui/styles.css; empty CSS removes the file.
func (h *uiEditor) saveStyles(w http.ResponseWriter, r *http.Request) {
	if !adminOnly(w, r) {
		return
	}
	var b struct {
		CSS string `json:"css"`
	}
	if err := httpserver.DecodeJSON(r, &b); err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	if strings.TrimSpace(b.CSS) == "" {
		h.apply(w, r, nil, []string{"ui/styles.css"})
		return
	}
	h.apply(w, r, map[string][]byte{"ui/styles.css": []byte(b.CSS)}, nil)
}

func assetPath(p string) (string, bool) {
	clean := path.Clean("/" + p)[1:]
	if clean != p || p == "" || strings.HasPrefix(p, ".") || strings.Contains(p, "/.") {
		return "", false
	}
	return "ui/assets/" + p, true
}

// saveAsset writes the request body to ui/assets/<path>.
func (h *uiEditor) saveAsset(w http.ResponseWriter, r *http.Request) {
	if !adminOnly(w, r) {
		return
	}
	rel, ok := assetPath(r.PathValue("path"))
	if !ok {
		httpserver.WriteError(w, r, kernel.NewError(kernel.CodeBadRequest, "invalid asset path"))
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, uidomain.MaxAssetBytes+1))
	if err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	h.apply(w, r, map[string][]byte{rel: data}, nil)
}

func (h *uiEditor) deleteAsset(w http.ResponseWriter, r *http.Request) {
	if !adminOnly(w, r) {
		return
	}
	rel, ok := assetPath(r.PathValue("path"))
	if !ok || !h.exists(rel) {
		httpserver.WriteError(w, r, kernel.ErrNotFound)
		return
	}
	h.apply(w, r, nil, []string{rel})
}
