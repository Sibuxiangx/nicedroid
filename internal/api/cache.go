package api

// Prompt caching for Anthropic-family requests.
//
// Anthropic only caches a prefix when the request marks it with an explicit
// cache_control breakpoint; without one the same long conversation is billed as
// fresh input on every turn. That is fine for a one-shot call and ruinous for an
// agent loop, which is the main thing this proxy is used for — so the proxy adds
// the breakpoints itself, the same way coding agents do.
//
// A breakpoint caches everything up to and including the block it sits on, so
// the useful placement is "the end of what will not change again":
//
//	tools    → last tool definition (the tool schema is stable across a session)
//	system   → last system block (the gate prompt plus any project instructions)
//	messages → last block of the final message (caches the whole conversation)
//
// Marking the final message is what makes turn N+1 cheap: everything the client
// sent before is a prefix of what it sends now. Anthropic allows at most four
// breakpoints per request; this uses at most three.
//
// Requests that already carry a cache_control are left completely alone: the
// caller knows its own reuse pattern better than a heuristic does.

// cacheControl is the marker Anthropic expects on a block.
var cacheControl = map[string]any{"type": "ephemeral"}

// maxCacheBreakpoints is Anthropic's documented per-request limit.
const maxCacheBreakpoints = 4

// blocksOf flattens a body field into a list of block maps. A request decoded
// from JSON holds its arrays as []any, but ToMessages builds them in Go as
// []map[string]any — a type assertion to []any silently fails on the latter
// and left the chat-completions path with no markers at all.
func blocksOf(v any) []map[string]any {
	switch list := v.(type) {
	case []any:
		out := make([]map[string]any, 0, len(list))
		for _, item := range list {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	case []map[string]any:
		return list
	}
	return nil
}

// injectCacheBreakpoints adds prompt-cache markers to an Anthropic Messages
// body, in place, and reports how many it added.
//
// It is a no-op when the body already carries a marker, when the shape is not
// what it expects, or when the marker budget is exhausted.
func injectCacheBreakpoints(body map[string]any) int {
	if body == nil || hasCacheControl(body) {
		return 0
	}

	added := 0

	// Tools: the last definition closes the stable tool block.
	if tools := blocksOf(body["tools"]); len(tools) > 0 {
		tools[len(tools)-1]["cache_control"] = cacheControl
		added++
	}

	// System: the last block closes the gate prompt and any project context.
	if sys := blocksOf(body["system"]); len(sys) > 0 {
		sys[len(sys)-1]["cache_control"] = cacheControl
		added++
	}

	// Messages: the last block of the last message closes the whole prefix.
	if added < maxCacheBreakpoints {
		if msgs := blocksOf(body["messages"]); len(msgs) > 0 {
			if markLastContentBlock(msgs[len(msgs)-1]) {
				added++
			}
		}
	}

	return added
}

// markLastContentBlock marks the final content block of one message, promoting
// a plain string body to a single text block so it has somewhere to carry the
// marker. Messages whose content is empty are left alone — an empty block would
// be rejected upstream.
func markLastContentBlock(msg map[string]any) bool {
	switch content := msg["content"].(type) {
	case string:
		if content == "" {
			return false
		}
		msg["content"] = []any{map[string]any{
			"type":          "text",
			"text":          content,
			"cache_control": cacheControl,
		}}
		return true
	case []any:
		blocks := blocksOf(content)
		if len(blocks) == 0 {
			return false
		}
		blocks[len(blocks)-1]["cache_control"] = cacheControl
		return true
	case []map[string]any:
		if len(content) == 0 {
			return false
		}
		content[len(content)-1]["cache_control"] = cacheControl
		return true
	}
	return false
}

// hasCacheControl reports whether any block in the body already carries a
// cache_control marker.
func hasCacheControl(body map[string]any) bool {
	if anyBlockMarked(blocksOf(body["tools"])) {
		return true
	}
	if anyBlockMarked(blocksOf(body["system"])) {
		return true
	}
	for _, msg := range blocksOf(body["messages"]) {
		switch content := msg["content"].(type) {
		case []any:
			if anyBlockMarked(blocksOf(content)) {
				return true
			}
		case []map[string]any:
			if anyBlockMarked(content) {
				return true
			}
		case map[string]any:
			if _, ok := content["cache_control"]; ok {
				return true
			}
		case string:
			return false
		}
	}
	return false
}

func anyBlockMarked(blocks []map[string]any) bool {
	for _, blk := range blocks {
		if _, marked := blk["cache_control"]; marked {
			return true
		}
	}
	return false
}
