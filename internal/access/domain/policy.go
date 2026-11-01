// Package domain is the Access context's domain: roles, policies and their
// translation to query predicates, credentials, and the relationship index
// port (spec 8).
package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"

	accessapi "organon/internal/access/api"
	rt "organon/internal/runtime/api"
	schemaapi "organon/internal/schema/api"
)

// PolicyDoc is a policy file of the manifest (spec 8.5).
type PolicyDoc struct {
	Type    string            `json:"type"`
	Read    string            `json:"read,omitempty"`
	Create  string            `json:"create,omitempty"`
	Update  string            `json:"update,omitempty"`
	Delete  string            `json:"delete,omitempty"`
	Move    string            `json:"move,omitempty"`
	Actions map[string]string `json:"actions,omitempty"`
}

// Policy is a compiled policy for one type or trait.
type Policy struct {
	Target  schemaapi.TypeID // base type or trait ID
	Sources map[accessapi.Operation]string
	Progs   map[accessapi.Operation]rt.Program
	// Builtin marks a framework default for a system type.
	Builtin bool
}

// PolicySource is one policy document with its file name.
type PolicySource struct {
	File string
	Data []byte
}

// PolicyProblem is a policy compile error or warning.
type PolicyProblem struct {
	File    string `json:"file,omitempty"`
	Pointer string `json:"pointer"`
	Message string `json:"message"`
	Warning bool   `json:"warning,omitempty"`
}

func (p PolicyProblem) Error() string { return p.File + "#" + p.Pointer + ": " + p.Message }

// PolicySet holds the compiled policies of an app, keyed by target.
type PolicySet struct {
	byTarget map[schemaapi.TypeID]*Policy
}

// Defaults are the built-in policies of system types (decided 2026-10-31,
// see notes/decisions.md). Manifest policies for these types replace them.
var Defaults = []PolicyDoc{
	{Type: string(schemaapi.TypeRoot), Read: "principal.kind != 'anonymous'"},
	{Type: string(schemaapi.TypeUser), Read: "principal.kind != 'anonymous'", Update: "isOwner(feature)"},
	{Type: string(schemaapi.TypeTeam), Read: "principal.kind != 'anonymous'", Create: "principal.kind != 'anonymous'",
		Update: "isOwner(feature)", Delete: "isOwner(feature)", Move: "isOwner(feature)"},
	{Type: string(schemaapi.TypeOrganization), Read: "principal.kind != 'anonymous'", Create: "principal.kind != 'anonymous'",
		Update: "isOwner(feature)", Delete: "isOwner(feature)"},
	{Type: string(schemaapi.TypeServiceAccount), Read: "isOwner(feature)", Create: "principal.kind != 'anonymous'",
		Update: "isOwner(feature)", Delete: "isOwner(feature)"},
	// The built-in transfer action of Ownable types (spec 10.3).
	{Type: string(schemaapi.TraitOwnable), Actions: map[string]string{"transfer": "isOwner(feature)"}},
	{Type: string(schemaapi.TypeGrant), Read: "principal.id == feature.subject || hasRole(feature, 'admin')",
		Create: "hasRole(parent, 'admin')", Delete: "hasRole(feature, 'admin')"},
}

// CompilePolicies compiles manifest policies plus defaults for system types
// the manifest does not cover. Warnings report read policies that cannot
// be turned into a query predicate (spec 8.5).
func CompilePolicies(exprs rt.Compiler, types schemaapi.TypeRegistry, srcs []PolicySource) (*PolicySet, []PolicyProblem) {
	set := &PolicySet{byTarget: map[schemaapi.TypeID]*Policy{}}
	var probs []PolicyProblem
	for _, s := range srcs {
		var doc PolicyDoc
		dec := json.NewDecoder(bytes.NewReader(s.Data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&doc); err != nil {
			probs = append(probs, PolicyProblem{File: s.File, Message: "invalid policy: " + err.Error()})
			continue
		}
		p, ps := compilePolicy(exprs, types, doc, s.File)
		probs = append(probs, ps...)
		if p == nil {
			continue
		}
		if _, dup := set.byTarget[p.Target]; dup {
			probs = append(probs, PolicyProblem{File: s.File, Pointer: "/type", Message: fmt.Sprintf("duplicate policy for %s", p.Target)})
			continue
		}
		set.byTarget[p.Target] = p
	}
	for _, d := range Defaults {
		id := schemaapi.TypeID(d.Type)
		if _, overridden := set.byTarget[id]; overridden {
			continue
		}
		p, ps := compilePolicy(exprs, types, d, "builtin")
		if len(ps) > 0 && !ps[0].Warning {
			panic(fmt.Sprintf("builtin policy for %s: %v", d.Type, ps))
		}
		p.Builtin = true
		set.byTarget[id] = p
	}
	return set, probs
}

func compilePolicy(exprs rt.Compiler, types schemaapi.TypeRegistry, doc PolicyDoc, file string) (*Policy, []PolicyProblem) {
	var probs []PolicyProblem
	id, err := schemaapi.ParseTypeID(doc.Type)
	if err != nil {
		return nil, []PolicyProblem{{File: file, Pointer: "/type", Message: err.Error()}}
	}
	id = id.Base()
	featureType := rt.OpenMap()
	if !id.IsTrait() {
		ct, ok := types.Get(id)
		if !ok {
			return nil, []PolicyProblem{{File: file, Pointer: "/type", Message: fmt.Sprintf("unknown type %s", id)}}
		}
		featureType = ct.ExprType()
	} else if len(types.Resolve(id)) == 0 {
		probs = append(probs, PolicyProblem{File: file, Pointer: "/type", Message: fmt.Sprintf("trait %s is not used by any type", id), Warning: true})
	}
	p := &Policy{Target: id, Sources: map[accessapi.Operation]string{}, Progs: map[accessapi.Operation]rt.Program{}}
	add := func(op accessapi.Operation, ptr, src string) {
		if src == "" {
			return
		}
		prog, err := exprs.Compile(src, Env(featureType), rt.Bool, false)
		if err != nil {
			probs = append(probs, PolicyProblem{File: file, Pointer: ptr, Message: err.Error()})
			return
		}
		p.Sources[op] = src
		p.Progs[op] = prog
	}
	add(accessapi.OpRead, "/read", doc.Read)
	add(accessapi.OpCreate, "/create", doc.Create)
	add(accessapi.OpUpdate, "/update", doc.Update)
	add(accessapi.OpDelete, "/delete", doc.Delete)
	add(accessapi.OpMove, "/move", doc.Move)
	names := make([]string, 0, len(doc.Actions))
	for n := range doc.Actions {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		add(accessapi.ActionOp(n), "/actions/"+n, doc.Actions[n])
	}
	if src := doc.Read; src != "" {
		if _, ok := TranslateRead(src); !ok {
			probs = append(probs, PolicyProblem{File: file, Pointer: "/read", Warning: true,
				Message: "read policy cannot be turned into a query predicate; queries on this type are post-filtered with the row limit"})
		}
	}
	for _, pr := range probs {
		if !pr.Warning {
			return nil, probs
		}
	}
	return p, probs
}

// Env is the policy expression environment (spec 8.5).
func Env(feature *rt.Type) rt.Env {
	return rt.Env{
		Vars: map[string]*rt.Type{
			"principal": accessapi.PrincipalExprType(),
			"feature":   feature,
			"patch":     rt.OpenMap(),
			"parent":    rt.OpenMap(),
			"input":     rt.OpenMap(),
		},
		Funcs: []string{rt.FnNow, rt.FnIsOwner, rt.FnHasRole, rt.FnInGroup},
	}
}

// For returns the policies that apply to a type: its own and its traits'.
func (s *PolicySet) For(ct *schemaapi.CompiledType) []*Policy {
	var out []*Policy
	if p, ok := s.byTarget[ct.Base()]; ok {
		out = append(out, p)
	}
	for _, t := range ct.Traits {
		if p, ok := s.byTarget[t.Base()]; ok {
			out = append(out, p)
		}
	}
	return out
}

// Targets returns the IDs that have policies, sorted.
func (s *PolicySet) Targets() []schemaapi.TypeID {
	out := make([]schemaapi.TypeID, 0, len(s.byTarget))
	for id := range s.byTarget {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
