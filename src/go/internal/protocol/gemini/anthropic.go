package gemini

import (
	"encoding/json"

	"github.com/outof0/anypick/go/internal/protocol"
)

// AnthropicToGemini translates an Anthropic /v1/messages request into a Gemini
// generateContent request. Ports anthropicToGemini (translate-anthropic.ts):
// the Anthropic body is first converted to OpenAI via the shared engine
// translator, then to Gemini, with thinking history restored from the original.
func AnthropicToGemini(body []byte, resolvedModel string) (*GeminiGenerateRequest, string, error) {
	openaiJSON, err := protocol.TranslateRequest(body)
	if err != nil {
		return nil, "", err
	}
	var openai OpenAIChatRequest
	if err := json.Unmarshal(openaiJSON, &openai); err != nil {
		return nil, "", err
	}
	gemini := OpenAIToGemini(openai, resolvedModel)
	ApplyGeminiThinkingConfig(gemini, ReasoningFromAnthropicRaw(body), resolvedModel)
	RestoreAnthropicThinkingHistory(gemini, decodeAnthropicRequest(body))
	model := resolvedModel
	if model == "" {
		model = MapToGeminiModel(openai.Model)
	}
	return gemini, model, nil
}

// GeminiToAnthropic translates a Gemini generateContent response into an
// Anthropic /v1/messages response. Ports geminiToAnthropic (translate-anthropic.ts).
func GeminiToAnthropic(res *GeminiGenerateResponse, model string, inputTokensFallback int) *AnthropicMessageResponse {
	if res.Error != nil {
		errType := res.Error.Status
		if errType == "" {
			errType = "api_error"
		}
		msg := res.Error.Message
		if msg == "" {
			msg = "Gemini error"
		}
		code := ""
		if res.Error.Code != nil {
			code = itoa(*res.Error.Code)
		}
		// The error carrier fields live on the embedded anthropicErrorCarrier;
		// assign them directly so IsAnthropicError/AnthropicError report.
		out := &AnthropicMessageResponse{}
		out.Type = "error"
		out.errorType = errType
		out.errorMessage = msg
		out.errorCode = code
		return out
	}
	cand := firstCandidate(res)
	parts := candidateParts(cand)
	content := make([]AnthropicContentBlock, 0)
	for _, part := range parts {
		if part.Text != "" {
			if part.Thought {
				sig := part.ThoughtSignature
				if sig == "" {
					sig = ""
				}
				block := AnthropicContentBlock{
					Type:      "thinking",
					Thinking:  orDefault(part.Text, EmptyThoughtCarrier),
					Signature: sig,
				}
				content = append(content, block)
			} else {
				content = append(content, AnthropicContentBlock{Type: "text", Text: part.Text})
			}
		} else if part.Thought && part.ThoughtSignature != "" {
			content = append(content, AnthropicContentBlock{
				Type:      "thinking",
				Thinking:  EmptyThoughtCarrier,
				Signature: part.ThoughtSignature,
			})
		}
		if part.FunctionCall != nil && part.FunctionCall.Name != "" {
			if part.ThoughtSignature != "" {
				content = append(content, AnthropicContentBlock{
					Type:      "thinking",
					Thinking:  ToolSignatureCarrier,
					Signature: part.ThoughtSignature,
				})
			}
			block := AnthropicContentBlock{
				Type: "tool_use",
				ID:   "call_" + RandomID(),
				Name: part.FunctionCall.Name,
			}
			args := part.FunctionCall.Args
			if args == nil {
				args = map[string]interface{}{}
			}
			raw, _ := json.Marshal(args)
			block.Input = raw
			if part.ThoughtSignature != "" {
				block.ThoughtSignature = part.ThoughtSignature
			}
			content = append(content, block)
		}
	}
	if len(content) == 0 {
		content = append(content, AnthropicContentBlock{Type: "text", Text: ""})
	}
	hasTools := false
	for _, b := range content {
		if b.Type == "tool_use" {
			hasTools = true
			break
		}
	}
	usage := &AnthropicUsage{}
	if res.UsageMetadata != nil {
		usage.InputTokens = tokenCountOrFallback(res.UsageMetadata.PromptTokenCount, inputTokensFallback)
		usage.OutputTokens = derefInt(res.UsageMetadata.CandidatesTokenCount)
	}
	return &AnthropicMessageResponse{
		ID:         "msg_" + RandomID(),
		Type:       "message",
		Role:       "assistant",
		Model:      model,
		Content:    content,
		StopReason: MapGeminiFinishToAnthropic(finishReason(cand), hasTools),
		Usage:      usage,
	}
}

// IsAnthropicError reports whether the response is an error payload.
func (r *AnthropicMessageResponse) IsAnthropicError() bool {
	return r.Type == "error"
}

func (r *AnthropicMessageResponse) AnthropicError() (typ, message, code string) {
	return r.errorType, r.errorMessage, r.errorCode
}

func tokenCountOrFallback(value *int, fallback int) int {
	if value != nil && isFiniteFloat(float64(*value)) && *value > 0 {
		return *value
	}
	if fallback > 0 {
		return fallback
	}
	return 0
}

func isFiniteFloat(v float64) bool {
	return v == v && v != float64PositiveInf && v != float64NegativeInf
}

const (
	float64PositiveInf = 1.797693134862315708e+308
	float64NegativeInf = -1.797693134862315708e+308
)

// ReasoningFromAnthropicRaw extracts the reasoning intent from raw Anthropic
// JSON (the struct decoder reuses decodeAnthropicRequest).
func ReasoningFromAnthropicRaw(body []byte) ReasoningIntent {
	req := decodeAnthropicRequest(body)
	return ReasoningFromAnthropic(req)
}

func decodeAnthropicRequest(body []byte) AnthropicMessageRequest {
	var req AnthropicMessageRequest
	_ = json.Unmarshal(body, &req)
	return req
}
