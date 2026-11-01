package application

import (
	"testing"

	"organon/internal/kernel"
)

// Spec 14.1: object nesting depth 8, list items 1,000.
func TestCheckShape(t *testing.T) {
	deep := map[string]any{}
	cur := deep
	for i := 0; i < 8; i++ {
		next := map[string]any{}
		cur["a"] = next
		cur = next
	}
	if err := checkShape("", deep, 0); err != nil {
		t.Fatalf("depth 8 rejected: %v", err)
	}
	cur["a"] = map[string]any{}
	if err := checkShape("", deep, 0); kernel.CodeOf(err) != kernel.CodeValidationFailed {
		t.Fatalf("depth 9 accepted: %v", err)
	}
	if err := checkShape("", map[string]any{"l": make([]any, 1000)}, 0); err != nil {
		t.Fatalf("1000 items rejected: %v", err)
	}
	if err := checkShape("", map[string]any{"l": make([]any, 1001)}, 0); kernel.CodeOf(err) != kernel.CodeValidationFailed {
		t.Fatalf("1001 items accepted: %v", err)
	}
}
