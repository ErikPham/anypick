//go:build !windows && !darwin

package ownership

import "testing"

func TestParseProcStartStampUsesField22NotVsize(t *testing.T) {
	// Synthetic /proc/pid/stat: comm may contain spaces and ')'. After the
	// final ')', field 22 (starttime) is 4242 and field 23 (vsize) is 999999.
	// Using the vsize index made VerifyLive fail once the child grew.
	line := "1234 (any pick) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 4242 999999 21 22 23 24 25 26 27 28 29 30 31 32 33 34 35 36 37 38 39 40 41 42 43"
	got, err := parseProcStartStamp(1234, line)
	if err != nil {
		t.Fatal(err)
	}
	if got != 4242 {
		t.Fatalf("starttime=%d want 4242 (vsize is 999999)", got)
	}
}
