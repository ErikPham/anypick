package client

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/transport"
)

// opencodeAdapter ports src/clients/opencode.ts: a multi-provider runtime
// configured through opencode.json(c) with an anypick-managed provider entry.
type opencodeAdapter struct{ home string }

func newOpenCode(home string) *opencodeAdapter { return &opencodeAdapter{home: home} }

const (
	opencodeManagedProviderID = "anypick-managed"
	opencodeRestoreFile       = "restore.json"
)

func (a *opencodeAdapter) ID() string   { return "opencode" }
func (a *opencodeAdapter) Name() string { return "OpenCode" }
func (a *opencodeAdapter) ShortName() string {
	return "OpenCode"
}

func (a *opencodeAdapter) Capabilities() *Capabilities {
	return &Capabilities{
		ID:                       "opencode",
		AcceptedProtocols:        []transport.Protocol{transport.OpenAI},
		SupportsIsolatedHome:     true,
		SupportsPersistentConfig: true,
		ProtocolPreference:       transport.OpenAI,
	}
}

func openCodeConfigDir(home string) string {
	if home == liveHome() {
		if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
			return filepath.Join(xdg, "opencode")
		}
	}
	return filepath.Join(home, ".config", "opencode")
}

func openCodeCandidatePaths(home string) (string, string) {
	dir := openCodeConfigDir(home)
	return filepath.Join(dir, "opencode.json"), filepath.Join(dir, "opencode.jsonc")
}

func openCodeEffectiveConfigPath(home string) string {
	jsonPath, jsoncPath := openCodeCandidatePaths(home)
	if _, err := os.Stat(jsonPath); err == nil {
		return jsonPath
	}
	if _, err := os.Stat(jsoncPath); err == nil {
		return jsoncPath
	}
	return jsonPath
}

// readOpenCodeConfig parses the live config, refusing to continue on garbage.
func readOpenCodeConfig(path string) (map[string]json.RawMessage, error) {
	doc := map[string]json.RawMessage{}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return doc, nil
		}
		return nil, err
	}
	if uerr := json.Unmarshal(stripJsonc(raw), &doc); uerr != nil {
		return nil, anyerr.Newf(
			fmt.Sprintf("Refusing to overwrite unreadable OpenCode config at %s: %v", path, uerr),
			"CLIENT_CONFIG_INVALID")
	}
	return doc, nil
}

// stripJsonc ports parseJsonc's normalization: comments become spaces and
// trailing commas are dropped without touching string contents.
func stripJsonc(raw []byte) []byte {
	text := string(raw)
	var out strings.Builder
	inString, escaped, lineComment, blockComment := false, false, false, false
	for i := 0; i < len(text); i++ {
		char := text[i]
		var next byte
		if i+1 < len(text) {
			next = text[i+1]
		}
		switch {
		case lineComment:
			if char == '\n' || char == '\r' {
				lineComment = false
				out.WriteByte(char)
			} else {
				out.WriteByte(' ')
			}
		case blockComment:
			if char == '*' && next == '/' {
				blockComment = false
				out.WriteString("  ")
				i++
			} else if char == '\n' || char == '\r' {
				out.WriteByte(char)
			} else {
				out.WriteByte(' ')
			}
		case inString:
			out.WriteByte(char)
			switch {
			case escaped:
				escaped = false
			case char == '\\':
				escaped = true
			case char == '"':
				inString = false
			}
		default:
			switch {
			case char == '"':
				inString = true
				out.WriteByte(char)
			case char == '/' && next == '/':
				lineComment = true
				out.WriteString("  ")
				i++
			case char == '/' && next == '*':
				blockComment = true
				out.WriteString("  ")
				i++
			default:
				out.WriteByte(char)
			}
		}
	}
	stripped := out.String()
	// Drop trailing commas before } or ] (outside strings — safe after strip).
	var cleaned strings.Builder
	for i := 0; i < len(stripped); i++ {
		c := stripped[i]
		if c == ',' {
			j := i + 1
			for j < len(stripped) && (stripped[j] == ' ' || stripped[j] == '\t' || stripped[j] == '\n' || stripped[j] == '\r') {
				j++
			}
			if j < len(stripped) && (stripped[j] == '}' || stripped[j] == ']') {
				continue
			}
		}
		cleaned.WriteByte(c)
	}
	return []byte(cleaned.String())
}

// endpointForOpenCode normalizes a base URL to end with /v1.
func endpointForOpenCode(endpoint string) string {
	e := strings.TrimSuffix(endpoint, "/")
	e = strings.TrimSuffix(e, "/v1")
	return e + "/v1"
}

type openCodeRestoreState struct {
	Version           int             `json:"version"`
	ConfigPath        string          `json:"configPath"`
	ProviderPresent   bool            `json:"providerPresent"`
	ProviderValue     json.RawMessage `json:"providerValue,omitempty"`
	ModelPresent      bool            `json:"modelPresent"`
	ModelValue        json.RawMessage `json:"modelValue,omitempty"`
	SmallModelPresent bool            `json:"smallModelPresent"`
	SmallModelValue   json.RawMessage `json:"smallModelValue,omitempty"`
}

func (a *opencodeAdapter) Apply(ctx *ApplyContext) (*ApplyResult, error) {
	targetHome := ctx.Home
	if ctx.IsolatedHome != "" {
		targetHome = ctx.IsolatedHome
	}
	configPath := openCodeEffectiveConfigPath(targetHome)
	secretPath := filepath.Join(openCodeConfigDir(targetHome), ".anypick-api-key")
	restorePath := filepath.Join(ctx.AnyPickRoot, "clients", "opencode", opencodeRestoreFile)

	modelIDs := []string{}
	if m := firstNonEmpty(ctx.ModelRoles["default"], ctx.DefaultModel); m != "" {
		modelIDs = append(modelIDs, m)
	}
	for _, role := range sortedKeys(ctx.ModelRoles) {
		if role == "default" {
			continue
		}
		if m := firstNonEmpty(ctx.ModelRoles[role]); m != "" {
			modelIDs = append(modelIDs, m)
		}
	}
	modelIDs = dedupeStrings(modelIDs)
	if ctx.Endpoint == "" || len(modelIDs) == 0 || modelIDs[0] == "" {
		return nil, anyerr.Newf(
			"OpenCode routing needs an endpoint and a selected default model.",
			"CLIENT_CONFIG_INVALID")
	}

	if ctx.DryRun {
		return &ApplyResult{ManagedPaths: []string{configPath, secretPath, restorePath}}, nil
	}

	doc, err := readOpenCodeConfig(configPath)
	if err != nil {
		return nil, err
	}
	providers, err := openCodeProviders(doc)
	if err != nil {
		return nil, err
	}

	if ctx.IsolatedHome == "" {
		if _, statErr := os.Stat(restorePath); os.IsNotExist(statErr) {
			restore := openCodeRestoreState{
				Version:           1,
				ConfigPath:        configPath,
				ProviderPresent:   providers[opencodeManagedProviderID] != nil,
				ModelPresent:      doc["model"] != nil,
				SmallModelPresent: doc["small_model"] != nil,
			}
			if v, ok := providers[opencodeManagedProviderID]; ok {
				restore.ProviderValue = v
			}
			if v, ok := doc["model"]; ok {
				restore.ModelValue = v
			}
			if v, ok := doc["small_model"]; ok {
				restore.SmallModelValue = v
			}
			rj, _ := json.MarshalIndent(restore, "", "  ")
			if mkErr := os.MkdirAll(filepath.Dir(restorePath), 0o700); mkErr == nil {
				_ = os.WriteFile(restorePath, append(rj, '\n'), 0o600)
			}
		}
	}

	if mkErr := os.MkdirAll(openCodeConfigDir(targetHome), 0o700); mkErr != nil {
		return nil, mkErr
	}
	apiKey := ctx.APIKey
	if apiKey == "" {
		apiKey = "anypick-proxy"
	}
	if wErr := os.WriteFile(secretPath, []byte(apiKey), 0o600); wErr != nil {
		return nil, wErr
	}

	models := map[string]interface{}{}
	for _, id := range modelIDs {
		models[id] = map[string]string{"name": id}
	}
	entry := map[string]interface{}{
		"npm":  "@ai-sdk/openai-compatible",
		"name": "AnyPick",
		"options": map[string]interface{}{
			"baseURL": endpointForOpenCode(ctx.Endpoint),
			"apiKey":  fmt.Sprintf("{file:%s}", secretPath),
		},
		"models": models,
	}
	ej, _ := json.Marshal(entry)
	providers[opencodeManagedProviderID] = ej
	pj, _ := json.Marshal(providers)
	doc["provider"] = pj
	doc["model"], _ = json.Marshal(opencodeManagedProviderID + "/" + modelIDs[0])

	out, err := json.MarshalIndent(map[string]json.RawMessage(doc), "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(configPath, append(out, '\n'), 0o600); err != nil {
		return nil, err
	}
	managedPaths := []string{configPath, secretPath}
	if ctx.IsolatedHome == "" {
		managedPaths = append(managedPaths, restorePath)
	}
	return &ApplyResult{ManagedPaths: managedPaths}, nil
}

// openCodeProviders extracts doc.provider as raw entries keyed by provider id.
func openCodeProviders(doc map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	raw, ok := doc["provider"]
	if !ok || string(raw) == "null" {
		return map[string]json.RawMessage{}, nil
	}
	m := map[string]json.RawMessage{}
	if json.Unmarshal(raw, &m) != nil {
		return nil, anyerr.Newf("OpenCode config.provider must be an object.", "CLIENT_CONFIG_INVALID")
	}
	return m, nil
}

func (a *opencodeAdapter) Reset() error {
	configPath := openCodeEffectiveConfigPath(a.home)
	restorePath := filepath.Join(anyPickRootFallback(), "clients", "opencode", opencodeRestoreFile)

	doc, err := readOpenCodeConfig(configPath)
	if err != nil {
		return err
	}
	providers, perr := openCodeProviders(doc)
	if perr != nil {
		return perr
	}
	delete(providers, opencodeManagedProviderID)

	if raw, rerr := os.ReadFile(restorePath); rerr == nil {
		var restore openCodeRestoreState
		if json.Unmarshal(raw, &restore) == nil {
			if restore.ProviderPresent && restore.ProviderValue != nil {
				providers[opencodeManagedProviderID] = restore.ProviderValue
			}
			if restore.ModelPresent && restore.ModelValue != nil {
				doc["model"] = restore.ModelValue
			} else {
				delete(doc, "model")
			}
			if restore.SmallModelPresent && restore.SmallModelValue != nil {
				doc["small_model"] = restore.SmallModelValue
			} else {
				delete(doc, "small_model")
			}
			_ = os.Remove(restorePath)
		}
	} else if v, ok := doc["model"]; ok {
		var s string
		if json.Unmarshal(v, &s) == nil && strings.HasPrefix(s, opencodeManagedProviderID+"/") {
			delete(doc, "model")
		}
	}

	if len(providers) > 0 {
		pj, _ := json.Marshal(providers)
		doc["provider"] = pj
	} else {
		delete(doc, "provider")
	}
	out, jerr := json.MarshalIndent(map[string]json.RawMessage(doc), "", "  ")
	if jerr != nil {
		return jerr
	}
	if err := os.WriteFile(configPath, append(out, '\n'), 0o600); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(openCodeConfigDir(a.home), ".anypick-api-key"))
	return nil
}

func (a *opencodeAdapter) Inspect() (*InspectResult, error) {
	configPath := openCodeEffectiveConfigPath(a.home)
	if _, err := os.Stat(configPath); err != nil {
		return &InspectResult{Present: false}, nil
	}
	res := &InspectResult{Present: true}
	doc, err := readOpenCodeConfig(configPath)
	if err != nil {
		res.Issues = []string{"OpenCode config unreadable"}
		return res, nil
	}
	providers, perr := openCodeProviders(doc)
	if perr == nil {
		if _, managed := providers[opencodeManagedProviderID]; managed {
			res.Summary = "AnyPick provider configured"
		} else {
			res.Summary = "config present (no AnyPick provider)"
		}
	}
	return res, nil
}

func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}
