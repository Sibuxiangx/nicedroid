package admin

import "testing"

func tpsRound(tps float64) BenchmarkResult {
	return BenchmarkResult{OK: true, Phase: "tps", TPS: tps, TTFTMs: 100}
}

func cacheRound(prompt, cached int) BenchmarkResult {
	return BenchmarkResult{OK: true, Phase: "cache", PromptTokens: prompt, CachedTokens: cached}
}

func TestSummarizeKeepsTheGroupsApart(t *testing.T) {
	// The cache rounds also carry a TPS figure, but it is not part of the
	// speed measurement: the two groups probe different things.
	s := summarizeProvider("databricks", []BenchmarkResult{
		tpsRound(80), tpsRound(100),
		cacheRound(2000, 0), cacheRound(2000, 1984), cacheRound(2000, 1990),
	})
	if s.CacheRounds != 3 {
		t.Fatalf("CacheRounds = %d, want 3", s.CacheRounds)
	}
	// (0 + 1984 + 1990) / 6000 — the speed group has already warmed the cache,
	// so every cache round counts as a steady-state reading.
	if s.CacheHitPct != 66.2 {
		t.Fatalf("CacheHitPct = %v, want 66.2", s.CacheHitPct)
	}
	if s.TPS != 90 {
		t.Fatalf("TPS = %v, want the median 90", s.TPS)
	}
	if s.TPSRoundsOK != 2 {
		t.Fatalf("TPSRoundsOK = %d, want 2", s.TPSRoundsOK)
	}
}

func TestSummarizeCacheHitWithoutRoundsToHit(t *testing.T) {
	// A provider that reported no cache figures has no hit rate to show.
	s := summarizeProvider("fireworks", []BenchmarkResult{tpsRound(30)})
	if s.CacheRounds != 0 || s.CacheHitPct != 0 {
		t.Fatalf("cache = %d/%v, want 0/0", s.CacheRounds, s.CacheHitPct)
	}
	if !s.OK {
		t.Fatal("a successful speed round must still summarise as OK")
	}
}

func TestPickBestPrefersCacheOverSpeed(t *testing.T) {
	// fireworks is twice as fast but re-bills the whole prompt each turn; the
	// slower caching backend is the one an agent loop can actually afford.
	best, _ := pickBest([]ProviderSummary{
		{Provider: "fireworks", OK: true, TPS: 200, CacheHitPct: 0, CacheRounds: 3},
		{Provider: "databricks", OK: true, TPS: 90, CacheHitPct: 99.4, CacheRounds: 3},
	})
	if best != "databricks" {
		t.Fatalf("best = %q, want databricks", best)
	}
}

func TestPickBestFallsBackToSpeedWhenNobodyCaches(t *testing.T) {
	best, _ := pickBest([]ProviderSummary{
		{Provider: "fireworks", OK: true, TPS: 200, CacheHitPct: 0, CacheRounds: 3},
		{Provider: "baseten", OK: true, TPS: 120, CacheHitPct: 20, CacheRounds: 3},
	})
	if best != "fireworks" {
		t.Fatalf("best = %q, want fireworks (fastest of a bad bunch)", best)
	}
}

func TestPickBestAmongCacheablesTakesTheFastest(t *testing.T) {
	best, _ := pickBest([]ProviderSummary{
		{Provider: "databricks", OK: true, TPS: 90, CacheHitPct: 99.4, CacheRounds: 3},
		{Provider: "baseten", OK: true, TPS: 140, CacheHitPct: 95.0, CacheRounds: 3},
	})
	if best != "baseten" {
		t.Fatalf("best = %q, want baseten (caching and faster)", best)
	}
}

func TestPickBestIgnoresFailingProviders(t *testing.T) {
	best, _ := pickBest([]ProviderSummary{
		{Provider: "fireworks", OK: false, Error: "upstream error"},
		{Provider: "baseten", OK: true, TPS: 100, CacheHitPct: 96, CacheRounds: 2},
	})
	if best != "baseten" {
		t.Fatalf("best = %q, want baseten", best)
	}
}

func TestBenchmarkPromptClearsTheCacheFloor(t *testing.T) {
	// Providers refuse to cache a prompt under ~1024 tokens, so a short
	// benchmark prompt would report every provider as cache-free.
	n := len(benchmarkCachePrefix) / 4
	if n < 1200 {
		t.Fatalf("benchmark cache prefix is ~%d tokens, want >= 1200", n)
	}
	if benchmarkCachePrefix != buildBenchmarkPrefix() {
		t.Fatal("benchmark cache prefix is not deterministic")
	}
}

func TestBenchmarkTaskAsksForALongGeneration(t *testing.T) {
	// TPS over a dozen output tokens measures prompt ingestion, not streaming
	// speed, which is how the benchmark ended up recommending a backend on a
	// 32 tok/s figure.
	if len(benchmarkTask) < 40 {
		t.Fatalf("benchmark task %q is too short to drive a long generation", benchmarkTask)
	}
}
