// Package app is the shared composition root of the fwd server and the fw
// CLI (spec 15.5): it loads and compiles a manifest and wires the bounded
// contexts with constructor injection. It is the only package that imports
// every context's internals.
package app

import (
	llmdomain "app-maker/internal/runtime/domain/llm"
	"fmt"
	"strings"

	accessdomain "app-maker/internal/access/domain"
	flowapp "app-maker/internal/flow/application"
	flowdomain "app-maker/internal/flow/domain"
	manifestdomain "app-maker/internal/manifest/domain"
	manifestfs "app-maker/internal/manifest/infrastructure/fs"
	"app-maker/internal/runtime/domain/expression"
	schemadomain "app-maker/internal/schema/domain"
	"app-maker/internal/schema/infrastructure/jsonschema"
)

// Problem is one validation finding across all manifest files.
type Problem struct {
	File    string `json:"file"`
	Pointer string `json:"pointer,omitempty"`
	Message string `json:"message"`
	Warning bool   `json:"warning,omitempty"`
}

func (p Problem) String() string {
	loc := p.File
	if p.Pointer != "" {
		loc += "#" + p.Pointer
	}
	kind := "error"
	if p.Warning {
		kind = "warning"
	}
	return fmt.Sprintf("%s: %s: %s", kind, loc, p.Message)
}

// ValidationError lists every error found by Compile.
type ValidationError struct{ Problems []Problem }

func (e *ValidationError) Error() string {
	var b strings.Builder
	b.WriteString("manifest is invalid:")
	for _, p := range e.Problems {
		if !p.Warning {
			b.WriteString("\n  " + p.String())
		}
	}
	return b.String()
}

// Compiled is a validated, compiled manifest.
type Compiled struct {
	Manifest *manifestdomain.Manifest
	Types    *schemadomain.Registry
	Policies *accessdomain.PolicySet
	Roles    *accessdomain.Roles
	Flows    *flowdomain.FlowSet
	Nodes    *flowdomain.NodeRegistry
	LLM      llmdomain.Config
	Warnings []Problem
}

// NodeCatalog returns the registry of built-in node types.
func NodeCatalog() *flowdomain.NodeRegistry {
	nodes := flowdomain.NewNodeRegistry()
	for _, n := range flowapp.Builtins() {
		if err := nodes.Register(n); err != nil {
			panic(err) // programmer error at startup
		}
	}
	return nodes
}

// Load reads a manifest directory and compiles it (fw validate).
func Load(dir, env string) (*Compiled, error) {
	m, err := manifestfs.Load(dir, env)
	if err != nil {
		return nil, err
	}
	return Compile(m)
}

// Compile type-checks every schema, expression, and policy (spec 12.3
// validate). All problems are reported together.
func Compile(m *manifestdomain.Manifest) (*Compiled, error) {
	exprs := expression.NewCompiler()
	var probs []Problem
	toSources := func(fs []manifestdomain.File) []schemadomain.Source {
		out := make([]schemadomain.Source, len(fs))
		for i, f := range fs {
			out[i] = schemadomain.Source{File: f.Path, Data: f.Data}
		}
		return out
	}
	reg, sp := schemadomain.NewCompiler(exprs, jsonschema.Factory{}).Compile(schemadomain.Input{
		Traits: toSources(m.Traits), Types: toSources(m.Types)})
	for _, p := range sp {
		probs = append(probs, Problem{File: p.File, Pointer: p.Pointer, Message: p.Message})
	}
	if reg == nil {
		return nil, &ValidationError{Problems: probs}
	}
	roles, err := accessdomain.NewRoles(m.App.Roles)
	if err != nil {
		probs = append(probs, Problem{File: "app.json", Pointer: "/roles", Message: err.Error()})
	}
	psrc := make([]accessdomain.PolicySource, len(m.Policies))
	for i, f := range m.Policies {
		psrc[i] = accessdomain.PolicySource{File: f.Path, Data: f.Data}
	}
	policies, pp := accessdomain.CompilePolicies(exprs, reg, psrc)
	var warnings []Problem
	for _, p := range pp {
		pr := Problem{File: p.File, Pointer: p.Pointer, Message: p.Message, Warning: p.Warning}
		if p.Warning {
			warnings = append(warnings, pr)
		} else {
			probs = append(probs, pr)
		}
	}
	probs = append(probs, checkAuth(m.App.Auth, m.App.Roles)...)
	llmCfg, err := llmdomain.ParseConfig(m.App.LLM)
	if err != nil {
		probs = append(probs, Problem{File: "app.json", Pointer: "/llm", Message: err.Error()})
	}
	nodes := NodeCatalog()
	fsrc := make([]flowdomain.Source, len(m.Flows))
	for i, f := range m.Flows {
		fsrc[i] = flowdomain.Source{File: f.Path, Data: f.Data}
	}
	codeFiles := map[string]bool{}
	for _, f := range m.Nodes {
		codeFiles[f.Path] = true
	}
	flows, fp := flowdomain.Compile(flowdomain.CompileDeps{Types: reg, Exprs: exprs, Nodes: nodes, Validators: jsonschema.Factory{},
		CodeFiles: codeFiles}, fsrc)
	for _, p := range fp {
		probs = append(probs, Problem{File: p.File, Pointer: p.Pointer, Message: p.Message})
	}
	if len(probs) > 0 {
		return nil, &ValidationError{Problems: append(probs, warnings...)}
	}
	return &Compiled{Manifest: m, Types: reg, Policies: policies, Roles: roles, Flows: flows, Nodes: nodes, LLM: llmCfg, Warnings: warnings}, nil
}
