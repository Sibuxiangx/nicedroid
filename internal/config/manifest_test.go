package config

import "testing"

// TestManifestDecodes guards the embedded catalog. A truncated or malformed
// manifest.json would otherwise surface as a panic at startup rather than as a
// failed build.
func TestManifestDecodes(t *testing.T) {
	if len(Catalog) == 0 {
		t.Fatal("catalog is empty")
	}
	if ManifestVersion() == "" {
		t.Fatal("manifest has no cli version")
	}
	if ManifestUserAgent() != "factory-cli/"+ManifestVersion() {
		t.Errorf("user agent %q does not match version %q", ManifestUserAgent(), ManifestVersion())
	}
	for _, m := range Catalog {
		if m.ID == "" || m.DisplayName == "" {
			t.Errorf("model %q has no id or display name", m.ID)
		}
		if m.Family == "" {
			t.Errorf("%s: no family", m.ID)
		}
		if m.Multiplier <= 0 {
			t.Errorf("%s: non-positive multiplier", m.ID)
		}
		if len(m.ReasoningEfforts) == 0 {
			t.Errorf("%s: no reasoning efforts", m.ID)
		}
		if m.DefaultReasonEffort == "" {
			t.Errorf("%s: no default reasoning effort", m.ID)
		}
		for _, p := range m.AvailableProviders() {
			if p == "" {
				t.Errorf("%s: empty provider in list", m.ID)
			}
		}
		if len(m.AvailableProviders()) == 0 {
			t.Errorf("%s: no providers", m.ID)
		}
	}
}

// TestPlatformVersusBackend pins the two-level provider model: Provider names
// the platform that serves the model, Providers lists the backends the gateway
// can dispatch to with x-api-provider. Conflating them is what put open-weight
// models under a "fireworks" heading instead of "factory".
func TestPlatformVersusBackend(t *testing.T) {
	m, ok := LookupModel("glm-5.3-flash")
	if !ok {
		t.Fatal("glm-5.3-flash is missing from the catalog")
	}
	if m.Provider != "factory" {
		t.Errorf("Provider = %q, want %q", m.Provider, "factory")
	}
	want := []string{"fireworks", "baseten", "databricks"}
	if len(m.Providers) != len(want) {
		t.Fatalf("Providers = %v, want %v", m.Providers, want)
	}
	for i := range want {
		if m.Providers[i] != want[i] {
			t.Errorf("Providers[%d] = %q, want %q", i, m.Providers[i], want[i])
		}
	}
	if got := m.DefaultProvider(); got != "fireworks" {
		t.Errorf("DefaultProvider = %q, want %q", got, "fireworks")
	}

	// A native model's backend is the platform's own API.
	c, ok := LookupModel("claude-opus-5-5")
	if !ok {
		t.Fatal("claude-opus-5-5 is missing from the catalog")
	}
	if c.Provider != "anthropic" {
		t.Errorf("Provider = %q, want %q", c.Provider, "anthropic")
	}
	if got := c.DefaultProvider(); got != "anthropic" {
		t.Errorf("DefaultProvider = %q, want %q", got, "anthropic")
	}
}

// TestNoTransportProviders guards against routing to a cloud marketplace
// transport: x-api-provider cannot select "azure_openai" or "bedrock_anthropic".
func TestNoTransportProviders(t *testing.T) {
	transports := map[string]bool{
		"bedrock_anthropic": true, "vertex_anthropic": true, "azure_anthropic": true,
		"bedrock_openai": true, "azure_openai": true, "snowflake": true, "together": true,
	}
	for _, m := range Catalog {
		for _, p := range m.AvailableProviders() {
			if transports[p] {
				t.Errorf("%s: provider list contains transport %q", m.ID, p)
			}
		}
	}
}

// TestCatalogHasPlatforms guards the grouping the admin UI renders: every
// platform must have at least one current model.
func TestCatalogHasPlatforms(t *testing.T) {
	current := map[string]int{}
	for _, m := range Catalog {
		if m.CLIVisible && m.DeprecatedOn == "" {
			current[m.Provider]++
		}
	}
	for _, p := range []string{"factory", "anthropic", "openai", "google", "xai"} {
		if current[p] == 0 {
			t.Errorf("platform %q has no current models", p)
		}
	}
}

// TestFamilyMapping pins the provider-to-wire-format rules: getting one wrong
// sends requests to an endpoint that cannot serve the model.
func TestFamilyMapping(t *testing.T) {
	cases := []struct {
		model string
		want  ModelFamily
	}{
		{"claude-opus-5-5", FamilyMessages},      // anthropic
		{"gpt-6-sol", FamilyResponses},           // openai
		{"gemini-3.8-flash", FamilyGoogle},       // google
		{"grok-4.7", FamilyChatCompletions},      // xai
		{"glm-5.3-flash", FamilyChatCompletions}, // factory, generic chat
	}
	for _, c := range cases {
		m, ok := LookupModel(c.model)
		if !ok {
			t.Errorf("%s is missing from the catalog", c.model)
			continue
		}
		if m.Family != c.want {
			t.Errorf("%s: family = %q, want %q", c.model, m.Family, c.want)
		}
	}

	// Gemini is reachable only through /v1/generate; nothing may translate it.
	for _, m := range Catalog {
		if m.Family == FamilyGoogle && !m.Passthrough {
			t.Errorf("%s: google models must be passthrough-only", m.ID)
		}
		if m.Family != FamilyGoogle && m.Passthrough {
			t.Errorf("%s: only google models may be passthrough", m.ID)
		}
	}
}

// TestCreditEstimate sanity-checks the cost model end to end: a known model
// with a known multiplier must produce the documented arithmetic.
func TestCreditEstimate(t *testing.T) {
	m, ok := LookupModel("glm-5.3-flash")
	if !ok {
		t.Fatal("glm-5.3-flash is missing from the catalog")
	}
	// weighted = 1000 + 3.34*100 = 1334; credits = ceil(1334 * 0.06) = 81
	if got := m.EstimateCredits(1000, 0, 0, 100); got != 81 {
		t.Errorf("EstimateCredits = %d, want 81", got)
	}
}
