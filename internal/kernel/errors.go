package kernel

import (
	"errors"
	"fmt"
	"strings"
)

// Stable error codes (spec 6.6). Interfaces map them to HTTP statuses.
const (
	CodeValidationFailed      = "validation_failed"
	CodeInvalidQuery          = "invalid_query"
	CodeUnauthenticated       = "unauthenticated"
	CodeForbidden             = "forbidden"
	CodeNotFound              = "not_found"
	CodeVersionConflict       = "version_conflict"
	CodeInvalidTransition     = "invalid_transition"
	CodePreconditionFailed    = "precondition_failed"
	CodeUnsupportedCapability = "unsupported_capability"
	CodeVersionRequired       = "version_required"
	CodeRateLimited           = "rate_limited"
	CodeFlowTimeout           = "flow_timeout"
	CodeQueryTooExpensive     = "query_too_expensive"
	CodeCrossRepository       = "cross_repository"
	CodeFlowFailed            = "flow_failed"
	CodeConstraint            = "constraint_violation"
	CodeInvalidState          = "invalid_state"
	CodeConflict              = "conflict"
	CodeBadRequest            = "bad_request"
	CodeInternal              = "internal"
)

// Error is a domain error with a stable code.
type Error struct {
	Code    string
	Message string
	// Status overrides the HTTP status for declared flow errors (spec 6.6,
	// "Declared"); zero means "derive from Code".
	Status int
}

func (e *Error) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

// Is matches errors with the same code.
func (e *Error) Is(target error) bool {
	var t *Error
	if errors.As(target, &t) {
		return t.Code == e.Code
	}
	return false
}

// NewError returns an *Error with code and formatted message.
func NewError(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Sentinel errors for errors.Is checks.
var (
	ErrNotFound        = &Error{Code: CodeNotFound, Message: "not found"}
	ErrForbidden       = &Error{Code: CodeForbidden, Message: "forbidden"}
	ErrUnauthenticated = &Error{Code: CodeUnauthenticated, Message: "unauthenticated"}
	ErrCrossRepository = &Error{Code: CodeCrossRepository, Message: "batch touches more than one repository"}
)

// VersionConflictError reports features whose stored version differs from
// the expected one (spec 3.5, 5.2).
type VersionConflictError struct {
	Conflicts []VersionConflict
}

// VersionConflict is one conflicting feature.
type VersionConflict struct {
	ID      ID
	Current Version
}

func (e *VersionConflictError) Error() string {
	parts := make([]string, len(e.Conflicts))
	for i, c := range e.Conflicts {
		parts[i] = fmt.Sprintf("%s@%d", c.ID, c.Current)
	}
	return "version_conflict: " + strings.Join(parts, ", ")
}

// Is matches any *Error with CodeVersionConflict.
func (e *VersionConflictError) Is(target error) bool {
	var t *Error
	return errors.As(target, &t) && t.Code == CodeVersionConflict
}

// ErrVersionConflict is the sentinel for errors.Is.
var ErrVersionConflict = &Error{Code: CodeVersionConflict}

// UnsupportedError reports a capability the repository lacks.
type UnsupportedError struct {
	Capability string
}

func (e *UnsupportedError) Error() string {
	return "unsupported_capability: " + e.Capability
}

// Is matches *Error with CodeUnsupportedCapability.
func (e *UnsupportedError) Is(target error) bool {
	var t *Error
	return errors.As(target, &t) && t.Code == CodeUnsupportedCapability
}

// ErrUnsupported is the sentinel for errors.Is.
var ErrUnsupported = &Error{Code: CodeUnsupportedCapability}

// ConstraintError reports a storage-level constraint violation such as a
// duplicate unique name or a restrict reference.
type ConstraintError struct {
	Path string
	Code string
	ID   ID
}

func (e *ConstraintError) Error() string {
	return fmt.Sprintf("constraint %s at %s", e.Code, e.Path)
}

// Is matches *Error with CodeConstraint.
func (e *ConstraintError) Is(target error) bool {
	var t *Error
	return errors.As(target, &t) && t.Code == CodeConstraint
}

// ErrConstraint is the sentinel for errors.Is.
var ErrConstraint = &Error{Code: CodeConstraint}

// FieldError is one validation problem (spec 4.9).
type FieldError struct {
	Path    string `json:"path"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ValidationError lists validation problems.
type ValidationError struct {
	Errors []FieldError
	// Op is the index of the failing operation within a transaction, or -1.
	Op int
}

func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Errors))
	for i, fe := range e.Errors {
		parts[i] = fe.Path + ": " + fe.Message
	}
	return "validation_failed: " + strings.Join(parts, "; ")
}

// Is matches *Error with CodeValidationFailed.
func (e *ValidationError) Is(target error) bool {
	var t *Error
	return errors.As(target, &t) && t.Code == CodeValidationFailed
}

// ErrValidation is the sentinel for errors.Is.
var ErrValidation = &Error{Code: CodeValidationFailed}

// NewValidationError builds a ValidationError from field errors.
func NewValidationError(errs ...FieldError) *ValidationError {
	return &ValidationError{Errors: errs, Op: -1}
}

// CodeOf returns the stable code of err, or CodeInternal.
func CodeOf(err error) string {
	if err == nil {
		return ""
	}
	var ke *Error
	if errors.As(err, &ke) {
		return ke.Code
	}
	var vc *VersionConflictError
	if errors.As(err, &vc) {
		return CodeVersionConflict
	}
	var ve *ValidationError
	if errors.As(err, &ve) {
		return CodeValidationFailed
	}
	var ue *UnsupportedError
	if errors.As(err, &ue) {
		return CodeUnsupportedCapability
	}
	var ce *ConstraintError
	if errors.As(err, &ce) {
		return CodeConstraint
	}
	return CodeInternal
}
