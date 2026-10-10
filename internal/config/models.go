package config

// ModelFamily describes which upstream wire format a model uses.
type ModelFamily string

const (
	FamilyChatCompletions ModelFamily = "chat"      // /api/llm/o/v1/chat/completions  (fireworks, baseten, mistral)
	FamilyMessages        ModelFamily = "messages"  // /api/llm/a/v1/messages           (anthropic)
	FamilyResponses       ModelFamily = "responses" // /api/llm/o/v1/responses         (openai)
	FamilyGoogle          ModelFamily = "google"    // /api/llm/g/v1/generate           (gemini, passthrough only)
)

// ModelTier distinguishes the two quota pools.
type ModelTier string

const (
	TierStandard ModelTier = "standard"
	TierCore     ModelTier = "core"
)

// Model describes one selectable model.
type Model struct {
	ID                  string      `json:"id"`
	DisplayName         string      `json:"display_name"`
	Family              ModelFamily `json:"family"`
	Provider            string      `json:"provider"`  // default provider (mirrors Providers[0])
	Providers           []string    `json:"providers"` // ordered provider preference; first is the default
	Tier                ModelTier   `json:"tier"`
	Vision              bool        `json:"vision"`
	Multiplier          float64     `json:"multiplier"`  // token multiplier (credits per weighted token)
	Passthrough         bool        `json:"passthrough"` // excluded from model interchange; native-format transparent proxy only
	DefaultReasonEffort string      `json:"default_reasoning_effort,omitempty"`
	ReasoningEfforts    []string    `json:"reasoning_efforts,omitempty"`

	// Cost parameters for Factory credit estimation (see cost.go). Zero values
	// fall back to per-provider defaults.
	OutputMultiplier    float64 `json:"output_token_multiplier,omitempty"`
	CacheReadMultiplier float64 `json:"cache_read_token_multiplier,omitempty"`

	// Optional promotional pricing window (RFC3339). While active, the token
	// multiplier is reduced by PromotionDiscount.
	PromotionDiscount float64 `json:"promotion_discount,omitempty"`
	PromotionStarts   string  `json:"promotion_starts,omitempty"`
	PromotionEnds     string  `json:"promotion_ends,omitempty"`
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

// Catalog is the built-in model catalog. Droid Core (open-weight) models bill
// against the separate core quota pool; everything else is standard.
// Cost parameters mirror the Droid CLI model registry.
var Catalog = []Model{
	// ---- Anthropic (standard) ----
	{ID: "claude-opus-5-5", DisplayName: "Claude Opus 5.5", Family: FamilyMessages, Provider: "anthropic", Tier: TierStandard, Vision: true, Multiplier: 1.6, OutputMultiplier: 5, CacheReadMultiplier: 0.05, DefaultReasonEffort: "medium", ReasoningEfforts: []string{"low", "medium", "high", "xhigh", "max"}},
	{ID: "claude-opus-5", DisplayName: "Claude Opus 5", Family: FamilyMessages, Provider: "anthropic", Tier: TierStandard, Vision: true, Multiplier: 2, DefaultReasonEffort: "high", ReasoningEfforts: []string{"off", "low", "medium", "high", "xhigh", "max"}},
	{ID: "claude-sonnet-5-5", DisplayName: "Claude Sonnet 5.5", Family: FamilyMessages, Provider: "anthropic", Tier: TierStandard, Vision: true, Multiplier: 0.8, OutputMultiplier: 5, CacheReadMultiplier: 0.05, DefaultReasonEffort: "high", ReasoningEfforts: []string{"off", "low", "medium", "high", "xhigh", "max"}},
	{ID: "claude-sonnet-4-6", DisplayName: "Claude Sonnet 4.6", Family: FamilyMessages, Provider: "anthropic", Tier: TierStandard, Vision: true, Multiplier: 1.2, DefaultReasonEffort: "high", ReasoningEfforts: []string{"off", "low", "medium", "high", "max"}},
	{ID: "claude-haiku-4-5-20251001", DisplayName: "Claude Haiku 4.5", Family: FamilyMessages, Provider: "anthropic", Tier: TierStandard, Vision: true, Multiplier: 0.4, DefaultReasonEffort: "off", ReasoningEfforts: []string{"off", "low", "medium", "high"}},

	// ---- OpenAI (standard) ----
	{ID: "gpt-6.1-sol", DisplayName: "GPT-6.1 Sol", Family: FamilyResponses, Provider: "openai", Tier: TierStandard, Vision: true, Multiplier: 0.8, OutputMultiplier: 5, CacheReadMultiplier: 0.05, DefaultReasonEffort: "medium", ReasoningEfforts: []string{"low", "medium", "high", "xhigh", "max"}},
	{ID: "gpt-6-sol", DisplayName: "GPT-6 Sol", Family: FamilyResponses, Provider: "openai", Tier: TierStandard, Vision: true, Multiplier: 0.8, OutputMultiplier: 5, DefaultReasonEffort: "medium", ReasoningEfforts: []string{"none", "low", "medium", "high", "xhigh", "max"}},
	{ID: "gpt-6-luna", DisplayName: "GPT-6 Luna", Family: FamilyResponses, Provider: "openai", Tier: TierStandard, Vision: true, Multiplier: 0.04, OutputMultiplier: 5, DefaultReasonEffort: "medium", ReasoningEfforts: []string{"none", "low", "medium", "high", "xhigh", "max"}},
	{ID: "gpt-5.4", DisplayName: "GPT-5.4", Family: FamilyResponses, Provider: "openai", Tier: TierStandard, Vision: true, Multiplier: 1, OutputMultiplier: 6, DefaultReasonEffort: "medium", ReasoningEfforts: []string{"low", "medium", "high", "xhigh"}},
	{ID: "gpt-5.4-mini", DisplayName: "GPT-5.4 Mini", Family: FamilyResponses, Provider: "openai", Tier: TierStandard, Vision: true, Multiplier: 0.3, OutputMultiplier: 6, DefaultReasonEffort: "high", ReasoningEfforts: []string{"low", "medium", "high", "xhigh"}},

	// ---- Google (standard, passthrough only: never swapped, never a swap target) ----
	{ID: "gemini-3.1-pro-preview", DisplayName: "Gemini 3.1 Pro", Family: FamilyGoogle, Provider: "google", Tier: TierStandard, Vision: true, Multiplier: 0.8, Passthrough: true, DefaultReasonEffort: "high", ReasoningEfforts: []string{"low", "medium", "high"}},
	{ID: "gemini-3-flash-preview", DisplayName: "Gemini 3 Flash", Family: FamilyGoogle, Provider: "google", Tier: TierStandard, Vision: true, Multiplier: 0.2, Passthrough: true, DefaultReasonEffort: "high", ReasoningEfforts: []string{"minimal", "low", "medium", "high"}},

	// ---- Droid Core (open-weight, core quota pool) ----
	// Multi-provider models list their backends in preference order, taken from
	// the CLI registry's apiProviders for the global region; the admin UI can
	// benchmark them and pin the fastest one.
	{ID: "glm-5.3", DisplayName: "GLM-5.3", Family: FamilyChatCompletions, Provider: "fireworks", Providers: []string{"fireworks", "baseten", "databricks"}, Tier: TierCore, Vision: true, Multiplier: 0.56, OutputMultiplier: 3.15, DefaultReasonEffort: "max", ReasoningEfforts: []string{"low", "high", "max"}},
	{ID: "glm-5.3-flash", DisplayName: "GLM-5.3-Flash", Family: FamilyChatCompletions, Provider: "fireworks", Providers: []string{"fireworks", "baseten", "databricks"}, Tier: TierCore, Vision: true, Multiplier: 0.06, OutputMultiplier: 3.34, CacheReadMultiplier: 0.2, DefaultReasonEffort: "high", ReasoningEfforts: []string{"low", "high", "max"}},
	{ID: "kimi-k3", DisplayName: "Kimi K3", Family: FamilyChatCompletions, Provider: "fireworks", Providers: []string{"fireworks", "baseten", "databricks"}, Tier: TierCore, Vision: false, Multiplier: 1.2, OutputMultiplier: 5, DefaultReasonEffort: "high", ReasoningEfforts: []string{"off", "low", "high", "max"}},
	{ID: "deepseek-v4.1-flash", DisplayName: "DeepSeek V4.1 Flash", Family: FamilyChatCompletions, Provider: "fireworks", Providers: []string{"fireworks", "baseten", "databricks"}, Tier: TierCore, Vision: true, Multiplier: 0.12, OutputMultiplier: 4, CacheReadMultiplier: 0.024, DefaultReasonEffort: "high", ReasoningEfforts: []string{"off", "low", "high", "max"}},
	{ID: "qwen3.8-max", DisplayName: "Qwen3.8 Max", Family: FamilyChatCompletions, Provider: "fireworks", Tier: TierCore, Vision: true, Multiplier: 0.8, OutputMultiplier: 3, DefaultReasonEffort: "xhigh", ReasoningEfforts: []string{"low", "medium", "xhigh"}},
	{ID: "inkling", DisplayName: "Inkling", Family: FamilyChatCompletions, Provider: "fireworks", Tier: TierCore, Vision: true, Multiplier: 0.4, OutputMultiplier: 4.05, CacheReadMultiplier: 0.17, DefaultReasonEffort: "high", ReasoningEfforts: []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}},
	{ID: "nemotron-3-ultra", DisplayName: "Nemotron 3 Ultra", Family: FamilyChatCompletions, Provider: "baseten", Providers: []string{"baseten", "fireworks"}, Tier: TierCore, Vision: false, Multiplier: 0.24, OutputMultiplier: 4, DefaultReasonEffort: "high", ReasoningEfforts: []string{"off", "high"}},
	{ID: "minimax-m3", DisplayName: "MiniMax M3", Family: FamilyChatCompletions, Provider: "fireworks", Tier: TierCore, Vision: false, Multiplier: 0.12, OutputMultiplier: 4, DefaultReasonEffort: "high", ReasoningEfforts: []string{"high"}},
	// Launch pricing: 50% off through October 20, 2026, then back to 0.544x.
	{ID: "mistral-large-4", DisplayName: "Mistral Large 4", Family: FamilyChatCompletions, Provider: "mistral", Tier: TierCore, Vision: false, Multiplier: 0.544, OutputMultiplier: 3.08, CacheReadMultiplier: 0.103, PromotionDiscount: 0.5, PromotionStarts: "2026-10-06T00:00:00Z", PromotionEnds: "2026-10-20T00:00:00Z", DefaultReasonEffort: "high", ReasoningEfforts: []string{"off", "high"}},
}

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
