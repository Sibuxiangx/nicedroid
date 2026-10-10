package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/Sibuxiangx/nicedroid/internal/config"
	"github.com/Sibuxiangx/nicedroid/internal/store"
	"github.com/Sibuxiangx/nicedroid/internal/upstream"
)

// ---------------- Anthropic Messages inbound ----------------

// Messages handles POST /v1/messages (Anthropic format inbound).
// When the target model speaks the Anthropic wire format the request is
// forwarded nearly verbatim (system prompt injected). Otherwise the request is
// translated to the canonical form and routed through the shared pipeline.
func (h *Handler) Messages(c echo.Context) error {
	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return anthropicError(c, http.StatusBadRequest, "invalid_request_error", "cannot read body")
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return anthropicError(c, http.StatusBadRequest, "invalid_request_error", "invalid JSON body")
	}
	modelID, _ := raw["model"].(string)
	if modelID == "" {
		return anthropicError(c, http.StatusBadRequest, "invalid_request_error", "model is required")
	}
	m, ok := config.LookupModel(modelID)
	if !ok {
		return anthropicError(c, http.StatusNotFound, "not_found_error", fmt.Sprintf("model %q not found", modelID))
	}
	if m.Passthrough {
		return anthropicError(c, http.StatusBadRequest, "invalid_request_error",
			fmt.Sprintf("model %q is passthrough-only; use its native endpoint instead", modelID))
	}

	// Anthropic-native upstream: forward nearly verbatim.
	if m.Family == config.FamilyMessages {
		return h.messagesPassthrough(c, raw, m)
	}

	// Otherwise translate to canonical and reuse the shared pipeline.
	creq := anthropicToCanonical(raw, modelID)
	return h.proxyChatLike(c, creq, clientFormatAnthropic, modelID)
}

// messagesPassthrough forwards an Anthropic request with the Droid system
// prompt injected. Upstream errors (including quota rejections) pass through.
func (h *Handler) messagesPassthrough(c echo.Context, raw map[string]any, m config.Model) error {
	start := time.Now()
	clientKey := clientKeyFromContext(c)
	stream, _ := raw["stream"].(bool)

	// Cache breakpoints are added before marshalling: Anthropic bills an
	// unmarked prefix as fresh input on every turn, which makes an agent loop
	// prohibitively expensive. Requests that already carry a marker are left as
	// the caller wrote them.
	body := injectAnthropicSystem(raw, h.systemPrompt())
	if h.promptCacheEnabled() {
		injectCacheBreakpoints(body)
	}
	reqBody, err := json.Marshal(body)
	if err != nil {
		return anthropicError(c, http.StatusBadRequest, "invalid_request_error", "cannot encode request")
	}

	ctx := c.Request().Context()
	if h.Cfg.RequestTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, h.Cfg.RequestTimeout)
		defer cancel()
	}

	key, ok := h.Router.NextKey(m.Tier)
	if !ok {
		return anthropicError(c, http.StatusServiceUnavailable, "api_error", "no upstream API key configured")
	}
	provider := h.resolveProvider(m)
	resp, err := h.Client.Do(ctx, upstream.Request{Path: upstream.PathMessages, APIKey: key.Key, Body: reqBody, Stream: stream}, provider)
	if err != nil {
		return anthropicError(c, http.StatusBadGateway, "api_error", "upstream error: "+err.Error())
	}
	defer resp.Body.Close()

	usage := &tokenUsage{}
	logRow := &store.RequestLog{
		ClientKey:      clientKey,
		RequestedModel: m.ID,
		UsedModel:      m.ID,
		Provider:       provider,
		Pool:           string(m.Tier),
		Stream:         stream,
	}
	finishLog := func() {
		logRow.LatencyMs = int(time.Since(start).Milliseconds())
		usage.finalizeTTFT(start)
		usage.snapshot(logRow)
		_ = h.Store.InsertLog(*logRow)
	}

	if resp.StatusCode != http.StatusOK {
		defer finishLog()
		logRow.Status = resp.StatusCode
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		logRow.Error = truncateStr(string(payload), 500)
		return c.JSON(resp.StatusCode, json.RawMessage(payload))
	}

	if stream {
		defer finishLog()
		logRow.Status = http.StatusOK
		c.Response().Header().Set(echo.HeaderContentType, "text/event-stream")
		c.Response().Header().Set("Cache-Control", "no-cache")
		c.Response().WriteHeader(http.StatusOK)
		return forwardSSEPreservingLines(&ttftWriter{c.Response(), usage}, resp.Body, func(payload []byte) {
			var ev map[string]any
			if json.Unmarshal(payload, &ev) == nil {
				captureAnthropicUsage(ev, usage)
			}
		}, usage)
	}

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		defer finishLog()
		logRow.Status = http.StatusBadGateway
		return anthropicError(c, http.StatusBadGateway, "api_error", "upstream read error")
	}
	var up map[string]any
	if json.Unmarshal(payload, &up) == nil {
		captureAnthropicUsage(up, usage)
	}
	defer finishLog()
	logRow.Status = http.StatusOK
	return c.JSON(http.StatusOK, json.RawMessage(payload))
}

// injectAnthropicSystem ensures the Droid system prompt leads the top-level
// system field (the gateway content gate requires it).
func injectAnthropicSystem(raw map[string]any, prompt string) map[string]any {
	out := make(map[string]any, len(raw))
	for k, v := range raw {
		out[k] = v
	}
	sys := []any{map[string]any{"type": "text", "text": prompt}}
	switch cur := raw["system"].(type) {
	case string:
		if cur != "" {
			sys = append(sys, map[string]any{"type": "text", "text": cur})
		}
	case []any:
		sys = append(sys, cur...)
	}
	out["system"] = sys
	return out
}

// ---------------- OpenAI Responses inbound ----------------

// Responses handles POST /v1/responses (OpenAI Responses API inbound).
func (h *Handler) Responses(c echo.Context) error {
	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return openAIError(c, http.StatusBadRequest, "invalid_request_error", "cannot read body")
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return openAIError(c, http.StatusBadRequest, "invalid_request_error", "invalid JSON body")
	}
	modelID, _ := raw["model"].(string)
	if modelID == "" {
		return openAIError(c, http.StatusBadRequest, "invalid_request_error", "model is required")
	}
	m, ok := config.LookupModel(modelID)
	if !ok {
		return openAIError(c, http.StatusNotFound, "invalid_request_error", fmt.Sprintf("model %q not found", modelID))
	}
	if m.Passthrough {
		return openAIError(c, http.StatusBadRequest, "invalid_request_error",
			fmt.Sprintf("model %q is passthrough-only; use its native endpoint instead", modelID))
	}

	if m.Family == config.FamilyResponses {
		return h.responsesPassthrough(c, raw, m)
	}
	creq := responsesToCanonical(raw, modelID)
	return h.proxyChatLike(c, creq, clientFormatResponses, modelID)
}

// responsesPassthrough forwards a Responses request with the Droid instructions
// injected. Upstream errors (including quota rejections) pass through.
func (h *Handler) responsesPassthrough(c echo.Context, raw map[string]any, m config.Model) error {
	start := time.Now()
	clientKey := clientKeyFromContext(c)
	stream, _ := raw["stream"].(bool)

	body := injectResponsesInstructions(raw, h.systemPrompt())

	ctx := c.Request().Context()
	if h.Cfg.RequestTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, h.Cfg.RequestTimeout)
		defer cancel()
	}

	key, ok := h.Router.NextKey(m.Tier)
	if !ok {
		return openAIError(c, http.StatusServiceUnavailable, "server_error", "no upstream API key configured")
	}
	provider := h.resolveProvider(m)
	resp, err := h.Client.Do(ctx, upstream.Request{Path: upstream.PathResponses, APIKey: key.Key, Body: body, Stream: stream}, provider)
	if err != nil {
		return openAIError(c, http.StatusBadGateway, "server_error", "upstream error: "+err.Error())
	}
	defer resp.Body.Close()

	usage := &tokenUsage{}
	logRow := &store.RequestLog{
		ClientKey:      clientKey,
		RequestedModel: m.ID,
		UsedModel:      m.ID,
		Provider:       provider,
		Pool:           string(m.Tier),
		Stream:         stream,
	}
	finishLog := func() {
		logRow.LatencyMs = int(time.Since(start).Milliseconds())
		usage.finalizeTTFT(start)
		usage.snapshot(logRow)
		_ = h.Store.InsertLog(*logRow)
	}

	if resp.StatusCode != http.StatusOK {
		defer finishLog()
		logRow.Status = resp.StatusCode
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		logRow.Error = truncateStr(string(payload), 500)
		return c.JSON(resp.StatusCode, json.RawMessage(payload))
	}

	if stream {
		defer finishLog()
		logRow.Status = http.StatusOK
		c.Response().Header().Set(echo.HeaderContentType, "text/event-stream")
		c.Response().Header().Set("Cache-Control", "no-cache")
		c.Response().WriteHeader(http.StatusOK)
		return forwardSSEPreservingLines(&ttftWriter{c.Response(), usage}, resp.Body, func(payload []byte) {
			var ev map[string]any
			if json.Unmarshal(payload, &ev) == nil && ev["type"] == "response.completed" {
				if r, ok := ev["response"].(map[string]any); ok {
					captureResponsesUsage(r, usage)
				}
			}
		}, usage)
	}

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		defer finishLog()
		logRow.Status = http.StatusBadGateway
		return openAIError(c, http.StatusBadGateway, "server_error", "upstream read error")
	}
	var up map[string]any
	if json.Unmarshal(payload, &up) == nil {
		captureResponsesUsage(up, usage)
	}
	defer finishLog()
	logRow.Status = http.StatusOK
	return c.JSON(http.StatusOK, json.RawMessage(payload))
}

// injectResponsesInstructions ensures the Droid prompt leads instructions.
func injectResponsesInstructions(raw map[string]any, prompt string) []byte {
	out := make(map[string]any, len(raw))
	for k, v := range raw {
		out[k] = v
	}
	cur, _ := raw["instructions"].(string)
	if cur == "" {
		out["instructions"] = prompt
	} else {
		out["instructions"] = prompt + "\n\n" + cur
	}
	b, _ := json.Marshal(out)
	return b
}

// ---------------- Google passthrough ----------------

// GeneratePassthrough handles POST /v1/generate: a transparent proxy to the
// Google generateContent endpoint. Gemini models never participate in model
// interchange; the payload is forwarded exactly as received.
func (h *Handler) GeneratePassthrough(c echo.Context) error {
	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]any{"error": "cannot read body"})
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
	}
	modelID, _ := raw["model"].(string)
	if modelID == "" {
		return c.JSON(http.StatusBadRequest, map[string]any{"error": "model is required"})
	}
	m, ok := config.LookupModel(modelID)
	if !ok {
		return c.JSON(http.StatusNotFound, map[string]any{"error": fmt.Sprintf("model %q not found", modelID)})
	}
	if !m.Passthrough {
		return c.JSON(http.StatusBadRequest, map[string]any{"error": fmt.Sprintf("model %q is not a passthrough model", modelID)})
	}

	start := time.Now()
	clientKey := clientKeyFromContext(c)
	stream, _ := raw["stream"].(bool)

	ctx := c.Request().Context()
	if h.Cfg.RequestTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, h.Cfg.RequestTimeout)
		defer cancel()
	}
	key, ok := h.Router.NextKey(m.Tier)
	if !ok {
		return c.JSON(http.StatusServiceUnavailable, map[string]any{"error": "no upstream API key configured"})
	}

	resp, err := h.Client.Do(ctx, upstream.Request{Path: upstream.PathGoogle, APIKey: key.Key, Body: body, Stream: stream}, h.resolveProvider(m))
	if err != nil {
		return c.JSON(http.StatusBadGateway, map[string]any{"error": "upstream error: " + err.Error()})
	}
	defer resp.Body.Close()

	usage := &tokenUsage{}
	logRow := &store.RequestLog{
		ClientKey:      clientKey,
		RequestedModel: m.ID,
		UsedModel:      m.ID,
		Provider:       h.resolveProvider(m),
		Pool:           string(m.Tier),
		Stream:         stream,
	}
	defer func() {
		logRow.LatencyMs = int(time.Since(start).Milliseconds())
		usage.finalizeTTFT(start)
		usage.snapshot(logRow)
		_ = h.Store.InsertLog(*logRow)
	}()

	if resp.StatusCode != http.StatusOK {
		logRow.Status = resp.StatusCode
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		logRow.Error = truncateStr(string(payload), 500)
		return c.JSON(resp.StatusCode, json.RawMessage(payload))
	}
	logRow.Status = http.StatusOK

	// The Google generate endpoint frames its answers as SSE even when the
	// client did not ask for streaming, so decide by the upstream content type
	// rather than by the request flag alone.
	if stream || strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		c.Response().Header().Set(echo.HeaderContentType, "text/event-stream")
		c.Response().Header().Set("Cache-Control", "no-cache")
		c.Response().WriteHeader(http.StatusOK)
		return forwardSSEPreservingLines(&ttftWriter{c.Response(), usage}, resp.Body, func(payload []byte) {
			var ev map[string]any
			if json.Unmarshal(payload, &ev) == nil {
				if um, ok := ev["usageMetadata"].(map[string]any); ok {
					if p, ok := numToInt(um["promptTokenCount"]); ok {
						usage.Prompt = p
					}
					if c, ok := numToInt(um["candidatesTokenCount"]); ok {
						usage.Completion = c
					}
					if cr, ok := numToInt(um["cachedContentTokenCount"]); ok {
						usage.CacheRead = cr
					}
				}
			}
		}, usage)
	}

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return c.JSON(http.StatusBadGateway, map[string]any{"error": "upstream read error"})
	}
	var up map[string]any
	if json.Unmarshal(payload, &up) == nil {
		if um, ok := up["usageMetadata"].(map[string]any); ok {
			if p, ok := numToInt(um["promptTokenCount"]); ok {
				usage.Prompt = p
			}
			if c, ok := numToInt(um["candidatesTokenCount"]); ok {
				usage.Completion = c
			}
			if cr, ok := numToInt(um["cachedContentTokenCount"]); ok {
				usage.CacheRead = cr
			}
		}
	}
	return c.JSON(http.StatusOK, json.RawMessage(payload))
}

// CountTokens forwards to the Anthropic count_tokens endpoint.
func (h *Handler) CountTokens(c echo.Context) error {
	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return anthropicError(c, http.StatusBadRequest, "invalid_request_error", "cannot read body")
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return anthropicError(c, http.StatusBadRequest, "invalid_request_error", "invalid JSON body")
	}
	modelID, _ := raw["model"].(string)
	m, ok := config.LookupModel(modelID)
	if !ok {
		return anthropicError(c, http.StatusNotFound, "not_found_error", fmt.Sprintf("model %q not found", modelID))
	}
	key, ok := h.Router.NextKey(m.Tier)
	if !ok {
		return anthropicError(c, http.StatusServiceUnavailable, "api_error", "no upstream API key configured")
	}
	resp, err := h.Client.Do(c.Request().Context(), upstream.Request{
		Path:   upstream.PathMessages + "/count_tokens",
		APIKey: key.Key,
		Body:   body,
	}, "anthropic")
	if err != nil {
		return anthropicError(c, http.StatusBadGateway, "api_error", "upstream error: "+err.Error())
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return c.JSON(resp.StatusCode, json.RawMessage(payload))
}

// anthropicError renders an Anthropic-style error payload.
func anthropicError(c echo.Context, status int, typ, msg string) error {
	return c.JSON(status, map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    typ,
			"message": msg,
		},
	})
}

// ---------------- request-direction converters ----------------

// anthropicToCanonical translates an Anthropic Messages request into the
// canonical ChatRequest form used by the shared pipeline.
func anthropicToCanonical(raw map[string]any, modelID string) *upstream.ChatRequest {
	req := &upstream.ChatRequest{Model: modelID}
	if s, _ := raw["stream"].(bool); s {
		req.Stream = true
	}
	if mt, ok := numToInt(raw["max_tokens"]); ok {
		req.MaxTokens = mt
	}
	if t, ok := numToFloat(raw["temperature"]); ok {
		req.Temperature = &t
	}
	if p, ok := numToFloat(raw["top_p"]); ok {
		req.TopP = &p
	}
	// thinking -> reasoning_effort
	if th, ok := raw["thinking"].(map[string]any); ok {
		if budget, ok := numToInt(th["budget_tokens"]); ok {
			req.ReasoningEffort = budgetToEffort(budget)
		}
	}
	// system -> leading system message
	var msgs []upstream.ChatMessage
	switch sys := raw["system"].(type) {
	case string:
		if sys != "" {
			msgs = append(msgs, upstream.ChatMessage{Role: "system", Content: mustJSON(sys)})
		}
	case []any:
		var text string
		for _, b := range sys {
			if blk, ok := b.(map[string]any); ok && blk["type"] == "text" {
				if t, ok := blk["text"].(string); ok {
					text = joinText(text, t)
				}
			}
		}
		if text != "" {
			msgs = append(msgs, upstream.ChatMessage{Role: "system", Content: mustJSON(text)})
		}
	}
	// messages
	if arr, ok := raw["messages"].([]any); ok {
		for _, item := range arr {
			msg, _ := item.(map[string]any)
			if msg == nil {
				continue
			}
			role, _ := msg["role"].(string)
			switch content := msg["content"].(type) {
			case string:
				msgs = append(msgs, upstream.ChatMessage{Role: role, Content: mustJSON(content)})
			case []any:
				var parts []any
				var toolCalls []any
				var toolResults []upstream.ChatMessage
				for _, b := range content {
					blk, _ := b.(map[string]any)
					if blk == nil {
						continue
					}
					switch blk["type"] {
					case "text":
						if t, ok := blk["text"].(string); ok && t != "" {
							parts = append(parts, map[string]any{"type": "text", "text": t})
						}
					case "image":
						if src, ok := blk["source"].(map[string]any); ok {
							if data, ok := src["data"].(string); ok {
								media, _ := src["media_type"].(string)
								parts = append(parts, map[string]any{
									"type":      "image_url",
									"image_url": map[string]any{"url": "data:" + media + ";base64," + data},
								})
							}
						}
					case "tool_use":
						args, _ := json.Marshal(blk["input"])
						toolCalls = append(toolCalls, map[string]any{
							"id":   blk["id"],
							"type": "function",
							"function": map[string]any{
								"name":      blk["name"],
								"arguments": string(args),
							},
						})
					case "tool_result":
						tru, _ := blk["tool_use_id"].(string)
						text := ""
						switch rc := blk["content"].(type) {
						case string:
							text = rc
						case []any:
							for _, rb := range rc {
								if rblk, ok := rb.(map[string]any); ok && rblk["type"] == "text" {
									if t, ok := rblk["text"].(string); ok {
										text = joinText(text, t)
									}
								}
							}
						}
						toolResults = append(toolResults, upstream.ChatMessage{Role: "tool", ToolCallID: tru, Content: mustJSON(text)})
					}
				}
				cm := upstream.ChatMessage{Role: role}
				if len(parts) > 0 {
					cm.Content = mustJSON(parts)
				} else {
					cm.Content = mustJSON("")
				}
				if len(toolCalls) > 0 {
					cm.ToolCalls = mustJSON(toolCalls)
					cm.Content = nil
				}
				msgs = append(msgs, cm)
				msgs = append(msgs, toolResults...)
			default:
				msgs = append(msgs, upstream.ChatMessage{Role: role, Content: mustJSON("")})
			}
		}
	}
	req.Messages = msgs
	// tools
	if tools, ok := raw["tools"].([]any); ok {
		var out []any
		for _, t := range tools {
			tool, _ := t.(map[string]any)
			if tool == nil {
				continue
			}
			out = append(out, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        tool["name"],
					"description": tool["description"],
					"parameters":  tool["input_schema"],
				},
			})
		}
		if len(out) > 0 {
			req.Tools = mustJSON(out)
		}
	}
	return req
}

// responsesToCanonical translates an OpenAI Responses request into canonical
// ChatRequest form.
func responsesToCanonical(raw map[string]any, modelID string) *upstream.ChatRequest {
	req := &upstream.ChatRequest{Model: modelID}
	if s, _ := raw["stream"].(bool); s {
		req.Stream = true
	}
	if mt, ok := numToInt(raw["max_output_tokens"]); ok {
		req.MaxTokens = mt
	}
	if t, ok := numToFloat(raw["temperature"]); ok {
		req.Temperature = &t
	}
	if p, ok := numToFloat(raw["top_p"]); ok {
		req.TopP = &p
	}
	if r, ok := raw["reasoning"].(map[string]any); ok {
		if e, ok := r["effort"].(string); ok {
			req.ReasoningEffort = e
		}
	}
	var msgs []upstream.ChatMessage
	if instr, _ := raw["instructions"].(string); instr != "" {
		msgs = append(msgs, upstream.ChatMessage{Role: "system", Content: mustJSON(instr)})
	}
	switch input := raw["input"].(type) {
	case string:
		msgs = append(msgs, upstream.ChatMessage{Role: "user", Content: mustJSON(input)})
	case []any:
		for _, item := range input {
			it, _ := item.(map[string]any)
			if it == nil {
				continue
			}
			typ, _ := it["type"].(string)
			switch typ {
			case "message", "":
				role, _ := it["role"].(string)
				var parts []any
				switch c := it["content"].(type) {
				case string:
					parts = append(parts, map[string]any{"type": "text", "text": c})
				case []any:
					for _, p := range c {
						part, _ := p.(map[string]any)
						if part == nil {
							continue
						}
						switch part["type"] {
						case "input_text", "output_text", "text":
							parts = append(parts, map[string]any{"type": "text", "text": part["text"]})
						case "input_image":
							if u, ok := part["image_url"].(string); ok {
								parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": u}})
							}
						}
					}
				}
				if role == "" {
					role = "user"
				}
				msgs = append(msgs, upstream.ChatMessage{Role: role, Content: mustJSON(parts)})
			case "function_call":
				callID, _ := it["call_id"].(string)
				name, _ := it["name"].(string)
				args, _ := it["arguments"].(string)
				toolCalls := []any{map[string]any{
					"id":       callID,
					"type":     "function",
					"function": map[string]any{"name": name, "arguments": args},
				}}
				msgs = append(msgs, upstream.ChatMessage{Role: "assistant", ToolCalls: mustJSON(toolCalls)})
			case "function_call_output":
				callID, _ := it["call_id"].(string)
				out, _ := it["output"].(string)
				msgs = append(msgs, upstream.ChatMessage{Role: "tool", ToolCallID: callID, Content: mustJSON(out)})
			}
		}
	}
	req.Messages = msgs
	// tools
	if tools, ok := raw["tools"].([]any); ok {
		var out []any
		for _, t := range tools {
			tool, _ := t.(map[string]any)
			if tool == nil {
				continue
			}
			out = append(out, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        tool["name"],
					"description": tool["description"],
					"parameters":  tool["parameters"],
				},
			})
		}
		if len(out) > 0 {
			req.Tools = mustJSON(out)
		}
	}
	return req
}

// ---------------- small helpers ----------------

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func numToFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

func budgetToEffort(budget int) string {
	switch {
	case budget <= 4096:
		return "low"
	case budget <= 12288:
		return "medium"
	case budget <= 24576:
		return "high"
	default:
		return "max"
	}
}

func joinText(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "\n" + b
}
