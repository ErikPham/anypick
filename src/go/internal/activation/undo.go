package activation

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/bindings"
	"github.com/outof0/anypick/go/internal/client"
	"github.com/outof0/anypick/go/internal/hub"
	"github.com/outof0/anypick/go/internal/journal"
)

// journalActivationUndo appends one compensation to the activation's journal
// before the mutation it undoes. The in-memory undo stack dies with the
// process; a restarted process replays params["activationUndo"] (invariant #3).
// An empty journal id is the direct step-test path, which has no entry to
// update. Execute always sets one, and a missing entry there refuses the write.
func journalActivationUndo(ctx context.Context, deps *Deps, journalID string, record map[string]any) error {
	if strings.TrimSpace(journalID) == "" {
		return nil
	}
	if deps == nil || deps.Journal == nil {
		return anyerr.Newf("activation journal is unavailable", "STATE_CONFLICT")
	}
	entry, err := deps.Journal.GetContext(ctx, journalID)
	if err != nil {
		return err
	}
	if entry == nil {
		return anyerr.Newf("activation journal "+journalID+" disappeared before its compensation was recorded", "STATE_CONFLICT")
	}
	next := map[string]any{}
	for k, v := range entry.Params {
		next[k] = v
	}
	var list []any
	if raw, ok := next["activationUndo"]; ok && raw != nil {
		existing, ok := raw.([]any)
		if !ok {
			return anyerr.Newf("activation journal compensation is unreadable", "STATE_CONFLICT")
		}
		list = append([]any{}, existing...)
	}
	list = append(list, record)
	next["activationUndo"] = list
	_, err = deps.Journal.UpdateContext(ctx, journalID, journal.Entry{Params: next})
	return err
}

func hubRouteUndoRecord(routeID string, prev *hub.RouteRowSnapshot) map[string]any {
	rec := map[string]any{"kind": "hub-route", "routeId": routeID}
	if prev == nil {
		return rec
	}
	rec["snapshot"] = map[string]any{
		"routeId":      prev.RouteID,
		"manifestJSON": prev.ManifestJSON,
		"tokenSecret":  prev.TokenSecret,
		"createdAt":    prev.CreatedAt,
		"updatedAt":    prev.UpdatedAt,
	}
	return rec
}

// asParam encodes a struct the way the journal will store it, so a later
// read does not depend on the in-memory Go type.
func asParam(v any) (any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func paramInto(raw, dest any) error {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, dest)
}

// applyActivationUndo replays journalled compensations in reverse. A no-op
// (the row already matches) still counts as applied: the compensation is done.
func applyActivationUndo(ctx context.Context, deps *Deps, raw any) (bool, []string) {
	list, ok := raw.([]any)
	if !ok {
		return false, []string{"activation-undo"}
	}
	if len(list) == 0 {
		return false, nil
	}
	var errs []string
	for i := len(list) - 1; i >= 0; i-- {
		rec, ok := list[i].(map[string]any)
		if !ok {
			errs = append(errs, "activation-undo")
			continue
		}
		if err := applyOneActivationUndo(ctx, deps, rec); err != nil {
			errs = append(errs, "activation-undo")
		}
	}
	// Every record was attempted. Success is "nothing left unreadable", and
	// the caller marks the entry rolled back only when errs is empty.
	return len(errs) == 0, errs
}

func applyOneActivationUndo(ctx context.Context, deps *Deps, rec map[string]any) error {
	kind, _ := rec["kind"].(string)
	switch kind {
	case "hub-port":
		return undoHubPort(ctx, deps, rec)
	case "hub-route":
		return undoHubRoute(ctx, deps, rec)
	case "global-binding":
		return undoGlobalBinding(ctx, deps, rec)
	case "project-binding":
		return undoProjectBinding(ctx, deps, rec)
	case "client-profile":
		return undoClientProfile(ctx, deps, rec)
	default:
		return anyerr.Newf("unknown activation compensation "+kind, "STATE_CONFLICT")
	}
}

func undoHubPort(ctx context.Context, deps *Deps, rec map[string]any) error {
	if deps == nil || deps.Hub == nil {
		return anyerr.Newf("hub store is unavailable for port compensation", "STATE_CONFLICT")
	}
	name, _ := rec["hubName"].(string)
	prev, prevOK := undoInt(rec["previousPort"])
	picked, pickedOK := undoInt(rec["pickedPort"])
	if name == "" || !prevOK || !pickedOK {
		return anyerr.Newf("hub port compensation is incomplete", "STATE_CONFLICT")
	}
	cur, err := deps.Hub.GetConfigContext(ctx, name)
	if err != nil {
		return err
	}
	// Something else already took the port. Leave it, the same way the
	// in-process undo does.
	if cur == nil || cur.Port != picked {
		return nil
	}
	cur.Port = prev
	_, err = deps.Hub.UpsertConfigContext(ctx, *cur)
	return err
}

func undoHubRoute(ctx context.Context, deps *Deps, rec map[string]any) error {
	if deps == nil || deps.Hub == nil {
		return anyerr.Newf("hub store is unavailable for route compensation", "STATE_CONFLICT")
	}
	routeID, _ := rec["routeId"].(string)
	if routeID == "" {
		return anyerr.Newf("hub route compensation is missing its route id", "STATE_CONFLICT")
	}
	raw, ok := rec["snapshot"]
	if !ok || raw == nil {
		return deps.Hub.DetachRouteContext(ctx, routeID)
	}
	// RouteRowSnapshot has no JSON tags. The journal record uses the names
	// written by hubRouteUndoRecord, so decode through a tagged copy.
	var stored struct {
		RouteID      string `json:"routeId"`
		ManifestJSON string `json:"manifestJSON"`
		TokenSecret  string `json:"tokenSecret"`
		CreatedAt    string `json:"createdAt"`
		UpdatedAt    string `json:"updatedAt"`
	}
	if err := paramInto(raw, &stored); err != nil {
		return err
	}
	if stored.RouteID == "" || stored.ManifestJSON == "" {
		return anyerr.Newf("hub route compensation snapshot is incomplete", "STATE_CONFLICT")
	}
	return deps.Hub.RestoreRouteRowContext(ctx, &hub.RouteRowSnapshot{
		RouteID: stored.RouteID, ManifestJSON: stored.ManifestJSON,
		TokenSecret: stored.TokenSecret, CreatedAt: stored.CreatedAt, UpdatedAt: stored.UpdatedAt,
	})
}

func undoGlobalBinding(ctx context.Context, deps *Deps, rec map[string]any) error {
	if deps == nil || deps.Bindings == nil {
		return anyerr.Newf("binding store is unavailable for compensation", "STATE_CONFLICT")
	}
	clientID, _ := rec["client"].(string)
	if clientID == "" {
		return anyerr.Newf("global binding compensation is missing its client", "STATE_CONFLICT")
	}
	raw, ok := rec["previous"]
	if !ok || raw == nil {
		return deps.Bindings.DeleteGlobalContext(ctx, clientID)
	}
	var previous bindings.Global
	if err := paramInto(raw, &previous); err != nil {
		return err
	}
	_, err := deps.Bindings.UpsertGlobalContext(ctx, previous)
	return err
}

func undoProjectBinding(ctx context.Context, deps *Deps, rec map[string]any) error {
	if deps == nil || deps.Bindings == nil {
		return anyerr.Newf("binding store is unavailable for compensation", "STATE_CONFLICT")
	}
	clientID, _ := rec["client"].(string)
	root, _ := rec["projectRoot"].(string)
	if clientID == "" || root == "" {
		return anyerr.Newf("project binding compensation is incomplete", "STATE_CONFLICT")
	}
	raw, ok := rec["previous"]
	if !ok || raw == nil {
		return deps.Bindings.DeleteProjectContext(ctx, root, clientID)
	}
	var previous bindings.Project
	if err := paramInto(raw, &previous); err != nil {
		return err
	}
	_, err := deps.Bindings.UpsertProjectContext(ctx, previous)
	return err
}

func undoClientProfile(ctx context.Context, deps *Deps, rec map[string]any) error {
	if deps == nil || deps.ClientProfiles == nil {
		return anyerr.Newf("client profile store is unavailable for compensation", "STATE_CONFLICT")
	}
	clientID, _ := rec["client"].(string)
	name, _ := rec["profile"].(string)
	if clientID == "" || name == "" {
		return anyerr.Newf("client profile compensation is incomplete", "STATE_CONFLICT")
	}
	raw, ok := rec["previous"]
	if !ok || raw == nil {
		return deps.ClientProfiles.DeleteContext(ctx, clientID, name)
	}
	var previous client.NamedProfile
	if err := paramInto(raw, &previous); err != nil {
		return err
	}
	_, err := deps.ClientProfiles.UpsertContext(ctx, previous.Client, previous.Name, previous.Spec)
	return err
}

func undoInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil
	default:
		return 0, false
	}
}
