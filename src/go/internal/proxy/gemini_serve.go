package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/protocol/gemini"
)

// geminiUpstreamGoogle is the API-key upstream (translate-types.ts DEFAULT_UPSTREAM).
const geminiUpstreamGoogle = "https://generativelanguage.googleapis.com"

// codeAssistProjectCache caches the resolved Code Assist project per auth token
// so each request does not re-call loadCodeAssist (mirrors the TS in-process
// oauthState.project cache).
type codeAssistProjectCache struct {
	mu      sync.Mutex
	byToken map[string]string
}

var codeAssistProjects = &codeAssistProjectCache{byToken: map[string]string{}}

func (c *codeAssistProjectCache) get(token string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.byToken[token]
	return v, ok
}

func (c *codeAssistProjectCache) set(token, project string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byToken[token] = project
}

// serveGemini is the single egress for every Gemini-backed route. It translates
// the inbound OpenAI/Anthropic/Responses request to a Gemini generateContent
// call, issues it (API-key or OAuth/Code Assist), and translates the response
// back — synthesizing SSE for streaming requests exactly like the TypeScript
// gemini-proxy (which always calls non-stream generateContent and fakes SSE).
func serveGemini(w http.ResponseWriter, r *http.Request, up *Upstream) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request_error", "unreadable body")
		return
	}
	geminiReq, resolvedModel, stream, terr := translateToGemini(up, r.Context(), r.URL.Path, body)
	if terr != nil {
		writeJSONError(w, http.StatusBadGateway, "upstream_error", terr.Error())
		return
	}
	if resolvedModel == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request_error",
			"model is required and no model could be discovered for this account")
		return
	}
	resp, cerr := callGemini(r.Context(), up, resolvedModel, geminiReq)
	if cerr != nil {
		writeGeminiError(w, cerr)
		return
	}
	writeGeminiResponse(w, r.URL.Path, resp, resolvedModel, stream)
}

// translateToGemini routes the inbound body to the right converter and returns
// the Gemini request, the resolved model id, and whether the client asked for
// streaming. Model resolution mirrors resolveRequestModel: it consults the live
// account catalog (liveGeminiCatalog) and falls back to the empty-catalog
// passthrough for unknown concrete ids.
func translateToGemini(up *Upstream, ctx context.Context, path string, body []byte) (*gemini.GeminiGenerateRequest, string, bool, error) {
	catalog := liveGeminiCatalog(ctx, up)
	switch path {
	case "/v1/chat/completions":
		var req gemini.OpenAIChatRequest
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, "", false, fmt.Errorf("invalid OpenAI chat body: %w", err)
		}
		model := gemini.ResolveGeminiModel(req.Model, catalog, stringFromEffort(req)).ID
		return gemini.OpenAIToGemini(req, model), model, req.Stream, nil
	case "/v1/responses":
		var req gemini.OpenAIResponsesRequest
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, "", false, fmt.Errorf("invalid OpenAI responses body: %w", err)
		}
		model := gemini.ResolveGeminiModel(req.Model, catalog, "").ID
		return gemini.OpenAIResponsesToGemini(req, model), model, req.Stream, nil
	case "/v1/messages":
		var req gemini.AnthropicMessageRequest
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, "", false, fmt.Errorf("invalid Anthropic messages body: %w", err)
		}
		if req.Model == "" {
			return nil, "", false, fmt.Errorf("model is required")
		}
		model := gemini.ResolveGeminiModel(req.Model, catalog, stringFromAnthropicEffort(req)).ID
		g, m, err := gemini.AnthropicToGemini(body, model)
		if err != nil {
			return nil, "", false, err
		}
		stream := req.Stream
		return g, m, stream, nil
	default:
		return nil, "", false, fmt.Errorf("unsupported gemini route %s", path)
	}
}

func stringFromEffort(req gemini.OpenAIChatRequest) string {
	if req.ReasoningEffort != "" {
		return req.ReasoningEffort
	}
	return req.Effort
}

func stringFromAnthropicEffort(req gemini.AnthropicMessageRequest) string {
	var oc map[string]interface{}
	if len(req.OutputConfig) > 0 {
		_ = json.Unmarshal(req.OutputConfig, &oc)
	}
	if oc != nil {
		if e, ok := oc["effort"].(string); ok {
			return e
		}
	}
	return ""
}

// callGemini issues one Gemini generateContent call. API-key mode posts to
// generativelanguage.googleapis.com with x-goog-api-key; OAuth mode posts to
// the Code Assist upstream with Authorization: Bearer + the {model, project,
// session_id} envelope. Ports callGemini (gemini-proxy/server.ts) including its
// 429 cooldown and gemini-cli ↔ antigravity auto-fallback on 401/403/404.
func callGemini(ctx context.Context, up *Upstream, model string, geminiReq *gemini.GeminiGenerateRequest) (*gemini.GeminiGenerateResponse, error) {
	keyMode := up.Gemini != nil && up.Gemini.APIKeyMode
	routeKey := "gemini:" + map[bool]string{true: "api-key", false: "oauth"}[keyMode] + ":" + model
	if cooling := geminiCooldowns.remainingMs(routeKey); cooling > 0 {
		return nil, &geminiUpstreamError{
			status:  429,
			message: "Gemini route is cooling down after a rate limit.",
			body:    "",
		}
	}
	if keyMode {
		return callGeminiAPIKey(ctx, up, model, routeKey, geminiReq)
	}
	return callGeminiOAuth(ctx, up, model, geminiReq, routeKey, false)
}

func callGeminiAPIKey(ctx context.Context, up *Upstream, model, routeKey string, geminiReq *gemini.GeminiGenerateRequest) (*gemini.GeminiGenerateResponse, error) {
	token := up.Auth
	if up.AuthProvider != nil {
		if got, perr := up.AuthProvider(ctx); perr == nil {
			token = got
		}
	}
	if token == "" {
		return nil, anyerr.Newf("no Gemini API key available", "GEMINI_NO_KEY")
	}
	apiBase := geminiUpstreamGoogle
	if up.Gemini != nil && up.Gemini.APIKeyBase != nil {
		apiBase = strings.TrimSuffix(up.Gemini.APIKeyBase.String(), "/")
	}
	target := fmt.Sprintf("%s/v1beta/models/%s:generateContent", apiBase, url.PathEscape(model))
	reqBody, err := json.Marshal(geminiReq)
	if err != nil {
		return nil, anyerr.Wrapf(err, "marshal gemini request", "GEMINI_ENCODE")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(reqBody))
	if err != nil {
		return nil, anyerr.Wrapf(err, "build gemini request", "GEMINI_ENCODE")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", token)

	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return nil, anyerr.Wrapf(err, "Gemini network error", "GEMINI_UPSTREAM")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, anyerr.Wrapf(err, "read gemini response", "GEMINI_UPSTREAM")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 429 backoff (mirrors classifyUpstreamFailure → cooldowns.set, applied
		// for every mode, not just OAuth).
		if resp.StatusCode == 429 {
			if ra := retryAfterMs(resp.Header.Get("Retry-After")); ra > 0 {
				geminiCooldowns.set(routeKey, ra)
			}
		}
		return nil, &geminiUpstreamError{
			status:  resp.StatusCode,
			message: extractGeminiErrorMessage(string(raw), resp.StatusCode),
			body:    string(raw),
		}
	}
	var parsed gemini.GeminiGenerateResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, anyerr.Wrapf(err, "decode gemini response", "GEMINI_UPSTREAM")
	}
	return &parsed, nil
}

// callGeminiOAuth performs one Code Assist request and, on 401/403/404, retries
// once through up.Gemini.AltRoute (ports callGeminiWithAutoFallback). routeKey
// tags the cooldown bucket so a 429 sets a backoff the next call observes.
func callGeminiOAuth(ctx context.Context, up *Upstream, model string, geminiReq *gemini.GeminiGenerateRequest, routeKey string, isFallback bool) (*gemini.GeminiGenerateResponse, error) {
	if up.AuthProvider == nil {
		return nil, anyerr.Newf("no Gemini OAuth provider available", "GEMINI_NO_OAUTH")
	}
	token, err := up.AuthProvider(ctx)
	if err != nil {
		return nil, anyerr.Wrapf(err, "Gemini OAuth refresh failed", "GEMINI_OAUTH")
	}
	base := geminiUpstreamGoogle
	if up.Gemini != nil && up.Gemini.CodeAssistBase != nil {
		base = strings.TrimSuffix(up.Gemini.CodeAssistBase.String(), "/")
	}
	project, perr := resolveCodeAssistProject(ctx, base, token)
	if perr != nil {
		return nil, perr
	}
	status, header, raw, rerr := doCodeAssistRequest(ctx, base, token, model, geminiReq, project)
	if rerr != nil {
		return nil, rerr
	}
	if status < 200 || status >= 300 {
		// 429 backoff (mirrors classifyUpstreamFailure → cooldowns.set).
		if status == 429 {
			if ra := retryAfterMs(header.Get("Retry-After")); ra > 0 {
				geminiCooldowns.set(routeKey, ra)
			}
		}
		// Auto auth fallback: one retry through the alternate identity.
		if !isFallback && up.Gemini != nil && up.Gemini.AltRoute != nil &&
			isAutoFallbackStatus(status) {
			alt := up.Gemini.AltRoute
			altToken, aerr := alt.AuthProvider(ctx)
			if aerr == nil {
				altBase := geminiUpstreamGoogle
				if alt.CodeAssistBase != nil {
					altBase = strings.TrimSuffix(alt.CodeAssistBase.String(), "/")
				}
				if altProject, perr := resolveCodeAssistProject(ctx, altBase, altToken); perr == nil {
					if s2, _, r2, e2 := doCodeAssistRequest(ctx, altBase, altToken, model, geminiReq, altProject); e2 == nil && s2 >= 200 && s2 < 300 {
						return unwrapCodeAssist(r2, s2)
					}
				}
			}
		}
		return nil, &geminiUpstreamError{
			status:  status,
			message: extractGeminiErrorMessage(string(raw), status),
			body:    string(raw),
		}
	}
	return unwrapCodeAssist(raw, status)
}

func doCodeAssistRequest(ctx context.Context, base, token, model string, geminiReq *gemini.GeminiGenerateRequest, project string) (int, http.Header, []byte, error) {
	geminiReq.SessionID = geminiSessionID
	envelope := &gemini.GeminiCodeAssistEnvelope{
		Model:        model,
		Project:      project,
		UserPromptID: gemini.RandomID(),
		Request:      geminiReq,
	}
	reqBody, err := json.Marshal(envelope)
	if err != nil {
		return 0, nil, nil, anyerr.Wrapf(err, "marshal gemini request", "GEMINI_ENCODE")
	}
	target := fmt.Sprintf("%s/v1internal:generateContent", base)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(reqBody))
	if err != nil {
		return 0, nil, nil, anyerr.Wrapf(err, "build gemini request", "GEMINI_ENCODE")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, nil, anyerr.Wrapf(err, "Gemini network error", "GEMINI_UPSTREAM")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return 0, nil, nil, anyerr.Wrapf(err, "read gemini response", "GEMINI_UPSTREAM")
	}
	return resp.StatusCode, resp.Header, raw, nil
}

// unwrapCodeAssist returns the inner generateContent response, peeling the
// Code Assist {response: ...} wrapper when present (parsed.response ?? parsed).
func unwrapCodeAssist(raw []byte, status int) (*gemini.GeminiGenerateResponse, error) {
	var parsed gemini.GeminiGenerateResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, anyerr.Wrapf(err, "decode gemini response", "GEMINI_UPSTREAM")
	}
	var wrapped struct {
		Response *gemini.GeminiGenerateResponse `json:"response"`
	}
	if json.Unmarshal(raw, &wrapped) == nil && wrapped.Response != nil {
		return wrapped.Response, nil
	}
	return &parsed, nil
}

func isAutoFallbackStatus(status int) bool {
	return status == 401 || status == 403 || status == 404
}

func retryAfterMs(header string) int64 {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0
	}
	if secs, err := strconv.Atoi(header); err == nil && secs > 0 {
		return int64(secs) * 1000
	}
	// HTTP-date form: compute the delta.
	if t, perr := http.ParseTime(header); perr == nil {
		ms := time.Until(t).Milliseconds()
		if ms > 0 {
			return ms
		}
	}
	return 0
}

// geminiSessionID is the per-process Code Assist session id, mirroring the TS
// proxySessionId (one value for the server's lifetime).
var geminiSessionID = gemini.RandomID()

// geminiCooldowns ports the TS CooldownRegistry: a 429 from any route parks
// that route's key for the Retry-After window so the next call fails fast
// instead of hammering the upstream.
type geminiCooldown struct {
	mu    sync.Mutex
	until map[string]time.Time
}

var geminiCooldowns = &geminiCooldown{until: map[string]time.Time{}}

func (c *geminiCooldown) remainingMs(key string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	u, ok := c.until[key]
	if !ok {
		return 0
	}
	rem := time.Until(u).Milliseconds()
	if rem <= 0 {
		delete(c.until, key)
		return 0
	}
	return rem
}

func (c *geminiCooldown) set(key string, ms int64) {
	if ms <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.until[key] = time.Now().Add(time.Duration(ms) * time.Millisecond)
}

// geminiErrorStatusMessage extracts an HTTP status + message from a Gemini
// egress error so the pool ring can decide whether to fail over.
func geminiErrorStatusMessage(err error) (int, string) {
	var ge *geminiUpstreamError
	if errorsAsGemini(err, &ge) {
		return ge.status, ge.message
	}
	return http.StatusBadGateway, err.Error()
}

// resolveCodeAssistProject calls loadCodeAssist to discover the project for the
// OAuth account, caching by token (mirrors oauthState.project).
func resolveCodeAssistProject(ctx context.Context, base, token string) (string, error) {
	if p, ok := codeAssistProjects.get(token); ok {
		return p, nil
	}
	target := fmt.Sprintf("%s/v1internal:loadCodeAssist", base)
	reqBody, _ := json.Marshal(map[string]interface{}{
		"metadata": map[string]interface{}{
			"ideType":    "GEMINI",
			"pluginType": "GEMINI",
			"platform":   "DARWIN_ARM64",
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(reqBody))
	if err != nil {
		return "", anyerr.Wrapf(err, "build loadCodeAssist request", "GEMINI_OAUTH")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", anyerr.Wrapf(err, "loadCodeAssist network error", "GEMINI_OAUTH")
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", anyerr.Newf(fmt.Sprintf("loadCodeAssist HTTP %d", resp.StatusCode), "GEMINI_OAUTH")
	}
	var parsed struct {
		CloiudCompanionProject string `json:"cloudaicompanionProject"`
	}
	_ = json.Unmarshal(raw, &parsed)
	project := parsed.CloiudCompanionProject
	if project == "" {
		return "", anyerr.Newf("loadCodeAssist returned no project", "GEMINI_OAUTH")
	}
	codeAssistProjects.set(token, project)
	return project, nil
}

type geminiUpstreamError struct {
	status  int
	message string
	body    string
}

func (e *geminiUpstreamError) Error() string { return e.message }

func extractGeminiErrorMessage(body string, status int) string {
	var parsed struct {
		Error *gemini.GeminiError `json:"error"`
	}
	if json.Unmarshal([]byte(body), &parsed) == nil && parsed.Error != nil {
		if parsed.Error.Message != "" {
			return parsed.Error.Message
		}
	}
	if strings.TrimSpace(body) != "" {
		return strings.TrimSpace(body)
	}
	return fmt.Sprintf("Gemini upstream error (HTTP %d)", status)
}

func writeGeminiError(w http.ResponseWriter, err error) {
	var ge *geminiUpstreamError
	if errorsAsGemini(err, &ge) {
		writeJSONError(w, ge.status, "proxy_error", ge.message)
		return
	}
	writeJSONError(w, http.StatusBadGateway, "upstream_error", err.Error())
}

func errorsAsGemini(err error, target **geminiUpstreamError) bool {
	if e, ok := err.(*geminiUpstreamError); ok {
		*target = e
		return true
	}
	return false
}

// writeGeminiResponse translates the Gemini generateContent response back to
// the inbound wire format and writes it. Streaming requests get a faked SSE
// lifecycle built from the single non-stream response (mirrors the TS handlers,
// which always call non-stream generateContent and synthesize SSE).
func writeGeminiResponse(w http.ResponseWriter, path string, resp *gemini.GeminiGenerateResponse, model string, stream bool) {
	switch path {
	case "/v1/chat/completions":
		openai := gemini.GeminiToOpenAI(resp, model)
		if openai.Error != nil {
			writeJSONError(w, http.StatusBadGateway, openai.Error.Type, openai.Error.Message)
			return
		}
		if stream {
			writeOpenAIChatStream(w, openai, model)
			return
		}
		writeGeminiJSON(w, http.StatusOK, openai)
	case "/v1/messages":
		anthropic := gemini.GeminiToAnthropic(resp, model, 0)
		if anthropic.IsAnthropicError() {
			typ, msg, code := anthropic.AnthropicError()
			writeAnthropicError(w, typ, msg, code)
			return
		}
		if stream {
			writeAnthropicStream(w, anthropic, model)
			return
		}
		writeGeminiJSON(w, http.StatusOK, anthropic)
	case "/v1/responses":
		responses := gemini.GeminiToOpenAIResponses(resp, model)
		if stream {
			writeOpenAIResponsesStream(w, responses)
			return
		}
		writeGeminiJSON(w, http.StatusOK, responses)
	default:
		writeJSONError(w, http.StatusNotFound, "not_found_error", "unsupported gemini route")
	}
}

func writeGeminiJSON(w http.ResponseWriter, status int, v interface{}) {
	raw, err := json.Marshal(v)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "proxy_error", "encode gemini response")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

func writeAnthropicError(w http.ResponseWriter, typ, msg, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadGateway)
	payload, _ := json.Marshal(map[string]interface{}{
		"type":  "error",
		"error": map[string]string{"type": typ, "message": msg, "code": code},
	})
	_, _ = w.Write(payload)
}

func flushWriter(w http.ResponseWriter) {
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func writeSseEvent(w http.ResponseWriter, event string, data interface{}) {
	raw, _ := json.Marshal(data)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, string(raw))
	flushWriter(w)
}

func writeOpenAIChatStream(w http.ResponseWriter, openai *gemini.OpenAIChatResponse, model string) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	id := openai.ID
	if id == "" {
		id = "chatcmpl_" + gemini.RandomID()
	}
	var content, reasoning string
	if len(openai.Choices) > 0 {
		content = gemini.MessageText(openai.Choices[0].Message)
		reasoning = openai.Choices[0].Message.ReasoningContent
	}
	if reasoning != "" {
		writeSseEvent(w, "chat.completion.chunk", map[string]interface{}{
			"id":     id,
			"object": "chat.completion.chunk",
			"model":  model,
			"choices": []map[string]interface{}{{
				"index":         0,
				"delta":         map[string]interface{}{"role": "assistant", "reasoning_content": reasoning},
				"finish_reason": nil,
			}},
		})
	}
	writeSseEvent(w, "chat.completion.chunk", map[string]interface{}{
		"id":     id,
		"object": "chat.completion.chunk",
		"model":  model,
		"choices": []map[string]interface{}{{
			"index":         0,
			"delta":         map[string]interface{}{"role": "assistant", "content": content},
			"finish_reason": nil,
		}},
	})
	writeSseEvent(w, "chat.completion.chunk", map[string]interface{}{
		"id":     id,
		"object": "chat.completion.chunk",
		"model":  model,
		"choices": []map[string]interface{}{{
			"index":         0,
			"delta":         map[string]interface{}{},
			"finish_reason": "stop",
		}},
	})
	fmt.Fprintf(w, "data: [DONE]\n\n")
	flushWriter(w)
}

func writeAnthropicStream(w http.ResponseWriter, msg *gemini.AnthropicMessageResponse, model string) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	id := msg.ID
	if id == "" {
		id = "msg_" + gemini.RandomID()
	}
	inputTokens := 0
	if msg.Usage != nil {
		inputTokens = msg.Usage.InputTokens
	}
	writeSseEvent(w, "message_start", map[string]interface{}{
		"type": "message_start",
		"message": map[string]interface{}{
			"id":            id,
			"type":          "message",
			"role":          "assistant",
			"model":         model,
			"content":       []interface{}{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage":         map[string]interface{}{"input_tokens": inputTokens, "output_tokens": 0},
		},
	})
	for index, block := range msg.Content {
		switch block.Type {
		case "thinking":
			writeSseEvent(w, "content_block_start", map[string]interface{}{
				"type":          "content_block_start",
				"index":         index,
				"content_block": map[string]interface{}{"type": "thinking", "thinking": "", "signature": ""},
			})
			if block.Thinking != "" {
				writeSseEvent(w, "content_block_delta", map[string]interface{}{
					"type":  "content_block_delta",
					"index": index,
					"delta": map[string]interface{}{"type": "thinking_delta", "thinking": block.Thinking},
				})
			}
			if block.Signature != "" {
				writeSseEvent(w, "content_block_delta", map[string]interface{}{
					"type":  "content_block_delta",
					"index": index,
					"delta": map[string]interface{}{"type": "signature_delta", "signature": block.Signature},
				})
			}
		case "tool_use":
			cb := map[string]interface{}{"type": "tool_use", "id": block.ID, "name": block.Name, "input": map[string]interface{}{}}
			if block.ThoughtSignature != "" {
				cb["thought_signature"] = block.ThoughtSignature
			}
			writeSseEvent(w, "content_block_start", map[string]interface{}{
				"type":          "content_block_start",
				"index":         index,
				"content_block": cb,
			})
			writeSseEvent(w, "content_block_delta", map[string]interface{}{
				"type":  "content_block_delta",
				"index": index,
				"delta": map[string]interface{}{"type": "input_json_delta", "partial_json": string(block.Input)},
			})
		case "redacted_thinking":
			writeSseEvent(w, "content_block_start", map[string]interface{}{
				"type":          "content_block_start",
				"index":         index,
				"content_block": block,
			})
		default:
			writeSseEvent(w, "content_block_start", map[string]interface{}{
				"type":          "content_block_start",
				"index":         index,
				"content_block": map[string]interface{}{"type": "text", "text": ""},
			})
			if block.Text != "" {
				writeSseEvent(w, "content_block_delta", map[string]interface{}{
					"type":  "content_block_delta",
					"index": index,
					"delta": map[string]interface{}{"type": "text_delta", "text": block.Text},
				})
			}
		}
		writeSseEvent(w, "content_block_stop", map[string]interface{}{"type": "content_block_stop", "index": index})
	}
	stopReason := msg.StopReason
	delta := map[string]interface{}{"stop_reason": stopReason}
	if msg.StopSequence != nil {
		delta["stop_sequence"] = *msg.StopSequence
	}
	outputTokens := 0
	if msg.Usage != nil {
		outputTokens = msg.Usage.OutputTokens
	}
	writeSseEvent(w, "message_delta", map[string]interface{}{
		"type":  "message_delta",
		"delta": delta,
		"usage": map[string]interface{}{"output_tokens": outputTokens},
	})
	writeSseEvent(w, "message_stop", map[string]interface{}{"type": "message_stop"})
}

func writeOpenAIResponsesStream(w http.ResponseWriter, response *gemini.OpenAIResponsesResponse) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	sequence := 0
	emit := func(typ string, payload map[string]interface{}) {
		payload["type"] = typ
		payload["sequence_number"] = sequence
		sequence++
		writeSseEvent(w, typ, payload)
	}
	inProgress := map[string]interface{}{
		"id":                  response.ID,
		"object":              response.Object,
		"created_at":          response.CreatedAt,
		"status":              "in_progress",
		"error":               nil,
		"incomplete_details":  nil,
		"model":               response.Model,
		"output":              []interface{}{},
		"parallel_tool_calls": response.ParallelToolCalls,
		"usage":               nil,
	}
	emit("response.created", map[string]interface{}{"response": inProgress})
	emit("response.in_progress", map[string]interface{}{"response": inProgress})

	for outputIndex, item := range response.Output {
		addedItem := map[string]interface{}{
			"id":                item.ID,
			"type":              item.Type,
			"summary":           item.Summary,
			"encrypted_content": item.EncryptedContent,
			"role":              item.Role,
			"content":           item.Content,
			"call_id":           item.CallID,
			"name":              item.Name,
			"arguments":         item.Arguments,
			"output":            item.Output,
		}
		addedItem["status"] = "in_progress"
		switch item.Type {
		case "reasoning":
			addedItem["summary"] = []interface{}{}
		case "message":
			addedItem["content"] = []interface{}{}
		case "function_call":
			addedItem["arguments"] = ""
		}
		emit("response.output_item.added", map[string]interface{}{"output_index": outputIndex, "item": addedItem})

		switch item.Type {
		case "reasoning":
			for summaryIndex, part := range item.Summary {
				emit("response.reasoning_summary_part.added", map[string]interface{}{
					"item_id": item.ID, "output_index": outputIndex, "summary_index": summaryIndex,
					"part": map[string]interface{}{"type": "summary_text", "text": ""},
				})
				if part.Text != "" {
					emit("response.reasoning_summary_text.delta", map[string]interface{}{
						"item_id": item.ID, "output_index": outputIndex, "summary_index": summaryIndex,
						"delta": part.Text,
					})
				}
				emit("response.reasoning_summary_text.done", map[string]interface{}{
					"item_id": item.ID, "output_index": outputIndex, "summary_index": summaryIndex,
					"text": part.Text,
				})
				emit("response.reasoning_summary_part.done", map[string]interface{}{
					"item_id": item.ID, "output_index": outputIndex, "summary_index": summaryIndex,
					"part": part,
				})
			}
		case "message":
			for contentIndex, part := range item.Content {
				emptyPart := part
				emptyPart.Text = ""
				emit("response.content_part.added", map[string]interface{}{
					"item_id": item.ID, "output_index": outputIndex, "content_index": contentIndex,
					"part": emptyPart,
				})
				if part.Text != "" {
					emit("response.output_text.delta", map[string]interface{}{
						"item_id": item.ID, "output_index": outputIndex, "content_index": contentIndex,
						"delta": part.Text, "logprobs": []interface{}{},
					})
				}
				emit("response.output_text.done", map[string]interface{}{
					"item_id": item.ID, "output_index": outputIndex, "content_index": contentIndex,
					"text": part.Text, "logprobs": []interface{}{},
				})
				emit("response.content_part.done", map[string]interface{}{
					"item_id": item.ID, "output_index": outputIndex, "content_index": contentIndex,
					"part": part,
				})
			}
		case "function_call":
			if item.Arguments != "" {
				emit("response.function_call_arguments.delta", map[string]interface{}{
					"item_id": item.ID, "output_index": outputIndex, "delta": item.Arguments,
				})
			}
			emit("response.function_call_arguments.done", map[string]interface{}{
				"item_id": item.ID, "output_index": outputIndex, "arguments": item.Arguments,
			})
		}
		// Emit the real (completed) item so the client sees final content.
		emit("response.output_item.done", map[string]interface{}{"output_index": outputIndex, "item": item})
	}
	emit("response.completed", map[string]interface{}{"response": response})
}
