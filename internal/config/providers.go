package config

// Provider capability table.
//
// The same open-weight model can be served by several backends (fireworks,
// baseten, databricks, mistral). They differ in speed, availability, and in
// small request-shape quirks, so the proxy keeps a per-provider capability
// table and adjusts outgoing requests accordingly.

// ProviderCapabilities describes request quirks for one backend.
type ProviderCapabilities struct {
	// EffortAliases maps a reasoning-effort value to a provider-accepted
	// synonym. Databricks, for example, rejects "off" and expects "none".
	EffortAliases map[string]string
	// SupportsStreamUsage reports whether the provider emits usage in the
	// final streaming chunk (stream_options.include_usage). Defaults to true.
	SupportsStreamUsage bool
}

var providerCapabilities = map[string]ProviderCapabilities{
	"databricks": {
		EffortAliases:       map[string]string{"off": "none", "minimal": "minimal"},
		SupportsStreamUsage: true,
	},
	"fireworks": {SupportsStreamUsage: true},
	"baseten":   {SupportsStreamUsage: true},
	"mistral":   {SupportsStreamUsage: true},
	"anthropic": {SupportsStreamUsage: true},
	"openai":    {SupportsStreamUsage: true},
	"google":    {SupportsStreamUsage: true},
}

// MapEffort translates a canonical reasoning effort into the value a provider
// accepts, returning the input unchanged when no alias applies.
func MapEffort(provider, effort string) string {
	if effort == "" {
		return effort
	}
	if caps, ok := providerCapabilities[provider]; ok {
		if mapped, ok := caps.EffortAliases[effort]; ok {
			return mapped
		}
	}
	return effort
}

// KnownProvider reports whether the provider has a capability entry.
func KnownProvider(provider string) bool {
	_, ok := providerCapabilities[provider]
	return ok
}
