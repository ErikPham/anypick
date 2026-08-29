package proxy

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strings"
)

// Ports src/providers/opencode-proxy/context-budget.ts (ADR context budget).

const ContextWindowErrorCode = "context_length_exceeded"

const openAIContextWindowMessage = "Your input exceeds the context window of this model. Please adjust your input and try again."

var contextWindowPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)prompt is too long`),
	regexp.MustCompile(`(?i)input is too long for requested model`),
	regexp.MustCompile(`(?i)exceeds the context window`),
	regexp.MustCompile(`(?i)input token count.*exceeds the maximum`),
	regexp.MustCompile(`(?i)tokens in request more than max tokens allowed`),
	regexp.MustCompile(`(?i)maximum prompt length is \d+`),
	regexp.MustCompile(`(?i)reduce the length of (?:the )?messages`),
	regexp.MustCompile(`(?i)maximum context length is \d+ tokens`),
	regexp.MustCompile(`(?i)exceeds the limit of \d+`),
	regexp.MustCompile(`(?i)exceeds the available context size`),
	regexp.MustCompile(`(?i)greater than the context length`),
	regexp.MustCompile(`(?i)context window exceeds limit`),
	regexp.MustCompile(`(?i)exceeded model token limit`),
	regexp.MustCompile(`(?i)context[_ ]length[_ ]exceeded`),
	regexp.MustCompile(`(?i)request entity too large`),
	regexp.MustCompile(`(?i)context length is only \d+ tokens`),
	regexp.MustCompile(`(?i)input length.*exceeds.*context length`),
	regexp.MustCompile(`(?i)input length and max_tokens exceed context limit`),
	regexp.MustCompile(`(?i)prompt too long; exceeded (?:max )?context length`),
	regexp.MustCompile(`(?i)too large for model with \d+ maximum context length`),
	regexp.MustCompile(`(?i)model_context_window_exceeded`),
	regexp.MustCompile(`(?i)prompt exceeds max length`),
	regexp.MustCompile(`\[1261\]`),
}

// IsContextWindowError mirrors isContextWindowError in context-budget.ts.
func IsContextWindowError(raw string) bool {
	// Fast structured check: any code/type field equal to context_length_exceeded.
	if strings.Contains(raw, `"`+ContextWindowErrorCode+`"`) {
		// Verify it appears as a code/type value, not just substring of message.
		var payload map[string]any
		if json.Unmarshal([]byte(raw), &payload) == nil {
			if checkCode(payload) {
				return true
			}
		} else {
			// Raw is not JSON object — still treat bare code as overflow if present
			// (mirrors payload/code check). The raw contains the code string.
			return true
		}
	}
	msg := extractMessage(raw)
	for _, re := range contextWindowPatterns {
		if re.MatchString(msg) {
			return true
		}
	}
	return false
}

func checkCode(payload map[string]any) bool {
	if v, ok := payload["code"].(string); ok && v == ContextWindowErrorCode {
		return true
	}
	if v, ok := payload["type"].(string); ok && v == ContextWindowErrorCode {
		return true
	}
	if errObj, ok := payload["error"].(map[string]any); ok {
		if v, ok := errObj["code"].(string); ok && v == ContextWindowErrorCode {
			return true
		}
		if v, ok := errObj["type"].(string); ok && v == ContextWindowErrorCode {
			return true
		}
	}
	if resp, ok := payload["response"].(map[string]any); ok {
		if errObj, ok := resp["error"].(map[string]any); ok {
			if v, ok := errObj["code"].(string); ok && v == ContextWindowErrorCode {
				return true
			}
			if v, ok := errObj["type"].(string); ok && v == ContextWindowErrorCode {
				return true
			}
		}
	}
	return false
}

func extractMessage(raw string) string {
	var payload map[string]any
	if json.Unmarshal([]byte(raw), &payload) != nil {
		return raw
	}
	if errObj, ok := payload["error"].(map[string]any); ok {
		if m, ok := errObj["message"].(string); ok {
			return m
		}
	}
	if resp, ok := payload["response"].(map[string]any); ok {
		if errObj, ok := resp["error"].(map[string]any); ok {
			if m, ok := errObj["message"].(string); ok {
				return m
			}
		}
	}
	if m, ok := payload["message"].(string); ok {
		return m
	}
	return raw
}

func anthropicContextWindowMessage(raw string) string {
	msg := strings.TrimSpace(extractMessage(raw))
	if m := regexp.MustCompile(`(?i)prompt is too long:\s*(\d+)\s+tokens\s*>\s*(\d+)\s+maximum`).FindStringSubmatch(msg); m != nil {
		return "prompt is too long: " + m[1] + " tokens > " + m[2] + " maximum"
	}
	if msg != "" && len(msg) <= 800 {
		return "prompt is too long: input exceeds this model's context window maximum. " + msg
	}
	return "prompt is too long: input exceeds this model's context window maximum. Please use /compact or reduce the input and try again."
}

func newAnthropicRequestID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "req_" + strings.Repeat("0", 24)
	}
	return "req_" + hex.EncodeToString(b)
}

func anthropicContextWindowErrorBody(raw, requestID string) []byte {
	if strings.TrimSpace(requestID) == "" {
		// Try upstream request_id, then generate.
		var payload map[string]any
		if json.Unmarshal([]byte(raw), &payload) == nil {
			if v, ok := payload["request_id"].(string); ok && strings.TrimSpace(v) != "" {
				requestID = strings.TrimSpace(v)
			}
		}
		if strings.TrimSpace(requestID) == "" {
			requestID = newAnthropicRequestID()
		}
	}
	body := map[string]any{
		"type": "error",
		"error": map[string]string{
			"type":    "invalid_request_error",
			"code":    ContextWindowErrorCode,
			"message": anthropicContextWindowMessage(raw),
		},
		"request_id": requestID,
	}
	raw2, _ := json.Marshal(body)
	return raw2
}

func openAIContextWindowErrorBody() []byte {
	body := map[string]any{
		"error": map[string]string{
			"message": openAIContextWindowMessage,
			"type":    "invalid_request_error",
			"param":   "input",
			"code":    ContextWindowErrorCode,
		},
	}
	raw, _ := json.Marshal(body)
	return raw
}
