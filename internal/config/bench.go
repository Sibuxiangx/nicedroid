package config

import "encoding/json"

// ProviderBenchmark is a model's recorded benchmark outcome, kept in the
// settings table so the autoselector can prefer a backend that actually caches
// without re-measuring on every start.
type ProviderBenchmark struct {
	// Version is the manifest version the run measured. A record from an older
	// catalog describes providers that may no longer exist, so it is ignored.
	Version string `json:"version"`
	Best    string `json:"best"`
	// CacheHit is the winner's average cache hit rate in percent. The
	// autoselector only trusts it while it clears CacheHitBar.
	CacheHit float64 `json:"cache_hit"`
	TPS      float64 `json:"tps"`
}

// BenchmarkSettingKey is the settings key holding a model's benchmark record.
func BenchmarkSettingKey(modelID string) string { return "bench:" + modelID }

// ParseProviderBenchmark decodes a stored benchmark record.
func ParseProviderBenchmark(raw string) (ProviderBenchmark, bool) {
	if raw == "" {
		return ProviderBenchmark{}, false
	}
	var b ProviderBenchmark
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		return ProviderBenchmark{}, false
	}
	if b.Best == "" || b.Version == "" {
		return ProviderBenchmark{}, false
	}
	return b, true
}

// CacheHitBar is the minimum average cache hit rate a provider must reach to be
// picked for agent traffic. Below it every turn re-bills the whole prompt as
// fresh input, which costs more than a slower backend that caches, so a
// provider under the bar is effectively unusable for an agent loop.
const CacheHitBar = 90.0
