package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func openaiUsage(prompt, cached, reasoning, completion int) map[string]any {
	u := map[string]any{
		"prompt_tokens":     float64(prompt),
		"completion_tokens": float64(completion),
		"total_tokens":      float64(prompt + completion),
		"prompt_tokens_details": map[string]any{
			"cached_tokens": float64(cached),
		},
	}
	if reasoning > 0 {
		u["completion_tokens_details"] = map[string]any{"reasoning_tokens": float64(reasoning)}
	}
	return u
}

// sseData pulls the JSON payload out of one "event: X\ndata: Y\n\n" frame.
func sseData(frame []byte) []byte {
	for _, line := range strings.Split(string(frame), "\n") {
		if rest, ok := strings.CutPrefix(line, "data: "); ok {
			return []byte(rest)
		}
	}
	return nil
}

func TestUsageFromAnthropicAddsCacheBackIntoPrompt(t *testing.T) {
	// Anthropic reports input_tokens without the cached portion; the internal
	// total has to include it or every downstream total under-counts.
	u := map[string]any{
		"input_tokens":                float64(52),
		"output_tokens":               float64(3),
		"cache_read_input_tokens":     float64(5440),
		"cache_creation_input_tokens": float64(120),
	}
	got := usageFromAnthropic(u)
	if got.Prompt != 52+5440+120 {
		t.Fatalf("Prompt = %d, want %d", got.Prompt, 52+5440+120)
	}
	if got.CacheRead != 5440 || got.CacheWrite != 120 {
		t.Fatalf("cache read/write = %d/%d, want 5440/120", got.CacheRead, got.CacheWrite)
	}
}

func TestUsageToAnthropicSplitsCachedOutOfInput(t *testing.T) {
	// The inverse of the above: Anthropic clients must see input_tokens as the
	// uncached portion only, with the cached portion in its own fields.
	got := usageToAnthropic(usageInfo{Prompt: 5492, Completion: 3, CacheRead: 5440})
	if got["input_tokens"] != 52 {
		t.Fatalf("input_tokens = %v, want 52", got["input_tokens"])
	}
	if got["cache_read_input_tokens"] != 5440 {
		t.Fatalf("cache_read_input_tokens = %v, want 5440", got["cache_read_input_tokens"])
	}
}

func TestUsageToResponsesExposesCachedTokens(t *testing.T) {
	// Regression: the Responses usage used to be rebuilt from prompt_tokens and
	// completion_tokens alone, so a client could not see whether caching fired.
	got := usageToResponses(usageInfo{Prompt: 5492, Completion: 3, CacheRead: 5440, Reasoning: 1})
	if got["input_tokens"] != 5492 || got["total_tokens"] != 5495 {
		t.Fatalf("input/total = %v/%v, want 5492/5495", got["input_tokens"], got["total_tokens"])
	}
	details, ok := got["input_tokens_details"].(map[string]any)
	if !ok || details["cached_tokens"] != 5440 {
		t.Fatalf("input_tokens_details = %v, want cached_tokens 5440", got["input_tokens_details"])
	}
	if got["output_tokens_details"].(map[string]any)["reasoning_tokens"] != 1 {
		t.Fatalf("output_tokens_details = %v, want reasoning_tokens 1", got["output_tokens_details"])
	}
}

func TestOpenAIToResponsesResponseKeepsCacheDetail(t *testing.T) {
	comp := map[string]any{
		"choices": []any{map[string]any{
			"message":       map[string]any{"role": "assistant", "content": "ok"},
			"finish_reason": "stop",
		}},
		"usage": openaiUsage(5492, 5440, 0, 3),
	}
	resp := openAIToResponsesResponse(comp, "glm-5.3-flash")
	u, ok := resp["usage"].(map[string]any)
	if !ok {
		t.Fatalf("usage missing: %v", resp)
	}
	details, ok := u["input_tokens_details"].(map[string]any)
	if !ok || details["cached_tokens"] != 5440 {
		t.Fatalf("input_tokens_details = %v, want cached_tokens 5440", u["input_tokens_details"])
	}
	if u["input_tokens"] != 5492 {
		t.Fatalf("input_tokens = %v, want 5492 (cached tokens included)", u["input_tokens"])
	}
}

func TestOpenAIToResponsesStreamFinalizeKeepsCacheDetail(t *testing.T) {
	s := newOpenAIToResponsesStream("glm-5.3-flash")
	s.handle(map[string]any{"usage": openaiUsage(5492, 5440, 0, 3)})
	var last map[string]any
	for _, f := range s.Finalize() {
		var m map[string]any
		if err := json.Unmarshal(sseData(f), &m); err != nil {
			continue
		}
		if m["type"] == "response.completed" {
			last = m
		}
	}
	if last == nil {
		t.Fatal("no response.completed frame")
	}
	resp := last["response"].(map[string]any)
	u := resp["usage"].(map[string]any)
	details := u["input_tokens_details"].(map[string]any)
	if got, _ := numToInt(details["cached_tokens"]); got != 5440 {
		t.Fatalf("cached_tokens = %v, want 5440", details["cached_tokens"])
	}
}

func TestOpenAIToAnthropicResponseSplitsCacheOutOfInput(t *testing.T) {
	comp := map[string]any{
		"choices": []any{map[string]any{
			"message":       map[string]any{"role": "assistant", "content": "ok"},
			"finish_reason": "stop",
		}},
		"usage": openaiUsage(5492, 5440, 0, 3),
	}
	msg := openAIToAnthropicResponse(comp, "glm-5.3-flash")
	u := msg["usage"].(map[string]any)
	if u["input_tokens"] != 52 {
		t.Fatalf("input_tokens = %v, want 52 (5492 minus 5440 cached)", u["input_tokens"])
	}
	if u["cache_read_input_tokens"] != 5440 {
		t.Fatalf("cache_read_input_tokens = %v, want 5440", u["cache_read_input_tokens"])
	}
}

func TestUsageRoundTripsThroughAnthropic(t *testing.T) {
	// anthropic -> openai -> anthropic must not drift, because the input_tokens
	// convention flips twice.
	orig := map[string]any{
		"input_tokens":            float64(52),
		"output_tokens":           float64(7),
		"cache_read_input_tokens": float64(5440),
	}
	viaOpenAI := usageToOpenAI(usageFromAnthropic(orig))
	back := usageToAnthropic(usageFromOpenAI(viaOpenAI))
	if back["input_tokens"] != 52 {
		t.Fatalf("input_tokens = %v, want 52", back["input_tokens"])
	}
	if back["cache_read_input_tokens"] != 5440 {
		t.Fatalf("cache_read_input_tokens = %v, want 5440", back["cache_read_input_tokens"])
	}
}

func TestUsageRoundTripsThroughResponses(t *testing.T) {
	orig := openaiUsage(5492, 5440, 2, 3)
	viaResponses := usageToResponses(usageFromOpenAI(orig))
	back := usageToOpenAI(usageFromResponses(viaResponses))
	if back["prompt_tokens"] != 5492 {
		t.Fatalf("prompt_tokens = %v, want 5492", back["prompt_tokens"])
	}
	details := back["prompt_tokens_details"].(map[string]any)
	if details["cached_tokens"] != 5440 {
		t.Fatalf("cached_tokens = %v, want 5440", details["cached_tokens"])
	}
	if back["completion_tokens_details"].(map[string]any)["reasoning_tokens"] != 2 {
		t.Fatalf("reasoning_tokens lost in round trip")
	}
}
