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
	if tools, ok := body["tools"].([]any); ok && len(tools) > 0 {
		if last, ok := tools[len(tools)-1].(map[string]any); ok {
			last["cache_control"] = cacheControl
			added++
		}
	}

	// System: the last block closes the gate prompt and any project context.
	if sys, ok := body["system"].([]any); ok && len(sys) > 0 {
		if last, ok := sys[len(sys)-1].(map[string]any); ok {
			last["cache_control"] = cacheControl
			added++
		}
	}

	// Messages: the last block of the last message closes the whole prefix.
	if added < maxCacheBreakpoints {
		if msgs, ok := body["messages"].([]any); ok && len(msgs) > 0 {
			if last, ok := msgs[len(msgs)-1].(map[string]any); ok {
				if markLastContentBlock(last) {
					added++
				}
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
		if len(content) == 0 {
			return false
		}
		last, ok := content[len(content)-1].(map[string]any)
		if !ok {
			return false
		}
		last["cache_control"] = cacheControl
		return true
	}
	return false
}

// hasCacheControl reports whether any block in the body already carries a
// cache_control marker.
func hasCacheControl(body map[string]any) bool {
	if tools, ok := body["tools"].([]any); ok && anyBlockMarked(tools) {
		return true
	}
	if sys, ok := body["system"].([]any); ok && anyBlockMarked(sys) {
		return true
	}
	if msgs, ok := body["messages"].([]any); ok {
		for _, m := range msgs {
			msg, ok := m.(map[string]any)
			if !ok {
				continue
			}
			switch content := msg["content"].(type) {
			case []any:
				if anyBlockMarked(content) {
					return true
				}
			case map[string]any:
				if _, ok := content["cache_control"]; ok {
					return true
				}
			}
		}
	}
	return false
}

func anyBlockMarked(blocks []any) bool {
	for _, b := range blocks {
		if blk, ok := b.(map[string]any); ok {
			if _, marked := blk["cache_control"]; marked {
				return true
			}
		}
	}
	return false
}
