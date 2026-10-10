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
	// A longer generation keeps the measurement window wide enough that timing
	// jitter does not swamp the provider-to-provider difference.
	benchmarkPrompt    = "Write the integers from 1 to 300, one per line, with no other text and no commentary."
	benchmarkMaxToken  = 850
	benchmarkTimeout   = 90 * time.Second
	benchmarkRounds    = 2
	benchmarkMaxRounds = 3
)

// BenchmarkResult is one provider's streaming probe outcome.
type BenchmarkResult struct {
	Provider  string  `json:"provider"`
	OK        bool    `json:"ok"`
	Round     int     `json:"round"`
	TTFTMs    int     `json:"ttft_ms"`
	ElapsedMs int     `json:"elapsed_ms"`
	Tokens    int     `json:"tokens"`
	Chars     int     `json:"chars"`
	TPS       float64 `json:"tps"`
	Error     string  `json:"error,omitempty"`
}

// ProviderSummary aggregates a provider's rounds into one comparable figure.
type ProviderSummary struct {
	Provider string  `json:"provider"`
	OK       bool    `json:"ok"`
	TPS      float64 `json:"tps"`
	TTFTMs   int     `json:"ttft_ms"`
	RoundsOK int     `json:"rounds_ok"`
	Rounds   int     `json:"rounds"`
	Error    string  `json:"error,omitempty"`
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
	best := ""
	bestTPS := 0.0
	for _, p := range targets {
		s := summarizeProvider(p, collected[p], rounds)
		summaries = append(summaries, s)
		if s.OK && s.TPS > bestTPS {
			bestTPS = s.TPS
			best = p
		}
	}
	_ = send("summary", map[string]any{
		"model":     m.ID,
		"providers": summaries,
		"best":      best,
		"best_tps":  math.Round(bestTPS*10) / 10,
	})
	return nil
}

// summarizeProvider reduces a provider's rounds to a median TPS and TTFT so a
// single lucky round cannot decide the winner.
func summarizeProvider(provider string, results []BenchmarkResult, rounds int) ProviderSummary {
	s := ProviderSummary{Provider: provider, Rounds: rounds}
	var tps []float64
	var ttfts []int
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
	usageTokens := 0

	_ = upstream.ParseSSE(resp.Body, func(payload []byte) error {
		var ev map[string]any
		if json.Unmarshal(payload, &ev) != nil {
			return nil
		}
		s, tok := benchmarkDelta(m.Family, ev)
		if s != "" {
			if firstAt.IsZero() {
				firstAt = time.Now()
			}
			chars += len(s)
		}
		if tok > usageTokens {
			usageTokens = tok
		}
		return nil
	})

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

// benchmarkDelta extracts streamed text and reported output tokens from one
// SSE event in whichever wire format the model family speaks.
func benchmarkDelta(family config.ModelFamily, ev map[string]any) (string, int) {
	switch family {
	case config.FamilyMessages:
		switch ev["type"] {
		case "content_block_delta":
			if d, ok := ev["delta"].(map[string]any); ok {
				if t, ok := d["text"].(string); ok {
					return t, 0
				}
			}
		case "message_delta":
			if u, ok := ev["usage"].(map[string]any); ok {
				return "", intOf(u["output_tokens"])
			}
		}
		return "", 0
	case config.FamilyResponses:
		switch ev["type"] {
		case "response.output_text.delta":
			if d, ok := ev["delta"].(string); ok {
				return d, 0
			}
		case "response.completed":
			if r, ok := ev["response"].(map[string]any); ok {
				if u, ok := r["usage"].(map[string]any); ok {
					return "", intOf(u["output_tokens"])
				}
			}
		}
		return "", 0
	default:
		// OpenAI chat/completions: deltas carry content, the last chunk carries
		// usage when stream_options.include_usage is set.
		out := ""
		if choices, ok := ev["choices"].([]any); ok && len(choices) > 0 {
			if ch, ok := choices[0].(map[string]any); ok {
				if d, ok := ch["delta"].(map[string]any); ok {
					if t, ok := d["content"].(string); ok {
						out = t
					}
				}
			}
		}
		tok := 0
		if u, ok := ev["usage"].(map[string]any); ok {
			tok = intOf(u["completion_tokens"])
		}
		return out, tok
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
