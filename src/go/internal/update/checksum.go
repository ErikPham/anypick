package update

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
)

// ParseSHA256SUMS parses a standard GNU coreutils SHA256SUMS file:
// "<hex>  <filename>" or "<hex> *<filename>" (binary mode). Lines that are
// blank or start with '#' are ignored. Returns a map of name → lowercase hex.
//
// Fail-closed: duplicate filenames and names containing "..", "/" or "\" are
// rejected. No basename trimming.
func ParseSHA256SUMS(r io.Reader) (map[string]string, error) {
	out := make(map[string]string)
	sc := bufio.NewScanner(r)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Standard form: 64 hex chars, two spaces (or space+*), then name.
		if len(line) < 66 {
			return nil, fmt.Errorf("SHA256SUMS line %d too short", lineNo)
		}
		hexPart := line[:64]
		sep := line[64:66]
		name := strings.TrimSpace(line[66:])
		if _, err := hex.DecodeString(hexPart); err != nil {
			return nil, fmt.Errorf("SHA256SUMS line %d: bad hex: %w", lineNo, err)
		}
		if sep != "  " && sep != " *" {
			// Tolerate a single space after the hex (some generators).
			if line[64] == ' ' {
				name = strings.TrimSpace(line[65:])
			} else {
				return nil, fmt.Errorf("SHA256SUMS line %d: expected two-space or space-star separator", lineNo)
			}
		}
		if name == "" {
			return nil, fmt.Errorf("SHA256SUMS line %d: empty filename", lineNo)
		}
		if err := ValidateAssetName(name); err != nil {
			return nil, fmt.Errorf("SHA256SUMS line %d: %w", lineNo, err)
		}
		lower := strings.ToLower(hexPart)
		if _, exists := out[name]; exists {
			return nil, fmt.Errorf("SHA256SUMS line %d: duplicate entry for %q", lineNo, name)
		}
		out[name] = lower
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ApplyChecksums fills Asset.SHA256 from a parsed SUMS map (matched by Name).
// It does not error; call RequireChecksums for fail-closed coverage.
func ApplyChecksums(assets []Asset, sums map[string]string) []Asset {
	out := make([]Asset, len(assets))
	copy(out, assets)
	for i := range out {
		if h, ok := sums[out[i].Name]; ok {
			out[i].SHA256 = h
		}
	}
	return out
}

// RequireChecksums fails closed when any installable asset lacks a non-empty
// SHA-256. SHA256SUMS.txt itself is exempt. Empty hash strings fail.
func RequireChecksums(assets []Asset) error {
	for _, a := range assets {
		if a.Name == "SHA256SUMS.txt" {
			continue
		}
		if strings.TrimSpace(a.SHA256) == "" {
			return fmt.Errorf("missing SHA-256 for asset %q", a.Name)
		}
	}
	return nil
}

// VerifyFileSHA256 hashes path and compares it to wantHex (case-insensitive).
// A mismatch returns an error whose text is suitable for StateBadChecksum.
// On mismatch the caller must leave the install untouched (delete staging only).
func VerifyFileSHA256(path, wantHex string) error {
	wantHex = strings.ToLower(strings.TrimSpace(wantHex))
	if wantHex == "" {
		return fmt.Errorf("missing expected SHA-256")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != wantHex {
		return fmt.Errorf("checksum mismatch: got %s want %s", got, wantHex)
	}
	return nil
}

// HashBytes returns the lowercase hex SHA-256 of b.
func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
