package gemini

import "encoding/json"

// IsRecord reports whether v is a non-array JSON object.
func IsRecord(v interface{}) bool {
	return isRecord(v)
}

func isRecord(v interface{}) bool {
	if v == nil {
		return false
	}
	if _, ok := v.(map[string]interface{}); ok {
		return true
	}
	if _, ok := v.(map[string]json.RawMessage); ok {
		return true
	}
	return false
}

// SanitizeGeminiSchema converts a client JSON Schema into the smaller Schema
// dialect Gemini's FunctionDeclaration accepts. OpenAI/Anthropic clients
// commonly send draft-07 metadata and object-schema keywords that Gemini
// rejects with a 400 instead of ignoring them, so those are dropped.
func SanitizeGeminiSchema(schema interface{}) map[string]interface{} {
	input := asMap(schema)
	out := map[string]interface{}{}
	scalarKeys := []string{
		"type", "format", "title", "description", "nullable",
		"maxItems", "minItems", "maxProperties", "minProperties",
	}
	for _, key := range scalarKeys {
		if v, ok := input[key]; ok && v != nil {
			out[key] = v
		}
	}
	if enum, ok := input["enum"].([]interface{}); ok {
		out["enum"] = enum
	}
	if req, ok := input["required"].([]interface{}); ok {
		filtered := make([]string, 0, len(req))
		for _, v := range req {
			if s, ok := v.(string); ok {
				filtered = append(filtered, s)
			}
		}
		out["required"] = filtered
	}
	if props, ok := input["properties"].(map[string]interface{}); ok {
		properties := map[string]interface{}{}
		for name, value := range props {
			properties[name] = SanitizeGeminiSchema(value)
		}
		out["properties"] = properties
	}
	if items, ok := input["items"]; ok && items != nil {
		out["items"] = SanitizeGeminiSchema(items)
	}

	// Gemini does not support additionalProperties/propertyNames/$schema or
	// arbitrary JSON-Schema composition keywords in FunctionDeclaration. If a
	// union has no usable top-level type, retain its first branch so the tool
	// remains callable rather than sending an invalid request.
	if _, ok := out["type"]; !ok {
		var alternatives []interface{}
		if anyOf, ok := input["anyOf"].([]interface{}); ok {
			alternatives = anyOf
		} else if oneOf, ok := input["oneOf"].([]interface{}); ok {
			alternatives = oneOf
		}
		for _, alt := range alternatives {
			if isRecord(alt) {
				merged := SanitizeGeminiSchema(alt)
				for k, v := range out {
					merged[k] = v
				}
				return merged
			}
		}
	}
	return out
}

func asMap(v interface{}) map[string]interface{} {
	switch m := v.(type) {
	case map[string]interface{}:
		return m
	case map[string]json.RawMessage:
		out := make(map[string]interface{}, len(m))
		for k, raw := range m {
			var decoded interface{}
			if json.Unmarshal(raw, &decoded) == nil {
				out[k] = decoded
			}
		}
		return out
	default:
		return map[string]interface{}{}
	}
}
