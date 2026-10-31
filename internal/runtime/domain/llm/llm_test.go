package llm

import "testing"

// Spec 11.7: manifests name aliases; environment config maps them.
func TestResolveAliases(t *testing.T) {
	c, err := ParseConfig([]byte(`{"aliases":{"default":"fake:echo","local":"openai-compatible:llama"},"monthlyBudgetUsd":5}`))
	if err != nil {
		t.Fatal(err)
	}
	for alias, want := range map[string]Target{
		"":        {"fake", "echo"},
		"default": {"fake", "echo"},
		"local":   {"openai-compatible", "llama"},
		"fast":    {"anthropic", "claude-haiku-5-5"},
	} {
		got, err := c.Resolve(alias)
		if err != nil || got != want {
			t.Errorf("Resolve(%q) = %v, %v; want %v", alias, got, err, want)
		}
	}
	if _, err := c.Resolve("huge"); err == nil {
		t.Error("unknown alias resolved")
	}
	if c.MonthlyBudgetUSD != 5 {
		t.Errorf("budget %v", c.MonthlyBudgetUSD)
	}
	if _, err := ParseConfig([]byte(`{"monthlyBudgetUsd":-1}`)); err == nil {
		t.Error("negative budget accepted")
	}
}

func TestCost(t *testing.T) {
	if got := Cost("claude-opus-5-5", 1_000_000, 100_000); got != 6 {
		t.Errorf("cost %v, want 6", got)
	}
	if got := Cost("unknown", 1000, 1000); got != 0 {
		t.Errorf("unknown model cost %v", got)
	}
}
