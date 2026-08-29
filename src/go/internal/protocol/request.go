// Package protocol translates between the Anthropic Messages API and the
// OpenAI Chat Completions API — request bodies, non-streaming responses, and
// Server-Sent-Event streams in both directions' most common shapes.
//
// The translation is deliberately conservative: fields it does not understand
// are dropped, never invented.
package protocol

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ---- request: anthropic → openai ----

type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	Source    *struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
	} `json:"source,omitempty"`
}

type anthropicMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type anthropicRequest struct {
	Model         string             `json:"model"`
	MaxTokens     int                `json:"max_tokens"`
	System        json.RawMessage    `json:"system,omitempty"`
	Messages      []anthropicMessage `json:"messages"`
	StopSequences []string           `json:"stop_sequences,omitempty"`
	Temperature   *float64           `json:"temperature,omitempty"`
	TopP          *float64           `json:"top_p,omitempty"`
	Stream        bool               `json:"stream,omitempty"`
	Tools         []anthropicTool    `json:"tools,omitempty"`
	ToolChoice    *struct {
		Type string `json:"type"`
		Name string `json:"name,omitempty"`
	} `json:"tool_choice,omitempty"`
}

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    json.RawMessage  `json:"content,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	Name       string           `json:"name,omitempty"`
}

type openAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// TranslateRequest converts an Anthropic /v1/messages body into an OpenAI
// /chat/completions body.
func TranslateRequest(body []byte) ([]byte, error) {
	var req anthropicRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("invalid anthropic request: %w", err)
	}

	out := map[string]interface{}{
		"model":    req.Model,
		"messages": []openAIMessage{},
	}
	if req.MaxTokens > 0 {
		out["max_tokens"] = req.MaxTokens
	}
	if req.Temperature != nil {
		out["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		out["top_p"] = *req.TopP
	}
	if len(req.StopSequences) > 0 {
		out["stop"] = req.StopSequences
	}
	if req.Stream {
		out["stream"] = true
	}

	messages := make([]openAIMessage, 0, len(req.Messages)+2)

	// System may be a plain string or an array of text blocks.
	if sys := joinSystemText(req.System); sys != "" {
		messages = append(messages, openAIMessage{
			Role:    "system",
			Content: jsonString(sys),
		})
	}

	for _, msg := range req.Messages {
		blocks := decodeBlocks(msg.Content)
		switch msg.Role {
		case "user":
			userMsgs := userBlocksToOpenAI(blocks)
			messages = append(messages, userMsgs...)
		case "assistant":
			messages = append(messages, assistantBlocksToOpenAI(blocks)...)
		default:
			return nil, fmt.Errorf("unsupported message role %q", msg.Role)
		}
	}
	out["messages"] = messages

	if len(req.Tools) > 0 {
		tools := make([]map[string]interface{}, 0, len(req.Tools))
		for _, t := range req.Tools {
			tools = append(tools, map[string]interface{}{
				"type": "function",
				"function": map[string]interface{}{
					"name":        t.Name,
					"description": t.Description,
					"parameters":  t.InputSchema,
				},
			})
		}
		out["tools"] = tools
	}
	if req.ToolChoice != nil {
		switch req.ToolChoice.Type {
		case "any":
			out["tool_choice"] = "required"
		case "auto":
			out["tool_choice"] = "auto"
		case "none":
			out["tool_choice"] = "none"
		case "tool":
			out["tool_choice"] = map[string]interface{}{
				"type":     "function",
				"function": map[string]string{"name": req.ToolChoice.Name},
			}
		}
	}

	return json.Marshal(out)
}

func joinSystemText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []contentBlock
	if json.Unmarshal(raw, &blocks) == nil {
		parts := make([]string, 0, len(blocks))
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func decodeBlocks(content json.RawMessage) []contentBlock {
	var s string
	if len(content) > 0 && json.Unmarshal(content, &s) == nil {
		return []contentBlock{{Type: "text", Text: s}}
	}
	var blocks []contentBlock
	_ = json.Unmarshal(content, &blocks)
	return blocks
}

func userBlocksToOpenAI(blocks []contentBlock) []openAIMessage {
	// Group consecutive tool_results into tool-role messages; everything else
	// merges into one user message (string when only text).
	var out []openAIMessage
	type part struct {
		text string
		img  *contentBlock
	}
	var parts []part
	flush := func() {
		if len(parts) == 0 {
			return
		}
		allText := true
		for _, p := range parts {
			if p.img != nil {
				allText = false
			}
		}
		if allText {
			sb := strings.Builder{}
			for _, p := range parts {
				sb.WriteString(p.text)
			}
			out = append(out, openAIMessage{Role: "user", Content: jsonString(sb.String())})
		} else {
			array := []map[string]interface{}{}
			for _, p := range parts {
				if p.img != nil {
					array = append(array, map[string]interface{}{
						"type":      "image_url",
						"image_url": map[string]string{"url": imageDataURL(p.img.Source)},
					})
				} else {
					array = append(array, map[string]interface{}{
						"type": "text", "text": p.text,
					})
				}
			}
			raw, _ := json.Marshal(array)
			out = append(out, openAIMessage{Role: "user", Content: raw})
		}
		parts = parts[:0]
	}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			parts = append(parts, part{text: b.Text})
		case "image":
			if b.Source != nil {
				parts = append(parts, part{img: &b})
			}
		case "tool_result":
			flush()
			out = append(out, openAIMessage{
				Role:       "tool",
				ToolCallID: b.ToolUseID,
				Content:    jsonString(toolResultText(b)),
			})
		default:
			// Unknown block types are dropped, never invented.
		}
	}
	flush()
	return out
}

func toolResultText(b contentBlock) string {
	var s string
	if len(b.Content) > 0 && json.Unmarshal(b.Content, &s) == nil {
		return s
	}
	var blocks []contentBlock
	if json.Unmarshal(b.Content, &blocks) == nil {
		texts := make([]string, 0, len(blocks))
		for _, inner := range blocks {
			if inner.Type == "text" {
				texts = append(texts, inner.Text)
			}
		}
		return strings.Join(texts, "\n")
	}
	return ""
}

func imageDataURL(src *struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}) string {
	if src == nil {
		return ""
	}
	return "data:" + src.MediaType + ";base64," + src.Data
}

func assistantBlocksToOpenAI(blocks []contentBlock) []openAIMessage {
	msg := openAIMessage{Role: "assistant"}
	texts := []string{}
	hasImage := false
	for _, b := range blocks {
		switch b.Type {
		case "text":
			texts = append(texts, b.Text)
		case "tool_use":
			call := openAIToolCall{ID: b.ID, Type: "function"}
			call.Function.Name = b.Name
			args := string(b.Input)
			if args == "" || string(args) == "null" {
				args = "{}"
			}
			call.Function.Arguments = args
			msg.ToolCalls = append(msg.ToolCalls, call)
		case "image":
			hasImage = true
		}
	}
	if len(texts) > 0 && !hasImage {
		msg.Content = jsonString(strings.Join(texts, ""))
	} else if hasImage {
		// OpenAI assistant images are unsupported; degrade to text-only.
		if len(texts) > 0 {
			msg.Content = jsonString(strings.Join(texts, ""))
		}
	}
	if len(msg.ToolCalls) == 0 && msg.Content == nil {
		msg.Content = jsonString("")
	}
	return []openAIMessage{msg}
}

func jsonString(s string) json.RawMessage {
	raw, _ := json.Marshal(s)
	return raw
}
