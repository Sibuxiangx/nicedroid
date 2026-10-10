package admin

import "testing"

func okRound(round, prompt, cached int, tps float64) BenchmarkResult {
	return BenchmarkResult{OK: true, Round: round, PromptTokens: prompt,
		CachedTokens: cached, TPS: tps, TTFTMs: 100}
}

func TestSummarizeCacheHitExcludesTheWriteRound(t *testing.T) {
	// Round 1 writes the cache and always reads 0, so folding it into the
	// average would cap a two-round run at 50% and make every provider look
	// unusable against the 90% bar.
	s := summarizeProvider("databricks", []BenchmarkResult{
		okRound(1, 2000, 0, 20),
		okRound(2, 2000, 1984, 90),
		okRound(3, 2000, 1990, 95),
	}, 3)
	if s.CacheRounds != 2 {
		t.Fatalf("CacheRounds = %d, want 2", s.CacheRounds)
	}
	// (1984 + 1990) / 4000
	if s.CacheHitPct != 99.4 {
		t.Fatalf("CacheHitPct = %v, want 99.4", s.CacheHitPct)
	}
	if s.TPS != 90 {
		t.Fatalf("TPS = %v, want the median 90", s.TPS)
	}
}

func TestSummarizeCacheHitWithoutRoundsToHit(t *testing.T) {
	// A single successful round can only ever write the cache, so there is no
	// hit rate to report and the UI has to show nothing rather than 0%.
	s := summarizeProvider("fireworks", []BenchmarkResult{okRound(1, 2000, 0, 30)}, 1)
	if s.CacheRounds != 0 || s.CacheHitPct != 0 {
		t.Fatalf("cache = %d/%v, want 0/0", s.CacheRounds, s.CacheHitPct)
	}
	if !s.OK {
		t.Fatal("a successful round must still summarise as OK")
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
	n := len(benchmarkPrompt) / 4
	if n < 1200 {
		t.Fatalf("benchmark prompt is ~%d tokens, want >= 1200", n)
	}
	if benchmarkPrompt != buildBenchmarkPrompt() {
		t.Fatal("benchmark prompt is not deterministic")
	}
}
