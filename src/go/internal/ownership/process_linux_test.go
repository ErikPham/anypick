//go:build !windows && !darwin

package ownership

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseProcStartStampUsesField22NotVsize(t *testing.T) {
	// Synthetic /proc/pid/stat: comm may contain spaces and ')'. After the
	// final ')' come fields 3..52, so fields[0] is state (field 3) and
	// starttime (field 22) is fields[19]. That is 18 filler values for fields
	// 4 through 21, then starttime 4242, then vsize (field 23) 999999.
	// Using the vsize index made VerifyLive fail once the child grew.
	line := "1234 (any pick) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 4242 999999 21 22 23 24 25 26 27 28 29 30 31 32 33 34 35 36 37 38 39 40 41 42 43"
	got, err := parseProcStartStamp(1234, line)
	if err != nil {
		t.Fatal(err)
	}
	if got != 4242 {
		t.Fatalf("starttime=%d want 4242 (vsize is 999999)", got)
	}
}

// The first version of this fixture carried 19 filler values before the
// starttime, which put 4242 at field 23 and made the test assert against its
// own data. It only ever ran on Linux, so it failed there for as long as the
// file existed. This asserts the offset from a real /proc shape instead of a
// hand-counted one.
func TestParseProcStartStampReadsField22OfARealStatLine(t *testing.T) {
	// The first two fields are pid and comm; everything after is positional.
	var b strings.Builder
	b.WriteString("4321 ")
	b.WriteString("(comm with ) paren and spaces) ")
	b.WriteString("R") // state, field 3
	for field := 4; field <= 21; field++ {
		fmt.Fprintf(&b, " %d", field)
	}
	b.WriteString(" 987654") // field 22, starttime
	b.WriteString(" 111111") // field 23, vsize

	got, err := parseProcStartStamp(4321, b.String())
	if err != nil {
		t.Fatal(err)
	}
	if got != 987654 {
		t.Fatalf("starttime=%d want 987654: the index is not reading field 22", got)
	}
}
