package hub

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/outof0/anypick/go/internal/ownership"
)

// ChildRevision reads the config revision a running hub child serves on its
// /health endpoint. It reports nothing about identity: callers that signal
// the child must still verify ownership through the pidfile record.
func ChildRevision(endpoint string) (int, bool) {
	_, revision, ok := childIdentity(endpoint)
	return revision, ok
}

// ChildSynced reports whether the child answering endpoint runs wantRevision.
// want <= 0 never matches, so children predating revisions always restart.
func ChildSynced(endpoint string, wantRevision int) bool {
	if wantRevision <= 0 {
		return false
	}
	_, revision, ok := childIdentity(endpoint)
	return ok && revision == wantRevision
}

func childIdentity(endpoint string) (string, int, bool) {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(strings.TrimSuffix(endpoint, "/") + "/health")
	if err != nil {
		return "", 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", 0, false
	}
	var payload struct {
		OK         bool   `json:"ok"`
		InstanceID string `json:"instanceId"`
		Revision   int    `json:"revision"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&payload) != nil {
		return "", 0, false
	}
	if !payload.OK {
		return "", 0, false
	}
	return payload.InstanceID, payload.Revision, true
}

// ShouldPublishPort reports whether a starter should assign a free port
// before spawn. A live child whose port is still 0 stays unpublished: a
// stored port the child is not bound to makes the next health check fail,
// and RestartStaleChild then refuses to signal it (ADR-0030).
func ShouldPublishPort(current int, childLive bool) bool {
	return current == 0 && !childLive
}

// HubsNeedingReload returns enabled hubs whose stored port can be checked
// and whose child is not serving the stored revision. Port 0 is omitted.
// The bound port is unknown, and a restart without an echo is forbidden.
func HubsNeedingReload(configs []Config, synced func(endpoint string, revision int) bool) []string {
	if synced == nil {
		synced = ChildSynced
	}
	var names []string
	for _, cfg := range configs {
		if !cfg.Enabled || cfg.Port == 0 {
			continue
		}
		endpoint := fmt.Sprintf("http://127.0.0.1:%d", cfg.Port)
		if synced(endpoint, cfg.Revision) {
			continue
		}
		names = append(names, cfg.Name)
	}
	return names
}

// ReloadUnsynced asks spawn to restart each hub from HubsNeedingReload.
// spawn is SpawnHubChild: an in-sync child is not in the list, so this does
// not restart a child that already serves the stored revision.
func ReloadUnsynced(configs []Config, spawn func(name string) (int, error)) error {
	if spawn == nil {
		return nil
	}
	var first error
	for _, name := range HubsNeedingReload(configs, ChildSynced) {
		if _, err := spawn(name); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// RestartStaleChild stops the hub child serving endpoint when it runs an
// older config revision than wantRevision, so the caller can spawn fresh.
// A child already in sync is adopted untouched (false, nil). An empty
// endpoint (kernel-assigned port the starter cannot address) keeps the old
// adopt behavior. Stopping is ownership-verified through the pidfile record
// plus the /health instance echo: a live process the record does not own is
// never signalled, and that refusal is an error, not a silent pass.
func RestartStaleChild(root, hubName, endpoint string, wantRevision int, timeout time.Duration) (bool, error) {
	if endpoint == "" || ChildSynced(endpoint, wantRevision) {
		return false, nil
	}
	pidFile := filepath.Join(root, "runtime", "hub", hubName, "service.pid")
	rec := ownership.Load(pidFile)
	if rec == nil {
		return false, fmt.Errorf("hub %q answers %s but has no trusted pid record; stop it manually", hubName, endpoint)
	}
	stopped := ownership.Stop(pidFile, ownership.StopOptions{
		Timeout: timeout,
		Health: func(r ownership.Record) bool {
			instance, _, ok := childIdentity(endpoint)
			return ok && instance == r.InstanceID
		},
	})
	if !stopped {
		return false, fmt.Errorf("could not verify ownership of hub %q; not signalling it", hubName)
	}
	return true, nil
}
