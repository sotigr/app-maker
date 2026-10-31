package app

import "testing"

func TestTrackerManifestCompiles(t *testing.T) {
	c, err := Load("../../examples/tracker", "dev")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Flows.All()) != 6 {
		t.Fatalf("flows: %d", len(c.Flows.All()))
	}
	for _, w := range c.Warnings {
		t.Log(w.String())
	}
}
