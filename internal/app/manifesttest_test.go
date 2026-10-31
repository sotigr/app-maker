package app

import (
	"context"
	"testing"
)

// Milestone 5: the tracker's manifest tests pass (spec 12.6).
func TestTrackerManifestTests(t *testing.T) {
	c, err := Load("../../examples/tracker", "dev")
	if err != nil {
		t.Fatal(err)
	}
	results, err := RunManifestTests(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("ran %d tests", len(results))
	}
	for _, r := range results {
		if !r.Passed {
			t.Errorf("%s: %v", r.Name, r.Failures)
		}
	}
}
