package repocheck

import (
	"bytes"
	"strings"
)

// Image asset magic bytes. Xcode asset catalogs and app icons are the only
// binary files a clean checkout legitimately tracks, and they are recognized by
// content, not by name: a Mach-O renamed to icon.png must still fail.
var (
	pngMagic  = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	icnsMagic = []byte("icns")
)

// indexBlob reports a tracked blob's size and its first eight bytes, read from
// the index rather than from disk so a working-tree edit cannot change the
// answer.
type indexBlob func(spec string) (size int64, head []byte)

// artifactFindings decides which tracked files are unverifiable blobs.
//
// tracked is every tracked path under the prefix and text is the subset git
// considers text; the difference is candidates. A candidate is excused only
// when it is empty (a legitimately tracked placeholder) or when its magic bytes
// prove it is a PNG or an ICNS.
//
// The parameters are plain values and one function so this decision can be
// tested without a repository: the parsing is what rots, the plumbing does not.
func artifactFindings(tracked, text []string, blob indexBlob) []Finding {
	isText := make(map[string]bool, len(text))
	for _, path := range uniqueSorted(text) {
		isText[path] = true
	}
	var findings []Finding
	for _, path := range uniqueSorted(tracked) {
		if isText[path] {
			continue
		}
		spec := ":" + path
		size, head := blob(spec)
		if size == 0 {
			continue
		}
		switch {
		case isImage(path, head, pngMagic), isImage(path, head, icnsMagic):
			continue
		case size < 0:
			findings = append(findings, fail(path+" could not be inspected",
				"git did not report its size, so this check cannot clear it"))
		default:
			findings = append(findings, fail(path+" is not text and not a verified image asset",
				"untrack it: git rm --cached "+path+" (the file stays on disk)",
				"only *.png and *.icns are exempt, and only when their magic bytes agree"))
		}
	}
	return findings
}

// isImage reports whether a candidate carries the expected image magic. The
// extension is required as well as the magic: a PNG blob named blob.bin is
// still an unexplained binary in a source tree.
func isImage(path string, head, magic []byte) bool {
	ext := strings.ToLower(path)
	okExtension := (bytes.Equal(magic, pngMagic) && strings.HasSuffix(ext, ".png")) ||
		(bytes.Equal(magic, icnsMagic) && strings.HasSuffix(ext, ".icns"))
	if !okExtension {
		return false
	}
	return len(head) >= len(magic) && bytes.Equal(head[:len(magic)], magic)
}
