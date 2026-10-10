package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/Sibuxiangx/nicedroid/internal/config"
	"github.com/Sibuxiangx/nicedroid/internal/upstream"
)

// ---- provider management ----

// ModelView is a catalog entry enriched with its resolved provider state.
type ModelView struct {
	config.Model
	CurrentProvider string `json:"current_provider"`
	PinnedProvider  string `json:"pinned_provider"`
	ProviderCount   int    `json:"provider_count"`
}

// ListModels returns the catalog with provider state for each model.
func (a *Admin) ListModels(c echo.Context) error {
	out := make([]ModelView, 0, len(config.Catalog))
	for _, m := range config.Catalog {
		pinned, _ := a.Store.GetProviderOverride(m.ID)
		cur := pinned
		if cur == "" {
			cur = m.DefaultProvider()
		}
		out = append(out, ModelView{
			Model:           m,
			CurrentProvider: cur,
			PinnedProvider:  pinned,
			ProviderCount:   len(m.AvailableProviders()),
		})
	}
	return c.JSON(http.StatusOK, map[string]any{"models": out})
}

// SetModelProvider pins the provider for a model; an empty value restores the
// catalog default.
func (a *Admin) SetModelProvider(c echo.Context) error {
	m, ok := config.LookupModel(c.Param("id"))
	if !ok {
		return c.JSON(http.StatusNotFound, map[string]any{"error": "model not found"})
	}
	var body struct {
		Provider string `json:"provider"`
	}
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]any{"error": "invalid body"})
	}
	p := strings.TrimSpace(body.Provider)
	if p != "" && !contains(m.AvailableProviders(), p) {
		return c.JSON(http.StatusBadRequest, map[string]any{
			"error": fmt.Sprintf("provider %q is not available for %s", p, m.ID),
		})
	}
	if err := a.Store.SetProviderOverride(m.ID, p); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]any{"error": err.Error()})
	}
	cur := p
	if cur == "" {
		cur = m.DefaultProvider()
	}
	return c.JSON(http.StatusOK, map[string]any{
		"ok":               true,
		"model":            m.ID,
		"current_provider": cur,
		"pinned_provider":  p,
	})
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// ---- provider benchmark ----

const (
	// benchmarkMaxToken and the timeout bound one probe; the round counts
	// control how many chances each provider gets.
	benchmarkMaxToken  = 850
	benchmarkTimeout   = 90 * time.Second
	benchmarkRounds    = 3
	benchmarkMaxRounds = 5
)

// benchmarkPrompt has to clear the ~1024-token floor every provider applies
// before it will cache a prefix, and be byte-identical across rounds so round N
// can hit what round N-1 wrote. A short prompt would make every provider look
// cache-free no matter how good its cache is. Built once at startup.
var benchmarkPrompt = buildBenchmarkPrompt()

// buildBenchmarkPrompt emits a stable block of filler long enough to clear the
// cache minimum, with enough variation between lines that a provider cannot
// collapse it to a handful of tokens.
func buildBenchmarkPrompt() string {
	var b strings.Builder
	b.WriteString("Repository conventions reference:\n")
	for i := 1; i <= 50; i++ {
		fmt.Fprintf(&b, "%03d. Module %d follows the house style: exported identifiers use MixedCaps, "+
			"errors are wrapped with the %%w verb, every public function carries a doc comment "+
			"beginning with its own name, and tests live beside the code they cover.\n", i, i)
	}
	return b.String()
}

// BenchmarkResult is one provider's streaming probe outcome.
type BenchmarkResult struct {
	Provider     string  `json:"provider"`
	OK           bool    `json:"ok"`
	Round        int     `json:"round"`
	TTFTMs       int     `json:"ttft_ms"`
	ElapsedMs    int     `json:"elapsed_ms"`
	Tokens       int     `json:"tokens"`
	Chars        int     `json:"chars"`
	TPS          float64 `json:"tps"`
	PromptTokens int     `json:"prompt_tokens"`
	CachedTokens int     `json:"cached_tokens"`
	Error        string  `json:"error,omitempty"`
}

// ProviderSummary aggregates a provider's rounds into one comparable figure.
type ProviderSummary struct {
	Provider string  `json:"provider"`
	OK       bool    `json:"ok"`
	TPS      float64 `json:"tps"`
	TTFTMs   int     `json:"ttft_ms"`
	RoundsOK int     `json:"rounds_ok"`
	Rounds   int     `json:"rounds"`
	// CacheHitPct is the average share of the prompt served from cache, over
	// the rounds that could hit (round 1 writes the cache). CacheRounds is 0
	// when the provider reported no usable cache figures.
	CacheHitPct float64 `json:"cache_hit_pct"`
	CacheRounds int     `json:"cache_rounds"`
	Error       string  `json:"error,omitempty"`
}

// BenchmarkProviders streams the same generation through every provider of a
// model and reports time-to-first-token plus tokens per second. Providers are
// probed round-robin over several rounds so time-varying upstream load affects
// them equally, and each provider is summarised by the median of its rounds.
// Results are pushed as SSE events so the UI can show each provider the moment
// it finishes.
func (a *Admin) BenchmarkProviders(c echo.Context) error {
	m, ok := config.LookupModel(c.Param("id"))
	if !ok {
		return c.JSON(http.StatusNotFound, map[string]any{"error": "model not found"})
	}
	if m.Passthrough {
		return c.JSON(http.StatusBadRequest, map[string]any{"error": "passthrough models have no provider list to test"})
	}
	var body struct {
		Providers []string `json:"providers"`
		Rounds    int      `json:"rounds"`
	}
	_ = c.Bind(&body)
	targets := body.Providers
	if len(targets) == 0 {
		targets = m.AvailableProviders()
	}
	if len(targets) == 0 {
		return c.JSON(http.StatusBadRequest, map[string]any{"error": "model has no providers to test"})
	}
	rounds := body.Rounds
	if rounds <= 0 {
		rounds = benchmarkRounds
	}
	if rounds > benchmarkMaxRounds {
		rounds = benchmarkMaxRounds
	}

	w := c.Response()
	w.Header().Set(echo.HeaderContentType, "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	send := func(event string, payload any) bool {
		raw, err := json.Marshal(payload)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, raw); err != nil {
			return false
		}
		w.Flush()
		return true
	}

	if !send("start", map[string]any{"model": m.ID, "providers": targets, "rounds": rounds}) {
		return nil
	}

	collected := make(map[string][]BenchmarkResult, len(targets))
	for round := 1; round <= rounds; round++ {
		for _, p := range targets {
			if c.Request().Context().Err() != nil {
				return nil
			}
			if !send("probe", map[string]any{"provider": p, "round": round}) {
				return nil
			}
			res := a.benchProvider(c.Request().Context(), m, p)
			res.Round = round
			collected[p] = append(collected[p], res)
			if !send("result", res) {
				return nil
			}
		}
	}

	summaries := make([]ProviderSummary, 0, len(targets))
	for _, p := range targets {
		summaries = append(summaries, summarizeProvider(p, collected[p], rounds))
	}
	best, bestSum := pickBest(summaries)
	if best != "" {
		// Remember the outcome so the autoselector can route to the backend
		// that caches without the operator re-running the benchmark.
		if raw, err := json.Marshal(config.ProviderBenchmark{
			Version:  config.ManifestVersion(),
			Best:     best,
			CacheHit: bestSum.CacheHitPct,
			TPS:      bestSum.TPS,
		}); err == nil {
			_ = a.Store.SetSetting(config.BenchmarkSettingKey(m.ID), string(raw))
		}
	}
	_ = send("summary", map[string]any{
		"model":          m.ID,
		"providers":      summaries,
		"best":           best,
		"best_tps":       bestSum.TPS,
		"best_cache_hit": bestSum.CacheHitPct,
		"cache_hit_bar":  config.CacheHitBar,
	})
	return nil
}

// pickBest ranks providers cache-first: among those that clear the cache bar
// the fastest wins, and when nobody clears it raw speed decides. A provider
// that cannot cache is rarely worth its speed, because an agent loop re-bills
// the whole prompt on every turn.
func pickBest(summaries []ProviderSummary) (string, ProviderSummary) {
	var cached, fastest ProviderSummary
	for _, s := range summaries {
		if !s.OK {
			continue
		}
		if s.CacheRounds > 0 && s.CacheHitPct >= config.CacheHitBar {
			if cached.Provider == "" || s.TPS > cached.TPS {
				cached = s
			}
		}
		if fastest.Provider == "" || s.TPS > fastest.TPS {
			fastest = s
		}
	}
	if cached.Provider != "" {
		return cached.Provider, cached
	}
	if fastest.Provider != "" {
		return fastest.Provider, fastest
	}
	return "", ProviderSummary{}
}

// summarizeProvider reduces a provider's rounds to a median TPS and TTFT so a
// single lucky round cannot decide the winner, plus an average cache hit rate
// over the rounds that could actually hit.
func summarizeProvider(provider string, results []BenchmarkResult, rounds int) ProviderSummary {
	s := ProviderSummary{Provider: provider, Rounds: rounds}
	var tps []float64
	var ttfts []int
	cached, prompt, cacheRounds := 0, 0, 0
	lastErr := ""
	for _, r := range results {
		if !r.OK {
			if r.Error != "" {
				lastErr = r.Error
			}
			continue
		}
		tps = append(tps, r.TPS)
		if r.TTFTMs > 0 {
			ttfts = append(ttfts, r.TTFTMs)
		}
		// Round 1 writes the cache, so counting it would cap a two-round run at
		// 50% and make every provider look unusable.
		if r.Round > 1 && r.PromptTokens > 0 {
			cached += r.CachedTokens
			prompt += r.PromptTokens
			cacheRounds++
		}
	}
	s.RoundsOK = len(tps)
	if s.RoundsOK == 0 {
		s.Error = lastErr
		if s.Error == "" {
			s.Error = "no successful round"
		}
		return s
	}
	s.OK = true
	s.TPS = math.Round(medianFloat(tps)*10) / 10
	if len(ttfts) > 0 {
		s.TTFTMs = medianInt(ttfts)
	}
	if cacheRounds > 0 && prompt > 0 {
		s.CacheRounds = cacheRounds
		s.CacheHitPct = math.Round(float64(cached)/float64(prompt)*1000) / 10
	}
	return s
}

func medianFloat(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

func medianInt(v []int) int {
	if len(v) == 0 {
		return 0
	}
	s := append([]int(nil), v...)
	sort.Ints(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// benchProvider runs one streaming probe against a single provider.
func (a *Admin) benchProvider(parent context.Context, m config.Model, provider string) BenchmarkResult {
	res := BenchmarkResult{Provider: provider}
	key, ok := a.Router.NextKey(m.Tier)
	if !ok {
		res.Error = "no upstream key configured"
		return res
	}

	effort := benchmarkEffort(m)
	creq := &upstream.ChatRequest{
		Model:           m.ID,
		Stream:          true,
		MaxTokens:       benchmarkMaxToken,
		ReasoningEffort: config.MapEffort(provider, effort),
		Messages: []upstream.ChatMessage{
			{Role: "user", Content: json.RawMessage(strconv.Quote(benchmarkPrompt))},
		},
	}
	prompt, _ := a.Store.GetSetting("system_prompt", a.Cfg.DroidSystemPrompt)
	var upBody map[string]any
	switch m.Family {
	case config.FamilyMessages:
		upBody = upstream.ToMessages(creq, prompt, benchmarkMaxToken)
	case config.FamilyResponses:
		upBody = upstream.ToResponses(creq, prompt)
	default:
		upBody = upstream.ToChatCompletions(creq, prompt)
	}
	raw, err := json.Marshal(upBody)
	if err != nil {
		res.Error = err.Error()
		return res
	}

	ctx, cancel := context.WithTimeout(parent, benchmarkTimeout)
	defer cancel()

	start := time.Now()
	resp, err := a.Client.Do(ctx, upstream.Request{
		Path:   upstream.Endpoint(m.Family),
		APIKey: key.Key,
		Body:   raw,
		Stream: true,
	}, provider)
	if err != nil {
		res.Error = "upstream error: " + err.Error()
		return res
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		buf := make([]byte, 4096)
		n, _ := resp.Body.Read(buf)
		res.Error = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(buf[:n])))
		return res
	}

	var firstAt time.Time
	chars := 0
	usageTokens, promptTokens, cachedTokens := 0, 0, 0

	_ = upstream.ParseSSE(resp.Body, func(payload []byte) error {
		var ev map[string]any
		if json.Unmarshal(payload, &ev) != nil {
			return nil
		}
		d := benchmarkDelta(m.Family, ev)
		if d.text != "" {
			if firstAt.IsZero() {
				firstAt = time.Now()
			}
			chars += len(d.text)
		}
		if d.outTokens > usageTokens {
			usageTokens = d.outTokens
		}
		if d.prompt > promptTokens {
			promptTokens = d.prompt
		}
		if d.cached > cachedTokens {
			cachedTokens = d.cached
		}
		return nil
	})

	res.PromptTokens = promptTokens
	res.CachedTokens = cachedTokens
	res.ElapsedMs = int(time.Since(start).Milliseconds())
	res.Chars = chars
	if !firstAt.IsZero() {
		res.TTFTMs = int(firstAt.Sub(start).Milliseconds())
	}
	// Prefer the provider-reported output token count; fall back to a
	// character-based estimate so a provider without usage still ranks.
	if usageTokens > 0 {
		res.Tokens = usageTokens
	} else if chars > 0 {
		res.Tokens = int(math.Ceil(float64(chars) / 4))
	}
	if res.Tokens == 0 {
		res.Error = "no output produced"
		return res
	}
	res.TPS = measureTPS(res.Tokens, res.ElapsedMs)
	res.OK = true
	return res
}

// measureTPS reports effective end-to-end throughput: output tokens divided by
// the whole wall-clock time of the request.
//
// Per-chunk timing looks more precise but is not trustworthy here. Providers
// (and the transport) may buffer a stream and release it as one burst, which
// makes the first and last chunk land together and inflates any
// "tokens / generation window" ratio by orders of magnitude. Total elapsed time
// cannot be faked that way: it is what the caller actually waits for, so it
// ranks providers by the experience they deliver. Time-to-first-token is
// reported separately.
func measureTPS(tokens, elapsedMs int) float64 {
	if tokens <= 0 || elapsedMs <= 0 {
		return 0
	}
	return math.Round(float64(tokens)/float64(elapsedMs)*1000*10) / 10
}

// benchmarkEffort picks the reasoning effort for a probe: the cheapest setting
// the model advertises, so the measurement reflects raw generation speed.
func benchmarkEffort(m config.Model) string {
	for _, e := range m.ReasoningEfforts {
		if e == "off" {
			return "off"
		}
	}
	return m.DefaultReasonEffort
}

// benchDelta is what one SSE event contributes to the measurement.
type benchDelta struct {
	text      string
	outTokens int
	prompt    int
	cached    int
}

// benchmarkDelta extracts streamed text, output tokens and prompt cache figures
// from one SSE event in whichever wire format the model family speaks.
func benchmarkDelta(family config.ModelFamily, ev map[string]any) benchDelta {
	switch family {
	case config.FamilyMessages:
		switch ev["type"] {
		case "content_block_delta":
			if d, ok := ev["delta"].(map[string]any); ok {
				if t, ok := d["text"].(string); ok {
					return benchDelta{text: t}
				}
			}
		case "message_start":
			// Anthropic's input_tokens excludes the cached portion, so the
			// prompt total has to be reassembled from the three fields.
			if msg, ok := ev["message"].(map[string]any); ok {
				if u, ok := msg["usage"].(map[string]any); ok {
					in := intOf(u["input_tokens"])
					read := intOf(u["cache_read_input_tokens"])
					write := intOf(u["cache_creation_input_tokens"])
					return benchDelta{prompt: in + read + write, cached: read}
				}
			}
		case "message_delta":
			if u, ok := ev["usage"].(map[string]any); ok {
				return benchDelta{outTokens: intOf(u["output_tokens"])}
			}
		}
		return benchDelta{}
	case config.FamilyResponses:
		switch ev["type"] {
		case "response.output_text.delta":
			if d, ok := ev["delta"].(string); ok {
				return benchDelta{text: d}
			}
		case "response.completed":
			if r, ok := ev["response"].(map[string]any); ok {
				if u, ok := r["usage"].(map[string]any); ok {
					d := benchDelta{prompt: intOf(u["input_tokens"]), outTokens: intOf(u["output_tokens"])}
					if det, ok := u["input_tokens_details"].(map[string]any); ok {
						d.cached = intOf(det["cached_tokens"])
					}
					return d
				}
			}
		}
		return benchDelta{}
	default:
		// OpenAI chat/completions: deltas carry content, the last chunk carries
		// usage when stream_options.include_usage is set.
		if choices, ok := ev["choices"].([]any); ok && len(choices) > 0 {
			if ch, ok := choices[0].(map[string]any); ok {
				if d, ok := ch["delta"].(map[string]any); ok {
					if t, ok := d["content"].(string); ok {
						return benchDelta{text: t}
					}
				}
			}
		}
		d := benchDelta{}
		if u, ok := ev["usage"].(map[string]any); ok {
			d.outTokens = intOf(u["completion_tokens"])
			d.prompt = intOf(u["prompt_tokens"])
			if det, ok := u["prompt_tokens_details"].(map[string]any); ok {
				d.cached = intOf(det["cached_tokens"])
			}
		}
		return d
	}
}

func intOf(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	}
	return 0
}
