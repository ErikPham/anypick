package ownership

import "testing"

func TestSelectStopJob(t *testing.T) {
	const pid = 42
	const start uint64 = 99
	stamped := jobName(pid, start)
	legacy := legacyJobName(pid)
	cases := []struct {
		name                 string
		pid                  int
		start                uint64
		processMatchesRecord bool
		stampedHoldsPID      bool
		legacyHoldsPID       bool
		wantName             string
		wantOK               bool
	}{
		{name: "pid", pid: 0, start: start, processMatchesRecord: true, stampedHoldsPID: true, wantOK: false},
		{name: "start", pid: pid, start: 0, processMatchesRecord: true, legacyHoldsPID: true, wantOK: false},
		{name: "identity", pid: pid, start: start, stampedHoldsPID: true, legacyHoldsPID: true, wantOK: false},
		{name: "stamped", pid: pid, start: start, processMatchesRecord: true, stampedHoldsPID: true, legacyHoldsPID: true, wantName: stamped, wantOK: true},
		{name: "legacy", pid: pid, start: start, processMatchesRecord: true, legacyHoldsPID: true, wantName: legacy, wantOK: true},
		{name: "neither", pid: pid, start: start, processMatchesRecord: true, wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := selectStopJob(tc.pid, tc.start, tc.processMatchesRecord, tc.stampedHoldsPID, tc.legacyHoldsPID)
			if ok != tc.wantOK || got != tc.wantName {
				t.Fatalf("selectStopJob = %q %v, want %q %v", got, ok, tc.wantName, tc.wantOK)
			}
		})
	}
}
