package gemini

import "encoding/json"

// mergeConsecutiveGeminiRoles folds adjacent turns that share a role.
// Gemini generateContent requires user and model to alternate, and one user
// turn must carry every functionResponse for the preceding functionCall turn.
func mergeConsecutiveGeminiRoles(contents []GeminiContent) []GeminiContent {
	if len(contents) < 2 {
		return contents
	}
	out := make([]GeminiContent, 0, len(contents))
	out = append(out, contents[0])
	for _, c := range contents[1:] {
		last := &out[len(out)-1]
		if last.Role == c.Role {
			last.Parts = append(last.Parts, c.Parts...)
			continue
		}
		out = append(out, c)
	}
	return out
}

// OpenAIToGemini translates an OpenAI /v1/chat/completions request into a Gemini
// generateContent request. Ports openAIToGemini (translate-chat.ts).
func OpenAIToGemini(req OpenAIChatRequest, resolvedModel string) *GeminiGenerateRequest {
	if resolvedModel == "" {
		resolvedModel = req.Model
	}
	contents := make([]GeminiContent, 0, len(req.Messages))
	toolNamesByID := map[string]string{}
	systemText := ""

	for _, msg := range req.Messages {
		switch msg.Role {
		case "system":
			t := MessageText(msg)
			if t != "" {
				if systemText != "" {
					systemText += "\n" + t
				} else {
					systemText = t
				}
			}
			continue
		case "tool":
			name := "tool"
			if msg.ToolCallID != "" {
				if n, ok := toolNamesByID[msg.ToolCallID]; ok {
					name = n
				}
			}
			if msg.Name != "" {
				name = msg.Name
			}
			contents = append(contents, GeminiContent{
				Role: "user",
				Parts: []GeminiPart{{
					FunctionResponse: &GeminiFunctionResponse{
						Name:     name,
						Response: map[string]interface{}{"result": MessageText(msg)},
					},
				}},
			})
			continue
		case "assistant":
			parts := make([]GeminiPart, 0)
			if msg.ReasoningContent != "" {
				part := GeminiPart{Text: msg.ReasoningContent, Thought: true}
				if msg.ReasoningSignature != "" {
					part.ThoughtSignature = msg.ReasoningSignature
				}
				parts = append(parts, part)
			}
			if text := MessageText(msg); text != "" {
				parts = append(parts, GeminiPart{Text: text})
			}
			attachedFallbackSignature := false
			for _, tc := range msg.ToolCalls {
				toolNamesByID[tc.ID] = tc.Function.Name
				fallbackSignature := ""
				if msg.ReasoningContent == "" && !attachedFallbackSignature {
					fallbackSignature = msg.ReasoningSignature
				}
				part := GeminiPart{}
				if tc.ThoughtSignature != "" {
					part.ThoughtSignature = tc.ThoughtSignature
				} else if fallbackSignature != "" {
					part.ThoughtSignature = fallbackSignature
				}
				args := SafeParseObject(tc.Function.Arguments)
				part.FunctionCall = &GeminiFunctionCall{Name: tc.Function.Name, Args: args}
				parts = append(parts, part)
				if fallbackSignature != "" {
					attachedFallbackSignature = true
				}
			}
			if len(parts) == 0 {
				parts = append(parts, GeminiPart{Text: ""})
			}
			contents = append(contents, GeminiContent{Role: "model", Parts: parts})
			continue
		default:
			contents = append(contents, GeminiContent{
				Role:  "user",
				Parts: []GeminiPart{{Text: orEmpty(MessageText(msg))}},
			})
		}
	}
	// generateContent rejects consecutive turns of the same role, and a
	// function-response turn must contain every response for the previous
	// function-call turn. Parallel tool calls arrive as separate messages.
	contents = mergeConsecutiveGeminiRoles(contents)

	// Gemini requires alternating user/model; ensure starts with user.
	if len(contents) == 0 {
		contents = append(contents, GeminiContent{Role: "user", Parts: []GeminiPart{{Text: ""}}})
	}
	if contents[0].Role == "model" {
		contents = append([]GeminiContent{{Role: "user", Parts: []GeminiPart{{Text: "(continue)"}}}}, contents...)
	}

	out := &GeminiGenerateRequest{Contents: contents}
	if systemText != "" {
		out.SystemInstruction = &GeminiContent{Parts: []GeminiPart{{Text: systemText}}}
	}

	gen := &GeminiGenerationConfig{}
	if req.MaxTokens != nil {
		gen.MaxOutputTokens = req.MaxTokens
	}
	if req.Temperature != nil {
		gen.Temperature = req.Temperature
	}
	if req.TopP != nil {
		gen.TopP = req.TopP
	}
	if stops := asStringSlice(req.Stop); len(stops) > 0 {
		gen.StopSequences = stops
	}
	out.GenerationConfig = gen
	ApplyGeminiThinkingConfig(out, ReasoningFromOpenAI(req), resolvedModel)
	if isEmptyGenerationConfig(gen) {
		out.GenerationConfig = nil
	}

	if len(req.Tools) > 0 {
		decls := make([]GeminiFunctionDeclaration, 0, len(req.Tools))
		for _, t := range req.Tools {
			if t.Type != "function" {
				continue
			}
			params := map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
			if len(t.Function.Parameters) > 0 {
				var decoded interface{}
				if json.Unmarshal(t.Function.Parameters, &decoded) == nil {
					params = SanitizeGeminiSchema(decoded)
				}
			}
			decls = append(decls, GeminiFunctionDeclaration{
				Name:        t.Function.Name,
				Description: t.Function.Description,
				Parameters:  params,
			})
		}
		if len(decls) > 0 {
			out.Tools = []GeminiTool{{FunctionDeclarations: decls}}
		}
	}
	return out
}

// GeminiToOpenAI translates a Gemini generateContent response into an OpenAI
// /v1/chat/completions response. Ports geminiToOpenAI (translate-chat.ts).
func GeminiToOpenAI(res *GeminiGenerateResponse, model string) *OpenAIChatResponse {
	if res.Error != nil {
		code := ""
		if res.Error.Code != nil {
			code = itoa(*res.Error.Code)
		}
		return &OpenAIChatResponse{Error: &OpenAIError{
			Message: orDefault(res.Error.Message, "Gemini error"),
			Type:    orDefault(res.Error.Status, "api_error"),
			Code:    code,
		}}
	}
	cand := firstCandidate(res)
	parts := candidateParts(cand)
	text := ""
	reasoning := ""
	reasoningSignature := ""
	tools := make([]OpenAIToolCall, 0)
	for _, p := range parts {
		if p.Text != "" {
			if p.Thought {
				reasoning += p.Text
				if reasoningSignature == "" {
					reasoningSignature = p.ThoughtSignature
				}
			} else {
				text += p.Text
			}
		}
		if p.FunctionCall != nil && p.FunctionCall.Name != "" {
			tc := OpenAIToolCall{ID: "call_" + RandomID(), Type: "function"}
			if p.ThoughtSignature != "" {
				tc.ThoughtSignature = p.ThoughtSignature
			}
			tc.Function.Name = p.FunctionCall.Name
			args, _ := json.Marshal(orEmptyMap(p.FunctionCall.Args))
			tc.Function.Arguments = string(args)
			tools = append(tools, tc)
		}
	}
	msg := OpenAIMessage{Role: "assistant", Content: jsonRawOrNull(text)}
	if reasoning != "" {
		msg.ReasoningContent = reasoning
	}
	sig := reasoningSignature
	if sig == "" && len(tools) > 0 {
		sig = tools[0].ThoughtSignature
	}
	if sig != "" {
		msg.ReasoningSignature = sig
	}
	if len(tools) > 0 {
		msg.ToolCalls = tools
	}
	hasTools := len(tools) > 0
	choice := OpenAIChatChoice{
		Index:        0,
		Message:      msg,
		FinishReason: MapGeminiFinish(finishReason(cand), hasTools),
	}
	usage := &OpenAIUsage{}
	if res.UsageMetadata != nil {
		usage.PromptTokens = derefInt(res.UsageMetadata.PromptTokenCount)
		usage.CompletionTokens = derefInt(res.UsageMetadata.CandidatesTokenCount)
		usage.TotalTokens = derefInt(res.UsageMetadata.TotalTokenCount)
	}
	return &OpenAIChatResponse{
		ID:      "chatcmpl_" + RandomID(),
		Object:  "chat.completion",
		Model:   model,
		Choices: []OpenAIChatChoice{choice},
		Usage:   usage,
	}
}

func orEmpty(s string) string { return s }

func orEmptyMap(m map[string]interface{}) map[string]interface{} {
	if m == nil {
		return map[string]interface{}{}
	}
	return m
}

func jsonRawOrNull(s string) json.RawMessage {
	if s == "" {
		return json.RawMessage("null")
	}
	raw, _ := json.Marshal(s)
	return raw
}

func firstCandidate(res *GeminiGenerateResponse) *GeminiCandidate {
	if res == nil || len(res.Candidates) == 0 {
		return nil
	}
	return &res.Candidates[0]
}

func candidateParts(cand *GeminiCandidate) []GeminiPart {
	if cand == nil || cand.Content == nil {
		return nil
	}
	return cand.Content.Parts
}

func finishReason(cand *GeminiCandidate) string {
	if cand == nil {
		return ""
	}
	return cand.FinishReason
}

func isEmptyGenerationConfig(g *GeminiGenerationConfig) bool {
	if g == nil {
		return true
	}
	return g.MaxOutputTokens == nil && g.Temperature == nil && g.TopP == nil &&
		len(g.StopSequences) == 0 && g.ThinkingConfig == nil
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
