package config

// ModelFamily describes which upstream wire format a model uses.
type ModelFamily string

const (
	FamilyChatCompletions ModelFamily = "chat"      // /api/llm/o/v1/chat/completions  (fireworks, baseten, mistral, databricks, xai)
	FamilyMessages        ModelFamily = "messages"  // /api/llm/a/v1/messages           (anthropic)
	FamilyResponses       ModelFamily = "responses" // /api/llm/o/v1/responses          (openai)
	FamilyGoogle          ModelFamily = "google"    // /api/llm/g/v1/generate           (gemini, passthrough only)
)

// ModelTier distinguishes the two quota pools.
type ModelTier string

const (
	TierStandard ModelTier = "standard"
	TierCore     ModelTier = "core"
)

// Model describes one selectable model. Every field is derived from
// manifest.json — see manifest.go for the translation. Editing this file to add
// a model is the wrong move; run the extractor instead.
type Model struct {
	ID          string      `json:"id"`
	DisplayName string      `json:"display_name"`
	Family      ModelFamily `json:"family"`
	Provider    string      `json:"provider"`  // default provider (mirrors Providers[0])
	Providers   []string    `json:"providers"` // ordered provider preference; first is the default
	Tier        ModelTier   `json:"tier"`
	Vision      bool        `json:"vision"`
	Multiplier  float64     `json:"multiplier"`  // token multiplier (credits per weighted token)
	Passthrough bool        `json:"passthrough"` // excluded from model interchange; native-format transparent proxy only

	DefaultReasonEffort string   `json:"default_reasoning_effort,omitempty"`
	ReasoningEfforts    []string `json:"reasoning_efforts,omitempty"`

	// Cost parameters for Factory credit estimation (see cost.go). Zero values
	// fall back to per-provider defaults.
	OutputMultiplier    float64 `json:"output_token_multiplier,omitempty"`
	CacheReadMultiplier float64 `json:"cache_read_token_multiplier,omitempty"`

	// Promotions are the registry's discount windows. Expired entries are kept
	// in the manifest, so the window is always evaluated at use time.
	Promotions []Promotion `json:"promotions,omitempty"`

	// Bookkeeping copied straight from the registry, exposed for the admin UI.
	ServiceTier  string `json:"service_tier,omitempty"` // premium | standard, the CLI's own tier
	DeprecatedOn string `json:"deprecated_on,omitempty"`
	Replacement  string `json:"replacement,omitempty"`
	CLIVisible   bool   `json:"cli_visible"`
}

// AvailableProviders returns the ordered provider list for the model, falling
// back to the single Provider field.
func (m Model) AvailableProviders() []string {
	if len(m.Providers) > 0 {
		return m.Providers
	}
	if m.Provider != "" {
		return []string{m.Provider}
	}
	return nil
}

// DefaultProvider returns the provider used when no override is configured.
func (m Model) DefaultProvider() string {
	if len(m.Providers) > 0 {
		return m.Providers[0]
	}
	return m.Provider
}

// Catalog is the model catalog, built from the embedded manifest at startup.
var Catalog = loadManifest()

var catalogIndex = func() map[string]Model {
	m := make(map[string]Model, len(Catalog))
	for _, mm := range Catalog {
		m[mm.ID] = mm
	}
	return m
}()

// LookupModel returns the catalog entry for id.
func LookupModel(id string) (Model, bool) {
	m, ok := catalogIndex[id]
	return m, ok
}

// CoreModels returns all models that bill against the core quota pool.
func CoreModels() []Model {
	var out []Model
	for _, m := range Catalog {
		if m.Tier == TierCore {
			out = append(out, m)
		}
	}
	return out
}
