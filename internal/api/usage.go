package api

// Usage accounting differs across the three wire formats in two ways that are
// easy to get wrong when translating: the field names, and whether the prompt
// total includes cached tokens. Anthropic's input_tokens EXCLUDES cache reads
// and writes; OpenAI's prompt_tokens and the Responses API's input_tokens both
// INCLUDE them. Every response-side conversion funnels the upstream usage
// through these helpers so cache and reasoning detail survive the trip —
// without them a /v1/responses client sees only input_tokens and cannot tell
// whether prompt caching engaged at all.

type usageInfo struct {
	Prompt     int // total prompt tokens, cached tokens included
	Completion int
	CacheRead  int
	CacheWrite int
	Reasoning  int
}

// usageFromOpenAI reads an OpenAI chat.completion usage object.
func usageFromOpenAI(u map[string]any) usageInfo {
	var info usageInfo
	info.Prompt, _ = numToInt(u["prompt_tokens"])
	info.Completion, _ = numToInt(u["completion_tokens"])
	if d, ok := u["prompt_tokens_details"].(map[string]any); ok {
		info.CacheRead, _ = numToInt(d["cached_tokens"])
	}
	if d, ok := u["completion_tokens_details"].(map[string]any); ok {
		info.Reasoning, _ = numToInt(d["reasoning_tokens"])
	}
	return info
}

// usageFromAnthropic reads an Anthropic Messages usage object and re-adds the
// cache tokens Anthropic excludes from input_tokens.
func usageFromAnthropic(u map[string]any) usageInfo {
	var info usageInfo
	fresh, _ := numToInt(u["input_tokens"])
	info.CacheRead, _ = numToInt(u["cache_read_input_tokens"])
	info.CacheWrite, _ = numToInt(u["cache_creation_input_tokens"])
	info.Prompt = fresh + info.CacheRead + info.CacheWrite
	info.Completion, _ = numToInt(u["output_tokens"])
	return info
}

// usageFromResponses reads a Responses API usage object.
func usageFromResponses(u map[string]any) usageInfo {
	var info usageInfo
	info.Prompt, _ = numToInt(u["input_tokens"])
	info.Completion, _ = numToInt(u["output_tokens"])
	if d, ok := u["input_tokens_details"].(map[string]any); ok {
		info.CacheRead, _ = numToInt(d["cached_tokens"])
	}
	if d, ok := u["output_tokens_details"].(map[string]any); ok {
		info.Reasoning, _ = numToInt(d["reasoning_tokens"])
	}
	return info
}

// usageToOpenAI renders the usage as an OpenAI chat.completion usage object.
func usageToOpenAI(in usageInfo) map[string]any {
	out := map[string]any{
		"prompt_tokens":         in.Prompt,
		"completion_tokens":     in.Completion,
		"total_tokens":          in.Prompt + in.Completion,
		"prompt_tokens_details": map[string]any{"cached_tokens": in.CacheRead},
	}
	if in.Reasoning > 0 {
		out["completion_tokens_details"] = map[string]any{"reasoning_tokens": in.Reasoning}
	}
	return out
}

// usageToAnthropic renders the usage as an Anthropic Messages usage object,
// with input_tokens holding only the uncached portion.
func usageToAnthropic(in usageInfo) map[string]any {
	return map[string]any{
		"input_tokens":                in.Prompt - in.CacheRead - in.CacheWrite,
		"output_tokens":               in.Completion,
		"cache_read_input_tokens":     in.CacheRead,
		"cache_creation_input_tokens": in.CacheWrite,
	}
}

// usageToResponses renders the usage as a Responses API usage object.
func usageToResponses(in usageInfo) map[string]any {
	out := map[string]any{
		"input_tokens":         in.Prompt,
		"output_tokens":        in.Completion,
		"total_tokens":         in.Prompt + in.Completion,
		"input_tokens_details": map[string]any{"cached_tokens": in.CacheRead},
	}
	if in.Reasoning > 0 {
		out["output_tokens_details"] = map[string]any{"reasoning_tokens": in.Reasoning}
	}
	return out
}
