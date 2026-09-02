package gemini

import (
	"encoding/json"
	"testing"
)

func intPtr(v int) *int { return &v }

func TestOpenAIToGeminiBasic(t *testing.T) {
	req := OpenAIChatRequest{
		Model: "gemini-2.5-pro",
		Messages: []OpenAIMessage{
			{Role: "system", Content: json.RawMessage(`"you are helpful"`)},
			{Role: "user", Content: json.RawMessage(`"hello"`)},
			{Role: "assistant", Content: json.RawMessage(`"hi there"`)},
		},
		MaxTokens: intPtr(100),
	}
	g := OpenAIToGemini(req, "gemini-2.5-pro")
	if g.SystemInstruction == nil || len(g.SystemInstruction.Parts) == 0 {
		t.Fatalf("system instruction missing: %+v", g.SystemInstruction)
	}
	if g.SystemInstruction.Parts[0].Text != "you are helpful" {
		t.Fatalf("system text = %q", g.SystemInstruction.Parts[0].Text)
	}
	// system is NOT also the first content turn
	if len(g.Contents) != 2 {
		t.Fatalf("contents = %d, want 2 (user+assistant)", len(g.Contents))
	}
	if g.Contents[0].Parts[0].Text != "hello" {
		t.Fatalf("user text = %q", g.Contents[0].Parts[0].Text)
	}
	if g.Contents[1].Parts[0].Text != "hi there" {
		t.Fatalf("assistant text = %q", g.Contents[1].Parts[0].Text)
	}
	if g.GenerationConfig == nil || g.GenerationConfig.MaxOutputTokens == nil ||
		*g.GenerationConfig.MaxOutputTokens != 100 {
		t.Fatalf("max tokens not translated: %+v", g.GenerationConfig)
	}
}

func TestOpenAIToGeminiToolCall(t *testing.T) {
	body := `{
		"model": "gemini-2.5-pro",
		"messages": [{"role": "user", "content": "weather?"}],
		"tools": [{
			"type": "function",
			"function": {"name": "get_weather", "description": "w", "parameters": {"type": "object"}}
		}]
	}`
	var req OpenAIChatRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}
	g := OpenAIToGemini(req, "gemini-2.5-pro")
	if len(g.Tools) != 1 || len(g.Tools[0].FunctionDeclarations) != 1 {
		t.Fatalf("tools = %+v", g.Tools)
	}
	if g.Tools[0].FunctionDeclarations[0].Name != "get_weather" {
		t.Fatalf("tool name = %q", g.Tools[0].FunctionDeclarations[0].Name)
	}
}

func TestGeminiToOpenAIChatText(t *testing.T) {
	res := &GeminiGenerateResponse{
		Candidates: []GeminiCandidate{{
			Content:      &GeminiContent{Role: "model", Parts: []GeminiPart{{Text: "hi"}}},
			FinishReason: "STOP",
		}},
		UsageMetadata: &GeminiUsageMetadata{
			PromptTokenCount:     intPtr(10),
			CandidatesTokenCount: intPtr(5),
			TotalTokenCount:      intPtr(15),
		},
	}
	out := GeminiToOpenAI(res, "gemini-2.5-pro")
	if out.Object != "chat.completion" {
		t.Fatalf("object = %q", out.Object)
	}
	if len(out.Choices) != 1 {
		t.Fatalf("choices = %d", len(out.Choices))
	}
	if MessageText(out.Choices[0].Message) != "hi" {
		t.Fatalf("content = %q", MessageText(out.Choices[0].Message))
	}
	if out.Choices[0].FinishReason != "stop" {
		t.Fatalf("finish_reason = %q", out.Choices[0].FinishReason)
	}
	if out.Usage == nil || out.Usage.TotalTokens != 15 {
		t.Fatalf("usage = %+v", out.Usage)
	}
}

func TestGeminiToOpenAIToolCall(t *testing.T) {
	res := &GeminiGenerateResponse{
		Candidates: []GeminiCandidate{{
			Content: &GeminiContent{Role: "model", Parts: []GeminiPart{{
				FunctionCall: &GeminiFunctionCall{Name: "get_weather", Args: map[string]interface{}{"city": "Hanoi"}},
			}}},
			FinishReason: "STOP",
		}},
	}
	out := GeminiToOpenAI(res, "gemini-2.5-pro")
	if len(out.Choices) != 1 {
		t.Fatalf("choices = %d", len(out.Choices))
	}
	if len(out.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("tool_calls = %+v", out.Choices[0].Message.ToolCalls)
	}
	tc := out.Choices[0].Message.ToolCalls[0]
	if tc.Function.Name != "get_weather" {
		t.Fatalf("tool name = %q", tc.Function.Name)
	}
	if out.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("finish_reason = %q (expected tool_calls for a function call)", out.Choices[0].FinishReason)
	}
}

func TestGeminiToOpenAIThinking(t *testing.T) {
	res := &GeminiGenerateResponse{
		Candidates: []GeminiCandidate{{
			Content: &GeminiContent{Role: "model", Parts: []GeminiPart{
				{Text: "let me think", Thought: true, ThoughtSignature: "sig"},
				{Text: "the answer"},
			}},
			FinishReason: "STOP",
		}},
	}
	out := GeminiToOpenAI(res, "gemini-2.5-pro")
	msg := out.Choices[0].Message
	if msg.ReasoningContent != "let me think" {
		t.Fatalf("reasoning_content = %q", msg.ReasoningContent)
	}
	if MessageText(msg) != "the answer" {
		t.Fatalf("content = %q", MessageText(msg))
	}
}

func TestAnthropicRoundTrip(t *testing.T) {
	body := `{
		"model": "gemini-2.5-pro",
		"max_tokens": 100,
		"system": "be terse",
		"messages": [{"role": "user", "content": "hi"}]
	}`
	g, model, err := AnthropicToGemini([]byte(body), "gemini-2.5-pro")
	if err != nil {
		t.Fatal(err)
	}
	if model != "gemini-2.5-pro" {
		t.Fatalf("model = %q", model)
	}
	if g.SystemInstruction == nil || g.SystemInstruction.Parts[0].Text != "be terse" {
		t.Fatalf("system = %+v", g.SystemInstruction)
	}
	if len(g.Contents) != 1 || g.Contents[0].Parts[0].Text != "hi" {
		t.Fatalf("contents = %+v", g.Contents)
	}
	res := &GeminiGenerateResponse{
		Candidates: []GeminiCandidate{{
			Content:      &GeminiContent{Role: "model", Parts: []GeminiPart{{Text: "yo"}}},
			FinishReason: "STOP",
		}},
		UsageMetadata: &GeminiUsageMetadata{PromptTokenCount: intPtr(3), CandidatesTokenCount: intPtr(1)},
	}
	out := GeminiToAnthropic(res, "gemini-2.5-pro", 3)
	if out.Type != "message" {
		t.Fatalf("type = %q", out.Type)
	}
	if len(out.Content) != 1 || out.Content[0].Type != "text" || out.Content[0].Text != "yo" {
		t.Fatalf("content = %+v", out.Content)
	}
	if out.StopReason != "end_turn" {
		t.Fatalf("stop_reason = %q", out.StopReason)
	}
	if out.Usage == nil || out.Usage.InputTokens != 3 {
		t.Fatalf("usage = %+v", out.Usage)
	}
}

func TestAnthropicToolUseRoundTrip(t *testing.T) {
	body := `{
		"model": "gemini-2.5-pro",
		"max_tokens": 100,
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "weather?"}]},
			{"role": "assistant", "content": [{"type": "tool_use", "id": "t1", "name": "get_weather", "input": {"city": "Hanoi"}}]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "t1", "content": "20C"}]}
		]
	}`
	g, _, err := AnthropicToGemini([]byte(body), "gemini-2.5-pro")
	if err != nil {
		t.Fatal(err)
	}
	// assistant tool_use + user tool_result should become model + user functionResponse turns
	if len(g.Contents) != 3 {
		t.Fatalf("contents = %d, want 3", len(g.Contents))
	}
	res := &GeminiGenerateResponse{
		Candidates: []GeminiCandidate{{
			Content: &GeminiContent{Role: "model", Parts: []GeminiPart{{
				FunctionCall: &GeminiFunctionCall{Name: "get_weather", Args: map[string]interface{}{"city": "Hanoi"}},
			}}},
			FinishReason: "STOP",
		}},
	}
	out := GeminiToAnthropic(res, "gemini-2.5-pro", 0)
	if out.Type != "message" || len(out.Content) != 1 || out.Content[0].Type != "tool_use" {
		t.Fatalf("content = %+v", out.Content)
	}
	if out.Content[0].Name != "get_weather" {
		t.Fatalf("tool name = %q", out.Content[0].Name)
	}
	if out.StopReason != "tool_use" {
		t.Fatalf("stop_reason = %q", out.StopReason)
	}
}

func TestOpenAIResponsesRoundTrip(t *testing.T) {
	req := OpenAIResponsesRequest{
		Model:  "gemini-2.5-pro",
		Input:  json.RawMessage(`"hello"`),
		Stream: false,
	}
	g := OpenAIResponsesToGemini(req, "gemini-2.5-pro")
	if len(g.Contents) != 1 || g.Contents[0].Parts[0].Text != "hello" {
		t.Fatalf("contents = %+v", g.Contents)
	}
	res := &GeminiGenerateResponse{
		Candidates: []GeminiCandidate{{
			Content:      &GeminiContent{Role: "model", Parts: []GeminiPart{{Text: "reply"}}},
			FinishReason: "STOP",
		}},
		UsageMetadata: &GeminiUsageMetadata{
			PromptTokenCount:     intPtr(4),
			CandidatesTokenCount: intPtr(2),
			TotalTokenCount:      intPtr(6),
		},
	}
	out := GeminiToOpenAIResponses(res, "gemini-2.5-pro")
	if out.Object != "response" {
		t.Fatalf("object = %q", out.Object)
	}
	if len(out.Output) == 0 {
		t.Fatalf("no output items")
	}
	msg := out.Output[0]
	if msg.Type != "message" || len(msg.Content) == 0 || msg.Content[0].Text != "reply" {
		t.Fatalf("output[0] = %+v", msg)
	}
	if out.Usage == nil || out.Usage.TotalTokens != 6 {
		t.Fatalf("usage = %+v", out.Usage)
	}
}

func TestResolveGeminiModelEmptyCatalogPassthrough(t *testing.T) {
	// Empty-catalog fallback: concrete ids pass through unchanged (matches the
	// TS passthrough behavior for unknown ids). Live catalog refresh is the
	// one remaining deferred M11 gap.
	r := ResolveGeminiModel("gemini-2.5-pro", nil, "")
	if r.ID != "gemini-2.5-pro" {
		t.Fatalf("resolved id = %q, want passthrough", r.ID)
	}
	// Alias mapping still applies even without a live catalog.
	if got := MapToGeminiModel("gemini-2.5-flash"); got != "gemini-2.5-flash" {
		t.Fatalf("MapToGeminiModel = %q", got)
	}
}

func TestMapGeminiFinish(t *testing.T) {
	cases := map[string]string{
		"STOP":                      "stop",
		"MAX_TOKENS":                "length",
		"SAFETY":                    "stop",
		"RECITATION":                "stop",
		"FINISH_REASON_UNSPECIFIED": "stop",
	}
	for in, want := range cases {
		if got := MapGeminiFinish(in, false); got != want {
			t.Fatalf("MapGeminiFinish(%q) = %q, want %q", in, got, want)
		}
	}
	// A function call maps to tool_calls when tools are present.
	if got := MapGeminiFinish("STOP", true); got != "tool_calls" {
		t.Fatalf("MapGeminiFinish(STOP, tools) = %q, want tool_calls", got)
	}
	if got := MapGeminiFinishToAnthropic("STOP", true); got != "tool_use" {
		t.Fatalf("MapGeminiFinishToAnthropic = %q, want tool_use", got)
	}
}
