package kernel

import "testing"

func TestFoldText(t *testing.T) {
	for in, want := range map[string]string{
		"Παπαδόπουλος": "παπαδοπουλοσ",
		"ΠΑΠΑΔΟΠΟΥΛΟΣ": "παπαδοπουλοσ",
		"Ϊ ΰ":          "ι υ",
		"Émile Zoë":    "emile zoe",
		"ABC-123":      "abc-123",
	} {
		if got := FoldText(in); got != want {
			t.Errorf("FoldText(%q) = %q, want %q", in, got, want)
		}
	}
}
