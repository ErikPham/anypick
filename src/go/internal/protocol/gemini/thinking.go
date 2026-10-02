package gemini

import (
	"encoding/json"
	"strings"
)

// ReasoningEffort is the public OpenAI/Anthropic effort vocabulary.
type ReasoningEffort string

const (
	EffortNone    ReasoningEffort = "none"
	EffortMinimal ReasoningEffort = "minimal"
	EffortLow     ReasoningEffort = "low"
	EffortMedium  ReasoningEffort = "medium"
	EffortHigh    ReasoningEffort = "high"
	EffortXHigh   ReasoningEffort = "xhigh"
	EffortMax     ReasoningEffort = "max"
)

// ReasoningIntent is the protocol-neutral reasoning control extracted from a
// client request. Ports reasoning.ts.
type ReasoningIntent struct {
	Effort         ReasoningEffort
	Enabled        *bool
	BudgetTokens   *int
	IncludeSummary bool
}

func normalizeReasoningEffort(value string) ReasoningEffort {
	normalized := strings.TrimSpace(strings.ToLower(value))
	switch normalized {
	case "none", "minimal", "low", "medium", "high", "xhigh", "max":
		return ReasoningEffort(normalized)
	default:
		return ""
	}
}

// ReasoningFromOpenAI extracts the reasoning intent from an OpenAI chat request.
func ReasoningFromOpenAI(req OpenAIChatRequest) ReasoningIntent {
	var reasoning map[string]interface{}
	if len(req.Reasoning) > 0 {
		_ = json.Unmarshal(req.Reasoning, &reasoning)
	}
	effort := normalizeReasoningEffort(req.ReasoningEffort)
	if effort == "" {
		if e := normalizeReasoningEffort(stringFromMap(reasoning, "effort")); e != "" {
			effort = e
		}
	}
	if effort == "" {
		effort = normalizeReasoningEffort(req.Effort)
	}
	intent := ReasoningIntent{}
	if effort != "" {
		intent.Effort = effort
		enabled := effort != EffortNone
		intent.Enabled = &enabled
	}
	if summary := stringFromMap(reasoning, "summary"); summary == "auto" || summary == "concise" || summary == "detailed" {
		intent.IncludeSummary = true
	}
	return intent
}

// ReasoningFromAnthropic extracts the reasoning intent from an Anthropic
// messages request.
func ReasoningFromAnthropic(req AnthropicMessageRequest) ReasoningIntent {
	var thinking map[string]interface{}
	if len(req.Thinking) > 0 {
		_ = json.Unmarshal(req.Thinking, &thinking)
	}
	var outputConfig map[string]interface{}
	if len(req.OutputConfig) > 0 {
		_ = json.Unmarshal(req.OutputConfig, &outputConfig)
	}
	effort := normalizeReasoningEffort(stringFromMap(outputConfig, "effort"))
	intent := ReasoningIntent{}
	if effort != "" {
		intent.Effort = effort
	}
	if thinkingType := stringFromMap(thinking, "type"); thinkingType != "" {
		switch strings.ToLower(thinkingType) {
		case "disabled":
			intent.Enabled = boolPtr(false)
		case "enabled", "adaptive":
			intent.Enabled = boolPtr(true)
		}
	}
	if bt, ok := numberFromMap(thinking, "budget_tokens"); ok && bt >= 0 {
		b := int(bt)
		intent.BudgetTokens = &b
	}
	if intent.Enabled != nil && *intent.Enabled && stringFromMap(thinking, "display") != "omitted" {
		intent.IncludeSummary = true
	}
	return intent
}

// ApplyGeminiThinkingConfig writes a Gemini thinkingConfig into the request
// from the extracted intent. Ports applyGeminiThinkingConfig (translate-thinking.ts).
func ApplyGeminiThinkingConfig(request *GeminiGenerateRequest, intent ReasoningIntent, model string) {
	if intent.Effort == "" && intent.Enabled == nil && intent.BudgetTokens == nil && !intent.IncludeSummary {
		return
	}
	if request.GenerationConfig == nil {
		request.GenerationConfig = &GeminiGenerationConfig{}
	}
	cfg := &GeminiThinkingConfig{}
	if intent.IncludeSummary {
		cfg.IncludeThoughts = true
	}
	if intent.BudgetTokens != nil {
		// Preserve Claude's exact manual budget; Gemini retains it for
		// compatibility on newer generateContent models.
		budget := *intent.BudgetTokens
		cfg.ThinkingBudget = &budget
	} else {
		effort := EffortMedium
		if intent.Enabled != nil && !*intent.Enabled {
			effort = EffortNone
		} else if intent.Effort != "" {
			effort = intent.Effort
		}
		major := ParseGeminiModelVersion(MapToGeminiModel(model))[0]
		if major == 0 || major >= 3 {
			var level string
			switch effort {
			case EffortNone, EffortMinimal:
				level = "MINIMAL"
			case EffortLow:
				level = "LOW"
			case EffortMedium:
				level = "MEDIUM"
			default:
				level = "HIGH"
			}
			cfg.ThinkingLevel = level
		} else {
			// Gemini 2.5 uses token budgets rather than levels. -1 asks Gemini
			// to choose dynamically and avoids inventing a fixed ceiling.
			var budget int
			switch effort {
			case EffortNone, EffortMinimal:
				budget = 0
			case EffortLow:
				budget = 1024
			case EffortMedium:
				budget = 4096
			default:
				budget = -1
			}
			cfg.ThinkingBudget = &budget
		}
	}
	request.GenerationConfig.ThinkingConfig = cfg
}

// RestoreAnthropicThinkingHistory rebuilds Claude assistant history without
// moving signatures between Gemini parts. Ports restoreAnthropicThinkingHistory.
func RestoreAnthropicThinkingHistory(request *GeminiGenerateRequest, source AnthropicMessageRequest) {
	targetAssistantTurns := make([]*GeminiContent, 0)
	for i := range request.Contents {
		if request.Contents[i].Role == "model" {
			targetAssistantTurns = append(targetAssistantTurns, &request.Contents[i])
		}
	}
	targetIndex := 0
	for _, message := range source.Messages {
		if message.Role != "assistant" {
			continue
		}
		if targetIndex >= len(targetAssistantTurns) {
			break
		}
		target := targetAssistantTurns[targetIndex]
		targetIndex++
		blocks := decodeAnthropicBlocks(message.Content)
		if len(blocks) == 0 {
			continue
		}
		rebuilt := make([]GeminiPart, 0, len(blocks))
		var pendingToolSignature string
		for _, block := range blocks {
			switch block.Type {
			case "thinking":
				thinking := block.Thinking
				signature := block.Signature
				if thinking == ToolSignatureCarrier {
					pendingToolSignature = signature
					continue
				}
				part := GeminiPart{Text: thinking, Thought: true}
				if thinking == EmptyThoughtCarrier {
					part.Text = ""
				}
				if signature != "" {
					part.ThoughtSignature = signature
				}
				rebuilt = append(rebuilt, part)
			case "text":
				rebuilt = append(rebuilt, GeminiPart{Text: block.Text})
			case "tool_use":
				blockSignature := block.ThoughtSignature
				thoughtSignature := blockSignature
				if thoughtSignature == "" {
					thoughtSignature = pendingToolSignature
				}
				part := GeminiPart{}
				if thoughtSignature != "" {
					part.ThoughtSignature = thoughtSignature
				}
				args := map[string]interface{}{}
				if len(block.Input) > 0 {
					var decoded map[string]interface{}
					if json.Unmarshal(block.Input, &decoded) == nil {
						args = decoded
					}
				}
				part.FunctionCall = &GeminiFunctionCall{Name: block.Name, Args: args}
				rebuilt = append(rebuilt, part)
				pendingToolSignature = ""
			}
		}
		if len(rebuilt) > 0 {
			target.Parts = rebuilt
		}
	}
}

func decodeAnthropicBlocks(content json.RawMessage) []AnthropicContentBlock {
	if len(content) == 0 {
		return nil
	}
	var s string
	if json.Unmarshal(content, &s) == nil {
		return []AnthropicContentBlock{{Type: "text", Text: s}}
	}
	var blocks []AnthropicContentBlock
	_ = json.Unmarshal(content, &blocks)
	return blocks
}

func stringFromMap(m map[string]interface{}, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func numberFromMap(m map[string]interface{}, key string) (float64, bool) {
	if m == nil {
		return 0, false
	}
	switch v := m[key].(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

func boolPtr(b bool) *bool { return &b }
