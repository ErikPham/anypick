package repocheck

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Every third-party action is pinned to a full commit SHA with the version it
// stands for in a trailing comment. A tag can be moved under us; a short or
// mistyped SHA fails only when the workflow runs, which for release.yml means
// on release day. That is how four pins in release.yml broke unnoticed until a
// release rehearsal (commit f60c96f). Whether each SHA exists upstream needs
// the network, so CI resolves them; this check keeps the shape honest offline.
var (
	usesLine    = regexp.MustCompile(`^\s*(?:-\s*)?uses:\s*([^\s#]+)\s*(#.*)?$`)
	pinnedRef   = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+(?:/[^@\s]+)?@[0-9a-f]{40}$`)
	versionNote = regexp.MustCompile(`^#\s*v\d`)
)

// CheckActions reports every workflow `uses:` that is not pinned to a full SHA
// with a version comment.
func CheckActions(root string) []Section {
	section := Section{Name: "workflow actions are pinned to a full SHA with a version comment"}
	files, _ := filepath.Glob(filepath.Join(root, ".github", "workflows", "*.y*ml"))
	sort.Strings(files)
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			section.Findings = append(section.Findings, Finding{Detail: err.Error()})
			continue
		}
		rel, _ := filepath.Rel(root, file)
		rel = filepath.ToSlash(rel)
		for i, line := range strings.Split(string(raw), "\n") {
			m := usesLine.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			ref, note := m[1], strings.TrimSpace(m[2])
			if strings.HasPrefix(ref, "./") || strings.HasPrefix(ref, "docker://") {
				continue
			}
			where := fmt.Sprintf("%s:%d", rel, i+1)
			switch {
			case !pinnedRef.MatchString(ref):
				section.Findings = append(section.Findings, Finding{
					Detail: fmt.Sprintf("%s uses %s, which is not pinned to a 40-character commit SHA", where, ref),
					Advice: []string{"pin it: uses: owner/repo@<full sha> # vX.Y.Z"},
				})
			case !versionNote.MatchString(note):
				section.Findings = append(section.Findings, Finding{
					Detail: fmt.Sprintf("%s pins %s without a `# vX.Y.Z` comment", where, ref),
					Advice: []string{"name the release the SHA stands for, so a reader can audit and bump it"},
				})
			default:
				section.Checked = append(section.Checked, where+" "+ref)
			}
		}
	}
	if len(section.Checked) == 0 && len(section.Findings) == 0 {
		section.Findings = append(section.Findings, Finding{
			Detail: "no `uses:` line found under .github/workflows",
			Advice: []string{"a check over zero actions proves nothing; did the workflows move?"},
		})
	}
	return []Section{section}
}
