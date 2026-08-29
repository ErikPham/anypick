package provider

import (
	"bufio"
	"strings"
)

// splitLines splits dotenv-style content on \n or \r\n without keeping
// terminators.
func splitLines(raw string) []string {
	sc := bufio.NewScanner(strings.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var lines []string
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	// Preserve a trailing empty line only when content ends with a newline —
	// Scanner already drops it, matching the TS split/join behaviour closely
	// enough for round-tripping.
	return lines
}

func joinLines(lines []string) string { return strings.Join(lines, "\n") }

// envLineKey extracts the assignment name from `KEY=value` / `export KEY=`
// lines, mirroring the TS regex /^(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)=/.
func envLineKey(line string) (string, bool) {
	body := strings.TrimSpace(line)
	if rest, ok := strings.CutPrefix(body, "export "); ok {
		body = strings.TrimSpace(rest)
	}
	idx := strings.IndexByte(body, '=')
	if idx <= 0 {
		return "", false
	}
	name := body[:idx]
	if name == "" {
		return "", false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		valid := c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(i > 0 && c >= '0' && c <= '9')
		if !valid {
			return "", false
		}
	}
	return name, true
}
