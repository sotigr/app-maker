// Package jsonschema adapts github.com/santhosh-tekuri/jsonschema/v6 to the
// schema domain's ValidatorFactory port.
package jsonschema

import (
	"bytes"
	"errors"
	"sort"
	"strings"

	js "github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	"golang.org/x/text/language"
	"golang.org/x/text/message"

	"organon/internal/kernel"
	"organon/internal/schema/api"
)

var printer = message.NewPrinter(language.English)

// Factory compiles JSON Schemas with format assertion enabled (spec 4.1).
type Factory struct{}

// New compiles schema into a validator.
func (Factory) New(id string, schema []byte) (api.Validator, error) {
	doc, err := js.UnmarshalJSON(bytes.NewReader(schema))
	if err != nil {
		return nil, err
	}
	c := js.NewCompiler()
	c.DefaultDraft(js.Draft2020)
	c.AssertFormat()
	url := "mem://" + strings.NewReplacer(":", "_", "#", "/").Replace(id) + ".json"
	if err := c.AddResource(url, doc); err != nil {
		return nil, err
	}
	sch, err := c.Compile(url)
	if err != nil {
		return nil, err
	}
	return &validator{sch: sch}, nil
}

type validator struct{ sch *js.Schema }

// Validate validates a wire JSON document (numbers as json.Number or
// float64) and returns flattened field errors, sorted by path.
func (v *validator) Validate(doc any) []kernel.FieldError {
	err := v.sch.Validate(doc)
	if err == nil {
		return nil
	}
	var ve *js.ValidationError
	if !errors.As(err, &ve) {
		return []kernel.FieldError{{Path: "", Code: "invalid", Message: err.Error()}}
	}
	var out []kernel.FieldError
	collect(ve, &out)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func collect(e *js.ValidationError, out *[]kernel.FieldError) {
	if len(e.Causes) > 0 {
		for _, c := range e.Causes {
			collect(c, out)
		}
		return
	}
	path := pointer(e.InstanceLocation)
	switch k := e.ErrorKind.(type) {
	case *kind.Required:
		for _, m := range k.Missing {
			*out = append(*out, kernel.FieldError{Path: path + "/" + m, Code: "required", Message: "is required"})
		}
		return
	case *kind.AdditionalProperties:
		for _, p := range k.Properties {
			*out = append(*out, kernel.FieldError{Path: path + "/" + p, Code: "additionalProperties", Message: "unknown or not writable property"})
		}
		return
	}
	code := "invalid"
	if kp := e.ErrorKind.KeywordPath(); len(kp) > 0 {
		code = kp[len(kp)-1]
	}
	*out = append(*out, kernel.FieldError{Path: path, Code: code, Message: e.ErrorKind.LocalizedString(printer)})
}

func pointer(loc []string) string {
	var b strings.Builder
	for _, t := range loc {
		b.WriteString("/")
		b.WriteString(strings.ReplaceAll(strings.ReplaceAll(t, "~", "~0"), "/", "~1"))
	}
	return b.String()
}
