package provider

import (
	"os"
	"path/filepath"
	"strings"
)

// discoverKiroProxyCommand mirrors resolveKiroProxyCommand: classification and
// the launcher share ONE resolver so the gate and the spawn can never disagree
// on a binary name or env override.
func discoverKiroProxyCommand() string {
	if bin := os.Getenv("KIROLINK_BIN"); bin != "" {
		if isExecutableFile(bin) {
			return bin
		}
	}
	// KIROLINK_JS points at a node entry; a node runtime plus that file counts
	// as an available bridge for classification purposes.
	if js := os.Getenv("KIROLINK_JS"); js != "" && isExecutableFile(js) {
		return js
	}

	names := []string{"kirolink", "kiro-proxy", "kiro-link"}
	dirs := PATHDirectories()
	for _, dir := range dirs {
		for _, name := range names {
			candidate := filepath.Join(dir, name)
			if isExecutableFile(candidate) {
				return candidate
			}
		}
	}
	return ""
}

func PATHDirectories() []string {
	var out []string
	if path := os.Getenv("PATH"); path != "" {
		out = append(out, strings.Split(path, string(os.PathListSeparator))...)
	}
	home, _ := os.UserHomeDir()
	out = append(out,
		filepath.Join(home, ".local", "bin"),
		"/opt/homebrew/bin",
		"/usr/local/bin",
	)
	return out
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode().Perm()&0o111 != 0
}
