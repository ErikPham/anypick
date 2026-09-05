package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httputil"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/outof0/anypick/go/internal/protocol"
)

// Serve forwards one authenticated inbound request to the described upstream.
// Dispatch order for POST /v1/messages: native Anthropic target when present,
// else translation into chat completions when TranslateAnthropic, else plain
// passthrough. Everything else forwards to the OpenAI surface. The per-account
// Server and the Proxy Hub share this single egress path so the security
// posture cannot diverge.
func Serve(w http.ResponseWriter, r *http.Request, up *Upstream) {
	if up == nil || up.BaseURL == nil {
		writeJSONError(w, http.StatusBadGateway, "upstream_error", "no upstream")
		return
	}
	isMessages := r.Method == http.MethodPost &&
		(r.URL.Path == "/v1/messages" || r.URL.Path == "/messages")
	isGemini := r.Method == http.MethodPost &&
		(r.URL.Path == "/v1/chat/completions" ||
			r.URL.Path == "/v1/responses" ||
			r.URL.Path == "/v1/messages")
	if isGemini && up.TranslateGemini {
		serveGemini(w, r, up)
		return
	}
	if isMessages && up.Anthropic != nil {
		serveNativeAnthropic(w, r, up)
		return
	}
	if isMessages && up.TranslateAnthropic {
		serveTranslatedMessagesWith(w, r, up)
		return
	}
	if r.Method == http.MethodPost && up.OpenAIPath != "" &&
		(r.URL.Path == "/v1/chat/completions" || r.URL.Path == "/v1/completions") {
		serveOpenAIPath(w, r, up)
		return
	}
	target := *up.BaseURL
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(&target)
			pr.Out.Host = target.Host
			injectUpstreamAuthFields(pr.Out.Context(), pr.Out, up)
		},
		FlushInterval: 10 * time.Millisecond,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			writeJSONError(w, http.StatusBadGateway, "upstream_error", "upstream unavailable")
		},
		ModifyResponse: func(resp *http.Response) error {
			return rewriteContextWindowResponse(resp, r)
		},
	}
	rp.ServeHTTP(w, r)
}

// serveOpenAIPath forwards an OpenAI-surface chat request to an upstream
// whose chat surface lives at a non-default path (Upstream.OpenAIPath joined
// onto the base path). Unlike the generic branch it never lets the inbound
// /v1/… path through: vendors like WorkBuddy read the /v1/ segment as an
// API-key channel marker and reject subscription calls on it (code 11128).
// The body is buffered once so Upstream.SanitizeOpenAIBody can run; the
// RESPONSE still streams via ReverseProxy. Without a sanitizer the buffered
// body forwards byte-identical to the generic branch.
func serveOpenAIPath(w http.ResponseWriter, r *http.Request, up *Upstream) {
	base := *up.BaseURL
	chatPath := strings.TrimSuffix(base.Path, "/") + up.OpenAIPath
	target := base
	target.Path = chatPath
	target.RawPath = ""
	var sanitized []byte
	sanitizing := false
	if up.SanitizeOpenAIBody != nil {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_request_error", "unreadable body")
			return
		}
		sanitized = up.SanitizeOpenAIBody(raw)
		sanitizing = true
	}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(&target)
			pr.Out.Host = target.Host
			// SetURL re-joins the inbound path; pin the subscription path.
			pr.Out.URL.Path = chatPath
			pr.Out.URL.RawPath = ""
			if sanitizing {
				pr.Out.Body = io.NopCloser(bytes.NewReader(sanitized))
				pr.Out.ContentLength = int64(len(sanitized))
				pr.Out.GetBody = func() (io.ReadCloser, error) {
					return io.NopCloser(bytes.NewReader(sanitized)), nil
				}
			}
			injectUpstreamAuthFields(pr.Out.Context(), pr.Out, up)
		},
		FlushInterval: 10 * time.Millisecond,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			writeJSONError(w, http.StatusBadGateway, "upstream_error", "upstream unavailable")
		},
		ModifyResponse: func(resp *http.Response) error {
			return rewriteContextWindowResponse(resp, r)
		},
	}
	rp.ServeHTTP(w, r)
}

// serveNativeAnthropic forwards /v1/messages verbatim to a vendor's native
// Anthropic-compatible endpoint (e.g. api.z.ai/api/anthropic). ReverseProxy
// joins the base path with the request path, so a base of …/anthropic plus
// /v1/messages lands on …/anthropic/v1/messages.
func serveNativeAnthropic(w http.ResponseWriter, r *http.Request, up *Upstream) {
	t := up.Anthropic
	header, prefix, token := resolveTargetAuth(r.Context(), t, up)
	target := *t.BaseURL
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(&target)
			pr.Out.Host = target.Host
			out := pr.Out
			out.Header.Del("Authorization")
			out.Header.Del("x-api-key")
			out.Header.Del("api-key")
			out.Header.Set(header, prefix+token)
			applyExtraHeaders(out, t.ExtraHeaders)
			applyExtraHeaders(out, up.ExtraHeaders)
		},
		FlushInterval: 10 * time.Millisecond,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			writeJSONError(w, http.StatusBadGateway, "upstream_error", "upstream unavailable")
		},
		ModifyResponse: func(resp *http.Response) error {
			return rewriteContextWindowResponse(resp, r)
		},
	}
	rp.ServeHTTP(w, r)
}

// serveTranslatedMessagesWith is the shared implementation used by both the
// per-account Server and the hub.
func serveTranslatedMessagesWith(w http.ResponseWriter, r *http.Request, up *Upstream) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request_error", "unreadable body")
		return
	}
	translated, terr := translateRequestBody(body)
	if terr != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request_error", terr.Error())
		return
	}
	if up.SanitizeOpenAIBody != nil {
		translated = up.SanitizeOpenAIBody(translated)
	}
	upstreamURL := *up.BaseURL
	upstreamURL.Path = strings.TrimSuffix(upstreamURL.Path, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
		upstreamURL.String(), bytes.NewReader(translated))
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	injectUpstreamAuthFields(r.Context(), req, up)

	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "upstream_error", "upstream unavailable")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		rawBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		raw := string(rawBody)
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			// Log the vendor envelope on 4xx: it names the real rejection
			// (channel/quota/model variants) that the client would otherwise
			// see only opaquely. Vendor-authored text only, safe to log.
			proxyLogf("upstream %d; vendor says: %s", resp.StatusCode, describeVendorError(raw))
			proxyLogf("upstream %d; failed shape: %s", resp.StatusCode, describeAnthropicShape(body))
			if strings.Contains(raw, "11128") {
				proxyLogf("upstream 11128; sanitized: %s", describeSanitizedOpenAI(translated))
				dump11128(body, translated)
			}
		}
		if IsContextWindowError(raw) {
			body := anthropicContextWindowErrorBody(raw, resp.Header.Get("request-id"))
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("request-id", extractRequestID(body))
			w.WriteHeader(400)
			_, _ = w.Write(body)
			return
		}
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(rawBody)
		return
	}
	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "text/event-stream") || strings.Contains(ct, "stream") {
		_ = protocol.TranslateStreamingResponse(resp, w)
		return
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "upstream_error", "truncated response")
		return
	}
	out, terr := protocol.TranslateResponse(raw)
	if terr != nil {
		writeJSONError(w, http.StatusBadGateway, "upstream_error", terr.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

// resolveUpstreamAuth returns the header/prefix/token to forward for an
// OpenAI-surface request. A configured AuthProvider wins over static Auth and
// may refresh a short-lived token; on provider error the token is left empty
// so the upstream rejects with 401 instead of the proxy inventing one.
func resolveUpstreamAuth(ctx context.Context, up *Upstream) (header, prefix, token string) {
	header, prefix = up.AuthHeader, up.AuthValuePrefix
	if header == "" {
		header = "Authorization"
		prefix = "Bearer "
	}
	token = up.Auth
	if up.AuthProvider != nil {
		if got, err := up.AuthProvider(ctx); err == nil {
			token = got
		}
	}
	return header, prefix, token
}

// resolveTargetAuth resolves the credential for a native Anthropic target,
// falling back to the parent Upstream's provider/header when the target leaves
// them empty.
func resolveTargetAuth(ctx context.Context, t *UpstreamTarget, up *Upstream) (header, prefix, token string) {
	header, prefix = t.AuthHeader, t.AuthValuePrefix
	if header == "" {
		header, prefix = up.AuthHeader, up.AuthValuePrefix
	}
	if header == "" {
		header = "Authorization"
		prefix = "Bearer "
	}
	token = t.Auth
	if token == "" {
		token = up.Auth
	}
	provider := t.AuthProvider
	if provider == nil {
		provider = up.AuthProvider
	}
	if provider != nil {
		if got, err := provider(ctx); err == nil {
			token = got
		}
	}
	return header, prefix, token
}

func applyExtraHeaders(out *http.Request, headers []ExtraHeader) {
	for _, h := range headers {
		if h.Name != "" {
			out.Header.Set(h.Name, h.Value)
		}
	}
}

func injectUpstreamAuthFields(ctx context.Context, out *http.Request, up *Upstream) {
	header, prefix, token := resolveUpstreamAuth(ctx, up)
	out.Header.Del("Authorization")
	out.Header.Del("x-api-key")
	out.Header.Del("api-key")
	out.Header.Set(header, prefix+token)
	applyExtraHeaders(out, up.ExtraHeaders)
}

func extractRequestID(body []byte) string {
	var payload struct {
		RequestID string `json:"request_id"`
	}
	if json.Unmarshal(body, &payload) == nil {
		return payload.RequestID
	}
	return ""
}

func rewriteContextWindowResponse(resp *http.Response, req *http.Request) error {
	if resp.StatusCode != 400 && resp.StatusCode != 413 {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil
	}
	_ = resp.Body.Close()
	if !IsContextWindowError(string(raw)) {
		resp.Body = io.NopCloser(bytes.NewReader(raw))
		return nil
	}
	path := req.URL.Path
	isAnthropic := path == "/v1/messages" || path == "/messages"
	// OpenAI Responses SSE compaction signal
	if path == "/v1/responses" {
		// Check if client asked for streaming (prefers SSE)
		if req.Header.Get("Accept") == "text/event-stream" || strings.Contains(req.URL.RawQuery, "stream") {
			sse := openAIResponsesContextWindowSSE()
			resp.StatusCode = 200
			resp.Header.Set("Content-Type", "text/event-stream; charset=utf-8")
			resp.Header.Set("Cache-Control", "no-cache, no-transform")
			resp.Header.Set("Connection", "keep-alive")
			resp.Header.Del("Content-Length")
			resp.ContentLength = int64(len(sse))
			resp.Body = io.NopCloser(bytes.NewReader(sse))
			return nil
		}
	}
	var body []byte
	var status int
	if isAnthropic {
		body = anthropicContextWindowErrorBody(string(raw), resp.Header.Get("request-id"))
		status = 400
		resp.Header.Set("request-id", extractRequestID(body))
	} else {
		body = openAIContextWindowErrorBody()
		status = 400
	}
	resp.StatusCode = status
	resp.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp.Header.Del("Content-Length")
	resp.Header.Del("Content-Encoding")
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	return nil
}

func openAIResponsesContextWindowSSE() []byte {
	// Minimal response.failed SSE that Codex/OpenAI Responses clients treat as compaction trigger.
	payload := `{"type":"response.failed","sequence_number":0,"response":{"id":"resp_context","object":"response","status":"failed","error":{"code":"context_length_exceeded","message":"` + openAIContextWindowMessage + `"}}}` + "\n"
	return []byte("event: response.failed\ndata: " + payload + "\n\n")
}

// describeAnthropicShape summarizes an Anthropic request for diagnostics:
// model, top-level keys, per-message roles with block types and sizes.
// Block TEXT is never logged.
func describeAnthropicShape(body []byte) string {
	var v map[string]any
	if json.Unmarshal(body, &v) != nil {
		return "unparseable"
	}
	var sb strings.Builder
	if m, ok := v["model"].(string); ok {
		sb.WriteString("model=" + m + " ")
	}
	if sys, ok := v["system"]; ok && sys != nil {
		switch s := sys.(type) {
		case string:
			if s != "" {
				sb.WriteString("system=text#" + strconv.Itoa(len(s)) + " ")
			}
		case []any:
			sb.WriteString("system=blocks#" + strconv.Itoa(len(s)) + " ")
		default:
			sb.WriteString("system=yes ")
		}
	}
	if s, ok := v["stream"].(bool); ok && s {
		if n, ok := v["max_tokens"].(float64); ok {
			sb.WriteString("maxtokens=" + strconv.Itoa(int(n)) + " ")
		}
		if t, ok := v["temperature"].(float64); ok {
			sb.WriteString("temp=" + strconv.FormatFloat(t, 'f', -1, 64) + " ")
		}
		if tc, ok := v["tool_choice"].(map[string]any); ok {
			sb.WriteString("toolchoice=")
			sb.WriteString(stringOrEmpty(tc["type"]))
			sb.WriteString(" ")
		}
		sb.WriteString("stream ")
	}
	if t, ok := v["thinking"].(map[string]any); ok {
		sb.WriteString("thinking=")
		sb.WriteString(stringOrEmpty(t["type"]))
		sb.WriteString(" ")
	}
	if msgs, ok := v["messages"].([]any); ok {
		sb.WriteString("msgs=[")
		for i, m := range msgs {
			if i > 0 {
				sb.WriteString(",")
			}
			mm, _ := m.(map[string]any)
			role, _ := mm["role"].(string)
			sb.WriteString(role + ":")
			switch c := mm["content"].(type) {
			case string:
				sb.WriteString("text#" + strconv.Itoa(len(c)))
			case []any:
				var parts []string
				for _, b := range c {
					if bm, ok := b.(map[string]any); ok {
						t, _ := bm["type"].(string)
						switch t {
						case "text":
							s, _ := bm["text"].(string)
							parts = append(parts, "text#"+strconv.Itoa(len(s)))
						case "tool_use", "tool_result", "thinking", "redacted_thinking", "image", "document", "server_tool_use", "web_search_tool_result", "mcp_tool_use", "mcp_tool_result", "container_upload":
							parts = append(parts, t)
						default:
							parts = append(parts, "other:"+t)
						}
					}
				}
				sb.WriteString(strings.Join(parts, "+"))
			}
		}
		sb.WriteString("]")
	}
	if tools, ok := v["tools"].([]any); ok {
		names := make([]string, 0, len(tools))
		for _, t := range tools {
			if tm, ok := t.(map[string]any); ok {
				if name, ok := tm["name"].(string); ok {
					names = append(names, name)
				}
			}
		}
		sb.WriteString(" toolnames=" + strings.Join(names, ","))
		sb.WriteString(" tools=" + strconv.Itoa(len(tools)))
	}
	return sb.String()
}

func stringOrEmpty(v any) string {
	s, _ := v.(string)
	return s
}

// describeSanitizedOpenAI summarizes the body we actually sent upstream
// after translation/sanitizer. Fingerprints only — no prompt text.
func dump11128(inbound, sanitized []byte) {
	root := os.Getenv("ANYPICK_HOME")
	if root == "" {
		home, _ := os.UserHomeDir()
		root = filepath.Join(home, ".anypick")
	}
	path := filepath.Join(root, "last-11128.json")
	var in map[string]any
	_ = json.Unmarshal(inbound, &in)
	payload := map[string]any{
		"sanitized": string(sanitized),
		"inbound":   in,
	}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, raw, 0o600)
}

func describeSanitizedOpenAI(body []byte) string {
	var v map[string]any
	if json.Unmarshal(body, &v) != nil {
		return "unparseable"
	}
	var sb strings.Builder
	if m, ok := v["model"].(string); ok {
		sb.WriteString("model=" + m + " ")
	}
	raw := strings.ToLower(string(body))
	for _, p := range []string{
		"official cli", "x-anthropic-billing-header", "github.com/anthropics",
		"claude-haiku", "claude-sonnet", "claude-opus",
	} {
		if strings.Contains(raw, p) {
			sb.WriteString("has=" + strings.ReplaceAll(p, " ", "_") + " ")
		}
	}
	if msgs, ok := v["messages"].([]any); ok {
		sb.WriteString("n=" + strconv.Itoa(len(msgs)) + " ")
		if len(msgs) > 0 {
			if mm, ok := msgs[0].(map[string]any); ok {
				sb.WriteString("lead=" + stringOrEmpty(mm["role"]) + " ")
			}
		}
	}
	if tools, ok := v["tools"].([]any); ok {
		sb.WriteString("tools=" + strconv.Itoa(len(tools)))
	}
	return sb.String()
}

// describeVendorError extracts the vendor envelope code/message/requestId.
// Vendor-authored text only — no user content flows through here.
func describeVendorError(raw string) string {
	var env struct {
		Code      int    `json:"code"`
		Msg       string `json:"msg"`
		RequestID string `json:"requestId"`
	}
	if json.Unmarshal([]byte(raw), &env) != nil || env.Msg == "" {
		if len(raw) > 160 {
			raw = raw[:160]
		}
		return "unparseable: " + raw
	}
	return "code=" + strconv.Itoa(env.Code) + " msg=" + env.Msg + " req=" + env.RequestID
}

func translateRequestBody(body []byte) ([]byte, error) { return protocol.TranslateRequest(body) }
