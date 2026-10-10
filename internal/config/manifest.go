package config

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Sibuxiangx/nicedroid"
)

// The model catalog is not written in Go. It is extracted from the Droid CLI
// binary into manifest.json by tools/extract-manifest, embedded into this
// binary, and translated here into Model values. Refreshing it happens in CI
// (.github/workflows/update-models.yml), never by editing Go code.

// Manifest is the decoded model catalog. The file is deterministic for a given
// CLI build, which is what lets the refresh workflow compare versions instead of
// diffing content.
type Manifest struct {
	Schema int             `json:"schema"`
	Source string          `json:"source"`
	CLI    ManifestCLI     `json:"cli"`
	Models []ManifestModel `json:"models"`
}

// ManifestCLI records which Droid build the catalog was taken from.
type ManifestCLI struct {
	Version   string `json:"version"`
	UserAgent string `json:"user_agent"`
}

// ManifestModel is one registry entry as extracted from the CLI.
type ManifestModel struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Provider         string   `json:"provider"`
	APIModelProvider string   `json:"api_model_provider"`
	Providers        []string `json:"providers"`
	QuotaPool        string   `json:"quota_pool"`
	ServiceTier      string   `json:"service_tier"`
	Vision           bool     `json:"vision"`
	PDF              bool     `json:"pdf"`
	Reasoning        struct {
		Efforts []string `json:"efforts"`
		Default string   `json:"default"`
	} `json:"reasoning"`
	Cost struct {
		TokenMultiplier     float64 `json:"token_multiplier"`
		OutputMultiplier    float64 `json:"output_multiplier"`
		CacheReadMultiplier float64 `json:"cache_read_multiplier"`
	} `json:"cost"`
	Promotions   []Promotion `json:"promotions"`
	DeprecatedOn string      `json:"deprecated_on"`
	Replacement  string      `json:"replacement"`
	CLIVisible   bool        `json:"cli_visible"`
}

// Promotion is a time-boxed discount on a model's token multiplier.
type Promotion struct {
	Discount  float64 `json:"discount"`
	StartsAt  string  `json:"starts_at"`
	ExpiresAt string  `json:"expires_at"`
	Label     string  `json:"label"`
}

func decodeManifest() Manifest {
	var m Manifest
	if err := json.Unmarshal(nicedroid.ManifestJSON, &m); err != nil {
		panic("embedded manifest.json is not valid JSON: " + err.Error())
	}
	if len(m.Models) == 0 {
		panic("embedded manifest.json contains no models")
	}
	if m.CLI.Version == "" {
		panic("embedded manifest.json has no cli.version")
	}
	return m
}

var embeddedManifest = decodeManifest()

// ManifestVersion is the CLI version the embedded catalog was extracted from.
//
// The proxy identifies itself upstream with this version, so when Factory ships
// a CLI release that the gateway stops accepting, refreshing the manifest is
// what moves the gateway forward.
func ManifestVersion() string { return embeddedManifest.CLI.Version }

// ManifestUserAgent is the user-agent the CLI would send for this build.
func ManifestUserAgent() string { return embeddedManifest.CLI.UserAgent }

// ManifestSummary describes the embedded catalog for startup logs.
func ManifestSummary() string {
	var current, retired int
	for _, m := range embeddedManifest.Models {
		if m.DeprecatedOn != "" || !m.CLIVisible {
			retired++
			continue
		}
		current++
	}
	return fmt.Sprintf("%d models from droid %s (%d current, %d retired)",
		len(embeddedManifest.Models), embeddedManifest.CLI.Version, current, retired)
}

// loadManifest translates the embedded catalog into Models.
func loadManifest() []Model {
	out := make([]Model, 0, len(embeddedManifest.Models))
	for _, e := range embeddedManifest.Models {
		family := familyFor(e)
		out = append(out, Model{
			ID:          e.ID,
			DisplayName: e.Name,
			Family:      family,
			// Provider is the platform that serves the model (anthropic,
			// openai, google, xai, factory). For open-weight models routed
			// through Factory that is "factory" while Providers holds the
			// backends it is actually dispatched to.
			Provider:            e.Provider,
			Providers:           append([]string(nil), e.Providers...),
			Tier:                tierFor(e.QuotaPool),
			Vision:              e.Vision,
			Multiplier:          e.Cost.TokenMultiplier,
			OutputMultiplier:    e.Cost.OutputMultiplier,
			CacheReadMultiplier: e.Cost.CacheReadMultiplier,
			Promotions:          e.Promotions,
			Passthrough:         family == FamilyGoogle,
			DefaultReasonEffort: e.Reasoning.Default,
			ReasoningEfforts:    append([]string(nil), e.Reasoning.Efforts...),
			ServiceTier:         e.ServiceTier,
			DeprecatedOn:        e.DeprecatedOn,
			Replacement:         e.Replacement,
			CLIVisible:          e.CLIVisible,
		})
	}
	return out
}

// familyFor picks the upstream wire format for a registry entry.
//
// The registry states it two ways: native models carry the provider whose API
// they use, while open-weight models routed through Factory carry an
// apiModelProvider describing the request shape ("generic-chat-completion-api",
// or "anthropic" for the few that speak Messages).
func familyFor(e ManifestModel) ModelFamily {
	switch e.Provider {
	case "anthropic":
		return FamilyMessages
	case "google":
		return FamilyGoogle
	case "openai":
		return FamilyResponses
	case "xai":
		// xAI's API is chat-completions shaped.
		return FamilyChatCompletions
	default: // factory
		if e.APIModelProvider == "anthropic" {
			return FamilyMessages
		}
		return FamilyChatCompletions
	}
}

// tierFor maps the registry's billing pool onto the gateway's quota pools.
// Only "core" is special; everything else bills against standard.
func tierFor(pool string) ModelTier {
	if strings.EqualFold(pool, string(TierCore)) {
		return TierCore
	}
	return TierStandard
}
