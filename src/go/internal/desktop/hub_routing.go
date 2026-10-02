package desktop

import (
	"context"
	"sort"
	"strings"

	"github.com/outof0/anypick/go/internal/lock"
	"github.com/outof0/anypick/go/internal/refs"
)

// hubRoutingSource is the display-safe part of a Hub source row needed to
// compile model ownership. The source id is intentionally kept opaque to the
// host; handlers turn it back into the provider/account pair inside Go.
type hubRoutingSource struct {
	id         string
	hub        string
	providerID string
	name       string
	label      string
	detail     string
	enabled    bool
}

type hubRoutingCandidate struct {
	id         string
	hub        string
	providerID string
	name       string
	label      string
	detail     string
	actionID   string
	// providerChoice offers the whole provider (failover across its
	// accounts) instead of one account. The handler pins the provider id
	// itself; SetModelOwner resolves it to the first attached source.
	providerChoice bool
}

type hubRoutingConflict struct {
	id         string
	kind       string
	title      string
	detail     string
	models     []string
	candidates []hubRoutingCandidate
}

// compileHubRouting is the engine counterpart of DesignHubRouting.compile in
// src/macos/HubRouting.swift. Both sides must use the same grouping and action
// ids: the native app renders the catalog, while these handlers perform the
// durable owner mutation after a picker selection.
func compileHubRouting(snap *Snapshot) ([]hubRoutingConflict, map[string]string) {
	sources := hubRoutingSources(snap)
	active := map[string]hubRoutingSource{}
	for id, source := range sources {
		if source.enabled {
			active[id] = source
		}
	}
	byModel := hubCatalogModels(snap, active)
	owners := hubPinnedOwners(snap, sources)
	publishable, choiceGroups, overlapGroups := hubRoutingGroups(byModel, owners)

	choice := hubSortedGroups(choiceGroups, false)
	overlap := hubSortedGroups(overlapGroups, true)
	return hubRoutingConflicts(choice, overlap, sources), publishable
}

func hubCatalogModels(snap *Snapshot, active map[string]hubRoutingSource) map[string]map[string]map[string]bool {
	// model -> catalog -> source ids. Keeping the catalog identity is what
	// distinguishes a same-provider account choice from an overlap between
	// different model providers.
	byModel := map[string]map[string]map[string]bool{}
	for _, row := range snap.HubCatalogs {
		accountID, _ := row["accountID"].(string)
		if accountID == "" || active[accountID].id == "" {
			continue
		}
		catalogID, _ := row["catalog"].(string)
		if catalogID == "" {
			continue
		}
		for _, model := range hubModelStrings(row["models"]) {
			if model == "" {
				continue
			}
			catalogs := byModel[model]
			if catalogs == nil {
				catalogs = map[string]map[string]bool{}
				byModel[model] = catalogs
			}
			accounts := catalogs[catalogID]
			if accounts == nil {
				accounts = map[string]bool{}
				catalogs[catalogID] = accounts
			}
			accounts[accountID] = true
		}
	}
	return byModel
}

func hubRoutingGroups(
	byModel map[string]map[string]map[string]bool,
	owners map[string]string,
) (map[string]string, map[string]*hubRoutingGroup, map[string]*hubRoutingGroup) {
	publishable := map[string]string{}
	choiceGroups := map[string]*hubRoutingGroup{}
	overlapGroups := map[string]*hubRoutingGroup{}
	models := make([]string, 0, len(byModel))
	for model := range byModel {
		models = append(models, model)
	}
	sort.Strings(models)

	for _, model := range models {
		catalogGroups := byModel[model]
		accountIDs := hubRoutingAccountIDs(catalogGroups)
		if owner := owners[model]; owner != "" && contains(accountIDs, owner) {
			publishable[model] = owner
			continue
		}

		if len(catalogGroups) == 1 {
			catalogID := ""
			var candidates []string
			for id, accounts := range catalogGroups {
				catalogID = id
				candidates = hubSortedSet(accounts)
			}
			if len(candidates) == 1 {
				publishable[model] = candidates[0]
				continue
			}
			key := catalogID + "\x00" + strings.Join(candidates, ",")
			group := choiceGroups[key]
			if group == nil {
				group = &hubRoutingGroup{catalogID: catalogID, accountIDs: candidates}
				choiceGroups[key] = group
			}
			group.models = append(group.models, model)
			continue
		}

		key := strings.Join(accountIDs, ",")
		group := overlapGroups[key]
		if group == nil {
			group = &hubRoutingGroup{accountIDs: accountIDs}
			overlapGroups[key] = group
		}
		group.models = append(group.models, model)
	}
	return publishable, choiceGroups, overlapGroups
}

func hubSortedGroups(groups map[string]*hubRoutingGroup, overlap bool) []*hubRoutingGroup {
	out := make([]*hubRoutingGroup, 0, len(groups))
	for _, group := range groups {
		out = append(out, group)
	}
	sort.Slice(out, func(i, j int) bool {
		if overlap {
			return strings.Join(out[i].accountIDs, "") < strings.Join(out[j].accountIDs, "")
		}
		if out[i].catalogID != out[j].catalogID {
			return out[i].catalogID < out[j].catalogID
		}
		return strings.Join(out[i].accountIDs, ",") < strings.Join(out[j].accountIDs, ",")
	})
	return out
}

func hubRoutingConflicts(
	choice, overlap []*hubRoutingGroup,
	sources map[string]hubRoutingSource,
) []hubRoutingConflict {
	conflicts := make([]hubRoutingConflict, 0, len(choice)+len(overlap))
	for index, group := range choice {
		group.models = sortedUnique(group.models)
		conflicts = append(conflicts, hubRoutingConflict{
			id:         "hub-source-choice-" + itoa(index+1),
			kind:       "source-choice",
			title:      hubCatalogDisplayName(group.catalogID),
			detail:     hubModelSummary(group.models),
			models:     group.models,
			candidates: hubRoutingCandidates(group.accountIDs, sources, "choice-"+itoa(index+1)),
		})
	}
	for index, group := range overlap {
		group.models = sortedUnique(group.models)
		var title string
		if len(group.models) == 1 {
			title = group.models[0]
		} else {
			title = itoa(len(group.models)) + " Models"
		}
		conflicts = append(conflicts, hubRoutingConflict{
			id:         "hub-conflict-" + itoa(index+1),
			kind:       "model-overlap",
			title:      title,
			detail:     hubModelSummary(group.models),
			models:     group.models,
			candidates: hubRoutingCandidates(group.accountIDs, sources, "overlap-"+itoa(index+1)),
		})
	}
	return conflicts
}

type hubRoutingGroup struct {
	catalogID  string
	models     []string
	accountIDs []string
}

func hubRoutingSources(snap *Snapshot) map[string]hubRoutingSource {
	out := make(map[string]hubRoutingSource, len(snap.HubSources))
	for _, row := range snap.HubSources {
		id, _ := row["id"].(string)
		if id == "" {
			continue
		}
		providerID, _ := row["providerId"].(string)
		name, _ := row["name"].(string)
		hubName, _ := row["hub"].(string)
		if hubName == "" {
			hubName = hubNameFromSourceID(id)
		}
		label, _ := row["label"].(string)
		if label == "" {
			label = name
		}
		detail, _ := row["detail"].(string)
		enabled, _ := row["enabled"].(bool)
		out[id] = hubRoutingSource{
			id: id, hub: hubName, providerID: providerID, name: name,
			label: label, detail: detail, enabled: enabled,
		}
	}
	return out
}

func hubRoutingCandidates(ids []string, sources map[string]hubRoutingSource, prefix string) []hubRoutingCandidate {
	out := make([]hubRoutingCandidate, 0, len(ids))
	for _, id := range ids {
		source, ok := sources[id]
		if !ok {
			continue
		}
		out = append(out, hubRoutingCandidate{
			id: id, hub: source.hub, providerID: source.providerID, name: source.name,
			label: source.label, detail: source.detail,
			actionID: prefix + ":" + id,
		})
	}
	// Provider-level choice, ahead of the accounts: choosing a provider pins
	// the group to it instead of one account. Only when the group spans
	// providers — inside one provider it would duplicate the accounts.
	byProvider := map[string][]string{}
	for _, id := range ids {
		if source, ok := sources[id]; ok && source.providerID != "" {
			byProvider[source.providerID] = append(byProvider[source.providerID], id)
		}
	}
	if len(byProvider) > 1 {
		pids := make([]string, 0, len(byProvider))
		for pid := range byProvider {
			pids = append(pids, pid)
		}
		sort.Strings(pids)
		providers := make([]hubRoutingCandidate, 0, len(pids))
		for _, pid := range pids {
			n := len(byProvider[pid])
			unit := "accounts"
			if n == 1 {
				unit = "account"
			}
			providers = append(providers, hubRoutingCandidate{
				id: "provider/" + pid, hub: sources[byProvider[pid][0]].hub, providerID: pid,
				label: pid, detail: itoa(n) + " " + unit,
				actionID:       prefix + ":provider/" + pid,
				providerChoice: true,
			})
		}
		out = append(providers, out...)
	}
	return out
}

func hubRoutingAccountIDs(groups map[string]map[string]bool) []string {
	seen := map[string]bool{}
	for _, accounts := range groups {
		for id := range accounts {
			seen[id] = true
		}
	}
	return hubSortedSet(seen)
}

func hubSortedSet(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func sortedUnique(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func hubModelStrings(value any) []string {
	switch values := value.(type) {
	case []string:
		return values
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if model, ok := value.(string); ok {
				out = append(out, model)
			}
		}
		return out
	default:
		return nil
	}
}

func hubPinnedOwners(snap *Snapshot, sources map[string]hubRoutingSource) map[string]string {
	out := map[string]string{}
	// Legacy rows store provider/account, not the full hub source id. Resolve
	// them in snapshot order — the same order Swift's first(where:) sees — so
	// both sides pick the same hub when one account is enabled in two of them.
	ordered := make([]string, 0, len(snap.HubSources))
	for _, row := range snap.HubSources {
		id, _ := row["id"].(string)
		if id == "" {
			continue
		}
		if _, ok := sources[id]; ok {
			ordered = append(ordered, id)
		}
	}
	for _, row := range snap.HubPublishedModels {
		needsChoice, _ := row["needsChoice"].(bool)
		if needsChoice {
			continue
		}
		model, _ := row["id"].(string)
		owner, _ := row["ownerAccountId"].(string)
		if model == "" || owner == "" {
			continue
		}
		if _, ok := sources[owner]; ok {
			out[model] = owner
			continue
		}
		// Accept rows written by older engine versions, which used
		// provider/account instead of the full hub source id.
		for _, id := range ordered {
			if strings.HasSuffix(id, "/"+owner) {
				out[model] = id
				break
			}
		}
	}
	return out
}

func hubNameFromSourceID(id string) string {
	parts := strings.SplitN(id, "/", 4)
	if len(parts) >= 2 && parts[0] == "hub" {
		return parts[1]
	}
	return "default"
}

func hubCatalogDisplayName(id string) string {
	switch id {
	case "anthropic":
		return "Claude"
	case "openai":
		return "OpenAI"
	case "gemini-api":
		return "Gemini"
	case "grok-api":
		return "Grok"
	case "opencode":
		return "OpenCode"
	case "openrouter":
		return "OpenRouter"
	default:
		if id == "" {
			return "Models"
		}
		return strings.ToUpper(id[:1]) + id[1:]
	}
}

func hubModelSummary(models []string) string {
	if len(models) == 0 {
		return "No models"
	}
	if len(models) == 1 {
		return models[0]
	}
	if len(models) == 2 {
		return models[0] + " and " + models[1]
	}
	return itoa(len(models)) + " models, including " + models[0]
}

// registerHubRoutingActions publishes both the conflict rows and the
// durable handlers behind their candidate action ids. The Swift layer can
// continue to compile the catalog locally, but a selection now has a real
// engine-side mutation to invoke.
func (s *Server) registerHubRoutingActions(snap *Snapshot, registry map[string]actionHandler) {
	conflicts, _ := compileHubRouting(snap)
	for _, conflict := range conflicts {
		candidates := make([]map[string]any, 0, len(conflict.candidates))
		for _, candidate := range conflict.candidates {
			candidates = append(candidates, map[string]any{
				"id":             candidate.id,
				"providerId":     candidate.providerID,
				"label":          candidate.label,
				"detail":         candidate.detail,
				"actionId":       candidate.actionID,
				"providerChoice": candidate.providerChoice,
			})
			models := append([]string(nil), conflict.models...)
			hubName := candidate.hub
			owner := candidate.providerID + "/" + candidate.name
			if candidate.providerChoice {
				owner = candidate.providerID
			}
			registry[candidate.actionID] = func(ctx context.Context, _ actionOptions) error {
				if s.Deps.Hub == nil {
					return nil
				}
				// The same scope the hub CLI and toggle handlers take: a pin
				// read-modify-writes the source list, so it must not race a
				// source toggle or another pin (invariant #5).
				return lock.WithMutationLock(ctx, s.Deps.Root,
					refs.MutationScopeForRef(refs.ProxyHubRef(hubName)), func(c context.Context) error {
						for _, model := range models {
							if err := s.Deps.Hub.SetModelOwner(c, hubName, model, owner); err != nil {
								return err
							}
						}
						return nil
					})
			}
		}
		snap.HubConflicts = append(snap.HubConflicts, map[string]any{
			"id":         conflict.id,
			"kind":       conflict.kind,
			"title":      conflict.title,
			"models":     conflict.models,
			"candidates": candidates,
		})
	}
	s.registerModelRoutingActions(snap, registry)
}

func (s *Server) registerModelRoutingActions(snap *Snapshot, registry map[string]actionHandler) {
	sources := hubRoutingSources(snap)
	active := map[string]hubRoutingSource{}
	for id, source := range sources {
		if source.enabled {
			active[id] = source
		}
	}
	byModel := hubCatalogModels(snap, active)
	for model, catalogMap := range byModel {
		accountIDs := hubRoutingAccountIDs(catalogMap)
		if len(accountIDs) <= 1 {
			continue
		}
		modelCandidates := hubRoutingCandidates(accountIDs, sources, "model-route:"+model)
		m := model
		for _, cand := range modelCandidates {
			hubName := cand.hub
			owner := cand.providerID + "/" + cand.name
			if cand.providerChoice {
				owner = cand.providerID
			}
			registry[cand.actionID] = func(ctx context.Context, _ actionOptions) error {
				if s.Deps.Hub == nil {
					return nil
				}
				return lock.WithMutationLock(ctx, s.Deps.Root,
					refs.MutationScopeForRef(refs.ProxyHubRef(hubName)), func(c context.Context) error {
						return s.Deps.Hub.SetModelOwner(c, hubName, m, owner)
					})
			}
		}
	}
}

func itoa(value int) string {
	// The ids in this file are small snapshot-local indexes. Avoid importing
	// fmt just for integer formatting in every model refresh.
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var digits [20]byte
	i := len(digits)
	for value > 0 {
		i--
		digits[i] = byte('0' + value%10)
		value /= 10
	}
	if negative {
		i--
		digits[i] = '-'
	}
	return string(digits[i:])
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
