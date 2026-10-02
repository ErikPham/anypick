package hub

import "os"

// readFileIfPresent reads a small state file, treating absence as empty.
func readFileIfPresent(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return raw, nil
}
