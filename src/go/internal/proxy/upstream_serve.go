package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httputil"
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

func translateRequestBody(body []byte) ([]byte, error) { return protocol.TranslateRequest(body) }
