package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/outof0/anypick/go/internal/app"
)

func serveHealthFixture(t *testing.T, instanceID string) (*httptest.Server, int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":true,"pid":%d,"instanceId":%q}`, os.Getpid(), instanceID)
	}))
	t.Cleanup(srv.Close)
	parsed, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, rawPort, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil {
		t.Fatal(err)
	}
	return srv, port
}

func TestServeRecordOwnershipRequiresHealthProof(t *testing.T) {
	_, port := serveHealthFixture(t, "instance-a")
	record := &serveRecord{PID: os.Getpid(), Port: port, InstanceID: "instance-a"}
	if !serveRecordOwned(record) {
		t.Fatal("matching pid and instance should prove ownership")
	}

	wrongInstance := *record
	wrongInstance.InstanceID = "instance-b"
	if serveRecordOwned(&wrongInstance) {
		t.Fatal("mismatched instance must not prove ownership")
	}

	wrongPID := *record
	wrongPID.PID++
	if serveRecordOwned(&wrongPID) {
		t.Fatal("mismatched pid must not prove ownership")
	}
}

func TestReadServeRecordRejectsLegacyPIDOnlyState(t *testing.T) {
	a := &app.App{Root: t.TempDir()}
	if err := writeJSONFile(servePIDPath(a), map[string]int{
		"pid":  os.Getpid(),
		"port": 1234,
	}, 0o600); err != nil {
		t.Fatal(err)
	}
	if record := readServeRecord(a); record != nil {
		t.Fatalf("legacy record without ownership token was accepted: %+v", record)
	}
}

func TestWaitForServeReadyIgnoresStalePortState(t *testing.T) {
	a := &app.App{Root: t.TempDir()}
	if err := writeServeRecord(a, &serveRecord{
		PID:        os.Getpid(),
		Port:       1,
		InstanceID: "stale-instance",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := waitForServeReady(a, os.Getpid(), 100*time.Millisecond); err == nil {
		t.Fatal("stale pid/port state must not count as ready")
	}
}

func TestRemoveServeRecordDoesNotDeleteAnotherInstance(t *testing.T) {
	a := &app.App{Root: t.TempDir()}
	record := &serveRecord{PID: os.Getpid(), Port: 1234, InstanceID: "current"}
	if err := writeServeRecord(a, record); err != nil {
		t.Fatal(err)
	}
	removeServeRecord(a, "old")
	if got := readServeRecord(a); got == nil || got.InstanceID != "current" {
		t.Fatalf("another instance's record was removed: %+v", got)
	}
	removeServeRecord(a, "current")
	if _, err := os.Stat(servePIDPath(a)); !os.IsNotExist(err) {
		t.Fatalf("matching record survived removal: %v", err)
	}
}

func TestWriteServeRecordReportsFilesystemFailure(t *testing.T) {
	a := &app.App{Root: t.TempDir()}
	if err := os.Mkdir(servePIDPath(a), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeServeRecord(a, &serveRecord{
		PID: os.Getpid(), Port: 1234, InstanceID: "instance",
	}); err == nil {
		t.Fatal("want pid record write error")
	}
}

func TestSpawnDetachedChildStartsBeforeClosingLog(t *testing.T) {
	trueBin, err := exec.LookPath("true")
	if err != nil {
		t.Skipf("true executable unavailable: %v", err)
	}
	logPath := t.TempDir() + "/serve.log"
	cmd, err := spawnDetachedChild(trueBin, nil, logPath)
	if err != nil {
		t.Fatalf("spawnDetachedChild: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("log file: %v", err)
	}
}

func TestSpawnDetachedChildReportsLogOpenFailure(t *testing.T) {
	trueBin, err := exec.LookPath("true")
	if err != nil {
		t.Skipf("true executable unavailable: %v", err)
	}
	blocker := t.TempDir() + "/blocker"
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := spawnDetachedChild(trueBin, nil, blocker+"/serve.log"); err == nil {
		t.Fatal("want log open failure")
	}
}
