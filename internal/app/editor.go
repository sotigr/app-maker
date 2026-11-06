package app

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"

	flowdomain "organon/internal/flow/domain"
	"organon/internal/kernel"
	"organon/internal/runtime/domain/expression"
	"organon/internal/schema/infrastructure/jsonschema"
	"organon/platform/httpserver"
)

// editorHandler serves the visual flow editor's server side (ADR 0002):
// validation with the same compiler as organon validate, and, in development
// only, saving flow files back into the manifest directory.
type editorHandler struct {
	rt *Runtime
}

func (h *editorHandler) register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/flows/validate", h.validate)
	if h.rt.Cfg.Dev {
		mux.HandleFunc("PUT /v1/dev/manifest/flows/{name}", h.save)
	}
}

type flowBody struct {
	Definition json.RawMessage `json:"definition"`
}

// compileOne validates a flow definition against the running app.
func (h *editorHandler) compileOne(name string, def json.RawMessage) []flowdomain.Problem {
	c := h.rt.App
	srcs := []flowdomain.Source{{File: "flows/" + name + ".json", Data: def}}
	// Other flows are included so subflow names and duplicates resolve.
	for _, f := range c.Flows.All() {
		if f.Name == name {
			continue
		}
		b, _ := json.Marshal(f.Def)
		srcs = append(srcs, flowdomain.Source{File: f.File, Data: b})
	}
	codeFiles := map[string]bool{}
	for _, f := range c.Manifest.Nodes {
		codeFiles[f.Path] = true
	}
	_, probs := flowdomain.Compile(flowdomain.CompileDeps{Types: c.Types, Exprs: expression.NewCompiler(), Nodes: c.Nodes,
		Validators: jsonschema.Factory{}, CodeFiles: codeFiles, SecretHosts: c.Manifest.App.SecretHosts()}, srcs)
	var mine []flowdomain.Problem
	for _, p := range probs {
		if p.File == srcs[0].File || p.Flow == name {
			mine = append(mine, p)
		}
	}
	return mine
}

func (h *editorHandler) validate(w http.ResponseWriter, r *http.Request) {
	if !adminOnly(w, r) {
		return
	}
	var b flowBody
	if err := httpserver.DecodeJSON(r, &b); err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	def, err := flowdomain.ParseDefinition(b.Definition)
	if err != nil {
		httpserver.WriteJSON(w, http.StatusOK, map[string]any{"valid": false, "problems": []flowdomain.Problem{{Message: err.Error()}}})
		return
	}
	probs := h.compileOne(def.Flow, b.Definition)
	if probs == nil {
		probs = []flowdomain.Problem{}
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"valid": len(probs) == 0, "problems": probs})
}

var flowName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)

// save writes flows/<file>.json in the manifest directory (dev only, ADR
// 0002); organon dev's hot reload then activates it.
func (h *editorHandler) save(w http.ResponseWriter, r *http.Request) {
	if !adminOnly(w, r) {
		return
	}
	name := r.PathValue("name")
	if !flowName.MatchString(name) {
		httpserver.WriteError(w, r, kernel.NewError(kernel.CodeBadRequest, "invalid flow name"))
		return
	}
	var b flowBody
	if err := httpserver.DecodeJSON(r, &b); err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	def, err := flowdomain.ParseDefinition(b.Definition)
	if err != nil || def.Flow != name {
		httpserver.WriteError(w, r, kernel.NewError(kernel.CodeValidationFailed, "the definition's flow name must be %q", name))
		return
	}
	if probs := h.compileOne(name, b.Definition); len(probs) > 0 {
		httpserver.WriteJSON(w, http.StatusUnprocessableEntity, map[string]any{"valid": false, "problems": probs})
		return
	}
	rel := filepath.Join("flows", name+".json")
	if f, ok := h.rt.App.Flows.Get(name); ok && f.File != "" {
		rel = filepath.FromSlash(f.File)
	}
	dir := h.rt.App.Manifest.Dir
	path := filepath.Join(dir, rel)
	if rp, err := filepath.Rel(dir, path); err != nil || rp != rel {
		httpserver.WriteError(w, r, kernel.NewError(kernel.CodeBadRequest, "invalid flow path"))
		return
	}
	var pretty any
	_ = json.Unmarshal(b.Definition, &pretty)
	out, _ := json.MarshalIndent(pretty, "", "  ")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"saved": filepath.ToSlash(rel)})
}
