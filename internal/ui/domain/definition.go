package domain

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	accessapi "organon/internal/access/api"
	rt "organon/internal/runtime/api"
)

// Compiled is the validated ui/ directory of a manifest.
type Compiled struct {
	// App is nil when the manifest has no UI.
	App   *App
	Pages []*Page // sorted by name
	// Styles is ui/styles.css, or nil.
	Styles []byte
	// Assets holds ui/assets/** by path below ui/assets/.
	Assets map[string][]byte
	// Languages lists the UI languages, the language of the ui/ files
	// first; nil when the app declares none (ADR 0018).
	Languages []string
	// Catalogs are the ui/i18n files by language (empty when missing).
	Catalogs map[string]*Translations
	// AppTexts are the translatable texts of ui/app.json.
	AppTexts []string
}

// App is ui/app.json: the shell around every page.
type App struct {
	Title        string
	Home         string
	RequireLogin bool
	Nav          []NavItem
	// Doc is the document sent to browsers (without $schema, x-layout).
	Doc json.RawMessage
}

// NavItem is one entry of the app navigation.
type NavItem struct {
	Page string
	// Visible is nil when the entry is always shown.
	Visible rt.Program
}

// Page is one compiled ui/pages/<name>.json.
type Page struct {
	Name   string
	File   string
	Route  string
	Params map[string]Param
	// Doc is the document sent to browsers (without $schema, x-layout).
	Doc json.RawMessage
	// Visible decides from the principal alone whether the page is
	// offered; nil means always.
	Visible rt.Program
	// Public is true when anonymous callers may see the page.
	Public bool
	// Types are the feature types the page binds to (for field metadata).
	Types []string
	// Texts are the page's translatable texts.
	Texts []string
}

// Param is a declared page parameter. Route parameters (":id") are
// required; the others are optional query-string parameters.
type Param struct {
	Type   string `json:"type"`
	Format string `json:"format,omitempty"`
}

// Problem is a validation finding in a ui/ file.
type Problem struct {
	File    string
	Pointer string
	Message string
	Warning bool
}

// VisibleTo evaluates a principal-only visibility expression.
func VisibleTo(p rt.Program, principal accessapi.Principal, now time.Time) bool {
	if p == nil {
		return true
	}
	v, err := p.Eval(principalVars(principal, now))
	b, _ := v.(bool)
	return err == nil && b
}

func principalVars(p accessapi.Principal, now time.Time) map[string]any {
	return map[string]any{"principal": p.ExprValue(), rt.FnNow: func() time.Time { return now }}
}

var (
	nameRe     = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)
	identRe    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	segRe      = regexp.MustCompile(`^[A-Za-z0-9_.~-]+$`)
	reservedRt = map[string]bool{"v1": true, "admin": true, "assets": true}
)

// routeParams parses a route like "/projects/:id" and returns its
// parameter names and its shape ("/projects/:") for conflict checks.
func routeParams(route string) (params []string, shape string, err error) {
	if route == "/" {
		return nil, "/", nil
	}
	if !strings.HasPrefix(route, "/") || strings.HasSuffix(route, "/") {
		return nil, "", fmt.Errorf("route must start with / and not end with /")
	}
	segs := strings.Split(route[1:], "/")
	if reservedRt[segs[0]] {
		return nil, "", fmt.Errorf("routes under /%s are reserved", segs[0])
	}
	var b strings.Builder
	for _, s := range segs {
		b.WriteString("/")
		switch {
		case strings.HasPrefix(s, ":"):
			if !identRe.MatchString(s[1:]) {
				return nil, "", fmt.Errorf("bad route parameter %q", s)
			}
			params = append(params, s[1:])
			b.WriteString(":")
		case segRe.MatchString(s):
			b.WriteString(s)
		default:
			return nil, "", fmt.Errorf("bad route segment %q", s)
		}
	}
	return params, b.String(), nil
}

func paramType(p Param) (*rt.Type, error) {
	switch p.Type {
	case "string":
		return rt.String, nil
	case "integer":
		return rt.Int, nil
	case "number":
		return rt.Float, nil
	case "boolean":
		return rt.Bool, nil
	}
	return nil, fmt.Errorf("parameter type must be string, integer, number, or boolean")
}

// stateType returns the expression type of a page state variable.
func stateType(t string) (*rt.Type, error) {
	switch t {
	case "string":
		return rt.String, nil
	case "integer":
		return rt.Int, nil
	case "number":
		return rt.Float, nil
	case "boolean":
		return rt.Bool, nil
	case "array":
		return rt.ListOf(rt.Any), nil
	case "object":
		return rt.OpenMap(), nil
	}
	return nil, fmt.Errorf("state type must be string, integer, number, boolean, array, or object")
}
