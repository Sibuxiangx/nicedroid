package config

import (
	"math"
	"time"
)

// Factory credit estimation.
//
// The Droid CLI computes per-turn Factory Standard Credits locally from token
// usage and per-model cost parameters (function `calculateFactoryCreditsForUsage`
// in the CLI bundle). The proxy mirrors that formula so request logs can show a
// comparable number:
//
//	weighted = fresh_input
//	         + 1.25 * cache_write_5m
//	         + 2.00 * cache_write_1h
//	         + cache_read_multiplier   * cache_read
//	         + output_multiplier       * output
//	credits  = ceil(weighted * token_multiplier * (1 - active_promo_discount))
//
// Defaults when a model does not override them: cache_read_multiplier 0.1;
// output_multiplier by provider family (anthropic 5, openai 8, google 6,
// factory 4, xai 5) with a 4 fallback. The CLI treats every cache write as the
// 5-minute TTL tier, and so do we.

const (
	defaultCacheReadMultiplier = 0.1
	defaultOutputMultiplier    = 4.0
	cacheWrite5mMultiplier     = 1.25
	cacheWrite1hMultiplier     = 2.0
)

// defaultOutputMultipliers maps a cost provider family to its output-token
// multiplier default, matching the CLI's provider table.
var defaultOutputMultipliers = map[string]float64{
	"anthropic": 5,
	"openai":    8,
	"google":    6,
	"factory":   4,
	"xai":       5,
}

// CostProvider returns the family used for cost defaults. Models served by
// Factory (open-weight / Droid Core) bill as "factory"; the rest map from the
// wire family.
func (m Model) CostProvider() string {
	switch m.Provider {
	case "anthropic":
		return "anthropic"
	case "openai":
		return "openai"
	case "google":
		return "google"
	case "xai":
		return "xai"
	default:
		// fireworks / baseten / mistral / databricks open-weight models all
		// bill through the Factory pool.
		return "factory"
	}
}

// OutputTokenMultiplier resolves the effective output multiplier.
func (m Model) OutputTokenMultiplier() float64 {
	if m.OutputMultiplier > 0 {
		return m.OutputMultiplier
	}
	if v, ok := defaultOutputMultipliers[m.CostProvider()]; ok {
		return v
	}
	return defaultOutputMultiplier
}

// CacheReadTokenMultiplier resolves the effective cache-read multiplier.
func (m Model) CacheReadTokenMultiplier() float64 {
	if m.CacheReadMultiplier > 0 {
		return m.CacheReadMultiplier
	}
	return defaultCacheReadMultiplier
}

// activePromoDiscount returns the promotional discount in effect right now,
// or 0 when none applies.
func (m Model) activePromoDiscount(now time.Time) float64 {
	if m.PromotionDiscount <= 0 || m.PromotionStarts == "" || m.PromotionEnds == "" {
		return 0
	}
	start, err1 := time.Parse(time.RFC3339, m.PromotionStarts)
	end, err2 := time.Parse(time.RFC3339, m.PromotionEnds)
	if err1 != nil || err2 != nil {
		return 0
	}
	if now.Before(start) || now.After(end) {
		return 0
	}
	return m.PromotionDiscount
}

// EstimateCredits computes Factory Standard Credits for one request, mirroring
// the Droid CLI formula. Inputs are token counts; cache writes are assumed to
// be the 5-minute TTL tier (what the CLI records when no breakdown is known).
func (m Model) EstimateCredits(inputFresh, cacheWrite, cacheRead, output int) int64 {
	if inputFresh < 0 {
		inputFresh = 0
	}
	if cacheWrite < 0 {
		cacheWrite = 0
	}
	if cacheRead < 0 {
		cacheRead = 0
	}
	if output < 0 {
		output = 0
	}
	weighted := float64(inputFresh) +
		cacheWrite5mMultiplier*float64(cacheWrite) +
		m.CacheReadTokenMultiplier()*float64(cacheRead) +
		m.OutputTokenMultiplier()*float64(output)
	multiplier := m.Multiplier * (1 - m.activePromoDiscount(time.Now()))
	credits := weighted * multiplier
	if credits <= 0 {
		return 0
	}
	// ceil, matching the CLI
	return int64(math.Ceil(credits))
}

// EstimateCreditsForModel looks up a model and computes credits; unknown models
// return 0.
func EstimateCreditsForModel(modelID string, inputFresh, cacheWrite, cacheRead, output int) int64 {
	m, ok := LookupModel(modelID)
	if !ok {
		return 0
	}
	return m.EstimateCredits(inputFresh, cacheWrite, cacheRead, output)
}

// EstimateCreditsForUsage is the log-friendly entry point: promptTotal is the
// total input token count including cached and cache-write tokens (as both the
// Anthropic and OpenAI wire formats report it), so the fresh input portion is
// derived here.
func EstimateCreditsForUsage(modelID string, promptTotal, cacheRead, cacheWrite, output int) int64 {
	fresh := promptTotal - cacheRead - cacheWrite
	if fresh < 0 {
		fresh = 0
	}
	return EstimateCreditsForModel(modelID, fresh, cacheWrite, cacheRead, output)
}
