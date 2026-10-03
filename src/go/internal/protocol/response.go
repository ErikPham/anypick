package protocol

import (
	"encoding/json"
	"fmt"
)

// ---- response: openai → anthropic (non-streaming) ----

type openAIChoice struct {
	Message struct {
		Content   json.RawMessage  `json:"content"`
		ToolCalls []openAIToolCall `json:"tool_calls"`
	} `json:"message"`
	FinishReason string `json:"finish_reason"`
}

type openAIResponse struct {
	ID      string         `json:"id"`
	Model   string         `json:"model"`
	Choices []openAIChoice `json:"choices"`
	Usage   *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

type anthropicResponse struct {
	ID           string         `json:"id"`
	Type         string         `json:"type"`
	Role         string         `json:"role"`
	Model        string         `json:"model"`
	Content      []contentBlock `json:"content"`
	StopReason   *string        `json:"stop_reason"`
	StopSequence *string        `json:"stop_sequence"`
	Usage        struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// TranslateResponse converts an OpenAI chat-completion response body into an
// Anthropic /v1/messages response body.
func TranslateResponse(body []byte) ([]byte, error) {
	var resp openAIResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("invalid openai response: %w", err)
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("openai response has no choices")
	}
	choice := resp.Choices[0]

	out := anthropicResponse{
		ID:    "msg_" + resp.ID,
		Type:  "message",
		Role:  "assistant",
		Model: resp.Model,
	}
	if resp.Usage != nil {
		out.Usage.InputTokens = resp.Usage.PromptTokens
		out.Usage.OutputTokens = resp.Usage.CompletionTokens
	}
	out.StopReason = mapFinishReason(choice.FinishReason)

	var texts []string
	if len(choice.Message.Content) > 0 && string(choice.Message.Content) != "null" {
		var content string
		if json.Unmarshal(choice.Message.Content, &content) == nil {
			texts = append(texts, content)
		} else {
			var parts []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if json.Unmarshal(choice.Message.Content, &parts) == nil {
				for _, p := range parts {
					if p.Type == "text" {
						texts = append(texts, p.Text)
					}
				}
			}
		}
	}
	for _, t := range texts {
		if t == "" {
			continue
		}
		out.Content = append(out.Content, contentBlock{Type: "text", Text: t})
	}

	for _, call := range choice.Message.ToolCalls {
		input := json.RawMessage(call.Function.Arguments)
		if !json.Valid(input) || len(input) == 0 {
			input = json.RawMessage("{}")
		}
		out.Content = append(out.Content, contentBlock{
			Type: "tool_use", ID: call.ID, Name: call.Function.Name, Input: input,
		})
	}
	if out.StopReason == nil {
		sr := "end_turn"
		out.StopReason = &sr
	}
	return json.Marshal(out)
}

func mapFinishReason(finish string) *string {
	switch finish {
	case "", "stop":
		s := "end_turn"
		return &s
	case "length":
		s := "max_tokens"
		return &s
	case "tool_calls", "function_call":
		s := "tool_use"
		return &s
	case "content_filter":
		s := "refusal"
		return &s
	default:
		s := "end_turn"
		return &s
	}
}
