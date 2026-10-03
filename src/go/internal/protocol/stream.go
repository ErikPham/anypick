package protocol

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ---- streaming: openai SSE → anthropic SSE ----

type openAIStreamChunk struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Content   json.RawMessage       `json:"content"`
			ToolCalls []openAIToolCallDelta `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// openAIToolCallDelta is one OpenAI tool_call fragment inside a stream chunk.
type openAIToolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// streamTranslator accumulates per-request block state while converting
// OpenAI deltas into Anthropic stream events.
type streamTranslator struct {
	w           http.ResponseWriter
	flusher     http.Flusher
	started     bool
	textOpen    bool
	toolBlocks  map[int]bool // openai tool index → anthropic block started
	blockIndex  int
	stopReason  *string
	inputTokens int
	outTokens   int
}

func (t *streamTranslator) send(event, data string) {
	fmt.Fprintf(t.w, "event: %s\ndata: %s\n\n", event, data)
	t.flusher.Flush()
}

func (t *streamTranslator) sendJSON(event string, v interface{}) {
	raw, _ := json.Marshal(v)
	t.send(event, string(raw))
}

// TranslateStreamingResponse consumes an OpenAI chat-completions SSE body and
// writes an Anthropic message stream. Returns the first error observed in the
// upstream stream, if any.
func TranslateStreamingResponse(upstream *http.Response, w http.ResponseWriter) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("response writer does not support flushing")
	}
	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	header.Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	t := &streamTranslator{
		w: w, flusher: flusher,
		toolBlocks: map[int]bool{},
	}
	// Use bufio.Reader instead of Scanner to avoid the 1 MiB token limit
	// truncating large tool-call deltas.
	reader := bufio.NewReader(upstream.Body)
	for {
		raw, err := reader.ReadString('\n')
		line := strings.TrimSpace(raw)
		if line != "" && strings.HasPrefix(line, "data:") {
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "[DONE]" {
				break
			}
			var chunk openAIStreamChunk
			if jsonErr := json.Unmarshal([]byte(payload), &chunk); jsonErr == nil {
				t.handleChunk(chunk)
			}
		}
		if err != nil {
			// Ensure the message is finalized even when upstream closes without [DONE].
			t.finish()
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
	t.finish()
	return nil
}

func (t *streamTranslator) handleChunk(chunk openAIStreamChunk) {
	if !t.started {
		t.started = true
		model := chunk.Model
		in := 0
		if chunk.Usage != nil {
			in = chunk.Usage.PromptTokens
		}
		t.inputTokens = in
		id := "msg_" + chunk.ID
		t.sendJSON("message_start", map[string]interface{}{
			"type": "message_start",
			"message": map[string]interface{}{
				"id": id, "type": "message", "role": "assistant",
				"model":   model,
				"content": []interface{}{},
				"usage":   map[string]int{"input_tokens": in, "output_tokens": 0},
			},
		})
	}
	if len(chunk.Choices) == 0 {
		if chunk.Usage != nil {
			t.outTokens = chunk.Usage.CompletionTokens
		}
		return
	}
	delta := chunk.Choices[0].Delta
	if chunk.Usage != nil {
		t.outTokens = chunk.Usage.CompletionTokens
	}

	var text string
	if len(delta.Content) > 0 && string(delta.Content) != "null" {
		_ = json.Unmarshal(delta.Content, &text)
	}
	if text != "" {
		if !t.textOpen {
			idx := t.blockIndex
			t.blockIndex++
			t.textOpen = true
			t.sendJSON("content_block_start", map[string]interface{}{
				"type": "content_block_start", "index": idx,
				"content_block": map[string]interface{}{"type": "text", "text": ""},
			})
		}
		t.sendJSON("content_block_delta", map[string]interface{}{
			"type":  "content_block_delta",
			"index": t.blockIndex - 1,
			"delta": map[string]string{"type": "text_delta", "text": text},
		})
	}

	t.emitToolCallDeltas(delta.ToolCalls)

	if chunk.Choices[0].FinishReason != nil && *chunk.Choices[0].FinishReason != "" {
		t.stopReason = mapFinishReason(*chunk.Choices[0].FinishReason)
	}
}

func (t *streamTranslator) emitToolCallDeltas(calls []openAIToolCallDelta) {
	for _, call := range calls {
		if call.ID != "" || call.Function.Name != "" {
			if !t.toolBlocks[call.Index] {
				t.closeTextBlock()
				idx := t.blockIndex + call.Index
				t.toolBlocks[call.Index] = true
				t.sendJSON("content_block_start", map[string]interface{}{
					"type": "content_block_start", "index": idx,
					"content_block": map[string]interface{}{
						"type": "tool_use", "id": call.ID,
						"name": call.Function.Name, "input": map[string]interface{}{},
					},
				})
			}
		}
		if call.Function.Arguments != "" {
			idx := t.blockIndex + call.Index
			t.sendJSON("content_block_delta", map[string]interface{}{
				"type": "content_block_delta", "index": idx,
				"delta": map[string]string{
					"type": "input_json_delta", "partial_json": call.Function.Arguments,
				},
			})
		}
	}
}

func (t *streamTranslator) closeTextBlock() {
	if !t.textOpen {
		return
	}
	t.textOpen = false
	t.sendJSON("content_block_stop", map[string]interface{}{
		"type": "content_block_stop", "index": t.blockIndex - 1,
	})
}

func (t *streamTranslator) finish() {
	t.closeTextBlock()
	maxToolIdx := -1
	for idx := range t.toolBlocks {
		if idx > maxToolIdx {
			maxToolIdx = idx
		}
	}
	if maxToolIdx >= 0 {
		t.sendJSON("content_block_stop", map[string]interface{}{
			"type": "content_block_stop", "index": t.blockIndex + maxToolIdx,
		})
	}
	if !t.started {
		// Upstream produced nothing usable; still emit a valid empty message.
		t.started = true
		t.sendJSON("message_start", map[string]interface{}{
			"type": "message_start",
			"message": map[string]interface{}{
				"id": "msg_empty", "type": "message", "role": "assistant",
				"model": "", "content": []interface{}{},
				"usage": map[string]int{"input_tokens": 0, "output_tokens": 0},
			},
		})
	}
	stop := t.stopReason
	if stop == nil {
		s := "end_turn"
		stop = &s
	}
	t.sendJSON("message_delta", map[string]interface{}{
		"type":  "message_delta",
		"delta": map[string]interface{}{"stop_reason": stop, "stop_sequence": nil},
		"usage": map[string]int{"output_tokens": t.outTokens},
	})
	t.send("message_stop", `{"type":"message_stop"}`)
}
