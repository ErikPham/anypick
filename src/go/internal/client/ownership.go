package client

import (
	"fmt"
	"sort"
	"strings"

	"github.com/outof0/anypick/go/internal/anyerr"
)

// ConfigMutation describes one region an adapter is about to write or delete:
// a file plus a selector within it ("env.ANTHROPIC_BASE_URL",
// "provider[opencode]", "# BEGIN anypick-managed"). Selectors are stable,
// value-free identifiers so conflicts can be surfaced and journaled without
// ever exposing user config or secrets.
type ConfigMutation struct {
	Path     string
	Selector string
	// Operation is "write" or "delete"; empty means "write".
	Operation string
}

// OwnershipEvidence records the regions AnyPick already owns in a file, as
// established by an AnyPick marker or by an AnyPick checkpoint of what Apply
// wrote earlier. Selectors not listed are presumed foreign when present.
type OwnershipEvidence struct {
	// Owned maps path → set of owned selectors.
	Owned map[string]map[string]bool
	// FileOwned marks files (or whole regions) whose ownership is established
	// wholesale, e.g. a file AnyPick generated entirely.
	FileOwned map[string]bool
}

func (e *OwnershipEvidence) owns(m ConfigMutation) bool {
	if e == nil {
		return false
	}
	if e.FileOwned[m.Path] {
		return true
	}
	return e.Owned[m.Path][m.Selector]
}

// ConfigConflict is one foreign region an apply would overwrite.
type ConfigConflict struct {
	ClientID  string `json:"clientId"`
	Path      string `json:"path"`
	Selector  string `json:"selector"`
	Operation string `json:"operation"`
}

// CheckOwnership returns the mutations whose current targets exist but are
// not owned by AnyPick. Existence itself is checked by the caller: it reads
// the live documents while parsing them anyway and records, per mutation,
// whether the selector currently holds a value. Only present-and-foreign
// selectors become conflicts.
func CheckOwnership(clientID string, present []ConfigMutation, owned *OwnershipEvidence) []ConfigConflict {
	var conflicts []ConfigConflict
	for _, m := range present {
		if owned.owns(m) {
			continue
		}
		conflicts = append(conflicts, ConfigConflict{
			ClientID:  clientID,
			Path:      m.Path,
			Selector:  m.Selector,
			Operation: m.Operation,
		})
	}
	sort.Slice(conflicts, func(i, j int) bool {
		if conflicts[i].Path != conflicts[j].Path {
			return conflicts[i].Path < conflicts[j].Path
		}
		return conflicts[i].Selector < conflicts[j].Selector
	})
	return conflicts
}

// ForeignConfigError builds the fail-closed error for a set of conflicts.
// It never embeds config values — only paths and selectors.
//
// The selectors are named so the user can see which lines are at stake without
// AnyPick quoting their values, and both escape hatches are real: the CLI flag
// exists (cmd/anypick `use --allow-foreign-config`) and the app confirms the
// same consent. Every takeover is preceded by a copy of the file into
// ANYPICK_HOME/recovery/clients/<client>/, so the suggestion to replace is not
// a suggestion to lose anything.
func ForeignConfigError(conflicts []ConfigConflict) error {
	if len(conflicts) == 0 {
		return nil
	}
	clientID := conflicts[0].ClientID
	paths := map[string]bool{}
	selectors := map[string]bool{}
	for _, c := range conflicts {
		paths[c.Path] = true
		if c.Selector != "" {
			selectors[c.Selector] = true
		}
	}
	list := make([]string, 0, len(paths))
	for p := range paths {
		list = append(list, p)
	}
	sort.Strings(list)
	detail := strings.Join(list, ", ")
	if len(selectors) > 0 {
		keys := make([]string, 0, len(selectors))
		for s := range selectors {
			keys = append(keys, s)
		}
		sort.Strings(keys)
		detail += " — " + strings.Join(keys, ", ")
	}
	return anyerr.Newf(
		fmt.Sprintf(
			"%s config contains settings not managed by AnyPick (%s). Nothing was changed; confirm the takeover to replace them.",
			clientID, detail),
		"CLIENT_CONFIG_FOREIGN",
		anyerr.WithMutated(false),
		anyerr.WithSuggestions(
			"Re-run with --allow-foreign-config to back up and replace these settings.",
			"In the AnyPick app, confirm the takeover when it offers to manage this app.",
			"Or remove the conflicting values yourself and retry."))
}
