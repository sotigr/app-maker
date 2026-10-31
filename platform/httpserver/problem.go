// Package httpserver holds cross-cutting HTTP code: problem details (RFC
// 9457), JSON helpers, and middleware. It contains no domain logic.
package httpserver

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"app-maker/internal/kernel"
)

// Problem is an RFC 9457 problem details body with a stable code (spec 6.1).
type Problem struct {
	Type      string              `json:"type"`
	Title     string              `json:"title"`
	Status    int                 `json:"status"`
	Code      string              `json:"code"`
	Detail    string              `json:"detail,omitempty"`
	Errors    []kernel.FieldError `json:"errors,omitempty"`
	Operation *int                `json:"operation,omitempty"`
	Current   map[string]int64    `json:"current,omitempty"`
}

var statusByCode = map[string]int{
	kernel.CodeValidationFailed:      http.StatusBadRequest,
	kernel.CodeInvalidQuery:          http.StatusBadRequest,
	kernel.CodeBadRequest:            http.StatusBadRequest,
	kernel.CodeQueryTooExpensive:     http.StatusBadRequest,
	kernel.CodeCrossRepository:       http.StatusBadRequest,
	kernel.CodeUnauthenticated:       http.StatusUnauthorized,
	kernel.CodeForbidden:             http.StatusForbidden,
	kernel.CodeNotFound:              http.StatusNotFound,
	kernel.CodeVersionConflict:       http.StatusConflict,
	kernel.CodeInvalidTransition:     http.StatusConflict,
	kernel.CodeConstraint:            http.StatusConflict,
	kernel.CodeConflict:              http.StatusConflict,
	kernel.CodePreconditionFailed:    http.StatusPreconditionFailed,
	kernel.CodeUnsupportedCapability: http.StatusUnprocessableEntity,
	kernel.CodeVersionRequired:       http.StatusPreconditionRequired,
	kernel.CodeRateLimited:           http.StatusTooManyRequests,
	kernel.CodeFlowTimeout:           http.StatusGatewayTimeout,
	kernel.CodeFlowFailed:            http.StatusInternalServerError,
	kernel.CodeInternal:              http.StatusInternalServerError,
}

// StatusOf maps a stable code to its HTTP status (spec 6.6).
func StatusOf(code string) int {
	if s, ok := statusByCode[code]; ok {
		return s
	}
	return http.StatusBadRequest // declared flow errors default to 400
}

type operationIndexer interface{ OperationIndex() int }

// ProblemFor converts an error into problem details. Internal errors never
// expose their message.
func ProblemFor(err error) Problem {
	code := kernel.CodeOf(err)
	p := Problem{Type: "about:blank", Code: code, Status: StatusOf(code), Title: http.StatusText(StatusOf(code))}
	var ke *kernel.Error
	if errors.As(err, &ke) {
		p.Detail = ke.Message
		if ke.Status != 0 {
			p.Status, p.Title = ke.Status, http.StatusText(ke.Status)
		}
	}
	var ve *kernel.ValidationError
	if errors.As(err, &ve) {
		p.Errors = ve.Errors
		p.Detail = "the request does not match the schema"
	}
	var vc *kernel.VersionConflictError
	if errors.As(err, &vc) {
		p.Current = map[string]int64{}
		for _, c := range vc.Conflicts {
			p.Current[c.ID.String()] = int64(c.Current)
		}
		p.Detail = "the feature was changed by someone else"
	}
	var ce *kernel.ConstraintError
	if errors.As(err, &ce) {
		p.Detail = ce.Code
		p.Errors = []kernel.FieldError{{Path: ce.Path, Code: ce.Code, Message: "constraint violated"}}
	}
	var ue *kernel.UnsupportedError
	if errors.As(err, &ue) {
		p.Detail = "the repository cannot " + ue.Capability
	}
	var oi operationIndexer
	if errors.As(err, &oi) {
		i := oi.OperationIndex()
		p.Operation = &i
	}
	if code == kernel.CodeInternal {
		p.Detail = ""
	}
	return p
}

// WriteError writes err as problem details and logs internal errors.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	p := ProblemFor(err)
	if p.Status >= 500 {
		slog.ErrorContext(r.Context(), "request failed", "err", err, "path", r.URL.Path, "request_id", RequestID(r.Context()))
	}
	if p.Code == kernel.CodeRateLimited {
		w.Header().Set("Retry-After", "60")
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}

// WriteJSON writes v with status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// DecodeJSON decodes a request body with numbers as json.Number, so
// integer and decimal values keep full precision.
func DecodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return kernel.NewError(kernel.CodeBadRequest, "request body too large")
		}
		return kernel.NewError(kernel.CodeBadRequest, "invalid JSON body: %v", err)
	}
	if dec.More() {
		return kernel.NewError(kernel.CodeBadRequest, "trailing data after JSON body")
	}
	return nil
}

// ReadBody reads the (size-limited) body.
func ReadBody(r *http.Request) ([]byte, error) {
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r.Body); err != nil {
		return nil, kernel.NewError(kernel.CodeBadRequest, "request body too large or unreadable")
	}
	return buf.Bytes(), nil
}

// ETag formats a version as an entity tag (spec 6.1).
func ETag(v int64) string { return `"` + strconv.FormatInt(v, 10) + `"` }

// IfMatch parses the If-Match header; ok is false when it is missing.
func IfMatch(r *http.Request) (version int64, ok bool, err error) {
	h := r.Header.Get("If-Match")
	if h == "" {
		return 0, false, nil
	}
	if len(h) >= 2 && h[0] == '"' && h[len(h)-1] == '"' {
		h = h[1 : len(h)-1]
	}
	if len(h) > 2 && h[:2] == "W/" {
		return 0, true, kernel.NewError(kernel.CodeBadRequest, "weak ETags are not supported")
	}
	n, perr := strconv.ParseInt(h, 10, 64)
	if perr != nil {
		return 0, true, kernel.NewError(kernel.CodeBadRequest, "If-Match must be a version ETag")
	}
	return n, true, nil
}
