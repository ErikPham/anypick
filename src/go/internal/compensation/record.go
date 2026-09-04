// Package compensation parses and replays the "src=>dest" records that
// runtime.PrepareClientConfigRecovery writes before an adapter may touch disk.
//
// There is exactly one producer and, before this package existed, two consumers
// that disagreed about which way the record reads. One of them
// (activation.recover) copied the live file over its own backup, destroying the
// only copy on every crash-recovery pass (ADR 0004). One parser for the format
// is the only shape where that class of bug cannot recur, and
// record_test.go fails the build if a second one appears.
package compensation

import (
	"os"
	"path/filepath"
	"strings"
)

// separator is the record delimiter. It is deliberately not exported: callers
// parse through Parse and never look at the wire format.
const separator = "=>"

// deleteMarker is the Src value the producer emits when the live file did not
// exist before activation, so recovery must remove what activation created.
const deleteMarker = "delete"

// Record is one compensation entry.
//
// Direction is load-bearing and reads opposite to intuition. The producer emits
// src+"=>"+dest where src is the LIVE client config and dest is the backup it
// just took. Replaying a record therefore reads the backup and writes the live
// file.
//
// The delete marker inverts the meaning of the fields: for it, dest is the live
// path and there is no backup at all.
type Record struct {
	Src  string
	Dest string
}

// Parse reads one record. ok is false when the string carries no separator or
// either side is empty — then the caller decides what a bare path means, which
// is why this returns a bool rather than an error.
func Parse(s string) (Record, bool) {
	i := strings.Index(s, separator)
	if i < 0 {
		return Record{}, false
	}
	rec := Record{
		Src:  strings.TrimSpace(s[:i]),
		Dest: strings.TrimSpace(s[i+len(separator):]),
	}
	if rec.Src == "" || rec.Dest == "" {
		return Record{}, false
	}
	return rec, true
}

// IsDelete reports whether this record marks a file activation created and
// recovery must remove.
func (r Record) IsDelete() bool { return r.Src == deleteMarker }

// Apply replays the record: read the backup, restore the live file.
//
// It is not atomic — it restores one file, and a caller replaying several has
// no all-or-nothing guarantee. That is the same guarantee the journal gives:
// every step is journalled, so an interrupted recovery is picked up again on the
// next bootstrap.
func (r Record) Apply() error {
	if r.IsDelete() {
		return os.RemoveAll(r.Dest)
	}
	data, err := os.ReadFile(r.Dest)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(r.Src), 0o700); err != nil {
		return err
	}
	return os.WriteFile(r.Src, data, 0o600)
}
