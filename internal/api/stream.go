package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/Sibuxiangx/nicedroid/internal/config"
	"github.com/Sibuxiangx/nicedroid/internal/store"
)

// ttftWriter records the write time of the first response byte (time to first
// token) into the usage tracker.
type ttftWriter struct {
	w io.Writer
	u *tokenUsage
}

func (t *ttftWriter) Write(p []byte) (int, error) {
	t.u.markFirstWrite()
	return t.w.Write(p)
}

// forwardSSEPreservingLines relays an SSE stream line-by-line, preserving
// event: lines and blank separators verbatim, while optionally inspecting
// data payloads (for usage capture).
func forwardSSEPreservingLines(w io.Writer, r io.Reader, onData func([]byte), u *tokenUsage) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	first := true
	for sc.Scan() {
		line := sc.Bytes()
		if bytes.HasPrefix(line, []byte("data:")) {
			payload := bytes.TrimSpace(line[len("data:"):])
			if len(payload) > 0 && onData != nil && string(payload) != "[DONE]" {
				onData(payload)
			}
		}
		if _, err := w.Write(line); err != nil {
			return err
		}
		if _, err := w.Write([]byte("\n")); err != nil {
			return err
		}
		if first {
			u.markFirstWrite()
			first = false
		}
	}
	return sc.Err()
}

// streamResponse translates the upstream stream into the client's wire format
// and writes it through. Usage and TTFT are recorded on the shared trackers.
func (h *Handler) streamResponse(c echo.Context, resp *http.Response, used config.Model, requested string, outFmt clientFormat, logRow *store.RequestLog, usage *tokenUsage, start time.Time) error {
	w := c.Response()

	logRow.Status = http.StatusOK
	c.Response().Header().Set(echo.HeaderContentType, "text/event-stream")
	c.Response().Header().Set("Cache-Control", "no-cache")
	c.Response().WriteHeader(http.StatusOK)

	writeAll := func(frames [][]byte) error {
		for _, f := range frames {
			if len(f) == 0 {
				continue
			}
			usage.markFirstWrite()
			if _, err := w.Write(f); err != nil {
				return err
			}
			w.Flush()
		}
		return nil
	}

	// ---- native passthroughs (no conversion, frames preserved) ----

	// Anthropic client <- Anthropic upstream (with usage capture for caching stats).
	if used.Family == config.FamilyMessages && outFmt == clientFormatAnthropic {
		return forwardSSEPreservingLines(&ttftWriter{w, usage}, resp.Body, func(payload []byte) {
			var m map[string]any
			if json.Unmarshal(payload, &m) == nil {
				captureAnthropicUsage(m, usage)
			}
		}, usage)
	}

	// OpenAI client <- OpenAI upstream.
	if used.Family == config.FamilyChatCompletions && outFmt == clientFormatOpenAI {
		return forwardSSEPreservingLines(&ttftWriter{w, usage}, resp.Body, func(payload []byte) {
			var m map[string]any
			if json.Unmarshal(payload, &m) == nil {
				captureOpenAIUsage(m, usage)
			}
		}, usage)
	}

	// Responses client <- Responses upstream.
	if used.Family == config.FamilyResponses && outFmt == clientFormatResponses {
		return forwardSSEPreservingLines(&ttftWriter{w, usage}, resp.Body, func(payload []byte) {
			var m map[string]any
			if json.Unmarshal(payload, &m) == nil && m["type"] == "response.completed" {
				if r, ok := m["response"].(map[string]any); ok {
					captureResponsesUsage(r, usage)
				}
			}
		}, usage)
	}

	// ---- conversion paths ----

	switch {
	case used.Family == config.FamilyChatCompletions && outFmt == clientFormatAnthropic:
		s := newOpenAIToAnthropicStream(requested)
		if err := writeAll([][]byte{s.messageStart()}); err != nil {
			return err
		}
		err := upstreamParseSSE(resp.Body, func(ev map[string]any) error {
			captureOpenAIUsage(ev, usage)
			return writeAll(s.handle(ev))
		})
		if err != nil && err != io.EOF {
			return err
		}
		return writeAll(s.Finalize())

	case used.Family == config.FamilyMessages && outFmt == clientFormatOpenAI:
		s := newAnthropicToOpenAIStream(requested)
		err := upstreamParseSSE(resp.Body, func(ev map[string]any) error {
			captureAnthropicUsage(ev, usage)
			return writeAll(s.handleOne(ev))
		})
		if err != nil && err != io.EOF {
			return err
		}
		return writeAll(s.Finalize())

	case used.Family == config.FamilyResponses && outFmt == clientFormatOpenAI:
		s := newResponsesToOpenAIStream(requested)
		err := upstreamParseSSE(resp.Body, func(ev map[string]any) error {
			return writeAll(s.handleOne(ev))
		})
		if err != nil && err != io.EOF {
			return err
		}
		return writeAll(s.Finalize())

	case used.Family == config.FamilyChatCompletions && outFmt == clientFormatResponses:
		s := newOpenAIToResponsesStream(requested)
		err := upstreamParseSSE(resp.Body, func(ev map[string]any) error {
			captureOpenAIUsage(ev, usage)
			return writeAll(s.handle(ev))
		})
		if err != nil && err != io.EOF {
			return err
		}
		return writeAll(s.Finalize())

	case used.Family == config.FamilyMessages && outFmt == clientFormatResponses:
		conv := newAnthropicToOpenAIStream(requested)
		rs := newOpenAIToResponsesStream(requested)
		err := upstreamParseSSE(resp.Body, func(ev map[string]any) error {
			captureAnthropicUsage(ev, usage)
			var out [][]byte
			for _, f := range conv.handleOne(ev) {
				out = append(out, rs.handle(parseChunkFrame(f))...)
			}
			return writeAll(out)
		})
		if err != nil && err != io.EOF {
			return err
		}
		return writeAll(rs.Finalize())

	case used.Family == config.FamilyResponses && outFmt == clientFormatAnthropic:
		conv := newResponsesToOpenAIStream(requested)
		as := newOpenAIToAnthropicStream(requested)
		if err := writeAll([][]byte{as.messageStart()}); err != nil {
			return err
		}
		err := upstreamParseSSE(resp.Body, func(ev map[string]any) error {
			var out [][]byte
			for _, f := range conv.handleOne(ev) {
				out = append(out, as.handle(parseChunkFrame(f))...)
			}
			return writeAll(out)
		})
		if err != nil && err != io.EOF {
			return err
		}
		return writeAll(as.Finalize())

	default:
		// Unknown combination: raw copy.
		_, err := io.Copy(&ttftWriter{w, usage}, resp.Body)
		return err
	}
}

// bufferedResponse translates a full (non-streaming) upstream response into
// the client's wire format.
func (h *Handler) bufferedResponse(c echo.Context, resp *http.Response, used config.Model, requested string, outFmt clientFormat, logRow *store.RequestLog, usage *tokenUsage, start time.Time) error {
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		logRow.Status = http.StatusBadGateway
		logRow.Error = "upstream read error"
		return openAIError(c, http.StatusBadGateway, "server_error", "upstream read error")
	}
	var up map[string]any
	if err := json.Unmarshal(body, &up); err != nil {
		logRow.Status = http.StatusBadGateway
		return c.JSON(http.StatusBadGateway, json.RawMessage(body))
	}

	logRow.Status = http.StatusOK
	usage.markFirstWrite()

	switch {
	// Native passthroughs keep the payload as-is.
	case used.Family == config.FamilyChatCompletions && outFmt == clientFormatOpenAI:
		captureOpenAIUsage(up, usage)
		return c.JSON(http.StatusOK, json.RawMessage(body))
	case used.Family == config.FamilyMessages && outFmt == clientFormatAnthropic:
		captureAnthropicUsage(up, usage)
		return c.JSON(http.StatusOK, json.RawMessage(body))
	case used.Family == config.FamilyResponses && outFmt == clientFormatResponses:
		captureResponsesUsage(up, usage)
		return c.JSON(http.StatusOK, json.RawMessage(body))

	// Conversions.
	case used.Family == config.FamilyMessages && outFmt == clientFormatOpenAI:
		captureAnthropicUsage(up, usage)
		return c.JSON(http.StatusOK, anthropicToOpenAIResponse(up, requested))
	case used.Family == config.FamilyResponses && outFmt == clientFormatOpenAI:
		captureResponsesUsage(up, usage)
		return c.JSON(http.StatusOK, responsesToOpenAIResponse(up, requested))
	case used.Family == config.FamilyChatCompletions && outFmt == clientFormatAnthropic:
		captureOpenAIUsage(up, usage)
		return c.JSON(http.StatusOK, openAIToAnthropicResponse(up, requested))
	case used.Family == config.FamilyChatCompletions && outFmt == clientFormatResponses:
		captureOpenAIUsage(up, usage)
		return c.JSON(http.StatusOK, openAIToResponsesResponse(up, requested))
	case used.Family == config.FamilyMessages && outFmt == clientFormatResponses:
		captureAnthropicUsage(up, usage)
		asOpenAI := anthropicToOpenAIResponse(up, requested)
		return c.JSON(http.StatusOK, openAIToResponsesResponse(asOpenAI, requested))
	case used.Family == config.FamilyResponses && outFmt == clientFormatAnthropic:
		captureResponsesUsage(up, usage)
		asOpenAI := responsesToOpenAIResponse(up, requested)
		return c.JSON(http.StatusOK, openAIToAnthropicResponse(asOpenAI, requested))
	default:
		return c.JSON(http.StatusOK, json.RawMessage(body))
	}
}

// ---------------- usage capture helpers ----------------

// captureOpenAIUsage reads prompt/completion/cache fields from an OpenAI-shaped
// usage object. prompt_tokens includes cached tokens; cached_tokens lives in
// prompt_tokens_details.
func captureOpenAIUsage(v map[string]any, usage *tokenUsage) {
	u, _ := v["usage"].(map[string]any)
	if u == nil {
		return
	}
	if p, ok := numToInt(u["prompt_tokens"]); ok {
		usage.Prompt = p
	}
	if c, ok := numToInt(u["completion_tokens"]); ok {
		usage.Completion = c
	}
	if details, ok := u["prompt_tokens_details"].(map[string]any); ok {
		if cached, ok := numToInt(details["cached_tokens"]); ok {
			usage.CacheRead = cached
		}
	}
}

// captureAnthropicUsage reads input/output/cache fields from an Anthropic
// usage object. input_tokens excludes cache reads/writes, so the stored prompt
// total is input + cache_read + cache_creation.
func captureAnthropicUsage(v map[string]any, usage *tokenUsage) {
	u, _ := v["usage"].(map[string]any)
	if u == nil {
		// stream events nest usage under "message" for message_start
		if msg, ok := v["message"].(map[string]any); ok {
			u, _ = msg["usage"].(map[string]any)
		}
	}
	if u == nil {
		return
	}
	fresh, haveFresh := numToInt(u["input_tokens"])
	cacheRead, haveRead := numToInt(u["cache_read_input_tokens"])
	cacheWrite, haveWrite := numToInt(u["cache_creation_input_tokens"])
	if haveRead {
		usage.CacheRead = cacheRead
	}
	if haveWrite {
		usage.CacheWrite = cacheWrite
	}
	if haveFresh {
		usage.Prompt = fresh + usage.CacheRead + usage.CacheWrite
	}
	if c, ok := numToInt(u["output_tokens"]); ok && c > 0 {
		usage.Completion = c
	}
}

// captureResponsesUsage reads usage from a Responses API payload.
func captureResponsesUsage(v map[string]any, usage *tokenUsage) {
	u, _ := v["usage"].(map[string]any)
	if u == nil {
		return
	}
	if p, ok := numToInt(u["input_tokens"]); ok {
		usage.Prompt = p
	}
	if c, ok := numToInt(u["output_tokens"]); ok {
		usage.Completion = c
	}
	if details, ok := u["input_tokens_details"].(map[string]any); ok {
		if cached, ok := numToInt(details["cached_tokens"]); ok {
			usage.CacheRead = cached
		}
	}
}

// upstreamParseSSE decodes upstream SSE frames and passes each `data:` JSON
// payload to fn. "[DONE]" payloads terminate quietly.
func upstreamParseSSE(r io.Reader, fn func(map[string]any) error) error {
	return parseSSE(r, func(payload []byte) error {
		if string(payload) == "[DONE]" {
			return io.EOF
		}
		var m map[string]any
		if err := json.Unmarshal(payload, &m); err != nil {
			return nil // skip unparseable frames
		}
		return fn(m)
	})
}

// parseChunkFrame extracts a chunk map out of a raw SSE frame.
func parseChunkFrame(frame []byte) map[string]any {
	p := stripDataPrefix(frame)
	var m map[string]any
	_ = json.Unmarshal(p, &m)
	return m
}
