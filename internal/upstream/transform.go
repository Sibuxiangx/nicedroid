package upstream

import (
	"encoding/json"
	"fmt"
)

// ChatMessage is the OpenAI chat message shape (the internal canonical form).
type ChatMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content,omitempty"`
	Name       string          `json:"name,omitempty"`
	ToolCalls  json.RawMessage `json:"tool_calls,omitempty"`   // OpenAI-style assistant tool calls
	ToolCallID string          `json:"tool_call_id,omitempty"` // OpenAI-style tool result correlation
}

// ChatRequest is an OpenAI chat/completions request.
type ChatRequest struct {
	Model           string          `json:"model"`
	Messages        []ChatMessage   `json:"messages"`
	Stream          bool            `json:"stream,omitempty"`
	MaxTokens       int             `json:"max_tokens,omitempty"`
	Temperature     *float64        `json:"temperature,omitempty"`
	TopP            *float64        `json:"top_p,omitempty"`
	ReasoningEffort string          `json:"reasoning_effort,omitempty"`
	Tools           json.RawMessage `json:"tools,omitempty"`
	ToolChoice      json.RawMessage `json:"tool_choice,omitempty"`
	Stop            json.RawMessage `json:"stop,omitempty"`
}

// ToMessages translates an OpenAI chat request into the Anthropic Messages
// envelope, prepending the required Droid system prompt.
func ToMessages(req *ChatRequest, systemPrompt string, maxTokensDefault int) map[string]any {
	var system []map[string]any
	if systemPrompt != "" {
		system = append(system, map[string]any{"type": "text", "text": systemPrompt})
	}
	msgs := make([]map[string]any, 0, len(req.Messages))
	for _, m := range req.Messages {
		switch m.Role {
		case "system", "developer":
			for _, block := range contentBlocks(m.Content) {
				if block["type"] == "text" {
					system = append(system, block)
				}
			}
		default:
			parts := contentBlocks(m.Content)
			if m.ToolCallID != "" {
				// OpenAI tool result -> Anthropic tool_result block (user turn)
				text := flattenText(m.Content)
				block := map[string]any{
					"type":        "tool_result",
					"tool_use_id": m.ToolCallID,
					"content":     text,
				}
				msgs = append(msgs, map[string]any{"role": "user", "content": []any{block}})
				continue
			}
			if len(m.ToolCalls) > 0 {
				var calls []map[string]any
				if err := json.Unmarshal(m.ToolCalls, &calls); err == nil {
					for _, tc := range calls {
						fn, _ := tc["function"].(map[string]any)
						name, _ := fn["name"].(string)
						argsStr, _ := fn["arguments"].(string)
						var input any = map[string]any{}
						if argsStr != "" {
							_ = json.Unmarshal([]byte(argsStr), &input)
						}
						parts = append(parts, map[string]any{
							"type":  "tool_use",
							"id":    tc["id"],
							"name":  name,
							"input": input,
						})
					}
				}
			}
			if len(parts) == 0 {
				parts = []map[string]any{{"type": "text", "text": ""}}
			}
			msgs = append(msgs, map[string]any{"role": m.Role, "content": parts})
		}
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = maxTokensDefault
	}
	out := map[string]any{
		"model":      req.Model,
		"max_tokens": maxTokens,
		"messages":   msgs,
	}
	if len(system) > 0 {
		out["system"] = system
	}
	if req.Stream {
		out["stream"] = true
	}
	if req.Temperature != nil {
		out["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		out["top_p"] = *req.TopP
	}
	if req.ReasoningEffort != "" {
		out["thinking"] = map[string]any{"type": "enabled", "budget_tokens": effortToBudget(req.ReasoningEffort)}
	}
	if len(req.Tools) > 0 {
		if tools := chatToolsToAnthropic(req.Tools); len(tools) > 0 {
			out["tools"] = tools
		}
	}
	if len(req.ToolChoice) > 0 {
		if tc := chatToolChoiceToAnthropic(req.ToolChoice); tc != nil {
			out["tool_choice"] = tc
		}
	}
	return out
}

// chatToolsToAnthropic converts OpenAI tool definitions into the Anthropic
// tools shape ({name, description, input_schema}). Both the standard nested
// form ({type:function, function:{...}}) and the flat form
// ({type:function, name, description, parameters}) are accepted; anything else
// passes through unchanged.
func chatToolsToAnthropic(raw json.RawMessage) []map[string]any {
	var tools []map[string]any
	if err := json.Unmarshal(raw, &tools); err != nil {
		return nil
	}
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		if fn, ok := t["function"].(map[string]any); ok {
			out = append(out, map[string]any{
				"name":         fn["name"],
				"description":  fn["description"],
				"input_schema": fn["parameters"],
			})
			continue
		}
		if name, ok := t["name"].(string); ok && name != "" {
			schema := t["parameters"]
			if schema == nil {
				schema = t["input_schema"]
			}
			out = append(out, map[string]any{
				"name":         name,
				"description":  t["description"],
				"input_schema": schema,
			})
			continue
		}
		// Already Anthropic-shaped.
		out = append(out, t)
	}
	return out
}

// chatToolChoiceToAnthropic maps OpenAI tool_choice onto the Anthropic shape.
// Accepts the nested form ({type:function, function:{name}}) and the flat form
// ({type:function, name}).
func chatToolChoiceToAnthropic(raw json.RawMessage) map[string]any {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		switch s {
		case "auto":
			return map[string]any{"type": "auto"}
		case "required":
			return map[string]any{"type": "any"}
		case "none":
			return nil
		}
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err == nil {
		if fn, ok := obj["function"].(map[string]any); ok {
			return map[string]any{"type": "tool", "name": fn["name"]}
		}
		if name, ok := obj["name"].(string); ok && name != "" {
			return map[string]any{"type": "tool", "name": name}
		}
	}
	return nil
}

func effortToBudget(effort string) int {
	switch effort {
	case "low", "minimal":
		return 4096
	case "medium":
		return 12288
	case "high":
		return 24576
	case "xhigh", "max":
		return 40960
	default:
		return 8192
	}
}

// contentBlocks normalizes heterogeneous content values into block maps.
func contentBlocks(raw json.RawMessage) []map[string]any {
	if len(raw) == 0 {
		return nil
	}
	// string form
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if s == "" {
			return nil
		}
		return []map[string]any{{"type": "text", "text": s}}
	}
	// array form
	var arr []map[string]any
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil
	}
	out := make([]map[string]any, 0, len(arr))
	for _, part := range arr {
		typ, _ := part["type"].(string)
		switch typ {
		case "text":
			out = append(out, map[string]any{"type": "text", "text": part["text"]})
		case "image_url":
			url := ""
			if iu, ok := part["image_url"].(map[string]any); ok {
				url, _ = iu["url"].(string)
			}
			out = append(out, imageBlock(url))
		case "image":
			out = append(out, part)
		default:
			out = append(out, part)
		}
	}
	return out
}

// imageBlock converts an OpenAI image_url to an Anthropic/base64 image block.
func imageBlock(url string) map[string]any {
	const prefix = "data:"
	if len(url) > len(prefix) && url[:len(prefix)] == prefix {
		rest := url[len(prefix):]
		mediaType := "image/png"
		data := rest
		for i := 0; i < len(rest); i++ {
			if rest[i] == ';' {
				mediaType = rest[:i]
				data = ""
				// skip to comma
				for j := i; j < len(rest); j++ {
					if rest[j] == ',' {
						data = rest[j+1:]
						break
					}
				}
				break
			}
		}
		return map[string]any{
			"type": "image",
			"source": map[string]any{
				"type":       "base64",
				"media_type": mediaType,
				"data":       data,
			},
		}
	}
	return map[string]any{
		"type":   "image",
		"source": map[string]any{"type": "url", "url": url},
	}
}

// ToChatCompletions translates the canonical request to the wire shape used by
// /chat/completions (fireworks and other open-weight providers).
func ToChatCompletions(req *ChatRequest, systemPrompt string) map[string]any {
	msgs := make([]map[string]any, 0, len(req.Messages)+1)
	if systemPrompt != "" {
		msgs = append(msgs, map[string]any{"role": "system", "content": systemPrompt})
	}
	for _, m := range req.Messages {
		msg := map[string]any{"role": m.Role, "content": normalizeContent(m.Content)}
		if len(m.ToolCalls) > 0 {
			msg["tool_calls"] = json.RawMessage(m.ToolCalls)
			if s, ok := msg["content"].(string); ok && s == "" {
				msg["content"] = ""
			}
		}
		if m.ToolCallID != "" {
			msg["tool_call_id"] = m.ToolCallID
			msg["content"] = flattenText(m.Content)
		}
		msgs = append(msgs, msg)
	}
	out := map[string]any{
		"model":    req.Model,
		"messages": msgs,
	}
	if req.MaxTokens > 0 {
		out["max_tokens"] = req.MaxTokens
	}
	if req.Stream {
		out["stream"] = true
		out["stream_options"] = map[string]any{"include_usage": true}
	}
	if req.Temperature != nil {
		out["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		out["top_p"] = *req.TopP
	}
	if req.ReasoningEffort != "" {
		out["reasoning_effort"] = req.ReasoningEffort
	}
	if len(req.Tools) > 0 {
		out["tools"] = json.RawMessage(req.Tools)
	}
	if len(req.ToolChoice) > 0 {
		out["tool_choice"] = json.RawMessage(req.ToolChoice)
	}
	return out
}

// flattenText extracts plain text from a content value.
func flattenText(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var arr []map[string]any
	if err := json.Unmarshal(raw, &arr); err != nil {
		return ""
	}
	out := ""
	for _, p := range arr {
		if t, ok := p["text"].(string); ok {
			out = joinNonEmpty(out, t)
		}
	}
	return out
}

// normalizeContent keeps string content as-is and collapses block arrays that
// contain only text into a plain string.
func normalizeContent(raw json.RawMessage) any {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var arr []map[string]any
	if err := json.Unmarshal(raw, &arr); err != nil {
		return ""
	}
	onlyText := true
	for _, p := range arr {
		if p["type"] != "text" {
			onlyText = false
			break
		}
	}
	if onlyText {
		var sb string
		for i, p := range arr {
			if i > 0 {
				sb += "\n"
			}
			if t, ok := p["text"].(string); ok {
				sb += t
			}
		}
		return sb
	}
	return arr
}

// ToResponses translates the canonical request into the /responses wire shape.
// instructions carries the Droid gate; input carries the conversation.
func ToResponses(req *ChatRequest, systemPrompt string) map[string]any {
	var instructions string
	input := make([]map[string]any, 0, len(req.Messages))
	for _, m := range req.Messages {
		switch m.Role {
		case "system", "developer":
			var s string
			if err := json.Unmarshal(m.Content, &s); err == nil {
				instructions = joinNonEmpty(instructions, s)
			} else {
				for _, b := range contentBlocks(m.Content) {
					if t, ok := b["text"].(string); ok {
						instructions = joinNonEmpty(instructions, t)
					}
				}
			}
		default:
			textType := "input_text"
			imageType := "input_image"
			if m.Role == "assistant" {
				textType = "output_text"
				imageType = "input_image"
			}
			var parts []map[string]any
			var s string
			if err := json.Unmarshal(m.Content, &s); err == nil {
				parts = append(parts, map[string]any{"type": textType, "text": s})
			} else {
				for _, b := range contentBlocks(m.Content) {
					switch b["type"] {
					case "text":
						parts = append(parts, map[string]any{"type": textType, "text": b["text"]})
					case "image":
						if src, ok := b["source"].(map[string]any); ok {
							if data, ok := src["data"].(string); ok {
								media, _ := src["media_type"].(string)
								parts = append(parts, map[string]any{
									"type":      imageType,
									"image_url": "data:" + media + ";base64," + data,
								})
							}
						}
					}
				}
			}
			if m.ToolCallID != "" {
				// OpenAI tool result -> function_call_output item
				input = append(input, map[string]any{
					"type":    "function_call_output",
					"call_id": m.ToolCallID,
					"output":  flattenText(m.Content),
				})
				continue
			}
			if len(parts) == 0 && len(m.ToolCalls) == 0 {
				parts = append(parts, map[string]any{"type": textType, "text": ""})
			}
			if len(parts) > 0 {
				input = append(input, map[string]any{"role": m.Role, "content": parts})
			}
			if len(m.ToolCalls) > 0 {
				var calls []map[string]any
				if err := json.Unmarshal(m.ToolCalls, &calls); err == nil {
					for _, tc := range calls {
						fn, _ := tc["function"].(map[string]any)
						input = append(input, map[string]any{
							"type":      "function_call",
							"call_id":   tc["id"],
							"name":      fn["name"],
							"arguments": fn["arguments"],
						})
					}
				}
			}
		}
	}
	instr := joinNonEmpty(systemPrompt, instructions)
	out := map[string]any{
		"model": req.Model,
		"input": input,
		"store": false,
	}
	if instr != "" {
		out["instructions"] = instr
	}
	if req.Stream {
		out["stream"] = true
	}
	if req.MaxTokens > 0 {
		out["max_output_tokens"] = req.MaxTokens
	}
	if req.Temperature != nil {
		out["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		out["top_p"] = *req.TopP
	}
	if req.ReasoningEffort != "" {
		out["reasoning"] = map[string]any{"effort": req.ReasoningEffort, "summary": "auto"}
	}
	if len(req.Tools) > 0 {
		out["tools"] = rewriteToolsForResponses(req.Tools)
	}
	return out
}

// rewriteToolsForResponses converts chat-completions tool definitions into the
// flatter Responses API shape.
func rewriteToolsForResponses(raw json.RawMessage) []map[string]any {
	var tools []map[string]any
	if err := json.Unmarshal(raw, &tools); err != nil {
		return nil
	}
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		// The Responses API requires type:function on every tool, which the
		// nested chat form carries one level up from the function object.
		typ, _ := t["type"].(string)
		if fn, ok := t["function"].(map[string]any); ok {
			delete(fn, "strict")
			if typ != "" {
				fn["type"] = typ
			}
			out = append(out, fn)
		} else {
			if typ != "" {
				t["type"] = typ
			}
			out = append(out, t)
		}
	}
	return out
}

func joinNonEmpty(parts ...string) string {
	out := ""
	for _, p := range parts {
		if p == "" {
			continue
		}
		if out == "" {
			out = p
		} else {
			out = out + "\n\n" + p
		}
	}
	return out
}

// --- response-side translation (upstream -> canonical OpenAI SSE) ---

// SSEChunk writes one OpenAI-style SSE data frame.
func SSEChunk(id, model string, delta map[string]any, finish any, usage map[string]any) ([]byte, error) {
	choice := map[string]any{
		"index":         0,
		"delta":         delta,
		"finish_reason": finish,
	}
	obj := map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": nowUnix(),
		"model":   model,
		"choices": []any{choice},
	}
	if usage != nil {
		obj["usage"] = usage
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	return append(append([]byte("data: "), b...), []byte("\n\n")...), nil
}

// ErrorJSON renders an OpenAI-style error body.
func ErrorJSON(status int, typ, msg string) []byte {
	b, _ := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": msg,
			"type":    typ,
			"code":    status,
		},
	})
	return b
}

func fmtErrorf(format string, args ...any) error { return fmt.Errorf(format, args...) }
