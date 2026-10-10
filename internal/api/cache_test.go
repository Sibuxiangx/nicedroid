package api

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/Sibuxiangx/nicedroid/internal/upstream"
)

// body builds a minimal Anthropic Messages request shape.
func body(messages []any, opts ...func(map[string]any)) map[string]any {
	b := map[string]any{"messages": messages}
	for _, o := range opts {
		o(b)
	}
	return b
}

func withSystem(blocks ...any) func(map[string]any) {
	return func(b map[string]any) { b["system"] = blocks }
}

func withTools(tools ...any) func(map[string]any) {
	return func(b map[string]any) { b["tools"] = tools }
}

func marked(m map[string]any) bool { _, ok := m["cache_control"]; return ok }

// TestInjectMarksLastOfEach covers the placement rule: a breakpoint caches
// everything up to and including its block, so each marker belongs on the last
// block of the region it closes.
func TestInjectMarksLastOfEach(t *testing.T) {
	tools := []any{
		map[string]any{"name": "a"},
		map[string]any{"name": "b"},
	}
	system := []any{
		map[string]any{"type": "text", "text": "gate"},
		map[string]any{"type": "text", "text": "project"},
	}
	msgs := []any{
		map[string]any{"role": "user", "content": "first"},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "text", "text": "middle"},
		}},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": "part one"},
			map[string]any{"type": "text", "text": "part two"},
		}},
	}
	b := body(msgs, withSystem(system...), withTools(tools...))

	if got := injectCacheBreakpoints(b); got != 3 {
		t.Fatalf("injected %d breakpoints, want 3", got)
	}

	if marked(tools[0].(map[string]any)) {
		t.Error("first tool was marked; only the last should be")
	}
	if !marked(tools[1].(map[string]any)) {
		t.Error("last tool was not marked")
	}
	if marked(system[0].(map[string]any)) {
		t.Error("first system block was marked; only the last should be")
	}
	if !marked(system[1].(map[string]any)) {
		t.Error("last system block was not marked")
	}
	// Only the final message is marked, on its final block.
	if _, ok := msgs[0].(map[string]any)["content"].(string); !ok {
		t.Error("earlier message content was rewritten")
	}
	last := msgs[2].(map[string]any)["content"].([]any)
	if marked(last[0].(map[string]any)) {
		t.Error("non-final block of the final message was marked")
	}
	if !marked(last[1].(map[string]any)) {
		t.Error("final block of the final message was not marked")
	}
}

// TestInjectPromotesStringContent: a plain string body has nowhere to carry the
// marker, so it becomes a single text block.
func TestInjectPromotesStringContent(t *testing.T) {
	b := body([]any{map[string]any{"role": "user", "content": "hello"}})
	if got := injectCacheBreakpoints(b); got != 1 {
		t.Fatalf("injected %d breakpoints, want 1", got)
	}
	blocks, ok := b["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if !ok {
		t.Fatal("string content was not promoted to a block array")
	}
	blk := blocks[0].(map[string]any)
	if blk["text"] != "hello" || blk["type"] != "text" || !marked(blk) {
		t.Errorf("promoted block is wrong: %v", blk)
	}
}

// TestInjectLeavesCallerMarkersAlone: an explicit cache_control means the caller
// manages its own reuse pattern, and second-guessing it would add breakpoints
// where they do not help.
func TestInjectLeavesCallerMarkersAlone(t *testing.T) {
	b := body(
		[]any{map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": "cached", "cache_control": map[string]any{"type": "ephemeral"}},
			map[string]any{"type": "text", "text": "fresh"},
		}}},
		withSystem(map[string]any{"type": "text", "text": "gate"}),
	)
	if got := injectCacheBreakpoints(b); got != 0 {
		t.Fatalf("injected %d breakpoints into a request that already had one, want 0", got)
	}
	sys := b["system"].([]any)[0].(map[string]any)
	if marked(sys) {
		t.Error("a system breakpoint was added despite the caller's own marker")
	}
}

// TestInjectSkipsEmptyContent: an empty block would be rejected upstream, so it
// must be left unmarked rather than turned into a broken request.
func TestInjectSkipsEmptyContent(t *testing.T) {
	for _, content := range []any{"", []any{}, nil} {
		b := body([]any{map[string]any{"role": "user", "content": content}})
		if got := injectCacheBreakpoints(b); got != 0 {
			t.Errorf("content %#v: injected %d breakpoints, want 0", content, got)
		}
	}
}

// TestInjectIsIdempotent guards the retry path: a provider failover rebuilds the
// request, and running the injection twice must not double up.
func TestInjectIsIdempotent(t *testing.T) {
	b := body([]any{map[string]any{"role": "user", "content": "hello"}})
	injectCacheBreakpoints(b)
	if got := injectCacheBreakpoints(b); got != 0 {
		t.Fatalf("second pass injected %d breakpoints, want 0", got)
	}
}

// The chat-completions path builds the Anthropic body in Go, where ToMessages
// makes system and messages []map[string]any rather than the []any that JSON
// decoding produces. A []any assertion silently failed on that, so no marker
// ever reached the upstream and every turn of a chat-completions client was
// billed as fresh input.
func TestInjectOnGoBuiltMessagesBody(t *testing.T) {
	req := &upstream.ChatRequest{Model: "claude-sonnet-5-5", MaxTokens: 32, Stream: true,
		Messages: []upstream.ChatMessage{
			{Role: "user", Content: json.RawMessage(strconv.Quote("say ok"))}}}
	body := upstream.ToMessages(req, "gate prompt", 8192)
	if hasCacheControl(body) {
		t.Fatal("a freshly built body must not look pre-marked")
	}
	n := injectCacheBreakpoints(body)
	if n != 2 {
		t.Fatalf("injected %d markers, want 2 (no tools in this request)", n)
	}
	sys := blocksOf(body["system"])
	if _, ok := sys[len(sys)-1]["cache_control"]; !ok {
		t.Error("system[-1] has no cache_control")
	}
	msgs := blocksOf(body["messages"])
	last := blocksOf(msgs[0]["content"])
	if _, ok := last[len(last)-1]["cache_control"]; !ok {
		t.Error("last message block has no cache_control")
	}
	if !hasCacheControl(body) {
		t.Error("hasCacheControl does not see a Go-built marker")
	}
}
