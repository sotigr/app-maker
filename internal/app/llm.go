package app

import (
	"context"
	"fmt"

	accessapi "app-maker/internal/access/api"
	"app-maker/internal/kernel"
	rtapi "app-maker/internal/runtime/api"
	runtimeapp "app-maker/internal/runtime/application"
	llminfra "app-maker/internal/runtime/infrastructure/llm"
	"app-maker/platform/config"
)

// llmRouter builds the providers named in the server configuration and the
// router serving app.json's aliases (spec 11.7). Provider keys are secret
// references, resolved here and never exposed to flows or code nodes.
func llmRouter(cfg config.Config, c *Compiled, secrets func(string) (string, bool), opt Options) (*runtimeapp.Router, error) {
	providers := map[string]rtapi.LLMProvider{"fake": llminfra.Fake{}, "anthropic": llminfra.NewAnthropic("", "")}
	for name, p := range cfg.LLM.Providers {
		key := ""
		if p.APIKey != "" {
			v, ok := secrets(p.APIKey)
			if !ok {
				return nil, fmt.Errorf("llm.providers.%s.apiKey: secret %s is not set", name, p.APIKey)
			}
			key = v
		}
		switch p.Kind {
		case "anthropic":
			providers[name] = llminfra.NewAnthropic(key, p.BaseURL)
		case "openai-compatible":
			providers[name] = llminfra.NewOpenAICompatible(p.BaseURL, key)
		case "fake":
			providers[name] = llminfra.Fake{}
		}
	}
	if f := cfg.LLM.Provider; f != "" {
		if _, ok := providers[f]; !ok {
			return nil, fmt.Errorf("llm.provider: unknown provider %q", f)
		}
	}
	return runtimeapp.NewRouter(c.LLM, providers, cfg.LLM.Provider, opt.Clock, opt.Log), nil
}

// ephemeral publishes ephemeral field values for flows (llm.complete
// streamTo) through the gateway, as the run's principal.
func (rt *Runtime) ephemeral(ctx context.Context, p accessapi.Principal, id kernel.ID, values map[string]any) error {
	if rt.Gateway == nil {
		return kernel.NewError(kernel.CodeUnsupportedCapability, "streaming is not enabled")
	}
	v, err := rt.Content.StreamReader().Get(ctx, p, id)
	if err != nil {
		return err
	}
	return rt.Gateway.Ephemeral(ctx, p, v.Type, id, values)
}
