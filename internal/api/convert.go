package api

import (
	"encoding/json"
	"fmt"
	"strings"
)

// This file implements the response-side bridge: translating upstream wire
// formats (Anthropic Messages, OpenAI Responses, OpenAI Chat Completions) into
// each other, for both streaming and non-streaming responses.

// ---------------- helpers ----------------

func rawJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// sseFrame builds one SSE frame: "event: X\ndata: Y\n\n" (event optional).
func sseFrame(event string, data []byte) []byte {
	var b []byte
	if event != "" {
		b = append(b, "event: "...)
		b = append(b, event...)
		b = append(b, '\n')
	}
	b = append(b, "data: "...)
	b = append(b, data...)
	b = append(b, '\n', '\n')
	return b
}

// ---------------- OpenAI chunk -> Anthropic events ----------------

// openAIToAnthropicStream converts an OpenAI chat.completion.chunk stream into
// Anthropic Messages SSE events.
type openAIToAnthropicStream struct {
	msgID       string
	model       string
	started     bool
	textStarted bool
	textIndex   int
	nextIndex   int
	usage       map[string]any
	finish      string
	toolIndexes map[string]int
}

func newOpenAIToAnthropicStream(model string) *openAIToAnthropicStream {
	return &openAIToAnthropicStream{model: model, msgID: "msg_" + randomID(), toolIndexes: map[string]int{}}
}

// messageStart emits the message_start event.
func (s *openAIToAnthropicStream) messageStart() []byte {
	s.started = true
	payload := map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            s.msgID,
			"type":          "message",
			"role":          "assistant",
			"model":         s.model,
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage":         map[string]any{"input_tokens": 0, "output_tokens": 0},
		},
	}
	return sseFrame("message_start", rawJSON(payload))
}

// handle processes one OpenAI chunk JSON and returns zero or more SSE frames.
func (s *openAIToAnthropicStream) handle(chunk map[string]any) [][]byte {
	var frames [][]byte
	if s.usage == nil {
		if u, ok := chunk["usage"].(map[string]any); ok && u != nil {
			s.usage = u
		}
	}
	choices, _ := chunk["choices"].([]any)
	if len(choices) == 0 {
		return nil
	}
	choice, _ := choices[0].(map[string]any)
	if choice == nil {
		return nil
	}
	delta, _ := choice["delta"].(map[string]any)
	if delta != nil {
		if s.textStarted == false {
			// First text delta: open a text content block.
			if txt, ok := delta["content"].(string); ok && txt != "" {
				s.textStarted = true
				s.textIndex = s.nextIndex
				s.nextIndex++
				frames = append(frames, sseFrame("content_block_start", rawJSON(map[string]any{
					"type":          "content_block_start",
					"index":         s.textIndex,
					"content_block": map[string]any{"type": "text", "text": ""},
				})))
			}
		}
		if txt, ok := delta["content"].(string); ok && txt != "" && s.textStarted {
			frames = append(frames, sseFrame("content_block_delta", rawJSON(map[string]any{
				"type":  "content_block_delta",
				"index": s.textIndex,
				"delta": map[string]any{"type": "text_delta", "text": txt},
			})))
		}
		// reasoning_content -> thinking block (Anthropic style)
		if rc, ok := delta["reasoning_content"].(string); ok && rc != "" {
			frames = append(frames, sseFrame("content_block_delta", rawJSON(map[string]any{
				"type":  "content_block_delta",
				"index": 0,
				"delta": map[string]any{"type": "thinking_delta", "thinking": rc},
			})))
		}
		// tool calls
		if tcs, ok := delta["tool_calls"].([]any); ok {
			for _, t := range tcs {
				tc, _ := t.(map[string]any)
				if tc == nil {
					continue
				}
				fn, _ := tc["function"].(map[string]any)
				if fn == nil {
					continue
				}
				if name, ok := fn["name"].(string); ok && name != "" {
					// New tool_use block. Reuse the upstream call id so the
					// client's tool_result correlates back exactly.
					callID, _ := tc["id"].(string)
					if callID == "" {
						callID = fmt.Sprintf("toolu_%s", randomID())
					}
					idx := s.nextIndex
					s.nextIndex++
					frames = append(frames, sseFrame("content_block_start", rawJSON(map[string]any{
						"type":  "content_block_start",
						"index": idx,
						"content_block": map[string]any{
							"type":  "tool_use",
							"id":    callID,
							"name":  name,
							"input": map[string]any{},
						},
					})))
					// track block index by tool call index
					s.toolIndexes[tcID(tc)] = idx
				}
				if args, ok := fn["arguments"].(string); ok && args != "" {
					idx := s.toolIndexes[tcID(tc)]
					frames = append(frames, sseFrame("content_block_delta", rawJSON(map[string]any{
						"type":  "content_block_delta",
						"index": idx,
						"delta": map[string]any{"type": "input_json_delta", "partial_json": args},
					})))
				}
			}
		}
	}
	if fr, ok := choice["finish_reason"].(string); ok && fr != "" {
		s.finish = fr
	}
	return frames
}

func tcID(tc map[string]any) string {
	if id, ok := tc["id"].(string); ok {
		return id
	}
	if idx, ok := tc["index"]; ok {
		return fmt.Sprintf("%v", idx)
	}
	return "0"
}

// finish_ maps OpenAI finish_reason to Anthropic stop_reason.
func mapFinishReason(fr string) string {
	switch fr {
	case "length":
		return "max_tokens"
	case "tool_calls", "function_call":
		return "tool_use"
	case "content_filter":
		return "end_turn"
	default:
		return "end_turn"
	}
}

// Finalize closes open blocks and emits message_delta + message_stop.
func (s *openAIToAnthropicStream) Finalize() [][]byte {
	var frames [][]byte
	if s.textStarted {
		frames = append(frames, sseFrame("content_block_stop", rawJSON(map[string]any{
			"type":  "content_block_stop",
			"index": s.textIndex,
		})))
	}
	for _, idx := range s.toolIndexes {
		frames = append(frames, sseFrame("content_block_stop", rawJSON(map[string]any{
			"type":  "content_block_stop",
			"index": idx,
		})))
	}
	usage := map[string]any{"input_tokens": 0, "output_tokens": 0}
	if s.usage != nil {
		if v, ok := s.usage["prompt_tokens"].(float64); ok {
			usage["input_tokens"] = int(v)
		}
		if v, ok := s.usage["completion_tokens"].(float64); ok {
			usage["output_tokens"] = int(v)
		}
	}
	stop := mapFinishReason(s.finish)
	frames = append(frames, sseFrame("message_delta", rawJSON(map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": stop, "stop_sequence": nil},
		"usage": usage,
	})))
	frames = append(frames, sseFrame("message_stop", rawJSON(map[string]any{"type": "message_stop"})))
	return frames
}

// toolIndexes tracks Anthropic block index per OpenAI tool call id.

// ---------------- Anthropic events -> OpenAI chunks ----------------

// anthropicToOpenAIStream converts Anthropic Messages SSE events into OpenAI
// chat.completion.chunk frames.
type anthropicToOpenAIStream struct {
	msgID     string
	model     string
	roleSent  bool
	usage     map[string]any
	finish    string
	textOpen  bool
	thinkOpen bool

	// tool-call bookkeeping: Anthropic blocks map onto OpenAI tool_call indexes
	blockIndex    int
	nextToolIndex int
	toolByBlock   map[int]int
}

func newAnthropicToOpenAIStream(model string) *anthropicToOpenAIStream {
	return &anthropicToOpenAIStream{
		model:       model,
		msgID:       "chatcmpl-" + randomID(),
		toolByBlock: map[int]int{},
	}
}

func (s *anthropicToOpenAIStream) roleChunk() []byte {
	s.roleSent = true
	return mustChunk(s.msgID, s.model, map[string]any{"role": "assistant", "content": ""}, nil, nil)
}

func mustChunk(id, model string, delta map[string]any, finish any, usage map[string]any) []byte {
	b, err := chunkFrame(id, model, delta, finish, usage)
	if err != nil {
		return nil
	}
	return b
}

// handleOne processes one raw Anthropic SSE event (payload map).
func (s *anthropicToOpenAIStream) handleOne(ev map[string]any) [][]byte {
	typ, _ := ev["type"].(string)
	var frames [][]byte
	switch typ {
	case "message_start":
		if msg, ok := ev["message"].(map[string]any); ok {
			if u, ok := msg["usage"].(map[string]any); ok {
				s.usage = u
			}
		}
		if !s.roleSent {
			frames = append(frames, s.roleChunk())
		}
	case "content_block_start":
		if idx, ok := numToInt(ev["index"]); ok {
			s.blockIndex = idx
		}
		if cb, ok := ev["content_block"].(map[string]any); ok {
			switch cb["type"] {
			case "thinking":
				s.thinkOpen = true
			case "text":
				s.textOpen = true
			case "tool_use":
				// Open a tool call with its id and name so clients can dispatch it.
				toolIdx := s.nextToolIndex
				s.nextToolIndex++
				s.toolByBlock[s.blockIndex] = toolIdx
				frames = append(frames, mustChunk(s.msgID, s.model, map[string]any{
					"tool_calls": []any{map[string]any{
						"index": toolIdx,
						"id":    cb["id"],
						"type":  "function",
						"function": map[string]any{
							"name":      cb["name"],
							"arguments": "",
						},
					}},
				}, nil, nil))
			}
		}
	case "content_block_delta":
		if delta, ok := ev["delta"].(map[string]any); ok {
			switch delta["type"] {
			case "text_delta":
				if txt, _ := delta["text"].(string); txt != "" {
					frames = append(frames, mustChunk(s.msgID, s.model, map[string]any{"content": txt}, nil, nil))
				}
			case "thinking_delta":
				if txt, _ := delta["thinking"].(string); txt != "" {
					frames = append(frames, mustChunk(s.msgID, s.model, map[string]any{"reasoning_content": txt}, nil, nil))
				}
			case "input_json_delta":
				if pj, _ := delta["partial_json"].(string); pj != "" {
					toolIdx := s.toolByBlock[s.blockIndex]
					frames = append(frames, mustChunk(s.msgID, s.model, map[string]any{
						"tool_calls": []any{map[string]any{
							"index":    toolIdx,
							"function": map[string]any{"arguments": pj},
						}},
					}, nil, nil))
				}
			}
		}
	case "message_delta":
		if d, ok := ev["delta"].(map[string]any); ok {
			if sr, _ := d["stop_reason"].(string); sr != "" {
				s.finish = mapAnthropicStop(sr)
			}
		}
		if u, ok := ev["usage"].(map[string]any); ok {
			if s.usage == nil {
				s.usage = map[string]any{}
			}
			for k, v := range u {
				s.usage[k] = v
			}
		}
	}
	return frames
}

func mapAnthropicStop(sr string) string {
	switch sr {
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	default:
		return "stop"
	}
}

// Finalize emits the closing chunk with usage and finish_reason.
func (s *anthropicToOpenAIStream) Finalize() [][]byte {
	usage := map[string]any(nil)
	if s.usage != nil {
		p, _ := numToInt(s.usage["input_tokens"])
		c, _ := numToInt(s.usage["output_tokens"])
		usage = map[string]any{
			"prompt_tokens":     p,
			"completion_tokens": c,
			"total_tokens":      p + c,
		}
	}
	fr := s.finish
	if fr == "" {
		fr = "stop"
	}
	frames := [][]byte{mustChunk(s.msgID, s.model, map[string]any{}, fr, usage)}
	frames = append(frames, []byte("data: [DONE]\n\n"))
	return frames
}

func numToInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil
	}
	return 0, false
}

// ---------------- Responses API -> OpenAI chunks ----------------

// responsesToOpenAIStream converts OpenAI Responses SSE events into chat
// completion chunks.
type responsesToOpenAIStream struct {
	msgID string
	model string
	sent  bool
	usage map[string]any
}

func newResponsesToOpenAIStream(model string) *responsesToOpenAIStream {
	return &responsesToOpenAIStream{msgID: "chatcmpl-" + randomID(), model: model}
}

func (s *responsesToOpenAIStream) handleOne(ev map[string]any) [][]byte {
	typ, _ := ev["type"].(string)
	var frames [][]byte
	switch typ {
	case "response.output_text.delta":
		if !s.sent {
			s.sent = true
			frames = append(frames, mustChunk(s.msgID, s.model, map[string]any{"role": "assistant", "content": ""}, nil, nil))
		}
		if d, _ := ev["delta"].(string); d != "" {
			frames = append(frames, mustChunk(s.msgID, s.model, map[string]any{"content": d}, nil, nil))
		}
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		if !s.sent {
			s.sent = true
			frames = append(frames, mustChunk(s.msgID, s.model, map[string]any{"role": "assistant", "content": ""}, nil, nil))
		}
		if d, _ := ev["delta"].(string); d != "" {
			frames = append(frames, mustChunk(s.msgID, s.model, map[string]any{"reasoning_content": d}, nil, nil))
		}
	case "response.completed":
		if resp, ok := ev["response"].(map[string]any); ok {
			if u, ok := resp["usage"].(map[string]any); ok {
				s.usage = u
			}
		}
	}
	return frames
}

// Finalize closes the stream.
func (s *responsesToOpenAIStream) Finalize() [][]byte {
	frames := [][]byte{}
	if !s.sent {
		frames = append(frames, mustChunk(s.msgID, s.model, map[string]any{"role": "assistant", "content": ""}, nil, nil))
	}
	var usage map[string]any
	if s.usage != nil {
		in, _ := numToInt(s.usage["input_tokens"])
		out, _ := numToInt(s.usage["output_tokens"])
		usage = map[string]any{
			"prompt_tokens":     in,
			"completion_tokens": out,
			"total_tokens":      in + out,
		}
	}
	frames = append(frames, mustChunk(s.msgID, s.model, map[string]any{}, "stop", usage))
	frames = append(frames, []byte("data: [DONE]\n\n"))
	return frames
}

// ---------------- non-streaming conversions ----------------

// anthropicToOpenAIResponse converts a full Anthropic message JSON into an
// OpenAI chat.completion JSON.
func anthropicToOpenAIResponse(msg map[string]any, model string) map[string]any {
	content := ""
	reasoning := ""
	var toolCalls []any
	if blocks, ok := msg["content"].([]any); ok {
		for _, b := range blocks {
			blk, _ := b.(map[string]any)
			if blk == nil {
				continue
			}
			switch blk["type"] {
			case "text":
				if t, _ := blk["text"].(string); t != "" {
					content += t
				}
			case "thinking":
				if t, _ := blk["thinking"].(string); t != "" {
					reasoning += t
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
			}
		}
	}
	finish := "stop"
	if sr, _ := msg["stop_reason"].(string); sr == "max_tokens" {
		finish = "length"
	} else if sr == "tool_use" {
		finish = "tool_calls"
	}
	message := map[string]any{"role": "assistant", "content": content}
	if reasoning != "" {
		message["reasoning_content"] = reasoning
	}
	if toolCalls != nil {
		message["tool_calls"] = toolCalls
	}
	out := map[string]any{
		"id":      "chatcmpl-" + randomID(),
		"object":  "chat.completion",
		"created": nowUnix(),
		"model":   model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       message,
			"finish_reason": finish,
		}},
	}
	if u, ok := msg["usage"].(map[string]any); ok {
		in, _ := numToInt(u["input_tokens"])
		out_, _ := numToInt(u["output_tokens"])
		out["usage"] = map[string]any{
			"prompt_tokens":     in,
			"completion_tokens": out_,
			"total_tokens":      in + out_,
		}
	}
	return out
}

// responsesToOpenAIResponse converts a full Responses API payload into an
// OpenAI chat.completion JSON.
func responsesToOpenAIResponse(resp map[string]any, model string) map[string]any {
	content := ""
	var toolCalls []any
	if out, ok := resp["output"].([]any); ok {
		for _, o := range out {
			item, _ := o.(map[string]any)
			if item == nil {
				continue
			}
			switch item["type"] {
			case "message":
				if parts, ok := item["content"].([]any); ok {
					for _, p := range parts {
						part, _ := p.(map[string]any)
						if part != nil && part["type"] == "output_text" {
							if t, _ := part["text"].(string); t != "" {
								content += t
							}
						}
					}
				}
			case "function_call":
				toolCalls = append(toolCalls, map[string]any{
					"id":   item["call_id"],
					"type": "function",
					"function": map[string]any{
						"name":      item["name"],
						"arguments": item["arguments"],
					},
				})
			}
		}
	}
	finish := "stop"
	if toolCalls != nil {
		finish = "tool_calls"
	} else if st, _ := resp["status"].(string); st == "incomplete" {
		finish = "length"
	}
	message := map[string]any{"role": "assistant", "content": content}
	if toolCalls != nil {
		message["tool_calls"] = toolCalls
	}
	out := map[string]any{
		"id":      "chatcmpl-" + randomID(),
		"object":  "chat.completion",
		"created": nowUnix(),
		"model":   model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       message,
			"finish_reason": finish,
		}},
	}
	if u, ok := resp["usage"].(map[string]any); ok {
		in, _ := numToInt(u["input_tokens"])
		out_, _ := numToInt(u["output_tokens"])
		out["usage"] = map[string]any{
			"prompt_tokens":     in,
			"completion_tokens": out_,
			"total_tokens":      in + out_,
		}
	}
	return out
}

// openAIToAnthropicResponse converts an OpenAI chat.completion JSON into an
// Anthropic message JSON.
func openAIToAnthropicResponse(comp map[string]any, model string) map[string]any {
	msg := map[string]any{}
	if choices, ok := comp["choices"].([]any); ok && len(choices) > 0 {
		if ch, ok := choices[0].(map[string]any); ok {
			if m, ok := ch["message"].(map[string]any); ok {
				msg = m
			}
		}
	}
	var blocks []any
	if rc, _ := msg["reasoning_content"].(string); rc != "" {
		blocks = append(blocks, map[string]any{"type": "thinking", "thinking": rc, "signature": ""})
	}
	if c, _ := msg["content"].(string); c != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": c})
	}
	if tcs, ok := msg["tool_calls"].([]any); ok {
		for _, t := range tcs {
			tc, _ := t.(map[string]any)
			if tc == nil {
				continue
			}
			fn, _ := tc["function"].(map[string]any)
			var input any = map[string]any{}
			if args, _ := fn["arguments"].(string); args != "" {
				_ = json.Unmarshal([]byte(args), &input)
			}
			blocks = append(blocks, map[string]any{
				"type":  "tool_use",
				"id":    tc["id"],
				"name":  fn["name"],
				"input": input,
			})
		}
	}
	if blocks == nil {
		blocks = []any{}
	}
	stop := "end_turn"
	if choices, ok := comp["choices"].([]any); ok && len(choices) > 0 {
		if ch, ok := choices[0].(map[string]any); ok {
			if fr, _ := ch["finish_reason"].(string); fr == "length" {
				stop = "max_tokens"
			} else if fr == "tool_calls" {
				stop = "tool_use"
			}
		}
	}
	out := map[string]any{
		"id":            "msg_" + randomID(),
		"type":          "message",
		"role":          "assistant",
		"model":         model,
		"content":       blocks,
		"stop_reason":   stop,
		"stop_sequence": nil,
	}
	if u, ok := comp["usage"].(map[string]any); ok {
		in, _ := numToInt(u["prompt_tokens"])
		out_, _ := numToInt(u["completion_tokens"])
		out["usage"] = map[string]any{"input_tokens": in, "output_tokens": out_}
	} else {
		out["usage"] = map[string]any{"input_tokens": 0, "output_tokens": 0}
	}
	return out
}

// openAIToResponsesResponse converts an OpenAI chat.completion JSON into a
// Responses API payload.
func openAIToResponsesResponse(comp map[string]any, model string) map[string]any {
	msg := map[string]any{}
	finish := "stop"
	if choices, ok := comp["choices"].([]any); ok && len(choices) > 0 {
		if ch, ok := choices[0].(map[string]any); ok {
			if m, ok := ch["message"].(map[string]any); ok {
				msg = m
			}
			if fr, _ := ch["finish_reason"].(string); fr != "" {
				finish = fr
			}
		}
	}
	text, _ := msg["content"].(string)
	output := []any{
		map[string]any{
			"type":   "message",
			"id":     "msg_" + randomID(),
			"role":   "assistant",
			"status": "completed",
			"content": []any{
				map[string]any{"type": "output_text", "text": text, "annotations": []any{}},
			},
		},
	}
	if tcs, ok := msg["tool_calls"].([]any); ok {
		for _, t := range tcs {
			tc, _ := t.(map[string]any)
			if tc == nil {
				continue
			}
			fn, _ := tc["function"].(map[string]any)
			output = append(output, map[string]any{
				"type":      "function_call",
				"id":        "fc_" + randomID(),
				"call_id":   tc["id"],
				"name":      fn["name"],
				"arguments": fn["arguments"],
				"status":    "completed",
			})
		}
	}
	status := "completed"
	if finish == "length" {
		status = "incomplete"
	}
	out := map[string]any{
		"id":         "resp_" + randomID(),
		"object":     "response",
		"created_at": nowUnix(),
		"status":     status,
		"model":      model,
		"output":     output,
	}
	if u, ok := comp["usage"].(map[string]any); ok {
		in, _ := numToInt(u["prompt_tokens"])
		out_, _ := numToInt(u["completion_tokens"])
		out["usage"] = map[string]any{"input_tokens": in, "output_tokens": out_, "total_tokens": in + out_}
	}
	return out
}

// ---------------- OpenAI chunk -> Responses events ----------------

// openAIToResponsesStream converts OpenAI chat.completion.chunk frames into
// Responses API SSE events (used when a /v1/responses client is routed to a
// chat-completions upstream, e.g. a Droid Core model). Tool calls surface as
// function_call items with streamed function_call_arguments deltas.
type openAIToResponsesStream struct {
	respID   string
	model    string
	created  bool
	itemID   string
	textOpen bool
	text     strings.Builder
	usage    map[string]any
	finish   string

	// output_index allocation: the first item to appear (message or tool call)
	// takes 0, the next takes 1, and so on.
	nextOutputIndex int
	msgIndex        int // -1 until the message item is opened
	fcItems         map[int]*responsesFuncItem
	fcOrder         []int
}

// responsesFuncItem tracks one streamed function_call item.
type responsesFuncItem struct {
	itemID      string
	callID      string
	name        string
	args        strings.Builder
	outputIndex int
	added       bool
}

func newOpenAIToResponsesStream(model string) *openAIToResponsesStream {
	return &openAIToResponsesStream{
		respID:   "resp_" + randomID(),
		model:    model,
		itemID:   "msg_" + randomID(),
		msgIndex: -1,
		fcItems:  map[int]*responsesFuncItem{},
	}
}

// handle processes one OpenAI chunk and emits Responses events.
func (s *openAIToResponsesStream) handle(chunk map[string]any) [][]byte {
	var frames [][]byte
	if chunk == nil {
		return nil
	}
	if u, ok := chunk["usage"].(map[string]any); ok && u != nil {
		s.usage = u
	}
	if !s.created {
		s.created = true
		frames = append(frames, sseFrame("response.created", rawJSON(map[string]any{
			"type": "response.created",
			"response": map[string]any{
				"id":         s.respID,
				"object":     "response",
				"created_at": nowUnix(),
				"status":     "in_progress",
				"model":      s.model,
				"output":     []any{},
			},
		})))
	}
	choices, _ := chunk["choices"].([]any)
	if len(choices) > 0 {
		if ch, ok := choices[0].(map[string]any); ok {
			if delta, ok := ch["delta"].(map[string]any); ok {
				if txt, _ := delta["content"].(string); txt != "" {
					if !s.textOpen {
						s.textOpen = true
						if s.msgIndex < 0 {
							s.msgIndex = s.nextOutputIndex
							s.nextOutputIndex++
						}
						// Open the assistant message item lazily so a
						// tool-only response never carries an empty message.
						frames = append(frames, sseFrame("response.output_item.added", rawJSON(map[string]any{
							"type":         "response.output_item.added",
							"output_index": s.msgIndex,
							"item": map[string]any{
								"type":    "message",
								"id":      s.itemID,
								"role":    "assistant",
								"status":  "in_progress",
								"content": []any{},
							},
						})))
						frames = append(frames, sseFrame("response.content_part.added", rawJSON(map[string]any{
							"type":          "response.content_part.added",
							"item_id":       s.itemID,
							"output_index":  s.msgIndex,
							"content_index": 0,
							"part":          map[string]any{"type": "output_text", "text": "", "annotations": []any{}},
						})))
					}
					s.text.WriteString(txt)
					frames = append(frames, sseFrame("response.output_text.delta", rawJSON(map[string]any{
						"type":          "response.output_text.delta",
						"item_id":       s.itemID,
						"output_index":  s.msgIndex,
						"content_index": 0,
						"delta":         txt,
					})))
				}
				if rc, _ := delta["reasoning_content"].(string); rc != "" {
					frames = append(frames, sseFrame("response.reasoning_summary_text.delta", rawJSON(map[string]any{
						"type":  "response.reasoning_summary_text.delta",
						"delta": rc,
					})))
				}
				// Tool calls -> function_call items with streamed arguments.
				if tcs, ok := delta["tool_calls"].([]any); ok {
					for _, t := range tcs {
						tc, _ := t.(map[string]any)
						if tc == nil {
							continue
						}
						idx := 0
						if v, ok := numToInt(tc["index"]); ok {
							idx = v
						}
						item := s.fcItems[idx]
						if item == nil {
							item = &responsesFuncItem{
								itemID:      "fc_" + randomID(),
								outputIndex: s.nextOutputIndex,
							}
							s.nextOutputIndex++
							s.fcItems[idx] = item
							s.fcOrder = append(s.fcOrder, idx)
						}
						fn, _ := tc["function"].(map[string]any)
						if id, ok := tc["id"].(string); ok && id != "" {
							item.callID = id
						}
						if fn != nil {
							if name, ok := fn["name"].(string); ok && name != "" {
								item.name = name
							}
							// Emit the item once the name is known.
							if !item.added && item.name != "" {
								item.added = true
								frames = append(frames, sseFrame("response.output_item.added", rawJSON(map[string]any{
									"type":         "response.output_item.added",
									"output_index": item.outputIndex,
									"item": map[string]any{
										"type":      "function_call",
										"id":        item.itemID,
										"call_id":   item.callID,
										"name":      item.name,
										"arguments": "",
										"status":    "in_progress",
									},
								})))
							}
							if args, ok := fn["arguments"].(string); ok && args != "" {
								item.args.WriteString(args)
								frames = append(frames, sseFrame("response.function_call_arguments.delta", rawJSON(map[string]any{
									"type":         "response.function_call_arguments.delta",
									"item_id":      item.itemID,
									"output_index": item.outputIndex,
									"delta":        args,
								})))
							}
						}
					}
				}
			}
			if fr, _ := ch["finish_reason"].(string); fr != "" {
				s.finish = fr
			}
		}
	}
	return frames
}

// Finalize emits terminal Responses events (completed) with the accumulated
// text and any function_call items, so clients that read the final response
// object get the full output.
func (s *openAIToResponsesStream) Finalize() [][]byte {
	var frames [][]byte
	if !s.created {
		frames = append(frames, s.handle(map[string]any{})...)
	}
	fullText := s.text.String()
	output := []any{}
	if s.textOpen {
		frames = append(frames, sseFrame("response.content_part.done", rawJSON(map[string]any{
			"type":          "response.content_part.done",
			"item_id":       s.itemID,
			"output_index":  s.msgIndex,
			"content_index": 0,
			"part":          map[string]any{"type": "output_text", "text": fullText, "annotations": []any{}},
		})))
		frames = append(frames, sseFrame("response.output_text.done", rawJSON(map[string]any{
			"type":          "response.output_text.done",
			"item_id":       s.itemID,
			"output_index":  s.msgIndex,
			"content_index": 0,
			"text":          fullText,
		})))
	}
	if s.textOpen || len(s.fcOrder) == 0 {
		msgIndex := s.msgIndex
		if msgIndex < 0 {
			msgIndex = 0
		}
		msgItem := map[string]any{
			"type":   "message",
			"id":     s.itemID,
			"role":   "assistant",
			"status": "completed",
			"content": []any{
				map[string]any{"type": "output_text", "text": fullText, "annotations": []any{}},
			},
		}
		frames = append(frames, sseFrame("response.output_item.done", rawJSON(map[string]any{
			"type":         "response.output_item.done",
			"output_index": msgIndex,
			"item":         msgItem,
		})))
		output = append(output, msgItem)
	}
	// Close out any streamed function_call items.
	for _, idx := range s.fcOrder {
		item := s.fcItems[idx]
		args := item.args.String()
		if !item.added {
			item.added = true
			frames = append(frames, sseFrame("response.output_item.added", rawJSON(map[string]any{
				"type":         "response.output_item.added",
				"output_index": item.outputIndex,
				"item": map[string]any{
					"type":      "function_call",
					"id":        item.itemID,
					"call_id":   item.callID,
					"name":      item.name,
					"arguments": "",
					"status":    "in_progress",
				},
			})))
		}
		frames = append(frames, sseFrame("response.function_call_arguments.done", rawJSON(map[string]any{
			"type":         "response.function_call_arguments.done",
			"item_id":      item.itemID,
			"output_index": item.outputIndex,
			"arguments":    args,
		})))
		fcItem := map[string]any{
			"type":      "function_call",
			"id":        item.itemID,
			"call_id":   item.callID,
			"name":      item.name,
			"arguments": args,
			"status":    "completed",
		}
		frames = append(frames, sseFrame("response.output_item.done", rawJSON(map[string]any{
			"type":         "response.output_item.done",
			"output_index": item.outputIndex,
			"item":         fcItem,
		})))
		output = append(output, fcItem)
	}
	status := "completed"
	if s.finish == "length" {
		status = "incomplete"
	}
	usage := map[string]any(nil)
	if s.usage != nil {
		p, _ := numToInt(s.usage["prompt_tokens"])
		c, _ := numToInt(s.usage["completion_tokens"])
		usage = map[string]any{"input_tokens": p, "output_tokens": c, "total_tokens": p + c}
	}
	resp := map[string]any{
		"id":         s.respID,
		"object":     "response",
		"created_at": nowUnix(),
		"status":     status,
		"model":      s.model,
		"output":     output,
	}
	if usage != nil {
		resp["usage"] = usage
	}
	frames = append(frames, sseFrame("response.completed", rawJSON(map[string]any{
		"type":     "response.completed",
		"response": resp,
	})))
	return frames
}

// ---------------- OpenAI chunk helper ----------------

func chunkFrame(id, model string, delta map[string]any, finish any, usage map[string]any) ([]byte, error) {
	obj := map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": nowUnix(),
		"model":   model,
		"choices": []any{map[string]any{
			"index":         0,
			"delta":         delta,
			"finish_reason": finish,
		}},
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

// stripDataPrefix removes a leading "data: " if present.
func stripDataPrefix(b []byte) []byte {
	s := strings.TrimSpace(string(b))
	if strings.HasPrefix(s, "data:") {
		s = strings.TrimSpace(s[len("data:"):])
	}
	return []byte(s)
}
