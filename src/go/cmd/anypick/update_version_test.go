package main

import "testing"

// TestCompareVersions pins the ordering the update check relies on. The old
// implementation compared versions as strings, so a 1.0.1 build was told to
// "upgrade" to the older 1.0.0 release, and a user who obeyed downgraded the
// CLI below the schema that had already migrated the data directory.
func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.1", "1.0.0", 1},  // the reported defect: newer vs older release
		{"1.0.0", "1.0.1", -1}, // and its mirror
		{"1.10.0", "1.9.0", 1}, // numeric, not lexicographic ("10" < "9" as strings)
		{"1.0", "1.0.0", -1},   // shorter version sorts before its padded equal
		{"1.1.0-beta", "1.1.0", -1},
		{"2.0.0", "1.9.9", 1},
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
		// Orderings must antisymmetrise, or every branch above is suspect.
		if got := compareVersions(c.b, c.a); got != -c.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", c.b, c.a, got, -c.want)
		}
	}
}

// TestUpdateDoesNotDowngrade is the behavioural half: with the latest release
// older than the running build, the command must not print an upgrade line.
func TestUpdateDoesNotDowngrade(t *testing.T) {
	if got := compareVersions("1.0.0", "1.0.1"); got >= 0 {
		t.Fatalf("latest 1.0.0 vs running 1.0.1 compared %d; the update command would advertise a downgrade", got)
	}
}
