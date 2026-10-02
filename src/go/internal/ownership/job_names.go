package ownership

import "strconv"

// jobName is the job object created for a child this build spawned. The start
// stamp is part of the name so a recycled PID cannot be assigned into the
// previous owner's job (ADR 0003).
func jobName(pid int, start uint64) string {
	return "AnyPick-PID-" + strconv.Itoa(pid) + "-" + strconv.FormatUint(start, 10)
}

// legacyJobName is the PID-only job a build from before the start stamp
// created. Stop may open it only after the live process matches the record.
func legacyJobName(pid int) string {
	return "AnyPick-PID-" + strconv.Itoa(pid)
}

// selectStopJob chooses which job name owns a stop. A PID-only name is not
// ownership by itself: the caller must already have matched the live process
// to the record, and the chosen job's process list must contain that PID.
// The stamped name wins when both jobs contain it. No match means do not
// signal a job.
func selectStopJob(pid int, start uint64, processMatchesRecord, stampedHoldsPID, legacyHoldsPID bool) (string, bool) {
	if pid <= 0 || start == 0 || !processMatchesRecord {
		return "", false
	}
	if stampedHoldsPID {
		return jobName(pid, start), true
	}
	if legacyHoldsPID {
		return legacyJobName(pid), true
	}
	return "", false
}
