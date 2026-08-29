// Package slug ports src/utils/slug.ts.
package slug

import (
	"fmt"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// MaxSlugLen mirrors MAX_SLUG_LEN.
const MaxSlugLen = 64

// Normalize ports normalizeSlug: human-friendly input is auto-slugified
// ("Kiro Key - 1k" → "kiro-key-1k"). Result is safe for filesystem paths:
// lowercase, [a-z0-9._-], no spaces.
func Normalize(raw, kind string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("%s is required.", kind)
	}

	// Unicode NFKD + strip combining marks so "café" → "cafe".
	name := norm.NFKD.String(strings.TrimSpace(raw))
	var b strings.Builder
	for _, r := range name {
		if r >= 0x0300 && r <= 0x036f {
			continue
		}
		b.WriteRune(r)
	}
	name = strings.ToLower(b.String())

	// Path separators and parent refs must never survive.
	name = replaceRuns(name, "/\\", "-")
	name = collapseDots(name)

	// Everything that is not a safe slug char → hyphen.
	name = mapUnsafe(name)

	// Collapse runs of separators (dots collapse to single dots).
	name = replaceRuns(name, "-", "-")
	name = replaceRuns(name, "_", "_")
	name = replaceRuns(name, ".", ".")

	// Trim leading/trailing separators.
	name = strings.Trim(name, "-._")

	if name == "" {
		return "", fmt.Errorf("%s %q has no usable characters. Use letters or digits.", kind, raw)
	}
	if len(name) > MaxSlugLen {
		name = strings.TrimRight(name[:MaxSlugLen], "-._")
	}
	if name == "" || !isAlnum(name[0]) {
		return "", fmt.Errorf("%s %q must start with a letter or digit after normalization.", kind, raw)
	}
	return name, nil
}

// AccountName ports normalizeAccountName.
func AccountName(raw string) (string, error) { return Normalize(raw, "Account name") }

func isAlnum(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= '0' && b <= '9'
}

func replaceRuns(s, set, with string) string {
	var b strings.Builder
	inRun := false
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(set, s[i]) >= 0 {
			if !inRun {
				b.WriteString(with)
				inRun = true
			}
			continue
		}
		b.WriteByte(s[i])
		inRun = false
	}
	return b.String()
}

// collapseDots implements `.replace(/\.\.+/g, '.')` — two or more dots
// collapse to one, but a single dot survives untouched.
func collapseDots(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '.' && i+1 < len(s) && s[i+1] == '.' {
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func mapUnsafe(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
			b.WriteByte(c)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}
