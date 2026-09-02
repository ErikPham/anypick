package gemini

import (
	"encoding/json"
	"time"
)

// OpenAIResponsesToGemini translates an OpenAI /v1/responses request into a
// Gemini generateContent request. Ports openAIResponsesToGemini
// (translate-responses.ts).
func OpenAIResponsesToGemini(req OpenAIResponsesRequest, resolvedModel string) *GeminiGenerateRequest {
	messages := make([]OpenAIMessage, 0)
	if req.Instructions != "" {
		messages = append(messages, OpenAIMessage{Role: "system", Content: jsonRaw(req.Instructions)})
	}

	var pendingReasoning string
	var pendingSignature string
	attachReasoning := func(message OpenAIMessage) OpenAIMessage {
		if pendingReasoning != "" {
			message.ReasoningContent = pendingReasoning
		}
		if pendingSignature != "" {
			message.ReasoningSignature = pendingSignature
		}
		pendingReasoning = ""
		pendingSignature = ""
		return message
	}

	if req.Input != nil {
		var input string
		if json.Unmarshal(req.Input, &input) == nil {
			messages = append(messages, OpenAIMessage{Role: "user", Content: jsonRaw(input)})
		} else {
			var items []map[string]interface{}
			if json.Unmarshal(req.Input, &items) == nil {
				for _, item := range items {
					typ, _ := item["type"].(string)
					switch typ {
					case "reasoning":
						pendingReasoning = FlattenResponsesText(item["summary"])
						if ec, ok := item["encrypted_content"].(string); ok && ec != "" {
							pendingSignature = ec
						}
						continue
					case "function_call":
						id := asString(item["call_id"], asString(item["id"], "call_"+RandomID()))
						name := asString(item["name"], "tool")
						args := asString(item["arguments"], "{}")
						messages = append(messages, attachReasoning(OpenAIMessage{
							Role: "assistant",
							ToolCalls: []OpenAIToolCall{{
								ID:       id,
								Type:     "function",
								Function: struct {
									Name      string `json:"name"`
									Arguments string `json:"arguments"`
								}{Name: name, Arguments: args},
							}},
						}))
						continue
					case "function_call_output":
						if pendingReasoning != "" {
							messages = append(messages, attachReasoning(OpenAIMessage{Role: "assistant"}))
						}
						messages = append(messages, OpenAIMessage{
							Role:       "tool",
							ToolCallID: asString(item["call_id"], asString(item["id"], "call_"+RandomID())),
							Content:    jsonRaw(FlattenResponsesText(item["output"])),
						})
						continue
					}
					rawRole, _ := item["role"].(string)
					role := "user"
					switch rawRole {
					case "assistant":
						role = "assistant"
					case "system", "developer":
						role = "system"
					}
					message := OpenAIMessage{Role: role, Content: jsonRaw(FlattenResponsesText(item["content"]))}
					if role == "assistant" {
						messages = append(messages, attachReasoning(message))
					} else {
						messages = append(messages, message)
					}
				}
			}
		}
	}
	if pendingReasoning != "" {
		messages = append(messages, attachReasoning(OpenAIMessage{Role: "assistant"}))
	}
	if allSystem(messages) {
		messages = append(messages, OpenAIMessage{Role: "user", Content: jsonRaw("")})
	}

	chat := OpenAIChatRequest{
		Model:    resolvedModel,
		Messages: messages,
	}
	if req.MaxOutputTokens != nil {
		chat.MaxTokens = req.MaxOutputTokens
	}
	if req.Temperature != nil {
		chat.Temperature = req.Temperature
	}
	if req.TopP != nil {
		chat.TopP = req.TopP
	}
	if len(req.Reasoning) > 0 {
		chat.Reasoning = req.Reasoning
	}
	functionTools := make([]OpenAITool, 0)
	for _, raw := range req.Tools {
		var tool map[string]interface{}
		if json.Unmarshal(raw, &tool) != nil {
			continue
		}
		if tool["type"] != "function" {
			continue
		}
		name, _ := tool["name"].(string)
		if name == "" {
			continue
		}
		decl := OpenAITool{Type: "function"}
		decl.Function.Name = name
		if d, ok := tool["description"].(string); ok {
			decl.Function.Description = d
		}
		if params, ok := tool["parameters"].(map[string]interface{}); ok {
			rawParams, _ := json.Marshal(params)
			decl.Function.Parameters = rawParams
		} else {
			rawParams, _ := json.Marshal(map[string]interface{}{"type": "object", "properties": map[string]interface{}{}})
			decl.Function.Parameters = rawParams
		}
		functionTools = append(functionTools, decl)
	}
	if len(functionTools) > 0 {
		chat.Tools = functionTools
	}
	if len(req.ToolChoice) > 0 {
		chat.ToolChoice = req.ToolChoice
	}
	return OpenAIToGemini(chat, resolvedModel)
}

// GeminiToOpenAIResponses translates a Gemini generateContent response into an
// OpenAI /v1/responses object. Ports geminiToOpenAIResponses
// (translate-responses.ts).
func GeminiToOpenAIResponses(res *GeminiGenerateResponse, model string) *OpenAIResponsesResponse {
	responseID := "resp_" + RandomID()
	output := make([]OpenAIResponseOutputItem, 0)
	parts := candidateParts(firstCandidate(res))
	reasoningText := ""
	reasoningSignature := ""
	for _, part := range parts {
		if part.Thought && part.Text != "" {
			reasoningText += part.Text
			if reasoningSignature == "" {
				reasoningSignature = part.ThoughtSignature
			}
		}
	}
	if reasoningText != "" || reasoningSignature != "" {
		item := OpenAIResponseOutputItem{
			ID:    "rs_" + RandomID(),
			Type:  "reasoning",
			Status: "completed",
		}
		if reasoningText != "" {
			item.Summary = []OpenAIResponseSummary{{Type: "summary_text", Text: reasoningText}}
		}
		item.EncryptedContent = reasoningSignature
		output = append(output, item)
	}
	text := ""
	for _, part := range parts {
		if !part.Thought && part.Text != "" {
			text += part.Text
		}
	}
	if text != "" {
		output = append(output, OpenAIResponseOutputItem{
			ID:   "msg_" + RandomID(),
			Type: "message",
			Status: "completed",
			Role: "assistant",
			Content: []OpenAIResponseContent{{
				Type: "output_text", Text: text, Annotations: []interface{}{}, Logprobs: []interface{}{},
			}},
		})
	}
	for _, part := range parts {
		if part.FunctionCall == nil || part.FunctionCall.Name == "" {
			continue
		}
		if part.ThoughtSignature != "" {
			output = append(output, OpenAIResponseOutputItem{
				ID:              "rs_fc_" + RandomID(),
				Type:            "reasoning",
				Status:          "completed",
				EncryptedContent: part.ThoughtSignature,
			})
		}
		args, _ := json.Marshal(orEmptyMap(part.FunctionCall.Args))
		output = append(output, OpenAIResponseOutputItem{
			ID:       "fc_" + RandomID(),
			Type:     "function_call",
			Status:   "completed",
			CallID:   "call_" + RandomID(),
			Name:     part.FunctionCall.Name,
			Arguments: string(args),
		})
	}
	if len(output) == 0 {
		output = append(output, OpenAIResponseOutputItem{
			ID:   "msg_" + RandomID(),
			Type: "message",
			Status: "completed",
			Role: "assistant",
			Content: []OpenAIResponseContent{{
				Type: "output_text", Text: "", Annotations: []interface{}{}, Logprobs: []interface{}{},
			}},
		})
	}
	maxTokens := firstCandidate(res) != nil && firstCandidate(res).FinishReason == "MAX_TOKENS"
	inputTokens := 0
	outputTokens := 0
	reasoningTokens := 0
	totalTokens := 0
	if res.UsageMetadata != nil {
		inputTokens = derefInt(res.UsageMetadata.PromptTokenCount)
		outputTokens = derefInt(res.UsageMetadata.CandidatesTokenCount)
		reasoningTokens = derefInt(res.UsageMetadata.ThoughtsTokenCount)
		totalTokens = derefInt(res.UsageMetadata.TotalTokenCount)
		if totalTokens == 0 {
			totalTokens = inputTokens + outputTokens
		}
	}
	status := "completed"
	var incompleteDetails interface{}
	if maxTokens {
		status = "incomplete"
		incompleteDetails = map[string]string{"reason": "max_output_tokens"}
	}
	return &OpenAIResponsesResponse{
		ID:              responseID,
		Object:          "response",
		CreatedAt:       time.Now().Unix(),
		Status:          status,
		Error:           nil,
		IncompleteDetails: incompleteDetails,
		Model:           model,
		Output:          output,
		ParallelToolCalls: true,
		Usage: &OpenAIResponsesUsage{
			InputTokens:        inputTokens,
			InputTokensDetails: map[string]int{"cached_tokens": 0},
			OutputTokens:       outputTokens,
			OutputTokensDetails: map[string]int{"reasoning_tokens": reasoningTokens},
			TotalTokens:        totalTokens,
		},
	}
}

func allSystem(messages []OpenAIMessage) bool {
	if len(messages) == 0 {
		return false
	}
	for _, m := range messages {
		if m.Role != "system" {
			return false
		}
	}
	return true
}

func asString(v interface{}, fallback string) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return fallback
}

func jsonRaw(s string) json.RawMessage {
	raw, _ := json.Marshal(s)
	return raw
}
