package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/Sibuxiangx/nicedroid/internal/config"
	"github.com/Sibuxiangx/nicedroid/internal/router"
	"github.com/Sibuxiangx/nicedroid/internal/store"
	"github.com/Sibuxiangx/nicedroid/internal/upstream"
)

// Handler serves the OpenAI-compatible proxy API.
type Handler struct {
	Cfg    config.Config
	Store  *store.Store
	Client *upstream.Client
	Router *router.Router
}

// NewHandler builds the API handler.
func NewHandler(cfg config.Config, st *store.Store, client *upstream.Client, rt *router.Router) *Handler {
	return &Handler{Cfg: cfg, Store: st, Client: client, Router: rt}
}

// Register mounts all proxy routes on the Echo instance.
func (h *Handler) Register(e *echo.Echo) {
	e.GET("/v1/models", h.ListModels)
	e.POST("/v1/chat/completions", h.ChatCompletions)
	e.POST("/v1/messages", h.Messages)
	e.POST("/v1/messages/count_tokens", h.CountTokens)
	e.POST("/v1/responses", h.Responses)
	e.POST("/v1/generate", h.GeneratePassthrough)
	e.GET("/healthz", h.Health)
}

// Health reports liveness.
func (h *Handler) Health(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]any{"ok": true})
}

// ListModels returns the OpenAI-compatible model list.
//
// Retired models are listed too, so a client already using one can still see
// it, but they carry deprecated/retired_on/replacement fields that clients can
// filter on. The CLI's own picker hides them; see the admin console for the
// same split.
func (h *Handler) ListModels(c echo.Context) error {
	var data []map[string]any
	for _, m := range config.Catalog {
		entry := map[string]any{
			"id":                m.ID,
			"object":            "model",
			"created":           nowUnix(),
			"owned_by":          m.Provider,
			"display_name":      m.DisplayName,
			"tier":              m.Tier,
			"family":            m.Family,
			"passthrough":       m.Passthrough,
			"vision":            m.Vision,
			"reasoning_efforts": m.ReasoningEfforts,
			"providers":         m.AvailableProviders(),
		}
		if m.DeprecatedOn != "" || !m.CLIVisible {
			entry["deprecated"] = true
		}
		if m.DeprecatedOn != "" {
			entry["retired_on"] = m.DeprecatedOn
		}
		if m.Replacement != "" {
			entry["replacement"] = m.Replacement
		}
		data = append(data, entry)
	}
	return c.JSON(http.StatusOK, map[string]any{"object": "list", "data": data})
}

// ChatCompletions handles POST /v1/chat/completions (OpenAI format inbound).
func (h *Handler) ChatCompletions(c echo.Context) error {
	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return openAIError(c, http.StatusBadRequest, "invalid_request_error", "cannot read body")
	}
	var req upstream.ChatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return openAIError(c, http.StatusBadRequest, "invalid_request_error", "invalid JSON body")
	}
	if req.Model == "" {
		return openAIError(c, http.StatusBadRequest, "invalid_request_error", "model is required")
	}
	return h.proxyChatLike(c, &req, clientFormatOpenAI, req.Model)
}

// clientFormat identifies which wire format the client expects back.
type clientFormat int

const (
	clientFormatOpenAI clientFormat = iota
	clientFormatAnthropic
	clientFormatResponses
)

// proxyChatLike is the shared pipeline for chat-style requests: resolve model,
// call upstream, and translate the response back. originalModel records the
// model the client asked for (for logging) when the request arrived in a
// different wire format or was pre-translated.
func (h *Handler) proxyChatLike(c echo.Context, req *upstream.ChatRequest, outFmt clientFormat, originalModel string) error {
	start := time.Now()
	clientKey := clientKeyFromContext(c)

	if originalModel == "" {
		originalModel = req.Model
	}
	model, ok := config.LookupModel(originalModel)
	if !ok {
		return openAIError(c, http.StatusNotFound, "invalid_request_error", fmt.Sprintf("model %q not found", originalModel))
	}

	ctx := c.Request().Context()
	if h.Cfg.RequestTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, h.Cfg.RequestTimeout)
		defer cancel()
	}

	key, ok := h.Router.NextKey(model.Tier)
	if !ok {
		return openAIError(c, http.StatusServiceUnavailable, "server_error", "no upstream API key configured; add one in the admin UI")
	}

	// Try the pinned/default provider first, then the remaining providers in
	// the model's list. Transient upstream failures (quota, rate limit, 5xx)
	// move on to the next provider instead of failing the client request.
	providers := h.providerChain(model)
	var resp *http.Response
	var usedProvider string
	var lastErr error
	for i, provider := range providers {
		upReq, err := h.buildUpstreamBody(req, model, provider)
		if err != nil {
			return openAIError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		}
		r, err := h.callUpstream(ctx, upReq, model, key, provider)
		if err != nil {
			lastErr = fmt.Errorf("provider %s: %w", provider, err)
			if i < len(providers)-1 {
				continue
			}
			return openAIError(c, http.StatusBadGateway, "server_error", "upstream error: "+lastErr.Error())
		}
		if retryableProviderStatus(r.StatusCode) && i < len(providers)-1 {
			io.Copy(io.Discard, r.Body)
			r.Body.Close()
			lastErr = fmt.Errorf("provider %s: HTTP %d", provider, r.StatusCode)
			continue
		}
		resp = r
		usedProvider = provider
		break
	}
	if resp == nil {
		msg := "no provider available"
		if lastErr != nil {
			msg = lastErr.Error()
		}
		return openAIError(c, http.StatusBadGateway, "server_error", "upstream error: "+msg)
	}
	defer resp.Body.Close()

	// log the request
	usage := &tokenUsage{}
	logRow := &store.RequestLog{
		ClientKey:      clientKey,
		RequestedModel: originalModel,
		UsedModel:      model.ID,
		Provider:       usedProvider,
		Pool:           string(model.Tier),
		Stream:         req.Stream,
	}
	defer func() {
		logRow.LatencyMs = int(time.Since(start).Milliseconds())
		usage.finalizeTTFT(start)
		usage.snapshot(logRow)
		_ = h.Store.InsertLog(*logRow)
	}()

	if resp.StatusCode != http.StatusOK {
		logRow.Status = resp.StatusCode
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		logRow.Error = truncateStr(string(body), 500)
		return c.JSON(resp.StatusCode, json.RawMessage(body))
	}

	if req.Stream {
		return h.streamResponse(c, resp, model, originalModel, outFmt, logRow, usage, start)
	}
	return h.bufferedResponse(c, resp, model, originalModel, outFmt, logRow, usage, start)
}

// retryableProviderStatus reports whether an upstream status should make the
// proxy try the next provider for the same model.
func retryableProviderStatus(status int) bool {
	return status == http.StatusPaymentRequired ||
		status == http.StatusTooManyRequests ||
		status >= 500
}

// resolveProvider returns the provider to use for a model: the admin-pinned
// override when set, then a measured benchmark result, then the catalog
// default. The catalog order is not chosen for caching, and a backend that
// cannot cache re-bills the whole prompt every turn, so a measurement that
// found one which does outranks it.
func (h *Handler) resolveProvider(m config.Model) string {
	if v, err := h.Store.GetProviderOverride(m.ID); err == nil && v != "" {
		return v
	}
	if raw, err := h.Store.GetSetting(config.BenchmarkSettingKey(m.ID), ""); err == nil {
		if b, ok := config.ParseProviderBenchmark(raw); ok &&
			b.Version == config.ManifestVersion() {
			return b.Best
		}
	}
	return m.DefaultProvider()
}

// providerChain returns the ordered list of providers to try: the resolved one
// first, then the rest of the model's list.
func (h *Handler) providerChain(m config.Model) []string {
	resolved := h.resolveProvider(m)
	all := m.AvailableProviders()
	if len(all) == 0 {
		if resolved == "" {
			return []string{""}
		}
		return []string{resolved}
	}
	chain := []string{resolved}
	for _, p := range all {
		if p != resolved {
			chain = append(chain, p)
		}
	}
	return chain
}

// tokenUsage accumulates usage for logging, including cache hits and TTFT.
type tokenUsage struct {
	Prompt     int
	Completion int
	CacheRead  int
	CacheWrite int
	TTFTMs     int
	firstWrite time.Time
}

// markFirstWrite records the time of the first response byte sent to the client.
func (u *tokenUsage) markFirstWrite() {
	if u.firstWrite.IsZero() {
		u.firstWrite = time.Now()
	}
}

// finalizeTTFT computes time-to-first-token relative to the request start.
func (u *tokenUsage) finalizeTTFT(start time.Time) {
	if !u.firstWrite.IsZero() {
		u.TTFTMs = int(u.firstWrite.Sub(start).Milliseconds())
	}
}

// snapshot copies accumulated usage into the log row.
func (u *tokenUsage) snapshot(logRow *store.RequestLog) {
	logRow.PromptTokens = u.Prompt
	logRow.CompletionTokens = u.Completion
	logRow.CacheReadTokens = u.CacheRead
	logRow.CacheWriteTokens = u.CacheWrite
	logRow.TTFTMs = u.TTFTMs
}

// buildUpstreamBody renders the canonical request into the wire format of the
// target model family. The upstream model id is always taken from the resolved
// model, and the reasoning effort is translated into whatever the chosen
// provider accepts.
func (h *Handler) buildUpstreamBody(req *upstream.ChatRequest, model config.Model, provider string) (map[string]any, error) {
	r := *req
	r.Model = model.ID
	r.ReasoningEffort = config.MapEffort(provider, r.ReasoningEffort)
	switch model.Family {
	case config.FamilyMessages:
		body := upstream.ToMessages(&r, h.systemPrompt(), 8192)
		if h.promptCacheEnabled() {
			injectCacheBreakpoints(body)
		}
		return body, nil
	case config.FamilyResponses:
		return upstream.ToResponses(&r, h.systemPrompt()), nil
	case config.FamilyGoogle:
		return nil, errors.New("google models are passthrough-only; use /v1/generate")
	default:
		return upstream.ToChatCompletions(&r, h.systemPrompt()), nil
	}
}

// callUpstream sends the request to the gateway using the given key and
// provider.
func (h *Handler) callUpstream(ctx context.Context, body map[string]any, model config.Model, key store.UpstreamKey, provider string) (*http.Response, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	path := upstream.Endpoint(model.Family)
	return h.Client.Do(ctx, upstream.Request{
		Path:   path,
		APIKey: key.Key,
		Body:   raw,
		Stream: true,
	}, provider)
}

func (h *Handler) systemPrompt() string {
	if p, err := h.Store.GetSetting("system_prompt", h.Cfg.DroidSystemPrompt); err == nil && p != "" {
		return p
	}
	return h.Cfg.DroidSystemPrompt
}

// promptCacheEnabled reports whether prompt-cache breakpoints are injected into
// Anthropic-family requests. The database setting wins over the environment so
// it can be toggled without a redeploy.
func (h *Handler) promptCacheEnabled() bool {
	if v, err := h.Store.GetSetting("prompt_cache", ""); err == nil && v != "" {
		return v == "true" || v == "on"
	}
	return h.Cfg.PromptCache
}

// openAIError renders an OpenAI-style error.
func openAIError(c echo.Context, status int, typ, msg string) error {
	return c.JSON(status, map[string]any{
		"error": map[string]any{
			"message": msg,
			"type":    typ,
			"code":    status,
		},
	})
}

// clientKeyFromContext pulls the authenticated client key label.
func clientKeyFromContext(c echo.Context) string {
	if v := c.Get("client_key_name"); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// bearer extracts the bearer token from the Authorization header.
func bearer(c echo.Context) string {
	h := c.Request().Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(h[len("Bearer "):])
	}
	return ""
}
