// Package ownership implements ADR-0003 (PROC-01) structured detached-child
// ownership for every lifecycle the engine supervises (hub, pool, supervisor).
//
// A numeric PID is never ownership proof: after PID reuse (ABA), liveness
// checks signal an unrelated process. Every spawn therefore writes an
// instance-stamped record; every read fails closed on absent, corrupt,
// wrong-version, or legacy numeric files; and stop verifies the live process
// still carries the recorded command tokens before signalling its session
// group. Hub children additionally prove identity through an instance-id
// /health echo.
package ownership

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/outof0/anypick/go/internal/atomicfile"
)

// RecordVersion is bumped only when the record shape changes incompatibly;
// older shapes fail closed like legacy numeric files.
const RecordVersion = 1

// EnvInstanceID carries the spawn's instance id into the child so hub servers
// can echo it back at /health (ADR 0003 §3).
const EnvInstanceID = "ANYPICK_SERVICE_INSTANCE_ID"

// Record is the structured ownership record persisted next to each detached
// child at mode 0o600.
type Record struct {
	RecordVersion int      `json:"recordVersion"`
	InstanceID    string   `json:"instanceId"`
	PID           int      `json:"pid"`
	Command       []string `json:"command"`
	Executable    string   `json:"executable,omitempty"`
	ProcessStart  uint64   `json:"processStart,omitempty"`
	CreatedAt     string   `json:"createdAt"`
}

// NewInstanceID generates a random UUIDv4-shaped identifier (same shape as
// proxy.NewInstanceID — kept independent to avoid a package cycle).
func NewInstanceID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	const h = "0123456789abcdef"
	out := make([]byte, 0, 36)
	for i, v := range b {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			out = append(out, '-')
		}
		out = append(out, h[v>>4], h[v&0x0f])
	}
	return string(out), nil
}

// Spawn launches selfExe with args as a setsid session leader, injects the
// instance id into the child environment, and atomically persists the
// ownership record at pidPath. stdout/stderr append to logPath when non-empty.
func Spawn(selfExe string, args []string, pidPath, logPath string, extraEnv ...string) (*Record, error) {
	instanceID, err := NewInstanceID()
	if err != nil {
		return nil, fmt.Errorf("generate instance id: %w", err)
	}
	if mkErr := os.MkdirAll(filepath.Dir(pidPath), 0o700); mkErr != nil {
		return nil, mkErr
	}
	cmd := exec.Command(selfExe, args...)
	cmd.Env = append(append(os.Environ(), extraEnv...), EnvInstanceID+"="+instanceID)
	var output *os.File
	if logPath != "" {
		output, _ = os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	}
	if output == nil {
		output, err = os.OpenFile(os.DevNull, os.O_RDWR, 0)
		if err != nil {
			return nil, fmt.Errorf("open detached child output: %w", err)
		}
	}
	defer output.Close()
	cmd.Stdout, cmd.Stderr = output, output
	spawned, err := startDetached(cmd, instanceID)
	if err != nil {
		return nil, err
	}
	// Identity is captured only once the child has exec'd; reading it straight
	// after fork would record the parent's executable and start stamp.
	executable, processStart, identityErr := spawnIdentity(cmd.Process.Pid, args)
	if identityErr != nil {
		_ = spawned.terminate()
		_ = spawned.close()
		_, _ = cmd.Process.Wait()
		return nil, identityErr
	}
	rec := &Record{
		RecordVersion: RecordVersion,
		InstanceID:    instanceID,
		PID:           cmd.Process.Pid,
		Command:       append([]string(nil), args...),
		Executable:    executable,
		ProcessStart:  processStart,
		CreatedAt:     time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00"),
	}
	if err := writeAtomic(pidPath, rec); err != nil {
		_ = spawned.terminate()
		_ = spawned.close()
		_, _ = cmd.Process.Wait()
		return nil, err
	}
	go func() {
		_, _ = cmd.Process.Wait()
		_ = spawned.close()
	}() // reap and release the platform process-group/job handle
	return rec, nil
}

func writeAtomic(path string, rec *Record) error {
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	// atomicfile uses a randomly named staging file. The fixed ".tmp" suffix
	// this used to write meant two concurrent writers raced on one path.
	return atomicfile.Write(path, raw, 0o600)
}

// Load reads the ownership record and fails closed: it returns nil when the
// file is absent, corrupt, wrong-version, or a legacy bare-PID file. Legacy
// numeric files are removed — they are never trusted, and the next start
// re-creates the record (ADR 0003 §2/§Consequences).
func Load(pidPath string) *Record {
	raw, err := os.ReadFile(pidPath)
	if err != nil {
		return nil
	}
	var rec Record
	if jerr := json.Unmarshal(raw, &rec); jerr != nil ||
		rec.RecordVersion != RecordVersion ||
		rec.PID <= 0 || len(rec.Command) == 0 ||
		len(rec.InstanceID) != 36 {
		_ = os.Remove(pidPath)
		return nil
	}
	return &rec
}

// Alive reports whether the recorded pid currently exists (liveness only —
// never ownership).
func (r *Record) Alive() bool {
	if r == nil || r.PID <= 0 {
		return false
	}
	return processAlive(r.PID)
}

// VerifyLive proves current ownership: the pid must be alive AND its live
// command line must contain every recorded argument token. A reused PID
// running unrelated code cannot match.
func (r *Record) VerifyLive() bool {
	if !r.Alive() {
		return false
	}
	return verifyLiveRecord(r)
}

// StopOptions tunes Stop. Health is an optional additional identity proof
// (e.g. a /health instance echo) evaluated after argv verification.
type StopOptions struct {
	// Timeout bounds the graceful SIGTERM wait (default 3s).
	Timeout time.Duration
	// Health receives the loaded record; returning false aborts the stop
	// (fail closed). Use for endpoints that echo the instance id.
	Health func(Record) bool
}

// Stop stops the verified child: load record (nil ⇒ nothing to stop),
// verify live identity, run optional health proof, SIGTERM the session group,
// wait bounded for the whole group, SIGKILL, then remove the record. Returns
// false — without signalling anything — whenever ownership cannot be proven.
//
// The wait is on the group, not on the recorded pid. A child that spawned a
// helper of its own (a proxy child running a provider CLI, a hub child running
// a pool) can exit while its grandchild is still flushing logs into the data
// directory, and a stop that returned then made "the proxy is gone" a promise
// no caller could rely on — callers, tests and the reap path all treated the
// return as final.
func Stop(pidPath string, opts StopOptions) bool {
	rec := Load(pidPath)
	if rec == nil {
		return false
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 3 * time.Second
	}
	if !rec.VerifyLive() {
		// Stale record: the child is gone or the PID was recycled. Clean the
		// file but never signal an unverified process group.
		_ = os.Remove(pidPath)
		return false
	}
	if opts.Health != nil && !opts.Health(*rec) {
		return false
	}
	// The child is its own session leader: negative pid signals the group so
	// helpers it spawned die with it.
	_ = terminateRecord(rec, false)
	if !waitForGroupGone(rec, opts.Timeout) {
		_ = terminateRecord(rec, true)
		// A SIGKILL cannot be ignored, so this wait is short: it only covers the
		// kernel reaping the group, and a group that somehow survives it must not
		// hold the caller for another full timeout.
		waitForGroupGone(rec, killGrace)
	}
	_ = os.Remove(pidPath)
	return true
}

// killGrace bounds the wait after SIGKILL.
const killGrace = 500 * time.Millisecond

// waitForGroupGone polls until no process of the child's group is left.
func waitForGroupGone(rec *Record, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if !groupAlive(rec) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}
