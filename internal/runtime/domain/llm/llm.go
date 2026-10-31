// Package llm holds the model-alias and cost rules of the llm.complete
// node (spec 11.7). Provider adapters live in runtime/infrastructure/llm.
package llm

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Config is the llm section of app.json (spec 12.2).
type Config struct {
	// Aliases map default, fast, large (or any name) to "<provider>:<model>";
	// a value without a provider prefix uses the anthropic provider.
	Aliases          map[string]string `json:"aliases,omitempty"`
	MonthlyBudgetUSD float64           `json:"monthlyBudgetUsd,omitempty"`
}

// DefaultAliases apply when app.json names no alias.
var DefaultAliases = map[string]string{
	"default": "anthropic:claude-opus-5-5",
	"fast":    "anthropic:claude-haiku-5-5",
	"large":   "anthropic:claude-opus-5-5",
}

// ParseConfig decodes the llm section; empty input yields the defaults.
func ParseConfig(raw json.RawMessage) (Config, error) {
	var c Config
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &c); err != nil {
			return Config{}, fmt.Errorf("llm: %w", err)
		}
	}
	if c.MonthlyBudgetUSD < 0 {
		return Config{}, fmt.Errorf("llm.monthlyBudgetUsd: must not be negative")
	}
	aliases := map[string]string{}
	for k, v := range DefaultAliases {
		aliases[k] = v
	}
	for k, v := range c.Aliases {
		if strings.TrimSpace(v) == "" {
			return Config{}, fmt.Errorf("llm.aliases.%s: empty model", k)
		}
		aliases[k] = v
	}
	c.Aliases = aliases
	return c, nil
}

// Target is a resolved alias.
type Target struct {
	Provider string
	Model    string
}

// Resolve maps an alias to a provider and model. Unknown aliases are an
// error so manifests never hard-code vendor model IDs by accident.
func (c Config) Resolve(alias string) (Target, error) {
	if alias == "" {
		alias = "default"
	}
	v, ok := c.Aliases[alias]
	if !ok {
		return Target{}, fmt.Errorf("unknown model alias %q", alias)
	}
	provider, model, found := strings.Cut(v, ":")
	if !found {
		return Target{Provider: "anthropic", Model: v}, nil
	}
	return Target{Provider: provider, Model: model}, nil
}

// price is USD per million input and output tokens.
type price struct{ in, out float64 }

// prices lists known models; others are estimated at zero cost.
var prices = map[string]price{
	"claude-fable-5-1":  {10, 50},
	"claude-opus-5-5":   {4, 20},
	"claude-sonnet-5-5": {2, 10},
	"claude-haiku-5-5":  {0.10, 0.50},
}

// Cost estimates the USD cost of a call.
func Cost(model string, inputTokens, outputTokens int64) float64 {
	p := prices[model]
	return (float64(inputTokens)*p.in + float64(outputTokens)*p.out) / 1e6
}
