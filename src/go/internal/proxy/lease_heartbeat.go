package proxy

import (
	"context"
	"time"
)

// startLeaseHeartbeat refreshes one serving lease until the owning service
// stops it. A heartbeat failure is intentionally non-fatal: the listener's
// instance echo remains the authoritative ownership proof, and maintenance
// will only reap an expired lease after that proof fails too.
func (s *Service) startLeaseHeartbeat(leaseID, instanceID string, ownerPID int) {
	if s.Jobs == nil || leaseID == "" || instanceID == "" || ownerPID <= 0 {
		return
	}
	s.runHeartbeat(leaseID, func(c context.Context) {
		_ = s.Jobs.HeartbeatContext(c, leaseID, instanceID, ownerPID)
	})
}

// startInstanceHeartbeat refreshes every lease owned by one serving instance.
// The detached child owns the lease the parent created but never learns the
// lease id, so it refreshes by the instance id it echoes on /health instead.
// Without it, a long-lived child would look stale as soon as the parent that
// started it exits.
func (s *Service) startInstanceHeartbeat(instanceID string, ownerPID int) {
	if s.Jobs == nil || instanceID == "" || ownerPID <= 0 {
		return
	}
	s.runHeartbeat("instance:"+instanceID, func(c context.Context) {
		_ = s.Jobs.HeartbeatInstanceContext(c, instanceID, ownerPID)
	})
}

// runHeartbeat ticks beat every HeartbeatInterval under a keyed cancel func.
// Starting under a key that is already beating replaces the old beat rather
// than running two of them against the same row.
func (s *Service) runHeartbeat(key string, beat func(context.Context)) {
	heartbeatCtx, cancel := context.WithCancel(context.Background())
	s.heartbeatMu.Lock()
	if previous := s.heartbeats[key]; previous != nil {
		previous()
	}
	s.heartbeats[key] = cancel
	s.heartbeatMu.Unlock()
	go func() {
		ticker := time.NewTicker(HeartbeatInterval)
		defer ticker.Stop()
		defer func() {
			s.heartbeatMu.Lock()
			if current := s.heartbeats[key]; current != nil {
				delete(s.heartbeats, key)
			}
			s.heartbeatMu.Unlock()
		}()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				beat(heartbeatCtx)
			}
		}
	}()
}

func (s *Service) stopLeaseHeartbeat(leaseID string) {
	if leaseID == "" {
		return
	}
	s.heartbeatMu.Lock()
	if cancel := s.heartbeats[leaseID]; cancel != nil {
		cancel()
		delete(s.heartbeats, leaseID)
	}
	s.heartbeatMu.Unlock()
}

// stopInstanceHeartbeat stops the beat started by startInstanceHeartbeat.
func (s *Service) stopInstanceHeartbeat(instanceID string) {
	if instanceID == "" {
		return
	}
	s.heartbeatMu.Lock()
	key := "instance:" + instanceID
	if cancel := s.heartbeats[key]; cancel != nil {
		cancel()
		delete(s.heartbeats, key)
	}
	s.heartbeatMu.Unlock()
}
