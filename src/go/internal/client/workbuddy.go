package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/transport"
)

// workbuddyAdapter makes WorkBuddy AI / CodeBuddy a *client*: both apps read a
// user-level models.json and treat every entry in it as an extra
// OpenAI-compatible provider. It is Tencent's own extension mechanism
// (documented for the CodeBuddy IDE and CLI, and implemented for the WorkBuddy
// product in its own binary), so this writes a supported file instead of
// patching application internals.
//
// The entry points at AnyPick's loopback proxy, which is what actually holds
// the borrowed account.
type workbuddyAdapter struct{ home string }

func newWorkBuddy(home string) *workbuddyAdapter { return &workbuddyAdapter{home: home} }

const (
	workbuddyModelsFile  = "models.json"
	workbuddyVendorID    = "anypick"
	workbuddyRestoreFile = "restore.json"
)

// workbuddyCandidateDirs are the per-product home directories that can hold a
// user-level models.json, in the order AnyPick prefers them.
var workbuddyCandidateDirs = []string{".workbuddy-ai", ".workbuddy", ".codebuddy"}

// workbuddyModelEntry mirrors one models.json entry. Fields are the documented
// ones; unknown fields on entries AnyPick keeps are preserved as raw JSON.
type workbuddyModelEntry struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Vendor string `json:"vendor,omitempty"`
	APIKey string `json:"apiKey,omitempty"`
	URL    string `json:"url,omitempty"`

	raw map[string]json.RawMessage
}

func (a *workbuddyAdapter) ID() string        { return "workbuddy" }
func (a *workbuddyAdapter) Name() string      { return "WorkBuddy AI" }
func (a *workbuddyAdapter) ShortName() string { return "WorkBuddy" }

func (a *workbuddyAdapter) Capabilities() *Capabilities {
	return &Capabilities{
		ID:                       "workbuddy",
		AcceptedProtocols:        []transport.Protocol{transport.OpenAI},
		SupportsPersistentConfig: true,
		ProtocolPreference:       transport.OpenAI,
	}
}

// workbuddyModelsPaths returns every models.json AnyPick should write.
//
// Which directory the app really reads depends on how it was launched: the
// WorkBuddy desktop app exports WORKBUDDY_CONFIG_DIR to itself and its
// children, so a session started from the app resolves to ~/.workbuddy-ai while
// the same app started from Finder — and AnyPick started from a plain Terminal,
// which inherits neither — resolves to ~/.workbuddy. Picking one is a coin
// flip that fails silently. An explicit override settles it; otherwise AnyPick
// writes to every candidate that exists, which costs at most two small files
// and always lands in the one the app is reading.
func workbuddyModelsPaths(home string) []string {
	if dir := workbuddyConfigDirOverride(); dir != "" {
		return []string{filepath.Join(dir, workbuddyModelsFile)}
	}
	var out []string
	for _, dir := range workbuddyCandidateDirs {
		if st, err := os.Stat(filepath.Join(home, dir)); err == nil && st.IsDir() {
			out = append(out, filepath.Join(home, dir, workbuddyModelsFile))
		}
	}
	if len(out) == 0 {
		// No product has run here yet; target the current one's directory.
		out = append(out, filepath.Join(home, workbuddyCandidateDirs[0], workbuddyModelsFile))
	}
	return out
}

func workbuddyConfigDirOverride() string {
	for _, key := range []string{"WORKBUDDY_CONFIG_DIR", "CODEBUDDY_CONFIG_DIR"} {
		v := strings.TrimSpace(os.Getenv(key))
		if v == "" {
			continue
		}
		if home, err := os.UserHomeDir(); err == nil && (v == "~" || strings.HasPrefix(v, "~/")) {
			return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(v, "~"), "/"))
		}
		return v
	}
	return ""
}

// readWorkBuddyModels loads one file, refusing to touch it if it is not a JSON
// object — a models.json we cannot parse belongs to the user, not to us.
func readWorkBuddyModels(path string) (map[string]json.RawMessage, error) {
	doc := map[string]json.RawMessage{}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return doc, nil
		}
		return nil, err
	}
	if json.Unmarshal(raw, &doc) != nil || !jsonLooksObject(raw) {
		return nil, anyerr.Newf(
			"Refusing to overwrite unreadable WorkBuddy models file at "+path+".",
			"CLIENT_CONFIG_INVALID")
	}
	return doc, nil
}

func workbuddyEntries(doc map[string]json.RawMessage) []workbuddyModelEntry {
	raw, ok := doc["models"]
	if !ok || string(raw) == "null" {
		return nil
	}
	var raws []json.RawMessage
	if json.Unmarshal(raw, &raws) != nil {
		return nil
	}
	entries := make([]workbuddyModelEntry, 0, len(raws))
	for _, item := range raws {
		var e workbuddyModelEntry
		if json.Unmarshal(item, &e) != nil {
			continue
		}
		// Keep unknown fields so a future schema change survives a round trip.
		_ = json.Unmarshal(item, &e.raw)
		entries = append(entries, e)
	}
	return entries
}

func writeWorkBuddyModels(path string, doc map[string]json.RawMessage, entries []workbuddyModelEntry) error {
	items := make([]json.RawMessage, 0, len(entries))
	for _, e := range entries {
		body, err := json.Marshal(e)
		if err != nil {
			return err
		}
		if e.raw == nil {
			items = append(items, body)
			continue
		}
		// Start from the original object so fields we do not model survive,
		// then let the known fields win.
		merged := map[string]json.RawMessage{}
		for k, v := range e.raw {
			merged[k] = v
		}
		var known map[string]json.RawMessage
		if json.Unmarshal(body, &known) == nil {
			for k, v := range known {
				merged[k] = v
			}
		}
		out, err := json.Marshal(merged)
		if err != nil {
			return err
		}
		items = append(items, out)
	}
	list, err := json.Marshal(items)
	if err != nil {
		return err
	}
	doc["models"] = list
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o600)
}

// workbuddyFileCheckpoint is what Reset needs to put one file back exactly as
// it was: whether it existed at all, and its prior models list if it did.
type workbuddyFileCheckpoint struct {
	Path        string          `json:"path"`
	Present     bool            `json:"present"`
	ModelsValue json.RawMessage `json:"modelsValue,omitempty"`
}

type workbuddyRestoreState struct {
	Version int                       `json:"version"`
	Files   []workbuddyFileCheckpoint `json:"files"`
}

func (a *workbuddyAdapter) Apply(ctx *ApplyContext) (*ApplyResult, error) {
	targetHome := ctx.Home
	if ctx.IsolatedHome != "" {
		targetHome = ctx.IsolatedHome
	}
	modelsPaths := workbuddyModelsPaths(targetHome)
	restorePath := filepath.Join(ctx.AnyPickRoot, "clients", "workbuddy", workbuddyRestoreFile)

	modelID := firstNonEmpty(ctx.ModelRoles["default"], ctx.DefaultModel)
	if ctx.Endpoint == "" || modelID == "" {
		return nil, anyerr.Newf(
			"WorkBuddy routing needs an endpoint and a selected default model.",
			"CLIENT_CONFIG_INVALID")
	}
	managed := append([]string(nil), modelsPaths...)
	if ctx.IsolatedHome == "" {
		managed = append(managed, restorePath)
	}
	if ctx.DryRun {
		return &ApplyResult{ManagedPaths: managed}, nil
	}

	apiKey := ctx.APIKey
	if apiKey == "" {
		apiKey = "anypick-proxy"
	}
	entry := workbuddyModelEntry{
		ID:     modelID,
		Name:   "AnyPick " + modelID,
		Vendor: workbuddyVendorID,
		APIKey: apiKey,
		// The proxy serves an OpenAI-compatible surface under /v1.
		URL: strings.TrimSuffix(ctx.Endpoint, "/") + "/v1",
	}

	checkpoints := make([]workbuddyFileCheckpoint, 0, len(modelsPaths))
	for _, path := range modelsPaths {
		doc, err := readWorkBuddyModels(path)
		if err != nil {
			return nil, err
		}
		// Checkpoint each file once, before the first write, so Reset can put
		// the user's list back even if AnyPick ran several times.
		if ctx.IsolatedHome == "" {
			checkpoints = append(checkpoints, workbuddyFileCheckpoint{
				Path: path, Present: doc["models"] != nil, ModelsValue: doc["models"],
			})
		}
		kept := make([]workbuddyModelEntry, 0, len(workbuddyEntries(doc))+1)
		for _, existing := range workbuddyEntries(doc) {
			// AnyPick owns exactly its own entries; the user's stay untouched.
			if strings.EqualFold(existing.Vendor, workbuddyVendorID) {
				continue
			}
			kept = append(kept, existing)
		}
		kept = append(kept, entry)
		if err := writeWorkBuddyModels(path, doc, kept); err != nil {
			return nil, err
		}
	}

	// Write the checkpoint after every file succeeded: a checkpoint that
	// claims more than was written would make Reset delete user data.
	if ctx.IsolatedHome == "" {
		if _, statErr := os.Stat(restorePath); os.IsNotExist(statErr) {
			state := workbuddyRestoreState{Version: 1, Files: checkpoints}
			if rj, merr := json.MarshalIndent(state, "", "  "); merr == nil {
				if mkErr := os.MkdirAll(filepath.Dir(restorePath), 0o700); mkErr == nil {
					_ = os.WriteFile(restorePath, append(rj, '\n'), 0o600)
				}
			}
		}
	}
	return &ApplyResult{ManagedPaths: managed}, nil
}

func (a *workbuddyAdapter) Reset() error {
	restorePath := filepath.Join(anyPickRootFallback(), "clients", "workbuddy", workbuddyRestoreFile)

	raw, rerr := os.ReadFile(restorePath)
	if rerr == nil {
		var state workbuddyRestoreState
		if json.Unmarshal(raw, &state) == nil {
			for _, file := range state.Files {
				if rerr := resetWorkBuddyFile(file); rerr != nil {
					return rerr
				}
			}
			_ = os.Remove(restorePath)
			return nil
		}
	}
	// No usable checkpoint: fall back to removing only the entries AnyPick
	// recognises, leaving every file the user had alone.
	for _, path := range workbuddyModelsPaths(a.home) {
		doc, err := readWorkBuddyModels(path)
		if err != nil {
			return err
		}
		kept := make([]workbuddyModelEntry, 0, len(workbuddyEntries(doc)))
		for _, entry := range workbuddyEntries(doc) {
			if strings.EqualFold(entry.Vendor, workbuddyVendorID) {
				continue
			}
			kept = append(kept, entry)
		}
		if err := writeWorkBuddyModels(path, doc, kept); err != nil {
			return err
		}
	}
	return nil
}

func resetWorkBuddyFile(file workbuddyFileCheckpoint) error {
	if !file.Present {
		// AnyPick created this file; remove it rather than leaving a stub.
		if err := os.Remove(file.Path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	doc, err := readWorkBuddyModels(file.Path)
	if err != nil {
		return err
	}
	if file.ModelsValue != nil {
		doc["models"] = file.ModelsValue
	} else {
		delete(doc, "models")
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(file.Path, append(out, '\n'), 0o600)
}

func (a *workbuddyAdapter) Inspect() (*InspectResult, error) {
	paths := workbuddyModelsPaths(a.home)
	res := &InspectResult{ConfigPaths: paths}
	managed := false
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			continue
		}
		res.Present = true
		doc, err := readWorkBuddyModels(path)
		if err != nil {
			res.Issues = append(res.Issues, "WorkBuddy models.json is unreadable at "+path)
			continue
		}
		for _, entry := range workbuddyEntries(doc) {
			if strings.EqualFold(entry.Vendor, workbuddyVendorID) {
				managed = true
				break
			}
		}
	}
	switch {
	case !res.Present:
		res.Summary = "no models.json"
	case managed:
		res.Summary = "AnyPick provider configured"
	default:
		res.Summary = "models.json present (no AnyPick entry)"
	}
	return res, nil
}
