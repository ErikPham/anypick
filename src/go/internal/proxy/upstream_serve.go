package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"time"

	"github.com/outof0/anypick/go/internal/egress"
	"github.com/outof0/anypick/go/internal/protocol"
)

// egressKind names which leaf serves one (request, upstream) pair.
//
// There is exactly one copy of this decision. Before it was extracted the
// single-account path and the pool ring each carried their own copy of the
// branch table, and they had already drifted: the ring's copy skipped the
// context-window rewrite every single-account branch applies, so a member
// that hit the context limit returned a raw vendor error and the client never
// compacted. Two copies of one decision is not duplication, it is a bug that
// has not happened yet — or, as here, one that already did.
type egressKind int

const (
	kindGemini egressKind = iota
	kindNativeAnthropic
	kindTranslatedMessages
	kindOpenAIPath
	kindPassthrough
)

func egressKindFor(r *http.Request, up *Upstream) egressKind {
	if up == nil {
		return kindPassthrough
	}
	isMessages := r.Method == http.MethodPost &&
		(r.URL.Path == "/v1/messages" || r.URL.Path == "/messages")
	isGemini := r.Method == http.MethodPost &&
		(r.URL.Path == "/v1/chat/completions" ||
			r.URL.Path == "/v1/responses" ||
			r.URL.Path == "/v1/messages")
	switch {
	case isGemini && up.TranslateGemini:
		return kindGemini
	case isMessages && up.Anthropic != nil:
		return kindNativeAnthropic
	case isMessages && up.TranslateAnthropic:
		return kindTranslatedMessages
	case r.Method == http.MethodPost && up.OpenAIPath != "" &&
		(r.URL.Path == "/v1/chat/completions" || r.URL.Path == "/v1/completions"):
		return kindOpenAIPath
	default:
		return kindPassthrough
	}
}

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
	switch egressKindFor(r, up) {
	case kindGemini:
		serveGemini(w, r, up)
		return
	case kindNativeAnthropic:
		serveNativeAnthropic(w, r, up)
		return
	case kindTranslatedMessages:
		serveTranslatedMessagesWith(w, r, up)
		return
	case kindOpenAIPath:
		serveOpenAIPath(w, r, up)
		return
	}
	target := *up.BaseURL
	header, prefix, token, aerr := resolveUpstreamAuth(r.Context(), up)
	if aerr != nil {
		proxyLogf("credential refresh failed: %s", redactAuthError(aerr))
		writeJSONError(w, http.StatusBadGateway, "proxy_error", "credential refresh failed")
		return
	}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(&target)
			pr.Out.Host = target.Host
			pr.Out.Header.Del("Authorization")
			pr.Out.Header.Del("x-api-key")
			pr.Out.Header.Del("api-key")
			pr.Out.Header.Set(header, prefix+token)
			applyExtraHeaders(pr.Out, up.ExtraHeaders)
		},
		Transport:     egress.Transport(),
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
		raw, err := ReadCappedBody(r, MaxBodyBytes)
		if errors.Is(err, ErrBodyTooLarge) {
			writeJSONError(w, http.StatusRequestEntityTooLarge, "invalid_request_error",
				fmt.Sprintf("request body exceeds %d bytes", MaxBodyBytes))
			return
		}
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_request_error", "unreadable body")
			return
		}
		sanitized = up.SanitizeOpenAIBody(raw)
		sanitizing = true
	}
	header, prefix, token, aerr := resolveUpstreamAuth(r.Context(), up)
	if aerr != nil {
		proxyLogf("credential refresh failed: %s", redactAuthError(aerr))
		writeJSONError(w, http.StatusBadGateway, "proxy_error", "credential refresh failed")
		return
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
			pr.Out.Header.Del("Authorization")
			pr.Out.Header.Del("x-api-key")
			pr.Out.Header.Del("api-key")
			pr.Out.Header.Set(header, prefix+token)
			applyExtraHeaders(pr.Out, up.ExtraHeaders)
		},
		Transport:     egress.Transport(),
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

// flattenedNativeUpstream flattens a native Anthropic target and its parent
// Upstream into one *Upstream: the target wins, the parent fills whatever the
// target leaves empty.
//
// Both callers need this shape and neither may own the merge. The pool ring
// needs a plain *Upstream to hand to its capture leaf; the single-account path
// needs the same fields to build its ReverseProxy. The copy that used to live
// in the pool skipped the header default that resolveUpstreamAuth applies, so
// a target that left AuthHeader empty was forwarded with an empty header name
// — a request the vendor answers 401, indistinguishable from a dead token.
func flattenedNativeUpstream(up *Upstream) *Upstream {
	t := up.Anthropic
	if t == nil {
		return up
	}
	auth := t.Auth
	if auth == "" {
		auth = up.Auth
	}
	header, prefix := t.AuthHeader, t.AuthValuePrefix
	if header == "" {
		header, prefix = up.AuthHeader, up.AuthValuePrefix
	}
	provider := t.AuthProvider
	if provider == nil {
		provider = up.AuthProvider
	}
	headers := append(append([]ExtraHeader(nil), t.ExtraHeaders...), up.ExtraHeaders...)
	return &Upstream{
		BaseURL:         t.BaseURL,
		Auth:            auth,
		AuthHeader:      header,
		AuthValuePrefix: prefix,
		AuthProvider:    provider,
		ExtraHeaders:    headers,
	}
}

// serveNativeAnthropic forwards /v1/messages verbatim to a vendor's native
// Anthropic-compatible endpoint (e.g. api.z.ai/api/anthropic). ReverseProxy
// joins the base path with the request path, so a base of …/anthropic plus
// /v1/messages lands on …/anthropic/v1/messages.
func serveNativeAnthropic(w http.ResponseWriter, r *http.Request, up *Upstream) {
	flat := flattenedNativeUpstream(up)
	header, prefix, token, aerr := resolveUpstreamAuth(r.Context(), flat)
	if aerr != nil {
		proxyLogf("credential refresh failed: %s", redactAuthError(aerr))
		writeJSONError(w, http.StatusBadGateway, "proxy_error", "credential refresh failed")
		return
	}
	target := *flat.BaseURL
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(&target)
			pr.Out.Host = target.Host
			out := pr.Out
			out.Header.Del("Authorization")
			out.Header.Del("x-api-key")
			out.Header.Del("api-key")
			out.Header.Set(header, prefix+token)
			applyExtraHeaders(out, flat.ExtraHeaders)
		},
		Transport:     egress.Transport(),
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
	body, err := ReadCappedBody(r, MaxBodyBytes)
	if errors.Is(err, ErrBodyTooLarge) {
		writeJSONError(w, http.StatusRequestEntityTooLarge, "invalid_request_error",
			fmt.Sprintf("request body exceeds %d bytes", MaxBodyBytes))
		return
	}
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
	if err := injectUpstreamAuthFields(r.Context(), req, up); err != nil {
		proxyLogf("credential refresh failed: %s", redactAuthError(err))
		writeJSONError(w, http.StatusBadGateway, "proxy_error", "credential refresh failed")
		return
	}

	client := egress.StreamClient()
	resp, err := client.Do(req)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "upstream_error", "upstream unavailable")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		rawBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		f := classifyTranslatedFailure(resp, body, translated, rawBody)
		if f.ContextWindow {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("request-id", extractRequestID(f.Body))
		} else {
			w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		}
		w.WriteHeader(f.Status)
		_, _ = w.Write(f.Body)
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

// translatedFailure is one non-2xx upstream answer on the translated
// /v1/messages path, already converted: a context-window error becomes the
// canonical 400 compaction body, everything else passes through verbatim.
//
// It carries the upstream headers for a caller that replays the response
// later (the pool ring needs Retry-After to price a cooldown). A caller that
// writes straight through sets Content-Type itself and ignores them.
type translatedFailure struct {
	Status        int
	Body          []byte
	ContextWindow bool
	Header        http.Header
}

// classifyTranslatedFailure logs why an upstream rejected a translated
// request and decides what the client should receive. It is shared by the
// committing path and the pool's capturing path, which differ in *where* the
// bytes go — not in what they are. The pool used to have its own copy without
// the context-window branch, so a member that hit the limit returned a raw
// vendor error and the client never compacted.
func classifyTranslatedFailure(resp *http.Response, sent, forwarded, raw []byte) translatedFailure {
	status := resp.StatusCode
	if status >= 400 && status < 500 {
		// Log the vendor envelope on 4xx: it names the real rejection
		// (channel/quota/model variants) that the client would otherwise
		// see only opaquely. Vendor-authored text only, safe to log.
		proxyLogf("upstream %d; vendor says: %s", status, describeVendorError(string(raw), status))
		proxyLogf("upstream %d; failed shape: %s", status, describeAnthropicShape(sent))
		if strings.Contains(string(raw), "11128") {
			proxyLogf("upstream 11128; sanitized: %s", describeSanitizedOpenAI(forwarded))
		}
	}
	if IsContextWindowError(string(raw)) {
		body := anthropicContextWindowErrorBody(string(raw), resp.Header.Get("request-id"))
		header := resp.Header.Clone()
		header.Set("Content-Type", "application/json")
		header.Set("request-id", extractRequestID(body))
		return translatedFailure{
			Status:        http.StatusBadRequest,
			Body:          body,
			ContextWindow: true,
			Header:        header,
		}
	}
	return translatedFailure{Status: status, Body: raw, Header: resp.Header}
}

// resolveUpstreamAuth returns the header/prefix/token to forward for an
// OpenAI-surface request. A configured AuthProvider wins over static Auth.
// On provider error it fails closed with an error: forwarding an empty or
// stale token would surface only as an upstream 401 and hide the real cause.
func resolveUpstreamAuth(ctx context.Context, up *Upstream) (header, prefix, token string, err error) {
	header, prefix = up.AuthHeader, up.AuthValuePrefix
	if header == "" {
		header = "Authorization"
		prefix = "Bearer "
	}
	token = up.Auth
	if up.AuthProvider != nil {
		got, perr := up.AuthProvider(ctx)
		if perr != nil {
			return header, prefix, "", perr
		}
		token = got
	}
	return header, prefix, token, nil
}

func applyExtraHeaders(out *http.Request, headers []ExtraHeader) {
	for _, h := range headers {
		if h.Name != "" {
			out.Header.Set(h.Name, h.Value)
		}
	}
}

func injectUpstreamAuthFields(ctx context.Context, out *http.Request, up *Upstream) error {
	header, prefix, token, err := resolveUpstreamAuth(ctx, up)
	if err != nil {
		return err
	}
	out.Header.Del("Authorization")
	out.Header.Del("x-api-key")
	out.Header.Del("api-key")
	out.Header.Set(header, prefix+token)
	applyExtraHeaders(out, up.ExtraHeaders)
	return nil
}

// redactAuthError keeps credential-refresh failures diagnosable without
// letting vendor/OIDC payloads cross the output boundary. Callers log this
// and return a generic "credential refresh failed" to the client.
func redactAuthError(err error) string {
	if err == nil {
		return "unknown"
	}
	msg := err.Error()
	// anyerr messages are already safe (no raw bodies after the OAuth
	// redaction); still bound the length so a compromised endpoint cannot
	// smuggle bulk data through logs.
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return msg
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
		// Counts only: tool names are user-defined and must not reach logs.
		sb.WriteString("tools=" + strconv.Itoa(len(tools)))
	}
	return sb.String()
}

func stringOrEmpty(v any) string {
	s, _ := v.(string)
	return s
}

// describeSanitizedOpenAI summarizes the body we actually sent upstream
// after translation/sanitizer. Fingerprints only — no prompt text, no raw
// dump. A previous revision wrote inbound+sanitized bodies to
// last-11128.json; that file could hold prompts, history, file contents and
// pasted secrets, so raw dumps are removed (P0). Diagnostics stay in the
// redacted log line above.

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

// describeVendorError extracts an allowlisted fingerprint from the vendor
// envelope: numeric code and request id only. Vendor message text and raw
// bytes never reach logs: a compromised or chatty upstream can echo prompts,
// tool arguments or secrets into its error message, and proxy.log gets
// attached to bug reports.
func describeVendorError(raw string, status int) string {
	var env struct {
		Code      int    `json:"code"`
		RequestID string `json:"requestId"`
	}
	if json.Unmarshal([]byte(raw), &env) != nil || (env.Code == 0 && env.RequestID == "") {
		return "unparseable status=" + strconv.Itoa(status) + " len=" + strconv.Itoa(len(raw))
	}
	out := "code=" + strconv.Itoa(env.Code)
	if env.RequestID != "" {
		out += " req=" + env.RequestID
	}
	return out
}

func translateRequestBody(body []byte) ([]byte, error) { return protocol.TranslateRequest(body) }
